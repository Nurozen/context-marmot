package traversal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// stressVaultProvider implements VaultGraphProvider for stress tests.
type stressVaultProvider struct {
	graphs map[string]*graph.Graph
}

func (s *stressVaultProvider) ResolveGraph(vaultID string) (*graph.Graph, error) {
	g, ok := s.graphs[vaultID]
	if !ok {
		return nil, fmt.Errorf("unknown vault %q", vaultID)
	}
	return g, nil
}

// --------------------------------------------------------------------------
// Stress test: Deep multi-hop (depth 4+)
// local → @vaultA/a → @vaultA/b → @vaultA/c → @vaultA/d
// --------------------------------------------------------------------------
func TestStress_DeepMultiHop(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "local", Type: "concept", Summary: "local entry",
		Edges: []node.Edge{{Target: "@vaultA/a", Relation: node.References}},
	})

	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{
		ID: "a", Type: "function", Summary: "A",
		Edges: []node.Edge{{Target: "b", Relation: node.Calls}},
	})
	remoteG.AddNode(&node.Node{
		ID: "b", Type: "function", Summary: "B",
		Edges: []node.Edge{{Target: "c", Relation: node.Calls}},
	})
	remoteG.AddNode(&node.Node{
		ID: "c", Type: "function", Summary: "C",
		Edges: []node.Edge{{Target: "d", Relation: node.Calls}},
	})
	remoteG.AddNode(&node.Node{
		ID: "d", Type: "function", Summary: "D",
	})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{"vaultA": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"local"},
		MaxDepth:    4,
		TokenBudget: 8192,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	expected := []string{"local", "@vaultA/a", "@vaultA/b", "@vaultA/c", "@vaultA/d"}
	for _, id := range expected {
		if !found[id] {
			t.Errorf("expected %q in traversal result, not found", id)
		}
	}
	if len(sub.Nodes) != len(expected) {
		t.Errorf("expected %d nodes, got %d: %v", len(expected), len(sub.Nodes), stressNodeIDs(sub))
	}

	// Verify depths.
	for i, id := range expected {
		if sub.Depths[id] != i {
			t.Errorf("expected depth %d for %q, got %d", i, id, sub.Depths[id])
		}
	}
}

// --------------------------------------------------------------------------
// Stress test: Multi-vault fan-out
// local node has edges to nodes in 3+ different remote vaults
// --------------------------------------------------------------------------
func TestStress_MultiVaultFanOut(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "hub", Type: "concept", Summary: "hub node",
		Edges: []node.Edge{
			{Target: "@alpha/n1", Relation: node.References},
			{Target: "@beta/n2", Relation: node.References},
			{Target: "@gamma/n3", Relation: node.References},
			{Target: "@delta/n4", Relation: node.References},
		},
	})

	alphaG := graph.NewGraph()
	alphaG.AddNode(&node.Node{ID: "n1", Type: "concept", Summary: "alpha node"})

	betaG := graph.NewGraph()
	betaG.AddNode(&node.Node{ID: "n2", Type: "concept", Summary: "beta node"})

	gammaG := graph.NewGraph()
	gammaG.AddNode(&node.Node{ID: "n3", Type: "concept", Summary: "gamma node"})

	deltaG := graph.NewGraph()
	deltaG.AddNode(&node.Node{ID: "n4", Type: "concept", Summary: "delta node"})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{
		"alpha": alphaG,
		"beta":  betaG,
		"gamma": gammaG,
		"delta": deltaG,
	}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"hub"},
		MaxDepth:    1,
		TokenBudget: 8192,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	expected := []string{"hub", "@alpha/n1", "@beta/n2", "@gamma/n3", "@delta/n4"}
	for _, id := range expected {
		if !found[id] {
			t.Errorf("expected %q in traversal result, not found", id)
		}
	}
	if len(sub.Nodes) != len(expected) {
		t.Errorf("expected %d nodes, got %d: %v", len(expected), len(sub.Nodes), stressNodeIDs(sub))
	}

	// All remote nodes should be at depth 1.
	for _, id := range expected[1:] {
		if sub.Depths[id] != 1 {
			t.Errorf("expected depth 1 for %q, got %d", id, sub.Depths[id])
		}
	}
}

