package codemode

import (
	"context"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// Read-method tests. These exercise registerClient's method bodies through the
// goja runtime with the shared testRig fixture (auth/login -> db/users edge,
// plus auth/logout). Each test runs a snippet through Execute (read-only).
// ---------------------------------------------------------------------------

// execJS runs a code snippet against the rig's engine and fails on error.
func execJS(t *testing.T, rig *testRig, code string) *Result {
	t.Helper()
	r := NewExecutor(rig.engine).Execute(context.Background(), code)
	if r.Error != "" {
		t.Fatalf("unexpected error running %q: %s", code, r.Error)
	}
	return r
}

func TestClient_GetNode_SingleArg(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// Project into a plain JS object so the result exports as a map (a bare
	// ClientNode return round-trips back as the Go struct).
	r := execJS(t, rig, `
        const n = client.getNode("auth/login");
        return { id: n.id, type: n.type, edges: n.edge_count };
    `)
	m := r.Value.(map[string]any)
	if m["id"] != "auth/login" {
		t.Fatalf("expected auth/login, got %v", m["id"])
	}
	if m["type"] != "function" {
		t.Errorf("expected type function, got %v", m["type"])
	}
	// auth/login has one outbound edge to db/users.
	if m["edges"].(int64) != 1 {
		t.Errorf("expected 1 outbound edge, got %v", m["edges"])
	}
}

func TestClient_GetNode_TwoArgs(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// (ns, tail) form where ns is non-empty joins with "/".
	r := execJS(t, rig, `return client.getNode("auth", "login").id;`)
	if r.Value != "auth/login" {
		t.Fatalf("expected auth/login from (ns, tail) form, got %v", r.Value)
	}
}

func TestClient_GetNode_TwoArgs_EmptyNamespace(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// Empty ns arg => id is just the tail (already namespaced).
	r := execJS(t, rig, `return client.getNode("", "auth/login").id;`)
	if r.Value != "auth/login" {
		t.Fatalf("expected auth/login with empty ns, got %v", r.Value)
	}
}

func TestClient_GetNode_NoArgs_Throws(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := NewExecutor(rig.engine).Execute(context.Background(), `return client.getNode();`)
	if r.Error == "" {
		t.Fatal("expected error when getNode called with no args")
	}
}

func TestClient_GetNeighbors_DefaultDepth(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// auth/login connects to db/users (outbound); depth default 1.
	r := execJS(t, rig, `return client.getNeighbors("auth/login").map(n => n.id);`)
	ids := r.Value.([]any)
	if len(ids) != 1 || ids[0] != "db/users" {
		t.Fatalf("expected [db/users], got %v", ids)
	}
}

func TestClient_GetNeighbors_InboundDirection(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// db/users has an inbound edge from auth/login; BFS covers both directions.
	r := execJS(t, rig, `return client.getNeighbors("db/users", 1).map(n => n.id);`)
	ids := r.Value.([]any)
	if len(ids) != 1 || ids[0] != "auth/login" {
		t.Fatalf("expected [auth/login] as inbound neighbor, got %v", ids)
	}
}

func TestClient_GetNeighbors_NsTailForm(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// (ns, id) form: second arg is a string, so it's treated as a tail.
	r := execJS(t, rig, `return client.getNeighbors("auth", "login").map(n => n.id);`)
	ids := r.Value.([]any)
	if len(ids) != 1 || ids[0] != "db/users" {
		t.Fatalf("expected [db/users] from (ns, id) form, got %v", ids)
	}
}

func TestClient_GetNeighbors_NsTailWithDepth(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// Three-arg form: (ns, id, depth).
	r := execJS(t, rig, `return client.getNeighbors("auth", "login", 2).length;`)
	if r.Value.(int64) < 1 {
		t.Fatalf("expected at least one neighbor, got %v", r.Value)
	}
}

func TestClient_GetNeighbors_DepthClamp(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// Depth 0 clamps to 1, huge depth clamps to 5 — both should not error.
	execJS(t, rig, `return client.getNeighbors("auth/login", 0);`)
	execJS(t, rig, `return client.getNeighbors("auth/login", 99);`)
}

func TestClient_GetNeighbors_MissingStart_Throws(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := NewExecutor(rig.engine).Execute(context.Background(), `return client.getNeighbors("does/not-exist");`)
	if r.Error == "" {
		t.Fatal("expected error for missing start node")
	}
}

func TestClient_GetNeighbors_NoArgs_Throws(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := NewExecutor(rig.engine).Execute(context.Background(), `return client.getNeighbors();`)
	if r.Error == "" {
		t.Fatal("expected error when getNeighbors called with no args")
	}
}

func TestClient_GetGraph_All(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `return client.getGraph().length;`)
	if r.Value.(int64) != 3 {
		t.Fatalf("expected 3 nodes in full graph, got %v", r.Value)
	}
}

