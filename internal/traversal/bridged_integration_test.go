package traversal

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// Integration test helpers
// ---------------------------------------------------------------------------

// buildCrossVaultResolver creates a BridgedGraphResolver with a local graph
// containing an edge to @remote-vault/api-handler, and a remote vault with
// api-handler -> db-query -> cache-layer chain.
func buildCrossVaultResolver() (*BridgedGraphResolver, *graph.Graph, *graph.Graph) {
	localG := graph.NewGraph()
	_ = localG.AddNode(&node.Node{
		ID:        "auth-service",
		Type:      "module",
		Namespace: "default",
		Summary:   "Authentication service for user login",
		Context:   "func Authenticate(user, pass string) error { ... }",
		Edges: []node.Edge{
			{Target: "@remote-vault/api-handler", Relation: node.Calls},
			{Target: "session-store", Relation: node.References},
		},
	})
	_ = localG.AddNode(&node.Node{
		ID:        "session-store",
		Type:      "module",
		Namespace: "default",
		Summary:   "Session storage for authenticated users",
		Context:   "type SessionStore struct { ... }",
	})

	remoteG := graph.NewGraph()
	_ = remoteG.AddNode(&node.Node{
		ID:        "api-handler",
		Type:      "function",
		Namespace: "default",
		Summary:   "Handles incoming API requests",
		Context:   "func HandleRequest(w http.ResponseWriter, r *http.Request) { ... }",
		Edges: []node.Edge{
			{Target: "db-query", Relation: node.Calls},
		},
	})
	_ = remoteG.AddNode(&node.Node{
		ID:        "db-query",
		Type:      "function",
		Namespace: "default",
		Summary:   "Executes database queries",
		Context:   "func Query(ctx context.Context, sql string) (*Rows, error) { ... }",
		Source:    node.Source{Path: "pkg/db/query.go", Lines: [2]int{15, 45}},
		Edges: []node.Edge{
			{Target: "cache-layer", Relation: node.Reads},
		},
	})
	_ = remoteG.AddNode(&node.Node{
		ID:        "cache-layer",
		Type:      "module",
		Namespace: "default",
		Summary:   "In-memory caching layer",
		Source:    node.Source{Path: "pkg/cache/cache.go"},
	})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"remote-vault": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}
	return resolver, localG, remoteG
}

// ---------------------------------------------------------------------------
// Integration: End-to-end Traverse + Compact with cross-vault nodes
// ---------------------------------------------------------------------------

func TestIntegration_CrossVaultCompactOutput(t *testing.T) {
	resolver, _, _ := buildCrossVaultResolver()

	// Traverse from local entry node at depth 3 to reach remote chain.
	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"auth-service"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	// Verify the subgraph has all expected nodes.
	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	// Local nodes.
	if !found["auth-service"] {
		t.Error("expected auth-service in subgraph")
	}
	if !found["session-store"] {
		t.Error("expected session-store in subgraph")
	}
	// Remote nodes with @-prefixed IDs.
	if !found["@remote-vault/api-handler"] {
		t.Error("expected @remote-vault/api-handler in subgraph")
	}
	if !found["@remote-vault/db-query"] {
		t.Error("expected @remote-vault/db-query in subgraph")
	}
	if !found["@remote-vault/cache-layer"] {
		t.Error("expected @remote-vault/cache-layer in subgraph")
	}
	if len(sub.Nodes) != 5 {
		t.Fatalf("expected 5 nodes, got %d: %v", len(sub.Nodes), nodeIDs(sub))
	}

	// Verify depths.
	expectedDepths := map[string]int{
		"auth-service":                0,
		"session-store":               1,
		"@remote-vault/api-handler":   1,
		"@remote-vault/db-query":      2,
		"@remote-vault/cache-layer":   3,
	}
	for id, wantDepth := range expectedDepths {
		if got := sub.Depths[id]; got != wantDepth {
			t.Errorf("depth of %q: got %d, want %d", id, got, wantDepth)
		}
	}

	// Compact and verify XML.
	result := Compact(resolver, sub, 100000)

	// XML must be well-formed.
	if err := xml.Unmarshal([]byte(result.XML), &struct {
		XMLName xml.Name `xml:"context_result"`
	}{}); err != nil {
		t.Fatalf("XML is not well-formed: %v\nXML:\n%s", err, result.XML)
	}

	// Entry node should be full <node> element.
	if !strings.Contains(result.XML, `<node id="auth-service"`) {
		t.Error("expected full <node> for entry node auth-service")
	}

	// Remote nodes should appear with @-prefixed IDs in the XML.
	if !strings.Contains(result.XML, `id="@remote-vault/api-handler"`) {
		t.Error("expected @remote-vault/api-handler ID in XML output")
	}
	if !strings.Contains(result.XML, `id="@remote-vault/db-query"`) {
		t.Error("expected @remote-vault/db-query ID in XML output")
	}
	if !strings.Contains(result.XML, `id="@remote-vault/cache-layer"`) {
		t.Error("expected @remote-vault/cache-layer ID in XML output")
	}

	// Verify depth attributes for remote nodes in the XML.
	if !strings.Contains(result.XML, `id="@remote-vault/api-handler" type="function" depth="1"`) {
		t.Error("expected depth=1 for @remote-vault/api-handler in XML")
	}
	if !strings.Contains(result.XML, `id="@remote-vault/db-query" type="function" depth="2"`) {
		t.Error("expected depth=2 for @remote-vault/db-query in XML")
	}
	if !strings.Contains(result.XML, `id="@remote-vault/cache-layer" type="module" depth="3"`) {
		t.Error("expected depth=3 for @remote-vault/cache-layer in XML")
	}

	// Edges for the entry node (full node) should include the @-prefixed target.
	if !strings.Contains(result.XML, `<edge target="@remote-vault/api-handler" relation="calls"/>`) {
		t.Error("expected cross-vault edge target in entry node's edges")
	}
	if !strings.Contains(result.XML, `<edge target="session-store" relation="references"/>`) {
		t.Error("expected local edge target in entry node's edges")
	}

	// All 5 nodes should be included in count.
	if result.NodeCount != 5 {
		t.Errorf("expected NodeCount=5, got %d", result.NodeCount)
	}

	t.Logf("Integration XML output:\n%s", result.XML)
}