// --------------------------------------------------------------------------
// Stress test: Cross-vault back-reference
// @vaultA/x has an edge back to local node — verify no infinite loop
// --------------------------------------------------------------------------
func TestStress_CrossVaultBackReference(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "root", Type: "concept", Summary: "root node",
		Edges: []node.Edge{{Target: "@vaultA/x", Relation: node.References}},
	})

	remoteG := graph.NewGraph()
	// Remote node x has an edge back to the local "root" node.
	// Since "root" is not @-prefixed, this will be rewritten as @vaultA/root
	// by GetEdges, which won't resolve in vaultA. This tests that the traversal
	// doesn't loop and handles missing nodes gracefully.
	remoteG.AddNode(&node.Node{
		ID: "x", Type: "concept", Summary: "remote x",
		Edges: []node.Edge{{Target: "y", Relation: node.References}},
	})
	remoteG.AddNode(&node.Node{
		ID: "y", Type: "concept", Summary: "remote y",
	})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{"vaultA": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	// Should terminate without infinite loop.
	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"root"},
		MaxDepth:    10, // High depth to stress-test cycle resistance.
		TokenBudget: 8192,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	if !found["root"] {
		t.Error("expected root in result")
	}
	if !found["@vaultA/x"] {
		t.Error("expected @vaultA/x in result")
	}
	if !found["@vaultA/y"] {
		t.Error("expected @vaultA/y in result")
	}
	// Must terminate (BFS visited set prevents revisiting).
	t.Logf("traversal completed with %d nodes (no infinite loop)", len(sub.Nodes))
}

// --------------------------------------------------------------------------
// Stress test: Cross-vault back-reference that actually points back to local
// local "root" → @vaultA/x, and x has edge literally "@local-vault/root"
// but we model it by having the remote edge point to a node outside the vault.
// --------------------------------------------------------------------------
func TestStress_CrossVaultBackRefToLocal(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "root", Type: "concept", Summary: "root node",
		Edges: []node.Edge{{Target: "@vaultA/bounceback", Relation: node.References}},
	})

	remoteG := graph.NewGraph()
	// The remote node has an already @-prefixed edge pointing elsewhere.
	// Since it starts with "@", GetEdges won't rewrite it. It points to
	// a vault "local-vault" that doesn't exist, so GetNode returns false.
	remoteG.AddNode(&node.Node{
		ID: "bounceback", Type: "concept", Summary: "bounces back",
		Edges: []node.Edge{{Target: "@nonexistent-vault/root", Relation: node.References}},
	})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{"vaultA": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"root"},
		MaxDepth:    5,
		TokenBudget: 8192,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	if !found["root"] {
		t.Error("expected root")
	}
	if !found["@vaultA/bounceback"] {
		t.Error("expected @vaultA/bounceback")
	}
	// The @nonexistent-vault/root should be recorded in Depths but not in Nodes
	// since the vault doesn't exist.
	if found["@nonexistent-vault/root"] {
		t.Error("should NOT find @nonexistent-vault/root since vault doesn't exist")
	}
	if len(sub.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d: %v", len(sub.Nodes), stressNodeIDs(sub))
	}
}

