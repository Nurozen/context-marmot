package codemode

import (
	"context"
	"fmt"
	"sort"

	"github.com/dop251/goja"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/graph"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/node"
)

// MaxListItems caps the number of nodes any list method returns to JS, so a
// huge graph can't blow up Go-side memory before result truncation kicks in
// at the executor layer. When this limit is hit the result includes a
// trailing sentinel marker.
const MaxListItems = 500

// truncatedMarkerNode signals that the list method capped its output. The
// LLM sees this as a sentinel string in the array and adjusts its answer.
var truncatedMarkerNode = ClientNode{ID: "<TRUNCATED at 500 items>", Status: "truncated"}

// ClientNode is the JS-friendly projection of a graph node returned by the
// `client` API. Edges are resolved into the node-id-plus-relation pairs the
// LLM most often wants to look at.
type ClientNode struct {
	ID        string       `json:"id"`
	Type      string       `json:"type"`
	Namespace string       `json:"namespace"`
	Status    string       `json:"status"`
	Summary   string       `json:"summary"`
	Tags      []string     `json:"tags"`
	Edges     []ClientEdge `json:"edges"`
	EdgeCount int          `json:"edge_count"`
}

// ClientEdge mirrors a single outbound edge from a node.
type ClientEdge struct {
	Target   string `json:"target"`
	Relation string `json:"relation"`
}

// ClientStats summarises the graph for getStats().
type ClientStats struct {
	NodeCount  int      `json:"node_count"`
	EdgeCount  int      `json:"edge_count"`
	Namespaces []string `json:"namespaces"`
}

