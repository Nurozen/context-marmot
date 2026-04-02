package classifier

import (
	"context"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/node"
)

// mockStore is an in-memory mock for EmbeddingStore.
type mockStore struct {
	results []embedding.ScoredResult
	err     error
}

func (m *mockStore) FindSimilar(_ []float32, _ float64, _ string) ([]embedding.ScoredResult, error) {
	return m.results, m.err
}

// mockEmbedder is a no-op embedder for tests.
type mockEmbedder struct{}

func (m *mockEmbedder) Embed(_ string) ([]float32, error) { return []float32{1.0, 0.0}, nil }
func (m *mockEmbedder) Model() string                     { return "mock" }

// mockGraph is an in-memory mock for GraphReader.
type mockGraph struct {
	nodes map[string]*node.Node
}

func (m *mockGraph) GetNode(id string) (*node.Node, bool) {
	n, ok := m.nodes[id]
	return n, ok
}

func newClassifier(store EmbeddingStore, provider llm.Provider) *Classifier {
	return &Classifier{
		Store:    store,
		Embedder: &mockEmbedder{},
		LLM:      provider,
	}
}

// TestClassify_ADD_NoSimilar — FindSimilar returns empty → result is ADD
func TestClassify_ADD_NoSimilar(t *testing.T) {
	c := newClassifier(&mockStore{results: nil}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{}}
	incoming := &node.Node{ID: "ns/node-a", Summary: "some summary", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD, got %s", result.Action)
	}
}

// TestClassify_ADD_AllSelf — FindSimilar returns only the incoming node's own ID → filtered to empty → ADD
func TestClassify_ADD_AllSelf(t *testing.T) {
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/self", Score: 0.99},
	}}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/self": {ID: "ns/self", Summary: "same node", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/self", Summary: "same node", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD, got %s", result.Action)
	}
}

// TestClassify_NOOP_LLMPath — FindSimilar returns a candidate, MockProvider returns NOOP → result is NOOP
func TestClassify_NOOP_LLMPath(t *testing.T) {
	mockLLM := &llm.MockProvider{Result: llm.ClassifyResult{
		Action:       llm.ActionNOOP,
		TargetNodeID: "ns/existing",
		Confidence:   0.98,
		Reasoning:    "identical content",
	}}
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/existing", Score: 0.97},
	}}, mockLLM)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/existing": {ID: "ns/existing", Summary: "existing node", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/new", Summary: "new node content", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionNOOP {
		t.Errorf("expected NOOP, got %s", result.Action)
	}
	if mockLLM.Calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", mockLLM.Calls)
	}
}

// TestClassify_UPDATE_LLMPath — MockProvider returns UPDATE with target ID → result is UPDATE with correct TargetNodeID
func TestClassify_UPDATE_LLMPath(t *testing.T) {
	mockLLM := &llm.MockProvider{Result: llm.ClassifyResult{
		Action:       llm.ActionUPDATE,
		TargetNodeID: "ns/target",
		Confidence:   0.85,
		Reasoning:    "same concept, richer content",
	}}
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/target", Score: 0.83},
	}}, mockLLM)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/target": {ID: "ns/target", Summary: "target node", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/incoming", Summary: "updated content", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionUPDATE {
		t.Errorf("expected UPDATE, got %s", result.Action)
	}
	if result.TargetNodeID != "ns/target" {
		t.Errorf("expected TargetNodeID 'ns/target', got %q", result.TargetNodeID)
	}
}

// TestClassify_SUPERSEDE_LLMPath — MockProvider returns SUPERSEDE → result is SUPERSEDE
func TestClassify_SUPERSEDE_LLMPath(t *testing.T) {
	mockLLM := &llm.MockProvider{Result: llm.ClassifyResult{
		Action:       llm.ActionSUPERSEDE,
		TargetNodeID: "ns/old",
		Confidence:   0.72,
		Reasoning:    "concept evolved",
	}}
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/old", Score: 0.70},
	}}, mockLLM)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/old": {ID: "ns/old", Summary: "old node", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/evolved", Summary: "evolved concept", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionSUPERSEDE {
		t.Errorf("expected SUPERSEDE, got %s", result.Action)
	}
}

// TestClassify_Fallback_NOOP — LLM is nil, FindSimilar returns score 0.97 → fallback returns NOOP
func TestClassify_Fallback_NOOP(t *testing.T) {
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/existing", Score: 0.97},
	}}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/existing": {ID: "ns/existing", Summary: "existing", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/new", Summary: "near identical", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionNOOP {
		t.Errorf("expected NOOP, got %s (score 0.97 >= ThresholdNOOP 0.95)", result.Action)
	}
	if result.TargetNodeID != "ns/existing" {
		t.Errorf("expected TargetNodeID 'ns/existing', got %q", result.TargetNodeID)
	}
}

// TestClassify_Fallback_UPDATE — LLM is nil, score 0.85 → UPDATE
func TestClassify_Fallback_UPDATE(t *testing.T) {
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/existing", Score: 0.85},
	}}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/existing": {ID: "ns/existing", Summary: "existing", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/new", Summary: "richer content", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionUPDATE {
		t.Errorf("expected UPDATE, got %s (score 0.85 >= ThresholdUPDATE 0.80)", result.Action)
	}
	if result.TargetNodeID != "ns/existing" {
		t.Errorf("expected TargetNodeID 'ns/existing', got %q", result.TargetNodeID)
	}
}

// TestClassify_Fallback_SUPERSEDE — LLM is nil, score 0.70 → SUPERSEDE
func TestClassify_Fallback_SUPERSEDE(t *testing.T) {
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/old", Score: 0.70},
	}}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/old": {ID: "ns/old", Summary: "old concept", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/evolved", Summary: "evolved concept", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionSUPERSEDE {
		t.Errorf("expected SUPERSEDE, got %s (score 0.70 >= ThresholdSUPERSEDE 0.65)", result.Action)
	}
	if result.TargetNodeID != "ns/old" {
		t.Errorf("expected TargetNodeID 'ns/old', got %q", result.TargetNodeID)
	}
}

// TestClassify_Fallback_ADD_LowScore — LLM is nil, score 0.55 (below SimilaritySearchThreshold) → FindSimilar filters it → ADD
// The mock store returns no results (simulating threshold filtering), so the result is ADD.
func TestClassify_Fallback_ADD_LowScore(t *testing.T) {
	// Mock store returns empty because score 0.55 is below SimilaritySearchThreshold (0.60).
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{}}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/distant": {ID: "ns/distant", Summary: "distant concept", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/new", Summary: "completely new concept", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD, got %s (score 0.55 below threshold 0.60)", result.Action)
	}
}

// TestClassify_NoContent — incoming node has empty summary and context → ADD immediately
func TestClassify_NoContent(t *testing.T) {
	c := newClassifier(&mockStore{results: nil}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{}}
	incoming := &node.Node{ID: "ns/empty", Summary: "", Context: "", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD, got %s", result.Action)
	}
	if result.Reasoning != "no content to compare" {
		t.Errorf("expected reasoning 'no content to compare', got %q", result.Reasoning)
	}
}
