package classifier

import (
	"context"
	"errors"
	"strings"
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

// errEmbedder always fails on Embed, to exercise the embed-error path.
type errEmbedder struct{}

func (m *errEmbedder) Embed(_ string) ([]float32, error) {
	return nil, errors.New("embed failed")
}
func (m *errEmbedder) Model() string { return "mock" }

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

// TestClassify_ContextTruncated — long Context (>6000 chars) is appended and truncated,
// exercising the embed-text build branch. Uses the LLM path so a result flows through.
func TestClassify_ContextTruncated(t *testing.T) {
	mockLLM := &llm.MockProvider{Result: llm.ClassifyResult{
		Action:       llm.ActionNOOP,
		TargetNodeID: "ns/existing",
	}}
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/existing", Score: 0.9},
	}}, mockLLM)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/existing": {ID: "ns/existing", Summary: "existing", Status: "active"},
	}}
	incoming := &node.Node{
		ID:      "ns/new",
		Summary: "summary",
		Context: strings.Repeat("x", 7000), // > 6000 to trigger truncation
		Status:  "active",
	}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionNOOP {
		t.Errorf("expected NOOP, got %s", result.Action)
	}
}

// TestClassify_ContextShort — short Context (<6000 chars) is appended without truncation.
func TestClassify_ContextShort(t *testing.T) {
	c := newClassifier(&mockStore{results: nil}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{}}
	incoming := &node.Node{
		ID:      "ns/new",
		Summary: "summary",
		Context: "extra context",
		Status:  "active",
	}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD, got %s", result.Action)
	}
}

// TestClassify_EmbedError — Embedder.Embed fails → returns ADD with no error.
func TestClassify_EmbedError(t *testing.T) {
	c := &Classifier{
		Store:    &mockStore{results: nil},
		Embedder: &errEmbedder{},
		LLM:      nil,
	}
	g := &mockGraph{nodes: map[string]*node.Node{}}
	incoming := &node.Node{ID: "ns/new", Summary: "some summary", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD on embed error, got %s", result.Action)
	}
}

// TestClassify_StoreError — FindSimilar returns an error → ADD "no similar nodes found".
func TestClassify_StoreError(t *testing.T) {
	c := newClassifier(&mockStore{err: errors.New("store failed")}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{}}
	incoming := &node.Node{ID: "ns/new", Summary: "some summary", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD on store error, got %s", result.Action)
	}
	if result.Reasoning != "no similar nodes found" {
		t.Errorf("expected reasoning 'no similar nodes found', got %q", result.Reasoning)
	}
}

// TestClassify_CandidatesNotInGraph — all candidates are missing from the graph
// (GetNode returns !ok) → llmCandidates empty → ADD.
func TestClassify_CandidatesNotInGraph(t *testing.T) {
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/ghost", Score: 0.9},
	}}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{}} // ns/ghost not present
	incoming := &node.Node{ID: "ns/new", Summary: "some summary", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD, got %s", result.Action)
	}
	if result.Reasoning != "no distinct similar nodes found" {
		t.Errorf("expected reasoning 'no distinct similar nodes found', got %q", result.Reasoning)
	}
}

// TestClassify_MissingCandidateSkipped — one candidate is missing from the graph
// (GetNode !ok → continue) but a later one resolves, so the LLM path still runs.
func TestClassify_MissingCandidateSkipped(t *testing.T) {
	mockLLM := &llm.MockProvider{Result: llm.ClassifyResult{
		Action:       llm.ActionUPDATE,
		TargetNodeID: "ns/present",
	}}
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/ghost", Score: 0.9},
		{NodeID: "ns/present", Score: 0.85},
	}}, mockLLM)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/present": {ID: "ns/present", Summary: "present", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/new", Summary: "some summary", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionUPDATE {
		t.Errorf("expected UPDATE, got %s", result.Action)
	}
}

// TestClassify_CapsAtFiveCandidates — more than 5 candidates resolve → the loop
// breaks after 5 (only 5 are passed to the LLM).
func TestClassify_CapsAtFiveCandidates(t *testing.T) {
	results := make([]embedding.ScoredResult, 0, 7)
	nodes := map[string]*node.Node{}
	for _, id := range []string{"ns/a", "ns/b", "ns/c", "ns/d", "ns/e", "ns/f", "ns/g"} {
		results = append(results, embedding.ScoredResult{NodeID: id, Score: 0.9})
		nodes[id] = &node.Node{ID: id, Summary: "node " + id, Status: "active"}
	}

	var got llm.ClassifyRequest
	mockLLM := &recordingProvider{result: llm.ClassifyResult{Action: llm.ActionADD}, captured: &got}
	c := newClassifier(&mockStore{results: results}, mockLLM)
	g := &mockGraph{nodes: nodes}
	incoming := &node.Node{ID: "ns/new", Summary: "some summary", Status: "active"}

	if _, err := c.Classify(context.Background(), incoming, g); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Candidates) != 5 {
		t.Errorf("expected candidates capped at 5, got %d", len(got.Candidates))
	}
}

