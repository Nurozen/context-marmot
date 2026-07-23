package embedding

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpsertCheckedGuard covers the model-poisoning guard (F2): writes into a
// store that already holds a different model are refused with a
// *ModelMismatchError naming both models, and the refused row is never
// written; same-model and empty-store writes pass through.
func TestUpsertCheckedGuard(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Empty store: any model is compatible and the write lands.
	if ok, err := store.CompatibleModel("model-a"); err != nil || !ok {
		t.Fatalf("CompatibleModel on empty store = (%v, %v), want true", ok, err)
	}
	if err := store.UpsertChecked("n1", []float32{1, 2}, "h1", "model-a"); err != nil {
		t.Fatalf("UpsertChecked into empty store: %v", err)
	}

	// Same model: still compatible, write lands.
	if err := store.UpsertChecked("n2", []float32{3, 4}, "h2", "model-a"); err != nil {
		t.Fatalf("UpsertChecked same model: %v", err)
	}

	// Different model: refused, typed error naming both models, no row written.
	if ok, err := store.CompatibleModel("model-b"); err != nil || ok {
		t.Fatalf("CompatibleModel mixed = (%v, %v), want false", ok, err)
	}
	err = store.UpsertChecked("n3", []float32{5, 6}, "h3", "model-b")
	if err == nil {
		t.Fatal("UpsertChecked with mismatched model succeeded, want refusal")
	}
	var mismatch *ModelMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error type = %T, want *ModelMismatchError", err)
	}
	if mismatch.WriteModel != "model-b" || len(mismatch.StoredModels) != 1 || mismatch.StoredModels[0] != "model-a" {
		t.Fatalf("mismatch fields = %+v", mismatch)
	}
	if !strings.Contains(err.Error(), "model-a") || !strings.Contains(err.Error(), "model-b") {
		t.Fatalf("error must name both models: %v", err)
	}
	if store.Count() != 2 {
		t.Fatalf("Count after refused write = %d, want 2", store.Count())
	}
	// The refused node must not be stale-checkable as present.
	stale, err := store.StaleCheck("n3", "h3")
	if err != nil || !stale {
		t.Fatalf("StaleCheck for refused node = (%v, %v), want stale (absent)", stale, err)
	}
}

// TestUpsertCheckedReadOnlyAndClosed pins the guard's rejection order on
// read-only and closed stores.
func TestUpsertCheckedReadOnlyAndClosed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "embeddings.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Upsert("n1", []float32{1}, "h", "model-a"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store.UpsertChecked("n2", []float32{1}, "h", "model-a"); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("UpsertChecked after Close = %v, want ErrStoreClosed", err)
	}
	if _, err := store.CompatibleModel("model-a"); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("CompatibleModel after Close = %v, want ErrStoreClosed", err)
	}

	ro, err := NewStoreReadOnly(dbPath)
	if err != nil {
		t.Fatalf("NewStoreReadOnly: %v", err)
	}
	defer func() { _ = ro.Close() }()
	if err := ro.UpsertChecked("n2", []float32{1}, "h", "model-a"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("UpsertChecked on read-only store = %v, want read-only rejection", err)
	}
}
