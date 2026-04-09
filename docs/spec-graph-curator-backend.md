# Graph Curator Backend — Technical Spec

## 1. Chat API Endpoint

**`POST /api/chat`** — SSE streaming response.

```go
// ChatRequest is the JSON body for POST /api/chat.
type ChatRequest struct {
    Message      string          `json:"message"`
    History      []ChatMessage   `json:"history,omitempty"`
    SelectedNodes []string       `json:"selected_nodes,omitempty"`
    Namespace    string          `json:"namespace,omitempty"`
    SessionID    string          `json:"session_id"`
}

type ChatMessage struct {
    Role    string      `json:"role"`    // "user" | "assistant"
    Content string      `json:"content"`
    Actions []ChatAction `json:"actions,omitempty"`
}

type ChatAction struct {
    Type    string `json:"type"`    // "highlight" | "sdk_call" | "suggestion"
    Payload any    `json:"payload"`
}
```

**Response**: SSE stream (`text/event-stream`). Events:

| Event | Data |
|-------|------|
| `delta` | `{"text": "..."}` — incremental LLM token |
| `action` | `ChatAction` JSON — highlight nodes, executed SDK result |
| `done` | `{"undo_id": "..."}` — final event, includes undo reference if mutations occurred |
| `error` | `{"error": "..."}` |

Slash commands (`/verify`, `/tag foo`, etc.) skip SSE streaming entirely and return a single JSON `ChatMessage` response with `Content-Type: application/json`.

## 2. LLM Integration

**System prompt assembly** (built per-request in `internal/curator/prompt.go`):

1. Role preamble: "You are a knowledge graph curator for ContextMarmot."
2. Graph stats: node count, edge count, namespace list (from `Engine.GetGraph()`).
3. Selected node context: full `APINode` JSON for each `selected_nodes` ID.
4. Available tools: JSON schema array for `context_query`, `context_write`, `context_verify`, `context_delete`, `context_tag`.

**Tool execution loop** — uses the existing `llm.Provider` interface extended with a new `Chat` method:

```go
// ChatProvider extends Provider with multi-turn, tool-calling chat.
type ChatProvider interface {
    Provider
    ChatStream(ctx context.Context, req ChatStreamRequest) (<-chan ChatEvent, error)
}

type ChatStreamRequest struct {
    System   string        // assembled system prompt
    Messages []ChatMessage // conversation history
    Tools    []ToolDef     // SDK tool schemas
}

type ChatEvent struct {
    Type     string // "text" | "tool_call" | "done" | "error"
    Text     string
    ToolCall *ToolCallEvent
}

type ToolCallEvent struct {
    Name string
    Args map[string]any
}
```

When the LLM emits a `tool_call` event, the handler:
1. Records an undo snapshot (see section 5).
2. Dispatches to `handleSDKCall` logic (the existing `switch` on tool name).
3. Appends the tool result as a `tool` role message.
4. Resumes LLM generation with the updated context.

## 3. Structured Commands

Parsed in `internal/curator/commands.go` before any LLM call. If the message starts with `/`, route directly:

```go
type SlashCommand struct {
    Name string   // "tag", "type", "verify", "delete", "link", "merge"
    Args []string // positional arguments
}

func ParseCommand(msg string) (*SlashCommand, bool)
```

| Command | Execution |
|---------|-----------|
| `/tag <tag>` | Calls `HandleContextTag` for each `selected_nodes` |
| `/type <type>` | Updates node type via `NodeStore.SaveNode` |
| `/verify` | Calls `HandleContextVerify` with selected or all nodes |
| `/delete` | Calls `HandleContextDelete` for selected node |
| `/link <src> <rel> <tgt>` | Calls `HandleContextWrite` adding the edge |
| `/merge <A> <B>` | Redirects B's inbound edges to A, copies missing tags/context, deletes B |

`/merge` is a compound operation: load both nodes, union edges, save A, delete B. The undo entry captures both original nodes.

## 4. Curation Suggestions Engine

New package: `internal/curator/suggestions.go`.

