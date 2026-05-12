# Spec: Code-Mode Curator Chat

Give the Graph Curator's natural-language chat the ability to actually
search and traverse the graph. Inspired by Cloudflare's Code Mode pattern:
the LLM emits JavaScript code, the server executes it in a sandbox against
the engine, results feed back into a second LLM call that produces the
final natural-language answer.

## Why code-mode (vs. tool calling)

We already ship a TypeScript SDK that mirrors every MCP tool plus the
graph read endpoints. A code-mode setup reuses that surface area directly:
the LLM writes idiomatic JS using a `client` global that mirrors the SDK.
This is more expressive than discrete tool calls (the model can compose
filters, aggregations, multi-hop traversals in a single call) and avoids
having to maintain a parallel tool-call schema for OpenAI and Anthropic.

## Flow

```
User question
       │
       ▼
[LLM call 1] system prompt = code-mode template + client API + selected nodes
       │
       │  emits assistant message with optional ```js block
       ▼
[Server parses code]
       │
       │  if code present: run in goja sandbox with `client` proxy
       │  else:            treat content as direct answer (skip phase 2)
       ▼
[Execution result]
       │
       │  serialize result + logs as JSON
       ▼
[LLM call 2] system prompt = "synthesize answer from execution result"
             messages = [original user, prev assistant code, tool result]
       │
       ▼
[Final assistant message returned to UI]
```

The UI shows the executed code (collapsed by default) plus the final
text answer. If the user asks a curation question that doesn't need
graph access ("how do I tag nodes?"), the LLM may skip the code step
and answer directly — phase 2 is conditional on whether phase 1 emitted
code.

## Sandbox

JavaScript runtime: **`github.com/dop251/goja`** (pure Go, ES5.1+).

Hard constraints enforced by the executor:

- **Timeout**: 5 seconds wall clock per execution (configurable).
- **No I/O**: no filesystem, no network, no `process`, no `require`.
- **No eval / Function constructor**: stripped from the runtime.
- **Memory cap**: cap interpreter heap at ~50 MB (best-effort via goja
  interrupt + a per-second size check).
- **Single execution**: each chat turn gets a fresh runtime; no state
  persists between turns.
- **No async / await**: all `client.*` methods are synchronous Go calls
  exposed to JS as blocking. Spec'd this way to dodge goja's incomplete
  promise support.

## Client API exposed to JS

All methods are SYNCHRONOUS (no `await` in user code). Read-only methods
work in any vault; write-side methods reject when `engine.ReadOnly == true`.

```ts
// Search and retrieval
client.query(input: { query: string, depth?: number, budget?: number }): QueryResult
client.search(query: string): SearchResult[]
client.getNode(namespace: string, id: string): MarmotNode
client.getNeighbors(namespace: string, id: string, depth?: number): MarmotNode[]
client.getGraph(namespace?: string): { nodes: MarmotNode[], edges: MarmotEdge[] }

// Filtering / listing
client.listByTag(tag: string): MarmotNode[]
client.listByType(type: string): MarmotNode[]
client.listByNamespace(ns: string): MarmotNode[]
client.listAllTags(): string[]
client.listAllTypes(): string[]
client.listNamespaces(): string[]

// Curation / health
client.listOrphans(): MarmotNode[]
client.getStats(): { nodeCount: number, edgeCount: number, namespaces: string[] }

// Console
console.log(...args): void  // captured into Result.logs
```

Write methods (`write`, `tag`, `delete`) are intentionally NOT exposed in
v1. The model can recommend slash commands (`/tag …`) but cannot mutate
the graph through code-mode. This keeps the trust surface small.

## ChatResponse extension

`internal/curator/types.go` `ChatResponse` gains optional fields:

```go
type ChatResponse struct {
    Message  ChatMessage   `json:"message"`
    UndoID   string        `json:"undo_id,omitempty"`
    CodeRun  *CodeRunInfo  `json:"code_run,omitempty"`  // NEW
}

