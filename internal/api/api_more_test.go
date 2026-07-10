package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/summary"
)

// ---------------------------------------------------------------------------
// Pure helper unit tests
// ---------------------------------------------------------------------------

func TestStripCodeBlocks(t *testing.T) {
	in := "Here is the answer.\n```js\nreturn client.getStats();\n```\nDone."
	got := stripCodeBlocks(in)
	if strings.Contains(got, "getStats") || strings.Contains(got, "```") {
		t.Errorf("expected code fence removed, got %q", got)
	}
	if !strings.Contains(got, "Here is the answer.") || !strings.Contains(got, "Done.") {
		t.Errorf("expected surrounding text preserved, got %q", got)
	}
	// No fences: returned trimmed unchanged.
	if got := stripCodeBlocks("  plain text  "); got != "plain text" {
		t.Errorf("expected trimmed plain text, got %q", got)
	}
	// Unterminated fence: loop breaks, remaining text preserved.
	if got := stripCodeBlocks("text ```unterminated"); !strings.Contains(got, "unterminated") {
		t.Errorf("expected unterminated fence preserved, got %q", got)
	}
}

func TestTruncatePreview(t *testing.T) {
	if got := truncatePreview("short", 80); got != "short" {
		t.Errorf("expected unchanged short string, got %q", got)
	}
	long := strings.Repeat("a", 100)
	got := truncatePreview(long, 10)
	if len(got) != 13 || !strings.HasSuffix(got, "...") {
		t.Errorf("expected 10 chars + '...', got %q (len %d)", got, len(got))
	}
}

func TestSplitQualifiedVaultID(t *testing.T) {
	cases := []struct {
		in            string
		vault, nodeID string
		ok            bool
	}{
		{"@vaultA/some/node", "vaultA", "some/node", true},
		{"noprefix/node", "", "", false},
		{"@", "", "", false},
		{"@vaultOnly", "", "", false},
		{"@/node", "", "", false},
		{"@vault/", "", "", false},
	}
	for _, c := range cases {
		v, n, ok := splitQualifiedVaultID(c.in)
		if ok != c.ok || v != c.vault || n != c.nodeID {
			t.Errorf("splitQualifiedVaultID(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, v, n, ok, c.vault, c.nodeID, c.ok)
		}
	}
}

func TestMatchNamespace(t *testing.T) {
	if !matchNamespace("auth", "auth") {
		t.Error("exact match should be true")
	}
	if !matchNamespace("", "default") {
		t.Error("empty vs default should match")
	}
	if !matchNamespace("default", "") {
		t.Error("default vs empty should match")
	}
	if matchNamespace("auth", "billing") {
		t.Error("different namespaces should not match")
	}
}

func TestDedupeAndRankSearchResults(t *testing.T) {
	in := []embedding.ScoredResult{
		{NodeID: "a", Score: 0.5},
		{NodeID: "b", Score: 0.9},
		{NodeID: "a", Score: 0.4}, // duplicate, lower score
		{NodeID: "c", Score: 0.7},
	}
	out := dedupeAndRankSearchResults(in, 2)
	if len(out) != 2 {
		t.Fatalf("expected 2 results after limit, got %d", len(out))
	}
	// Highest scores first: b (0.9), c (0.7).
	if out[0].NodeID != "b" || out[1].NodeID != "c" {
		t.Errorf("expected [b, c] ranked, got %+v", out)
	}
}

func TestNodeToAPI_WithSource(t *testing.T) {
	n := &node.Node{
		ID:        "src/node",
		Type:      "function",
		Namespace: "default",
		Status:    "active",
		Summary:   "with source",
		Source:    node.Source{Path: "src/foo.go", Lines: [2]int{1, 10}, Hash: "abc123"},
		Edges:     []node.Edge{{Target: "other", Relation: node.Calls, Class: node.Behavioral}},
	}
	api := nodeToAPI(n, 3)
	if api.Source == nil {
		t.Fatal("expected non-nil Source")
	}
	if api.Source.Path != "src/foo.go" || api.Source.Hash != "abc123" || api.Source.Lines != [2]int{1, 10} {
		t.Errorf("unexpected source mapping: %+v", api.Source)
	}
	if api.EdgeCount != 3 || len(api.Edges) != 1 {
		t.Errorf("unexpected edge mapping: count=%d edges=%v", api.EdgeCount, api.Edges)
	}
	// Nil tags become an empty slice (JSON: []).
	if api.Tags == nil {
		t.Error("expected non-nil tags slice")
	}
}

// ---------------------------------------------------------------------------
// handleVersion
// ---------------------------------------------------------------------------

func TestHandleVersion(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/version", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp VersionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Version != 0 {
		t.Errorf("expected initial version 0, got %d", resp.Version)
	}
	if resp.AppVersion != "dev" {
		t.Errorf("expected default app_version %q, got %q", "dev", resp.AppVersion)
	}

	// After a change, the version bumps.
	server.NotifyChange()
	rec2 := doRequest(t, handler, "GET", "/api/version", "")
	var resp2 VersionResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp2.Version != 1 {
		t.Errorf("expected version 1 after NotifyChange, got %d", resp2.Version)
	}

	// The build version threaded from cmd/marmot surfaces as app_version.
	server.WithAppVersion("v0.1.10-test")
	rec3 := doRequest(t, handler, "GET", "/api/version", "")
	var resp3 VersionResponse
	if err := json.NewDecoder(rec3.Body).Decode(&resp3); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp3.AppVersion != "v0.1.10-test" {
		t.Errorf("expected app_version %q, got %q", "v0.1.10-test", resp3.AppVersion)
	}
	// An empty version string must not clobber the current value.
	server.WithAppVersion("")
	rec4 := doRequest(t, handler, "GET", "/api/version", "")
	var resp4 VersionResponse
	if err := json.NewDecoder(rec4.Body).Decode(&resp4); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp4.AppVersion != "v0.1.10-test" {
		t.Errorf("expected app_version to stay %q, got %q", "v0.1.10-test", resp4.AppVersion)
	}
}

