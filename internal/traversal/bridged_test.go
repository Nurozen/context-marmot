package traversal

import (
	"fmt"
	"testing"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

func TestParseVaultPrefix(t *testing.T) {
	tests := []struct {
		input     string
		wantVault string
		wantNode  string
	}{
		{"simple-node", "", "simple-node"},
		{"@vault-a/some/node", "vault-a", "some/node"},
		{"@vault-b/node", "vault-b", "node"},
		{"@incomplete", "", "@incomplete"},
		{"normal/path/node", "", "normal/path/node"},
		{"", "", ""},
		{"@v/n", "v", "n"},
	}
	for _, tt := range tests {
		vault, nodeID := parseVaultPrefix(tt.input)
		if vault != tt.wantVault || nodeID != tt.wantNode {
			t.Errorf("parseVaultPrefix(%q) = (%q, %q), want (%q, %q)",
				tt.input, vault, nodeID, tt.wantVault, tt.wantNode)
		}
	}
}

func TestBridgedGraphResolver_LocalOnly(t *testing.T) {
	g := graph.NewGraph()
	n := &node.Node{ID: "test-node", Type: "concept", Summary: "test"}
	g.AddNode(n)

	resolver := &BridgedGraphResolver{Local: g}

	got, ok := resolver.GetNode("test-node")
	if !ok {
		t.Fatal("expected to find local node")
	}
	if got.ID != "test-node" {
		t.Fatalf("expected ID test-node, got %s", got.ID)
	}

	_, ok = resolver.GetNode("missing")
	if ok {
		t.Fatal("expected missing node to return false")
	}
}

func TestBridgedGraphResolver_LocalEdges(t *testing.T) {
	g := graph.NewGraph()
	n := &node.Node{
		ID: "a", Type: "function", Summary: "A",
		Edges: []node.Edge{{Target: "b", Relation: node.Calls}},
	}
	g.AddNode(n)
	g.AddNode(&node.Node{ID: "b", Type: "function", Summary: "B"})

	resolver := &BridgedGraphResolver{Local: g}

	edges := resolver.GetEdges("a", graph.Outbound)
	if len(edges) != 1 {
		t.Fatalf("expected 1 outbound edge, got %d", len(edges))
	}
	if edges[0].Target != "b" {
		t.Fatalf("expected edge target b, got %s", edges[0].Target)
	}
}

func TestBridgedGraphResolver_NilVaults(t *testing.T) {
	g := graph.NewGraph()
	g.AddNode(&node.Node{ID: "local", Type: "concept", Summary: "local"})

	resolver := &BridgedGraphResolver{Local: g, Vaults: nil}

	// With nil Vaults, @-prefixed IDs fall back to local lookup.
	_, ok := resolver.GetNode("@remote/node")
	if ok {
		t.Fatal("expected @remote/node to not be found with nil Vaults (falls to local lookup)")
	}

	edges := resolver.GetEdges("@remote/node", graph.Outbound)
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges for unknown @-prefixed node, got %d", len(edges))
	}
}

// mockVaultProvider implements VaultGraphProvider for testing.
type mockVaultProvider struct {
	graphs map[string]*graph.Graph
}

func (m *mockVaultProvider) ResolveGraph(vaultID string) (*graph.Graph, error) {
	g, ok := m.graphs[vaultID]
	if !ok {
		return nil, fmt.Errorf("unknown vault %q", vaultID)
	}
	return g, nil
}

func TestBridgedGraphResolver_CrossVault(t *testing.T) {
	// Local graph.
	localG := graph.NewGraph()
	localNode := &node.Node{
		ID: "local-node", Type: "concept", Summary: "local",
		Edges: []node.Edge{{Target: "@remote-vault/remote-node", Relation: node.References}},
	}
	localG.AddNode(localNode)

	// Remote graph.
	remoteG := graph.NewGraph()
	remoteNode := &node.Node{ID: "remote-node", Type: "concept", Summary: "remote"}
	remoteG.AddNode(remoteNode)

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"remote-vault": remoteG}}

	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	// Can resolve local node.
	n, ok := resolver.GetNode("local-node")
	if !ok || n.ID != "local-node" {
		t.Fatal("expected to find local-node")
	}

	// Can resolve remote node via @prefix — ID is rewritten to @-prefixed form.
	n, ok = resolver.GetNode("@remote-vault/remote-node")
	if !ok || n.ID != "@remote-vault/remote-node" {
		t.Fatalf("expected ID @remote-vault/remote-node, got %q", n.ID)
	}
}

func TestBridgedGraphResolver_CrossVaultEdges(t *testing.T) {
	// Remote graph with edges.
	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{
		ID: "r-a", Type: "function", Summary: "Remote A",
		Edges: []node.Edge{{Target: "r-b", Relation: node.Calls}},
	})
	remoteG.AddNode(&node.Node{ID: "r-b", Type: "function", Summary: "Remote B"})

	localG := graph.NewGraph()
	localG.AddNode(&node.Node{ID: "local", Type: "concept", Summary: "local"})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	edges := resolver.GetEdges("@rv/r-a", graph.Outbound)
	if len(edges) != 1 {
		t.Fatalf("expected 1 outbound edge from remote node, got %d", len(edges))
	}
	if edges[0].Target != "@rv/r-b" {
		t.Fatalf("expected edge target @rv/r-b, got %s", edges[0].Target)
	}
}

