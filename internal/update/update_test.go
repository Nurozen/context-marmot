package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockNodeStore is an in-memory node store for testing.
type mockNodeStore struct {
	nodes map[string]*node.Node // keyed by ID
	dir   string                // temp dir for NodePath
}

func newMockNodeStore(dir string) *mockNodeStore {
	return &mockNodeStore{
		nodes: make(map[string]*node.Node),
		dir:   dir,
	}
}

func (m *mockNodeStore) LoadNode(path string) (*node.Node, error) {
	// Derive ID from path: strip dir prefix and .md suffix.
	rel, err := filepath.Rel(m.dir, path)
	if err != nil {
		return nil, err
	}
	if len(rel) < 4 {
		return nil, fmt.Errorf("path too short: %s", path)
	}
	id := rel[:len(rel)-3] // strip .md
	n, ok := m.nodes[id]
	if !ok {
		return nil, fmt.Errorf("node %q not found", id)
	}
	// Return a copy to avoid mutation issues.
	cp := *n
	cp.Source = n.Source
	cp.Edges = append([]node.Edge(nil), n.Edges...)
	return &cp, nil
}

func (m *mockNodeStore) SaveNode(n *node.Node) error {
	m.nodes[n.ID] = n
	return nil
}

func (m *mockNodeStore) NodePath(id string) string {
	return filepath.Join(m.dir, id+".md")
}

func (m *mockNodeStore) ListActiveNodes() ([]node.NodeMeta, error) {
	var metas []node.NodeMeta
	for _, n := range m.nodes {
		if n.IsActive() {
			metas = append(metas, node.NodeMeta{ID: n.ID, Status: n.Status})
		}
	}
	return metas, nil
}

// mockGraph implements GraphReader for testing.
type mockGraph struct {
	nodes    map[string]*node.Node
	outEdges map[string][]node.Edge
	inEdges  map[string][]node.Edge
}

func newMockGraph() *mockGraph {
	return &mockGraph{
		nodes:    make(map[string]*node.Node),
		outEdges: make(map[string][]node.Edge),
		inEdges:  make(map[string][]node.Edge),
	}
}

func (g *mockGraph) GetNode(id string) (*node.Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

func (g *mockGraph) GetEdges(id string, direction graph.Direction) []node.Edge {
	switch direction {
	case graph.Outbound:
		return g.outEdges[id]
	case graph.Inbound:
		return g.inEdges[id]
	default:
		return nil
	}
}

// addNode adds a node and registers its edges in both forward and reverse maps.
func (g *mockGraph) addNode(n *node.Node) {
	g.nodes[n.ID] = n
	for _, e := range n.Edges {
		g.outEdges[n.ID] = append(g.outEdges[n.ID], e)
		rev := node.Edge{Target: n.ID, Relation: e.Relation}
		g.inEdges[e.Target] = append(g.inEdges[e.Target], rev)
	}
}

// mockEmbeddingStore records Upsert calls.
type mockEmbeddingStore struct {
	upserted map[string]upsertRecord
}

type upsertRecord struct {
	embedding   []float32
	summaryHash string
	model       string
}

func newMockEmbeddingStore() *mockEmbeddingStore {
	return &mockEmbeddingStore{upserted: make(map[string]upsertRecord)}
}

func (m *mockEmbeddingStore) Upsert(nodeID string, embedding []float32, summaryHash string, model string) error {
	m.upserted[nodeID] = upsertRecord{embedding: embedding, summaryHash: summaryHash, model: model}
	return nil
}

// mockEmbedder returns a fixed vector for any text.
type mockEmbedder struct {
	dim       int
	modelName string
}

func newMockEmbedder() *mockEmbedder {
	return &mockEmbedder{dim: 4, modelName: "test-model"}
}

func (m *mockEmbedder) Embed(_ string) ([]float32, error) {
	v := make([]float32, m.dim)
	for i := range v {
		v[i] = 0.1 * float32(i+1)
	}
	return v, nil
}

func (m *mockEmbedder) Model() string {
	return m.modelName
}

// ---------------------------------------------------------------------------
// Helper to create a temp source file with given content
// ---------------------------------------------------------------------------

func writeTempSource(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDetectChanges(t *testing.T) {
	dir := t.TempDir()
	srcPath := writeTempSource(t, dir, "main.go", "package main\nfunc main() {}\n")

	store := newMockNodeStore(dir)
	store.nodes["A"] = &node.Node{
		ID:     "A",
		Status: node.StatusActive,
		Source: node.Source{
			Path: srcPath,
			Hash: "wrong-hash-value",
		},
		Summary: "Node A",
	}

	mg := newMockGraph()
	eng := NewEngine(store, mg, newMockEmbeddingStore(), newMockEmbedder())

	changed, err := eng.DetectChanges(context.Background())
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed, got %d", len(changed))
	}
	if changed[0].NodeID != "A" {
		t.Errorf("expected node A, got %s", changed[0].NodeID)
	}
	if changed[0].StoredHash != "wrong-hash-value" {
		t.Errorf("unexpected stored hash: %s", changed[0].StoredHash)
	}
	if changed[0].CurrentHash == "" {
		t.Error("current hash should not be empty for an existing file")
	}
}

func TestDetectChangesNoChange(t *testing.T) {
	// When the stored hash matches the current file, no changes should be detected.
	dir := t.TempDir()
	content := "package main\n"
	srcPath := writeTempSource(t, dir, "same.go", content)

	store := newMockNodeStore(dir)
	// Use the engine to detect the actual hash first, then store it.
	mg := newMockGraph()
	eng := NewEngine(store, mg, newMockEmbeddingStore(), newMockEmbedder())

	// Put a node with the wrong hash, detect, get actual hash, then re-test.
	store.nodes["S"] = &node.Node{
		ID:     "S",
		Status: node.StatusActive,
		Source: node.Source{Path: srcPath, Hash: "placeholder"},
		Summary: "Same node",
	}
	changed, _ := eng.DetectChanges(context.Background())
	if len(changed) == 0 {
		t.Fatal("should have detected a change with placeholder hash")
	}
	actualHash := changed[0].CurrentHash

	// Now set the correct hash and re-detect.
	store.nodes["S"].Source.Hash = actualHash
	changed2, err := eng.DetectChanges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed2) != 0 {
		t.Errorf("expected 0 changes when hash matches, got %d", len(changed2))
	}
}