// ---------------------------------------------------------------------------
// handleSuggestions
// ---------------------------------------------------------------------------

func TestHandleSuggestions(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()

	// Add an orphan + untyped node to guarantee suggestions.
	orphan := &node.Node{ID: "lonely/orphan", Type: "", Namespace: "default", Status: node.StatusActive, Summary: ""}
	if err := engine.GetGraph().AddNode(orphan); err != nil {
		t.Fatal(err)
	}
	// Superseded nodes are never analyzed and must not inflate node_count
	// (the UI derives its "N nodes · X% curated" health summary from it).
	old := &node.Node{ID: "lonely/old", Type: "concept", Namespace: "default", Status: node.StatusSuperseded, Summary: "old"}
	if err := engine.GetGraph().AddNode(old); err != nil {
		t.Fatal(err)
	}
	activeCount := len(engine.GetGraph().AllActiveNodes())

	rec := doRequest(t, handler, "GET", "/api/curator/suggestions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp SuggestionsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.NodeCount != activeCount {
		t.Errorf("expected node_count %d (active nodes only), got %d", activeCount, resp.NodeCount)
	}
	if len(resp.Suggestions) == 0 {
		t.Error("expected at least one suggestion for the orphan node")
	}

	// With namespace scoping + pagination params.
	rec2 := doRequest(t, handler, "GET", "/api/curator/suggestions?ns=default&limit=1&offset=0&check_stale=true", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with params, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp2 SuggestionsResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp2.Suggestions) > 1 {
		t.Errorf("expected at most 1 suggestion with limit=1, got %d", len(resp2.Suggestions))
	}
}

// ---------------------------------------------------------------------------
// handleHeat with a populated heat map
// ---------------------------------------------------------------------------

func TestHandleHeatWithPairs(t *testing.T) {
	server, engine := newTestServer(t)
	hm := heatmap.New("default")
	// Record co-access between two known default-namespace nodes.
	hm.RecordCoAccess([]string{"auth/login", "auth/token"}, 0.9)
	engine.WithHeatMap(hm)
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/heat/default", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Pairs []APIHeatPair `json:"pairs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Pairs) == 0 {
		t.Fatal("expected at least one heat pair")
	}
	found := false
	for _, p := range resp.Pairs {
		if (p.A == "auth/login" && p.B == "auth/token") || (p.A == "auth/token" && p.B == "auth/login") {
			found = true
			if p.Weight <= 0 {
				t.Errorf("expected positive weight, got %f", p.Weight)
			}
		}
	}
	if !found {
		t.Errorf("expected auth/login <-> auth/token pair, got %+v", resp.Pairs)
	}

	// Graph endpoint also surfaces heat pairs for the namespace.
	recGraph := doRequest(t, handler, "GET", "/api/graph/default", "")
	var graphResp GraphResponse
	if err := json.NewDecoder(recGraph.Body).Decode(&graphResp); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	if len(graphResp.HeatPairs) == 0 {
		t.Error("expected heat pairs embedded in graph response")
	}

	// _all view also includes heat pairs.
	recAll := doRequest(t, handler, "GET", "/api/graph/_all", "")
	var allResp GraphResponse
	if err := json.NewDecoder(recAll.Body).Decode(&allResp); err != nil {
		t.Fatalf("decode _all: %v", err)
	}
	if len(allResp.HeatPairs) == 0 {
		t.Error("expected heat pairs in _all response")
	}
}