func TestClient_GetGraph_FilteredNamespace(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// All seeded nodes carry namespace "default" (the folder is only an ID
	// prefix), so filtering by "default" returns all three and a bogus
	// namespace returns none.
	r := execJS(t, rig, `return client.getGraph("default").length;`)
	if r.Value.(int64) != 3 {
		t.Fatalf("expected 3 default-namespace nodes, got %v", r.Value)
	}
	r = execJS(t, rig, `return client.getGraph("nope").length;`)
	if r.Value.(int64) != 0 {
		t.Fatalf("expected 0 nodes for unknown namespace, got %v", r.Value)
	}
}

func TestClient_ListByTag(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// Tag a node then list by that tag. Uses write context to add the tag.
	ex := NewExecutor(rig.engine)
	write := &WriteContext{SessionID: "s", Namespace: "default", UndoStack: curator.NewUndoStack()}
	if r := ex.ExecuteWithWrites(context.Background(), `return client.tag("auth/login", "security");`, write); r.Error != "" {
		t.Fatalf("tag setup failed: %s", r.Error)
	}

	r := execJS(t, rig, `return client.listByTag("security").map(n => n.id);`)
	ids := r.Value.([]any)
	if len(ids) != 1 || ids[0] != "auth/login" {
		t.Fatalf("expected [auth/login] for tag security, got %v", ids)
	}
}

func TestClient_ListByTag_Empty_Throws(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := NewExecutor(rig.engine).Execute(context.Background(), `return client.listByTag("");`)
	if r.Error == "" {
		t.Fatal("expected error for empty tag")
	}
}

func TestClient_ListByType(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `return client.listByType("function").map(n => n.id).sort();`)
	ids := r.Value.([]any)
	if len(ids) != 2 {
		t.Fatalf("expected 2 function nodes, got %v", ids)
	}
}

func TestClient_ListByType_Empty_Throws(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := NewExecutor(rig.engine).Execute(context.Background(), `return client.listByType("");`)
	if r.Error == "" {
		t.Fatal("expected error for empty type")
	}
}

func TestClient_ListByNamespace(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// All seeded nodes are in the "default" namespace.
	r := execJS(t, rig, `return client.listByNamespace("default").length;`)
	if r.Value.(int64) != 3 {
		t.Fatalf("expected 3 default namespace nodes, got %v", r.Value)
	}
}

func TestClient_ListByNamespace_Empty_Throws(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := NewExecutor(rig.engine).Execute(context.Background(), `return client.listByNamespace("");`)
	if r.Error == "" {
		t.Fatal("expected error for empty namespace")
	}
}

func TestClient_ListAllTags(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	write := &WriteContext{SessionID: "s", Namespace: "default", UndoStack: curator.NewUndoStack()}
	if r := ex.ExecuteWithWrites(context.Background(), `return client.tag("auth/login", ["zeta", "alpha"]);`, write); r.Error != "" {
		t.Fatalf("tag setup failed: %s", r.Error)
	}

	r := execJS(t, rig, `return client.listAllTags();`)
	tags := r.Value.([]string)
	// Sorted output: alpha before zeta.
	if len(tags) != 2 || tags[0] != "alpha" || tags[1] != "zeta" {
		t.Fatalf("expected sorted [alpha zeta], got %v", tags)
	}
}

func TestClient_ListAllTypes(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `return client.listAllTypes();`)
	types := r.Value.([]string)
	// function and module, sorted.
	if len(types) != 2 || types[0] != "function" || types[1] != "module" {
		t.Fatalf("expected [function module], got %v", types)
	}
}

func TestClient_ListNamespaces(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `return client.listNamespaces();`)
	ns := r.Value.([]string)
	if len(ns) != 1 || ns[0] != "default" {
		t.Fatalf("expected [default], got %v", ns)
	}
}

func TestClient_ListOrphans(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// auth/logout has no inbound and no outbound edges — it's the orphan.
	r := execJS(t, rig, `return client.listOrphans().map(n => n.id);`)
	ids := r.Value.([]any)
	if len(ids) != 1 || ids[0] != "auth/logout" {
		t.Fatalf("expected [auth/logout] orphan, got %v", ids)
	}
}

