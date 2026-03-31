package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/embedding"
)

// testEngine creates an Engine in a temp directory with a mock embedder.
func testEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	embedder := embedding.NewMockEmbedder("test-model")
	eng, err := NewEngine(dir, embedder)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// makeCallToolRequest builds a CallToolRequest with the given args map.
func makeCallToolRequest(name string, args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
}

// resultText extracts the first text content from a CallToolResult.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("empty content in result")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

func TestEngineCreation(t *testing.T) {
	eng := testEngine(t)
	if eng.NodeStore == nil {
		t.Error("NodeStore is nil")
	}
	if eng.Graph == nil {
		t.Error("Graph is nil")
	}
	if eng.EmbeddingStore == nil {
		t.Error("EmbeddingStore is nil")
	}
	if eng.Graph.NodeCount() != 0 {
		t.Errorf("expected empty graph, got %d nodes", eng.Graph.NodeCount())
	}
}

func TestWriteQueryRoundtrip(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Write a node.
	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":        "auth/login",
		"type":      "function",
		"namespace": "default",
		"summary":   "Handles user login with OAuth2 flow",
		"context":   "func login() { ... }",
	})

	writeRes, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if writeRes.IsError {
		t.Fatalf("write returned error: %s", resultText(t, writeRes))
	}

	// Parse write result.
	var wr WriteResult
	text := resultText(t, writeRes)
	if err := json.Unmarshal([]byte(text), &wr); err != nil {
		t.Fatalf("unmarshal write result: %v", err)
	}
	if wr.NodeID != "auth/login" {
		t.Errorf("expected node_id=auth/login, got %s", wr.NodeID)
	}
	if wr.Status != "created" {
		t.Errorf("expected status=created, got %s", wr.Status)
	}
	if wr.Hash == "" {
		t.Error("expected non-empty hash")
	}

	// Verify node is in graph.
	if eng.Graph.NodeCount() != 1 {
		t.Errorf("expected 1 node in graph, got %d", eng.Graph.NodeCount())
	}

	// Query for the node.
	queryReq := makeCallToolRequest("context_query", map[string]any{
		"query":  "login OAuth2",
		"depth":  2,
		"budget": 4096,
	})

	queryRes, err := eng.HandleContextQuery(ctx, queryReq)
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	if queryRes.IsError {
		t.Fatalf("query returned error: %s", resultText(t, queryRes))
	}

	xml := resultText(t, queryRes)
	if xml == "" {
		t.Error("expected non-empty XML result")
	}
	// The result should contain our node.
	if !containsString(xml, "auth/login") {
		t.Errorf("expected XML to contain auth/login, got: %s", xml)
	}
}

func TestWriteWithEdgesAndCycleRejection(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Write node A.
	writeA := makeCallToolRequest("context_write", map[string]any{
		"id":      "mod/a",
		"type":    "module",
		"summary": "Module A",
		"edges": []map[string]any{
			{"target": "mod/b", "relation": "contains"},
		},
	})
	resA, err := eng.HandleContextWrite(ctx, writeA)
	if err != nil {
		t.Fatalf("write A: %v", err)
	}
	if resA.IsError {
		t.Fatalf("write A error: %s", resultText(t, resA))
	}

	// Write node B.
	writeB := makeCallToolRequest("context_write", map[string]any{
		"id":      "mod/b",
		"type":    "module",
		"summary": "Module B",
	})
	resB, err := eng.HandleContextWrite(ctx, writeB)
	if err != nil {
		t.Fatalf("write B: %v", err)
	}
	if resB.IsError {
		t.Fatalf("write B error: %s", resultText(t, resB))
	}

	// Now try to write node B with a structural edge back to A (creating a cycle).
	writeBCycle := makeCallToolRequest("context_write", map[string]any{
		"id":      "mod/b",
		"type":    "module",
		"summary": "Module B updated",
		"edges": []map[string]any{
			{"target": "mod/a", "relation": "contains"},
		},
	})
	resCycle, err := eng.HandleContextWrite(ctx, writeBCycle)
	if err != nil {
		t.Fatalf("write B cycle: %v", err)
	}
	if !resCycle.IsError {
		t.Fatal("expected structural cycle to be rejected")
	}
	errText := resultText(t, resCycle)
	if !containsString(errText, "cycle") {
		t.Errorf("expected cycle error, got: %s", errText)
	}

	// Behavioral edges should be allowed even if they create a cycle.
	writeBBehavioral := makeCallToolRequest("context_write", map[string]any{
		"id":      "mod/b",
		"type":    "module",
		"summary": "Module B with behavioral cycle",
		"edges": []map[string]any{
			{"target": "mod/a", "relation": "calls"},
		},
	})
	resBehav, err := eng.HandleContextWrite(ctx, writeBBehavioral)
	if err != nil {
		t.Fatalf("write B behavioral: %v", err)
	}
	if resBehav.IsError {
		t.Fatalf("behavioral cycle should be allowed: %s", resultText(t, resBehav))
	}
}