// TestClassify_Fallback_ADD_BelowSupersede — LLM is nil, best candidate score is
// above the search threshold but below ThresholdSUPERSEDE → fallback default ADD.
func TestClassify_Fallback_ADD_BelowSupersede(t *testing.T) {
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "ns/weak", Score: 0.62}, // 0.60 <= 0.62 < 0.65
	}}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{
		"ns/weak": {ID: "ns/weak", Summary: "weakly similar", Status: "active"},
	}}
	incoming := &node.Node{ID: "ns/new", Summary: "some summary", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD, got %s (score 0.62 below ThresholdSUPERSEDE 0.65)", result.Action)
	}
	if result.Reasoning != "no sufficiently similar node" {
		t.Errorf("expected reasoning 'no sufficiently similar node', got %q", result.Reasoning)
	}
}

// recordingProvider captures the ClassifyRequest it receives for assertions.
type recordingProvider struct {
	result   llm.ClassifyResult
	captured *llm.ClassifyRequest
}

func (m *recordingProvider) Classify(_ context.Context, req llm.ClassifyRequest) (llm.ClassifyResult, error) {
	*m.captured = req
	return m.result, nil
}
func (m *recordingProvider) Model() string { return "mock" }
func (m *recordingProvider) Summarize(_ context.Context, _ llm.SummarizeRequest) (string, error) {
	return "", nil
}
func (m *recordingProvider) Chat(_ context.Context, _ llm.ChatRequest) (string, error) {
	return "", nil
}

// TestClassify_Fallback_SkipsParentChild — regression for manual_test.md
// agent-1 issue 1: a file/module node and its own child function have
// near-identical embeddings, but the fallback must never NOOP/UPDATE/SUPERSEDE
// across a parent/child (path-prefix) relationship.
func TestClassify_Fallback_SkipsParentChild(t *testing.T) {
	// Child function vs. its parent file node: only related candidate → ADD.
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "web/src/cart", Score: 0.75},
	}}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{
		"web/src/cart": {ID: "web/src/cart", Summary: "cart module", Status: "active"},
	}}
	incoming := &node.Node{ID: "web/src/cart/renderCartSummary", Summary: "renders the cart summary", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionADD {
		t.Errorf("expected ADD for parent candidate, got %s (target %s)", result.Action, result.TargetNodeID)
	}

	// Parent file node vs. its own child: same rule in the other direction.
	c2 := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "web/src/cart/renderCartSummary", Score: 0.97},
	}}, nil)
	g2 := &mockGraph{nodes: map[string]*node.Node{
		"web/src/cart/renderCartSummary": {ID: "web/src/cart/renderCartSummary", Summary: "renders the cart summary", Status: "active"},
	}}
	incoming2 := &node.Node{ID: "web/src/cart", Summary: "cart module", Status: "active"}

	result2, err := c2.Classify(context.Background(), incoming2, g2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result2.Action != llm.ActionADD {
		t.Errorf("expected ADD for child candidate, got %s (target %s)", result2.Action, result2.TargetNodeID)
	}
}

// TestClassify_Fallback_PrefersUnrelatedCandidate — when both a related
// (parent) and an unrelated candidate are similar, the fallback must use the
// unrelated one.
func TestClassify_Fallback_PrefersUnrelatedCandidate(t *testing.T) {
	c := newClassifier(&mockStore{results: []embedding.ScoredResult{
		{NodeID: "web/src/cart", Score: 0.80},       // parent — must be skipped
		{NodeID: "web/src/legacyCart", Score: 0.70}, // unrelated — usable
	}}, nil)
	g := &mockGraph{nodes: map[string]*node.Node{
		"web/src/cart":       {ID: "web/src/cart", Summary: "cart module", Status: "active"},
		"web/src/legacyCart": {ID: "web/src/legacyCart", Summary: "legacy cart summary renderer", Status: "active"},
	}}
	incoming := &node.Node{ID: "web/src/cart/renderCartSummary", Summary: "renders the cart summary", Status: "active"}

	result, err := c.Classify(context.Background(), incoming, g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != llm.ActionSUPERSEDE {
		t.Errorf("expected SUPERSEDE against unrelated candidate, got %s", result.Action)
	}
	if result.TargetNodeID != "web/src/legacyCart" {
		t.Errorf("expected target web/src/legacyCart, got %q", result.TargetNodeID)
	}
}

func TestIsHierarchicallyRelated(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"web/src/cart", "web/src/cart/renderCartSummary", true},
		{"web/src/cart/renderCartSummary", "web/src/cart", true},
		{"web/src/cart", "web/src/cartography", false},
		{"web/src/cart", "web/src/legacyCart", false},
		{"main", "main/Hello", true},
		{"a", "b", false},
	}
	for _, tc := range cases {
		if got := isHierarchicallyRelated(tc.a, tc.b); got != tc.want {
			t.Errorf("isHierarchicallyRelated(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