func TestClient_GetStats(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `
        const s = client.getStats();
        return { nc: s.node_count, ec: s.edge_count, ns: s.namespaces.length };
    `)
	m := r.Value.(map[string]any)
	if m["nc"].(int64) != 3 {
		t.Errorf("expected node_count 3, got %v", m["nc"])
	}
	if m["ec"].(int64) != 1 {
		t.Errorf("expected edge_count 1, got %v", m["ec"])
	}
	if m["ns"].(int64) != 1 {
		t.Errorf("expected 1 namespace, got %v", m["ns"])
	}
}

func TestClient_Search_ReturnsArray(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// With the mock embedder + seeded embeddings, search returns an array
	// (possibly of matches). We assert it's an array and doesn't error.
	r := execJS(t, rig, `return Array.isArray(client.search("login"));`)
	if r.Value != true {
		t.Fatalf("expected search to return an array, got %v", r.Value)
	}
}

func TestClient_Search_Empty_Throws(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := NewExecutor(rig.engine).Execute(context.Background(), `return client.search("");`)
	if r.Error == "" {
		t.Fatal("expected error for empty search query")
	}
}

func TestClient_Query_Shape(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// query returns { xml, error, nodes }. depth/budget exercise toInt.
	r := execJS(t, rig, `
        const res = client.query({query: "login", depth: 2, budget: 1000});
        return { hasXml: typeof res.xml === "string", isErr: res.error, nodesArr: Array.isArray(res.nodes) };
    `)
	m := r.Value.(map[string]any)
	if m["hasXml"] != true {
		t.Errorf("expected xml string, got %v", m["hasXml"])
	}
	if m["nodesArr"] != true {
		t.Errorf("expected nodes array, got %v", m["nodesArr"])
	}
}

func TestClient_Query_MissingQuery_Throws(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	r := NewExecutor(rig.engine).Execute(context.Background(), `return client.query({});`)
	if r.Error == "" {
		t.Fatal("expected error when query field missing")
	}
}

// ---------------------------------------------------------------------------
// Direct unit tests for pure helpers.
// ---------------------------------------------------------------------------

