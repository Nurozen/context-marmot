package curator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/node"
)

func TestPushPopLIFO(t *testing.T) {
	us := NewUndoStack()

	e1 := UndoEntry{ID: "a", SessionID: "s1", Timestamp: time.Now()}
	e2 := UndoEntry{ID: "b", SessionID: "s1", Timestamp: time.Now()}
	e3 := UndoEntry{ID: "c", SessionID: "s1", Timestamp: time.Now()}

	us.Push("s1", e1)
	us.Push("s1", e2)
	us.Push("s1", e3)

	got := us.Pop("s1")
	if got == nil || got.ID != "c" {
		t.Fatalf("expected 'c', got %v", got)
	}
	got = us.Pop("s1")
	if got == nil || got.ID != "b" {
		t.Fatalf("expected 'b', got %v", got)
	}
	got = us.Pop("s1")
	if got == nil || got.ID != "a" {
		t.Fatalf("expected 'a', got %v", got)
	}
}

func TestPopEmptyReturnsNil(t *testing.T) {
	us := NewUndoStack()

	if got := us.Pop("nonexistent"); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestMaxEntriesLimit(t *testing.T) {
	us := NewUndoStack()

	for i := 0; i < 55; i++ {
		us.Push("s1", UndoEntry{ID: time.Now().String(), SessionID: "s1"})
	}

	if n := us.Len("s1"); n != MaxUndoEntries {
		t.Fatalf("expected %d entries, got %d", MaxUndoEntries, n)
	}
}

func TestMultipleSessionsIndependent(t *testing.T) {
	us := NewUndoStack()

	us.Push("s1", UndoEntry{ID: "s1-a", SessionID: "s1"})
	us.Push("s1", UndoEntry{ID: "s1-b", SessionID: "s1"})
	us.Push("s2", UndoEntry{ID: "s2-x", SessionID: "s2"})

	if n := us.Len("s1"); n != 2 {
		t.Fatalf("s1: expected 2, got %d", n)
	}
	if n := us.Len("s2"); n != 1 {
		t.Fatalf("s2: expected 1, got %d", n)
	}

	got := us.Pop("s2")
	if got == nil || got.ID != "s2-x" {
		t.Fatalf("s2: expected 's2-x', got %v", got)
	}
	if us.Len("s2") != 0 {
		t.Fatalf("s2: expected 0 after pop")
	}
	// s1 unaffected
	if us.Len("s1") != 2 {
		t.Fatalf("s1: expected 2, got %d", us.Len("s1"))
	}
}

func TestPeek(t *testing.T) {
	us := NewUndoStack()

	if got := us.Peek("s1"); got != nil {
		t.Fatalf("expected nil on empty, got %+v", got)
	}

	us.Push("s1", UndoEntry{ID: "a", SessionID: "s1"})
	got := us.Peek("s1")
	if got == nil || got.ID != "a" {
		t.Fatalf("expected 'a', got %v", got)
	}
	// Peek should not remove the entry.
	if us.Len("s1") != 1 {
		t.Fatalf("peek should not remove entry; expected len 1, got %d", us.Len("s1"))
	}
}

func TestSnapshotNodesExistingAndMissing(t *testing.T) {
	dir := t.TempDir()
	store := node.NewStore(dir)

	// Create a node on disk.
	existing := &node.Node{
		ID:        "existing-node",
		Type:      "function",
		Namespace: "test",
		Summary:   "An existing node",
	}
	if err := store.SaveNode(existing); err != nil {
		t.Fatalf("save node: %v", err)
	}

	// Verify the file exists.
	path := filepath.Join(dir, "existing-node.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected node file to exist: %v", err)
	}

	snapshots := SnapshotNodes(store, "test", []string{"existing-node", "no-such-node"})

	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}

	// First snapshot: existing node.
	if !snapshots[0].Existed {
		t.Fatal("expected existing-node to have Existed=true")
	}
	if snapshots[0].Node == nil {
		t.Fatal("expected existing-node snapshot to have non-nil Node")
	}
	if snapshots[0].Node.ID != "existing-node" {
		t.Fatalf("expected ID 'existing-node', got %q", snapshots[0].Node.ID)
	}

	// Second snapshot: non-existing node.
	if snapshots[1].Existed {
		t.Fatal("expected no-such-node to have Existed=false")
	}
	if snapshots[1].Node != nil {
		t.Fatal("expected no-such-node snapshot to have nil Node")
	}
}
