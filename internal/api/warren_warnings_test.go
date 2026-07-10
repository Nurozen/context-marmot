package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// captureStderr redirects os.Stderr to a pipe for the duration of fn and
// returns everything written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	_ = w.Close()
	os.Stderr = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	return string(out)
}

// TestWarrenNodeUpdateEmbeddingFailureWarns (A6 #3): when the editable node
// write succeeds but the embedding refresh cannot (read-only .marmot-data),
// the response must carry a warning field instead of silently leaving the
// embedding stale — and must NOT 500, because the node write is durable.
func TestWarrenNodeUpdateEmbeddingFailureWarns(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("read-only dir injection does not work as root")
	}
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	warrenRoot := setupAPIWarren(t, workspaceRoot, "product-platform", "project-a", "project-a-vault")
	if _, err := warrenpkg.SetEditable(workspaceRoot, "product-platform", "project-a", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}

	dataDir := filepath.Join(warrenRoot, "projects", "project-a", ".marmot", ".marmot-data")
	if err := os.Chmod(dataDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0o755) })

	updatePath := "/api/node/" + url.PathEscape("@project-a-vault/service/api")
	var rec = doRequest(t, handler, "PUT", updatePath, `{"summary":"summary edited with broken embeddings"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (node write is durable), got %d: %s", rec.Code, rec.Body.String())
	}
	var resp NodeUpdateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.Warning, "embedding not updated") {
		t.Fatalf("warning = %q, want embedding-not-updated", resp.Warning)
	}

	// The node write itself landed.
	store := node.NewStore(filepath.Join(warrenRoot, "projects", "project-a", ".marmot"))
	updated, err := store.LoadNode(store.NodePath("service/api"))
	if err != nil {
		t.Fatalf("load node: %v", err)
	}
	if updated.Summary != "summary edited with broken embeddings" {
		t.Fatalf("node write lost: %q", updated.Summary)
	}
}

// TestSearchWarnsOncePerBrokenVault (A6 #7): a mounted vault without an
// embeddings DB is excluded from search best-effort, warning exactly once
// per vault per server, not once per query.
func TestSearchWarnsOncePerBrokenVault(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	warrenRoot := setupAPIWarren(t, workspaceRoot, "product-platform", "project-a", "project-a-vault")
	wireWarrenVaultRegistry(t, engine)

	// Break the remote vault: remove its embeddings DB.
	if err := os.Remove(filepath.Join(warrenRoot, "projects", "project-a", ".marmot", ".marmot-data", "embeddings.db")); err != nil {
		t.Fatalf("remove remote db: %v", err)
	}

	out := captureStderr(t, func() {
		for i := 0; i < 3; i++ {
			rec := doRequest(t, handler, "GET", "/api/search?q=Service%20API&ns=_warren/product-platform", "")
			if rec.Code != http.StatusOK {
				t.Errorf("search %d: expected 200, got %d: %s", i, rec.Code, rec.Body.String())
			}
		}
	})
	if got := strings.Count(out, `warren vault "project-a-vault" embedding store unavailable`); got != 1 {
		t.Fatalf("warning fired %d times across 3 queries, want exactly 1; stderr: %q", got, out)
	}
}

// TestWarrenGraphReportsSkippedMounts (A6 #9): an unavailable mount is no
// longer a silent partial graph — the response lists it in "skipped".
func TestWarrenGraphReportsSkippedMounts(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	warrenRoot := setupAPIWarren(t, workspaceRoot, "product-platform", "project-a", "project-a-vault")

	// Make the mount unavailable.
	if err := os.RemoveAll(filepath.Join(warrenRoot, "projects", "project-a")); err != nil {
		t.Fatalf("remove project: %v", err)
	}

	var rec *httptest.ResponseRecorder
	out := captureStderr(t, func() {
		rec = doRequest(t, handler, "GET", "/api/warren/product-platform/graph", "")
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp GraphResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Skipped) != 1 || resp.Skipped[0] != "project-a" {
		t.Fatalf("skipped = %+v, want [project-a]", resp.Skipped)
	}
	if !strings.Contains(out, "unavailable") {
		t.Fatalf("expected unavailable warning on stderr, got %q", out)
	}
}

// TestWarrenNodeUpdateRefreshFailureWarns (A6 #4): a failed registry refresh
// after an editable write is announced on stderr; the response is unaffected.
func TestWarrenNodeUpdateRefreshFailureWarns(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	setupAPIWarren(t, workspaceRoot, "product-platform", "project-a", "project-a-vault")
	if _, err := warrenpkg.SetEditable(workspaceRoot, "product-platform", "project-a", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}
	// Registry wired but the vault was never loaded: Refresh errors.
	wireWarrenVaultRegistry(t, engine)

	updatePath := "/api/node/" + url.PathEscape("@project-a-vault/service/api")
	var rec *httptest.ResponseRecorder
	out := captureStderr(t, func() {
		rec = doRequest(t, handler, "PUT", updatePath, `{"summary":"refresh warning check"}`)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(out, "refresh after editable write failed") {
		t.Fatalf("expected refresh warning on stderr, got %q", out)
	}
}

// TestFindWarrenMountWarnsOnBrokenState (A6 #8): a corrupt workspace state
// still reports "mount not found" (contract unchanged) but the real cause is
// visible on stderr.
func TestFindWarrenMountWarnsOnBrokenState(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	setupAPIWarren(t, workspaceRoot, "product-platform", "project-a", "project-a-vault")

	// Corrupt the workspace warren state.
	statePath := filepath.Join(engine.MarmotDir, "_warren.md")
	if err := os.WriteFile(statePath, []byte("---\nwarrens: [broken\n---\n"), 0o644); err != nil {
		t.Fatalf("corrupt state: %v", err)
	}

	updatePath := "/api/node/" + url.PathEscape("@project-a-vault/service/api")
	var rec *httptest.ResponseRecorder
	out := captureStderr(t, func() {
		rec = doRequest(t, handler, "PUT", updatePath, `{"summary":"x"}`)
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (contract unchanged), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(out, "warren mounts unavailable while resolving vault") {
		t.Fatalf("expected mounts-unavailable warning on stderr, got %q", out)
	}
}