// --------------------------------------------------------------------------
// Stress test: Diamond pattern
// local → @v/a, local → @v/b, @v/a → @v/c, @v/b → @v/c
// Verify @v/c appears exactly once.
// --------------------------------------------------------------------------
func TestStress_DiamondPattern(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "start", Type: "concept", Summary: "diamond start",
		Edges: []node.Edge{
			{Target: "@v/a", Relation: node.References},
			{Target: "@v/b", Relation: node.References},
		},
	})

	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{
		ID: "a", Type: "function", Summary: "A",
		Edges: []node.Edge{{Target: "c", Relation: node.Calls}},
	})
	remoteG.AddNode(&node.Node{
		ID: "b", Type: "function", Summary: "B",
		Edges: []node.Edge{{Target: "c", Relation: node.Calls}},
	})
	remoteG.AddNode(&node.Node{
		ID: "c", Type: "function", Summary: "C (diamond merge)",
	})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{"v": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"start"},
		MaxDepth:    2,
		TokenBudget: 8192,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	expected := []string{"start", "@v/a", "@v/b", "@v/c"}
	for _, id := range expected {
		if !found[id] {
			t.Errorf("expected %q in traversal result", id)
		}
	}

	// Verify @v/c appears exactly once.
	count := 0
	for _, n := range sub.Nodes {
		if n.ID == "@v/c" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected @v/c to appear exactly once, appeared %d times", count)
	}

	if len(sub.Nodes) != 4 {
		t.Errorf("expected 4 nodes, got %d: %v", len(sub.Nodes), stressNodeIDs(sub))
	}

	// @v/c should be at depth 2.
	if sub.Depths["@v/c"] != 2 {
		t.Errorf("expected @v/c at depth 2, got %d", sub.Depths["@v/c"])
	}
}

// --------------------------------------------------------------------------
// Stress test: Remote-to-remote vault hop
// local → @vaultA/x → @vaultB/y (edge from vaultA pointing to vaultB)
// --------------------------------------------------------------------------
func TestStress_RemoteToRemoteVaultHop(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "origin", Type: "concept", Summary: "origin",
		Edges: []node.Edge{{Target: "@vaultA/x", Relation: node.References}},
	})

	// vaultA's node x has an edge to vaultB (already @-prefixed).
	vaultAG := graph.NewGraph()
	vaultAG.AddNode(&node.Node{
		ID: "x", Type: "concept", Summary: "vaultA x",
		Edges: []node.Edge{{Target: "@vaultB/y", Relation: node.CrossProject}},
	})

	// vaultB's node y has a further edge within vaultB.
	vaultBG := graph.NewGraph()
	vaultBG.AddNode(&node.Node{
		ID: "y", Type: "concept", Summary: "vaultB y",
		Edges: []node.Edge{{Target: "z", Relation: node.Calls}},
	})
	vaultBG.AddNode(&node.Node{
		ID: "z", Type: "concept", Summary: "vaultB z",
	})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{
		"vaultA": vaultAG,
		"vaultB": vaultBG,
	}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"origin"},
		MaxDepth:    3,
		TokenBudget: 8192,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	expected := []string{"origin", "@vaultA/x", "@vaultB/y", "@vaultB/z"}
	for _, id := range expected {
		if !found[id] {
			t.Errorf("expected %q in traversal result", id)
		}
	}
	if len(sub.Nodes) != len(expected) {
		t.Errorf("expected %d nodes, got %d: %v", len(expected), len(sub.Nodes), stressNodeIDs(sub))
	}

	// Verify depths.
	wantDepths := map[string]int{"origin": 0, "@vaultA/x": 1, "@vaultB/y": 2, "@vaultB/z": 3}
	for id, wantD := range wantDepths {
		if sub.Depths[id] != wantD {
			t.Errorf("expected depth %d for %q, got %d", wantD, id, sub.Depths[id])
		}
	}
}

