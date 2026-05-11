package codemode

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nurozen/context-marmot/internal/curator"
)

// SDKReference describes the synchronous `client` API the LLM may call.
// This is the only documentation the model gets, so it must be precise.
const SDKReference = `// Available API (all methods are SYNCHRONOUS — DO NOT use 'await').
// Each method returns plain JS values; throw on missing data.

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

interface ClientNode {
  id: string;
  type: string;
  namespace: string;
  status: string;
  summary: string;
  tags: string[];
  edges: { target: string, relation: string }[];
  edge_count: number;
}`

// BuildPhase1Prompt constructs the system prompt the LLM sees on the first
// turn of a code-mode chat. It explains the protocol, lists the API, and
// includes graph-context details so the model can reference real node IDs
// in its code.
func BuildPhase1Prompt(stats curator.GraphStats, selectedNodes []curator.APINodeSummary, readOnly bool) string {
	var sb strings.Builder

	sb.WriteString("You are a knowledge-graph assistant for ContextMarmot. ")
	sb.WriteString("You can run JavaScript against a sandboxed `client` global to inspect the user's graph.\n\n")

	sb.WriteString("## Decision rule\n")
	sb.WriteString("- If the user asks ANY question that touches the graph — a node, a tag, a type, a count, ")
	sb.WriteString("\"what\", \"which\", \"how many\", \"show me\", \"find\", \"list\", \"downstream\", \"upstream\", ")
	sb.WriteString("\"connected\", \"depends on\", \"references\", \"called by\", \"about\" — write code. ")
	sb.WriteString("Do NOT ask the user for clarification first. Make a reasonable guess at what they want, ")
	sb.WriteString("run the code, and let the result inform your follow-up question if any.\n")
	sb.WriteString("- Answer directly without code ONLY when the user is asking a how-to question about ")
	sb.WriteString("ContextMarmot itself (e.g. \"how do I tag a node?\").\n")
	sb.WriteString("- Never claim a fact about the graph without first running code to verify it.\n")
	sb.WriteString("- Never tell the user to run a slash command in order to LOOK SOMETHING UP. Slash commands ")
	sb.WriteString("mutate the graph; they are not query tools. Reads always go through code.\n\n")

	sb.WriteString("## Protocol\n")
	sb.WriteString("Emit ONE fenced JavaScript block. The runtime executes synchronously — do NOT use `await`. ")
	sb.WriteString("End your code with `return <value>` so the result is sent to you for the next turn.\n\n")
	sb.WriteString("Node IDs are full paths like `core/mcp-engine` (namespace/path/to/node). Always reference ")
	sb.WriteString("nodes by their full ID, not the tail alone.\n\n")

	sb.WriteString("## API\n")
	sb.WriteString("```ts\n")
	sb.WriteString(SDKReference)
	sb.WriteString("\n```\n\n")

	sb.WriteString("## Examples (study these — they cover the most common patterns)\n")
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

	sb.WriteString("## Graph context\n")
	fmt.Fprintf(&sb, "- Nodes: %d\n", stats.NodeCount)
	fmt.Fprintf(&sb, "- Edges: %d\n", stats.EdgeCount)
	if len(stats.Namespaces) > 0 {
		fmt.Fprintf(&sb, "- Namespaces: %s\n", strings.Join(stats.Namespaces, ", "))
	}
	if readOnly {
		sb.WriteString("- The vault is READ-ONLY. Do not suggest writes.\n")
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

	sb.WriteString("\n## Slash commands — these MUTATE the graph; never use them to look anything up\n")
	sb.WriteString("Only suggest these when the user explicitly asked to CHANGE something. They are write\n")
	sb.WriteString("operations the user runs in their input box — you cannot execute them yourself.\n")
	sb.WriteString("- `/tag <name>` — add a tag to the currently selected nodes\n")
	sb.WriteString("- `/untag <name>` — remove a tag from selected nodes\n")
	sb.WriteString("- `/type <type>` — change the type field of selected nodes\n")
	sb.WriteString("- `/link <A> <relation> <B>` — CREATE a new edge from A to B (this writes; it does NOT read existing edges)\n")
	sb.WriteString("- `/unlink <A> <relation> <B>` — remove an edge\n")
	sb.WriteString("- `/merge <A> <B>` — soft-delete B and redirect its edges to A\n")
	sb.WriteString("- `/delete` — soft-delete the selected node(s)\n")
	sb.WriteString("- `/verify` — run a health check (read-only diagnostic)\n")
	sb.WriteString("\nIf the user wants to SEE edges, neighbors, or relationships, use code (client.getNode, ")
	sb.WriteString("client.getNeighbors, etc.) — never suggest /link or /unlink as a way to inspect.\n")

	return sb.String()
}

// BuildPhase2Prompt constructs the system prompt for the answer-synthesis
// turn. It hands the LLM the original question, the code it wrote, and the
// execution outcome (or error) so it can produce a final natural-language
// reply.
func BuildPhase2Prompt(originalUserMessage, code string, result *Result) string {
	var sb strings.Builder

	sb.WriteString("You just executed JavaScript on behalf of the user. ")
	sb.WriteString("Now write a clear, concise natural-language answer based on the result.\n\n")

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
		sb.WriteString("\nApologise briefly, explain what went wrong in plain English, and suggest a corrected approach.\n")
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

	sb.WriteString("\n## How to answer\n")
	sb.WriteString("- Reference specific node IDs, tags, types, or counts from the result.\n")
	sb.WriteString("- Use markdown for emphasis. Wrap node IDs in backticks: `auth/login`.\n")
	sb.WriteString("- Be concise. Do not paste the raw JSON back unless the user asked for it.\n")
	sb.WriteString("- If the result was empty, say so and suggest a different angle.\n")
	sb.WriteString("- Do NOT emit more code in this reply. Just answer.\n")

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