type CodeRunInfo struct {
    Code        string   `json:"code"`           // the JS the LLM emitted
    Result      any      `json:"result"`         // JSON-marshaled return value
    Logs        []string `json:"logs"`           // captured console.log
    Error       string   `json:"error,omitempty"` // runtime error if any
    DurationMS  int64    `json:"duration_ms"`
}
```

The frontend renders `CodeRun` as a collapsible block above the final
assistant message. If `Error` is non-empty, the block is auto-expanded
in red.

## System prompt (phase 1)

```
You are a knowledge graph curator with access to the user's ContextMarmot
graph through a JavaScript runtime.

To answer questions about the graph, write a single block of JavaScript
that uses the `client` API, then return a value. The runtime is
synchronous — DO NOT use `await`. The runtime executes once per turn;
you cannot maintain state.

Available API: <SDK SUMMARY HERE>

Examples:
  // Find nodes about authentication
  const result = client.query({ query: "authentication", depth: 2 });
  return result.nodes.slice(0, 10);

  // Count nodes by tag
  const tags = client.listAllTags();
  return tags.map(t => ({ tag: t, count: client.listByTag(t).length }));

  // Get neighbors of a specific node
  return client.getNeighbors("default", "auth/login", 1);

If the question can be answered without graph data (e.g. "how do I tag
nodes?"), respond directly without code. Otherwise, ALWAYS emit code in
a single ```js block and return a value.

GRAPH CONTEXT: <stats + selected nodes>
```

## System prompt (phase 2)

```
You just ran the following code on behalf of the user:

```js
<CODE>
```

It returned: <RESULT JSON, truncated to 8KB>
Logs: <captured console.log output>
<error message if any>

Original user question: <user message>

Now write a clear, concise natural-language answer. Reference specific
node IDs, tags, or counts from the result. Use markdown for emphasis.
If the result was empty or an error, explain what happened and suggest
what the user might try.
```

## Truncation rules

- Code-mode result JSON cap: 8 KB before sending to phase 2 LLM.
  Larger results truncated with `"<TRUNCATED at 8KB>"` footer.
- Code length cap: 4 KB. Larger emissions are an error returned to phase 2
  ("your previous code was too long; rewrite shorter").
- Logs cap: 50 entries, 1 KB each.

## Iteration

v1: single-shot (phase 1 → phase 2 → done). The model gets one code
execution per user turn.

Future: multi-step loop (phase 1 → exec → phase 1' → exec' → … → final),
capped at 3 hops. Out of scope for now.

## Failure modes

| Failure | Behavior |
|---|---|
| LLM emits no code, no answer | Ask again with stricter prompt; cap at 1 retry |
| Code parse error (malformed JS) | Phase 2 prompt includes the parse error; LLM apologizes |
| Code timeout (>5s) | Phase 2 prompt explains; LLM apologizes; CodeRunInfo.Error set |
| Code throws | Phase 2 prompt includes the throw; LLM explains/apologizes |
| LLM call 1 fails | Standard 500; no phase 2 |
| LLM call 2 fails | Return phase-1 message + CodeRunInfo so user sees the code at least |

## Read-only mode interaction

When the engine is read-only:
- Phase-1 system prompt explicitly says "this is a read-only documentation
  bundle; do not suggest writes".
- The code-mode `client` exposes only read methods (write methods aren't
  in the v1 API anyway, so no extra gating needed).

## Out of scope

- Streaming partial results to the UI (server returns final response only).
- Persistent execution state across turns.
- Write methods in code-mode.
- LLM-driven loop / multi-step reasoning.
- Cost-aware throttling (uses standard 1024-token max for both calls).

## Test plan

- Sandbox: timeout enforcement, no fs/net access, eval blocked.
- API: each `client.*` method round-trips correctly.
- Chat: question that should trigger code → code-run + final answer.
- Chat: question that doesn't need code → direct answer, no CodeRunInfo.
- Chat: malformed code → graceful error.
- Frontend: code block renders, collapses, expands, shows result/error.
- Manual end-to-end: ask "what nodes are about auth?" against the demo
  vault; verify the LLM uses `client.query`, executes, and produces a
  grounded answer.
