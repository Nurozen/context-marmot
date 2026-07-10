package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// --- U5a: POST /api/warren/{id}/mount, POST /api/warren/{id}/unmount,
// GET /api/doctor/workspace, and the skipped_reasons graph field (U4.4).

// warrenStatusProject fetches /api/warren/{id}/status and returns the status
// row for projectID (fails the test when the row is missing).
func warrenStatusProject(t *testing.T, handler http.Handler, warrenID, projectID string) warrenpkg.ProjectStatus {
	t.Helper()
	rec := doRequest(t, handler, "GET", "/api/warren/"+warrenID+"/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("warren status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp WarrenStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	for _, project := range resp.Projects {
		if project.ProjectID == projectID {
			return project
		}
	}
	t.Fatalf("project %q missing from warren %q status: %+v", projectID, warrenID, resp.Projects)
	return warrenpkg.ProjectStatus{}
}

// TestWarrenMountEndpoint drives a full mount → unmount round trip over HTTP
// and asserts both the workspace state (via the status endpoint) and the
// engine reload (via the vault registry) observe each step.
func TestWarrenMountEndpoint(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	registerAPIWarren(t, workspaceRoot, "wp", "project-a", "project-a-vault")
	wireWarrenVaultRegistry(t, engine)

	if got := warrenStatusProject(t, handler, "wp", "project-a"); got.Active {
		t.Fatalf("fixture must start unmounted, got %+v", got)
	}

	rec := doRequest(t, handler, "POST", "/api/warren/wp/mount", `{"projects":["project-a"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mount = %d: %s", rec.Code, rec.Body.String())
	}
	var mountResp WarrenMountResponse
	if err := json.NewDecoder(rec.Body).Decode(&mountResp); err != nil {
		t.Fatalf("decode mount response: %v", err)
	}
	if mountResp.Action != "mounted" || mountResp.Status != "reloaded" ||
		len(mountResp.Projects) != 1 || mountResp.Projects[0] != "project-a" {
		t.Fatalf("mount response = %+v", mountResp)
	}
	status := warrenStatusProject(t, handler, "wp", "project-a")
	if !status.Active || !status.Available {
		t.Fatalf("post-mount status = %+v, want active+available", status)
	}
	if ids := engine.VaultRegistry.KnownVaultIDs(); len(ids) != 1 || ids[0] != "project-a-vault" {
		t.Fatalf("post-mount KnownVaultIDs = %v, want [project-a-vault] (reload did not run)", ids)
	}

	rec = doRequest(t, handler, "POST", "/api/warren/wp/unmount", `{"projects":["project-a"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unmount = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&mountResp); err != nil {
		t.Fatalf("decode unmount response: %v", err)
	}
	if mountResp.Action != "unmounted" || mountResp.Status != "reloaded" {
		t.Fatalf("unmount response = %+v", mountResp)
	}
	if status := warrenStatusProject(t, handler, "wp", "project-a"); status.Active {
		t.Fatalf("post-unmount status = %+v, want dormant", status)
	}
	if ids := engine.VaultRegistry.KnownVaultIDs(); len(ids) != 0 {
		t.Fatalf("post-unmount KnownVaultIDs = %v, want empty", ids)
	}
}

// TestWarrenMountEndpointAll expands {"all": true} server-side: mount covers
// every manifest project, unmount covers every active project (and an
// unmount --all with nothing mounted is a no-op, not an error).
func TestWarrenMountEndpointAll(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	registerAPIWarren(t, workspaceRoot, "wp", "project-a", "project-a-vault")
	wireWarrenVaultRegistry(t, engine)

	rec := doRequest(t, handler, "POST", "/api/warren/wp/mount", `{"all":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mount all = %d: %s", rec.Code, rec.Body.String())
	}
	if status := warrenStatusProject(t, handler, "wp", "project-a"); !status.Active {
		t.Fatalf("mount all left project dormant: %+v", status)
	}

	rec = doRequest(t, handler, "POST", "/api/warren/wp/unmount", `{"all":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unmount all = %d: %s", rec.Code, rec.Body.String())
	}
	if status := warrenStatusProject(t, handler, "wp", "project-a"); status.Active {
		t.Fatalf("unmount all left project mounted: %+v", status)
	}

	// Nothing mounted anymore: unmount --all is a clean no-op.
	rec = doRequest(t, handler, "POST", "/api/warren/wp/unmount", `{"all":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("no-op unmount all = %d: %s", rec.Code, rec.Body.String())
	}
	var resp WarrenMountResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode no-op response: %v", err)
	}
	if len(resp.Projects) != 0 {
		t.Fatalf("no-op unmount all projects = %v, want empty", resp.Projects)
	}
}

// TestWarrenMountEndpointRefusalPassthrough pins that warren-layer refusals
// reach the HTTP client verbatim as 400s: a vault-ID collision across two
// warrens carries the same message the CLI prints.
func TestWarrenMountEndpointRefusalPassthrough(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	registerAPIWarren(t, workspaceRoot, "warren-a", "proj-a", "shared-vault")
	registerAPIWarren(t, workspaceRoot, "warren-b", "proj-b", "shared-vault")
	wireWarrenVaultRegistry(t, engine)

	rec := doRequest(t, handler, "POST", "/api/warren/warren-a/mount", `{"projects":["proj-a"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first mount = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, handler, "POST", "/api/warren/warren-b/mount", `{"projects":["proj-b"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("colliding mount = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "collides with") {
		t.Fatalf("colliding mount error = %q, want the warren collision message", rec.Body.String())
	}

	// Unknown project: same passthrough contract.
	rec = doRequest(t, handler, "POST", "/api/warren/warren-a/mount", `{"projects":["nope"]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not registered in Warren") {
		t.Fatalf("unknown project mount = %d %q, want 400 + warren message", rec.Code, rec.Body.String())
	}
}

// TestWarrenMountEndpointValidation covers the endpoint-owned error shapes:
// unregistered warren (404), empty project list (400), not-mounted unmount
// passthrough (400).
func TestWarrenMountEndpointValidation(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	registerAPIWarren(t, workspaceRoot, "wp", "project-a", "project-a-vault")
	wireWarrenVaultRegistry(t, engine)

	rec := doRequest(t, handler, "POST", "/api/warren/nope/mount", `{"projects":["project-a"]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unregistered warren mount = %d, want 404: %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, handler, "POST", "/api/warren/wp/mount", `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no projects specified") {
		t.Fatalf("empty mount = %d %q, want 400 no-projects message", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, handler, "POST", "/api/warren/wp/unmount", `{"projects":["project-a"]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not mounted") {
		t.Fatalf("not-mounted unmount = %d %q, want 400 not-mounted message", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, handler, "POST", "/api/warren/wp/mount", `{"projects":`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid JSON body") {
		t.Fatalf("malformed body = %d %q, want 400 invalid JSON", rec.Code, rec.Body.String())
	}
}

// TestWarrenMountEndpointIdentityNoOp: mounting an identified project over
// HTTP succeeds but records nothing (identity is derived, not mounted) —
// the same no-op contract as the CLI.
func TestWarrenMountEndpointIdentityNoOp(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	setupSelfAliasWarren(t, engine)

	rec := doRequest(t, handler, "POST", "/api/warren/wp/mount", `{"projects":["self-proj"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("identity mount = %d: %s", rec.Code, rec.Body.String())
	}
	state, _, err := warrenpkg.LoadWorkspaceState(workspaceRoot)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	if got := state.Warrens["wp"].ActiveProjects; len(got) != 0 {
		t.Fatalf("identity mount recorded state %v, want none", got)
	}
	// Status reports identity via SelfAlias (never as a mount: Active stays
	// false) and serves it from the live workspace vault, read-only.
	status := warrenStatusProject(t, handler, "wp", "self-proj")
	if !status.SelfAlias || status.Active || status.Editable {
		t.Fatalf("identity status = %+v, want self_alias (not active) read-only", status)
	}
	if status.Path != engine.MarmotDir {
		t.Fatalf("identity status path = %q, want the live workspace %q", status.Path, engine.MarmotDir)
	}
	if ids := engine.VaultRegistry.KnownVaultIDs(); len(ids) != 0 {
		t.Fatalf("identity mount routed the local vault: KnownVaultIDs = %v", ids)
	}
}

// TestDoctorWorkspaceEndpoint returns the DoctorWorkspace report verbatim:
// an identified project surfaces as a self_identity info issue.
func TestDoctorWorkspaceEndpoint(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	setupSelfAliasWarren(t, engine)

	rec := doRequest(t, handler, "GET", "/api/doctor/workspace", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("doctor workspace = %d: %s", rec.Code, rec.Body.String())
	}
	var report warrenpkg.DoctorReport
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("decode doctor report: %v", err)
	}
	var identity *warrenpkg.DoctorIssue
	for i := range report.Issues {
		if report.Issues[i].Code == "self_identity" {
			identity = &report.Issues[i]
		}
		if report.Issues[i].Severity == "error" {
			t.Errorf("healthy identity workspace reported error issue: %+v", report.Issues[i])
		}
	}
	if identity == nil {
		t.Fatalf("doctor report missing self_identity info: %+v", report.Issues)
	}
	if identity.Severity != "info" || identity.ProjectID != "self-proj" {
		t.Fatalf("self_identity issue = %+v, want info for self-proj", identity)
	}
}

// TestWarrenGraphSkippedReasons (U4.4): a skipped project carries its reason
// over HTTP, not just on stderr.
func TestWarrenGraphSkippedReasons(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	warrenRoot := setupAPIWarren(t, workspaceRoot, "wp", "project-a", "project-a-vault")
	wireWarrenVaultRegistry(t, engine)

	// Make the mounted checkout unavailable.
	if err := os.RemoveAll(filepath.Join(warrenRoot, "projects", "project-a", ".marmot")); err != nil {
		t.Fatalf("remove checkout: %v", err)
	}

	rec := doRequest(t, handler, "GET", "/api/warren/wp/graph", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("warren graph = %d: %s", rec.Code, rec.Body.String())
	}
	var resp GraphResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode graph response: %v", err)
	}
	if len(resp.Skipped) != 1 || resp.Skipped[0] != "project-a" {
		t.Fatalf("skipped = %v, want [project-a]", resp.Skipped)
	}
	reason, ok := resp.SkippedReasons["project-a"]
	if !ok || !strings.Contains(reason, "unavailable") {
		t.Fatalf("skipped_reasons = %v, want an unavailable reason for project-a", resp.SkippedReasons)
	}
}