func TestToInt(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{int(5), 5},
		{int64(7), 7},
		{float64(3.9), 3},
		{float32(2.1), 2},
		{"nope", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := toInt(c.in); got != c.want {
			t.Errorf("toInt(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestHasTag(t *testing.T) {
	n := &node.Node{Tags: []string{"a", "b"}}
	if !hasTag(n, "b") {
		t.Error("expected hasTag to find 'b'")
	}
	if hasTag(n, "c") {
		t.Error("did not expect hasTag to find 'c'")
	}
}

func TestToClientNode_Nil(t *testing.T) {
	if got := toClientNode(nil, nil); got.ID != "" {
		t.Errorf("expected zero ClientNode for nil node, got %+v", got)
	}
}

func TestToClientNode_TruncatesContextAndSetsSource(t *testing.T) {
	n := &node.Node{
		ID:      "core/x",
		Type:    "service",
		Context: strings.Repeat("y", 3000),
	}
	n.Source.Path = "internal/x.go"
	n.Source.Lines = [2]int{1, 10}
	out := toClientNode(nil, n)
	if !strings.Contains(out.Context, "context truncated") {
		t.Error("expected long context to be truncated")
	}
	if out.Source == nil || out.Source.Path != "internal/x.go" {
		t.Errorf("expected source populated, got %+v", out.Source)
	}
}

func TestBfsNeighbors_NilGraph(t *testing.T) {
	if got := bfsNeighbors(nil, "x", 1); got != nil {
		t.Errorf("expected nil for nil graph, got %v", got)
	}
}

func TestCollectCapped_NilGraph(t *testing.T) {
	if got := collectCapped(nil, func(*node.Node) bool { return true }); got != nil {
		t.Errorf("expected nil for nil graph, got %v", got)
	}
}

func TestExtractText_Nil(t *testing.T) {
	if got := extractText(nil); got != "" {
		t.Errorf("expected empty for nil result, got %q", got)
	}
}

func TestIsErr_Nil(t *testing.T) {
	if !isErr(nil) {
		t.Error("expected nil result to be treated as error")
	}
}

func TestAllNamespaces_DefaultBucket(t *testing.T) {
	g := graph.NewGraph()
	_ = g.UpsertNode(&node.Node{ID: "solo", Status: "active"}) // empty namespace -> "default"
	got := allNamespaces(g)
	if len(got) != 1 || got[0] != "default" {
		t.Fatalf("expected [default], got %v", got)
	}
}

func TestNodeNotFoundError_NilEngine(t *testing.T) {
	err := nodeNotFoundError(nil, "foo")
	if err == nil || !strings.Contains(err.Error(), "foo") {
		t.Errorf("expected not-found error mentioning foo, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Multi-namespace fixtures
// ---------------------------------------------------------------------------

// newMultiNSRig seeds the standard default-namespace fixture plus a real
// "core" namespace: prefixed IDs, matching frontmatter, an on-disk
// _namespace.md manifest, and a namespace manager loaded from disk.
func newMultiNSRig(t *testing.T) *testRig {
	t.Helper()
	return newTestRigWith(t, func(r *testRig) {
		r.writeNodeNS(t, "core", "", "engine", "service", "Core engine service", [][2]string{
			{"core/api", "depends_on"},
		})
		r.writeNodeNS(t, "core", "", "api", "service", "Core API surface", nil)
	})
}

func TestClient_ListByNamespace_MultiNamespace(t *testing.T) {
	rig := newMultiNSRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `return client.listByNamespace("core").map(n => n.id).sort();`)
	ids := r.Value.([]any)
	if len(ids) != 2 || ids[0] != "core/api" || ids[1] != "core/engine" {
		t.Fatalf("expected [core/api core/engine], got %v", ids)
	}

	r = execJS(t, rig, `return client.listByNamespace("default").length;`)
	if r.Value.(int64) != 3 {
		t.Fatalf("expected 3 default-namespace nodes, got %v", r.Value)
	}
}

func TestClient_GetGraph_MultiNamespace(t *testing.T) {
	rig := newMultiNSRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `return client.getGraph().length;`)
	if r.Value.(int64) != 5 {
		t.Fatalf("expected 5 nodes in full graph, got %v", r.Value)
	}
	r = execJS(t, rig, `return client.getGraph("core").length;`)
	if r.Value.(int64) != 2 {
		t.Fatalf("expected 2 core nodes, got %v", r.Value)
	}
	r = execJS(t, rig, `return client.getGraph("default").length;`)
	if r.Value.(int64) != 3 {
		t.Fatalf("expected 3 default nodes, got %v", r.Value)
	}
}

func TestClient_ListNamespaces_MultiNamespace(t *testing.T) {
	rig := newMultiNSRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `return client.listNamespaces();`)
	ns := r.Value.([]string)
	if len(ns) != 2 || ns[0] != "core" || ns[1] != "default" {
		t.Fatalf("expected [core default], got %v", ns)
	}
}

func TestClient_GetStats_MultiNamespace(t *testing.T) {
	rig := newMultiNSRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `
        const s = client.getStats();
        return { nc: s.node_count, ns: s.namespaces.length };
    `)
	m := r.Value.(map[string]any)
	if m["nc"].(int64) != 5 {
		t.Errorf("expected node_count 5, got %v", m["nc"])
	}
	if m["ns"].(int64) != 2 {
		t.Errorf("expected 2 namespaces, got %v", m["ns"])
	}
}

func TestClient_GetNode_TwoArgs_RealNamespace(t *testing.T) {
	rig := newMultiNSRig(t)
	defer rig.cleanup()

	r := execJS(t, rig, `
        const n = client.getNode("core", "engine");
        return { id: n.id, ns: n.namespace };
    `)
	m := r.Value.(map[string]any)
	if m["id"] != "core/engine" || m["ns"] != "core" {
		t.Fatalf("expected core/engine in namespace core, got %v", m)
	}

	// Bare tail resolves through ResolveNodeID's known-namespace prefixing.
	r = execJS(t, rig, `return client.getNode("engine").id;`)
	if r.Value != "core/engine" {
		t.Fatalf("expected bare tail to resolve to core/engine, got %v", r.Value)
	}
}

func TestClient_EmptyNamespaceField_TreatedAsDefault(t *testing.T) {
	// A hand-authored node file with no namespace field must count as
	// "default" for both getGraph and listByNamespace (regression: getGraph
	// used to compare the raw field and miss such nodes).
	rig := newTestRigWith(t, func(r *testRig) {
		r.writeNodeNS(t, "", "", "legacy", "concept", "Hand-authored legacy node", nil)
	})
	defer rig.cleanup()

	r := execJS(t, rig, `return client.listByNamespace("default").map(n => n.id).sort();`)
	byNS := r.Value.([]any)
	if len(byNS) != 4 {
		t.Fatalf("expected 4 default nodes from listByNamespace, got %v", byNS)
	}

	r = execJS(t, rig, `return client.getGraph("default").map(n => n.id).sort();`)
	byGraph := r.Value.([]any)
	if len(byGraph) != 4 {
		t.Fatalf("expected getGraph(\"default\") to agree with listByNamespace, got %v", byGraph)
	}

	r = execJS(t, rig, `return client.listNamespaces();`)
	ns := r.Value.([]string)
	if len(ns) != 1 || ns[0] != "default" {
		t.Fatalf("expected the legacy node to fold into [default], got %v", ns)
	}
}
