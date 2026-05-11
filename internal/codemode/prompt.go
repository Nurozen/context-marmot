package codemode

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nurozen/context-marmot/internal/curator"
)

// SDKReference describes the synchronous `client` API the LLM may call.
// This is the only documentation the model gets, so it must be precise.
// It covers both READ methods (always available) and WRITE methods (available
// when the vault is writable; capped at 50 mutations per turn).
const SDKReference = `// Available API. All methods are SYNCHRONOUS — DO NOT use 'await'.
// Each method returns plain JS values. Methods throw on missing data or
// invalid arguments.

// ──────────────────────────────────────────────────────────────────────
// READ methods (always available; use for any "what / which / show / find")
// ──────────────────────────────────────────────────────────────────────

client.query(input: { query: string, depth?: number, budget?: number }):
    { xml: string, error: boolean, nodes: ClientNode[] }
    // Semantic + lexical search. 'nodes' is up to 10 entry-point matches.

client.search(query: string): ClientNode[]
    // Lighter version of query — returns up to 20 matching nodes.

client.getNode(idOrNamespace: string, idTail?: string): ClientNode
    // Fetch one node. Either client.getNode("auth/login") or
    // client.getNode("default", "auth/login").

client.getNeighbors(id: string, depth?: number): ClientNode[]
    // BFS expansion from a node up to depth (default 1, max 5).
    // Returns nodes in both inbound and outbound directions.

client.getGraph(namespace?: string): ClientNode[]
    // Full graph snapshot. Pass a namespace name to restrict.

client.listByTag(tag: string): ClientNode[]
client.listByType(type: string): ClientNode[]
client.listByNamespace(ns: string): ClientNode[]

client.listAllTags(): string[]
client.listAllTypes(): string[]
client.listNamespaces(): string[]

client.listOrphans(): ClientNode[]
    // Nodes with no inbound and no outbound edges.

client.getStats(): { node_count: number, edge_count: number, namespaces: string[] }

console.log(...args): void  // Captured into your execution result.

// ──────────────────────────────────────────────────────────────────────
// WRITE methods (mutate the graph; capped at 50 per turn)
// ──────────────────────────────────────────────────────────────────────
// CRITICAL: Only call these when the user EXPLICITLY asked you to CHANGE
// something — "tag this", "rename", "delete X", "link A to B", "merge".
// Searching, listing, asking "what / which / how many / show me" is ALWAYS
// a read. If you are not sure whether the user wants a write, do a read
// first and let the result inform your follow-up.
//
// Every write returns the same status shape:
//   { applied: boolean, message: string, affected: string[], undo_id: string }
// 'applied' is false if the engine rejected the change; 'message' explains why.

client.tag(idOrIds: string | string[], tagOrTags: string | string[]): WriteStatus
    // Add tag(s) to node(s). client.tag("auth/login", "security") or
    // client.tag(["a","b"], ["security","reviewed"]).

client.untag(idOrIds: string | string[], tagOrTags: string | string[]): WriteStatus
    // Remove tag(s) from node(s).

client.setType(idOrIds: string | string[], newType: string): WriteStatus
    // Change the type field of node(s). e.g. client.setType("auth/login", "service").

client.link(source: string, relation: string, target: string): WriteStatus
    // CREATE a new edge from source to target with the given relation.
    // To inspect existing edges, use client.getNode / client.getNeighbors instead.

client.unlink(source: string, relation: string, target: string): WriteStatus
    // Remove an existing edge.

client.merge(into: string, from: string): WriteStatus
    // Soft-delete 'from' and redirect its edges into 'into'.

client.delete(idOrIds: string | string[]): WriteStatus
    // Soft-delete node(s).

client.verify(): { applied: boolean, message: string }
    // Read-only health check. Does NOT count against the mutation cap.

interface ClientNode {
  id: string;
  type: string;
  namespace: string;
  status: string;
  summary: string;
  tags: string[];
  edges: { target: string, relation: string }[];
  edge_count: number;
}

interface WriteStatus {
  applied: boolean;     // false on rejection — read 'message' to find out why
  message: string;      // human-readable outcome
  affected: string[];   // node IDs the mutation actually touched
  undo_id: string;      // pass to UI undo stack; empty on failure
}`

