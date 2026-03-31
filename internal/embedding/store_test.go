package embedding

import (
	"fmt"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore(:memory:): %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestUpsertAndSearch(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	// Generate embeddings for a few nodes.
	vec1, err := emb.Embed("user authentication login")
	if err != nil {
		t.Fatal(err)
	}
	vec2, err := emb.Embed("user authentication logout")
	if err != nil {
		t.Fatal(err)
	}
	vec3, err := emb.Embed("database connection pooling")
	if err != nil {
		t.Fatal(err)
	}

	// Upsert all three.
	if err := store.Upsert("auth/login", vec1, "hash1", "test-model"); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("auth/logout", vec2, "hash2", "test-model"); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("db/pool", vec3, "hash3", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Search for "user authentication login" — should find auth/login first.
	query, err := emb.Embed("user authentication login")
	if err != nil {
		t.Fatal(err)
	}
	results, err := store.Search(query, 3, "test-model")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].NodeID != "auth/login" {
		t.Errorf("expected first result to be auth/login, got %s", results[0].NodeID)
	}
}

func TestSearchOrderedBySimilarity(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	// "user login auth" is closest to "user login authentication"
	// "database query" is farther away.
	vec1, _ := emb.Embed("user login authentication")
	vec2, _ := emb.Embed("user login auth")
	vec3, _ := emb.Embed("database query optimization")

	store.Upsert("auth/login", vec1, "h1", "test-model")
	store.Upsert("auth/auth", vec2, "h2", "test-model")
	store.Upsert("db/query", vec3, "h3", "test-model")

	query, _ := emb.Embed("user login authentication")
	results, err := store.Search(query, 3, "test-model")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// First result should be most similar (auth/login, exact match).
	if results[0].NodeID != "auth/login" {
		t.Errorf("expected first result auth/login, got %s", results[0].NodeID)
	}

	// Scores should be in descending order (higher similarity = closer).
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not ordered by similarity: result[%d].Score=%f > result[%d].Score=%f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

func TestCrossModelQueryRejection(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("model-a")

	vec, _ := emb.Embed("some text")
	if err := store.Upsert("node1", vec, "hash1", "model-a"); err != nil {
		t.Fatal(err)
	}

	query, _ := emb.Embed("some text")

	// Searching with a different model should fail.
	_, err := store.Search(query, 5, "model-b")
	if err == nil {
		t.Fatal("expected error for cross-model query, got nil")
	}
	t.Logf("correctly rejected cross-model query: %v", err)
}

func TestDeleteRemovesFromBothTables(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	vec, _ := emb.Embed("node to delete")
	if err := store.Upsert("to-delete", vec, "hash", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Verify it exists via search.
	results, err := store.Search(vec, 1, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected to find the node before deletion")
	}

	// Delete it.
	if err := store.Delete("to-delete"); err != nil {
		t.Fatal(err)
	}

	// StaleCheck should return true (node not found = stale).
	stale, err := store.StaleCheck("to-delete", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale=true after deletion")
	}

	// Add another node so the store isn't empty (empty store skips model check).
	vec2, _ := emb.Embed("still here")
	if err := store.Upsert("keeper", vec2, "hash2", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Search should no longer return the deleted node.
	results, err = store.Search(vec, 5, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.NodeID == "to-delete" {
			t.Error("deleted node still appears in search results")
		}
	}
}

func TestStaleCheckDetectsChangedHash(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	vec, _ := emb.Embed("original content")
	if err := store.Upsert("node1", vec, "original-hash", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Same hash should not be stale.
	stale, err := store.StaleCheck("node1", "original-hash")
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Error("expected stale=false for matching hash")
	}

	// Different hash should be stale.
	stale, err = store.StaleCheck("node1", "new-hash")
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale=true for different hash")
	}
}

func TestStaleCheckMissingNode(t *testing.T) {
	store := newTestStore(t)

	// Non-existent node should be stale.
	stale, err := store.StaleCheck("nonexistent", "any-hash")
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale=true for missing node")
	}
}

func TestEmptyDatabaseSearch(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	query, _ := emb.Embed("anything")

	// Searching an empty store should return empty results (no model mismatch since no data).
	results, err := store.Search(query, 5, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty store, got %d", len(results))
	}
}

func TestUpsertUpdatesExistingNode(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	vec1, _ := emb.Embed("version one")
	if err := store.Upsert("node1", vec1, "hash-v1", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Update with new embedding and hash.
	vec2, _ := emb.Embed("version two completely different")
	if err := store.Upsert("node1", vec2, "hash-v2", "test-model"); err != nil {
		t.Fatal(err)
	}

	// StaleCheck should reflect the new hash.
	stale, err := store.StaleCheck("node1", "hash-v2")
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Error("expected stale=false for updated hash")
	}

	stale, err = store.StaleCheck("node1", "hash-v1")
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Error("expected stale=true for old hash after update")
	}

	// Search should find the node with the new embedding.
	results, err := store.Search(vec2, 1, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected to find the updated node")
	}
	if results[0].NodeID != "node1" {
		t.Errorf("expected node1 as top result, got %s", results[0].NodeID)
	}
}

func TestMockEmbedderSimilarity(t *testing.T) {
	emb := NewMockEmbedder("test")

	// Similar texts should produce similar vectors.
	v1, _ := emb.Embed("user authentication login")
	v2, _ := emb.Embed("user authentication logout")
	v3, _ := emb.Embed("database connection pooling")

	sim12 := cosineSimilarity(v1, v2)
	sim13 := cosineSimilarity(v1, v3)

	t.Logf("similarity(login, logout) = %.4f", sim12)
	t.Logf("similarity(login, db_pool) = %.4f", sim13)

	// "login" and "logout" share most of their text, so should be more similar
	// than "login" and "database connection pooling".
	if sim12 <= sim13 {
		t.Errorf("expected similar texts to have higher cosine similarity: sim(login,logout)=%.4f <= sim(login,db_pool)=%.4f",
			sim12, sim13)
	}
}

func TestMockEmbedderDimension(t *testing.T) {
	emb := NewMockEmbedder("test")

	vec, err := emb.Embed("hello world")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != emb.Dimension() {
		t.Errorf("expected dimension %d, got %d", emb.Dimension(), len(vec))
	}
}

func TestMockEmbedderDeterministic(t *testing.T) {
	emb := NewMockEmbedder("test")

	v1, _ := emb.Embed("deterministic test")
	v2, _ := emb.Embed("deterministic test")

	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("non-deterministic embedding at index %d: %f != %f", i, v1[i], v2[i])
		}
	}
}

