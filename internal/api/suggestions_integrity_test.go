package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/curator"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// GET /api/curator/suggestions — integrity_issues field
//
// The Issues panel loads this endpoint; the integrity issues it returns must
// be the exact set the /verify slash command counts (ui_validation2 Issue 1:
// "/verify reports 8 issues but the Issues tab shows only 2 suggestions").
// ---------------------------------------------------------------------------

func getSuggestions(t *testing.T, server *Server, query string) SuggestionsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/curator/suggestions"+query, nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("suggestions returned %d: %s", w.Code, w.Body.String())
	}
	var resp SuggestionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode suggestions: %v", err)
	}
	return resp
}

// injectDanglingEdge appends an edge to a missing target on an existing node,
// through the same store+graph path the command handlers use.
func injectDanglingEdge(t *testing.T, engine *mcpserver.Engine, id, missingTarget string) {
	t.Helper()
	diskNode, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(id))
	if err != nil {
		t.Fatalf("load node %s: %v", id, err)
	}
	diskNode.Edges = append(diskNode.Edges, node.Edge{Target: missingTarget, Relation: node.Contains})
	if err := engine.NodeStore.SaveNode(diskNode); err != nil {
		t.Fatalf("save node %s: %v", id, err)
	}
	if err := engine.GetGraph().UpsertNode(diskNode); err != nil {
		t.Fatalf("upsert node %s: %v", id, err)
	}
}

func TestSuggestions_IntegrityIssuesEmptyArrayWhenClean(t *testing.T) {
	server, _ := newTestServer(t)

	// The raw body must carry integrity_issues as [] (not null / absent) so
	// the frontend never has to null-check the key.
	req := httptest.NewRequest(http.MethodGet, "/api/curator/suggestions", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("suggestions returned %d", w.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	blob, ok := raw["integrity_issues"]
	if !ok {
		t.Fatal("response missing integrity_issues key")
	}
	if strings.TrimSpace(string(blob)) == "null" {
		t.Fatal("integrity_issues must be an array, got null")
	}

	resp := getSuggestions(t, server, "")
	if len(resp.IntegrityIssues) != 0 {
		t.Errorf("expected no integrity issues on the clean fixture, got %+v", resp.IntegrityIssues)
	}
	// The fixture has 4 nodes, all active.
	if resp.IntegrityNodeCount != 4 {
		t.Errorf("integrity_node_count = %d, want 4", resp.IntegrityNodeCount)
	}
}

func TestSuggestions_IntegrityIssuesMatchVerify(t *testing.T) {
	server, engine := newTestServer(t)

	injectDanglingEdge(t, engine, "api/routes", "ghost/missing")

	resp := getSuggestions(t, server, "")
	if len(resp.IntegrityIssues) != 1 {
		t.Fatalf("expected 1 integrity issue, got %d: %+v", len(resp.IntegrityIssues), resp.IntegrityIssues)
	}
	issue := resp.IntegrityIssues[0]
	if issue.NodeID != "api/routes" {
		t.Errorf("node_id = %q, want api/routes", issue.NodeID)
	}
	if issue.Type != "dangling_edge" {
		t.Errorf("type = %q, want dangling_edge", issue.Type)
	}
	if issue.Severity != "error" {
		t.Errorf("severity = %q, want error (dangling contains edge)", issue.Severity)
	}
	if !strings.Contains(issue.Message, "ghost/missing") {
		t.Errorf("message should mention the missing target, got %q", issue.Message)
	}

	// Consistency with the /verify chat message: same count, same scope.
	result, err := curator.ExecuteCommand(context.Background(),
		&curator.SlashCommand{Name: "verify"}, engine, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(result.Message, "found 1 issue(s)") {
		t.Errorf("/verify message %q disagrees with the API's 1 issue", result.Message)
	}
	if !strings.Contains(result.Message, "4 node(s)") {
		t.Errorf("/verify message %q should cover 4 nodes", result.Message)
	}
	if resp.IntegrityNodeCount != 4 {
		t.Errorf("integrity_node_count = %d, want 4 to match /verify", resp.IntegrityNodeCount)
	}
}

func TestSuggestions_IntegrityIssuesNamespaceScoped(t *testing.T) {
	server, engine := newTestServer(t)

	injectDanglingEdge(t, engine, "db/users", "ghost/gone")

	// Matching namespace: the issue is included.
	resp := getSuggestions(t, server, "?ns=default")
	if len(resp.IntegrityIssues) != 1 {
		t.Fatalf("expected 1 issue for ns=default, got %d", len(resp.IntegrityIssues))
	}

	// Unknown namespace: nothing is scanned — and crucially the empty
	// selection must NOT fall back to the whole graph.
	resp = getSuggestions(t, server, "?ns=nope")
	if len(resp.IntegrityIssues) != 0 {
		t.Errorf("expected no issues for an unknown namespace, got %+v", resp.IntegrityIssues)
	}
	if resp.IntegrityNodeCount != 0 {
		t.Errorf("integrity_node_count = %d, want 0 for unknown namespace", resp.IntegrityNodeCount)
	}
}
