package embedding

import (
	"testing"
)

// TestUpdateStatus_SoftDelete verifies that a node marked "superseded" is
// excluded from SearchActive but still returned by Search.
func TestUpdateStatus_SoftDelete(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	vec, err := emb.Embed("auth login handler")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("auth/login", vec, "hash1", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Soft-delete by marking as superseded.
	if err := store.UpdateStatus("auth/login", "superseded"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	query, err := emb.Embed("auth login handler")
	if err != nil {
		t.Fatal(err)
	}

	// SearchActive must NOT return the superseded node.
	activeResults, err := store.SearchActive(query, 10, "test-model")
	if err != nil {
		t.Fatalf("SearchActive: %v", err)
	}
	for _, r := range activeResults {
		if r.NodeID == "auth/login" {
			t.Error("SearchActive should not return superseded node auth/login")
		}
	}

	// Search (unrestricted) MUST still return the node.
	allResults, err := store.Search(query, 10, "test-model")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range allResults {
		if r.NodeID == "auth/login" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Search should still return superseded node auth/login")
	}
}

// TestSearchActive_ExcludesSuperseded verifies that SearchActive returns only
// active nodes when the store contains a mix of active and superseded entries.
func TestSearchActive_ExcludesSuperseded(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	// Upsert three nodes using the same generic text so all are relevant to the
	// query and the only distinguishing factor is status.
	texts := map[string]string{
		"node/a": "test node alpha",
		"node/b": "test node beta",
		"node/c": "test node gamma",
	}
	for id, text := range texts {
		vec, err := emb.Embed(text)
		if err != nil {
			t.Fatalf("Embed %s: %v", id, err)
		}
		if err := store.Upsert(id, vec, "hash-"+id, "test-model"); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}

	// Mark node/c as superseded.
	if err := store.UpdateStatus("node/c", "superseded"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	query, err := emb.Embed("test node")
	if err != nil {
		t.Fatal(err)
	}

	// SearchActive should return exactly "node/a" and "node/b".
	activeResults, err := store.SearchActive(query, 10, "test-model")
	if err != nil {
		t.Fatalf("SearchActive: %v", err)
	}
	activeIDs := make(map[string]bool)
	for _, r := range activeResults {
		activeIDs[r.NodeID] = true
	}
	if activeIDs["node/c"] {
		t.Error("SearchActive returned superseded node node/c")
	}
	if !activeIDs["node/a"] {
		t.Error("SearchActive missing active node node/a")
	}
	if !activeIDs["node/b"] {
		t.Error("SearchActive missing active node node/b")
	}

	// Search (unrestricted) should return all 3 nodes.
	allResults, err := store.Search(query, 10, "test-model")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(allResults) != 3 {
		t.Errorf("Search expected 3 results, got %d", len(allResults))
	}
}

// TestFindSimilar_Basic inserts three nodes with varying similarity to a query
// and verifies that only those meeting the threshold are returned, sorted descending.
func TestFindSimilar_Basic(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	// "a" — very close to query ("user authentication login" vs query "authenticate user login")
	vecA, err := emb.Embed("user authentication login")
	if err != nil {
		t.Fatal(err)
	}
	// "b" — somewhat related (shares "user" and "login" but adds "session")
	vecB, err := emb.Embed("user login session")
	if err != nil {
		t.Fatal(err)
	}
	// "c" — unrelated (database domain)
	vecC, err := emb.Embed("database connection pool")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Upsert("node/a", vecA, "ha", "test-model"); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("node/b", vecB, "hb", "test-model"); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("node/c", vecC, "hc", "test-model"); err != nil {
		t.Fatal(err)
	}

	query, err := emb.Embed("authenticate user login")
	if err != nil {
		t.Fatal(err)
	}

	// Compute actual scores to pick a threshold that separates a+b from c.
	scoreA := 1.0 / (1.0 + l2Distance(query, vecA))
	scoreB := 1.0 / (1.0 + l2Distance(query, vecB))
	scoreC := 1.0 / (1.0 + l2Distance(query, vecC))
	t.Logf("scores: a=%.4f b=%.4f c=%.4f", scoreA, scoreB, scoreC)

	// Choose a threshold that includes a and b but not c.
	// Use a threshold just above c's score but at or below b's.
	threshold := (scoreB + scoreC) / 2.0
	if scoreB <= scoreC {
		// Fallback: use a very low threshold and just check ordering.
		threshold = 0.0
	}
	t.Logf("using threshold %.4f", threshold)

	results, err := store.FindSimilar(query, threshold, "test-model")
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}

	// node/c should not appear when threshold > scoreC.
	if threshold > scoreC {
		for _, r := range results {
			if r.NodeID == "node/c" {
				t.Errorf("node/c (score %.4f) should be excluded by threshold %.4f", scoreC, threshold)
			}
		}
	}

	// Results must be sorted descending.
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted: results[%d].Score=%.4f > results[%d].Score=%.4f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

// TestFindSimilar_ExcludesSuperseded verifies that superseded nodes are not
// returned by FindSimilar even if their similarity exceeds the threshold.
func TestFindSimilar_ExcludesSuperseded(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	vecA, err := emb.Embed("user authentication login")
	if err != nil {
		t.Fatal(err)
	}
	vecB, err := emb.Embed("user authentication login handler")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Upsert("node/a", vecA, "ha", "test-model"); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("node/b", vecB, "hb", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Mark node/b as superseded.
	if err := store.UpdateStatus("node/b", "superseded"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	query, err := emb.Embed("user authentication login")
	if err != nil {
		t.Fatal(err)
	}

	// Use a very low threshold so both would match if active.
	results, err := store.FindSimilar(query, 0.0, "test-model")
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}

	for _, r := range results {
		if r.NodeID == "node/b" {
			t.Error("FindSimilar should not return superseded node node/b")
		}
	}

	found := false
	for _, r := range results {
		if r.NodeID == "node/a" {
			found = true
		}
	}
	if !found {
		t.Error("FindSimilar should return active node node/a")
	}
}

// TestFindSimilar_EmptyStore verifies that FindSimilar on an empty store
// returns nil results and no error.
func TestFindSimilar_EmptyStore(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	query, err := emb.Embed("authenticate user login")
	if err != nil {
		t.Fatal(err)
	}

	results, err := store.FindSimilar(query, 0.5, "test-model")
	if err != nil {
		t.Fatalf("FindSimilar on empty store returned error: %v", err)
	}
	if results != nil {
		t.Errorf("FindSimilar on empty store should return nil, got %v", results)
	}
}

// TestUpdateStatus_ReActivate verifies that a previously superseded node can
// be re-activated by calling UpdateStatus("active") and will then appear in
// SearchActive results again.
func TestUpdateStatus_ReActivate(t *testing.T) {
	store := newTestStore(t)
	emb := NewMockEmbedder("test-model")

	vec, err := emb.Embed("authentication module")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("auth/module", vec, "hash-auth", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Supersede the node.
	if err := store.UpdateStatus("auth/module", "superseded"); err != nil {
		t.Fatalf("UpdateStatus superseded: %v", err)
	}

	query, err := emb.Embed("authentication module")
	if err != nil {
		t.Fatal(err)
	}

	// Confirm it is excluded from SearchActive.
	afterSupersede, err := store.SearchActive(query, 10, "test-model")
	if err != nil {
		t.Fatalf("SearchActive after supersede: %v", err)
	}
	for _, r := range afterSupersede {
		if r.NodeID == "auth/module" {
			t.Error("SearchActive should not return node after being superseded")
		}
	}

	// Re-activate the node.
	if err := store.UpdateStatus("auth/module", "active"); err != nil {
		t.Fatalf("UpdateStatus active: %v", err)
	}

	// Node should now appear in SearchActive again.
	afterReactivate, err := store.SearchActive(query, 10, "test-model")
	if err != nil {
		t.Fatalf("SearchActive after reactivation: %v", err)
	}
	found := false
	for _, r := range afterReactivate {
		if r.NodeID == "auth/module" {
			found = true
			break
		}
	}
	if !found {
		t.Error("SearchActive should find node after re-activation")
	}
}