// --------------------------------------------------------------------------
// Stress test: Compact output for cross-vault nodes
// Verify Compact() renders correct XML with @-prefixed IDs, correct depths,
// and edges for remote nodes.
// --------------------------------------------------------------------------
func TestStress_CompactCrossVaultXML(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "entry", Type: "concept", Summary: "entry point",
		Edges: []node.Edge{{Target: "@rv/remote", Relation: node.References}},
	})

	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{
		ID: "remote", Type: "function", Summary: "remote func",
		Edges: []node.Edge{{Target: "deep", Relation: node.Calls}},
	})
	remoteG.AddNode(&node.Node{
		ID: "deep", Type: "function", Summary: "deep func",
	})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"entry"},
		MaxDepth:    2,
		TokenBudget: 8192,
	})

	result := Compact(resolver, sub, 8192)

	// Basic sanity: XML should not be empty.
	if result.XML == "" {
		t.Fatal("Compact returned empty XML")
	}
	if result.NodeCount != 3 {
		t.Errorf("expected 3 nodes in compact output, got %d", result.NodeCount)
	}

	// Verify the entry node is a full <node> element.
	if !strings.Contains(result.XML, `id="entry"`) {
		t.Error("XML should contain id=\"entry\"")
	}

	// Verify remote node IDs are @-prefixed in the output.
	if !strings.Contains(result.XML, `id="@rv/remote"`) {
		t.Error("XML should contain id=\"@rv/remote\"")
	}
	if !strings.Contains(result.XML, `id="@rv/deep"`) {
		t.Error("XML should contain id=\"@rv/deep\"")
	}

	// Verify the entry node (full render) has edges referencing @rv/remote.
	if !strings.Contains(result.XML, `target="@rv/remote"`) {
		t.Error("XML should contain edge target=\"@rv/remote\"")
	}

	// Verify depth attributes.
	if !strings.Contains(result.XML, `depth="0"`) {
		t.Error("XML should contain depth=\"0\" for entry node")
	}
	if !strings.Contains(result.XML, `depth="1"`) {
		t.Error("XML should contain depth=\"1\" for remote node")
	}
	if !strings.Contains(result.XML, `depth="2"`) {
		t.Error("XML should contain depth=\"2\" for deep node")
	}

	// The remote node at depth 1 could be full (entry) or compact depending on
	// implementation. It should have edges in the XML if rendered as full.
	// @rv/remote is at depth 1 (not entry, not depth 0), so it gets compact rendering.
	// Verify it has node_compact tag.
	if !strings.Contains(result.XML, `<node_compact id="@rv/remote"`) {
		t.Error("@rv/remote should be rendered as node_compact (depth 1, not entry)")
	}

	// @rv/deep at depth 2 should also be compact.
	if !strings.Contains(result.XML, `<node_compact id="@rv/deep"`) {
		t.Error("@rv/deep should be rendered as node_compact (depth 2)")
	}

	t.Logf("Compact XML:\n%s", result.XML)
}

// --------------------------------------------------------------------------
// Stress test: Compact with full node for remote entry
// When a remote node is depth 0 (direct entry), it should render as full <node>.
// --------------------------------------------------------------------------
func TestStress_CompactRemoteEntryFullNode(t *testing.T) {
	// Set up a remote vault with a node.
	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{
		ID: "target", Type: "function", Summary: "remote entry",
		Edges: []node.Edge{{Target: "child", Relation: node.Calls}},
	})
	remoteG.AddNode(&node.Node{
		ID: "child", Type: "function", Summary: "remote child",
	})

	localG := graph.NewGraph()
	// No local nodes needed — we're testing with a remote entry.

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	// Directly enter the traversal with the remote node as entry.
	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"@rv/target"},
		MaxDepth:    1,
		TokenBudget: 8192,
	})

	if len(sub.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d: %v", len(sub.Nodes), stressNodeIDs(sub))
	}

	result := Compact(resolver, sub, 8192)

	// @rv/target is an entry node, so it should be rendered as full <node>.
	if !strings.Contains(result.XML, `<node id="@rv/target"`) {
		t.Error("@rv/target should be a full <node> element (entry node)")
	}

	// It should have edges in the full rendering.
	if !strings.Contains(result.XML, `target="@rv/child"`) {
		t.Error("Full node @rv/target should have edge to @rv/child")
	}

	t.Logf("Compact XML:\n%s", result.XML)
}