func TestVerifyDetectsDanglingEdges(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Write a node with an edge to a nonexistent node.
	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "test/node",
		"type":    "function",
		"summary": "Test node",
		"edges": []map[string]any{
			{"target": "test/nonexistent", "relation": "calls"},
		},
	})
	writeRes, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if writeRes.IsError {
		t.Fatalf("write error: %s", resultText(t, writeRes))
	}

	// Verify integrity.
	verifyReq := makeCallToolRequest("context_verify", map[string]any{
		"check": "integrity",
	})
	verifyRes, err := eng.HandleContextVerify(ctx, verifyReq)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verifyRes.IsError {
		t.Fatalf("verify error: %s", resultText(t, verifyRes))
	}

	var vr VerifyResult
	text := resultText(t, verifyRes)
	if err := json.Unmarshal([]byte(text), &vr); err != nil {
		t.Fatalf("unmarshal verify result: %v", err)
	}

	if vr.Total == 0 {
		t.Error("expected at least one issue (dangling edge)")
	}

	found := false
	for _, issue := range vr.Issues {
		if issue.Type == "dangling_edge" && issue.NodeID == "test/node" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected dangling_edge issue for test/node, got: %+v", vr.Issues)
	}
}

func TestQueryEmptyGraph(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	queryReq := makeCallToolRequest("context_query", map[string]any{
		"query": "anything",
	})

	queryRes, err := eng.HandleContextQuery(ctx, queryReq)
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	if queryRes.IsError {
		t.Fatalf("query returned error: %s", resultText(t, queryRes))
	}

	xml := resultText(t, queryRes)
	if !containsString(xml, `nodes="0"`) {
		t.Errorf("expected empty result with nodes=0, got: %s", xml)
	}
}

func TestQueryMissingParameter(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	queryReq := makeCallToolRequest("context_query", map[string]any{})
	res, err := eng.HandleContextQuery(ctx, queryReq)
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	if !res.IsError {
		t.Error("expected error when query is missing")
	}
}

func TestVerifyAllOnEmptyGraph(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	verifyReq := makeCallToolRequest("context_verify", map[string]any{
		"check": "all",
	})
	res, err := eng.HandleContextVerify(ctx, verifyReq)
	if err != nil {
		t.Fatalf("HandleContextVerify: %v", err)
	}
	if res.IsError {
		t.Fatalf("verify error: %s", resultText(t, res))
	}

	var vr VerifyResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &vr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if vr.Total != 0 {
		t.Errorf("expected 0 issues on empty graph, got %d", vr.Total)
	}
}

