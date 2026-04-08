package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// Cross-Vault Edge Write Tests
// ---------------------------------------------------------------------------

// TestCrossVaultEdgePreservation verifies that writing a node with an
// @vault-id/node-id edge target preserves the full @-prefixed target
// through the handler's namespace auto-prefix logic.
func TestCrossVaultEdgePreservation(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "api/gateway",
		"type":    "module",
		"summary": "API gateway that calls auth service in another vault",
		"edges": []map[string]any{
			{"target": "@vault-b/auth/login", "relation": "calls"},
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("write returned error: %s", resultText(t, res))
	}

	// Verify the node was stored with the @-prefixed target preserved.
	n, ok := eng.GetGraph().GetNode("api/gateway")
	if !ok {
		t.Fatal("node api/gateway not found in graph")
	}
	if len(n.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(n.Edges))
	}
	if n.Edges[0].Target != "@vault-b/auth/login" {
		t.Errorf("expected edge target @vault-b/auth/login, got %q", n.Edges[0].Target)
	}
	if n.Edges[0].Relation != "calls" {
		t.Errorf("expected edge relation calls, got %q", n.Edges[0].Relation)
	}
}

// TestCrossVaultEdgeNamespaceAutoPrefix verifies that the namespace auto-prefix
// logic does NOT corrupt @-prefixed edge targets. When namespace != "default",
// the handler prepends the namespace to bare targets — but must skip @-prefixed ones.
func TestCrossVaultEdgeNamespaceAutoPrefix(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":        "svc/handler",
		"type":      "function",
		"namespace": "backend",
		"summary":   "Service handler with cross-vault and local edges",
		"edges": []map[string]any{
			// Cross-vault edge: should NOT get namespace-prefixed.
			{"target": "@vault-b/auth/login", "relation": "calls"},
			// Local edge in same namespace: should get namespace-prefixed.
			{"target": "svc/helper", "relation": "calls"},
			// Already-prefixed edge: should NOT be double-prefixed.
			{"target": "backend/svc/utils", "relation": "imports"},
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("write returned error: %s", resultText(t, res))
	}

	// The node ID itself should be namespace-prefixed.
	n, ok := eng.GetGraph().GetNode("backend/svc/handler")
	if !ok {
		t.Fatal("node backend/svc/handler not found in graph")
	}

	if len(n.Edges) != 3 {
		t.Fatalf("expected 3 edges, got %d", len(n.Edges))
	}

	edgeTargets := make(map[string]bool)
	for _, e := range n.Edges {
		edgeTargets[e.Target] = true
	}

	// Cross-vault edge must be preserved as-is.
	if !edgeTargets["@vault-b/auth/login"] {
		t.Error("cross-vault edge @vault-b/auth/login was corrupted or missing")
	}
	// Local edge should be namespace-prefixed.
	if !edgeTargets["backend/svc/helper"] {
		t.Error("local edge svc/helper was not auto-prefixed to backend/svc/helper")
	}
	// Already-prefixed edge should remain as-is.
	if !edgeTargets["backend/svc/utils"] {
		t.Error("already-prefixed edge backend/svc/utils was double-prefixed or missing")
	}

	t.Logf("Edge targets after write: %v", edgeTargets)
}

// TestCrossVaultEdgeCycleDetectionNoFalsePositive verifies that cycle detection
// does not false-positive on @-prefixed structural edge targets. Since the
// remote node doesn't exist in the local graph, the DFS from targetID finds
// nothing — no cycle.
func TestCrossVaultEdgeCycleDetectionNoFalsePositive(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Write a node with a structural (contains) edge to a cross-vault target.
	// This should succeed because @vault-b/auth/module doesn't exist locally,
	// so no cycle can form.
	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "arch/root",
		"type":    "module",
		"summary": "Root architecture module spanning vaults",
		"edges": []map[string]any{
			{"target": "@vault-b/auth/module", "relation": "contains"},
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("structural edge to cross-vault target was incorrectly rejected: %s", resultText(t, res))
	}

	// Verify it was written.
	n, ok := eng.GetGraph().GetNode("arch/root")
	if !ok {
		t.Fatal("node arch/root not found in graph")
	}
	if len(n.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(n.Edges))
	}
	if n.Edges[0].Class != node.Structural {
		t.Errorf("expected structural edge class, got %q", n.Edges[0].Class)
	}
	if n.Edges[0].Target != "@vault-b/auth/module" {
		t.Errorf("expected target @vault-b/auth/module, got %q", n.Edges[0].Target)
	}
}