```go
type Suggestion struct {
    ID       string `json:"id"`       // deterministic hash for dedup
    Type     string `json:"type"`     // "orphan" | "missing_summary" | "duplicate" | "stale" | "untyped" | "disconnected"
    Severity string `json:"severity"` // "error" | "warning" | "info"
    NodeIDs  []string `json:"node_ids"`
    Message  string `json:"message"`
    Fix      FixAction `json:"fix"`
}

type FixAction struct {
    Command string         `json:"command"` // slash command to fix, e.g. "/delete orphan-node"
    Auto    bool           `json:"auto"`    // true if safe to auto-apply
    Args    map[string]any `json:"args,omitempty"`
}

func Analyze(g *graph.Graph, es *embedding.Store, opts AnalyzeOpts) []Suggestion
```

**`GET /api/curator/suggestions?ns=<namespace>&limit=20`** — returns paginated `[]Suggestion`.

Detection logic:
- **Orphans**: nodes with 0 in+out edges.
- **Missing summaries**: `node.Summary == ""`.
- **Duplicates**: for each node, query embedding store for top-1 neighbor; if similarity > 0.95 and IDs differ, flag. Skipped when `Embedder == nil`.
- **Stale**: delegates to `verify.VerifyStaleness`.
- **Untyped**: `node.Type == ""`.
- **Disconnected subgraphs**: BFS/union-find on edge set; components with < 3 nodes flagged as "info".

Results sorted by severity (error > warning > info), then by node count descending.

## 5. Mutation Undo System

```go
type UndoEntry struct {
    ID        string       `json:"id"`
    SessionID string       `json:"session_id"`
    Timestamp time.Time    `json:"timestamp"`
    Snapshots []NodeSnapshot `json:"snapshots"` // pre-mutation state
    Deleted   []string     `json:"deleted"`     // node IDs that were created (undo = delete)
}

type NodeSnapshot struct {
    Node *node.Node `json:"node"`
    Existed bool    `json:"existed"` // false if node was created by the mutation
}
```

Storage: `sync.Map` keyed by `SessionID` holding a `[]UndoEntry` stack (max 50 entries, LIFO).

**`POST /api/chat/undo`** — pops the top entry for the session:
- For each snapshot where `Existed == true`: restore by calling `NodeStore.SaveNode` + `Graph.UpsertNode`.
- For each snapshot where `Existed == false`: delete the node (it was created by the mutation).
- For each ID in `Deleted`: these were deleted nodes — restore from snapshot.
- Calls `Server.NotifyChange()` to push SSE update.

```json
// Request
{ "session_id": "tab-abc123" }
// Response
{ "restored": 2, "undo_id": "..." }
```

## 6. Graceful Degradation

| Condition | Behavior |
|-----------|----------|
| No LLM key (`ChatProvider == nil`) | Slash commands work. Free-text returns `{"error": "Configure an LLM provider for natural language curation"}` with HTTP 200 + `type: "error"` action. |
| No embeddings (`Embedder == nil`) | Suggestions skip duplicate detection. Semantic search unavailable — commands that need it return a clear message. |
| Large graphs (>1000 nodes) | `Analyze` accepts `AnalyzeOpts.NodeIDs []string` to scope to visible/filtered nodes. Pagination via `limit`+`offset` query params. Stale checks skipped unless explicitly requested (`check_stale=true`). |

## Component Boundaries

```
internal/curator/
  ├── commands.go      // ParseCommand, ExecuteCommand
  ├── prompt.go        // BuildSystemPrompt, BuildToolDefs
  ├── suggestions.go   // Analyze, Suggestion, FixAction
  └── undo.go          // UndoStack, UndoEntry, NodeSnapshot

internal/llm/
  └── chat.go          // ChatProvider interface, ChatStreamRequest, ChatEvent

internal/api/
  └── chat_handlers.go // handleChat, handleChatUndo, handleSuggestions
```

Routes added to `Server.registerRoutes()`:
```go
s.mux.HandleFunc("POST /api/chat", s.handleChat)
s.mux.HandleFunc("POST /api/chat/undo", s.handleChatUndo)
s.mux.HandleFunc("GET /api/curator/suggestions", s.handleSuggestions)
```