func TestVerifySpecificNodes(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Write two nodes.
	for _, id := range []string{"a/one", "a/two"} {
		req := makeCallToolRequest("context_write", map[string]any{
			"id":      id,
			"type":    "function",
			"summary": "Node " + id,
		})
		res, err := eng.HandleContextWrite(ctx, req)
		if err != nil || res.IsError {
			t.Fatalf("write %s failed", id)
		}
	}

	// Verify only one node.
	verifyReq := makeCallToolRequest("context_verify", map[string]any{
		"node_ids": []string{"a/one"},
		"check":    "integrity",
	})
	res, err := eng.HandleContextVerify(ctx, verifyReq)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	var vr VerifyResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &vr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// No issues expected for a self-contained node.
	if vr.Total != 0 {
		t.Errorf("expected 0 issues, got %d: %+v", vr.Total, vr.Issues)
	}
}

func TestWriteUpdatesExistingNode(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Write initial node.
	req1 := makeCallToolRequest("context_write", map[string]any{
		"id":      "test/update",
		"type":    "function",
		"summary": "Original summary",
	})
	res1, _ := eng.HandleContextWrite(ctx, req1)
	if res1.IsError {
		t.Fatalf("first write failed: %s", resultText(t, res1))
	}

	// Update the same node.
	req2 := makeCallToolRequest("context_write", map[string]any{
		"id":      "test/update",
		"type":    "function",
		"summary": "Updated summary",
		"context": "new context",
	})
	res2, _ := eng.HandleContextWrite(ctx, req2)
	if res2.IsError {
		t.Fatalf("second write failed: %s", resultText(t, res2))
	}

	// Verify the graph still has exactly 1 node.
	if eng.Graph.NodeCount() != 1 {
		t.Errorf("expected 1 node after update, got %d", eng.Graph.NodeCount())
	}

	// Verify the node content was updated.
	n, ok := eng.Graph.GetNode("test/update")
	if !ok {
		t.Fatal("node not found after update")
	}
	if n.Summary != "Updated summary" {
		t.Errorf("expected updated summary, got %q", n.Summary)
	}
}

func TestServerCreation(t *testing.T) {
	eng := testEngine(t)
	srv := NewServer(eng)
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
	if srv.mcpServer == nil {
		t.Fatal("mcpServer is nil")
	}
}