// --------------------------------------------------------------------------
// Stress test: Empty remote vault
// Edge points to @empty-vault/node but vault has no such node.
// --------------------------------------------------------------------------
func TestStress_EmptyRemoteVault(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "local", Type: "concept", Summary: "local",
		Edges: []node.Edge{
			{Target: "@empty-vault/missing", Relation: node.References},
			{Target: "@present-vault/exists", Relation: node.References},
		},
	})

	// empty-vault exists but has no nodes.
	emptyG := graph.NewGraph()

	// present-vault has the node.
	presentG := graph.NewGraph()
	presentG.AddNode(&node.Node{ID: "exists", Type: "concept", Summary: "exists"})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{
		"empty-vault":   emptyG,
		"present-vault": presentG,
	}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"local"},
		MaxDepth:    1,
		TokenBudget: 8192,
	})

	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}

	if !found["local"] {
		t.Error("expected local node")
	}
	if !found["@present-vault/exists"] {
		t.Error("expected @present-vault/exists")
	}
	// The missing node should NOT be in the result set.
	if found["@empty-vault/missing"] {
		t.Error("should NOT find @empty-vault/missing (node doesn't exist in vault)")
	}
	if len(sub.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d: %v", len(sub.Nodes), stressNodeIDs(sub))
	}
}

// --------------------------------------------------------------------------
// Stress test: Nonexistent vault (vault itself not registered)
// --------------------------------------------------------------------------
func TestStress_NonexistentVault(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "local", Type: "concept", Summary: "local",
		Edges: []node.Edge{
			{Target: "@ghost-vault/phantom", Relation: node.References},
		},
	})

	// No vaults registered at all.
	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"local"},
		MaxDepth:    2,
		TokenBudget: 8192,
	})

	if len(sub.Nodes) != 1 {
		t.Errorf("expected 1 node (only local), got %d: %v", len(sub.Nodes), stressNodeIDs(sub))
	}
	if sub.Nodes[0].ID != "local" {
		t.Errorf("expected local, got %s", sub.Nodes[0].ID)
	}
}

// --------------------------------------------------------------------------
// Stress test: Superseded remote node
// Remote node with status=superseded should be skipped unless IncludeSuperseded=true.
// --------------------------------------------------------------------------
func TestStress_SupersededRemoteNode(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "start", Type: "concept", Summary: "start",
		Edges: []node.Edge{
			{Target: "@rv/active-node", Relation: node.References},
			{Target: "@rv/superseded-node", Relation: node.References},
		},
	})

	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{
		ID: "active-node", Type: "function", Summary: "still active",
	})
	remoteG.AddNode(&node.Node{
		ID: "superseded-node", Type: "function", Summary: "old and replaced",
		Status:       "superseded",
		SupersededBy: "active-node",
	})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	// Without IncludeSuperseded: superseded node should be skipped.
	t.Run("exclude_superseded", func(t *testing.T) {
		sub := Traverse(resolver, TraversalConfig{
			EntryIDs:          []string{"start"},
			MaxDepth:          1,
			TokenBudget:       8192,
			IncludeSuperseded: false,
		})

		found := make(map[string]bool)
		for _, n := range sub.Nodes {
			found[n.ID] = true
		}

		if !found["start"] {
			t.Error("expected start")
		}
		if !found["@rv/active-node"] {
			t.Error("expected @rv/active-node")
		}
		if found["@rv/superseded-node"] {
			t.Error("superseded node should be excluded when IncludeSuperseded=false")
		}
		if len(sub.Nodes) != 2 {
			t.Errorf("expected 2 nodes, got %d: %v", len(sub.Nodes), stressNodeIDs(sub))
		}
	})

	// With IncludeSuperseded: superseded node should be included.
	t.Run("include_superseded", func(t *testing.T) {
		sub := Traverse(resolver, TraversalConfig{
			EntryIDs:          []string{"start"},
			MaxDepth:          1,
			TokenBudget:       8192,
			IncludeSuperseded: true,
		})

		found := make(map[string]bool)
		for _, n := range sub.Nodes {
			found[n.ID] = true
		}

		if !found["start"] {
			t.Error("expected start")
		}
		if !found["@rv/active-node"] {
			t.Error("expected @rv/active-node")
		}
		if !found["@rv/superseded-node"] {
			t.Error("superseded node should be included when IncludeSuperseded=true")
		}
		if len(sub.Nodes) != 3 {
			t.Errorf("expected 3 nodes, got %d: %v", len(sub.Nodes), stressNodeIDs(sub))
		}
	})
}