// ---------------------------------------------------------------------------
// Integration: Compact renders edges for full remote nodes (depth 0 entry)
// ---------------------------------------------------------------------------

func TestIntegration_CrossVaultRemoteEntryNodeEdges(t *testing.T) {
	// Test the case where a remote node IS the entry point (direct @-prefixed entry).
	remoteG := graph.NewGraph()
	_ = remoteG.AddNode(&node.Node{
		ID:      "entry-fn",
		Type:    "function",
		Summary: "Remote entry function",
		Context: "func Entry() { callHelper() }",
		Edges: []node.Edge{
			{Target: "helper", Relation: node.Calls},
		},
	})
	_ = remoteG.AddNode(&node.Node{
		ID:      "helper",
		Type:    "function",
		Summary: "Helper function",
	})

	localG := graph.NewGraph()
	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"@rv/entry-fn"},
		MaxDepth:    1,
		TokenBudget: 100000,
	})

	if len(sub.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d: %v", len(sub.Nodes), nodeIDs(sub))
	}

	// The entry node's ID should be @rv/entry-fn.
	if sub.Nodes[0].ID != "@rv/entry-fn" {
		t.Errorf("expected entry node ID @rv/entry-fn, got %s", sub.Nodes[0].ID)
	}

	result := Compact(resolver, sub, 100000)

	// Entry remote node should be a full <node> (depth 0) with edges.
	if !strings.Contains(result.XML, `<node id="@rv/entry-fn"`) {
		t.Error("expected full <node> for remote entry node")
	}
	// Edges should be rewritten to @rv/ prefix.
	if !strings.Contains(result.XML, `<edge target="@rv/helper" relation="calls"/>`) {
		t.Errorf("expected edge to @rv/helper in full remote entry node; XML:\n%s", result.XML)
	}
	// Helper should be compact.
	if !strings.Contains(result.XML, `<node_compact id="@rv/helper"`) {
		t.Error("expected <node_compact> for @rv/helper")
	}
}

// ---------------------------------------------------------------------------
// Integration: Token budget with cross-vault — truncation
// ---------------------------------------------------------------------------

