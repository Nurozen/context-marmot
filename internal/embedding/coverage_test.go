package embedding

import (
	"container/heap"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newTestOpenAIEmbedderNoServer creates an embedder that never issues real
// network requests (used for input-validation paths that return early).
func newTestOpenAIEmbedderNoServer(t *testing.T) *OpenAIEmbedder {
	t.Helper()
	e, err := NewOpenAIEmbedder("sk-test", "text-embedding-3-small")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// --- mock.go ---

func TestMockEmbedContext(t *testing.T) {
	m := NewMockEmbedder("test-model")

	// Normal context succeeds.
	vec, err := m.EmbedContext(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("EmbedContext: %v", err)
	}
	if len(vec) != m.Dimension() {
		t.Errorf("expected dimension %d, got %d", m.Dimension(), len(vec))
	}

	// Cancelled context returns the context error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.EmbedContext(ctx, "hello world"); err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestMockEmbedBatch(t *testing.T) {
	m := NewMockEmbedder("test-model")

	texts := []string{"alpha", "beta", "gamma"}
	results, err := m.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(results) != len(texts) {
		t.Fatalf("expected %d results, got %d", len(texts), len(results))
	}
	for i, vec := range results {
		if len(vec) != m.Dimension() {
			t.Errorf("result %d: expected dimension %d, got %d", i, m.Dimension(), len(vec))
		}
	}

	// Empty batch.
	empty, err := m.EmbedBatch(nil)
	if err != nil {
		t.Fatalf("EmbedBatch(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 results for empty batch, got %d", len(empty))
	}
}

func TestMockEmbedEmptyText(t *testing.T) {
	m := NewMockEmbedder("test-model")

	vec, err := m.Embed("")
	if err != nil {
		t.Fatalf("Embed(empty): %v", err)
	}
	if len(vec) != m.Dimension() {
		t.Fatalf("expected dimension %d, got %d", m.Dimension(), len(vec))
	}
	if vec[0] != 1.0 {
		t.Errorf("expected vec[0]=1.0 for empty text, got %f", vec[0])
	}
}

func TestMockEmbedShortText(t *testing.T) {
	m := NewMockEmbedder("test-model")

	// Text shorter than the trigram window (3) exercises the windowSize clamp.
	for _, s := range []string{"a", "ab"} {
		vec, err := m.Embed(s)
		if err != nil {
			t.Fatalf("Embed(%q): %v", s, err)
		}
		if len(vec) != m.Dimension() {
			t.Errorf("Embed(%q): expected dimension %d, got %d", s, m.Dimension(), len(vec))
		}
	}
}

// --- openai.go ---

func TestOpenAIEmbedContext_WrongCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return two embeddings for a single-text request.
		resp := openaiResponse{Data: []openaiEmbedding{
			{Embedding: make([]float32, 1536), Index: 0},
			{Embedding: make([]float32, 1536), Index: 1},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	_, err := e.EmbedContext(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for wrong embedding count")
	}
	if !strings.Contains(err.Error(), "expected 1 embedding") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpenAIEmbedContext_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call so the retry-backoff select hits ctx.Done()

	_, err := e.EmbedContext(ctx, "hello")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestOpenAIEmbedBatch_Empty(t *testing.T) {
	e := newTestOpenAIEmbedderNoServer(t)
	results, err := e.EmbedBatch(nil)
	if err != nil {
		t.Fatalf("EmbedBatch(nil): %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty batch, got %v", results)
	}
}

func TestOpenAIEmbedBatch_CountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a single embedding regardless of how many inputs were sent.
		resp := openaiResponse{Data: []openaiEmbedding{
			{Embedding: make([]float32, 1536), Index: 0},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	_, err := e.EmbedBatch([]string{"a", "b", "c"})
	if err == nil {
		t.Fatal("expected error for embedding count mismatch")
	}
	if !strings.Contains(err.Error(), "expected 3 embeddings") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpenAIEmbedBatch_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openaiRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		var n int
		switch v := req.Input.(type) {
		case string:
			n = 1
		case []interface{}:
			n = len(v)
		}
		resp := openaiResponse{Data: make([]openaiEmbedding, n)}
		// Return out-of-order indices to exercise the sort.
		for i := range resp.Data {
			resp.Data[i] = openaiEmbedding{Embedding: make([]float32, 1536), Index: n - 1 - i}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	results, err := e.EmbedBatch([]string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestOpenAICallAPI_400WithErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad input value","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	_, err := e.Embed("hello")
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "bad input value") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpenAICallAPI_400NonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	_, err := e.Embed("hello")
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "bad request (400)") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpenAICallAPI_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`server exploded`))
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	_, err := e.Embed("hello")
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "unexpected status 500") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpenAICallAPI_BadJSONOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{not valid json`))
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	_, err := e.Embed("hello")
	if err == nil {
		t.Fatal("expected error for malformed 200 response")
	}
	if !strings.Contains(err.Error(), "unmarshal response") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- store.go ---

func TestNewStore_OpenError(t *testing.T) {
	// A path inside a non-existent directory cannot be opened.
	badPath := filepath.Join(t.TempDir(), "no-such-dir", "store.db")
	_, err := NewStore(badPath)
	if err == nil {
		t.Fatal("expected error opening store at invalid path")
	}
}

func TestNewStore_FileBased(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "embeddings.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", dbPath, err)
	}
	defer store.Close()

	emb := NewMockEmbedder("test-model")
	vec, _ := emb.Embed("persisted node")
	if err := store.Upsert("node1", vec, "hash1", "test-model"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if store.Count() != 1 {
		t.Errorf("expected count 1, got %d", store.Count())
	}
}

func TestStoredDimension(t *testing.T) {
	store := newTestStore(t)

	// Empty store returns dimension 0.
	dim, err := store.StoredDimension()
	if err != nil {
		t.Fatalf("StoredDimension(empty): %v", err)
	}
	if dim != 0 {
		t.Errorf("expected dimension 0 for empty store, got %d", dim)
	}

	emb := NewMockEmbedder("test-model")
	vec, _ := emb.Embed("some node")
	if err := store.Upsert("node1", vec, "hash1", "test-model"); err != nil {
		t.Fatal(err)
	}

	dim, err = store.StoredDimension()
	if err != nil {
		t.Fatalf("StoredDimension: %v", err)
	}
	if dim != emb.Dimension() {
		t.Errorf("expected dimension %d, got %d", emb.Dimension(), dim)
	}
}

func TestStoredDimension_MalformedBlob(t *testing.T) {
	store := newTestStore(t)

	// Insert a blob whose length is not a multiple of 4 directly.
	err := store.db.Exec(`INSERT INTO embeddings (node_id, embedding, summary_hash, model)
		VALUES ('bad', x'010203', 'h', 'test-model')`)
	if err != nil {
		t.Fatalf("insert malformed: %v", err)
	}

	if _, err := store.StoredDimension(); err == nil {
		t.Fatal("expected error for malformed blob dimension")
	}
}

func TestCount(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	if store.Count() != 0 {
		t.Errorf("expected count 0 for empty store, got %d", store.Count())
	}

	for i, text := range []string{"one", "two", "three"} {
		vec, _ := emb.Embed(text)
		if err := store.Upsert("node"+text, vec, "h", "test-model"); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}
	if store.Count() != 3 {
		t.Errorf("expected count 3, got %d", store.Count())
	}
}

func TestDeserializeFloat32_InvalidLength(t *testing.T) {
	_, err := DeserializeFloat32([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for length not multiple of 4")
	}
	if !strings.Contains(err.Error(), "not a multiple of 4") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSerializeDeserializeEmpty(t *testing.T) {
	data, err := SerializeFloat32([]float32{})
	if err != nil {
		t.Fatalf("SerializeFloat32(empty): %v", err)
	}
	result, err := DeserializeFloat32(data)
	if err != nil {
		t.Fatalf("DeserializeFloat32(empty): %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d", len(result))
	}
}

func TestL2Distance_MismatchedLengths(t *testing.T) {
	d := l2Distance([]float32{1, 2, 3}, []float32{1, 2})
	if d != math.Inf(1) {
		t.Errorf("expected +Inf for mismatched lengths, got %f", d)
	}
}

func TestCosineSimilarity_EdgeCases(t *testing.T) {
	// Mismatched lengths -> 0.
	if s := cosineSimilarity([]float32{1, 2, 3}, []float32{1, 2}); s != 0 {
		t.Errorf("expected 0 for mismatched lengths, got %f", s)
	}
	// Zero vector -> denom 0 -> 0.
	if s := cosineSimilarity([]float32{0, 0, 0}, []float32{1, 2, 3}); s != 0 {
		t.Errorf("expected 0 for zero vector, got %f", s)
	}
	// Identical vectors -> 1.
	s := cosineSimilarity([]float32{1, 2, 3}, []float32{1, 2, 3})
	if s < 0.999 || s > 1.001 {
		t.Errorf("expected ~1 for identical vectors, got %f", s)
	}
}

// TestScoredHeapPush exercises the heap.Push path on scoredHeap, which the
// production search code does not otherwise hit (it uses Init + Pop).
func TestScoredHeapPush(t *testing.T) {
	h := &scoredHeap{}
	heap.Init(h)
	heap.Push(h, ScoredResult{NodeID: "a", Score: 0.5})
	heap.Push(h, ScoredResult{NodeID: "b", Score: 0.9})
	heap.Push(h, ScoredResult{NodeID: "c", Score: 0.1})

	if h.Len() != 3 {
		t.Fatalf("expected len 3, got %d", h.Len())
	}
	// Max-heap: highest score pops first.
	top := heap.Pop(h).(ScoredResult)
	if top.NodeID != "b" {
		t.Errorf("expected top node b, got %s", top.NodeID)
	}
}

func TestUpsert_StoredDimensionError(t *testing.T) {
	store := newTestStore(t)

	// Corrupt the store with a malformed blob so storedDimensionLocked fails.
	if err := store.db.Exec(`INSERT INTO embeddings (node_id, embedding, summary_hash, model)
		VALUES ('bad', x'010203', 'h', 'test-model')`); err != nil {
		t.Fatalf("insert malformed: %v", err)
	}

	emb := NewMockEmbedder("test-model")
	vec, _ := emb.Embed("node")
	if err := store.Upsert("node1", vec, "hash1", "test-model"); err == nil {
		t.Fatal("expected error when stored dimension check fails")
	}
}

func TestSearch_DimensionErrorFromMalformedStore(t *testing.T) {
	store := newTestStore(t)

	if err := store.db.Exec(`INSERT INTO embeddings (node_id, embedding, summary_hash, model)
		VALUES ('bad', x'010203', 'h', 'test-model')`); err != nil {
		t.Fatalf("insert malformed: %v", err)
	}

	emb := NewMockEmbedder("test-model")
	query, _ := emb.Embed("query")
	if _, err := store.Search(query, 5, "test-model"); err == nil {
		t.Fatal("expected error from malformed store on Search")
	}
	if _, err := store.SearchActive(query, 5, "test-model"); err == nil {
		t.Fatal("expected error from malformed store on SearchActive")
	}
	if _, err := store.FindSimilar(query, 0.5, "test-model"); err == nil {
		t.Fatal("expected error from malformed store on FindSimilar")
	}
}

func TestSearchActive_ModelMismatch(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("model-a")

	vec, _ := emb.Embed("some node")
	if err := store.Upsert("node1", vec, "hash1", "model-a"); err != nil {
		t.Fatal(err)
	}

	query, _ := emb.Embed("some node")
	if _, err := store.SearchActive(query, 5, "model-b"); err == nil {
		t.Fatal("expected model mismatch error on SearchActive")
	}
	if _, err := store.FindSimilar(query, 0.1, "model-b"); err == nil {
		t.Fatal("expected model mismatch error on FindSimilar")
	}
}

func TestSearchActive_DimensionMismatch(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	vec, _ := emb.Embed("some node")
	if err := store.Upsert("node1", vec, "hash1", "test-model"); err != nil {
		t.Fatal(err)
	}

	short := make([]float32, 10)
	if _, err := store.SearchActive(short, 5, "test-model"); err == nil {
		t.Fatal("expected dimension mismatch on SearchActive")
	}
	if _, err := store.FindSimilar(short, 0.1, "test-model"); err == nil {
		t.Fatal("expected dimension mismatch on FindSimilar")
	}
}

func TestFindSimilar_CapsAtMaxResults(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	// Insert 15 near-identical nodes; all should exceed a 0 threshold.
	for i := 0; i < 15; i++ {
		vec, _ := emb.Embed("shared common text body content")
		nodeID := "node" + string(rune('a'+i))
		if err := store.Upsert(nodeID, vec, "h", "test-model"); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}

	query, _ := emb.Embed("shared common text body content")
	results, err := store.FindSimilar(query, 0.0, "test-model")
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	// Capped at 10.
	if len(results) > 10 {
		t.Errorf("expected results capped at 10, got %d", len(results))
	}
}

func TestUpdateStatus_NoRows(t *testing.T) {
	store := newTestStore(t)
	// Updating a non-existent node should not error.
	if err := store.UpdateStatus("nonexistent", "superseded"); err != nil {
		t.Errorf("UpdateStatus on missing node: %v", err)
	}
}

func TestDelete_MissingNode(t *testing.T) {
	store := newTestStore(t)
	// Deleting a non-existent node should not error.
	if err := store.Delete("nonexistent"); err != nil {
		t.Errorf("Delete on missing node: %v", err)
	}
}