func TestDetectChangesNoSource(t *testing.T) {
	dir := t.TempDir()
	store := newMockNodeStore(dir)
	store.nodes["B"] = &node.Node{
		ID:      "B",
		Status:  node.StatusActive,
		Summary: "Node without source",
	}

	mg := newMockGraph()
	eng := NewEngine(store, mg, newMockEmbeddingStore(), newMockEmbedder())

	changed, err := eng.DetectChanges(context.Background())
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("expected 0 changed, got %d", len(changed))
	}
}

func TestDetectChangesMissingFile(t *testing.T) {
	dir := t.TempDir()
	store := newMockNodeStore(dir)
	store.nodes["C"] = &node.Node{
		ID:     "C",
		Status: node.StatusActive,
		Source: node.Source{
			Path: filepath.Join(dir, "nonexistent.go"),
			Hash: "some-hash",
		},
		Summary: "Node with missing source",
	}

	mg := newMockGraph()
	eng := NewEngine(store, mg, newMockEmbeddingStore(), newMockEmbedder())

	changed, err := eng.DetectChanges(context.Background())
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed, got %d", len(changed))
	}
	if changed[0].CurrentHash != "" {
		t.Errorf("expected empty current hash for missing file, got %q", changed[0].CurrentHash)
	}
}

func TestPropagateStale(t *testing.T) {
	// Build graph: A -> B -> C (outbound edges).
	// Changing C should propagate to B (depth 1) and A (depth 2) via inbound edges.
	mg := newMockGraph()
	mg.addNode(&node.Node{
		ID:    "A",
		Edges: []node.Edge{{Target: "B", Relation: node.References}},
	})
	mg.addNode(&node.Node{
		ID:    "B",
		Edges: []node.Edge{{Target: "C", Relation: node.References}},
	})
	mg.addNode(&node.Node{ID: "C"})

	eng := NewEngine(nil, mg, nil, nil)

	affected := eng.PropagateStale([]string{"C"}, 3)
	// C is changed (depth 0), B depends on C (depth 1), A depends on B (depth 2).
	if len(affected) != 3 {
		t.Fatalf("expected 3 affected, got %d: %+v", len(affected), affected)
	}

	byID := make(map[string]AffectedNode)
	for _, a := range affected {
		byID[a.NodeID] = a
	}

	if byID["C"].Depth != 0 || byID["C"].Reason != "source_changed" {
		t.Errorf("unexpected C: %+v", byID["C"])
	}
	if byID["B"].Depth != 1 {
		t.Errorf("expected B at depth 1, got %d", byID["B"].Depth)
	}
	if byID["A"].Depth != 2 {
		t.Errorf("expected A at depth 2, got %d", byID["A"].Depth)
	}
}

