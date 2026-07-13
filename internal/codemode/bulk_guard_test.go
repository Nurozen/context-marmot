package codemode

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/curator"
)

// ---------------------------------------------------------------------------
// Bulk mutation guard
//
// A single generated program may not mutate more than max(5, 50% of the
// graph's active nodes) distinct nodes unless it calls client.allowBulk()
// first. This is the server-side safety net for over-broad NL curation
// (ui_validation2 Issue 3: "tag the auth-related nodes" tagged all 16 nodes
// because mock-embedder search returned everything).
// ---------------------------------------------------------------------------

// newBulkRig builds a rig with 12 active nodes (3 standard + 9 extra), so
// the guard limit is max(5, 12/2) = 6.
func newBulkRig(t *testing.T) *testRig {
	t.Helper()
	return newTestRigWith(t, func(r *testRig) {
		for i := 0; i < 9; i++ {
			r.writeNode(t, "svc", fmt.Sprintf("extra%d", i), "function",
				fmt.Sprintf("Extra service function %d", i), nil)
		}
	})
}

func allNodeIDsJS(rig *testRig) string {
	ids := make([]string, 0)
	for _, n := range rig.engine.GetGraph().AllActiveNodes() {
		ids = append(ids, `"`+n.ID+`"`)
	}
	return "[" + strings.Join(ids, ",") + "]"
}

func newWriteContext() *WriteContext {
	return &WriteContext{
		SessionID: "bulk-session",
		Namespace: "default",
		UndoStack: curator.NewUndoStack(),
	}
}

func TestBulkGuard_BlocksWholeGraphMutation(t *testing.T) {
	rig := newBulkRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	write := newWriteContext()

	r := ex.ExecuteWithWrites(context.Background(),
		"return client.tag("+allNodeIDsJS(rig)+", \"security\");", write)

	if r.Error == "" {
		t.Fatalf("expected bulk mutation guard error, got success: %v", r.Value)
	}
	if !strings.Contains(r.Error, "bulk mutation guard") {
		t.Errorf("error should name the bulk mutation guard, got: %s", r.Error)
	}
	if !strings.Contains(r.Error, "allowBulk") {
		t.Errorf("error should point at client.allowBulk(), got: %s", r.Error)
	}

	// Nothing was applied: no tags on any node, no undo entry.
	for _, n := range rig.engine.GetGraph().AllActiveNodes() {
		for _, tag := range n.Tags {
			if tag == "security" {
				t.Errorf("node %s was tagged despite the guard", n.ID)
			}
		}
	}
	if write.UndoStack.Len("bulk-session") != 0 {
		t.Errorf("expected empty undo stack, got %d entries", write.UndoStack.Len("bulk-session"))
	}

	// The rejected attempt is visible in the audit trail.
	if len(r.Mutations) != 1 || r.Mutations[0].Success {
		t.Fatalf("expected exactly one failed mutation record, got %+v", r.Mutations)
	}
	if !strings.Contains(r.Mutations[0].Message, "bulk mutation guard") {
		t.Errorf("mutation record should carry the guard message, got: %s", r.Mutations[0].Message)
	}
}

func TestBulkGuard_AllowBulkOptInSucceeds(t *testing.T) {
	rig := newBulkRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	write := newWriteContext()

	r := ex.ExecuteWithWrites(context.Background(), `
        client.allowBulk();
        const s = client.tag(`+allNodeIDsJS(rig)+`, "global");
        return s.applied;
    `, write)

	if r.Error != "" {
		t.Fatalf("unexpected error with allowBulk: %s", r.Error)
	}
	if r.Value != true {
		t.Fatalf("expected applied=true, got %v", r.Value)
	}
	tagged := 0
	for _, n := range rig.engine.GetGraph().AllActiveNodes() {
		for _, tag := range n.Tags {
			if tag == "global" {
				tagged++
			}
		}
	}
	if tagged != 12 {
		t.Errorf("expected all 12 nodes tagged, got %d", tagged)
	}
}