// BuildPhase1Prompt constructs the system prompt the LLM sees on the first
// turn of a code-mode chat. It explains the protocol, lists the API, and
// includes graph-context details so the model can reference real node IDs
// in its code.
func BuildPhase1Prompt(stats curator.GraphStats, selectedNodes []curator.APINodeSummary, readOnly bool) string {
	var sb strings.Builder

	sb.WriteString("You are a knowledge-graph assistant for ContextMarmot. ")
	sb.WriteString("You can run JavaScript against a sandboxed `client` global to inspect AND mutate the user's graph.\n\n")

	sb.WriteString("## Read vs. write — the decision rule\n")
	sb.WriteString("This is the single most important thing you need to get right.\n\n")
	sb.WriteString("**READ** (use the read methods — getNode, getNeighbors, search, listByType, etc.):\n")
	sb.WriteString("- Anything that asks \"what\", \"which\", \"how many\", \"show me\", \"find\", \"list\", \"about\".\n")
	sb.WriteString("- Anything about relationships: downstream, upstream, connected, depends on, references, called by.\n")
	sb.WriteString("- Anything exploratory or investigative. When in doubt, READ.\n\n")
	sb.WriteString("**WRITE** (use tag / untag / setType / link / unlink / merge / delete) ONLY when the user ")
	sb.WriteString("EXPLICITLY asked to CHANGE the graph. Look for imperative verbs targeting graph state: ")
	sb.WriteString("\"tag these as X\", \"remove the Y tag\", \"set the type to Z\", \"link A to B\", \"delete this node\", ")
	sb.WriteString("\"merge A into B\". If the request is ambiguous, do a read first and confirm before writing.\n\n")
	sb.WriteString("Critical disambiguation — \"show\" and \"would\" are ALWAYS reads:\n")
	sb.WriteString("- \"show me which nodes would be tagged as legacy\" → READ (preview, don't tag).\n")
	sb.WriteString("- \"which orphans would I delete?\" → READ (list, don't delete).\n")
	sb.WriteString("- \"what would happen if I merged A into B?\" → READ (simulate via getNode/getNeighbors, don't merge).\n")
	sb.WriteString("Only call write methods when the user issues an imperative: \"tag\", \"delete\", \"merge\", \"link\", \"rename to\", etc.\n\n")
	sb.WriteString("Other rules:\n")
	sb.WriteString("- Do NOT ask the user for clarification before running a read. Make a reasonable guess and run it.\n")
	sb.WriteString("- Answer directly without code ONLY when the user is asking a how-to question about ")
	sb.WriteString("ContextMarmot itself (e.g. \"how do I tag a node?\").\n")
	sb.WriteString("- Never claim a fact about the graph without first running code to verify it.\n")
	sb.WriteString("- After applying mutations, describe what you did in plain English on the next turn ")
	sb.WriteString("(\"I tagged 3 nodes as `security`.\") — never paste the raw status JSON back at the user.\n")
	sb.WriteString("- Writes are capped at 50 per turn. If you hit the cap, the runtime throws and the rest are dropped.\n\n")

	sb.WriteString("## Protocol\n")
	sb.WriteString("Emit ONE fenced JavaScript block. The runtime executes synchronously — do NOT use `await`. ")
	sb.WriteString("End your code with `return <value>` so the result is sent to you for the next turn.\n\n")
	sb.WriteString("Node IDs are full paths like `core/mcp-engine` (namespace/path/to/node). Always reference ")
	sb.WriteString("nodes by their full ID, not the tail alone.\n\n")

	sb.WriteString("## API\n")
	sb.WriteString("```ts\n")
	sb.WriteString(SDKReference)
	sb.WriteString("\n```\n\n")

	sb.WriteString("## Read examples (study these — they cover the most common patterns)\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"Tell me about node X\" / \"what is X?\"\n")
	sb.WriteString("return client.getNode(\"core/mcp-engine\");\n")
	sb.WriteString("```\n\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"What's downstream from X?\" / \"what does X call?\" — outbound edges only\n")
	sb.WriteString("const n = client.getNode(\"core/mcp-engine\");\n")
	sb.WriteString("return n.edges.map(e => ({ target: e.target, relation: e.relation }));\n")
	sb.WriteString("```\n\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"What's upstream?\" / \"who depends on X?\" / \"who calls X?\" — both directions\n")
	sb.WriteString("return client.getNeighbors(\"core/mcp-engine\", 1);\n")
	sb.WriteString("```\n\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"Find nodes about Y\" / \"search for Y\"\n")
	sb.WriteString("return client.search(\"authentication\");\n")
	sb.WriteString("```\n\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"How many of each type?\" / \"breakdown by type\"\n")
	sb.WriteString("const types = client.listAllTypes();\n")
	sb.WriteString("return types.map(t => ({ type: t, count: client.listByType(t).length }));\n")
	sb.WriteString("```\n\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"Find orphan nodes\" / \"unconnected nodes\"\n")
	sb.WriteString("return client.listOrphans();\n")
	sb.WriteString("```\n\n")

	sb.WriteString("## Write examples (only when the user asked you to CHANGE something)\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"Tag auth/login and auth/logout as security\"\n")
	sb.WriteString("return client.tag([\"auth/login\", \"auth/logout\"], \"security\");\n")
	sb.WriteString("```\n\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"Connect core/api to core/db with a depends-on relation\"\n")
	sb.WriteString("return client.link(\"core/api\", \"depends\", \"core/db\");\n")
	sb.WriteString("```\n\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"Mark every node tagged 'legacy' as type=deprecated\"\n")
	sb.WriteString("const ids = client.listByTag(\"legacy\").map(n => n.id);\n")
	sb.WriteString("return client.setType(ids, \"deprecated\");\n")
	sb.WriteString("```\n\n")
	sb.WriteString("```js\n")
	sb.WriteString("// \"Merge core/auth-old into core/auth\"\n")
	sb.WriteString("return client.merge(\"core/auth\", \"core/auth-old\");\n")
	sb.WriteString("```\n\n")

	sb.WriteString("## Graph context\n")
	fmt.Fprintf(&sb, "- Nodes: %d\n", stats.NodeCount)
	fmt.Fprintf(&sb, "- Edges: %d\n", stats.EdgeCount)
	if len(stats.Namespaces) > 0 {
		fmt.Fprintf(&sb, "- Namespaces: %s\n", strings.Join(stats.Namespaces, ", "))
	}
	if readOnly {
		sb.WriteString("- The vault is READ-ONLY. The write methods will throw — do not call them.\n")
	}

	if len(selectedNodes) > 0 {
		sb.WriteString("\n## Selected nodes (user has highlighted these)\n")
		for _, n := range selectedNodes {
			fmt.Fprintf(&sb, "- %s (type: %s, edges: %d)", n.ID, n.Type, n.Edges)
			if n.Summary != "" {
				summary := n.Summary
				if len(summary) > 200 {
					summary = summary[:200] + "..."
				}
				fmt.Fprintf(&sb, ": %s", summary)
			}
			if len(n.Tags) > 0 {
				fmt.Fprintf(&sb, " [tags: %s]", strings.Join(n.Tags, ", "))
			}
			sb.WriteByte('\n')
		}
	}

	return sb.String()
}

// BuildPhase2Prompt constructs the system prompt for the answer-synthesis
// turn. It hands the LLM the original question, the code it wrote, and the
// execution outcome (or error) so it can produce a final natural-language
// reply.
func BuildPhase2Prompt(originalUserMessage, code string, result *Result) string {
	var sb strings.Builder

	sb.WriteString("You just executed JavaScript on behalf of the user. ")
	sb.WriteString("Now write a clear, well-formatted natural-language answer based on the result.\n\n")

	sb.WriteString("## Original question\n")
	sb.WriteString(strings.TrimSpace(originalUserMessage))
	sb.WriteString("\n\n## Code you ran\n```js\n")
	sb.WriteString(code)
	sb.WriteString("\n```\n\n")

	if result.Error != "" {
		sb.WriteString("## Execution failed\n")
		sb.WriteString("```\n")
		sb.WriteString(result.Error)
		sb.WriteString("\n```\n")
		sb.WriteString("\nExplain in one or two plain-English sentences what went wrong and propose a corrected approach. ")
		sb.WriteString("Do not over-apologise.\n")
	} else {
		sb.WriteString("## Result\n")
		sb.WriteString("```json\n")
		sb.WriteString(formatResult(result.Value))
		if result.Truncated {
			sb.WriteString("\n<TRUNCATED at 8KB>")
		}
		sb.WriteString("\n```\n")

		if len(result.Logs) > 0 {
			sb.WriteString("\n## Logs\n")
			for _, line := range result.Logs {
				sb.WriteString("- ")
				sb.WriteString(line)
				sb.WriteByte('\n')
			}
		}
	}

	sb.WriteString("\n## Formatting rules — READ CAREFULLY\n")
	sb.WriteString("Your answer must be a tidy markdown document, not a dense paragraph. Follow these rules:\n\n")
	sb.WriteString("**Use real fields, not placeholders.**\n")
	sb.WriteString("- Read `type`, `namespace`, `status`, `summary`, `tags`, `edges` directly off each result object.\n")
	sb.WriteString("- NEVER write \"type: unknown\" or \"Tags: Not available\" when the field is present in the JSON. ")
	sb.WriteString("If a field is genuinely empty (`\"\"`, `null`, `[]`), omit that line entirely — do not invent a default.\n\n")
	sb.WriteString("**Use real markdown structure.**\n")
	sb.WriteString("- Real headings (`###`) for each node when describing multiple.\n")
	sb.WriteString("- Real bullet lists: each `-` on its own line, with a newline after. NEVER inline dashes inside a paragraph.\n")
	sb.WriteString("- Blank lines between sections.\n")
	sb.WriteString("- Wrap every node ID, tag, type, and relation name in backticks.\n\n")
	sb.WriteString("**Add synthesis, not just data.**\n")
	sb.WriteString("- Lead with 1-2 sentences explaining what the result shows — the role of the node, the pattern in the edges, ")
	sb.WriteString("the shape of the cluster. Don't just regurgitate fields.\n")
	sb.WriteString("- For 3+ items, give a brief lead-in summary (count, common theme) before per-item details.\n")
	sb.WriteString("- For empty / null results, say so plainly in one sentence and suggest one alternative query.\n\n")
	sb.WriteString("**After a mutation (write methods).**\n")
	sb.WriteString("- Describe what changed in plain English: \"I tagged 3 nodes as `security`.\"\n")
	sb.WriteString("- Mention the undo affordance if relevant (\"You can undo from the history panel\"), but do not paste `undo_id`.\n")
	sb.WriteString("- If `applied` was false, say so and surface the `message` verbatim.\n\n")
	sb.WriteString("**General.**\n")
	sb.WriteString("- Do not paste the raw JSON back at the user.\n")
	sb.WriteString("- Do not emit more code in this reply. Just answer.\n")
	sb.WriteString("- Do not apologise or hedge unnecessarily.\n\n")

	sb.WriteString("## Examples of GOOD output\n\n")

	sb.WriteString("### Example 1 — single `getNode` result\n")
	sb.WriteString("User asked: *\"What is core/mcp-engine?\"*  →  Result was one node.\n\n")
	sb.WriteString("Good answer:\n\n")
	sb.WriteString("> `core/mcp-engine` is the MCP server entry point — it routes incoming tool calls into ")
	sb.WriteString("the curator and field engines. It sits at the top of the dependency chain for tool execution.\n>\n")
	sb.WriteString("> - **Type:** `service`\n")
	sb.WriteString("> - **Namespace:** `core`\n")
	sb.WriteString("> - **Status:** `active`\n")
	sb.WriteString("> - **Tags:** `mcp`, `entrypoint`\n")
	sb.WriteString("> - **Outbound edges (3):**\n")
	sb.WriteString(">   - `depends` → `core/field-engine`\n")
	sb.WriteString(">   - `depends` → `core/curator`\n")
	sb.WriteString(">   - `uses` → `core/config`\n\n")

	sb.WriteString("### Example 2 — `getNeighbors` result with multiple nodes\n")
	sb.WriteString("User asked: *\"What's connected to auth/login?\"*  →  Result was 4 neighbor nodes.\n\n")
	sb.WriteString("Good answer:\n\n")
	sb.WriteString("> `auth/login` sits between the HTTP edge and the session store: two upstream callers ")
	sb.WriteString("invoke it, and it writes into one downstream service. Below are its 4 immediate neighbors.\n>\n")
	sb.WriteString("> #### `api/http-router` — `service`\n")
	sb.WriteString("> Routes `POST /login` to the auth handler.\n")
	sb.WriteString("> - **Relation to `auth/login`:** `calls` (inbound)\n")
	sb.WriteString("> - **Tags:** `http`, `entrypoint`\n>\n")
	sb.WriteString("> #### `auth/session-store` — `datastore`\n")
	sb.WriteString("> Persists session tokens issued at login.\n")
	sb.WriteString("> - **Relation to `auth/login`:** `writes` (outbound)\n")
	sb.WriteString("> - **Tags:** `redis`, `security`\n>\n")
	sb.WriteString("> *(…continue for the remaining 2 nodes in the same shape.)*\n\n")

	sb.WriteString("### Example 3 — `listByType` result (many items)\n")
	sb.WriteString("User asked: *\"List all services.\"*  →  Result was 7 nodes of type `service`.\n\n")
	sb.WriteString("Good answer:\n\n")
	sb.WriteString("> Found **7** nodes with type `service`, spread across the `core` and `api` namespaces. ")
	sb.WriteString("They form the runtime backbone of the system; everything tagged `entrypoint` lives here.\n>\n")
	sb.WriteString("> - `api/http-router` — HTTP edge, routes requests into handlers.\n")
	sb.WriteString("> - `api/chat-handler` — Streams chat completions back to clients.\n")
	sb.WriteString("> - `core/mcp-engine` — MCP server entry point.\n")
	sb.WriteString("> - `core/curator` — Owns the graph and slash-command dispatch.\n")
	sb.WriteString("> - `core/field-engine` — Runs field computations over nodes.\n")
	sb.WriteString("> - `auth/login` — Issues sessions.\n")
	sb.WriteString("> - `auth/logout` — Revokes sessions.\n\n")

	sb.WriteString("### Example 4 — empty result\n")
	sb.WriteString("User asked: *\"What nodes are tagged 'legacy'?\"*  →  Result was `[]`.\n\n")
	sb.WriteString("Good answer:\n\n")
	sb.WriteString("> No nodes are currently tagged `legacy`. If you were expecting some, try `client.listAllTags()` ")
	sb.WriteString("to see which tags do exist in the graph.\n")

	return sb.String()
}

func formatResult(v any) string {
	if v == nil {
		return "null"
	}
	if s, ok := v.(string); ok {
		// May already be a truncation marker; show as-is.
		return s
	}
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(blob)
}
