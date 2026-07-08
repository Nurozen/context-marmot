package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/node"
)

// newTestEngine builds an Engine backed by a mock store containing one node
// whose source file lives in dir, with a deliberately stale hash so a batch
// update detects and reindexes it.
func newTestEngine(t *testing.T, dir string) (*Engine, *mockNodeStore, *mockEmbeddingStore) {
	t.Helper()
	srcPath := writeTempSource(t, dir, "watched.go", "package watched\n")
	store := newMockNodeStore(dir)
	store.nodes["W"] = &node.Node{
		ID:      "W",
		Status:  node.StatusActive,
		Source:  node.Source{Path: srcPath, Hash: "stale"},
		Summary: "Watched node",
	}
	emb := newMockEmbeddingStore()
	eng := NewEngine(store, newMockGraph(), emb, newMockEmbedder())
	return eng, store, emb
}

func TestWatcherStartTriggersBatchUpdate(t *testing.T) {
	dir := t.TempDir()
	eng, _, _ := newTestEngine(t, dir)

	// Signal reindex completion over a channel to avoid racing on shared maps.
	reindexed := make(chan int, 4)
	eng.WithOnChange(func(n int) { reindexed <- n })

	cfg := WatcherConfig{
		Paths:          []string{dir},
		Debounce:       20 * time.Millisecond,
		PropagateDepth: 1,
	}
	w, err := NewWatcher(eng, cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Trigger a filesystem write event on a watched directory.
	if err := os.WriteFile(filepath.Join(dir, "trigger.go"), []byte("package watched\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case n := <-reindexed:
		if n < 1 {
			t.Fatalf("expected at least 1 reindexed node, got %d", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for batch update to reindex node W")
	}

	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestWatcherStartStopViaContext(t *testing.T) {
	dir := t.TempDir()
	eng, _, _ := newTestEngine(t, dir)

	cfg := WatcherConfig{Paths: []string{dir}, Debounce: 10 * time.Millisecond}
	w, err := NewWatcher(eng, cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	// Cancelling the context should let the run goroutine exit; Stop still works.
	cancel()
	time.Sleep(30 * time.Millisecond)
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop after context cancel: %v", err)
	}
}

func TestWatcherStopWithoutStart(t *testing.T) {
	dir := t.TempDir()
	eng, _, _ := newTestEngine(t, dir)
	w, err := NewWatcher(eng, WatcherConfig{Paths: []string{dir}})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	// Stop before Start must not block on doneCh.
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop without start: %v", err)
	}
	// Second Stop is a no-op via stopOnce.
	if err := w.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestExecuteBatchUpdateDirect(t *testing.T) {
	dir := t.TempDir()
	eng, _, emb := newTestEngine(t, dir)
	w, err := NewWatcher(eng, WatcherConfig{Paths: []string{dir}, PropagateDepth: 1})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	// Directly exercise the batch update path.
	w.executeBatchUpdate(context.Background())

	if _, ok := emb.upserted["W"]; !ok {
		t.Fatal("expected node W to be reindexed by executeBatchUpdate")
	}
}
