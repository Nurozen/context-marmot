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
func registerClient(rt *goja.Runtime, engine *mcpserver.Engine) error {
	if engine == nil {
		return fmt.Errorf("nil engine")
	}
	client := rt.NewObject()

	mustSet := func(name string, fn any) {
		if err := client.Set(name, fn); err != nil {
			panic(rt.NewGoError(fmt.Errorf("register %s: %w", name, err)))
		}
	}

	// query — semantic search via the existing MCP handler.
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
			// Convenience: also project entry node IDs from the active graph
			// using the same query so the LLM can drill in.
			"nodes": searchToClientNodes(engine, q, 10),
		})
	})

	// search — lightweight wrapper that returns just node IDs+summaries.
	mustSet("search", func(call goja.FunctionCall) goja.Value {
		q := call.Argument(0).String()
		if q == "" {
			panic(rt.NewTypeError("client.search: query is required"))
		}
		return rt.ToValue(searchToClientNodes(engine, q, 20))
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
	mustSet("getGraph", func(call goja.FunctionCall) goja.Value {
		ns := ""
		if len(call.Arguments) > 0 {
			ns = call.Argument(0).String()
		}
		g := engine.GetGraph()
		nodes := g.AllActiveNodes()
		out := make([]ClientNode, 0, len(nodes))
		for _, n := range nodes {
			if ns != "" && n.Namespace != ns {
				continue
			}
			out = append(out, toClientNode(g, n))
		}
		return rt.ToValue(out)
	})

	// listByTag.
	mustSet("listByTag", func(call goja.FunctionCall) goja.Value {
		tag := call.Argument(0).String()
		if tag == "" {
			panic(rt.NewTypeError("client.listByTag: tag is required"))
		}
		g := engine.GetGraph()
		out := make([]ClientNode, 0)
		for _, n := range g.AllActiveNodes() {
			if hasTag(n, tag) {
				out = append(out, toClientNode(g, n))
			}
		}
		return rt.ToValue(out)
	})

	// listByType.
	mustSet("listByType", func(call goja.FunctionCall) goja.Value {
		t := call.Argument(0).String()
		if t == "" {
			panic(rt.NewTypeError("client.listByType: type is required"))
		}
		g := engine.GetGraph()
		out := make([]ClientNode, 0)
		for _, n := range g.AllActiveNodes() {
			if n.Type == t {
				out = append(out, toClientNode(g, n))
			}
		}
		return rt.ToValue(out)
	})

	// listByNamespace.
	mustSet("listByNamespace", func(call goja.FunctionCall) goja.Value {
		ns := call.Argument(0).String()
		if ns == "" {
			panic(rt.NewTypeError("client.listByNamespace: namespace is required"))
		}
		g := engine.GetGraph()
		out := make([]ClientNode, 0)
		for _, n := range g.AllActiveNodes() {
			nodeNS := n.Namespace
			if nodeNS == "" {
				nodeNS = "default"
			}
			if nodeNS == ns {
				out = append(out, toClientNode(g, n))
			}
		}
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
		out := make([]ClientNode, 0)
		for _, n := range g.AllActiveNodes() {
			if len(g.GetEdges(n.ID, graph.Outbound)) == 0 &&
				len(g.GetEdges(n.ID, graph.Inbound)) == 0 {
				out = append(out, toClientNode(g, n))
			}
		}
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
			for _, e := range g.GetEdges(id, graph.Outbound) {
				if _, seen := visited[e.Target]; seen {
					continue
				}
				visited[e.Target] = struct{}{}
				if n, ok := g.GetNode(e.Target); ok {
					out = append(out, toClientNode(g, n))
				}
				next = append(next, e.Target)
			}
			for _, e := range g.GetEdges(id, graph.Inbound) {
				if _, seen := visited[e.Target]; seen {
					continue
				}
				visited[e.Target] = struct{}{}
				if n, ok := g.GetNode(e.Target); ok {
					out = append(out, toClientNode(g, n))
				}
				next = append(next, e.Target)
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}
	return out
}

// searchToClientNodes runs a query through HandleContextQuery and projects
// the entry nodes back into the JS shape. It uses the same path as MCP so
// it benefits from the lexical fallback when no embedder key is set.
func searchToClientNodes(engine *mcpserver.Engine, query string, limit int) []ClientNode {
	g := engine.GetGraph()
	// Reuse the same lightweight search as the existing /api/search handler:
	// we directly probe the embedding store. This avoids round-tripping XML.
	if engine.Embedder == nil || engine.EmbeddingStore == nil {
		return nil
	}
	vec, err := engine.Embedder.Embed(query)
	if err != nil {
		// Lexical fallback — call HandleContextQuery and try to extract IDs.
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "context_query", Arguments: map[string]any{"query": query}}}
		_, _ = engine.HandleContextQuery(context.Background(), req)
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