// TestNodePersistence verifies that written nodes are actually saved to disk.
func TestNodePersistence(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "persist/test",
		"type":    "concept",
		"summary": "Persistence test node",
		"source": map[string]any{
			"path": "src/test.go",
			"hash": "abc123",
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil || res.IsError {
		t.Fatalf("write failed: %v / %s", err, resultText(t, res))
	}

	// Load the node back from disk.
	path := eng.NodeStore.NodePath("persist/test")
	loaded, err := eng.NodeStore.LoadNode(path)
	if err != nil {
		t.Fatalf("load persisted node: %v", err)
	}
	if loaded.ID != "persist/test" {
		t.Errorf("expected id=persist/test, got %s", loaded.ID)
	}
	if loaded.Source.Path != "src/test.go" {
		t.Errorf("expected source.path=src/test.go, got %s", loaded.Source.Path)
	}
}

// containsString is a test helper for checking substring presence.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestWriteWithSource verifies that the source field is correctly persisted.
func TestWriteWithSource(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "src/func",
		"type":    "function",
		"summary": "A function with source",
		"source": map[string]any{
			"path":  "src/main.go",
			"lines": []int{10, 20},
			"hash":  "deadbeef",
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil || res.IsError {
		t.Fatalf("write failed")
	}

	n, ok := eng.Graph.GetNode("src/func")
	if !ok {
		t.Fatal("node not found")
	}
	if n.Source.Path != "src/main.go" {
		t.Errorf("source path: got %q", n.Source.Path)
	}
	if n.Source.Lines != [2]int{10, 20} {
		t.Errorf("source lines: got %v", n.Source.Lines)
	}
	if n.Source.Hash != "deadbeef" {
		t.Errorf("source hash: got %q", n.Source.Hash)
	}
}

// TestMultipleNodesQueryReturnsResults writes several nodes and queries to verify
// the search-traverse-compact pipeline.
func TestMultipleNodesQueryReturnsResults(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	nodes := []struct {
		id      string
		summary string
	}{
		{"auth/login", "Handles user login with OAuth2"},
		{"auth/logout", "Handles user logout and session cleanup"},
		{"db/users", "Database access layer for user records"},
	}

	for _, n := range nodes {
		req := makeCallToolRequest("context_write", map[string]any{
			"id":      n.id,
			"type":    "function",
			"summary": n.summary,
		})
		res, err := eng.HandleContextWrite(ctx, req)
		if err != nil || res.IsError {
			t.Fatalf("write %s failed", n.id)
		}
	}

	// Query for auth-related content.
	queryReq := makeCallToolRequest("context_query", map[string]any{
		"query":  "user authentication login",
		"budget": 8192,
	})
	res, err := eng.HandleContextQuery(ctx, queryReq)
	if err != nil || res.IsError {
		t.Fatal("query failed")
	}

	xml := resultText(t, res)
	// Should return non-empty results.
	if containsString(xml, `nodes="0"`) {
		t.Error("expected non-zero nodes in query result")
	}
}

// TestWriteSelfLoop verifies that a structural self-loop is rejected.
func TestWriteSelfLoop(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "loop/self",
		"type":    "module",
		"summary": "Self-referencing module",
		"edges": []map[string]any{
			{"target": "loop/self", "relation": "contains"},
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !res.IsError {
		t.Error("expected structural self-loop to be rejected")
	}
}

// TestWriteIDRequired verifies that the id parameter is required.
func TestWriteIDRequired(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	req := makeCallToolRequest("context_write", map[string]any{
		"type":    "function",
		"summary": "Missing ID",
	})
	res, err := eng.HandleContextWrite(ctx, req)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !res.IsError {
		t.Error("expected error when id is missing")
	}
}

// TestEdgeClassification ensures that behavioral edges (e.g., calls) do not
// fail even when they create cycles in the write handler.
func TestBehavioralCycleAllowed(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Create A -> B (calls) and B -> A (calls) — both behavioral, no cycle error.
	for _, spec := range []struct {
		id, target string
	}{
		{"cyc/a", "cyc/b"},
		{"cyc/b", "cyc/a"},
	} {
		req := makeCallToolRequest("context_write", map[string]any{
			"id":      spec.id,
			"type":    "function",
			"summary": "Cycle node " + spec.id,
			"edges": []map[string]any{
				{"target": spec.target, "relation": "calls"},
			},
		})
		res, err := eng.HandleContextWrite(ctx, req)
		if err != nil || res.IsError {
			t.Fatalf("write %s failed: %v", spec.id, resultText(t, res))
		}
	}

	if eng.Graph.NodeCount() != 2 {
		t.Errorf("expected 2 nodes, got %d", eng.Graph.NodeCount())
	}
}

// TestEdgeRelationsInGraph checks that edges written via context_write appear
// in the graph's adjacency lists.
func TestEdgeRelationsInGraph(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Write node with edges.
	req := makeCallToolRequest("context_write", map[string]any{
		"id":      "edge/source",
		"type":    "function",
		"summary": "Source with edges",
		"edges": []map[string]any{
			{"target": "edge/target1", "relation": "calls"},
			{"target": "edge/target2", "relation": "reads"},
		},
	})
	res, _ := eng.HandleContextWrite(ctx, req)
	if res.IsError {
		t.Fatalf("write failed: %s", resultText(t, res))
	}

	n, ok := eng.Graph.GetNode("edge/source")
	if !ok {
		t.Fatal("node not found")
	}
	if len(n.Edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(n.Edges))
	}

	// Verify edge types.
	hasTarget := func(target string) bool {
		for _, e := range n.Edges {
			if e.Target == target {
				return true
			}
		}
		return false
	}
	if !hasTarget("edge/target1") {
		t.Error("missing edge to edge/target1")
	}
	if !hasTarget("edge/target2") {
		t.Error("missing edge to edge/target2")
	}
}