func TestPropagateStaleMaxDepth(t *testing.T) {
	// A -> B -> C, change C with maxDepth=1: only B should be found.
	mg := newMockGraph()
	mg.addNode(&node.Node{
		ID:    "A",
		Edges: []node.Edge{{Target: "B", Relation: node.References}},
	})
	mg.addNode(&node.Node{
		ID:    "B",
		Edges: []node.Edge{{Target: "C", Relation: node.References}},
	})
	mg.addNode(&node.Node{ID: "C"})

	eng := NewEngine(nil, mg, nil, nil)

	affected := eng.PropagateStale([]string{"C"}, 1)
	// C (depth 0) + B (depth 1). A should NOT be included.
	if len(affected) != 2 {
		t.Fatalf("expected 2 affected, got %d: %+v", len(affected), affected)
	}

	for _, a := range affected {
		if a.NodeID == "A" {
			t.Error("A should not be reached with maxDepth=1")
		}
	}
}

func TestPropagateStaleNoDuplicates(t *testing.T) {
	// Diamond: A -> B, A -> C, B -> D, C -> D.
	// Change D: both B and C depend on D, A depends on both B and C.
	// Each node should appear exactly once.
	mg := newMockGraph()
	mg.addNode(&node.Node{
		ID: "A",
		Edges: []node.Edge{
			{Target: "B", Relation: node.References},
			{Target: "C", Relation: node.References},
		},
	})
	mg.addNode(&node.Node{
		ID:    "B",
		Edges: []node.Edge{{Target: "D", Relation: node.References}},
	})
	mg.addNode(&node.Node{
		ID:    "C",
		Edges: []node.Edge{{Target: "D", Relation: node.References}},
	})
	mg.addNode(&node.Node{ID: "D"})

	eng := NewEngine(nil, mg, nil, nil)

	affected := eng.PropagateStale([]string{"D"}, 3)

	counts := make(map[string]int)
	for _, a := range affected {
		counts[a.NodeID]++
	}

	for id, count := range counts {
		if count > 1 {
			t.Errorf("node %s appears %d times, expected 1", id, count)
		}
	}

	// Should have D, B, C, A = 4 nodes.
	if len(affected) != 4 {
		t.Errorf("expected 4 affected nodes, got %d", len(affected))
	}
}

func TestReindex(t *testing.T) {
	dir := t.TempDir()
	srcPath := writeTempSource(t, dir, "main.go", "package main\n")

	store := newMockNodeStore(dir)
	store.nodes["X"] = &node.Node{
		ID:     "X",
		Status: node.StatusActive,
		Source: node.Source{
			Path: srcPath,
			Hash: "old-hash",
		},
		Summary: "Node X summary",
	}

	emb := newMockEmbeddingStore()
	eng := NewEngine(store, newMockGraph(), emb, newMockEmbedder())

	result := eng.Reindex(context.Background(), []string{"X"})
	if len(result.Updated) != 1 {
		t.Fatalf("expected 1 updated, got %d", len(result.Updated))
	}
	if len(result.Failed) != 0 {
		t.Fatalf("expected 0 failed, got %d: %v", len(result.Failed), result.Errors)
	}

	// Verify the hash was updated in the store.
	updated := store.nodes["X"]
	if updated.Source.Hash == "old-hash" {
		t.Error("source hash should have been updated")
	}
	if updated.Source.Hash == "" {
		t.Error("source hash should not be empty for an existing file")
	}
}

func TestReindexEmbedding(t *testing.T) {
	dir := t.TempDir()
	srcPath := writeTempSource(t, dir, "lib.go", "package lib\n")

	store := newMockNodeStore(dir)
	store.nodes["Y"] = &node.Node{
		ID:     "Y",
		Status: node.StatusActive,
		Source: node.Source{
			Path: srcPath,
			Hash: "old",
		},
		Summary: "Node Y",
		Context: "Some context for Y",
	}

	emb := newMockEmbeddingStore()
	embedder := newMockEmbedder()
	eng := NewEngine(store, newMockGraph(), emb, embedder)

	eng.Reindex(context.Background(), []string{"Y"})

	rec, ok := emb.upserted["Y"]
	if !ok {
		t.Fatal("embedding should have been upserted for Y")
	}
	if rec.model != "test-model" {
		t.Errorf("expected model test-model, got %s", rec.model)
	}
	if len(rec.embedding) != 4 {
		t.Errorf("expected 4-dim embedding, got %d", len(rec.embedding))
	}
}

