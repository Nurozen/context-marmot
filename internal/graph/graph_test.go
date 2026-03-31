package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeNode(id string, edges ...node.Edge) *node.Node {
	return &node.Node{
		ID:        id,
		Type:      "function",
		Namespace: "test",
		Status:    "active",
		Edges:     edges,
		Summary:   "Node " + id,
	}
}

func structuralEdge(target string) node.Edge {
	return node.Edge{Target: target, Relation: node.Contains, Class: node.Structural}
}

func behavioralEdge(target string) node.Edge {
	return node.Edge{Target: target, Relation: node.Calls, Class: node.Behavioral}
}

// ---------------------------------------------------------------------------
// AddNode + GetNode roundtrip
// ---------------------------------------------------------------------------

func TestAddNode_GetNode(t *testing.T) {
	g := NewGraph()
	n := makeNode("a")

	if err := g.AddNode(n); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	got, ok := g.GetNode("a")
	if !ok {
		t.Fatal("GetNode returned false for existing node")
	}
	if got.ID != "a" {
		t.Errorf("got ID %q, want %q", got.ID, "a")
	}
}

func TestAddNode_Duplicate(t *testing.T) {
	g := NewGraph()
	n := makeNode("a")
	if err := g.AddNode(n); err != nil {
		t.Fatal(err)
	}
	if err := g.AddNode(n); err == nil {
		t.Fatal("expected error adding duplicate node")
	}
}

func TestAddNode_Nil(t *testing.T) {
	g := NewGraph()
	if err := g.AddNode(nil); err == nil {
		t.Fatal("expected error adding nil node")
	}
}

func TestGetNode_NotFound(t *testing.T) {
	g := NewGraph()
	_, ok := g.GetNode("missing")
	if ok {
		t.Fatal("GetNode returned true for non-existent node")
	}
}

// ---------------------------------------------------------------------------
// AddEdge — structural allowed (no cycle)
// ---------------------------------------------------------------------------

func TestAddEdge_StructuralNoCycle(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))

	err := g.AddEdge("a", structuralEdge("b"))
	if err != nil {
		t.Fatalf("AddEdge structural (no cycle): %v", err)
	}

	if g.EdgeCount() != 1 {
		t.Errorf("EdgeCount = %d, want 1", g.EdgeCount())
	}
}

// ---------------------------------------------------------------------------
// AddEdge — structural cycle rejected
// ---------------------------------------------------------------------------

func TestAddEdge_StructuralCycleRejected(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddNode(makeNode("c"))

	// a -> b -> c (structural)
	if err := g.AddEdge("a", structuralEdge("b")); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge("b", structuralEdge("c")); err != nil {
		t.Fatal(err)
	}

	// c -> a would create a structural cycle: MUST be rejected.
	err := g.AddEdge("c", structuralEdge("a"))
	if err == nil {
		t.Fatal("expected error for structural cycle c -> a")
	}

	// Verify edge was NOT added.
	if g.EdgeCount() != 2 {
		t.Errorf("EdgeCount = %d, want 2 (cycle edge should not be added)", g.EdgeCount())
	}
}

func TestAddEdge_StructuralSelfLoop(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))

	err := g.AddEdge("a", structuralEdge("a"))
	if err == nil {
		t.Fatal("expected error for structural self-loop")
	}
}

// ---------------------------------------------------------------------------
// AddEdge — behavioral cycle allowed
// ---------------------------------------------------------------------------

func TestAddEdge_BehavioralCycleAllowed(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddNode(makeNode("c"))

	// a -> b -> c -> a (all behavioral) — cycles are OK.
	if err := g.AddEdge("a", behavioralEdge("b")); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge("b", behavioralEdge("c")); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge("c", behavioralEdge("a")); err != nil {
		t.Fatal(err)
	}

	if g.EdgeCount() != 3 {
		t.Errorf("EdgeCount = %d, want 3", g.EdgeCount())
	}
}

// ---------------------------------------------------------------------------
// AddEdge — source not found
// ---------------------------------------------------------------------------

func TestAddEdge_SourceNotFound(t *testing.T) {
	g := NewGraph()
	err := g.AddEdge("missing", structuralEdge("b"))
	if err == nil {
		t.Fatal("expected error for missing source node")
	}
}

