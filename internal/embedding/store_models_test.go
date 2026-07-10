package embedding

import (
	"path/filepath"
	"testing"
)

// TestModelsAndHasStatusColumn covers the doctor-facing introspection pair:
// distinct models sorted, status column detected, both usable on a
// read-only open (doctor must never mutate remote vaults), and both gated
// after Close.
func TestModelsAndHasStatusColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "embeddings.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	models, err := store.Models()
	if err != nil || len(models) != 0 {
		t.Fatalf("Models on empty store = (%v, %v), want none", models, err)
	}
	if err := store.Upsert("b", []float32{1, 2}, "h", "model-two"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Upsert("a", []float32{1, 2}, "h", "model-one"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ro, err := NewStoreReadOnly(dbPath)
	if err != nil {
		t.Fatalf("NewStoreReadOnly: %v", err)
	}
	models, err = ro.Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0] != "model-one" || models[1] != "model-two" {
		t.Fatalf("Models = %v, want sorted [model-one model-two]", models)
	}
	hasStatus, err := ro.HasStatusColumn()
	if err != nil || !hasStatus {
		t.Fatalf("HasStatusColumn = (%t, %v), want true", hasStatus, err)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := ro.Models(); err != ErrStoreClosed {
		t.Fatalf("Models after Close err = %v, want ErrStoreClosed", err)
	}
	if _, err := ro.HasStatusColumn(); err != ErrStoreClosed {
		t.Fatalf("HasStatusColumn after Close err = %v, want ErrStoreClosed", err)
	}
}