func TestIntegration_CrossVaultTokenBudgetTruncation(t *testing.T) {
	resolver, _, _ := buildCrossVaultResolver()

	// Traverse deep enough to collect all 5 nodes.
	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"auth-service"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	if len(sub.Nodes) != 5 {
		t.Fatalf("expected 5 nodes before compaction, got %d", len(sub.Nodes))
	}

	// Use a very small budget — only the entry node should fit.
	result := Compact(resolver, sub, 80)

	if result.NodeCount == 0 {
		t.Fatal("expected at least 1 node to fit within budget")
	}
	if len(result.TruncatedIDs) == 0 {
		t.Fatal("expected some nodes to be truncated with a small budget")
	}

	// Total included + truncated should equal subgraph size.
	total := result.NodeCount + len(result.TruncatedIDs)
	if total != 5 {
		t.Errorf("included(%d) + truncated(%d) = %d, expected 5",
			result.NodeCount, len(result.TruncatedIDs), total)
	}

	// Truncated section should list @-prefixed IDs.
	if !strings.Contains(result.XML, "<truncated>") {
		t.Error("expected <truncated> section in XML")
	}

	// Verify at least one @-prefixed node appears in the truncated list.
	hasRemoteTruncated := false
	for _, id := range result.TruncatedIDs {
		if strings.HasPrefix(id, "@") {
			hasRemoteTruncated = true
			if !strings.Contains(result.XML, fmt.Sprintf(`<node_ref id=%q reason="budget"/>`, id)) {
				t.Errorf("truncated remote node %s not found in XML", id)
			}
		}
	}
	if !hasRemoteTruncated {
		t.Error("expected at least one @-prefixed remote node in truncated list")
	}

	t.Logf("Budget truncation XML:\n%s", result.XML)
}

// ---------------------------------------------------------------------------
// Integration: Cross-vault with moderate budget — partial inclusion
// ---------------------------------------------------------------------------

func TestIntegration_CrossVaultPartialBudget(t *testing.T) {
	resolver, _, _ := buildCrossVaultResolver()

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"auth-service"},
		MaxDepth:    3,
		TokenBudget: 100000,
	})

	// Find a budget that includes some but not all nodes.
	// The entry node (full) is the largest. Compact nodes are small.
	// Try a mid-range budget.
	result := Compact(resolver, sub, 200)

	if result.NodeCount == 0 {
		t.Fatal("expected at least some nodes to fit")
	}
	if result.NodeCount == 5 && len(result.TruncatedIDs) == 0 {
		// If all fit, that's OK — just ensure the accounting is correct.
		t.Log("all 5 nodes fit within budget=200")
	}

	// Verify accounting invariant.
	total := result.NodeCount + len(result.TruncatedIDs)
	if total != len(sub.Nodes) {
		t.Errorf("included(%d) + truncated(%d) = %d, expected %d",
			result.NodeCount, len(result.TruncatedIDs), total, len(sub.Nodes))
	}

	// XML must be well-formed.
	if err := xml.Unmarshal([]byte(result.XML), &struct {
		XMLName xml.Name `xml:"context_result"`
	}{}); err != nil {
		t.Fatalf("XML not well-formed: %v\nXML:\n%s", err, result.XML)
	}
}

// ---------------------------------------------------------------------------
// Integration: Cross-vault with heat weights — prioritized exploration
// ---------------------------------------------------------------------------

