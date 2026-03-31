package traversal

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildLinearGraph creates: A -> B -> C -> D (depth chain).
func buildLinearGraph() *graph.Graph {
	g := graph.NewGraph()
	nodes := []*node.Node{
		{ID: "A", Type: "function", Summary: "Node A", Context: "func A() {}"},
		{ID: "B", Type: "function", Summary: "Node B", Context: "func B() {}",
			Source: node.Source{Path: "src/b.go", Lines: [2]int{10, 20}}},
		{ID: "C", Type: "module", Summary: "Node C", Context: "package C",
			Source: node.Source{Path: "src/c.go", Lines: [2]int{1, 50}}},
		{ID: "D", Type: "class", Summary: "Node D", Context: "class D {}",
			Source: node.Source{Path: "src/d.go"}},
	}
	for _, n := range nodes {
		n.Edges = nil // edges added via graph.AddEdge
		_ = g.AddNode(n)
	}
	_ = g.AddEdge("A", node.Edge{Target: "B", Relation: node.Calls})
	_ = g.AddEdge("B", node.Edge{Target: "C", Relation: node.Calls})
	_ = g.AddEdge("C", node.Edge{Target: "D", Relation: node.Calls})
	return g
}

// buildBranchingGraph creates:
//
//	Entry1 -> A -> C
//	Entry2 -> B -> C -> D
func buildBranchingGraph() *graph.Graph {
	g := graph.NewGraph()
	for _, n := range []*node.Node{
		{ID: "Entry1", Type: "function", Summary: "Entry 1"},
		{ID: "Entry2", Type: "function", Summary: "Entry 2"},
		{ID: "A", Type: "function", Summary: "A node"},
		{ID: "B", Type: "function", Summary: "B node"},
		{ID: "C", Type: "module", Summary: "C node", Source: node.Source{Path: "c.go", Lines: [2]int{1, 10}}},
		{ID: "D", Type: "module", Summary: "D node", Source: node.Source{Path: "d.go"}},
	} {
		_ = g.AddNode(n)
	}
	_ = g.AddEdge("Entry1", node.Edge{Target: "A", Relation: node.Calls})
	_ = g.AddEdge("Entry2", node.Edge{Target: "B", Relation: node.Calls})
	_ = g.AddEdge("A", node.Edge{Target: "C", Relation: node.Reads})
	_ = g.AddEdge("B", node.Edge{Target: "C", Relation: node.Reads})
	_ = g.AddEdge("C", node.Edge{Target: "D", Relation: node.Calls})
	return g
}

// ---------------------------------------------------------------------------
// Traversal tests
// ---------------------------------------------------------------------------

func TestTraverseBasicBFS(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    10,
		TokenBudget: 100000,
	})

	if len(sub.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(sub.Nodes))
	}
	if !sub.EntryNodes["A"] {
		t.Error("A should be an entry node")
	}
	if sub.Depths["A"] != 0 {
		t.Errorf("expected depth 0 for A, got %d", sub.Depths["A"])
	}
	if sub.Depths["B"] != 1 {
		t.Errorf("expected depth 1 for B, got %d", sub.Depths["B"])
	}
	if sub.Depths["D"] != 3 {
		t.Errorf("expected depth 3 for D, got %d", sub.Depths["D"])
	}
}

func TestTraverseMaxDepthOne(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    1,
		TokenBudget: 100000,
	})

	if len(sub.Nodes) != 2 {
		t.Fatalf("expected 2 nodes (A,B), got %d: %v", len(sub.Nodes), nodeIDs(sub))
	}
}

func TestTraverseMaxDepthTwo(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    2,
		TokenBudget: 100000,
	})

	if len(sub.Nodes) != 3 {
		t.Fatalf("expected 3 nodes (A,B,C), got %d: %v", len(sub.Nodes), nodeIDs(sub))
	}
}

func TestTraverseMultipleEntryNodes(t *testing.T) {
	g := buildBranchingGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"Entry1", "Entry2"},
		MaxDepth:    10,
		TokenBudget: 100000,
	})

	if !sub.EntryNodes["Entry1"] || !sub.EntryNodes["Entry2"] {
		t.Error("both entry nodes should be marked")
	}

	// All 6 nodes should be reachable.
	if len(sub.Nodes) != 6 {
		t.Fatalf("expected 6 nodes, got %d: %v", len(sub.Nodes), nodeIDs(sub))
	}

	// C is reachable at depth 2 from both entries.
	if sub.Depths["C"] != 2 {
		t.Errorf("expected depth 2 for C, got %d", sub.Depths["C"])
	}
}

