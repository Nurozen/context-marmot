package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nurozen/context-marmot/internal/classifier"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/node"
)

// newClassifyTestEngine creates a fresh Engine with an in-memory embedding
// store and a mock embedder. Unlike testEngine, it does NOT use NewEngine
// (which opens a real SQLite file) — it constructs the engine directly so
// that we can use an in-memory ":memory:" SQLite database.
func newClassifyTestEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()

	embStore, err := embedding.NewStore(":memory:")
	if err != nil {
		t.Fatalf("embedding.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = embStore.Close() })

	emb := embedding.NewMockEmbedder("test-model")
	g := graph.NewGraph()
	ns := node.NewStore(dir)

	eng := &Engine{
		NodeStore:      ns,
		EmbeddingStore: embStore,
		Embedder:       emb,
		MarmotDir:      dir,
	}
	eng.SetGraph(g)
	return eng
}

// writeNodeDirect writes a node directly (no classifier) using the same summary
// for both calls so that the mock embedder produces similar vectors.
func writeNodeDirect(t *testing.T, eng *Engine, id, summary string) WriteResult {
	t.Helper()
	ctx := context.Background()
	req := makeCallToolRequest("context_write", map[string]any{
		"id":      id,
		"type":    "concept",
		"summary": summary,
	})
	res, err := eng.HandleContextWrite(ctx, req)
	if err != nil {
		t.Fatalf("HandleContextWrite(%s): %v", id, err)
	}
	if res.IsError {
		t.Fatalf("write %s returned error: %s", id, resultText(t, res))
	}
	var wr WriteResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &wr); err != nil {
		t.Fatalf("unmarshal WriteResult(%s): %v", id, err)
	}
	return wr
}

// TestContextWrite_Classify_ADD verifies that when the classifier returns ADD,
// a new node is created with status "created".
func TestContextWrite_Classify_ADD(t *testing.T) {
	eng := newClassifyTestEngine(t)

	// Wire classifier with MockProvider returning ADD.
	eng.Classifier = &classifier.Classifier{
		Store:    eng.EmbeddingStore,
		Embedder: eng.Embedder,
		LLM: &llm.MockProvider{
			Result: llm.ClassifyResult{
				Action:    llm.ActionADD,
				Reasoning: "new concept",
			},
		},
	}

	ctx := context.Background()
	req := makeCallToolRequest("context_write", map[string]any{
		"id":      "classify/add-node",
		"type":    "concept",
		"summary": "A brand new concept about distributed tracing",
	})

	res, err := eng.HandleContextWrite(ctx, req)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("write returned error: %s", resultText(t, res))
	}

	var wr WriteResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &wr); err != nil {
		t.Fatalf("unmarshal WriteResult: %v", err)
	}

	if wr.Status != "created" {
		t.Errorf("expected status=created, got %s", wr.Status)
	}
	if wr.NodeID != "classify/add-node" {
		t.Errorf("expected node_id=classify/add-node, got %s", wr.NodeID)
	}
	if wr.Hash == "" {
		t.Error("expected non-empty hash")
	}

	// Node must exist in graph.
	if _, ok := eng.GetGraph().GetNode("classify/add-node"); !ok {
		t.Error("node not found in graph after ADD write")
	}
}

// TestContextWrite_Classify_UPDATE verifies that when the classifier returns
// UPDATE for an existing node, the write proceeds and status is "updated".
// We use nearly identical summaries so the mock embedder produces similar
// vectors and FindSimilar returns a candidate, allowing the MockProvider to
// be called.
func TestContextWrite_Classify_UPDATE(t *testing.T) {
	eng := newClassifyTestEngine(t)

	// Use a shared summary so embeddings are highly similar.
	sharedSummary := "OAuth2 authentication flow with PKCE support and token rotation"

	// Write node "classify/a" without classifier first.
	writeNodeDirect(t, eng, "classify/a", sharedSummary)

	// Wire classifier with MockProvider returning UPDATE targeting "classify/a".
	eng.Classifier = &classifier.Classifier{
		Store:    eng.EmbeddingStore,
		Embedder: eng.Embedder,
		LLM: &llm.MockProvider{
			Result: llm.ClassifyResult{
				Action:       llm.ActionUPDATE,
				TargetNodeID: "classify/a",
				Reasoning:    "richer content for same concept",
			},
		},
	}

	ctx := context.Background()
	// Write "classify/a" again with the same summary (triggers FindSimilar hit).
	req2 := makeCallToolRequest("context_write", map[string]any{
		"id":      "classify/a",
		"type":    "concept",
		"summary": sharedSummary,
	})
	res2, err := eng.HandleContextWrite(ctx, req2)
	if err != nil {
		t.Fatalf("HandleContextWrite (update): %v", err)
	}
	if res2.IsError {
		t.Fatalf("write returned error: %s", resultText(t, res2))
	}

	var wr WriteResult
	if err := json.Unmarshal([]byte(resultText(t, res2)), &wr); err != nil {
		t.Fatalf("unmarshal WriteResult: %v", err)
	}

	if wr.Status != "updated" {
		t.Errorf("expected status=updated, got %s", wr.Status)
	}
	if wr.NodeID != "classify/a" {
		t.Errorf("expected node_id=classify/a, got %s", wr.NodeID)
	}

	// Graph should still have exactly 1 node.
	if eng.GetGraph().NodeCount() != 1 {
		t.Errorf("expected 1 node in graph, got %d", eng.GetGraph().NodeCount())
	}
}