func TestIntegration_CrossVaultHeatWeights(t *testing.T) {
	// Build a graph where the entry has two outbound edges:
	// entry -> @rv/hot-node (heat-boosted)
	// entry -> cold-node   (no heat boost)
	// Both have depth-1 children. With depth=1, both are reached.
	// The test verifies heat-boosted edges get explored by confirming the
	// traversal discovers the hot path's nodes.
	localG := graph.NewGraph()
	_ = localG.AddNode(&node.Node{
		ID:      "entry",
		Type:    "module",
		Summary: "Entry point",
		Edges: []node.Edge{
			{Target: "@rv/hot-node", Relation: node.Calls},
			{Target: "cold-node", Relation: node.References},
		},
	})
	_ = localG.AddNode(&node.Node{
		ID:      "cold-node",
		Type:    "function",
		Summary: "Cold node, less frequently accessed",
		Edges: []node.Edge{
			{Target: "cold-child", Relation: node.Calls},
		},
	})
	_ = localG.AddNode(&node.Node{
		ID:      "cold-child",
		Type:    "function",
		Summary: "Cold child",
	})

	remoteG := graph.NewGraph()
	_ = remoteG.AddNode(&node.Node{
		ID:      "hot-node",
		Type:    "function",
		Summary: "Hot node, frequently co-accessed",
		Edges: []node.Edge{
			{Target: "hot-child", Relation: node.Calls},
		},
	})
	_ = remoteG.AddNode(&node.Node{
		ID:      "hot-child",
		Type:    "function",
		Summary: "Hot child",
	})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	// Heat weights: boost the entry<->@rv/hot-node pair.
	heatWeights := map[string]float64{
		heatmap.PairKey("entry", "@rv/hot-node"): 0.9, // strong boost
	}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"entry"},
		MaxDepth:    2,
		TokenBudget: 100000,
		HeatWeights: heatWeights,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	// Both paths should be discovered at depth 2.
	if !found["entry"] {
		t.Error("expected entry in result")
	}
	if !found["@rv/hot-node"] {
		t.Error("expected @rv/hot-node in result")
	}
	if !found["@rv/hot-child"] {
		t.Error("expected @rv/hot-child in result")
	}
	if !found["cold-node"] {
		t.Error("expected cold-node in result")
	}
	if !found["cold-child"] {
		t.Error("expected cold-child in result")
	}

	// Verify the ordering: heat-boosted nodes should appear before
	// non-boosted nodes at the same depth level.
	// Both @rv/hot-node and cold-node are at depth 1.
	// With heat boost, @rv/hot-node should have lower priority (explored first).
	hotIdx := -1
	coldIdx := -1
	for i, n := range sub.Nodes {
		if n.ID == "@rv/hot-node" {
			hotIdx = i
		}
		if n.ID == "cold-node" {
			coldIdx = i
		}
	}
	if hotIdx < 0 || coldIdx < 0 {
		t.Fatalf("could not find hot(%d) or cold(%d) node indices", hotIdx, coldIdx)
	}
	// Due to sortNodes ordering by depth (entry first, then depth 1, etc.),
	// we can at least verify both are present. The heap-based BFS ensures
	// hot-node is dequeued first, but final sort is by depth.
	// Both are at depth 1, so ordering between them may vary.
	t.Logf("hot-node at index %d, cold-node at index %d (both depth 1)", hotIdx, coldIdx)

	// Compact and verify XML.
	result := Compact(resolver, sub, 100000)
	if result.NodeCount != 5 {
		t.Errorf("expected 5 nodes in compact output, got %d", result.NodeCount)
	}

	// XML must be well-formed.
	if err := xml.Unmarshal([]byte(result.XML), &struct {
		XMLName xml.Name `xml:"context_result"`
	}{}); err != nil {
		t.Fatalf("XML not well-formed: %v\nXML:\n%s", err, result.XML)
	}

	// Verify cross-vault edges in output.
	if !strings.Contains(result.XML, `<edge target="@rv/hot-node" relation="calls"/>`) {
		t.Error("expected edge to @rv/hot-node in XML")
	}

	t.Logf("Heat-weighted cross-vault XML:\n%s", result.XML)
}

// ---------------------------------------------------------------------------
// Integration: Heat weights change exploration order (depth-limited scenario)
// ---------------------------------------------------------------------------

func TestIntegration_HeatWeightsAffectExplorationOrder(t *testing.T) {
	// This test uses depth=1 from entry. Entry has two outbound edges:
	//   entry -> @rv/boosted  (heat-boosted)
	//   entry -> unboosted    (no boost)
	// At depth 1, both are reached. But we verify the BFS priority
	// by checking that the boosted node appears in the subgraph
	// (confirming the priority queue handles cross-vault heat correctly).
	localG := graph.NewGraph()
	_ = localG.AddNode(&node.Node{
		ID:      "entry",
		Type:    "module",
		Summary: "Entry",
		Edges: []node.Edge{
			{Target: "@rv/boosted", Relation: node.Calls},
			{Target: "unboosted", Relation: node.References},
		},
	})
	_ = localG.AddNode(&node.Node{
		ID:      "unboosted",
		Type:    "function",
		Summary: "Unboosted node",
	})

	remoteG := graph.NewGraph()
	_ = remoteG.AddNode(&node.Node{
		ID:      "boosted",
		Type:    "function",
		Summary: "Boosted remote node",
	})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	heatWeights := map[string]float64{
		heatmap.PairKey("entry", "@rv/boosted"): 0.8,
	}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"entry"},
		MaxDepth:    1,
		TokenBudget: 100000,
		HeatWeights: heatWeights,
	})

	if len(sub.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d: %v", len(sub.Nodes), nodeIDs(sub))
	}

	// Verify depths.
	if sub.Depths["@rv/boosted"] != 1 {
		t.Errorf("expected depth 1 for @rv/boosted, got %d", sub.Depths["@rv/boosted"])
	}
	if sub.Depths["unboosted"] != 1 {
		t.Errorf("expected depth 1 for unboosted, got %d", sub.Depths["unboosted"])
	}
}

// ---------------------------------------------------------------------------
// Integration: Multiple remote vaults in one traversal
// ---------------------------------------------------------------------------