func TestTraverseEmptyGraph(t *testing.T) {
	g := graph.NewGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"nonexistent"},
		MaxDepth:    5,
		TokenBudget: 100000,
	})

	if len(sub.Nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(sub.Nodes))
	}
}

func TestTraverseNoEntryIDs(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    nil,
		MaxDepth:    5,
		TokenBudget: 100000,
	})

	if len(sub.Nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(sub.Nodes))
	}
}

func TestTraverseDepthZero(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    0,
		TokenBudget: 100000,
	})

	// Only the entry node itself, no expansion.
	if len(sub.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d: %v", len(sub.Nodes), nodeIDs(sub))
	}
	if sub.Nodes[0].ID != "A" {
		t.Errorf("expected node A, got %s", sub.Nodes[0].ID)
	}
}

func TestTraverseEntryNodesFirst(t *testing.T) {
	g := buildBranchingGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"Entry1", "Entry2"},
		MaxDepth:    2,
		TokenBudget: 100000,
	})

	// Entry nodes should appear before deeper nodes.
	if len(sub.Nodes) < 2 {
		t.Fatalf("expected at least 2 nodes, got %d", len(sub.Nodes))
	}
	for i, n := range sub.Nodes {
		if sub.EntryNodes[n.ID] {
			continue
		}
		// This non-entry node should come after all entry nodes.
		for j := i + 1; j < len(sub.Nodes); j++ {
			if sub.EntryNodes[sub.Nodes[j].ID] {
				t.Errorf("entry node %s appeared after non-entry node %s", sub.Nodes[j].ID, n.ID)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Compaction tests
// ---------------------------------------------------------------------------

func TestCompactWellFormedXML(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	result := Compact(g, sub, 100000)

	// The output should be parseable as XML.
	if err := xml.Unmarshal([]byte(result.XML), &struct {
		XMLName xml.Name `xml:"context_result"`
	}{}); err != nil {
		t.Fatalf("XML is not well-formed: %v\nXML:\n%s", err, result.XML)
	}
}

func TestCompactFullVsCompactNodes(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	result := Compact(g, sub, 100000)

	// Entry node A (depth 0) should get full <node> with context.
	if !strings.Contains(result.XML, `<node id="A"`) {
		t.Error("expected full <node> element for entry node A")
	}
	if !strings.Contains(result.XML, "<context") {
		t.Error("expected <context> in full node element")
	}

	// Deeper nodes B, C, D should get <node_compact>.
	for _, id := range []string{"B", "C", "D"} {
		if !strings.Contains(result.XML, `<node_compact id="`+id+`"`) {
			t.Errorf("expected <node_compact> for deeper node %s", id)
		}
	}

	if result.NodeCount != 4 {
		t.Errorf("expected NodeCount=4, got %d", result.NodeCount)
	}
}

func TestCompactTokenBudgetTruncation(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	// Use a very small budget to force truncation.
	result := Compact(g, sub, 50)

	if len(result.TruncatedIDs) == 0 {
		t.Error("expected some nodes to be truncated with a small budget")
	}
	if !strings.Contains(result.XML, "<truncated>") {
		t.Error("expected <truncated> section in XML")
	}
	if !strings.Contains(result.XML, `reason="budget"`) {
		t.Error("expected budget reason in truncated node_ref")
	}

	// Total included + truncated should equal subgraph node count.
	total := result.NodeCount + len(result.TruncatedIDs)
	if total != len(sub.Nodes) {
		t.Errorf("included(%d) + truncated(%d) = %d, expected %d",
			result.NodeCount, len(result.TruncatedIDs), total, len(sub.Nodes))
	}
}

func TestCompactXMLEntityEscaping(t *testing.T) {
	g := graph.NewGraph()
	_ = g.AddNode(&node.Node{
		ID:      "code",
		Type:    "function",
		Summary: "Returns a<b && c>d & e",
		Context: "if (x < 10 && y > 20) { fmt.Println(\"a & b\"); }",
	})

	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"code"},
		MaxDepth:    0,
		TokenBudget: 100000,
	})

	result := Compact(g, sub, 100000)

	// Verify the XML is well-formed despite special chars.
	if err := xml.Unmarshal([]byte(result.XML), &struct {
		XMLName xml.Name `xml:"context_result"`
	}{}); err != nil {
		t.Fatalf("XML not well-formed after escaping: %v\nXML:\n%s", err, result.XML)
	}

	// Raw <, >, & should NOT appear unescaped in content.
	// Check the context and summary sections specifically.
	if strings.Contains(result.XML, "a<b") {
		t.Error("found unescaped '<' in summary")
	}
	if strings.Contains(result.XML, "c>d") {
		t.Error("found unescaped '>' in summary")
	}
	if strings.Contains(result.XML, "x < 10") {
		t.Error("found unescaped '<' in context")
	}
	if strings.Contains(result.XML, `"a & b"`) {
		t.Error("found unescaped '&' in context")
	}
}