// TestContextWrite_Classify_SUPERSEDE verifies that when the classifier returns
// SUPERSEDE for "old", the old node is soft-deleted and the new node "new" is
// created.
// We use the same summary for both nodes so FindSimilar returns a candidate.
func TestContextWrite_Classify_SUPERSEDE(t *testing.T) {
	eng := newClassifyTestEngine(t)

	// Use a shared summary so embeddings are highly similar and FindSimilar
	// returns "classify/old" as a candidate when "classify/new" is written.
	sharedSummary := "Authentication concept with session management and token validation"

	// Write the "old" node without classifier.
	writeNodeDirect(t, eng, "classify/old", sharedSummary)

	// Wire classifier with MockProvider returning SUPERSEDE targeting "classify/old".
	eng.Classifier = &classifier.Classifier{
		Store:    eng.EmbeddingStore,
		Embedder: eng.Embedder,
		LLM: &llm.MockProvider{
			Result: llm.ClassifyResult{
				Action:       llm.ActionSUPERSEDE,
				TargetNodeID: "classify/old",
				Reasoning:    "concept evolved significantly",
			},
		},
	}

	ctx := context.Background()
	// Write the "new" node using the same summary so FindSimilar triggers.
	reqNew := makeCallToolRequest("context_write", map[string]any{
		"id":      "classify/new",
		"type":    "concept",
		"summary": sharedSummary,
	})
	resNew, err := eng.HandleContextWrite(ctx, reqNew)
	if err != nil {
		t.Fatalf("HandleContextWrite (new): %v", err)
	}
	if resNew.IsError {
		t.Fatalf("write new node returned error: %s", resultText(t, resNew))
	}

	var wr WriteResult
	if err := json.Unmarshal([]byte(resultText(t, resNew)), &wr); err != nil {
		t.Fatalf("unmarshal WriteResult: %v", err)
	}

	// New node should be created.
	if wr.NodeID != "classify/new" {
		t.Errorf("expected node_id=classify/new, got %s", wr.NodeID)
	}
	if wr.Hash == "" {
		t.Error("expected non-empty hash for new node")
	}

	// Old node should be superseded — load from disk and check status.
	oldPath := eng.NodeStore.NodePath("classify/old")
	oldNode, err := eng.NodeStore.LoadNode(oldPath)
	if err != nil {
		t.Fatalf("load old node from disk: %v", err)
	}
	if oldNode.Status != node.StatusSuperseded {
		t.Errorf("expected old node status=superseded, got %s", oldNode.Status)
	}
	if oldNode.SupersededBy != "classify/new" {
		t.Errorf("expected old node superseded_by=classify/new, got %s", oldNode.SupersededBy)
	}

	// New node should exist in graph.
	if _, ok := eng.GetGraph().GetNode("classify/new"); !ok {
		t.Error("new node not found in graph after SUPERSEDE write")
	}
}

// TestContextWrite_Classify_NOOP verifies that when the classifier returns
// NOOP, no write occurs and the result returns the existing node's hash with
// status "noop".
// We use the same summary for both writes so FindSimilar returns a candidate.
func TestContextWrite_Classify_NOOP(t *testing.T) {
	eng := newClassifyTestEngine(t)

	sharedSummary := "Stable concept about database connection pooling with configurable pool size"

	// Write "classify/existing" node (no classifier).
	wrFirst := writeNodeDirect(t, eng, "classify/existing", sharedSummary)
	originalHash := wrFirst.Hash

	// Wire classifier with MockProvider returning NOOP targeting "classify/existing".
	eng.Classifier = &classifier.Classifier{
		Store:    eng.EmbeddingStore,
		Embedder: eng.Embedder,
		LLM: &llm.MockProvider{
			Result: llm.ClassifyResult{
				Action:       llm.ActionNOOP,
				TargetNodeID: "classify/existing",
				Confidence:   0.99,
				Reasoning:    "near-identical content",
			},
		},
	}

	ctx := context.Background()
	// Attempt to write "classify/noop-attempt" using the same summary — should be a NOOP.
	reqNoop := makeCallToolRequest("context_write", map[string]any{
		"id":      "classify/noop-attempt",
		"type":    "concept",
		"summary": sharedSummary,
	})
	resNoop, err := eng.HandleContextWrite(ctx, reqNoop)
	if err != nil {
		t.Fatalf("HandleContextWrite (noop): %v", err)
	}
	if resNoop.IsError {
		t.Fatalf("noop write returned error: %s", resultText(t, resNoop))
	}

	var wr WriteResult
	if err := json.Unmarshal([]byte(resultText(t, resNoop)), &wr); err != nil {
		t.Fatalf("unmarshal noop WriteResult: %v", err)
	}

	if wr.Status != "noop" {
		t.Errorf("expected status=noop, got %s", wr.Status)
	}
	if wr.NodeID != "classify/existing" {
		t.Errorf("expected node_id=classify/existing (the existing node), got %s", wr.NodeID)
	}
	if wr.Hash != originalHash {
		t.Errorf("expected unchanged hash %s, got %s", originalHash, wr.Hash)
	}

	// The noop-attempt node should NOT have been written to the graph.
	if _, ok := eng.GetGraph().GetNode("classify/noop-attempt"); ok {
		t.Error("noop-attempt node should not exist in graph after NOOP")
	}

	// Graph should still have exactly 1 node (the original "existing").
	if eng.GetGraph().NodeCount() != 1 {
		t.Errorf("expected 1 node in graph after NOOP, got %d", eng.GetGraph().NodeCount())
	}
}
