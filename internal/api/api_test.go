package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/node"
)

// testEdge is a convenience struct for writing test node files.
type testEdge struct {
	target   string
	relation string
}

// writeTestNode creates a properly formatted YAML-frontmatter markdown file
// in the marmot directory, including subdirectories as needed.
func writeTestNode(t *testing.T, marmotDir, id, nodeType, summary string, edges []testEdge) {
	t.Helper()

	// Build YAML frontmatter.
	var buf strings.Builder
	buf.WriteString("---\n")
	buf.WriteString(fmt.Sprintf("id: %s\n", id))
	buf.WriteString(fmt.Sprintf("type: %s\n", nodeType))
	buf.WriteString("namespace: default\n")
	buf.WriteString("status: active\n")
	if len(edges) > 0 {
		buf.WriteString("edges:\n")
		for _, e := range edges {
			buf.WriteString(fmt.Sprintf("    - target: %s\n", e.target))
			buf.WriteString(fmt.Sprintf("      relation: %s\n", e.relation))
		}
	}
	buf.WriteString("---\n\n")
	buf.WriteString(summary)
	buf.WriteString("\n")

	// Derive file path: id maps to subdirectory structure.
	filePath := filepath.Join(marmotDir, id+".md")
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir for node %s: %v", id, err)
	}
	if err := os.WriteFile(filePath, []byte(buf.String()), 0o644); err != nil {
		t.Fatalf("write node %s: %v", id, err)
	}
}

// setupTestEngine builds a real *mcp.Engine from a temp directory with test nodes.
func setupTestEngine(t *testing.T) *mcpserver.Engine {
	t.Helper()
	dir := t.TempDir()
	marmotDir := filepath.Join(dir, ".marmot")
	if err := os.MkdirAll(filepath.Join(marmotDir, ".marmot-data"), 0o755); err != nil {
		t.Fatalf("mkdir .marmot-data: %v", err)
	}

	// Write _config.md.
	configContent := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Create test node files.
	writeTestNode(t, marmotDir, "auth/login", "function", "JWT authentication login handler", []testEdge{
		{"db/users", "reads"},
		{"auth/token", "calls"},
	})
	writeTestNode(t, marmotDir, "auth/token", "function", "Token generation and validation", []testEdge{
		{"auth/login", "references"},
	})
	writeTestNode(t, marmotDir, "db/users", "module", "User database access layer", nil)
	writeTestNode(t, marmotDir, "api/routes", "module", "HTTP route definitions", []testEdge{
		{"auth/login", "contains"},
	})

	// Create engine with mock embedder.
	embedder := embedding.NewMockEmbedder("mock-test")
	engine, err := mcpserver.NewEngine(marmotDir, embedder)
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })

	// Seed embedding store so that search works.
	seedEmbeddings(t, engine)

	return engine
}

// seedEmbeddings inserts embeddings for all active nodes in the engine so
// that search tests have data to query against.
func seedEmbeddings(t *testing.T, engine *mcpserver.Engine) {
	t.Helper()
	for _, n := range engine.Graph.AllActiveNodes() {
		text := n.Summary
		if text == "" {
			text = n.ID
		}
		vec, err := engine.Embedder.Embed(text)
		if err != nil {
			t.Fatalf("embed node %s: %v", n.ID, err)
		}
		h := sha256.Sum256([]byte(text))
		hash := hex.EncodeToString(h[:])
		if err := engine.EmbeddingStore.Upsert(n.ID, vec, hash, engine.Embedder.Model()); err != nil {
			t.Fatalf("upsert embedding for %s: %v", n.ID, err)
		}
	}
}

// newTestServer creates a Server from a test engine with no embedded assets.
func newTestServer(t *testing.T) (*Server, *mcpserver.Engine) {
	t.Helper()
	engine := setupTestEngine(t)
	server := NewServer(engine, nil)
	return server, engine
}