// ---------------------------------------------------------------------------
// RemoveNode cascades edge cleanup
// ---------------------------------------------------------------------------

func TestRemoveNode_CascadesEdges(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddNode(makeNode("c"))

	g.AddEdge("a", behavioralEdge("b"))
	g.AddEdge("b", behavioralEdge("c"))
	g.AddEdge("c", behavioralEdge("b")) // inbound to b

	if g.EdgeCount() != 3 {
		t.Fatalf("EdgeCount before removal = %d, want 3", g.EdgeCount())
	}

	// Remove b: should remove a->b, b->c, c->b.
	if err := g.RemoveNode("b"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}

	if g.NodeCount() != 2 {
		t.Errorf("NodeCount = %d, want 2", g.NodeCount())
	}
	if g.EdgeCount() != 0 {
		t.Errorf("EdgeCount after removing b = %d, want 0", g.EdgeCount())
	}

	// Verify node is gone.
	if _, ok := g.GetNode("b"); ok {
		t.Error("GetNode(b) should return false after removal")
	}
}

func TestRemoveNode_NotFound(t *testing.T) {
	g := NewGraph()
	if err := g.RemoveNode("nope"); err == nil {
		t.Fatal("expected error removing non-existent node")
	}
}

// ---------------------------------------------------------------------------
// RemoveEdge
// ---------------------------------------------------------------------------

func TestRemoveEdge(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddEdge("a", behavioralEdge("b"))

	if g.EdgeCount() != 1 {
		t.Fatalf("EdgeCount = %d, want 1", g.EdgeCount())
	}

	if err := g.RemoveEdge("a", "b"); err != nil {
		t.Fatalf("RemoveEdge: %v", err)
	}

	if g.EdgeCount() != 0 {
		t.Errorf("EdgeCount = %d, want 0", g.EdgeCount())
	}

	// Verify inbound is also cleaned up.
	inEdges := g.GetEdges("b", Inbound)
	if len(inEdges) != 0 {
		t.Errorf("inbound edges for b = %d, want 0", len(inEdges))
	}
}

func TestRemoveEdge_NotFound(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))

	if err := g.RemoveEdge("a", "b"); err == nil {
		t.Fatal("expected error removing non-existent edge")
	}
}

// ---------------------------------------------------------------------------
// GetEdges both directions
// ---------------------------------------------------------------------------

func TestGetEdges_BothDirections(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddNode(makeNode("c"))

	g.AddEdge("a", behavioralEdge("b"))
	g.AddEdge("c", behavioralEdge("b"))

	// Outbound from a.
	out := g.GetEdges("a", Outbound)
	if len(out) != 1 || out[0].Target != "b" {
		t.Errorf("outbound edges from a: got %v, want [->b]", out)
	}

	// Inbound to b.
	in := g.GetEdges("b", Inbound)
	if len(in) != 2 {
		t.Errorf("inbound edges to b: got %d, want 2", len(in))
	}

	// Outbound from b (none).
	outB := g.GetEdges("b", Outbound)
	if len(outB) != 0 {
		t.Errorf("outbound edges from b: got %d, want 0", len(outB))
	}
}

// ---------------------------------------------------------------------------
// GetNeighbors at various depths
// ---------------------------------------------------------------------------

func TestGetNeighbors_Depth1(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddNode(makeNode("c"))

	g.AddEdge("a", behavioralEdge("b"))
	g.AddEdge("b", behavioralEdge("c"))

	neighbors := g.GetNeighbors("a", 1)
	if len(neighbors) != 1 {
		t.Fatalf("depth=1: got %d neighbors, want 1", len(neighbors))
	}
	if neighbors[0].ID != "b" {
		t.Errorf("depth=1: got %q, want %q", neighbors[0].ID, "b")
	}
}

func TestGetNeighbors_Depth2(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddNode(makeNode("c"))
	g.AddNode(makeNode("d"))

	g.AddEdge("a", behavioralEdge("b"))
	g.AddEdge("b", behavioralEdge("c"))
	g.AddEdge("c", behavioralEdge("d"))

	neighbors := g.GetNeighbors("a", 2)
	if len(neighbors) != 2 {
		t.Fatalf("depth=2: got %d neighbors, want 2", len(neighbors))
	}
	ids := map[string]bool{}
	for _, n := range neighbors {
		ids[n.ID] = true
	}
	if !ids["b"] || !ids["c"] {
		t.Errorf("depth=2: want b and c, got %v", ids)
	}
}