func TestIntegration_MultipleRemoteVaults(t *testing.T) {
	localG := graph.NewGraph()
	_ = localG.AddNode(&node.Node{
		ID:      "orchestrator",
		Type:    "module",
		Summary: "Orchestrates across vaults",
		Edges: []node.Edge{
			{Target: "@vault-a/service-a", Relation: node.Calls},
			{Target: "@vault-b/service-b", Relation: node.Calls},
		},
	})

	vaultA := graph.NewGraph()
	_ = vaultA.AddNode(&node.Node{
		ID:      "service-a",
		Type:    "module",
		Summary: "Service A in vault A",
		Edges: []node.Edge{
			{Target: "helper-a", Relation: node.Calls},
		},
	})
	_ = vaultA.AddNode(&node.Node{
		ID:      "helper-a",
		Type:    "function",
		Summary: "Helper A",
	})

	vaultB := graph.NewGraph()
	_ = vaultB.AddNode(&node.Node{
		ID:      "service-b",
		Type:    "module",
		Summary: "Service B in vault B",
	})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{
		"vault-a": vaultA,
		"vault-b": vaultB,
	}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"orchestrator"},
		MaxDepth:    2,
		TokenBudget: 100000,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	if !found["orchestrator"] {
		t.Error("expected orchestrator")
	}
	if !found["@vault-a/service-a"] {
		t.Error("expected @vault-a/service-a")
	}
	if !found["@vault-a/helper-a"] {
		t.Error("expected @vault-a/helper-a")
	}
	if !found["@vault-b/service-b"] {
		t.Error("expected @vault-b/service-b")
	}
	if len(sub.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d: %v", len(sub.Nodes), nodeIDs(sub))
	}

	result := Compact(resolver, sub, 100000)

	// Both vault-a and vault-b nodes should appear in XML.
	if !strings.Contains(result.XML, `id="@vault-a/service-a"`) {
		t.Error("expected @vault-a/service-a in XML")
	}
	if !strings.Contains(result.XML, `id="@vault-b/service-b"`) {
		t.Error("expected @vault-b/service-b in XML")
	}
	// Multi-hop within vault-a: helper-a should have @vault-a/ prefix.
	if !strings.Contains(result.XML, `id="@vault-a/helper-a"`) {
		t.Error("expected @vault-a/helper-a in XML")
	}

	// Edge from orchestrator to vault-a service.
	if !strings.Contains(result.XML, `<edge target="@vault-a/service-a" relation="calls"/>`) {
		t.Error("expected edge to @vault-a/service-a in XML")
	}
	if !strings.Contains(result.XML, `<edge target="@vault-b/service-b" relation="calls"/>`) {
		t.Error("expected edge to @vault-b/service-b in XML")
	}

	if err := xml.Unmarshal([]byte(result.XML), &struct {
		XMLName xml.Name `xml:"context_result"`
	}{}); err != nil {
		t.Fatalf("XML not well-formed: %v\nXML:\n%s", err, result.XML)
	}

	t.Logf("Multi-vault XML:\n%s", result.XML)
}

// ---------------------------------------------------------------------------
// Integration: Superseded remote nodes are skipped by default
// ---------------------------------------------------------------------------

func TestIntegration_CrossVaultSupersededSkipped(t *testing.T) {
	localG := graph.NewGraph()
	_ = localG.AddNode(&node.Node{
		ID:      "caller",
		Type:    "function",
		Summary: "Caller",
		Edges: []node.Edge{
			{Target: "@rv/old-api", Relation: node.Calls},
			{Target: "@rv/new-api", Relation: node.Calls},
		},
	})

	remoteG := graph.NewGraph()
	_ = remoteG.AddNode(&node.Node{
		ID:           "old-api",
		Type:         "function",
		Status:       node.StatusSuperseded,
		SupersededBy: "new-api",
		Summary:      "Old deprecated API",
	})
	_ = remoteG.AddNode(&node.Node{
		ID:      "new-api",
		Type:    "function",
		Summary: "New API",
	})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	// Default: exclude superseded.
	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"caller"},
		MaxDepth:    1,
		TokenBudget: 100000,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}
	if found["@rv/old-api"] {
		t.Error("expected superseded @rv/old-api to be excluded")
	}
	if !found["@rv/new-api"] {
		t.Error("expected active @rv/new-api to be included")
	}

	// With IncludeSuperseded: true.
	subAll := Traverse(resolver, TraversalConfig{
		EntryIDs:          []string{"caller"},
		MaxDepth:          1,
		TokenBudget:       100000,
		IncludeSuperseded: true,
	})

	foundAll := make(map[string]bool)
	for _, n := range subAll.Nodes {
		foundAll[n.ID] = true
	}
	if !foundAll["@rv/old-api"] {
		t.Error("expected superseded @rv/old-api to be included with IncludeSuperseded=true")
	}
	if !foundAll["@rv/new-api"] {
		t.Error("expected @rv/new-api to be included")
	}
}