func TestBridgedGraphResolver_CrossVaultUnknownVault(t *testing.T) {
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{ID: "local", Type: "concept", Summary: "local"})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	_, ok := resolver.GetNode("@nonexistent/node")
	if ok {
		t.Fatal("expected false for unknown vault")
	}

	edges := resolver.GetEdges("@nonexistent/node", graph.Outbound)
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges for unknown vault, got %d", len(edges))
	}
}

func TestBridgedGraphResolver_TraverseCrossVault(t *testing.T) {
	// Local graph with edge pointing to remote vault.
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "local-node", Type: "concept", Summary: "local",
		Edges: []node.Edge{{Target: "@remote-vault/remote-node", Relation: node.References}},
	})

	// Remote graph: remote-node -> deep-node (both within remote-vault).
	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{
		ID: "remote-node", Type: "concept", Summary: "remote",
		Edges: []node.Edge{{Target: "deep-node", Relation: node.References}},
	})
	remoteG.AddNode(&node.Node{ID: "deep-node", Type: "concept", Summary: "deep"})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"remote-vault": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	// Depth 1: should reach remote-node but not deep-node.
	sub1 := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"local-node"},
		MaxDepth:    1,
		TokenBudget: 4096,
	})

	if len(sub1.Nodes) != 2 {
		t.Fatalf("depth-1: expected 2 nodes (local + remote), got %d: %v", len(sub1.Nodes), nodeIDs(sub1))
	}

	// Depth 2: should also reach deep-node via @-prefix rewriting.
	sub2 := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"local-node"},
		MaxDepth:    2,
		TokenBudget: 4096,
	})

	found := make(map[string]bool)
	for _, n := range sub2.Nodes {
		found[n.ID] = true
	}
	if !found["local-node"] {
		t.Error("expected local-node in traversal result")
	}
	if !found["@remote-vault/remote-node"] {
		t.Error("expected @remote-vault/remote-node in traversal result")
	}
	if !found["@remote-vault/deep-node"] {
		t.Error("expected @remote-vault/deep-node in depth-2 traversal result")
	}
	if len(sub2.Nodes) != 3 {
		t.Fatalf("depth-2: expected 3 nodes, got %d: %v", len(sub2.Nodes), nodeIDs(sub2))
	}
}

func TestBridgedGraphResolver_TraverseDepthZeroCrossVault(t *testing.T) {
	// At depth 0, cross-vault edges should NOT be followed.
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "entry", Type: "concept", Summary: "entry",
		Edges: []node.Edge{{Target: "@rv/remote", Relation: node.References}},
	})

	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{ID: "remote", Type: "concept", Summary: "remote"})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"entry"},
		MaxDepth:    0,
		TokenBudget: 4096,
	})

	if len(sub.Nodes) != 1 {
		t.Fatalf("expected 1 node at depth 0, got %d", len(sub.Nodes))
	}
}

func TestBridgedGraphResolver_MultiHopCrossVault(t *testing.T) {
	// Local -> @rv/a -> (a has edge to b within rv).
	// With edge-target rewriting, b should be reachable as @rv/b.
	localG := graph.NewGraph()
	localG.AddNode(&node.Node{
		ID: "start", Type: "concept", Summary: "start",
		Edges: []node.Edge{{Target: "@rv/a", Relation: node.Calls}},
	})

	remoteG := graph.NewGraph()
	remoteG.AddNode(&node.Node{
		ID: "a", Type: "function", Summary: "Remote A",
		Edges: []node.Edge{{Target: "b", Relation: node.Calls}},
	})
	remoteG.AddNode(&node.Node{ID: "b", Type: "function", Summary: "Remote B"})

	vaults := &mockVaultProvider{graphs: map[string]*graph.Graph{"rv": remoteG}}
	resolver := &BridgedGraphResolver{Local: localG, Vaults: vaults}

	sub := Traverse(resolver, TraversalConfig{
		EntryIDs:    []string{"start"},
		MaxDepth:    2,
		TokenBudget: 4096,
	})

	// start (depth 0) -> @rv/a (depth 1) -> @rv/b (depth 2, rewritten from "b")
	found := make(map[string]bool)
	for _, n := range sub.Nodes {
		found[n.ID] = true
	}
	if !found["start"] {
		t.Error("expected start in traversal result")
	}
	if !found["@rv/a"] {
		t.Error("expected @rv/a in traversal result")
	}
	if !found["@rv/b"] {
		t.Error("expected @rv/b in traversal result (multi-hop via @rv/b)")
	}
	if len(sub.Nodes) != 3 {
		t.Fatalf("expected 3 nodes (start + a + b), got %d: %v", len(sub.Nodes), nodeIDs(sub))
	}
}
