package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// failingEmbedder implements embedding.Embedder and always returns an error
// from Embed. Used to simulate "no API key" or otherwise-unusable embedders
// so we can exercise the lexical fallback path.
type failingEmbedder struct{ model string }

func (f *failingEmbedder) Embed(string) ([]float32, error) {
	return nil, errors.New("no api key")
}
func (f *failingEmbedder) EmbedBatch([]string) ([][]float32, error) {
	return nil, errors.New("no api key")
}
func (f *failingEmbedder) Model() string { return f.model }
func (f *failingEmbedder) Dimension() int {
	return 1536
}

// newReadOnlyTestEngine returns a ready-to-use Engine with read-only set
// and a passing mock embedder. Mirrors newClassifyTestEngine.
func newReadOnlyTestEngine(t *testing.T) *Engine {
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
		ReadOnly:       true,
	}
	eng.SetGraph(g)
	return eng
}

func TestHandleContextWrite_ReadOnly(t *testing.T) {
	eng := newReadOnlyTestEngine(t)
	req := makeCallToolRequest("context_write", map[string]any{
		"id":      "test-node",
		"summary": "anything",
	})
	res, err := eng.HandleContextWrite(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for read-only write")
	}
	if got := resultText(t, res); !strings.Contains(got, "read-only") {
		t.Errorf("expected error message to mention read-only, got %q", got)
	}
}

func TestHandleContextDelete_ReadOnly(t *testing.T) {
	eng := newReadOnlyTestEngine(t)
	req := makeCallToolRequest("context_delete", map[string]any{
		"id": "anything",
	})
	res, err := eng.HandleContextDelete(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleContextDelete: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for read-only delete")
	}
	if got := resultText(t, res); !strings.Contains(got, "read-only") {
		t.Errorf("expected error message to mention read-only, got %q", got)
	}
}

func TestHandleContextTag_ReadOnly(t *testing.T) {
	eng := newReadOnlyTestEngine(t)
	req := makeCallToolRequest("context_tag", map[string]any{
		"query": "anything",
		"tag":   "x",
	})
	res, err := eng.HandleContextTag(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleContextTag: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for read-only tag")
	}
	if got := resultText(t, res); !strings.Contains(got, "read-only") {
		t.Errorf("expected error message to mention read-only, got %q", got)
	}
}

// TestHandleContextQuery_ReadOnly verifies queries still work when the engine
// is read-only. Seed the graph + embedding store directly to avoid going through
// the (now-blocked) write handler.
func TestHandleContextQuery_ReadOnly(t *testing.T) {
	eng := newReadOnlyTestEngine(t)

	n := &node.Node{
		ID:        "doc/login",
		Type:      "concept",
		Namespace: "default",
		Status:    node.StatusActive,
		Summary:   "user authentication and login flow",
	}
	if err := eng.GetGraph().UpsertNode(n); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	vec, err := eng.Embedder.Embed(n.Summary)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if err := eng.EmbeddingStore.Upsert(n.ID, vec, "h", eng.Embedder.Model()); err != nil {
		t.Fatalf("EmbeddingStore.Upsert: %v", err)
	}

	req := makeCallToolRequest("context_query", map[string]any{
		"query": "user authentication login",
		"depth": 1,
	})
	res, err := eng.HandleContextQuery(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}
	xml := resultText(t, res)
	if !strings.Contains(xml, "<context_result") {
		t.Errorf("expected XML result envelope, got %q", xml)
	}
}

// TestHandleContextQuery_LexicalFallback verifies that when the embedder is
// unusable, context_query still returns relevant nodes via lexical match.
func TestHandleContextQuery_LexicalFallback(t *testing.T) {
	dir := t.TempDir()
	embStore, err := embedding.NewStore(":memory:")
	if err != nil {
		t.Fatalf("embedding.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = embStore.Close() })

	eng := &Engine{
		NodeStore:      node.NewStore(dir),
		EmbeddingStore: embStore,
		Embedder:       &failingEmbedder{model: "broken"},
		MarmotDir:      dir,
	}
	eng.SetGraph(graph.NewGraph())

	// Seed a few nodes directly into the graph.
	nodes := []*node.Node{
		{ID: "auth/login", Type: "concept", Namespace: "default", Status: node.StatusActive, Summary: "user authentication and login flow"},
		{ID: "render/markdown", Type: "concept", Namespace: "default", Status: node.StatusActive, Summary: "render markdown to HTML"},
		{ID: "billing/invoice", Type: "concept", Namespace: "default", Status: node.StatusActive, Summary: "calculate tax for invoice"},
	}
	for _, n := range nodes {
		if err := eng.GetGraph().UpsertNode(n); err != nil {
			t.Fatalf("UpsertNode(%s): %v", n.ID, err)
		}
	}

	req := makeCallToolRequest("context_query", map[string]any{
		"query": "user authentication login",
		"depth": 1,
	})
	res, err := eng.HandleContextQuery(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}
	xml := resultText(t, res)

	// We should have found auth/login via the lexical path; the unrelated
	// nodes should not appear.
	if !strings.Contains(xml, "auth/login") {
		t.Errorf("expected auth/login in lexical-fallback result, got %q", xml)
	}
	if strings.Contains(xml, "billing/invoice") {
		t.Errorf("did not expect billing/invoice in lexical-fallback result: %q", xml)
	}
}
