package embedding

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// journalMode returns the active journal_mode of the store's connection.
func journalMode(t *testing.T, s *Store) string {
	t.Helper()
	stmt, _, err := s.db.Prepare(`PRAGMA journal_mode`)
	if err != nil {
		t.Fatalf("prepare journal_mode pragma: %v", err)
	}
	defer func() { _ = stmt.Close() }()
	if !stmt.Step() {
		t.Fatalf("journal_mode pragma returned no row: %v", stmt.Err())
	}
	return stmt.ColumnText(0)
}

func TestNewStore_WALEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "embeddings.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", dbPath, err)
	}

	if mode := journalMode(t, store); mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}

	// The -wal sidecar should appear once a write hits the database.
	emb := NewMockEmbedder("test-model")
	vec, _ := emb.Embed("wal sidecar node")
	if err := store.Upsert("wal/node", vec, "hash1", "test-model"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		t.Errorf("expected -wal sidecar after Upsert: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: the WAL flip is persisted in the DB header and data survives.
	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen NewStore(%q): %v", dbPath, err)
	}
	defer reopened.Close()

	if mode := journalMode(t, reopened); mode != "wal" {
		t.Errorf("journal_mode after reopen = %q, want %q", mode, "wal")
	}
	if got := reopened.Count(); got != 1 {
		t.Errorf("Count after reopen = %d, want 1", got)
	}
}

func TestNewStore_MemoryStillWorks(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore(:memory:): %v", err)
	}
	defer store.Close()

	// The WAL pragma is a no-op for in-memory databases.
	if mode := journalMode(t, store); mode != "memory" {
		t.Errorf("journal_mode = %q, want %q", mode, "memory")
	}

	// Upsert/Search round-trip still works.
	emb := NewMockEmbedder("test-model")
	vec, _ := emb.Embed("in-memory node")
	if err := store.Upsert("mem/node", vec, "hash1", "test-model"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	query, _ := emb.Embed("in-memory node")
	results, err := store.Search(query, 1, "test-model")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].NodeID != "mem/node" {
		t.Errorf("Search results = %+v, want single mem/node", results)
	}
}

// TestNewStore_ConcurrentConns is the regression test for the multi-process
// failure modes: two Stores on one file are two OS-level connections, so this
// exercises the real OFD/shm locking path. A writer loop and a reader loop run
// concurrently for ~2s; with WAL + busy_timeout neither side may ever see
// "database is locked".
func TestNewStore_ConcurrentConns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "embeddings.db")

	writerStore, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (writer): %v", err)
	}
	defer writerStore.Close()

	readerStore, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (reader): %v", err)
	}
	defer readerStore.Close()

	emb := NewMockEmbedder("test-model")
	// Seed one row so reader queries validate against a stored dimension.
	seed, _ := emb.Embed("seed node")
	if err := writerStore.Upsert("seed", seed, "hash-seed", "test-model"); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}
	query, _ := emb.Embed("seed node")

	deadline := time.Now().Add(2 * time.Second)
	var (
		mu   sync.Mutex
		errs []error
	)
	record := func(op string, err error) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, fmt.Errorf("%s: %w", op, err))
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: upsert in a tight loop on connection 1.
	go func() {
		defer wg.Done()
		for i := 0; time.Now().Before(deadline); i++ {
			vec, _ := emb.Embed(fmt.Sprintf("node %d", i%10))
			if err := writerStore.Upsert(fmt.Sprintf("node/%d", i%10), vec, fmt.Sprintf("hash%d", i), "test-model"); err != nil {
				record("Upsert", err)
			}
		}
	}()

	// Reader: search in a tight loop on connection 2.
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			if _, err := readerStore.SearchActive(query, 5, "test-model"); err != nil {
				record("SearchActive", err)
			}
		}
	}()

	wg.Wait()

	// No error of any kind is expected; "database is locked" in particular
	// would reproduce the pre-WAL failure mode.
	for _, err := range errs {
		t.Errorf("concurrent access error: %v", err)
	}
}