// ---------------------------------------------------------------------------
// handleBridges with a namespace manager holding bridges
// ---------------------------------------------------------------------------

func TestHandleBridgesWithBridges(t *testing.T) {
	server, engine := newTestServer(t)
	engine.NSManager = &namespace.Manager{
		VaultDir: engine.MarmotDir,
		Namespaces: map[string]*namespace.Namespace{
			"auth": {Name: "auth"},
			"db":   {Name: "db"},
		},
		Bridges: map[string]*namespace.Bridge{
			"auth--db": {Source: "auth", Target: "db", AllowedRelations: []string{"reads"}},
		},
		CrossVaultBridges: []*namespace.Bridge{
			{Source: "auth", Target: "remote", AllowedRelations: []string{"references"}, SourceVaultID: "v1", TargetVaultID: "v2"},
		},
	}
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/bridges", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp BridgesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Bridges) != 2 {
		t.Fatalf("expected 2 bridges (1 regular + 1 cross-vault), got %d: %+v", len(resp.Bridges), resp.Bridges)
	}
	var sawCrossVault bool
	for _, b := range resp.Bridges {
		if b.Target == "remote" {
			sawCrossVault = true
			if !b.IsCrossVault {
				t.Error("expected cross-vault bridge flagged IsCrossVault=true")
			}
		}
	}
	if !sawCrossVault {
		t.Error("expected the cross-vault bridge in the response")
	}
}

// ---------------------------------------------------------------------------
// handleSummary success path
// ---------------------------------------------------------------------------

