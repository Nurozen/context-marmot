package curator

import (
	"fmt"
	"testing"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

func TestOrphanDetection(t *testing.T) {
	g := graph.NewGraph()

	// Add an orphan node (no edges).
	orphan := &node.Node{ID: "orphan-1", Type: "concept", Summary: "An orphan"}
	if err := g.AddNode(orphan); err != nil {
		t.Fatal(err)
	}

	// Add two connected nodes.
	src := &node.Node{
		ID:      "src",
		Type:    "function",
		Summary: "Source node",
		Edges: []node.Edge{
			{Target: "tgt", Relation: node.Calls},
		},
	}
	tgt := &node.Node{ID: "tgt", Type: "function", Summary: "Target node"}
	if err := g.AddNode(tgt); err != nil {
		t.Fatal(err)
	}
	if err := g.AddNode(src); err != nil {
		t.Fatal(err)
	}

	results := Analyze(g, nil, nil, nil, AnalyzeOpts{})

	// Should find exactly 1 orphan suggestion.
	var orphans []Suggestion
	for _, s := range results {
		if s.Type == "orphan" {
			orphans = append(orphans, s)
		}
	}
	if len(orphans) != 1 {
		t.Errorf("expected 1 orphan suggestion, got %d", len(orphans))
	}
	if len(orphans) > 0 && orphans[0].NodeIDs[0] != "orphan-1" {
		t.Errorf("expected orphan node ID 'orphan-1', got %q", orphans[0].NodeIDs[0])
	}
	if len(orphans) > 0 && orphans[0].Severity != "warning" {
		t.Errorf("expected severity 'warning', got %q", orphans[0].Severity)
	}
}

func TestMissingSummaryDetection(t *testing.T) {
	g := graph.NewGraph()

	// Node with a summary.
	withSummary := &node.Node{ID: "has-summary", Type: "concept", Summary: "Has a summary"}
	if err := g.AddNode(withSummary); err != nil {
		t.Fatal(err)
	}

	// Node without a summary.
	noSummary := &node.Node{ID: "no-summary", Type: "concept", Summary: ""}
	if err := g.AddNode(noSummary); err != nil {
		t.Fatal(err)
	}

	results := Analyze(g, nil, nil, nil, AnalyzeOpts{})

	var missing []Suggestion
	for _, s := range results {
		if s.Type == "missing_summary" {
			missing = append(missing, s)
		}
	}
	if len(missing) != 1 {
		t.Errorf("expected 1 missing_summary suggestion, got %d", len(missing))
	}
	if len(missing) > 0 && missing[0].NodeIDs[0] != "no-summary" {
		t.Errorf("expected node ID 'no-summary', got %q", missing[0].NodeIDs[0])
	}
	if len(missing) > 0 && missing[0].Severity != "info" {
		t.Errorf("expected severity 'info', got %q", missing[0].Severity)
	}
}

func TestUntypedDetection(t *testing.T) {
	g := graph.NewGraph()

	typed := &node.Node{ID: "typed-node", Type: "function", Summary: "Typed"}
	if err := g.AddNode(typed); err != nil {
		t.Fatal(err)
	}

	untyped := &node.Node{ID: "untyped-node", Type: "", Summary: "No type"}
	if err := g.AddNode(untyped); err != nil {
		t.Fatal(err)
	}

	results := Analyze(g, nil, nil, nil, AnalyzeOpts{})

	var untypedSuggestions []Suggestion
	for _, s := range results {
		if s.Type == "untyped" {
			untypedSuggestions = append(untypedSuggestions, s)
		}
	}
	if len(untypedSuggestions) != 1 {
		t.Errorf("expected 1 untyped suggestion, got %d", len(untypedSuggestions))
	}
	if len(untypedSuggestions) > 0 && untypedSuggestions[0].NodeIDs[0] != "untyped-node" {
		t.Errorf("expected node ID 'untyped-node', got %q", untypedSuggestions[0].NodeIDs[0])
	}
	if len(untypedSuggestions) > 0 && untypedSuggestions[0].Severity != "warning" {
		t.Errorf("expected severity 'warning', got %q", untypedSuggestions[0].Severity)
	}
}

func TestPagination(t *testing.T) {
	g := graph.NewGraph()

	// Create 5 orphan nodes to generate 5 suggestions.
	for i := 0; i < 5; i++ {
		n := &node.Node{
			ID:      fmt.Sprintf("orphan-%d", i),
			Type:    "concept",
			Summary: fmt.Sprintf("Orphan %d", i),
		}
		if err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}

	// Get all results (should be 5 orphan suggestions).
	all := Analyze(g, nil, nil, nil, AnalyzeOpts{})
	if len(all) < 5 {
		t.Fatalf("expected at least 5 suggestions, got %d", len(all))
	}

	// Test limit.
	limited := Analyze(g, nil, nil, nil, AnalyzeOpts{Limit: 2})
	if len(limited) != 2 {
		t.Errorf("expected 2 suggestions with limit=2, got %d", len(limited))
	}

	// Test offset.
	offset := Analyze(g, nil, nil, nil, AnalyzeOpts{Offset: 3})
	if len(offset) != len(all)-3 {
		t.Errorf("expected %d suggestions with offset=3, got %d", len(all)-3, len(offset))
	}

	// Test limit + offset.
	paged := Analyze(g, nil, nil, nil, AnalyzeOpts{Limit: 2, Offset: 1})
	if len(paged) != 2 {
		t.Errorf("expected 2 suggestions with limit=2 offset=1, got %d", len(paged))
	}

	// Test offset beyond results.
	empty := Analyze(g, nil, nil, nil, AnalyzeOpts{Offset: 100})
	if len(empty) != 0 {
		t.Errorf("expected 0 suggestions with offset=100, got %d", len(empty))
	}
}

func TestEmptyGraph(t *testing.T) {
	g := graph.NewGraph()
	results := Analyze(g, nil, nil, nil, AnalyzeOpts{})
	if len(results) != 0 {
		t.Errorf("expected 0 suggestions for empty graph, got %d", len(results))
	}
}

func TestNilGraph(t *testing.T) {
	results := Analyze(nil, nil, nil, nil, AnalyzeOpts{})
	if len(results) != 0 {
		t.Errorf("expected 0 suggestions for nil graph, got %d", len(results))
	}
}

func TestSeveritySorting(t *testing.T) {
	g := graph.NewGraph()

	// Create a node that is both orphan (warning) and missing summary (info).
	n := &node.Node{ID: "multi-issue", Type: "concept", Summary: ""}
	if err := g.AddNode(n); err != nil {
		t.Fatal(err)
	}

	results := Analyze(g, nil, nil, nil, AnalyzeOpts{})

	if len(results) < 2 {
		t.Fatalf("expected at least 2 suggestions, got %d", len(results))
	}

	// Verify ordering: warnings should come before info.
	for i := 1; i < len(results); i++ {
		prev := severityOrder(results[i-1].Severity)
		curr := severityOrder(results[i].Severity)
		if prev > curr {
			t.Errorf("suggestions not sorted by severity: %q (idx %d) before %q (idx %d)",
				results[i-1].Severity, i-1, results[i].Severity, i)
		}
	}
}

func TestDeterministicIDs(t *testing.T) {
	// Same type + node IDs should produce the same suggestion ID.
	id1 := suggestionID("orphan", []string{"a", "b"})
	id2 := suggestionID("orphan", []string{"b", "a"})
	if id1 != id2 {
		t.Errorf("expected same ID for same type+nodes regardless of order, got %q and %q", id1, id2)
	}

	// Different types should produce different IDs.
	id3 := suggestionID("duplicate", []string{"a", "b"})
	if id1 == id3 {
		t.Errorf("expected different IDs for different types, both got %q", id1)
	}
}

func TestScopedAnalysis(t *testing.T) {
	g := graph.NewGraph()

	a := &node.Node{ID: "scoped-a", Type: "concept", Summary: "A"}
	b := &node.Node{ID: "scoped-b", Type: "", Summary: "B"}
	c := &node.Node{ID: "scoped-c", Type: "", Summary: ""}
	for _, n := range []*node.Node{a, b, c} {
		if err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}

	// Only analyze node "scoped-b".
	results := Analyze(g, nil, nil, nil, AnalyzeOpts{NodeIDs: []string{"scoped-b"}})

	for _, s := range results {
		for _, id := range s.NodeIDs {
			if id != "scoped-b" {
				t.Errorf("found suggestion for node %q outside requested scope", id)
			}
		}
	}
}

