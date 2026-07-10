package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/graph"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/routes"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

const selfAliasLiveSummary = "LIVE service API quartz revision"

// setupSelfAliasWarren is the self-vault variant of setupAPIWarren: the
// workspace's live vault carries the same vault_id ("local-vault") as the
// warren's copy of it, and the live copy of the shared node diverges from
// the warren snapshot (summary "Service API") so tests can tell which one a
// surface served. Under R2 identity the fixture is identity-only by
// construction: setupAPIWarren's Mount call is a recorded-nothing no-op for
// the self project (asserted below), so every test in this file exercises
// the derived-identity path with zero mounts. Returns the warren root.
func setupSelfAliasWarren(t *testing.T, engine *mcpserver.Engine) string {
	t.Helper()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	cfg := "---\nversion: \"1\"\nvault_id: local-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(engine.MarmotDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	warrenRoot := setupAPIWarren(t, workspaceRoot, "wp", "self-proj", "local-vault")
	state, _, err := warrenpkg.LoadWorkspaceState(workspaceRoot)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	if got := state.Warrens["wp"].ActiveProjects; len(got) != 0 {
		t.Fatalf("self mount recorded state %v; the fixture must be identity-only (mount of self is a no-op)", got)
	}

	// Diverge the LIVE vault's node from the warren snapshot and make the
	// engine (graph + embeddings) see it.
	writeTestNode(t, engine.MarmotDir, "service/api", "module", selfAliasLiveSummary, nil)
	g, err := graph.LoadGraph(engine.NodeStore)
	if err != nil {
		t.Fatalf("reload graph: %v", err)
	}
	engine.SetGraph(g)
	seedEmbeddings(t, engine)

	// Mirror buildEngine's warren wiring with the local vault ID (so the
	// engine caches LocalVaultID and the registry knows its own identity).
	t.Setenv("MARMOT_ROUTES", "off")
	engine.WithVaultRegistry(namespace.NewVaultRegistry("local-vault", engine.MarmotDir, nil, routes.EmptyTable()))
	if err := engine.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState: %v", err)
	}
	return warrenRoot
}

