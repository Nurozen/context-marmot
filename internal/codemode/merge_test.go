package codemode

import (
	"context"
	"testing"

	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// TestExecute_Writes_MergeSnapshotsInboundSources merges `from` (db/users,
// which has an inbound edge from auth/login) into `into` (auth/logout). This
// exercises the merge snapshot path that pulls in third-party edge sources —
// inboundSourceIDs and mergeIDLists — plus the merge branch of
// canonicalizeCommandIDs.
func TestExecute_Writes_MergeSnapshotsInboundSources(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	stack := curator.NewUndoStack()
	write := &WriteContext{
		SessionID: "test-session",
		Namespace: "default",
		UndoStack: stack,
	}

	r := ex.ExecuteWithWrites(context.Background(), `
        return client.merge("auth/logout", "db/users");
    `, write)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	res := r.Value.(map[string]any)
	if res["applied"] != true {
		t.Fatalf("expected merge applied=true, got %+v", res)
	}
	if len(r.Mutations) != 1 || r.Mutations[0].Op != "merge" {
		t.Fatalf("expected one merge mutation, got %+v", r.Mutations)
	}

	// The undo entry must snapshot both the merged node AND auth/login (the
	// inbound source whose edge got rewritten) — otherwise undo corrupts the
	// graph. So expect at least 2 snapshots.
	entry := stack.Pop("test-session")
	if entry == nil {
		t.Fatal("expected an undo entry for the merge")
	}
	if len(entry.Snapshots) < 2 {
		t.Fatalf("expected >=2 snapshots (from + inbound source), got %d", len(entry.Snapshots))
	}
}

// TestExecute_Writes_Unlink removes an existing edge, covering the unlink
// registration and the link/unlink branch of canonicalizeCommandIDs.
func TestExecute_Writes_Unlink(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	write := &WriteContext{
		SessionID: "test-session",
		Namespace: "default",
		UndoStack: curator.NewUndoStack(),
	}

	// auth/login --reads--> db/users exists in the fixture.
	r := ex.ExecuteWithWrites(context.Background(), `
        return client.unlink("auth/login", "reads", "db/users");
    `, write)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	res := r.Value.(map[string]any)
	if res["applied"] != true {
		t.Fatalf("expected unlink applied=true, got %+v", res)
	}
}

// TestInboundSourceIDs_Direct unit-tests the helper on a small graph, including
// the nil-graph and no-edge branches.
func TestInboundSourceIDs_Direct(t *testing.T) {
	if got := inboundSourceIDs(nil, "x"); got != nil {
		t.Errorf("expected nil for nil graph, got %v", got)
	}

	g := graph.NewGraph()
	_ = g.UpsertNode(&node.Node{ID: "a", Status: "active", Edges: []node.Edge{{Target: "b", Relation: "uses"}}})
	_ = g.UpsertNode(&node.Node{ID: "b", Status: "active"})

	// b has one inbound source: a.
	got := inboundSourceIDs(g, "b")
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("expected [a] inbound to b, got %v", got)
	}
	// A node with no inbound edges returns nil.
	if got := inboundSourceIDs(g, "a"); got != nil {
		t.Errorf("expected nil inbound for a, got %v", got)
	}
}

// TestMergeIDLists_Direct unit-tests the union helper, including dedup within
// each list and across lists.
func TestMergeIDLists_Direct(t *testing.T) {
	got := mergeIDLists([]string{"a", "a", "b"}, []string{"b", "c"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: expected %q, got %q (%v)", i, want[i], got[i], got)
		}
	}
}