// TestCrossVaultEdgeGraphCleanup verifies that RemoveNode correctly handles
// @-prefixed edge targets in inEdges. When a node with cross-vault edges is
// removed, the graph should not panic or leave stale entries.
func TestCrossVaultEdgeGraphCleanup(t *testing.T) {
	g := graph.NewGraph()

	// Add a node with a cross-vault edge.
	n := &node.Node{
		ID:        "local/service",
		Type:      "module",
		Namespace: "default",
		Status:    node.StatusActive,
		Edges: []node.Edge{
			{Target: "@vault-b/auth/login", Relation: "calls", Class: node.Behavioral},
			{Target: "local/helper", Relation: "calls", Class: node.Behavioral},
		},
	}
	if err := g.AddNode(n); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	// Verify edges are registered.
	outEdges := g.GetEdges("local/service", graph.Outbound)
	if len(outEdges) != 2 {
		t.Fatalf("expected 2 outbound edges, got %d", len(outEdges))
	}

	// Verify inbound edges on the @-prefixed target.
	inEdges := g.GetEdges("@vault-b/auth/login", graph.Inbound)
	if len(inEdges) != 1 {
		t.Fatalf("expected 1 inbound edge on @vault-b/auth/login, got %d", len(inEdges))
	}
	if inEdges[0].Target != "local/service" {
		t.Errorf("expected inbound edge source local/service, got %q", inEdges[0].Target)
	}

	// Now remove the node — should cleanly remove all edges including @-prefixed ones.
	if err := g.RemoveNode("local/service"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}

	// Verify node is gone.
	if _, ok := g.GetNode("local/service"); ok {
		t.Error("node local/service should have been removed")
	}

	// Verify outbound edges are gone.
	outEdges = g.GetEdges("local/service", graph.Outbound)
	if len(outEdges) != 0 {
		t.Errorf("expected 0 outbound edges after removal, got %d", len(outEdges))
	}

	// Verify inbound edge on @-prefixed target is cleaned up.
	inEdges = g.GetEdges("@vault-b/auth/login", graph.Inbound)
	if len(inEdges) != 0 {
		t.Errorf("expected 0 inbound edges on @vault-b/auth/login after removal, got %d", len(inEdges))
	}

	// Verify local helper inbound edge is also cleaned up.
	inEdges = g.GetEdges("local/helper", graph.Inbound)
	if len(inEdges) != 0 {
		t.Errorf("expected 0 inbound edges on local/helper after removal, got %d", len(inEdges))
	}
}

// TestCrossVaultEdgeUpsertCleanup verifies that UpsertNode (which remove+add
// internally) correctly handles @-prefixed edge targets during the
// remove-then-add cycle.
func TestCrossVaultEdgeUpsertCleanup(t *testing.T) {
	g := graph.NewGraph()

	// Add initial node with cross-vault edge.
	n1 := &node.Node{
		ID:     "local/svc",
		Type:   "module",
		Status: node.StatusActive,
		Edges: []node.Edge{
			{Target: "@vault-x/remote/node", Relation: "calls", Class: node.Behavioral},
		},
	}
	if err := g.AddNode(n1); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	// Verify initial state.
	inEdges := g.GetEdges("@vault-x/remote/node", graph.Inbound)
	if len(inEdges) != 1 {
		t.Fatalf("expected 1 inbound edge on @vault-x/remote/node, got %d", len(inEdges))
	}

	// Upsert the node with a DIFFERENT cross-vault edge (old one should be removed).
	n2 := &node.Node{
		ID:     "local/svc",
		Type:   "module",
		Status: node.StatusActive,
		Edges: []node.Edge{
			{Target: "@vault-y/other/node", Relation: "reads", Class: node.Behavioral},
		},
	}
	if err := g.UpsertNode(n2); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	// Old cross-vault inbound edge should be cleaned up.
	inEdges = g.GetEdges("@vault-x/remote/node", graph.Inbound)
	if len(inEdges) != 0 {
		t.Errorf("expected 0 inbound edges on @vault-x/remote/node after upsert, got %d", len(inEdges))
	}

	// New cross-vault inbound edge should be present.
	inEdges = g.GetEdges("@vault-y/other/node", graph.Inbound)
	if len(inEdges) != 1 {
		t.Errorf("expected 1 inbound edge on @vault-y/other/node after upsert, got %d", len(inEdges))
	}
}