func TestRunBatchUpdate(t *testing.T) {
	dir := t.TempDir()
	srcPath := writeTempSource(t, dir, "app.go", "package app\n")

	store := newMockNodeStore(dir)
	store.nodes["N1"] = &node.Node{
		ID:     "N1",
		Status: node.StatusActive,
		Source: node.Source{
			Path: srcPath,
			Hash: "stale-hash",
		},
		Summary: "N1 summary",
	}
	store.nodes["N2"] = &node.Node{
		ID:      "N2",
		Status:  node.StatusActive,
		Summary: "N2 summary",
		Edges:   []node.Edge{{Target: "N1", Relation: node.References}},
	}

	mg := newMockGraph()
	mg.addNode(store.nodes["N1"])
	mg.addNode(store.nodes["N2"])

	emb := newMockEmbeddingStore()
	eng := NewEngine(store, mg, emb, newMockEmbedder())

	result, err := eng.RunBatchUpdate(context.Background(), 3)
	if err != nil {
		t.Fatalf("RunBatchUpdate: %v", err)
	}

	if len(result.Changed) != 1 {
		t.Errorf("expected 1 changed, got %d", len(result.Changed))
	}
	if len(result.Changed) > 0 && result.Changed[0].NodeID != "N1" {
		t.Errorf("expected changed node N1, got %s", result.Changed[0].NodeID)
	}

	// N1 (direct) + N2 (dependency) = 2 affected.
	if len(result.Affected) != 2 {
		t.Errorf("expected 2 affected, got %d", len(result.Affected))
	}
}

func TestOnChangeCallback(t *testing.T) {
	dir := t.TempDir()
	srcPath := writeTempSource(t, dir, "cb.go", "package cb\n")

	store := newMockNodeStore(dir)
	store.nodes["CB"] = &node.Node{
		ID:     "CB",
		Status: node.StatusActive,
		Source: node.Source{
			Path: srcPath,
			Hash: "old",
		},
		Summary: "Callback node",
	}

	var callbackCount int
	eng := NewEngine(store, newMockGraph(), newMockEmbeddingStore(), newMockEmbedder())
	eng.WithOnChange(func(count int) {
		callbackCount = count
	})

	eng.Reindex(context.Background(), []string{"CB"})

	if callbackCount != 1 {
		t.Errorf("expected callback with count=1, got %d", callbackCount)
	}
}

func TestOnChangeCallbackNotCalledOnFailure(t *testing.T) {
	dir := t.TempDir()
	store := newMockNodeStore(dir)
	// Node "MISSING" is not in the store, so LoadNode will fail.

	var called bool
	eng := NewEngine(store, newMockGraph(), newMockEmbeddingStore(), newMockEmbedder())
	eng.WithOnChange(func(_ int) {
		called = true
	})

	result := eng.Reindex(context.Background(), []string{"MISSING"})
	if called {
		t.Error("callback should not be called when all nodes fail")
	}
	if len(result.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(result.Failed))
	}
}

func TestWatcherConfig(t *testing.T) {
	cfg := DefaultWatcherConfig()
	if cfg.Debounce != 2*time.Second {
		t.Errorf("expected 2s debounce, got %v", cfg.Debounce)
	}
	if cfg.PropagateDepth != 3 {
		t.Errorf("expected depth 3, got %d", cfg.PropagateDepth)
	}
}

func TestNewWatcherInvalidPath(t *testing.T) {
	eng := NewEngine(nil, nil, nil, nil)
	cfg := WatcherConfig{
		Paths:    []string{"/nonexistent/path/that/should/not/exist"},
		Debounce: time.Second,
	}
	_, err := NewWatcher(eng, cfg)
	if err == nil {
		t.Fatal("expected error for non-existent watch path")
	}
}

func TestNewWatcherValidPath(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(nil, nil, nil, nil)
	cfg := WatcherConfig{
		Paths:    []string{dir},
		Debounce: time.Second,
	}
	w, err := NewWatcher(eng, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("stop error: %v", err)
	}
}