func TestHandleSummarySuccess(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()

	if err := summary.WriteSummary(engine.MarmotDir, "default", &summary.SummaryResult{
		Namespace:   "default",
		Content:     "This namespace holds authentication logic.",
		NodeCount:   4,
		GeneratedAt: time.Now(),
	}); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	rec := doRequest(t, handler, "GET", "/api/summary/default", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp SummaryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Namespace != "default" || !strings.Contains(resp.Content, "authentication") {
		t.Errorf("unexpected summary response: %+v", resp)
	}
	if resp.NodeCount != 4 {
		t.Errorf("expected node count 4, got %d", resp.NodeCount)
	}
}

// ---------------------------------------------------------------------------
// Warren success paths: list, status, refresh
// ---------------------------------------------------------------------------

func TestWarrenListStatusRefreshSuccess(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	setupAPIWarren(t, workspaceRoot, "product-platform", "project-a", "project-a-vault")
	wireWarrenVaultRegistry(t, engine)

	// List warrens.
	recList := doRequest(t, handler, "GET", "/api/warrens", "")
	if recList.Code != http.StatusOK {
		t.Fatalf("warrens list: expected 200, got %d: %s", recList.Code, recList.Body.String())
	}
	var listResp WarrensResponse
	if err := json.NewDecoder(recList.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode warrens: %v", err)
	}
	if _, ok := listResp.Warrens["product-platform"]; !ok {
		t.Errorf("expected product-platform in warren list, got %+v", listResp.Warrens)
	}

	// Status for the registered warren.
	recStatus := doRequest(t, handler, "GET", "/api/warren/product-platform/status", "")
	if recStatus.Code != http.StatusOK {
		t.Fatalf("warren status: expected 200, got %d: %s", recStatus.Code, recStatus.Body.String())
	}
	var statusResp WarrenStatusResponse
	if err := json.NewDecoder(recStatus.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if statusResp.WarrenID != "product-platform" {
		t.Errorf("expected warren id product-platform, got %q", statusResp.WarrenID)
	}
	if len(statusResp.Projects) == 0 {
		t.Error("expected at least one project in status")
	}

	// Refresh the registered warren.
	recRefresh := doRequest(t, handler, "POST", "/api/warren/product-platform/refresh", "")
	if recRefresh.Code != http.StatusOK {
		t.Fatalf("warren refresh: expected 200, got %d: %s", recRefresh.Code, recRefresh.Body.String())
	}
	var refreshResp map[string]string
	if err := json.NewDecoder(recRefresh.Body).Decode(&refreshResp); err != nil {
		t.Fatalf("decode refresh: %v", err)
	}
	if refreshResp["warren_id"] != "product-platform" {
		t.Errorf("expected refresh warren_id product-platform, got %q", refreshResp["warren_id"])
	}
	if refreshResp["status"] != "reloaded" {
		t.Errorf("expected refresh status %q, got %q", "reloaded", refreshResp["status"])
	}
}

// TestWarrenRefreshPicksUpNewMount (B3.1): a warren mounted AFTER the server
// started becomes searchable once POST /api/warren/{id}/refresh reloads the
// engine's warren state — the endpoint is a real trigger, not a printf stub.
func TestWarrenRefreshPicksUpNewMount(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)

	// Simulate startup with no mounts: an always-created empty registry.
	t.Setenv("MARMOT_ROUTES", "off")
	engine.WithVaultRegistry(namespace.NewVaultRegistry("", engine.MarmotDir, nil, routes.EmptyTable()))
	if err := engine.ReloadWarrenState(); err != nil {
		t.Fatalf("initial ReloadWarrenState: %v", err)
	}

	// Mount a warren while the server is live.
	setupAPIWarren(t, workspaceRoot, "product-platform", "project-a", "project-a-vault")

	// The registry does not know the vault yet (no reload since the mount).
	searchPath := "/api/search?q=" + url.QueryEscape("Service API") + "&ns=" + url.QueryEscape("_warren/product-platform")
	recBefore := doRequest(t, handler, "GET", searchPath, "")
	if recBefore.Code != http.StatusOK {
		t.Fatalf("search before refresh: expected 200, got %d: %s", recBefore.Code, recBefore.Body.String())
	}
	var respBefore SearchResponse
	if err := json.NewDecoder(recBefore.Body).Decode(&respBefore); err != nil {
		t.Fatalf("decode search before refresh: %v", err)
	}
	if len(respBefore.Results) != 0 {
		t.Fatalf("expected no warren results before refresh, got %+v", respBefore.Results)
	}

	// POST the refresh endpoint, then the mount is queryable.
	recRefresh := doRequest(t, handler, "POST", "/api/warren/product-platform/refresh", "")
	if recRefresh.Code != http.StatusOK {
		t.Fatalf("refresh: expected 200, got %d: %s", recRefresh.Code, recRefresh.Body.String())
	}
	recAfter := doRequest(t, handler, "GET", searchPath, "")
	if recAfter.Code != http.StatusOK {
		t.Fatalf("search after refresh: expected 200, got %d: %s", recAfter.Code, recAfter.Body.String())
	}
	var respAfter SearchResponse
	if err := json.NewDecoder(recAfter.Body).Decode(&respAfter); err != nil {
		t.Fatalf("decode search after refresh: %v", err)
	}
	found := false
	for _, r := range respAfter.Results {
		if r.NodeID == "@project-a-vault/service/api" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected @project-a-vault/service/api after refresh, got %+v", respAfter.Results)
	}
}

// ---------------------------------------------------------------------------
// handleChatUndo: LIFO pop, created-node deletion, error paths
// ---------------------------------------------------------------------------

func TestHandleChatUndo_LIFOAndErrors(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// Invalid JSON -> 400.
	if rec := doRequest(t, handler, "POST", "/api/chat/undo", "{bad"); rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rec.Code)
	}
	// Missing session_id -> 400.
	if rec := doRequest(t, handler, "POST", "/api/chat/undo", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing session_id, got %d", rec.Code)
	}
	// No entries -> 404.
	if rec := doRequest(t, handler, "POST", "/api/chat/undo", `{"session_id":"empty"}`); rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for empty undo stack, got %d", rec.Code)
	}

	// Perform a slash tag mutation, then LIFO-undo (no undo_id).
	tagBody := `{"message":"/tag reversible","session_id":"lifo","selected_nodes":["auth/login"]}`
	recTag := doRequest(t, handler, "POST", "/api/chat", tagBody)
	if recTag.Code != http.StatusOK {
		t.Fatalf("tag failed: %d %s", recTag.Code, recTag.Body.String())
	}
	recUndo := doRequest(t, handler, "POST", "/api/chat/undo", `{"session_id":"lifo"}`)
	if recUndo.Code != http.StatusOK {
		t.Fatalf("LIFO undo failed: %d %s", recUndo.Code, recUndo.Body.String())
	}
	var undoResp ChatUndoResponse
	if err := json.NewDecoder(recUndo.Body).Decode(&undoResp); err != nil {
		t.Fatalf("decode undo: %v", err)
	}
	if undoResp.Restored < 1 {
		t.Errorf("expected at least 1 restored node, got %d", undoResp.Restored)
	}
}