// registerClient wires the `client` global. Each method is synchronous and
// either returns a Go value (auto-converted by goja) or panics with a JS
// exception via runtime.NewTypeError / NewGoError.
//
// The scope's WriteContext, if non-nil, unlocks write methods (tag, untag,
// type, link, unlink, merge, delete). Each successful write is recorded in
// scope.mutations with an undo stack entry.
func registerClient(rt *goja.Runtime, scope *runScope) error {
	engine := scope.engine
	if engine == nil {
		return fmt.Errorf("nil engine")
	}
	client := rt.NewObject()

	mustSet := func(name string, fn any) {
		if err := client.Set(name, fn); err != nil {
			panic(rt.NewGoError(fmt.Errorf("register %s: %w", name, err)))
		}
	}

	// query — semantic search via the existing MCP handler. The XML payload
	// the MCP handler returns is a token-budgeted blob meant for an agent's
	// context window; for code-mode we additionally project the entry nodes
	// into the structured ClientNode shape so the LLM can index by ID.
	mustSet("query", func(call goja.FunctionCall) goja.Value {
		input := exportObject(rt, call.Argument(0))
		q, _ := input["query"].(string)
		if q == "" {
			panic(rt.NewTypeError("client.query: 'query' string is required"))
		}
		args := map[string]any{"query": q}
		if v, ok := input["depth"]; ok {
			args["depth"] = toInt(v)
		}
		if v, ok := input["budget"]; ok {
			args["budget"] = toInt(v)
		}
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "context_query", Arguments: args}}
		result, err := engine.HandleContextQuery(context.Background(), req)
		if err != nil {
			panic(rt.NewGoError(err))
		}
		return rt.ToValue(map[string]any{
			"xml":   extractText(result),
			"error": isErr(result),
			"nodes": searchEntryNodes(engine, q, 10),
		})
	})

	// search — lightweight wrapper that returns just node IDs+summaries.
	mustSet("search", func(call goja.FunctionCall) goja.Value {
		q := call.Argument(0).String()
		if q == "" {
			panic(rt.NewTypeError("client.search: query is required"))
		}
		return rt.ToValue(searchEntryNodes(engine, q, 20))
	})

	// getNode — fetch a single node by ID. Accepts either ("ns", "id") or
	// ("ns/id") for convenience.
	mustSet("getNode", func(call goja.FunctionCall) goja.Value {
		var id string
		switch len(call.Arguments) {
		case 0:
			panic(rt.NewTypeError("client.getNode: id required"))
		case 1:
			id = call.Argument(0).String()
		default:
			ns := call.Argument(0).String()
			rest := call.Argument(1).String()
			if ns != "" {
				id = ns + "/" + rest
			} else {
				id = rest
			}
		}
		n, ok := engine.ResolveNodeID(id)
		if !ok {
			panic(rt.NewGoError(fmt.Errorf("node %q not found", id)))
		}
		return rt.ToValue(toClientNode(engine.GetGraph(), n))
	})

	// getNeighbors — BFS from a node up to depth.
	mustSet("getNeighbors", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			panic(rt.NewTypeError("client.getNeighbors: id required"))
		}
		// Accept (id, depth?) or (ns, id, depth?).
		var id string
		var depth int
		switch len(call.Arguments) {
		case 1:
			id = call.Argument(0).String()
			depth = 1
		case 2:
			// Could be (id, depth) or (ns, id).
			second := call.Argument(1)
			if second.ExportType() != nil && second.ExportType().Kind().String() == "string" {
				id = call.Argument(0).String() + "/" + second.String()
				depth = 1
			} else {
				id = call.Argument(0).String()
				depth = toInt(second.Export())
			}
		default:
			id = call.Argument(0).String() + "/" + call.Argument(1).String()
			depth = toInt(call.Argument(2).Export())
		}
		if depth <= 0 {
			depth = 1
		}
		if depth > 5 {
			depth = 5
		}
		out := bfsNeighbors(engine.GetGraph(), id, depth)
		return rt.ToValue(out)
	})

	// getGraph — full graph snapshot, optionally filtered to a namespace.
	// Capped at MaxListItems entries to bound memory.
	mustSet("getGraph", func(call goja.FunctionCall) goja.Value {
		ns := ""
		if len(call.Arguments) > 0 {
			ns = call.Argument(0).String()
		}
		g := engine.GetGraph()
		out := collectCapped(g, func(n *node.Node) bool {
			return ns == "" || n.Namespace == ns
		})
		return rt.ToValue(out)
	})

	// listByTag.
	mustSet("listByTag", func(call goja.FunctionCall) goja.Value {
		tag := call.Argument(0).String()
		if tag == "" {
			panic(rt.NewTypeError("client.listByTag: tag is required"))
		}
		out := collectCapped(engine.GetGraph(), func(n *node.Node) bool { return hasTag(n, tag) })
		return rt.ToValue(out)
	})

	// listByType.
	mustSet("listByType", func(call goja.FunctionCall) goja.Value {
		t := call.Argument(0).String()
		if t == "" {
			panic(rt.NewTypeError("client.listByType: type is required"))
		}
		out := collectCapped(engine.GetGraph(), func(n *node.Node) bool { return n.Type == t })
		return rt.ToValue(out)
	})

	// listByNamespace.
	mustSet("listByNamespace", func(call goja.FunctionCall) goja.Value {
		ns := call.Argument(0).String()
		if ns == "" {
			panic(rt.NewTypeError("client.listByNamespace: namespace is required"))
		}
		out := collectCapped(engine.GetGraph(), func(n *node.Node) bool {
			nodeNS := n.Namespace
			if nodeNS == "" {
				nodeNS = "default"
			}
			return nodeNS == ns
		})
		return rt.ToValue(out)
	})

	// listAllTags.
	mustSet("listAllTags", func(call goja.FunctionCall) goja.Value {
		g := engine.GetGraph()
		seen := make(map[string]struct{})
		for _, n := range g.AllActiveNodes() {
			for _, t := range n.Tags {
				seen[t] = struct{}{}
			}
		}
		out := make([]string, 0, len(seen))
		for t := range seen {
			out = append(out, t)
		}
		sort.Strings(out)
		return rt.ToValue(out)
	})

	// listAllTypes.
	mustSet("listAllTypes", func(call goja.FunctionCall) goja.Value {
		g := engine.GetGraph()
		seen := make(map[string]struct{})
		for _, n := range g.AllActiveNodes() {
			if n.Type != "" {
				seen[n.Type] = struct{}{}
			}
		}
		out := make([]string, 0, len(seen))
		for t := range seen {
			out = append(out, t)
		}
		sort.Strings(out)
		return rt.ToValue(out)
	})

	// listNamespaces.
	mustSet("listNamespaces", func(call goja.FunctionCall) goja.Value {
		return rt.ToValue(allNamespaces(engine.GetGraph()))
	})

	// listOrphans — nodes with no inbound and no outbound edges.
	mustSet("listOrphans", func(call goja.FunctionCall) goja.Value {
		g := engine.GetGraph()
		out := collectCapped(g, func(n *node.Node) bool {
			return len(g.GetEdges(n.ID, graph.Outbound)) == 0 &&
				len(g.GetEdges(n.ID, graph.Inbound)) == 0
		})
		return rt.ToValue(out)
	})

	// getStats.
	mustSet("getStats", func(call goja.FunctionCall) goja.Value {
		g := engine.GetGraph()
		nodes := g.AllActiveNodes()
		edges := 0
		for _, n := range nodes {
			edges += len(g.GetEdges(n.ID, graph.Outbound))
		}
		return rt.ToValue(ClientStats{
			NodeCount:  len(nodes),
			EdgeCount:  edges,
			Namespaces: allNamespaces(g),
		})
	})

	// Mutation methods (tag, untag, setType, link, unlink, merge, delete,
	// verify). Registered only when the executor was given a WriteContext.
	if err := registerWrites(rt, client, scope); err != nil {
		return err
	}

	return rt.GlobalObject().Set("client", client)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func toClientNode(g *graph.Graph, n *node.Node) ClientNode {
	if n == nil {
		return ClientNode{}
	}
	out := ClientNode{
		ID:        n.ID,
		Type:      n.Type,
		Namespace: n.Namespace,
		Status:    n.Status,
		Summary:   n.Summary,
		Tags:      append([]string{}, n.Tags...),
	}
	if g != nil {
		edges := g.GetEdges(n.ID, graph.Outbound)
		out.EdgeCount = len(edges)
		for _, e := range edges {
			out.Edges = append(out.Edges, ClientEdge{Target: e.Target, Relation: string(e.Relation)})
		}
	}
	return out
}