func TestGetNeighbors_Depth0(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddEdge("a", behavioralEdge("b"))

	neighbors := g.GetNeighbors("a", 0)
	if len(neighbors) != 0 {
		t.Errorf("depth=0: got %d neighbors, want 0", len(neighbors))
	}
}

func TestGetNeighbors_Missing(t *testing.T) {
	g := NewGraph()
	neighbors := g.GetNeighbors("missing", 3)
	if len(neighbors) != 0 {
		t.Errorf("missing node: got %d neighbors, want 0", len(neighbors))
	}
}

// ---------------------------------------------------------------------------
// NodeCount and EdgeCount
// ---------------------------------------------------------------------------

func TestNodeCount_EdgeCount(t *testing.T) {
	g := NewGraph()

	if g.NodeCount() != 0 {
		t.Errorf("empty graph NodeCount = %d", g.NodeCount())
	}
	if g.EdgeCount() != 0 {
		t.Errorf("empty graph EdgeCount = %d", g.EdgeCount())
	}

	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddNode(makeNode("c"))

	if g.NodeCount() != 3 {
		t.Errorf("NodeCount = %d, want 3", g.NodeCount())
	}

	g.AddEdge("a", behavioralEdge("b"))
	g.AddEdge("a", structuralEdge("c"))

	if g.EdgeCount() != 2 {
		t.Errorf("EdgeCount = %d, want 2", g.EdgeCount())
	}
}

// ---------------------------------------------------------------------------
// AddNode with edges registers them
// ---------------------------------------------------------------------------

func TestAddNode_RegistersEdges(t *testing.T) {
	g := NewGraph()

	// Add target nodes first so the graph has them.
	g.AddNode(makeNode("b"))

	// Add node with edges already set.
	a := makeNode("a", behavioralEdge("b"))
	if err := g.AddNode(a); err != nil {
		t.Fatal(err)
	}

	out := g.GetEdges("a", Outbound)
	if len(out) != 1 || out[0].Target != "b" {
		t.Errorf("outbound from a: %v", out)
	}

	in := g.GetEdges("b", Inbound)
	if len(in) != 1 || in[0].Target != "a" {
		t.Errorf("inbound to b: %v", in)
	}

	if g.EdgeCount() != 1 {
		t.Errorf("EdgeCount = %d, want 1", g.EdgeCount())
	}
}

// ---------------------------------------------------------------------------
// Mixed structural and behavioral edges
// ---------------------------------------------------------------------------

func TestMixedEdges(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddNode(makeNode("c"))

	// Structural chain: a -> b
	if err := g.AddEdge("a", structuralEdge("b")); err != nil {
		t.Fatal(err)
	}

	// Behavioral cycle: b -> c -> a  (allowed even though a -> b is structural)
	if err := g.AddEdge("b", behavioralEdge("c")); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge("c", behavioralEdge("a")); err != nil {
		t.Fatal(err)
	}

	if g.EdgeCount() != 3 {
		t.Errorf("EdgeCount = %d, want 3", g.EdgeCount())
	}

	// Now try to add structural edge b -> a which would form structural cycle.
	err := g.AddEdge("b", structuralEdge("a"))
	if err == nil {
		t.Fatal("expected structural cycle rejection for b -> a")
	}

	// Edge count should not change.
	if g.EdgeCount() != 3 {
		t.Errorf("EdgeCount after rejection = %d, want 3", g.EdgeCount())
	}
}

// ---------------------------------------------------------------------------
// Structural cycle detection ignores behavioral edges
// ---------------------------------------------------------------------------

func TestStructuralCycleCheck_IgnoresBehavioral(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))
	g.AddNode(makeNode("c"))

	// Behavioral: a -> b -> c
	g.AddEdge("a", behavioralEdge("b"))
	g.AddEdge("b", behavioralEdge("c"))

	// Structural: c -> a should be ALLOWED because the existing path a->b->c
	// is purely behavioral.
	err := g.AddEdge("c", structuralEdge("a"))
	if err != nil {
		t.Fatalf("structural c->a should be allowed (no structural path from a to c): %v", err)
	}
}