// doRequest is a helper that performs an HTTP request against the server handler
// and returns the recorder for inspection.
func doRequest(t *testing.T, handler http.Handler, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

func TestHandleGraph(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/graph/default", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp GraphResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify structure fields exist.
	if resp.Namespace != "default" {
		t.Errorf("expected namespace 'default', got %q", resp.Namespace)
	}
	if resp.NodeCount != len(resp.Nodes) {
		t.Errorf("node_count %d != len(nodes) %d", resp.NodeCount, len(resp.Nodes))
	}
	if resp.EdgeCount != len(resp.Edges) {
		t.Errorf("edge_count %d != len(edges) %d", resp.EdgeCount, len(resp.Edges))
	}

	// We created 4 active nodes.
	if resp.NodeCount != 4 {
		t.Errorf("expected 4 nodes, got %d", resp.NodeCount)
	}

	// Verify each node has required fields.
	for _, n := range resp.Nodes {
		if n.ID == "" {
			t.Error("node has empty ID")
		}
		if n.Type == "" {
			t.Errorf("node %s has empty type", n.ID)
		}
		if n.Summary == "" {
			t.Errorf("node %s has empty summary", n.ID)
		}
		// Edges slice should be non-nil (initialized to empty).
		if n.Edges == nil {
			t.Errorf("node %s has nil edges", n.ID)
		}
	}

	// Verify edge_count includes both in and out edges.
	// auth/login has 2 outbound (db/users, auth/token) and 2 inbound (auth/token->references, api/routes->contains) = 4.
	found := false
	for _, n := range resp.Nodes {
		if n.ID == "auth/login" {
			found = true
			if n.EdgeCount < 2 {
				t.Errorf("auth/login edge_count should be >= 2 (in+out), got %d", n.EdgeCount)
			}
			break
		}
	}
	if !found {
		t.Error("auth/login node not found in graph response")
	}

	// Verify outbound edges are present in the flat edges list.
	if resp.EdgeCount < 4 {
		t.Errorf("expected at least 4 outbound edges total, got %d", resp.EdgeCount)
	}
}

func TestHandleGraphIncludeSuperseded(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()

	// Add a superseded node directly to the graph.
	supersededNode := &node.Node{
		ID:           "auth/legacy",
		Type:         "function",
		Namespace:    "default",
		Status:       node.StatusSuperseded,
		SupersededBy: "auth/login",
		Summary:      "Legacy authentication handler (deprecated)",
	}
	if err := engine.Graph.AddNode(supersededNode); err != nil {
		t.Fatalf("add superseded node: %v", err)
	}

	// Without include_superseded: should NOT include the superseded node.
	rec := doRequest(t, handler, "GET", "/api/graph/default", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp GraphResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, n := range resp.Nodes {
		if n.ID == "auth/legacy" {
			t.Error("superseded node should NOT appear without include_superseded=true")
		}
	}
	if resp.NodeCount != 4 {
		t.Errorf("expected 4 active nodes, got %d", resp.NodeCount)
	}

	// With include_superseded=true: should include it.
	rec2 := doRequest(t, handler, "GET", "/api/graph/default?include_superseded=true", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}
	var resp2 GraphResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	foundSuperseded := false
	for _, n := range resp2.Nodes {
		if n.ID == "auth/legacy" {
			foundSuperseded = true
			if n.Status != node.StatusSuperseded {
				t.Errorf("expected status 'superseded', got %q", n.Status)
			}
			break
		}
	}
	if !foundSuperseded {
		t.Error("superseded node should appear with include_superseded=true")
	}
	if resp2.NodeCount != 5 {
		t.Errorf("expected 5 nodes (4 active + 1 superseded), got %d", resp2.NodeCount)
	}
}

func TestHandleNode(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// Successful fetch.
	rec := doRequest(t, handler, "GET", "/api/node/default/auth/login", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var apiNode APINode
	if err := json.NewDecoder(rec.Body).Decode(&apiNode); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if apiNode.ID != "auth/login" {
		t.Errorf("expected ID 'auth/login', got %q", apiNode.ID)
	}
	if apiNode.Type != "function" {
		t.Errorf("expected type 'function', got %q", apiNode.Type)
	}
	if apiNode.Summary != "JWT authentication login handler" {
		t.Errorf("expected summary 'JWT authentication login handler', got %q", apiNode.Summary)
	}
	if apiNode.Status != "active" {
		t.Errorf("expected status 'active', got %q", apiNode.Status)
	}
	// Should have 2 outbound edges defined on the node.
	if len(apiNode.Edges) != 2 {
		t.Errorf("expected 2 edges on node, got %d", len(apiNode.Edges))
	}
	// EdgeCount should include both in and out edges.
	if apiNode.EdgeCount < 2 {
		t.Errorf("expected edge_count >= 2, got %d", apiNode.EdgeCount)
	}

	// Not found.
	rec404 := doRequest(t, handler, "GET", "/api/node/default/nonexistent", "")
	if rec404.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent node, got %d", rec404.Code)
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(rec404.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error == "" {
		t.Error("expected non-empty error message for 404")
	}
}

func TestHandleNodeSlashInID(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// The {id...} wildcard pattern should capture "auth/login" (with the slash).
	rec := doRequest(t, handler, "GET", "/api/node/default/auth/login", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for slashed ID, got %d: %s", rec.Code, rec.Body.String())
	}

	var apiNode APINode
	if err := json.NewDecoder(rec.Body).Decode(&apiNode); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if apiNode.ID != "auth/login" {
		t.Errorf("expected ID 'auth/login', got %q", apiNode.ID)
	}

	// Also test a deeper path: api/routes.
	rec2 := doRequest(t, handler, "GET", "/api/node/default/api/routes", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for api/routes, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var apiNode2 APINode
	if err := json.NewDecoder(rec2.Body).Decode(&apiNode2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if apiNode2.ID != "api/routes" {
		t.Errorf("expected ID 'api/routes', got %q", apiNode2.ID)
	}
}

func TestHandleSearch(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// Successful search.
	rec := doRequest(t, handler, "GET", "/api/search?q=authentication", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp SearchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Error("expected at least one search result for 'authentication'")
	}
	// Verify result structure.
	for _, r := range resp.Results {
		if r.NodeID == "" {
			t.Error("search result has empty node_id")
		}
		if r.Score <= 0 {
			t.Errorf("search result %s has non-positive score %f", r.NodeID, r.Score)
		}
		if r.Summary == "" {
			t.Errorf("search result %s has empty summary", r.NodeID)
		}
	}

	// Missing q parameter returns 400.
	rec400 := doRequest(t, handler, "GET", "/api/search", "")
	if rec400.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing q param, got %d", rec400.Code)
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(rec400.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !strings.Contains(errResp.Error, "q parameter") {
		t.Errorf("expected error about q parameter, got %q", errResp.Error)
	}
}

func TestHandleNamespaces(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/namespaces", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp NamespacesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Namespaces) == 0 {
		t.Fatal("expected at least one namespace")
	}

	// Find the default namespace.
	found := false
	for _, ns := range resp.Namespaces {
		if ns.Name == "default" {
			found = true
			if ns.NodeCount != 4 {
				t.Errorf("expected 4 nodes in default namespace, got %d", ns.NodeCount)
			}
			break
		}
	}
	if !found {
		t.Error("default namespace not found in response")
	}
}

func TestHandleBridges(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/bridges", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp BridgesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// No namespace manager means no bridges.
	if len(resp.Bridges) != 0 {
		t.Errorf("expected 0 bridges, got %d", len(resp.Bridges))
	}
	// Verify the Bridges field is a non-nil empty slice (JSON: [] not null).
	if resp.Bridges == nil {
		t.Error("bridges should be empty array, not null")
	}
}

func TestHandleHeat(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/heat/default", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// The response is an anonymous struct with pairs field.
	var resp struct {
		Pairs []APIHeatPair `json:"pairs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// No heat map configured means empty pairs.
	if len(resp.Pairs) != 0 {
		t.Errorf("expected 0 heat pairs, got %d", len(resp.Pairs))
	}
}

func TestHandleNodeUpdate(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()

	// PUT /api/node/{id...} updates summary.
	body := `{"summary": "Updated JWT login handler with MFA support"}`
	rec := doRequest(t, handler, "PUT", "/api/node/auth/login", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updateResp NodeUpdateResponse
	if err := json.NewDecoder(rec.Body).Decode(&updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateResp.NodeID != "auth/login" {
		t.Errorf("expected node_id 'auth/login', got %q", updateResp.NodeID)
	}
	if updateResp.Status != "updated" {
		t.Errorf("expected status 'updated', got %q", updateResp.Status)
	}
	if updateResp.Hash == "" {
		t.Error("expected non-empty hash in update response")
	}

	// Re-fetch the node to verify the summary was updated.
	n, ok := engine.Graph.GetNode("auth/login")
	if !ok {
		t.Fatal("auth/login not found in graph after update")
	}
	if n.Summary != "Updated JWT login handler with MFA support" {
		t.Errorf("expected updated summary, got %q", n.Summary)
	}

	// Also verify via HTTP GET.
	recGet := doRequest(t, handler, "GET", "/api/node/default/auth/login", "")
	if recGet.Code != http.StatusOK {
		t.Fatalf("expected 200 on re-fetch, got %d", recGet.Code)
	}
	var apiNode APINode
	if err := json.NewDecoder(recGet.Body).Decode(&apiNode); err != nil {
		t.Fatalf("decode re-fetch: %v", err)
	}
	if apiNode.Summary != "Updated JWT login handler with MFA support" {
		t.Errorf("HTTP re-fetch: expected updated summary, got %q", apiNode.Summary)
	}

	// PUT with empty body should return 400.
	recBad := doRequest(t, handler, "PUT", "/api/node/auth/login", `{}`)
	if recBad.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty update, got %d", recBad.Code)
	}

	// PUT for nonexistent node should return 404.
	rec404 := doRequest(t, handler, "PUT", "/api/node/nonexistent/node", `{"summary": "test"}`)
	if rec404.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent node update, got %d", rec404.Code)
	}
}

func TestCORS(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// OPTIONS request should return CORS headers and 204.
	rec := doRequest(t, handler, "OPTIONS", "/api/graph/default", "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", rec.Code)
	}
	origin := rec.Header().Get("Access-Control-Allow-Origin")
	if origin != "*" {
		t.Errorf("expected Access-Control-Allow-Origin '*', got %q", origin)
	}
	methods := rec.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(methods, "GET") || !strings.Contains(methods, "PUT") {
		t.Errorf("expected Allow-Methods to include GET and PUT, got %q", methods)
	}
	headers := rec.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(headers, "Content-Type") {
		t.Errorf("expected Allow-Headers to include Content-Type, got %q", headers)
	}

	// Regular GET request should also include CORS origin header.
	recGet := doRequest(t, handler, "GET", "/api/namespaces", "")
	originGet := recGet.Header().Get("Access-Control-Allow-Origin")
	if originGet != "*" {
		t.Errorf("GET: expected Access-Control-Allow-Origin '*', got %q", originGet)
	}
}

func TestNotFoundRoutes(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// With no embedded assets, unmatched routes should 404.
	rec := doRequest(t, handler, "GET", "/api/nonexistent", "")
	// The server has no assets, so the mux will return 404.
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for /api/nonexistent, got %d", rec.Code)
	}

	// POST to a GET-only route should return 405.
	recPost := doRequest(t, handler, "POST", "/api/graph/default", "")
	if recPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST /api/graph/default, got %d", recPost.Code)
	}
}

func TestHandleSearchWithNamespaceFilter(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// Search with namespace filter.
	rec := doRequest(t, handler, "GET", "/api/search?q=authentication&ns=default", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp SearchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Should still return results since all test nodes are in "default" namespace.
	if len(resp.Results) == 0 {
		t.Error("expected results with ns=default filter")
	}

	// Search with non-matching namespace filter should return empty results.
	recEmpty := doRequest(t, handler, "GET", "/api/search?q=authentication&ns=nonexistent", "")
	if recEmpty.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recEmpty.Code)
	}
	var respEmpty SearchResponse
	if err := json.NewDecoder(recEmpty.Body).Decode(&respEmpty); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(respEmpty.Results) != 0 {
		t.Errorf("expected 0 results with ns=nonexistent, got %d", len(respEmpty.Results))
	}
}

func TestHandleSearchWithLimit(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/search?q=handler&limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp SearchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) > 2 {
		t.Errorf("expected at most 2 results with limit=2, got %d", len(resp.Results))
	}
}

func TestHandleGraphMissingNamespace(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// GET /api/graph/ with empty namespace pattern doesn't match the route.
	rec := doRequest(t, handler, "GET", "/api/graph/", "")
	// Go 1.22 mux: "/api/graph/" won't match "GET /api/graph/{namespace}" so it's a 404 or redirect.
	if rec.Code == http.StatusOK {
		t.Error("expected non-200 for empty namespace")
	}
}

func TestHandleNodeUpdateContext(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()

	// Update context only.
	body := `{"context": "Used in the main authentication flow for all API endpoints."}`
	rec := doRequest(t, handler, "PUT", "/api/node/auth/token", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify context was updated in the graph.
	n, ok := engine.Graph.GetNode("auth/token")
	if !ok {
		t.Fatal("auth/token not found after update")
	}
	if n.Context != "Used in the main authentication flow for all API endpoints." {
		t.Errorf("expected updated context, got %q", n.Context)
	}
}

func TestHandleNodeUpdateInvalidJSON(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "PUT", "/api/node/auth/login", `{invalid json}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestHandleSummaryNotFound(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// No summary exists for any namespace in the test setup.
	rec := doRequest(t, handler, "GET", "/api/summary/default", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing summary, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGraphEdgeStructure(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/graph/default", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp GraphResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Verify edge structure fields.
	for _, e := range resp.Edges {
		if e.Source == "" {
			t.Error("edge has empty source")
		}
		if e.Target == "" {
			t.Error("edge has empty target")
		}
		if e.Relation == "" {
			t.Error("edge has empty relation")
		}
		if e.Class == "" {
			t.Error("edge has empty class")
		}
		// Class should be "structural" or "behavioral".
		if e.Class != "structural" && e.Class != "behavioral" {
			t.Errorf("edge %s->%s has unexpected class %q", e.Source, e.Target, e.Class)
		}
	}

	// Verify specific edges exist.
	edgeExists := func(src, tgt, rel string) bool {
		for _, e := range resp.Edges {
			if e.Source == src && e.Target == tgt && e.Relation == rel {
				return true
			}
		}
		return false
	}

	if !edgeExists("auth/login", "db/users", "reads") {
		t.Error("expected edge auth/login -> db/users (reads)")
	}
	if !edgeExists("auth/login", "auth/token", "calls") {
		t.Error("expected edge auth/login -> auth/token (calls)")
	}
	if !edgeExists("auth/token", "auth/login", "references") {
		t.Error("expected edge auth/token -> auth/login (references)")
	}
	if !edgeExists("api/routes", "auth/login", "contains") {
		t.Error("expected edge api/routes -> auth/login (contains)")
	}
}

func TestGraphNonExistentNamespace(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "GET", "/api/graph/nonexistent", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (empty graph), got %d", rec.Code)
	}

	var resp GraphResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.NodeCount != 0 {
		t.Errorf("expected 0 nodes for nonexistent namespace, got %d", resp.NodeCount)
	}
	if resp.EdgeCount != 0 {
		t.Errorf("expected 0 edges for nonexistent namespace, got %d", resp.EdgeCount)
	}
}

func TestResponseContentType(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	endpoints := []string{
		"/api/graph/default",
		"/api/namespaces",
		"/api/bridges",
		"/api/heat/default",
		"/api/search?q=test",
	}

	for _, ep := range endpoints {
		rec := doRequest(t, handler, "GET", ep, "")
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: expected Content-Type application/json, got %q", ep, ct)
		}
	}
}