func hasTag(n *node.Node, tag string) bool {
	for _, t := range n.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func allNamespaces(g *graph.Graph) []string {
	seen := make(map[string]struct{})
	for _, n := range g.AllActiveNodes() {
		ns := n.Namespace
		if ns == "" {
			ns = "default"
		}
		seen[ns] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func bfsNeighbors(g *graph.Graph, startID string, depth int) []ClientNode {
	if g == nil {
		return nil
	}
	visited := map[string]struct{}{startID: {}}
	frontier := []string{startID}
	out := make([]ClientNode, 0)
	for d := 0; d < depth; d++ {
		next := make([]string, 0)
		for _, id := range frontier {
			for _, dir := range []graph.Direction{graph.Outbound, graph.Inbound} {
				for _, e := range g.GetEdges(id, dir) {
					if _, seen := visited[e.Target]; seen {
						continue
					}
					visited[e.Target] = struct{}{}
					if len(out) >= MaxListItems {
						out = append(out, truncatedMarkerNode)
						return out
					}
					if n, ok := g.GetNode(e.Target); ok {
						out = append(out, toClientNode(g, n))
					}
					next = append(next, e.Target)
				}
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}
	return out
}

// searchEntryNodes returns up to `limit` nodes that match `query` by
// projecting the embedding store's top-K hits back into ClientNode shape.
// When the embedder is unusable (no API key, mock with no matches), returns
// an empty slice — the LLM gets the empty array and can pick another tool.
func searchEntryNodes(engine *mcpserver.Engine, query string, limit int) []ClientNode {
	if engine == nil || engine.Embedder == nil || engine.EmbeddingStore == nil {
		return nil
	}
	g := engine.GetGraph()
	vec, err := engine.Embedder.Embed(query)
	if err != nil {
		return nil
	}
	results, err := engine.EmbeddingStore.SearchActive(vec, limit, engine.Embedder.Model())
	if err != nil || len(results) == 0 {
		return nil
	}
	out := make([]ClientNode, 0, len(results))
	for _, r := range results {
		if n, ok := g.GetNode(r.NodeID); ok {
			out = append(out, toClientNode(g, n))
		}
	}
	return out
}

// collectCapped iterates the graph's active nodes, filters by `keep`, and
// returns at most MaxListItems entries. When the cap is hit, a sentinel
// node is appended so the LLM can detect truncation.
func collectCapped(g *graph.Graph, keep func(*node.Node) bool) []ClientNode {
	if g == nil {
		return nil
	}
	out := make([]ClientNode, 0)
	for _, n := range g.AllActiveNodes() {
		if !keep(n) {
			continue
		}
		if len(out) >= MaxListItems {
			out = append(out, truncatedMarkerNode)
			return out
		}
		out = append(out, toClientNode(g, n))
	}
	return out
}

func extractText(result *mcp.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}
	if tc, ok := result.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func isErr(result *mcp.CallToolResult) bool {
	if result == nil {
		return true
	}
	return result.IsError
}

func exportObject(_ *goja.Runtime, v goja.Value) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return map[string]any{}
	}
	exp := v.Export()
	if m, ok := exp.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case float32:
		return int(x)
	}
	return 0
}
