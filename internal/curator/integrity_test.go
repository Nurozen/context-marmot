package curator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// CollectIntegrityIssues — the shared collector behind /verify and the
// GET /api/curator/suggestions integrity_issues field. The two consumers
// must count the same set (ui_validation2 Issue 1: /verify said "8 issues"
// while the Issues tab showed only 2 curator suggestions).
// ---------------------------------------------------------------------------

func TestCollectIntegrityIssues_MatchesVerifyCount(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "ok", Type: "concept", Status: "active"})
	// Two dangling "contains" edges (hard errors) on one node.
	addTestNode(t, eng, &node.Node{
		ID: "broken", Type: "module", Status: "active",
		Edges: []node.Edge{
			{Target: "ghost/one", Relation: node.Contains},
			{Target: "ghost/two", Relation: node.Contains},
		},
	})

	issues, nodes := CollectIntegrityIssues(eng, nil)
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes covered, got %d", len(nodes))
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 dangling-edge issues, got %d: %+v", len(issues), issues)
	}
	for _, issue := range issues {
		if issue.NodeID != "broken" {
			t.Errorf("issue node = %q, want broken", issue.NodeID)
		}
		if string(issue.IssueType) != "dangling_edge" {
			t.Errorf("issue type = %q, want dangling_edge", issue.IssueType)
		}
	}

	// The /verify chat message must report the same count.
	result, err := ExecuteCommand(context.Background(), &SlashCommand{Name: "verify"}, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("found %d issue(s)", len(issues))
	if !strings.Contains(result.Message, want) {
		t.Errorf("verify message %q should contain %q", result.Message, want)
	}
}

func TestCollectIntegrityIssues_ScopedSelection(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "clean", Type: "concept", Status: "active"})
	addTestNode(t, eng, &node.Node{
		ID: "dirty", Type: "module", Status: "active",
		Edges: []node.Edge{{Target: "nowhere", Relation: node.Contains}},
	})

	// Scoped to the clean node only: an edge from clean to dirty would not
	// dangle, and dirty's issue is out of scope.
	issues, nodes := CollectIntegrityIssues(eng, []string{"clean"})
	if len(nodes) != 1 || nodes[0].ID != "clean" {
		t.Fatalf("expected scope of exactly [clean], got %d nodes", len(nodes))
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues in the clean scope, got %+v", issues)
	}

	// Scoped to the dirty node: its dangling edge is reported.
	issues, _ = CollectIntegrityIssues(eng, []string{"dirty"})
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue for dirty scope, got %d", len(issues))
	}
}

func TestCollectIntegrityIssues_EmptyGraph(t *testing.T) {
	eng := setupTestEngine(t)
	issues, nodes := CollectIntegrityIssues(eng, nil)
	if len(issues) != 0 || len(nodes) != 0 {
		t.Errorf("expected nothing on an empty graph, got %d issues / %d nodes", len(issues), len(nodes))
	}
}