// TestWarrenNodeUpdateRefusesSelfAlias (R1.5a): a PUT to the workspace's own
// vault ID is refused with the own-vault message and touches neither the
// live node nor the warren copy.
func TestWarrenNodeUpdateRefusesSelfAlias(t *testing.T) {
	server, engine := newTestServer(t)
	warrenRoot := setupSelfAliasWarren(t, engine)

	rec := doRequest(t, server.Handler(), "PUT", "/api/node/@local-vault/service/api", `{"summary":"should never land"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self-alias @-update = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "own vault") || !strings.Contains(rec.Body.String(), "/api/node/service/api") {
		t.Fatalf("refusal = %q, want own-vault message pointing at the unqualified PUT", rec.Body.String())
	}

	liveNode, err := os.ReadFile(filepath.Join(engine.MarmotDir, "service", "api.md"))
	if err != nil {
		t.Fatalf("read live node: %v", err)
	}
	if strings.Contains(string(liveNode), "should never land") {
		t.Fatal("refused write landed in the live vault")
	}
	copyNode, err := os.ReadFile(filepath.Join(warrenRoot, "projects", "self-proj", ".marmot", "service", "api.md"))
	if err != nil {
		t.Fatalf("read warren copy node: %v", err)
	}
	if strings.Contains(string(copyNode), "should never land") {
		t.Fatal("refused write landed in the warren copy")
	}
}

// TestResolveSearchNodeSelfAlias (R1.5b): the workspace's own vault ID
// resolves against the live graph with local_alias provenance — read-only,
// zero staleness, never through the registry.
func TestResolveSearchNodeSelfAlias(t *testing.T) {
	server, engine := newTestServer(t)
	setupSelfAliasWarren(t, engine)

	n, prov, ok := server.resolveSearchNode("@local-vault/service/api")
	if !ok {
		t.Fatal("@local-vault/service/api did not resolve")
	}
	if n.Summary != selfAliasLiveSummary {
		t.Fatalf("resolved summary = %q, want the LIVE node", n.Summary)
	}
	if prov == nil || prov.Source != "local_alias" {
		t.Fatalf("provenance = %+v, want source local_alias", prov)
	}
	if prov.Editable {
		t.Fatal("self-alias provenance must be read-only (edit via the unqualified local node)")
	}
	if prov.MarmotDir != engine.MarmotDir {
		t.Fatalf("provenance marmot_dir = %q, want the workspace %q", prov.MarmotDir, engine.MarmotDir)
	}
	if prov.QualifiedID != "@local-vault/service/api" {
		t.Fatalf("provenance qualified_id = %q", prov.QualifiedID)
	}
}

// TestWarrenGraphSelfAliasServesLiveNodes (R1.5c): the warren graph view
// renders a self-alias project's nodes from the LIVE vault (not the
// snapshot), @-qualified, with local_alias read-only provenance.
func TestWarrenGraphSelfAliasServesLiveNodes(t *testing.T) {
	server, engine := newTestServer(t)
	setupSelfAliasWarren(t, engine)

	rec := doRequest(t, server.Handler(), "GET", "/api/warren/wp/graph", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("warren graph = %d: %s", rec.Code, rec.Body.String())
	}
	var resp GraphResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode graph response: %v", err)
	}
	var shared *APINode
	for i := range resp.Nodes {
		if resp.Nodes[i].ID == "@local-vault/service/api" {
			shared = &resp.Nodes[i]
			break
		}
	}
	if shared == nil {
		t.Fatalf("@local-vault/service/api missing from warren graph: %d nodes", len(resp.Nodes))
	}
	if shared.Summary != selfAliasLiveSummary {
		t.Fatalf("warren graph served %q, want the LIVE node (snapshot shadowing)", shared.Summary)
	}
	if shared.Provenance == nil || shared.Provenance.Source != "local_alias" {
		t.Fatalf("provenance = %+v, want local_alias", shared.Provenance)
	}
	if shared.Provenance.Editable {
		t.Fatal("self-alias graph provenance must be read-only")
	}
	if shared.Provenance.MarmotDir != engine.MarmotDir {
		t.Fatalf("provenance marmot_dir = %q, want the workspace %q", shared.Provenance.MarmotDir, engine.MarmotDir)
	}
}

// TestWarrenGraphDedupesIdentifiedProjects: a warren can carry two projects
// that both hold the workspace's vault_id — coherent, not a conflict, per the
// alias contract (R1.2) — and ActiveMounts synthesizes an identity entry for
// each. The graph endpoint must render the live vault once: node IDs are
// keyed by vault (@<vault_id>/<node>), so a second pass would duplicate every
// node and edge in one GraphResponse.
func TestWarrenGraphDedupesIdentifiedProjects(t *testing.T) {
	server, engine := newTestServer(t)
	warrenRoot := setupSelfAliasWarren(t, engine)

	// A second identified project in the same warren: its checkout carries
	// the workspace's vault_id too.
	marmotDir := filepath.Join(warrenRoot, "projects", "self-proj-2", ".marmot")
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatalf("mkdir second checkout: %v", err)
	}
	if err := warrenpkg.SaveProjectMetadata(marmotDir, &warrenpkg.ProjectMetadata{
		ProjectID: "self-proj-2",
		WarrenID:  "wp",
		VaultID:   "local-vault",
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	if _, err := warrenpkg.AddProject(warrenRoot, warrenpkg.Project{
		ProjectID: "self-proj-2",
		Path:      "projects/self-proj-2/.marmot",
	}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	rec := doRequest(t, server.Handler(), "GET", "/api/warren/wp/graph", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("warren graph = %d: %s", rec.Code, rec.Body.String())
	}
	var resp GraphResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode graph response: %v", err)
	}
	if len(resp.Nodes) == 0 {
		t.Fatal("warren graph is empty; expected the live vault rendered once")
	}
	seen := make(map[string]bool, len(resp.Nodes))
	for _, n := range resp.Nodes {
		if seen[n.ID] {
			t.Errorf("duplicate node ID %q in warren graph (live vault rendered per identity mount, not per vault)", n.ID)
		}
		seen[n.ID] = true
	}
	edges := make(map[string]bool, len(resp.Edges))
	for _, e := range resp.Edges {
		key := e.Source + "->" + e.Target + ":" + e.Relation
		if edges[key] {
			t.Errorf("duplicate edge %s in warren graph", key)
		}
		edges[key] = true
	}
	if len(resp.Skipped) != 0 {
		t.Errorf("identity dedupe must not report skips, got %v", resp.Skipped)
	}
}

// TestWarrenScopedSearchIncludesSelfAlias (R1.5d): _warren/<id>-scoped search
// returns the self-alias project's LIVE results @-qualified — before the
// alias-aware branch the local-ID skip made the project invisible in its own
// warren's search.
func TestWarrenScopedSearchIncludesSelfAlias(t *testing.T) {
	server, engine := newTestServer(t)
	setupSelfAliasWarren(t, engine)

	rec := doRequest(t, server.Handler(), "GET", "/api/search?q="+url.QueryEscape(selfAliasLiveSummary)+"&ns="+url.QueryEscape("_warren/wp"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("warren-scoped search = %d: %s", rec.Code, rec.Body.String())
	}
	var resp SearchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	var hit *SearchResult
	for i := range resp.Results {
		if resp.Results[i].NodeID == "@local-vault/service/api" {
			hit = &resp.Results[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("warren-scoped search missing @local-vault/service/api: %+v", resp.Results)
	}
	if hit.Summary != selfAliasLiveSummary {
		t.Fatalf("scoped search served %q, want the LIVE summary", hit.Summary)
	}
	if hit.Provenance == nil || hit.Provenance.Source != "local_alias" || hit.Provenance.Editable {
		t.Fatalf("provenance = %+v, want read-only local_alias", hit.Provenance)
	}
}

// TestWarrensResponseIdentifiedProjects (R2.6): GET /api/warrens grafts the
// computed identified_projects onto each warren entry — identity is derived,
// never stored, so the raw state alone could not show it.
func TestWarrensResponseIdentifiedProjects(t *testing.T) {
	server, engine := newTestServer(t)
	setupSelfAliasWarren(t, engine)

	rec := doRequest(t, server.Handler(), "GET", "/api/warrens", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/warrens = %d: %s", rec.Code, rec.Body.String())
	}
	var resp WarrensResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode warrens response: %v", err)
	}
	entry, ok := resp.Warrens["wp"]
	if !ok {
		t.Fatalf("warren wp missing from response: %+v", resp.Warrens)
	}
	if len(entry.IdentifiedProjects) != 1 || entry.IdentifiedProjects[0] != "self-proj" {
		t.Fatalf("identified_projects = %v, want [self-proj]", entry.IdentifiedProjects)
	}
	if len(entry.ActiveProjects) != 0 {
		t.Fatalf("active_projects = %v, want none (identity is not a mount)", entry.ActiveProjects)
	}
}

// TestWarrenWriteSelfAliasRefusalEquivalence: the third refusal case of the
// write-equivalence matrix — both the HTTP API and MCP @-write paths refuse a
// self-alias vault ID with the write-locally remediation, even when legacy
// state marks the self project editable, and no write lands anywhere.
func TestWarrenWriteSelfAliasRefusalEquivalence(t *testing.T) {
	server, engine := newTestServer(t)
	warrenRoot := setupSelfAliasWarren(t, engine)
	workspaceRoot := filepath.Dir(engine.MarmotDir)

	// Legacy editable self-mount state (hand-written past SetEditable's
	// refusal, as an old binary could have).
	state, body, err := warrenpkg.LoadWorkspaceState(workspaceRoot)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["wp"]
	entry.EditableProjects = []string{"self-proj"}
	state.Warrens["wp"] = entry
	if err := warrenpkg.SaveWorkspaceState(workspaceRoot, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	rec := doRequest(t, server.Handler(), "PUT", "/api/node/@local-vault/service/api", `{"summary":"split brain attempt"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "own vault") {
		t.Fatalf("API self-alias write = %d %q, want 403 own-vault refusal", rec.Code, rec.Body.String())
	}

	res, err := engine.HandleContextWrite(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_write",
			Arguments: map[string]any{
				"id":      "@local-vault/service/api",
				"type":    "module",
				"summary": "split brain attempt",
			},
		},
	})
	if err != nil {
		t.Fatalf("MCP self-alias write: %v", err)
	}
	if !res.IsError {
		t.Fatalf("MCP self-alias write must be refused: %+v", res.Content)
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "write the node locally") {
		t.Fatalf("MCP refusal = %+v, want write-locally message", res.Content[0])
	}

	// No write landed in the live vault or the warren copy.
	for _, path := range []string{
		filepath.Join(engine.MarmotDir, "service", "api.md"),
		filepath.Join(warrenRoot, "projects", "self-proj", ".marmot", "service", "api.md"),
	} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if strings.Contains(string(data), "split brain attempt") {
			t.Fatalf("refused self-alias write landed in %s:\n%s", path, data)
		}
	}
}