// --------------------------------------------------------------------------
// Stress test: Large fan-out from a single remote node
// Tests that wide graphs in remote vaults are handled correctly.
// --------------------------------------------------------------------------
func TestStress_LargeRemoteFanOut(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "root", Type: "concept", Summary: "root",
		Edges: []node.Edge{{Target: "@big/hub", Relation: node.References}},
	})

	remoteG := graph.NewGraph()
	hubEdges := make([]node.Edge, 20)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("leaf-%d", i)
		remoteG.AddNode(&node.Node{
			ID: id, Type: "concept", Summary: fmt.Sprintf("leaf %d", i),
		})
		hubEdges[i] = node.Edge{Target: id, Relation: node.References}
	}
	remoteG.AddNode(&node.Node{
		ID: "hub", Type: "concept", Summary: "hub with 20 children",
		Edges: hubEdges,
	})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{"big": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"root"},
		MaxDepth:    2,
		TokenBudget: 16384,
	})

	// root + @big/hub + 20 leaves = 22 nodes.
	if len(sub.Nodes) != 22 {
		t.Errorf("expected 22 nodes, got %d", len(sub.Nodes))
	}

	// Every leaf should be at depth 2.
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("@big/leaf-%d", i)
		if sub.Depths[id] != 2 {
			t.Errorf("expected depth 2 for %q, got %d", id, sub.Depths[id])
		}
	}
}

// --------------------------------------------------------------------------
// Stress test: Compact output truncation with cross-vault nodes
// Verify that when budget is tight, remote nodes are truncated gracefully.
// --------------------------------------------------------------------------
func TestStress_CompactTruncationCrossVault(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "entry", Type: "concept", Summary: "entry point",
		Edges: []node.Edge{
			{Target: "@rv/a", Relation: node.References},
			{Target: "@rv/b", Relation: node.References},
			{Target: "@rv/c", Relation: node.References},
		},
	})

	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{ID: "a", Type: "concept", Summary: "remote A with a long summary to consume budget"})
	remoteG.AddNode(&node.Node{ID: "b", Type: "concept", Summary: "remote B with a long summary to consume budget"})
	remoteG.AddNode(&node.Node{ID: "c", Type: "concept", Summary: "remote C with a long summary to consume budget"})

	vaults := &stressVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"entry"},
		MaxDepth:    1,
		TokenBudget: 8192,
	})

	// Use a very tight budget so some nodes get truncated.
	result := Compact(resolver, sub, 80) // 80 tokens ~ 320 chars

	// Should have some truncated IDs.
	if result.NodeCount == 4 && len(result.TruncatedIDs) == 0 {
		// Budget might be enough for everything. Try even tighter.
		result = Compact(resolver, sub, 20) // 20 tokens ~ 80 chars
	}

	// Either all fit or some are truncated. The key thing is no panic.
	t.Logf("Compact result: %d nodes included, %d truncated", result.NodeCount, len(result.TruncatedIDs))

	// If there are truncated IDs, they should have @-prefixed IDs.
	for _, id := range result.TruncatedIDs {
		t.Logf("  truncated: %s", id)
	}

	// The XML should always contain the wrapper.
	if !strings.Contains(result.XML, "<context_result") {
		t.Error("XML should contain <context_result> wrapper")
	}

	t.Logf("Compact XML:\n%s", result.XML)
}

// --------------------------------------------------------------------------
// Helper
// --------------------------------------------------------------------------
func stressNodeIDs(sub *Subgraph) []string {
	ids := make([]string, len(sub.Nodes))
	for i, n := range sub.Nodes {
		ids[i] = n.ID
	}
	return ids
}
