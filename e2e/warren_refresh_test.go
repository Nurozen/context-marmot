//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedWarrenFixture builds a warren repo with one project vault containing a
// distinctive node, indexes it with the mock embedder, and returns the
// warren root.
func seedWarrenFixture(t *testing.T) string {
	t.Helper()
	warrenRoot := t.TempDir()
	projVault := filepath.Join(warrenRoot, "projects", "proj-a", ".marmot")
	if err := os.MkdirAll(filepath.Join(projVault, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(warrenRoot, "_warren.md"): "---\nwarren_id: wp\nversion: 1\nprojects:\n    - project_id: proj-a\n      path: projects/proj-a/.marmot\n---\n",
		filepath.Join(projVault, "_config.md"):  "---\nversion: \"1\"\nvault_id: proj-a-vault\nnamespace: default\nembedding_provider: mock\n---\n",
		filepath.Join(projVault, "_warren.md"):  "---\nproject_id: proj-a\nwarren_id: wp\nvault_id: proj-a-vault\n---\n",
		filepath.Join(projVault, "ledger.md"):   "---\nid: ledger\ntype: module\nnamespace: default\nstatus: active\n---\n\nWarren payment ledger reconciliation service\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if out, err := runCLI(warrenRoot, "index", "--dir", filepath.Join("projects", "proj-a", ".marmot")); err != nil {
		t.Fatalf("index warren project: %v\n%s", err, out)
	}
	return warrenRoot
}

// TestWarrenMountWhileOwnerLive pins the Workstream B freshness model end to
// end: a daemon owner is serving a vault; a second process registers and
// mounts a warren project and runs `marmot warren refresh`; the live
// session's context_query then returns results from the newly mounted vault
// without any restart.
func TestWarrenMountWhileOwnerLive(t *testing.T) {
	proj := seedProject(t)
	owner := startMCP(t, proj)

	// Baseline: the distinctive warren node is not visible yet.
	baseline := owner.callTool(900, "context_query", map[string]any{
		"query": "warren payment ledger reconciliation",
	})
	if strings.Contains(baseline, "@proj-a-vault/") {
		t.Fatalf("warren results visible before any mount:\n%s", baseline)
	}

	warrenRoot := seedWarrenFixture(t)

	// Register + mount + refresh from separate CLI processes while the
	// owner keeps serving.
	if out, err := runCLI(proj, "warren", "register", "--dir", ".marmot", "wp", warrenRoot); err != nil {
		t.Fatalf("warren register: %v\n%s", err, out)
	}
	if out, err := runCLI(proj, "warren", "mount", "--dir", ".marmot", "--warren", "wp", "proj-a"); err != nil {
		t.Fatalf("warren mount: %v\n%s", err, out)
	}
	out, err := runCLI(proj, "warren", "refresh", "--dir", ".marmot", "--warren", "wp")
	if err != nil {
		t.Fatalf("warren refresh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "refreshed") {
		t.Fatalf("unexpected refresh output: %q", out)
	}

	// The owner's watcher fires on the _warren.md touch (1s debounce);
	// poll the live session until the mounted vault answers.
	deadline := time.Now().Add(20 * time.Second)
	id := 901
	for {
		res, qerr := owner.callToolErr(id, "context_query", map[string]any{
			"query": "warren payment ledger reconciliation",
		}, 10*time.Second)
		id++
		if qerr == nil && strings.Contains(res, "@proj-a-vault/ledger") {
			return // freshness model holds
		}
		if time.Now().After(deadline) {
			t.Fatalf("live owner never picked up the warren mount (last err %v):\n%s", qerr, res)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