func TestCompactEmptySubgraph(t *testing.T) {
	g := graph.NewGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"x"},
		MaxDepth:    5,
		TokenBudget: 100000,
	})

	result := Compact(g, sub, 100000)

	if result.NodeCount != 0 {
		t.Errorf("expected 0 nodes, got %d", result.NodeCount)
	}
	if !strings.Contains(result.XML, `nodes="0"`) {
		t.Error("expected nodes=0 in XML")
	}
}

func TestCompactNilSubgraph(t *testing.T) {
	g := graph.NewGraph()
	result := Compact(g, nil, 100000)

	if result.NodeCount != 0 {
		t.Errorf("expected 0 nodes, got %d", result.NodeCount)
	}
}

func TestCompactTruncatedListsOverBudgetNodes(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	// Budget just large enough for the entry node but not all.
	result := Compact(g, sub, 80)

	for _, id := range result.TruncatedIDs {
		if !strings.Contains(result.XML, fmt.Sprintf(`<node_ref id=%q reason="budget"/>`, id)) {
			t.Errorf("truncated node %s not found in XML", id)
		}
	}
}

func TestCompactSourceRefInCompactNodes(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	result := Compact(g, sub, 100000)

	// B has source with lines.
	if !strings.Contains(result.XML, `<source path="src/b.go" lines="10-20"/>`) {
		t.Error("expected source ref with lines for node B")
	}

	// D has source without meaningful lines.
	if !strings.Contains(result.XML, `<source path="src/d.go"/>`) {
		t.Error("expected source ref without lines for node D")
	}
}

func TestCompactEdgesInFullNodes(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	result := Compact(g, sub, 100000)

	// A is the entry node and should have edges section.
	if !strings.Contains(result.XML, `<edge target="B" relation="calls"/>`) {
		t.Error("expected edge A->B in full node A")
	}
}

func TestTokenEstimationRoughlyCharsDiv4(t *testing.T) {
	content := strings.Repeat("abcd", 100) // 400 chars
	tokens := estimateTokens(content)
	if tokens != 100 {
		t.Errorf("expected 100 tokens for 400 chars, got %d", tokens)
	}

	content2 := "hello" // 5 chars -> 1 token (floor division)
	tokens2 := estimateTokens(content2)
	if tokens2 != 1 {
		t.Errorf("expected 1 token for 5 chars, got %d", tokens2)
	}

	content3 := "" // 0 chars -> 0 tokens
	tokens3 := estimateTokens(content3)
	if tokens3 != 0 {
		t.Errorf("expected 0 tokens for empty string, got %d", tokens3)
	}
}

func TestCompactTokenEstimateField(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	result := Compact(g, sub, 100000)

	// Token estimate should be roughly len(XML)/4.
	expected := len(result.XML) / 4
	if result.TokenEstimate != expected {
		t.Errorf("expected TokenEstimate=%d, got %d", expected, result.TokenEstimate)
	}
}

func TestCompactDefaultModeIsAdjacency(t *testing.T) {
	g := buildLinearGraph()
	sub := Traverse(g, TraversalConfig{
		EntryIDs:    []string{"A"},
		MaxDepth:    1,
		TokenBudget: 100000,
	})

	// The traversal should default to adjacency mode.
	// Verify it ran without error and produced results.
	if sub == nil || len(sub.Nodes) == 0 {
		t.Fatal("expected non-empty subgraph with default mode")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func nodeIDs(sub *Subgraph) []string {
	ids := make([]string, len(sub.Nodes))
	for i, n := range sub.Nodes {
		ids[i] = n.ID
	}
	return ids
}

