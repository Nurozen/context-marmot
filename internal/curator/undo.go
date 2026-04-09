package curator

import (
	"os"
	"sync"
	"time"

	"github.com/nurozen/context-marmot/internal/node"
)

// MaxUndoEntries is the maximum number of undo entries kept per session.
const MaxUndoEntries = 50

// UndoEntry captures the pre-mutation state of nodes so that the mutation can
// be reversed.
type UndoEntry struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Timestamp time.Time      `json:"timestamp"`
	Snapshots []NodeSnapshot `json:"snapshots"`
	Created   []string       `json:"created"` // node IDs that were created (undo = delete them)
}

// NodeSnapshot holds the state of a single node before a mutation.
type NodeSnapshot struct {
	Node    *node.Node `json:"node"`
	Existed bool       `json:"existed"` // false if node was created by the mutation
}

// UndoStack is a per-session LIFO stack of UndoEntry values, safe for
// concurrent access.
type UndoStack struct {
	mu     sync.Mutex
	stacks map[string][]UndoEntry // keyed by session ID
}

// NewUndoStack creates an empty UndoStack.
func NewUndoStack() *UndoStack {
	return &UndoStack{
		stacks: make(map[string][]UndoEntry),
	}
}

// Push records a new undo entry. If the stack exceeds MaxUndoEntries, the
// oldest entry is dropped.
func (u *UndoStack) Push(sessionID string, entry UndoEntry) {
	u.mu.Lock()
	defer u.mu.Unlock()

	stack := u.stacks[sessionID]
	stack = append(stack, entry)
	if len(stack) > MaxUndoEntries {
		stack = stack[len(stack)-MaxUndoEntries:]
	}
	u.stacks[sessionID] = stack
}

// Pop removes and returns the most recent entry for the session.
// Returns nil if the stack is empty.
func (u *UndoStack) Pop(sessionID string) *UndoEntry {
	u.mu.Lock()
	defer u.mu.Unlock()

	stack := u.stacks[sessionID]
	if len(stack) == 0 {
		return nil
	}
	entry := stack[len(stack)-1]
	u.stacks[sessionID] = stack[:len(stack)-1]
	return &entry
}

// Peek returns the most recent entry without removing it.
// Returns nil if the stack is empty.
func (u *UndoStack) Peek(sessionID string) *UndoEntry {
	u.mu.Lock()
	defer u.mu.Unlock()

	stack := u.stacks[sessionID]
	if len(stack) == 0 {
		return nil
	}
	entry := stack[len(stack)-1]
	return &entry
}

// Len returns the stack depth for a session.
func (u *UndoStack) Len(sessionID string) int {
	u.mu.Lock()
	defer u.mu.Unlock()

	return len(u.stacks[sessionID])
}

// SnapshotNodes loads the current state of the given node IDs from the store.
// Returns snapshots suitable for an UndoEntry. Nodes that don't exist on disk
// get Existed=false with a nil Node.
func SnapshotNodes(store *node.Store, namespace string, nodeIDs []string) []NodeSnapshot {
	snapshots := make([]NodeSnapshot, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		path, err := store.SafeNodePath(id)
		if err != nil {
			snapshots = append(snapshots, NodeSnapshot{Node: nil, Existed: false})
			continue
		}
		if _, statErr := os.Stat(path); statErr != nil {
			snapshots = append(snapshots, NodeSnapshot{Node: nil, Existed: false})
			continue
		}
		n, err := store.LoadNode(path)
		if err != nil {
			snapshots = append(snapshots, NodeSnapshot{Node: nil, Existed: false})
			continue
		}
		snapshots = append(snapshots, NodeSnapshot{Node: n, Existed: true})
	}
	return snapshots
}