// TestHandleChatUndo_DeletesCreatedNode exercises the branch where a snapshot
// records a node that did NOT exist before the mutation (created), so undo
// deletes it.
func TestHandleChatUndo_DeletesCreatedNode(t *testing.T) {
	server, engine := newTestServer(t)

	// Manually craft an undo entry with a "created" snapshot (Existed=false,
	// non-nil Node) and push it onto the stack.
	created := &node.Node{ID: "created/node", Type: "concept", Namespace: "default", Status: node.StatusActive, Summary: "created by mutation"}
	if err := engine.NodeStore.SaveNode(created); err != nil {
		t.Fatal(err)
	}
	if err := engine.GetGraph().UpsertNode(created); err != nil {
		t.Fatal(err)
	}
	server.undoStack.Push("sess-created", curator.UndoEntry{
		ID:        "undo-created",
		SessionID: "sess-created",
		Timestamp: time.Now(),
		Snapshots: []curator.NodeSnapshot{{Node: created, Existed: false}},
		Created:   []string{"created/node"},
	})

	handler := server.Handler()
	rec := doRequest(t, handler, "POST", "/api/chat/undo", `{"session_id":"sess-created"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("undo failed: %d %s", rec.Code, rec.Body.String())
	}
	// The created node should now be removed from the graph.
	if _, ok := engine.GetGraph().GetNode("created/node"); ok {
		t.Error("expected created/node to be removed from the graph after undo")
	}
}

// ---------------------------------------------------------------------------
// handleSDKCall success paths (structured content + parsed text)
// ---------------------------------------------------------------------------

func TestHandleSDKCall_ContextWriteSuccess(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	body := `{"id":"sdk/created","type":"function","summary":"created via SDK","namespace":"default"}`
	rec := doRequest(t, handler, "POST", "/api/sdk/context_write", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for context_write, got %d: %s", rec.Code, rec.Body.String())
	}
	// Body should be valid JSON (structured or parsed content).
	var parsed any
	if err := json.NewDecoder(rec.Body).Decode(&parsed); err != nil {
		t.Fatalf("expected JSON response body, got error: %v", err)
	}
}

func TestHandleSDKCall_ContextQuerySuccess(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	body := `{"query":"authentication","depth":1,"budget":2048}`
	rec := doRequest(t, handler, "POST", "/api/sdk/context_query", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for context_query, got %d: %s", rec.Code, rec.Body.String())
	}
	// Query returns a context string wrapped in JSON.
	var parsed map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&parsed); err != nil {
		t.Fatalf("expected JSON object, got: %v", err)
	}
}

func TestHandleSDKCall_MissingToolName(t *testing.T) {
	server, _ := newTestServer(t)
	// Hit the handler directly with an empty {tool} value.
	req := httptest.NewRequest("POST", "/api/sdk/", strings.NewReader(`{}`))
	req.SetPathValue("tool", "")
	rec := httptest.NewRecorder()
	server.handleSDKCall(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty tool name, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleNode staleness + raw-id fallback
// ---------------------------------------------------------------------------

func TestHandleNode_StaleAndFallback(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()

	// Write a source file, then store a node with a wrong hash so it reads stale.
	projectRoot := filepath.Dir(engine.MarmotDir)
	srcRel := "src/stale.go"
	srcAbs := filepath.Join(projectRoot, srcRel)
	if err := os.MkdirAll(filepath.Dir(srcAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcAbs, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleNode := &node.Node{
		ID:        "stale/node",
		Type:      "function",
		Namespace: "default",
		Status:    node.StatusActive,
		Summary:   "stale node",
		Source:    node.Source{Path: srcRel, Hash: "wrong-hash"},
	}
	if err := engine.NodeStore.SaveNode(staleNode); err != nil {
		t.Fatal(err)
	}
	if err := engine.GetGraph().UpsertNode(staleNode); err != nil {
		t.Fatal(err)
	}

	rec := doRequest(t, handler, "GET", "/api/node/default/stale/node", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var apiNode APINode
	if err := json.NewDecoder(rec.Body).Decode(&apiNode); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !apiNode.IsStale {
		t.Error("expected node to be reported stale")
	}
}

// ---------------------------------------------------------------------------
// handleSSE via a cancellable request context
// ---------------------------------------------------------------------------

func TestHandleSSE(t *testing.T) {
	server, _ := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		server.handleSSE(rec, req)
		close(done)
	}()

	// Deterministic sync (no sleeps): wait until the handler registered its
	// client channel, notify, then wait until the handler consumed the
	// notification. The handler writes the graph-changed frame immediately
	// after consuming and only observes cancellation at its next select, so
	// draining guarantees the frame is written before cancel().
	waitFor := func(cond func() bool, what string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout waiting for %s", what)
			}
			time.Sleep(time.Millisecond)
		}
	}
	var clientCh chan struct{}
	waitFor(func() bool {
		clientCh = nil
		server.sseClients.Range(func(k, _ any) bool {
			clientCh = k.(chan struct{})
			return false
		})
		return clientCh != nil
	}, "SSE client registration")
	server.NotifyChange()
	waitFor(func() bool { return len(clientCh) == 0 }, "notification consumption")
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleSSE did not return after context cancel")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "\"version\"") {
		t.Errorf("expected an initial version frame, got %q", body)
	}
	if !strings.Contains(body, "graph-changed") {
		t.Errorf("expected a graph-changed event, got %q", body)
	}
}

// ---------------------------------------------------------------------------
// registerRoutes asset-serving branch
// ---------------------------------------------------------------------------

func TestServerWithAssets(t *testing.T) {
	engine := setupTestEngine(t)
	assets := fstest.MapFS{
		"dist/index.html": {Data: []byte("<html><body>marmot spa</body></html>")},
		"dist/app.js":     {Data: []byte("console.log('hi')")},
	}
	server := NewServer(engine, assets)
	handler := server.Handler()

	// Root serves index.html.
	recRoot := doRequest(t, handler, "GET", "/", "")
	if recRoot.Code != http.StatusOK || !strings.Contains(recRoot.Body.String(), "marmot spa") {
		t.Fatalf("expected index.html at root, got %d: %s", recRoot.Code, recRoot.Body.String())
	}
	if ct := recRoot.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}

	// Explicit /index.html also served.
	recIndex := doRequest(t, handler, "GET", "/index.html", "")
	if recIndex.Code != http.StatusOK || !strings.Contains(recIndex.Body.String(), "marmot spa") {
		t.Errorf("expected index.html served, got %d", recIndex.Code)
	}

	// Existing asset served through the file server.
	recAsset := doRequest(t, handler, "GET", "/app.js", "")
	if recAsset.Code != http.StatusOK || !strings.Contains(recAsset.Body.String(), "console.log") {
		t.Errorf("expected app.js served, got %d: %s", recAsset.Code, recAsset.Body.String())
	}

	// Unknown client-side route falls back to index.html (SPA).
	recSPA := doRequest(t, handler, "GET", "/some/client/route", "")
	if recSPA.Code != http.StatusOK || !strings.Contains(recSPA.Body.String(), "marmot spa") {
		t.Errorf("expected SPA fallback to index.html, got %d: %s", recSPA.Code, recSPA.Body.String())
	}

	// API routes still work with assets present.
	recAPI := doRequest(t, handler, "GET", "/api/version", "")
	if recAPI.Code != http.StatusOK {
		t.Errorf("expected API route to work with assets, got %d", recAPI.Code)
	}
}

// Unknown /api/* paths must return a JSON 404 rather than falling through to
// the SPA index.html (which returned 200 text/html and confused API clients).
func TestUnknownAPIPathsReturnJSON404(t *testing.T) {
	engine := setupTestEngine(t)
	assets := fstest.MapFS{
		"dist/index.html": {Data: []byte("<html><body>marmot spa</body></html>")},
	}
	server := NewServer(engine, assets)
	handler := server.Handler()

	for _, path := range []string{"/api", "/api/graph", "/api/nodes", "/api/definitely/not/a/route"} {
		rec := doRequest(t, handler, "GET", path, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: expected 404, got %d: %s", path, rec.Code, rec.Body.String())
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("GET %s: expected JSON content type, got %q", path, ct)
		}
		var resp ErrorResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Errorf("GET %s: expected JSON error body, decode failed: %v", path, err)
		} else if resp.Error == "" {
			t.Errorf("GET %s: expected non-empty error message", path)
		}
	}

	// A wrong method on a known route also gets the JSON 404, not index.html.
	recPost := doRequest(t, handler, "POST", "/api/graph/default", "")
	if recPost.Code != http.StatusNotFound {
		t.Errorf("POST /api/graph/default: expected 404, got %d: %s", recPost.Code, recPost.Body.String())
	}
	if body := recPost.Body.String(); strings.Contains(body, "marmot spa") {
		t.Errorf("POST /api/graph/default: got SPA fallback instead of JSON error: %s", body)
	}

	// Known routes and the SPA fallback keep working alongside the API check.
	recOK := doRequest(t, handler, "GET", "/api/version", "")
	if recOK.Code != http.StatusOK {
		t.Errorf("expected /api/version 200, got %d", recOK.Code)
	}
	recGraph := doRequest(t, handler, "GET", "/api/graph/default", "")
	if recGraph.Code != http.StatusOK {
		t.Errorf("expected /api/graph/default 200, got %d", recGraph.Code)
	}
	recSPA := doRequest(t, handler, "GET", "/some/client/route", "")
	if recSPA.Code != http.StatusOK || !strings.Contains(recSPA.Body.String(), "marmot spa") {
		t.Errorf("expected SPA fallback for non-API route, got %d: %s", recSPA.Code, recSPA.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleLLMChat: history with code blocks (stripCodeBlocks) + phase-1 error
// ---------------------------------------------------------------------------

func TestHandleLLMChat_StripsHistoryCode(t *testing.T) {
	server, _ := newTestServer(t)
	mock := &llm.MockProvider{ChatResult: "Direct answer, no code needed."}
	server.WithLLMChat(mock)
	handler := server.Handler()

	// History includes a prior assistant turn with a code block + a system msg
	// (which is skipped). stripCodeBlocks should run on the assistant content.
	body := `{
		"message": "follow-up question",
		"session_id": "hist-1",
		"history": [
			{"role": "system", "content": "ignored"},
			{"role": "user", "content": "earlier question"},
			{"role": "assistant", "content": "Earlier answer.\n` + "```js\\nreturn 1;\\n```" + `\nTrailer."}
		]
	}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp curator.ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Message.Content != "Direct answer, no code needed." {
		t.Errorf("unexpected content: %q", resp.Message.Content)
	}
}

func TestHandleLLMChat_Phase1Error(t *testing.T) {
	server, _ := newTestServer(t)
	mock := &llm.MockProvider{ChatErr: context.DeadlineExceeded}
	server.WithLLMChat(mock)
	handler := server.Handler()

	body := `{"message":"anything","session_id":"err-1"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on phase-1 LLM error, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleGraph with include_superseded + check_stale query flags
// ---------------------------------------------------------------------------

func TestHandleGraph_CheckStaleFlag(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()

	projectRoot := filepath.Dir(engine.MarmotDir)
	srcRel := "src/g.go"
	srcAbs := filepath.Join(projectRoot, srcRel)
	if err := os.MkdirAll(filepath.Dir(srcAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcAbs, []byte("package g\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := &node.Node{
		ID: "graph/stale", Type: "function", Namespace: "default", Status: node.StatusActive,
		Summary: "stale in graph", Source: node.Source{Path: srcRel, Hash: "nope"},
	}
	if err := engine.NodeStore.SaveNode(stale); err != nil {
		t.Fatal(err)
	}
	if err := engine.GetGraph().UpsertNode(stale); err != nil {
		t.Fatal(err)
	}

	rec := doRequest(t, handler, "GET", "/api/graph/default?check_stale=true", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp GraphResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, n := range resp.Nodes {
		if n.ID == "graph/stale" {
			found = true
			if !n.IsStale {
				t.Error("expected graph/stale to be flagged stale")
			}
		}
	}
	if !found {
		t.Error("graph/stale node not present in response")
	}
}