// TestCrossVaultEdgeJSONUnmarshal verifies that JSON unmarshalling of edge
// input correctly produces an Edge with the full @-prefixed target intact.
func TestCrossVaultEdgeJSONUnmarshal(t *testing.T) {
	input := `[{"target": "@vault-b/auth/login", "relation": "calls"}]`

	var edges []WriteEdgeInput
	if err := json.Unmarshal([]byte(input), &edges); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].Target != "@vault-b/auth/login" {
		t.Errorf("expected target @vault-b/auth/login, got %q", edges[0].Target)
	}
	if edges[0].Relation != "calls" {
		t.Errorf("expected relation calls, got %q", edges[0].Relation)
	}
}

// TestCrossVaultEdgeMultipleVaults verifies that edges to multiple different
// vaults are all preserved correctly through a write.
func TestCrossVaultEdgeMultipleVaults(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "hub/orchestrator",
		"type":    "module",
		"summary": "Central orchestrator connecting multiple vaults",
		"edges": []map[string]any{
			{"target": "@vault-auth/auth/service", "relation": "calls"},
			{"target": "@vault-db/db/connector", "relation": "reads"},
			{"target": "@vault-cache/cache/store", "relation": "writes"},
			{"target": "hub/config", "relation": "imports"},
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("write returned error: %s", resultText(t, res))
	}

	n, ok := eng.GetGraph().GetNode("hub/orchestrator")
	if !ok {
		t.Fatal("node hub/orchestrator not found")
	}
	if len(n.Edges) != 4 {
		t.Fatalf("expected 4 edges, got %d", len(n.Edges))
	}

	expectedTargets := map[string]string{
		"@vault-auth/auth/service": "calls",
		"@vault-db/db/connector":   "reads",
		"@vault-cache/cache/store": "writes",
		"hub/config":               "imports",
	}

	for _, e := range n.Edges {
		expectedRel, ok := expectedTargets[e.Target]
		if !ok {
			t.Errorf("unexpected edge target: %q", e.Target)
			continue
		}
		if string(e.Relation) != expectedRel {
			t.Errorf("edge to %s: expected relation %q, got %q", e.Target, expectedRel, e.Relation)
		}
		delete(expectedTargets, e.Target)
	}
	for target := range expectedTargets {
		t.Errorf("missing edge to %s", target)
	}
}

// TestCrossVaultWriteResultContainsCorrectID verifies the write response
// contains the correct node ID when writing nodes with cross-vault edges.
func TestCrossVaultWriteResultContainsCorrectID(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "net/proxy",
		"type":    "function",
		"summary": "Proxy that routes to cross-vault endpoints",
		"edges": []map[string]any{
			{"target": "@vault-b/backend/handler", "relation": "calls"},
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("write error: %s", resultText(t, res))
	}

	var wr WriteResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &wr); err != nil {
		t.Fatalf("unmarshal write result: %v", err)
	}
	if wr.NodeID != "net/proxy" {
		t.Errorf("expected node_id=net/proxy, got %s", wr.NodeID)
	}
	if wr.Status != "created" {
		t.Errorf("expected status=created, got %s", wr.Status)
	}
	if wr.Hash == "" {
		t.Error("expected non-empty hash")
	}
}

// TestCrossVaultWouldCreateCycleWithRemoteTarget verifies that WouldCreateCycle
// correctly returns false when the target is an @-prefixed ID that doesn't
// exist in the local graph.
func TestCrossVaultWouldCreateCycleWithRemoteTarget(t *testing.T) {
	g := graph.NewGraph()

	// Add a local node.
	n := &node.Node{
		ID:     "local/a",
		Type:   "module",
		Status: node.StatusActive,
		Edges: []node.Edge{
			{Target: "local/b", Relation: "contains", Class: node.Structural},
		},
	}
	if err := g.AddNode(n); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	// Adding structural edge from local/a to @vault/remote should NOT be a cycle.
	if g.WouldCreateCycle("local/a", "@vault/remote/node") {
		t.Error("WouldCreateCycle should return false for @-prefixed target not in local graph")
	}

	// Adding structural edge from local/b to @vault/remote should also NOT be a cycle.
	if g.WouldCreateCycle("local/b", "@vault/remote/node") {
		t.Error("WouldCreateCycle should return false for @-prefixed target not in local graph")
	}

	// Sanity check: actual cycle local/b -> local/a SHOULD be detected.
	if !g.WouldCreateCycle("local/b", "local/a") {
		t.Error("WouldCreateCycle should return true for actual local cycle")
	}
}