func TestSearchTopKLimit(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	// Insert 5 nodes.
	texts := []string{
		"alpha bravo charlie",
		"alpha bravo delta",
		"alpha bravo echo",
		"alpha bravo foxtrot",
		"alpha bravo golf",
	}
	for i, text := range texts {
		vec, _ := emb.Embed(text)
		nodeID := fmt.Sprintf("node/%d", i)
		if err := store.Upsert(nodeID, vec, fmt.Sprintf("h%d", i), "test-model"); err != nil {
			t.Fatal(err)
		}
	}

	query, _ := emb.Embed("alpha bravo charlie")

	// Request only top 2.
	results, err := store.Search(query, 2, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestDimensionMismatch(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	// Insert a valid embedding first to establish the store's dimension.
	vec, _ := emb.Embed("establish dimension")
	if err := store.Upsert("good", vec, "hash", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Wrong dimension on upsert (mismatched with stored dimension).
	short := make([]float32, 100)
	err := store.Upsert("bad", short, "hash", "test-model")
	if err == nil {
		t.Error("expected error for wrong dimension on upsert")
	}

	// Wrong dimension on search.
	_, err = store.Search(short, 5, "test-model")
	if err == nil {
		t.Error("expected error for wrong dimension on search")
	}

	// Empty embedding should also fail.
	err = store.Upsert("empty", nil, "hash", "test-model")
	if err == nil {
		t.Error("expected error for empty embedding on upsert")
	}
}

func TestSerializeDeserializeRoundtrip(t *testing.T) {
	original := []float32{1.0, -2.5, 3.14, 0.0, -0.001}
	data, err := SerializeFloat32(original)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DeserializeFloat32(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != len(original) {
		t.Fatalf("length mismatch: %d vs %d", len(result), len(original))
	}
	for i := range original {
		if result[i] != original[i] {
			t.Errorf("mismatch at %d: %f vs %f", i, result[i], original[i])
		}
	}
}
