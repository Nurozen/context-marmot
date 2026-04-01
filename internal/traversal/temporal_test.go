package traversal

import (
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// buildTemporalGraph constructs a small graph:
//
//	"a" (active) -> "b" (active) -> "c" (superseded)
//
// Edges use the Calls relation (behavioral, cycles allowed).
func buildTemporalGraph() *graph.Graph {
	g := graph.NewGraph()
	nodes := []*node.Node{
		{ID: "a", Type: "function", Status: node.StatusActive, Summary: "Node A active"},
		{ID: "b", Type: "function", Status: node.StatusActive, Summary: "Node B active"},
		{ID: "c", Type: "function", Status: node.StatusSuperseded, Summary: "Node C superseded"},
	}
	for _, n := range nodes {
		if err := g.AddNode(n); err != nil {
			panic("buildTemporalGraph AddNode: " + err.Error())
		}
	}
	if err := g.AddEdge("a", node.Edge{Target: "b", Relation: node.Calls}); err != nil {
		panic("buildTemporalGraph AddEdge a->b: " + err.Error())
	}
	if err := g.AddEdge("b", node.Edge{Target: "c", Relation: node.Calls}); err != nil {
		panic("buildTemporalGraph AddEdge b->c: " + err.Error())
	}
	return g
}

// TestTraversal_ExcludesSupersededByDefault verifies that traversal with
// IncludeSuperseded: false (the default) stops expanding at superseded nodes.
func TestTraversal_ExcludesSupersededByDefault(t *testing.T) {
	g := buildTemporalGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:          []string{"a"},
		MaxDepth:          3,
		IncludeSuperseded: false,
	})

	ids := make(map[string]bool)
	for _, n := range sub.Nodes {
		ids[n.ID] = true
	}

	if !ids["a"] {
		t.Error("expected active node 'a' to be included")
	}
	if !ids["b"] {
		t.Error("expected active node 'b' to be included")
	}
	if ids["c"] {
		t.Error("superseded node 'c' should NOT be included when IncludeSuperseded=false")
	}
}

// TestTraversal_IncludeSuperseded verifies that setting IncludeSuperseded: true
// causes superseded nodes to be traversed and returned.
func TestTraversal_IncludeSuperseded(t *testing.T) {
	g := buildTemporalGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:          []string{"a"},
		MaxDepth:          3,
		IncludeSuperseded: true,
	})

	ids := make(map[string]bool)
	for _, n := range sub.Nodes {
		ids[n.ID] = true
	}

	for _, want := range []string{"a", "b", "c"} {
		if !ids[want] {
			t.Errorf("expected node %q to be included when IncludeSuperseded=true", want)
		}
	}
	if len(sub.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(sub.Nodes))
	}
}

// TestCompact_StatusAttribute verifies that Compact serialises the status
// attribute correctly for both superseded and active nodes.
func TestCompact_StatusAttribute(t *testing.T) {
	g := buildTemporalGraph()

	// ---- superseded node in XML ----
	// Traverse with IncludeSuperseded so 'c' is present in the subgraph.
	subAll := Traverse(g, TraversalConfig{
		EntryIDs:          []string{"a"},
		MaxDepth:          3,
		IncludeSuperseded: true,
	})
	resultAll := Compact(g, subAll, 100000)

	if !strings.Contains(resultAll.XML, `status="superseded"`) {
		t.Errorf("expected status=\"superseded\" in XML for node 'c';\nXML:\n%s", resultAll.XML)
	}

	// ---- active node in XML ----
	// Traverse without superseded so only active nodes are present.
	subActive := Traverse(g, TraversalConfig{
		EntryIDs:          []string{"a"},
		MaxDepth:          1,
		IncludeSuperseded: false,
	})
	resultActive := Compact(g, subActive, 100000)

	// Both 'a' (entry/full node) and 'b' (compact node) are active.
	// compact.go normalises "" -> "active" before writing, so we expect the
	// literal attribute value "active" for both element types.
	if !strings.Contains(resultActive.XML, `status="active"`) {
		t.Errorf("expected status=\"active\" in XML for active nodes;\nXML:\n%s", resultActive.XML)
	}
	// Superseded node should be entirely absent from this compaction.
	if strings.Contains(resultActive.XML, `id="c"`) {
		t.Errorf("superseded node 'c' should not appear in active-only compact output;\nXML:\n%s", resultActive.XML)
	}
}
