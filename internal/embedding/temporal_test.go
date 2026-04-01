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