// ---------------------------------------------------------------------------
// LoadGraph from temporary node store on disk
// ---------------------------------------------------------------------------

func TestLoadGraph(t *testing.T) {
	dir := t.TempDir()
	store := node.NewStore(dir)

	nodes := []*node.Node{
		{
			ID: "a", Type: "function", Namespace: "test", Status: "active",
			Edges:   []node.Edge{{Target: "b", Relation: node.Calls, Class: node.Behavioral}},
			Summary: "Node a.",
		},
		{
			ID: "b", Type: "function", Namespace: "test", Status: "active",
			Summary: "Node b.",
		},
		{
			ID: "c", Type: "module", Namespace: "test", Status: "active",
			Edges: []node.Edge{
				{Target: "a", Relation: node.Contains, Class: node.Structural},
			},
			Summary: "Node c.",
		},
	}

	for _, n := range nodes {
		if err := store.SaveNode(n); err != nil {
			t.Fatalf("SaveNode(%s): %v", n.ID, err)
		}
	}

	g, err := LoadGraph(store)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	if g.NodeCount() != 3 {
		t.Errorf("NodeCount = %d, want 3", g.NodeCount())
	}

	// Check edges were loaded.
	if g.EdgeCount() != 2 {
		t.Errorf("EdgeCount = %d, want 2", g.EdgeCount())
	}

	// Verify specific nodes.
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := g.GetNode(id); !ok {
			t.Errorf("missing node %q after LoadGraph", id)
		}
	}

	// Verify outbound edge from a.
	outA := g.GetEdges("a", Outbound)
	if len(outA) != 1 || outA[0].Target != "b" {
		t.Errorf("outbound from a: %v", outA)
	}
}

func TestLoadGraph_SkipsMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	store := node.NewStore(dir)

	// Save a good node.
	good := &node.Node{
		ID: "good", Type: "function", Namespace: "test", Status: "active",
		Summary: "Good node.",
	}
	if err := store.SaveNode(good); err != nil {
		t.Fatal(err)
	}

	// Write a malformed file directly.
	badPath := store.NodePath("bad")
	if err := writeFile(t, badPath, "this is not valid frontmatter"); err != nil {
		t.Fatal(err)
	}

	g, err := LoadGraph(store)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	if g.NodeCount() != 1 {
		t.Errorf("NodeCount = %d, want 1 (malformed should be skipped)", g.NodeCount())
	}
}

// ---------------------------------------------------------------------------
// Edge classification via AddEdge (auto-classify from relation)
// ---------------------------------------------------------------------------

func TestAddEdge_AutoClassifies(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("a"))
	g.AddNode(makeNode("b"))

	// Edge with no Class set — should be derived from relation.
	e := node.Edge{Target: "b", Relation: node.Imports}
	if err := g.AddEdge("a", e); err != nil {
		t.Fatal(err)
	}

	out := g.GetEdges("a", Outbound)
	if len(out) != 1 {
		t.Fatalf("expected 1 outbound edge, got %d", len(out))
	}
	if out[0].Class != node.Structural {
		t.Errorf("Class = %q, want %q", out[0].Class, node.Structural)
	}
}

// ---------------------------------------------------------------------------
// Large-ish graph BFS
// ---------------------------------------------------------------------------

func TestGetNeighbors_LargerGraph(t *testing.T) {
	g := NewGraph()

	// Build a chain: n0 -> n1 -> n2 -> ... -> n9
	for i := 0; i < 10; i++ {
		g.AddNode(makeNode(fmt.Sprintf("n%d", i)))
	}
	for i := 0; i < 9; i++ {
		g.AddEdge(fmt.Sprintf("n%d", i), behavioralEdge(fmt.Sprintf("n%d", i+1)))
	}

	// Depth 5 from n0 should reach n1..n5.
	neighbors := g.GetNeighbors("n0", 5)
	if len(neighbors) != 5 {
		t.Errorf("depth=5 from n0: got %d, want 5", len(neighbors))
	}

	// Depth 100 from n0 should reach all 9 (n1..n9).
	all := g.GetNeighbors("n0", 100)
	if len(all) != 9 {
		t.Errorf("depth=100 from n0: got %d, want 9", len(all))
	}
}

// ---------------------------------------------------------------------------
// Helper: write file for test
// ---------------------------------------------------------------------------

func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	// Ensure parent dir exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