func TestBulkGuard_AtLimitAllowed_OverLimitBlocked(t *testing.T) {
	rig := newBulkRig(t) // 12 active nodes -> limit 6
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)

	ids := make([]string, 0, 12)
	for _, n := range rig.engine.GetGraph().AllActiveNodes() {
		ids = append(ids, n.ID)
	}
	quote := func(ss []string) string {
		out := make([]string, len(ss))
		for i, s := range ss {
			out[i] = `"` + s + `"`
		}
		return "[" + strings.Join(out, ",") + "]"
	}

	// Exactly the limit (6 of 12) is allowed.
	r := ex.ExecuteWithWrites(context.Background(),
		"return client.tag("+quote(ids[:6])+", \"six\").applied;", newWriteContext())
	if r.Error != "" || r.Value != true {
		t.Fatalf("tagging exactly the limit should pass, got error=%q value=%v", r.Error, r.Value)
	}

	// One more (7 of 12) trips the guard.
	r = ex.ExecuteWithWrites(context.Background(),
		"return client.tag("+quote(ids[:7])+", \"seven\");", newWriteContext())
	if r.Error == "" || !strings.Contains(r.Error, "bulk mutation guard") {
		t.Fatalf("tagging over the limit should trip the guard, got error=%q", r.Error)
	}
}

func TestBulkGuard_AccumulatesAcrossCalls(t *testing.T) {
	rig := newBulkRig(t) // limit 6
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	write := newWriteContext()

	// A loop of single-node writes must trip once the distinct total
	// crosses the limit, even though each call is small.
	r := ex.ExecuteWithWrites(context.Background(), `
        const ids = `+allNodeIDsJS(rig)+`;
        let applied = 0;
        for (const id of ids) {
            client.tag(id, "loop");
            applied++;
        }
        return applied;
    `, write)

	if r.Error == "" || !strings.Contains(r.Error, "bulk mutation guard") {
		t.Fatalf("expected the loop to trip the bulk guard, got error=%q value=%v", r.Error, r.Value)
	}
	// The first 6 writes succeeded and stay undoable; the 7th was rejected.
	success := 0
	for _, m := range r.Mutations {
		if m.Success {
			success++
		}
	}
	if success != 6 {
		t.Errorf("expected 6 successful mutations before the guard, got %d", success)
	}
	if write.UndoStack.Len("bulk-session") != 6 {
		t.Errorf("expected 6 undo entries, got %d", write.UndoStack.Len("bulk-session"))
	}
}

func TestBulkGuard_RepeatWritesToSameNodeDontAccumulate(t *testing.T) {
	rig := newBulkRig(t) // limit 6
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)

	// 10 writes, all against one node: only 1 distinct node -> no guard.
	r := ex.ExecuteWithWrites(context.Background(), `
        for (let i = 0; i < 10; i++) {
            client.tag("auth/login", "t" + i);
        }
        return true;
    `, newWriteContext())

	if r.Error != "" {
		t.Fatalf("repeat writes to one node must not trip the guard: %s", r.Error)
	}
}

func TestBulkGuard_SmallGraphFloorOfFive(t *testing.T) {
	rig := newTestRig(t) // 3 active nodes -> limit max(5, 1) = 5
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)

	// Mutating the whole 3-node graph is under the floor of 5: allowed.
	r := ex.ExecuteWithWrites(context.Background(), `
        return client.tag(["auth/login", "auth/logout", "db/users"], "small").applied;
    `, newWriteContext())

	if r.Error != "" || r.Value != true {
		t.Fatalf("whole-graph write under the floor of 5 should pass, got error=%q value=%v", r.Error, r.Value)
	}
}

// ---------------------------------------------------------------------------
// Prompt mandates for the over-apply guard (ui_validation2 Issue 3)
// ---------------------------------------------------------------------------

func TestBuildPhase1Prompt_MutationSafetyMandates(t *testing.T) {
	got := BuildPhase1Prompt(curator.GraphStats{NodeCount: 16, EdgeCount: 12}, nil, false)

	for _, want := range []string{
		"Mutation safety",
		"relevance filtering is MANDATORY",
		"NEVER pipe raw",
		"score",
		"DO NOT mutate anything",
		"bulk mutation guard",
		"client.allowBulk()",
		"max(5, 50% of the graph's nodes)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("phase-1 prompt missing mutation-safety mandate %q", want)
		}
	}
}

func TestSDKReference_DocumentsAllowBulkAndScore(t *testing.T) {
	for _, want := range []string{
		"client.allowBulk()",
		"bulk mutation guard",
		"score?: number",
	} {
		if !strings.Contains(SDKReference, want) {
			t.Errorf("SDK reference missing %q", want)
		}
	}
}

func TestSearchHitsCarryScores(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	r := ex.Execute(context.Background(), `
        const hits = client.search("login handler");
        if (!hits.length) return "no hits";
        return typeof hits[0].score;
    `)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	if r.Value != "number" {
		t.Errorf("search hits should carry a numeric score, got %v", r.Value)
	}
}
