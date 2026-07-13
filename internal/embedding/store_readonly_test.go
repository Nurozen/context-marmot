package embedding

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedClosedDB builds an embeddings DB with n rows via the normal read-write
// path, then closes it so the WAL is checkpointed away and only the main
// database file remains. Returns the db path.
func seedClosedDB(t *testing.T, n int) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "embeddings.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	emb := NewMockEmbedder("test-model")
	for i := 0; i < n; i++ {
		vec, _ := emb.Embed(strings.Repeat("x", i+1))
		if err := store.Upsert(nodeID(i), vec, "hash", "test-model"); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A clean close of the last connection checkpoints and removes sidecars.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be gone after clean close, stat err=%v", dbPath+suffix, err)
		}
	}
	return dbPath
}

func nodeID(i int) string {
	return "node/" + strings.Repeat("a", i+1)
}

// TestNewStoreReadOnly_DoesNotMutate is the cross-vault-read regression: a
// read-only open plus a search must not rewrite the journal mode, migrate
// schema, create sidecars, or change a single byte of the remote DB file.
func TestNewStoreReadOnly_DoesNotMutate(t *testing.T) {
	dbPath := seedClosedDB(t, 3)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db bytes: %v", err)
	}

	ro, err := NewStoreReadOnly(dbPath)
	if err != nil {
		t.Fatalf("NewStoreReadOnly: %v", err)
	}
	defer ro.Close()

	// journal_mode stays whatever the file header says (wal from NewStore);
	// the point is the RO conn never *executes* a journal-mode flip.
	if mode := journalMode(t, ro); mode != "wal" {
		t.Errorf("journal_mode = %q, want wal (persisted header, unmodified)", mode)
	}

	emb := NewMockEmbedder("test-model")
	query, _ := emb.Embed("x")
	results, err := ro.SearchActive(query, 5, "test-model")
	if err != nil {
		t.Fatalf("SearchActive: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("SearchActive results = %d, want 3", len(results))
	}
	if got := ro.Count(); got != 3 {
		t.Errorf("Count = %d, want 3", got)
	}

	// SQLite requires the -shm index to read a WAL-mode DB, so the RO open
	// creates transient sidecars — but they must carry no data: the -wal
	// stays empty (nothing was written) and the main file is byte-identical.
	if fi, err := os.Stat(dbPath + "-wal"); err == nil && fi.Size() != 0 {
		t.Errorf("read-only open wrote %d bytes into the WAL", fi.Size())
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db bytes after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("read-only open + search changed the database file bytes")
	}
}

func TestNewStoreReadOnly_WritesRejected(t *testing.T) {
	dbPath := seedClosedDB(t, 1)
	ro, err := NewStoreReadOnly(dbPath)
	if err != nil {
		t.Fatalf("NewStoreReadOnly: %v", err)
	}
	defer ro.Close()

	emb := NewMockEmbedder("test-model")
	vec, _ := emb.Embed("new node")
	if err := ro.Upsert("new/node", vec, "h", "test-model"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("Upsert err = %v, want read-only rejection", err)
	}
	if err := ro.UpdateStatus(nodeID(0), "superseded"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("UpdateStatus err = %v, want read-only rejection", err)
	}
	if err := ro.Delete(nodeID(0)); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("Delete err = %v, want read-only rejection", err)
	}
	if err := ro.Checkpoint(); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("Checkpoint err = %v, want read-only rejection", err)
	}
}

func TestNewStoreReadOnly_MissingFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.db")
	if _, err := NewStoreReadOnly(dbPath); err == nil {
		t.Fatal("expected error for missing file")
	}
	// The open must not have created the file in the remote checkout.
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("read-only open created %s (stat err=%v)", dbPath, err)
	}
}

// TestCheckpointTruncatesWAL: after Checkpoint the -wal is zeroed and a copy
// of the main database file alone contains every row.
func TestCheckpointTruncatesWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "embeddings.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	emb := NewMockEmbedder("test-model")
	const rows = 6
	for i := 0; i < rows; i++ {
		vec, _ := emb.Embed(strings.Repeat("y", i+1))
		if err := store.Upsert(nodeID(i), vec, "h", "test-model"); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	fi, err := os.Stat(dbPath + "-wal")
	if err != nil || fi.Size() == 0 {
		t.Fatalf("expected non-empty WAL before checkpoint (err=%v)", err)
	}

	if err := store.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	fi, err = os.Stat(dbPath + "-wal")
	if err != nil {
		t.Fatalf("stat WAL after checkpoint: %v", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("WAL size after TRUNCATE checkpoint = %d, want 0", fi.Size())
	}

	// A main-file-only copy is complete: all rows readable.
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	data, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read main db: %v", err)
	}
	if err := os.WriteFile(copyPath, data, 0o644); err != nil {
		t.Fatalf("write copy: %v", err)
	}
	copied, err := NewStoreReadOnly(copyPath)
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	defer copied.Close()
	if got := copied.Count(); got != rows {
		t.Fatalf("copy has %d rows, want %d", got, rows)
	}
}
