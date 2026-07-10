//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// --- Warren e2e scenarios (plan: artifacts/warren_resolution_plan.md,
// "The first warren e2e scenarios"). Isolation is HOME=<tmp> via hermeticEnv
// (runCLI/startMCP), never MARMOT_ROUTES=off: warren e2e needs routes to
// function, and all warren state lands inside the temp HOME where the tests
// can inspect it.

const (
	warrenID = "wa"

	projA      = "pa"
	projAVault = "pa-vault"
	// hotwalSummary exists only in project A's WAL at import time (written
	// through a live serve session that is still holding the DB open when
	// `warren project import` runs), so finding it later through the
	// imported copy pins the checkpoint-before-copy behavior (A2).
	hotwalID      = "e2e/hotwal"
	hotwalSummary = "Quokka telemetry beacon reconciles zanzibar ledger entries across regions."
	hotwalQuery   = "quokka telemetry beacon zanzibar ledger"

	projB      = "pb"
	projBVault = "pb-vault"
	burrowID   = "e2e/burrowmark"
	burrowBody = "Wombat burrow cartography atlas mapping tunnel networks for reconciliation."
	burrowQry  = "wombat burrow cartography atlas tunnel"
)

// seedWarren builds the shared warren fixture: two source projects with
// distinctive marker nodes, a warren repo with both imported, and a separate
// consumer workspace. Project A is imported while a live serve session still
// holds its embeddings DB open with an un-checkpointed write in the WAL
// (the A2 hot-WAL condition).
func seedWarren(t *testing.T) (warrenRoot, consumer string) {
	t.Helper()

	// Source project A: its distinctive node arrives via a live MCP session
	// so it exists only in the WAL when the import below copies the vault.
	srcA := seedProject(t)

	// Source project B: its distinctive node is indexed normally.
	srcB := seedProject(t)
	writeNodeFile(t, filepath.Join(srcB, ".marmot"), burrowID, burrowBody)
	if out, err := runCLI(srcB, "index", "--dir", ".marmot"); err != nil {
		t.Fatalf("index source B: %v\n%s", err, out)
	}

	warrenRoot = t.TempDir()
	if out, err := runCLI(warrenRoot, "warren", "init", "--id", warrenID); err != nil {
		t.Fatalf("warren init: %v\n%s", err, out)
	}

	// Hold project A's DB open across the import: the write below commits
	// into the -wal sidecar, which the import excludes — only the
	// checkpoint-before-copy makes the imported main file row-complete.
	sa := startMCP(t, srcA)
	writeOut := sa.callTool(1, "context_write", map[string]any{
		"id":      hotwalID,
		"type":    "concept",
		"summary": hotwalSummary,
	})
	if !strings.Contains(writeOut, `"status":"created"`) {
		t.Fatalf("hot-WAL write: expected created, got %s", writeOut)
	}
	if out, err := runCLI(warrenRoot, "warren", "project", "import", projA,
		filepath.Join(srcA, ".marmot"), "--vault-id", projAVault); err != nil {
		t.Fatalf("import %s: %v\n%s", projA, err, out)
	}
	if err := sa.closeAndWait(5 * time.Second); err != nil {
		t.Fatalf("close source A serve: %v", err)
	}

	if out, err := runCLI(warrenRoot, "warren", "project", "import", projB,
		filepath.Join(srcB, ".marmot"), "--vault-id", projBVault); err != nil {
		t.Fatalf("import %s: %v\n%s", projB, err, out)
	}

	consumer = seedProject(t)
	return warrenRoot, consumer
}

// writeNodeFile writes a minimal active node markdown file for id under
// vaultDir (id path convention: <vault>/<id>.md).
func writeNodeFile(t *testing.T, vaultDir, id, body string) {
	t.Helper()
	content := fmt.Sprintf("---\nid: %s\ntype: concept\nnamespace: default\nstatus: active\n---\n\n%s\n", id, body)
	path := filepath.Join(vaultDir, filepath.FromSlash(id)+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists (stat err: %v), want absent", path, err)
	}
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// burrowCachePath mirrors internal/warren.materializedProjectPath: the
// workspace-local copy a burrowed project is served from.
func burrowCachePath(consumer, projectID string) string {
	return filepath.Join(consumer, ".marmot", ".marmot-data", "warrens", warrenID, "projects", projectID, ".marmot")
}

// TestWarrenRegisterMountQueryServe is scenario E1: register → mount → query
// through a real serve — the baseline flow. One project is mounted plain,
// the other via `burrow --materialize` (the explicit flag; C2's implied
// materialization is pinned separately in the E5 lifecycle test). Both the
// CLI `query` and MCP `context_query` must return @vault-prefixed remote
// results, and the Tier 1 assertions hold:
//
//   - A1: querying the warren checkout mutates nothing — embeddings.db is
//     byte-identical afterwards (no journal-mode flip, no schema migration)
//     and any WAL sidecar the read-only open had to create stays empty
//     (SQLite needs the -shm index to read a WAL-mode DB; see the
//     NewStoreReadOnly doc — the sidecars are inert, the data file is not
//     touched).
//   - A2: the imported embeddings.db is row-complete although the source WAL
//     was hot (held open by a live serve) at import time.
//   - A3: the burrow cache contains no .marmot-data/.env and no WAL sidecars.
func TestWarrenRegisterMountQueryServe(t *testing.T) {
	warrenRoot, consumer := seedWarren(t)

	if out, err := runCLI(consumer, "warren", "register", "--dir", ".marmot", warrenID, warrenRoot); err != nil {
		t.Fatalf("warren register: %v\n%s", err, out)
	}
	if out, err := runCLI(consumer, "warren", "mount", "--dir", ".marmot", "--warren", warrenID, projA); err != nil {
		t.Fatalf("warren mount %s: %v\n%s", projA, err, out)
	}

	// Plant a secret and (empty) WAL sidecars in project B's checkout vault
	// so the burrow below has something to refuse to copy (A3).
	checkoutB := filepath.Join(warrenRoot, "projects", projB, ".marmot")
	dataB := filepath.Join(checkoutB, ".marmot-data")
	if err := os.WriteFile(filepath.Join(dataB, ".env"), []byte("SECRET_TOKEN=hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{"embeddings.db-wal", "embeddings.db-shm"} {
		if err := os.WriteFile(filepath.Join(dataB, sidecar), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Explicit --materialize per the reviewer's correction: E1 must not
	// depend on C2's burrow-implies-materialize default.
	out, err := runCLI(consumer, "warren", "burrow", "--dir", ".marmot", "--warren", warrenID, "--materialize", projB)
	if err != nil {
		t.Fatalf("warren burrow --materialize %s: %v\n%s", projB, err, out)
	}

	// A3: the cache exists and carries neither the secret nor the sidecars.
	cacheB := burrowCachePath(consumer, projB)
	if _, err := os.Stat(filepath.Join(cacheB, ".marmot-data", "embeddings.db")); err != nil {
		t.Fatalf("burrow cache missing embeddings.db: %v", err)
	}
	mustNotExist(t, filepath.Join(cacheB, ".marmot-data", ".env"))
	mustNotExist(t, filepath.Join(cacheB, ".marmot-data", "embeddings.db-wal"))
	mustNotExist(t, filepath.Join(cacheB, ".marmot-data", "embeddings.db-shm"))

	// A1 baseline: byte snapshot of project A's checkout DB before any
	// cross-vault query resolves it (project A is a plain mount, so queries
	// read the checkout, not a cache).
	checkoutADB := filepath.Join(warrenRoot, "projects", projA, ".marmot", ".marmot-data", "embeddings.db")
	dbBefore := readFileBytes(t, checkoutADB)

	// CLI query returns the remote (checkout-mounted) result — and because
	// hotwalSummary only ever existed in the source WAL at import time, this
	// also pins A2's checkpoint-before-copy.
	queryOut, err := runCLI(consumer, "query", "--dir", ".marmot", "--query", hotwalQuery)
	if err != nil {
		t.Fatalf("query: %v\n%s", err, queryOut)
	}
	if !strings.Contains(queryOut, "@"+projAVault+"/"+hotwalID) {
		t.Errorf("CLI query missing @%s/%s:\n%s", projAVault, hotwalID, queryOut)
	}

	// MCP context_query returns results from both mounted vaults.
	s := startMCP(t, consumer)
	mcpA := s.callTool(10, "context_query", map[string]any{"query": hotwalQuery, "budget": 4000})
	if !strings.Contains(mcpA, "@"+projAVault+"/"+hotwalID) {
		t.Errorf("MCP query missing @%s/%s:\n%s", projAVault, hotwalID, mcpA)
	}
	mcpB := s.callTool(11, "context_query", map[string]any{"query": burrowQry, "budget": 4000})
	if !strings.Contains(mcpB, "@"+projBVault+"/"+burrowID) {
		t.Errorf("MCP query missing @%s/%s:\n%s", projBVault, burrowID, mcpB)
	}
	if err := s.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("serve shutdown: %v", err)
	}

	// A1: the cross-vault reads above opened the checkout DB read-only —
	// no byte of the database changed (no journal-mode flip, no schema
	// migration), and if reading the WAL-mode file forced SQLite to create
	// a -wal sidecar, it carries no data (same assertion as the
	// internal/namespace registry regression test).
	dbAfter := readFileBytes(t, checkoutADB)
	if !bytes.Equal(dbBefore, dbAfter) {
		t.Errorf("warren checkout embeddings.db mutated by read-only queries (%d -> %d bytes)", len(dbBefore), len(dbAfter))
	}
	if fi, err := os.Stat(checkoutADB + "-wal"); err == nil && fi.Size() != 0 {
		t.Errorf("read-only queries wrote %d bytes into the checkout's WAL sidecar", fi.Size())
	}
}

// TestWarrenConcurrentMounts is scenario E2: two concurrent CLI processes
// `warren mount` distinct projects into the same workspace. Both mounts must
// survive in the workspace _warren.md — the cross-process regression for
// A5's flocked read-modify-write (before the flock, one mount's state write
// silently dropped the other's).
func TestWarrenConcurrentMounts(t *testing.T) {
	warrenRoot, consumer := seedWarren(t)
	if out, err := runCLI(consumer, "warren", "register", "--dir", ".marmot", warrenID, warrenRoot); err != nil {
		t.Fatalf("warren register: %v\n%s", err, out)
	}

	mount := func(project string) *exec.Cmd {
		cmd := exec.Command(binPath, "warren", "mount", "--dir", ".marmot", "--warren", warrenID, project)
		cmd.Dir = consumer
		cmd.Env = hermeticEnv(consumer)
		return cmd
	}
	cmdA, cmdB := mount(projA), mount(projB)
	if err := cmdA.Start(); err != nil {
		t.Fatalf("start mount %s: %v", projA, err)
	}
	if err := cmdB.Start(); err != nil {
		t.Fatalf("start mount %s: %v", projB, err)
	}
	if err := cmdA.Wait(); err != nil {
		t.Errorf("mount %s failed: %v", projA, err)
	}
	if err := cmdB.Wait(); err != nil {
		t.Errorf("mount %s failed: %v", projB, err)
	}

	out, err := runCLI(consumer, "warren", "status", "--dir", ".marmot", "--warren", warrenID, "--json")
	if err != nil {
		t.Fatalf("warren status: %v\n%s", err, out)
	}
	// The JSON array may be preceded by stderr warnings in CombinedOutput.
	start, end := strings.Index(out, "["), strings.LastIndex(out, "]")
	if start < 0 || end < start {
		t.Fatalf("no JSON array in status output:\n%s", out)
	}
	var statuses []struct {
		ProjectID string `json:"project_id"`
		Active    bool   `json:"active"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &statuses); err != nil {
		t.Fatalf("parse status JSON: %v\n%s", err, out)
	}
	active := map[string]bool{}
	for _, st := range statuses {
		active[st.ProjectID] = st.Active
	}
	for _, project := range []string{projA, projB} {
		if !active[project] {
			t.Errorf("project %s not active after concurrent mounts (lost update); status: %+v", project, statuses)
		}
	}
}

// TestWarrenIndexForceRefusedWhileMounted is scenario E4: while a serve in
// another workspace has a warren-mounted vault resolved (holding B4's shared
// read flock on the vault), `index --force` on that vault must refuse with a
// non-zero exit instead of deleting the embeddings DB under the reader; it
// succeeds once the reading process exits.
func TestWarrenIndexForceRefusedWhileMounted(t *testing.T) {
	warrenRoot, consumer := seedWarren(t)
	if out, err := runCLI(consumer, "warren", "register", "--dir", ".marmot", warrenID, warrenRoot); err != nil {
		t.Fatalf("warren register: %v\n%s", err, out)
	}
	if out, err := runCLI(consumer, "warren", "mount", "--dir", ".marmot", "--warren", warrenID, projA); err != nil {
		t.Fatalf("warren mount: %v\n%s", err, out)
	}

	s := startMCP(t, consumer)
	// A cross-vault query resolves the remote embedding store, taking the
	// shared vault.read.lock in the checkout for the life of the serve.
	res := s.callTool(20, "context_query", map[string]any{"query": hotwalQuery, "budget": 4000})
	if !strings.Contains(res, "@"+projAVault+"/") {
		t.Fatalf("mounted vault not resolved by serve (no @%s/ results):\n%s", projAVault, res)
	}

	vaultRel := filepath.Join("projects", projA, ".marmot")
	out, err := runCLI(warrenRoot, "index", "--force", "--dir", vaultRel)
	if err == nil {
		t.Fatalf("index --force succeeded while another process holds the vault read lock:\n%s", out)
	}
	if !strings.Contains(out, "open read-only by another marmot process") {
		t.Errorf("index --force refusal missing the warren-mount explanation:\n%s", out)
	}

	// The reader exits; its flock is kernel-released, so the rebuild is safe
	// and must succeed.
	if err := s.closeAndWait(5 * time.Second); err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}
	out, err = runCLI(warrenRoot, "index", "--force", "--dir", vaultRel)
	if err != nil {
		t.Fatalf("index --force after reader exit: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Indexed ") {
		t.Errorf("post-release index did no work (want an \"Indexed N/M\" line):\n%s", out)
	}
}

// TestWarrenEditableWriteBackAndBurrowLifecycle is scenario E5: an MCP
// context_write to an @vault ID of an editable mount lands in the warren
// checkout (C8/A4) and is searchable after the write's own registry refresh;
// then the burrow lifecycle: bare `burrow` materializes (C2), the cache
// serves queries after the checkout disappears, and
// `burrow --drop` + `unmount` + `unregister` leave the workspace clean (C1).
func TestWarrenEditableWriteBackAndBurrowLifecycle(t *testing.T) {
	warrenRoot, consumer := seedWarren(t)
	if out, err := runCLI(consumer, "warren", "register", "--dir", ".marmot", warrenID, warrenRoot); err != nil {
		t.Fatalf("warren register: %v\n%s", err, out)
	}
	// edit implies mount for project A; project B is mounted plain.
	out, err := runCLI(consumer, "warren", "edit", "--dir", ".marmot", "--warren", warrenID, projA)
	if err != nil {
		t.Fatalf("warren edit: %v\n%s", err, out)
	}
	if !strings.Contains(out, "editable") {
		t.Errorf("warren edit output missing editable confirmation:\n%s", out)
	}
	if out, err := runCLI(consumer, "warren", "mount", "--dir", ".marmot", "--warren", warrenID, projB); err != nil {
		t.Fatalf("warren mount %s: %v\n%s", projB, err, out)
	}

	// Editable write-back over MCP: update an existing mounted node.
	const sentinel = "Editable writeback sentinel: login now rotates opal session tokens hourly."
	s := startMCP(t, consumer)
	writeOut := s.callTool(30, "context_write", map[string]any{
		"id":      "@" + projAVault + "/auth/login",
		"summary": sentinel,
	})
	if !strings.Contains(writeOut, `"status":"updated"`) {
		t.Fatalf("editable @-write: expected updated, got %s", writeOut)
	}

	// The write landed under the CHECKOUT (never a cache) — the A4 contract.
	checkoutNode := filepath.Join(warrenRoot, "projects", projA, ".marmot", "auth", "login.md")
	if data := readFileBytes(t, checkoutNode); !strings.Contains(string(data), "opal session tokens") {
		t.Fatalf("editable write did not land in the checkout %s:\n%s", checkoutNode, data)
	}

	// Query-after-refresh: the write path refreshes the cached vault, so the
	// same live session finds the new summary without a restart.
	queryOut := s.callTool(31, "context_query", map[string]any{
		"query":  "editable writeback sentinel opal session tokens",
		"budget": 4000,
	})
	if !strings.Contains(queryOut, "@"+projAVault+"/auth/login") {
		t.Errorf("live query does not see the editable write:\n%s", queryOut)
	}
	if err := s.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("serve shutdown: %v", err)
	}

	// Burrow lifecycle. Bare `burrow` (no --materialize) must create the
	// cache — C2's implied materialization.
	if out, err := runCLI(consumer, "warren", "burrow", "--dir", ".marmot", "--warren", warrenID, projB); err != nil {
		t.Fatalf("warren burrow: %v\n%s", err, out)
	}
	cacheB := burrowCachePath(consumer, projB)
	if _, err := os.Stat(filepath.Join(cacheB, ".marmot-data", "embeddings.db")); err != nil {
		t.Fatalf("bare burrow did not materialize a cache: %v", err)
	}

	// The checkout disappears (e.g. the git clone is moved away); burrowed
	// projects keep answering from the cache.
	awayRoot := warrenRoot + ".away"
	if err := os.Rename(warrenRoot, awayRoot); err != nil {
		t.Fatalf("rename checkout away: %v", err)
	}
	queryOut2, err := runCLI(consumer, "query", "--dir", ".marmot", "--query", burrowQry)
	if err != nil {
		t.Fatalf("query with checkout gone: %v\n%s", err, queryOut2)
	}
	if !strings.Contains(queryOut2, "@"+projBVault+"/"+burrowID) {
		t.Errorf("burrow cache did not serve the query after the checkout vanished:\n%s", queryOut2)
	}

	// Teardown verbs work without the checkout: drop caches, unmount all,
	// unregister — the C1 escape hatch that used to require hand-editing
	// _warren.md.
	if out, err := runCLI(consumer, "warren", "burrow", "--dir", ".marmot", "--warren", warrenID, "--drop", "--all"); err != nil {
		t.Fatalf("warren burrow --drop: %v\n%s", err, out)
	}
	mustNotExist(t, cacheB)
	if out, err := runCLI(consumer, "warren", "unmount", "--dir", ".marmot", "--warren", warrenID, "--all"); err != nil {
		t.Fatalf("warren unmount --all: %v\n%s", err, out)
	}
	if out, err := runCLI(consumer, "warren", "unregister", "--dir", ".marmot", "--warren", warrenID); err != nil {
		t.Fatalf("warren unregister: %v\n%s", err, out)
	}
	out, err = runCLI(consumer, "warren", "list", "--dir", ".marmot")
	if err != nil {
		t.Fatalf("warren list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No Warrens registered.") {
		t.Errorf("workspace state not clean after unregister:\n%s", out)
	}
}

// TestWarrenGitRoadmapLoop is scenario E6 (git roadmap loop, pinning D1–D3):
// a local bare repo is the origin and a clone of it is the registered
// warren. `warren refresh --pull` fast-forwards the clone and
// re-materializes the burrow cache (the D2 provenance commit advances and
// the cache picks up the upstream revision); a dirty checkout — dirtied by a
// real editable-mount write through MCP (C8) — is refused, never stashed;
// `warren propose` then turns that write into exactly one pathspec-limited
// commit on a marmot/propose/... branch, returns to the original branch,
// leaves a dirty file outside the project untouched, and never pushes.
func TestWarrenGitRoadmapLoop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	authorRoot, consumer := seedWarren(t)

	// Hermetic git identity and default branch for every git invocation in
	// this test: the test's own calls and the ones the marmot binary spawns
	// for pull/propose both run with HOME=consumer (hermeticEnv), so a
	// .gitconfig there governs all of them.
	gitcfg := "[user]\n\tname = Marmot E2E\n\temail = marmot-e2e@test.invalid\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(filepath.Join(consumer, ".gitconfig"), []byte(gitcfg), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = hermeticEnv(consumer)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Author repo → bare origin → consumer-side clone (the warren the
	// workspace registers). Locks and SQLite sidecars that marmot may create
	// inside a live checkout are gitignored, as a real warren author would
	// (warren init already ignores the manifest lock; doctor nudges the
	// rest), so the D1 dirty check sees only real content changes.
	if err := os.WriteFile(filepath.Join(authorRoot, ".gitignore"),
		[]byte("_warren.md.lock\n*.lock\n*.db-wal\n*.db-shm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(authorRoot, "init")
	git(authorRoot, "add", "-A")
	git(authorRoot, "commit", "-m", "warren fixture")
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(authorRoot, "clone", "--bare", ".", origin)
	git(authorRoot, "remote", "add", "origin", origin)
	warrenRoot := filepath.Join(t.TempDir(), "warren-clone")
	git(consumer, "clone", origin, warrenRoot)

	if out, err := runCLI(consumer, "warren", "register", "--dir", ".marmot", warrenID, warrenRoot); err != nil {
		t.Fatalf("warren register: %v\n%s", err, out)
	}
	// Project A mounted plain (edited below); project B burrowed so refresh
	// --pull has a cache to re-materialize.
	if out, err := runCLI(consumer, "warren", "mount", "--dir", ".marmot", "--warren", warrenID, projA); err != nil {
		t.Fatalf("warren mount: %v\n%s", err, out)
	}
	if out, err := runCLI(consumer, "warren", "burrow", "--dir", ".marmot", "--warren", warrenID, projB); err != nil {
		t.Fatalf("warren burrow: %v\n%s", err, out)
	}

	oldHead := git(warrenRoot, "rev-parse", "HEAD")
	provPath := filepath.Join(filepath.Dir(burrowCachePath(consumer, projB)), "provenance.md")
	if prov := string(readFileBytes(t, provPath)); !strings.Contains(prov, oldHead) {
		t.Fatalf("burrow provenance not pinned to clone HEAD %s:\n%s", oldHead, prov)
	}

	// An upstream commit lands: the author revises project B's node and
	// pushes it to the origin.
	const upstreamMark = "Upstream quartz revision of the tunnel atlas."
	authorNode := filepath.Join(authorRoot, "projects", projB, ".marmot", filepath.FromSlash(burrowID)+".md")
	nodeData := readFileBytes(t, authorNode)
	if err := os.WriteFile(authorNode, append(nodeData, []byte("\n"+upstreamMark+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	git(authorRoot, "commit", "-am", "upstream: revise burrow cartography")
	git(authorRoot, "push", "origin", "main")

	// refresh --pull fast-forwards and re-materializes the stale burrow.
	out, err := runCLI(consumer, "warren", "refresh", "--dir", ".marmot", "--warren", warrenID, "--pull")
	if err != nil {
		t.Fatalf("warren refresh --pull: %v\n%s", err, out)
	}
	if !strings.Contains(out, "checkout pulled") {
		t.Errorf("refresh --pull did not report a pull:\n%s", out)
	}
	if !strings.Contains(out, "Re-materialized burrow cache(s): "+projB) {
		t.Errorf("refresh --pull did not re-materialize %s:\n%s", projB, out)
	}
	newHead := git(warrenRoot, "rev-parse", "HEAD")
	if newHead == oldHead {
		t.Fatalf("clone HEAD did not advance past %s", shortHash(oldHead))
	}
	if prov := string(readFileBytes(t, provPath)); !strings.Contains(prov, newHead) {
		t.Errorf("provenance commit did not advance to %s:\n%s", shortHash(newHead), prov)
	}
	cacheNode := filepath.Join(burrowCachePath(consumer, projB), filepath.FromSlash(burrowID)+".md")
	if cache := string(readFileBytes(t, cacheNode)); !strings.Contains(cache, upstreamMark) {
		t.Errorf("re-materialized cache is missing the upstream revision:\n%s", cache)
	}

	// A real editable-mount write (C8) dirties the checkout...
	if out, err := runCLI(consumer, "warren", "edit", "--dir", ".marmot", "--warren", warrenID, projA); err != nil {
		t.Fatalf("warren edit: %v\n%s", err, out)
	}
	const sentinel = "Roadmap loop sentinel: login rotates jasper tokens daily."
	s := startMCP(t, consumer)
	writeOut := s.callTool(40, "context_write", map[string]any{
		"id":      "@" + projAVault + "/auth/login",
		"summary": sentinel,
	})
	if !strings.Contains(writeOut, `"status":"updated"`) {
		t.Fatalf("editable @-write: expected updated, got %s", writeOut)
	}
	if err := s.closeAndWait(5 * time.Second); err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}

	// ...which refresh --pull refuses to touch (no stash, no force).
	out, err = runCLI(consumer, "warren", "refresh", "--dir", ".marmot", "--warren", warrenID, "--pull")
	if err == nil {
		t.Fatalf("refresh --pull succeeded on a dirty checkout:\n%s", out)
	}
	if !strings.Contains(out, "uncommitted change") {
		t.Errorf("dirty-checkout refusal missing the uncommitted-changes message:\n%s", out)
	}
	checkoutNode := filepath.Join(warrenRoot, "projects", projA, ".marmot", "auth", "login.md")
	if data := readFileBytes(t, checkoutNode); !strings.Contains(string(data), "jasper tokens") {
		t.Fatalf("dirty refusal destroyed the editable-mount edit:\n%s", data)
	}

	// A dirty file OUTSIDE the project must never be swept into a proposal.
	if err := os.WriteFile(filepath.Join(warrenRoot, "NOTES.txt"), []byte("scratch outside any project\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// propose: exactly one pathspec-limited commit on a fresh branch.
	out, err = runCLI(consumer, "warren", "propose", "--dir", ".marmot", "--warren", warrenID, projA)
	if err != nil {
		t.Fatalf("warren propose: %v\n%s", err, out)
	}
	if !strings.Contains(out, "never pushes") {
		t.Errorf("propose output missing the never-pushes note:\n%s", out)
	}
	branch := regexp.MustCompile(`marmot/propose/` + projA + `-[0-9]{8}-[0-9]{6}`).FindString(out)
	if branch == "" {
		t.Fatalf("no propose branch name in output:\n%s", out)
	}
	if cur := git(warrenRoot, "symbolic-ref", "--short", "HEAD"); cur != "main" {
		t.Errorf("propose did not return to the original branch: on %q", cur)
	}
	if n := git(warrenRoot, "rev-list", "--count", "main.."+branch); n != "1" {
		t.Errorf("propose branch is %s commits ahead of main, want exactly 1", n)
	}
	for _, file := range strings.Fields(git(warrenRoot, "diff", "--name-only", "main", branch)) {
		if !strings.HasPrefix(file, "projects/"+projA+"/") {
			t.Errorf("propose commit swept in a file outside the project: %s", file)
		}
	}
	if committed := git(warrenRoot, "show", branch+":projects/"+projA+"/.marmot/auth/login.md"); !strings.Contains(committed, "jasper tokens") {
		t.Errorf("propose commit is missing the editable write:\n%s", committed)
	}
	// The unrelated dirty file stays dirty in the working tree, unproposed.
	if status := git(warrenRoot, "status", "--porcelain"); !strings.Contains(status, "NOTES.txt") {
		t.Errorf("dirty file outside the project vanished after propose:\n%s", status)
	}
	// Local-only by design: nothing was pushed to the origin.
	if remote := git(origin, "branch", "--list"); strings.Contains(remote, "marmot/propose") {
		t.Errorf("propose pushed a branch to origin:\n%s", remote)
	}
}

// shortHash trims a commit hash for readable failure messages.
func shortHash(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// --- Self-mount aliasing (R1; plan: artifacts/warren_identity_ux_plan.md) ---

const (
	selfWarrenID  = "wsa"
	selfProj      = "self"
	consumerVault = "consumer-vault"
	selfNodeID    = "e2e/selfnode"
	selfStaleText = "Falcon migration ledger stanza original snapshot rev."
	selfLiveText  = "Falcon migration ledger stanza crimson addendum rev."

	bridgeheadID    = "e2e/bridgehead"
	bridgeheadText  = "Wombat bridgehead cartography referencing the consumer ledger."
	bridgeheadQuery = "wombat bridgehead cartography consumer ledger"
)

// writeSummaryNode writes a node file with an explicit frontmatter summary
// (query output renders summaries, so tests can assert on the text) and an
// optional raw edges YAML block.
func writeSummaryNode(t *testing.T, vaultDir, id, summary, edgesYAML string) {
	t.Helper()
	content := "---\nid: " + id + "\ntype: concept\nnamespace: default\nstatus: active\nsummary: " + summary + "\n" + edgesYAML + "---\n\n" + summary + "\n"
	path := filepath.Join(vaultDir, filepath.FromSlash(id)+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedSelfAliasWarren builds the self-mount fixture: a consumer workspace
// whose _config.md carries a vault_id, a warren holding (a) the consumer's
// own project imported WITHOUT --vault-id — the vault_id-preserving import
// path that creates the collision by construction — and (b) a second project
// whose marker node's edge crosses the manifest bridge into the consumer's
// vault via a self-qualified target. After the import, the live consumer
// node diverges from the warren snapshot so tests can tell which copy a
// query served.
func seedSelfAliasWarren(t *testing.T) (warrenRoot, consumer string) {
	t.Helper()
	consumer = seedProject(t)
	cfg := "---\nversion: \"1\"\nvault_id: " + consumerVault + "\nnamespace: default\nembedding_provider: mock\ntoken_budget: 8192\n---\n"
	if err := os.WriteFile(filepath.Join(consumer, ".marmot", "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSummaryNode(t, filepath.Join(consumer, ".marmot"), selfNodeID, selfStaleText, "")
	if out, err := runCLI(consumer, "index", "--dir", ".marmot"); err != nil {
		t.Fatalf("index consumer: %v\n%s", err, out)
	}

	warrenRoot = t.TempDir()
	if out, err := runCLI(warrenRoot, "warren", "init", "--id", selfWarrenID); err != nil {
		t.Fatalf("warren init: %v\n%s", err, out)
	}
	// Import self WITHOUT --vault-id: the copy keeps consumer-vault.
	if out, err := runCLI(warrenRoot, "warren", "project", "import", selfProj,
		filepath.Join(consumer, ".marmot")); err != nil {
		t.Fatalf("import %s: %v\n%s", selfProj, err, out)
	}

	// Second project: its marker node references the consumer's node with a
	// self-qualified target, so a query entering here traverses the bridge.
	srcB := seedProject(t)
	edges := "edges:\n    - target: \"@" + consumerVault + "/" + selfNodeID + "\"\n      relation: references\n"
	writeSummaryNode(t, filepath.Join(srcB, ".marmot"), bridgeheadID, bridgeheadText, edges)
	if out, err := runCLI(srcB, "index", "--dir", ".marmot"); err != nil {
		t.Fatalf("index source B: %v\n%s", err, out)
	}
	if out, err := runCLI(warrenRoot, "warren", "project", "import", projB,
		filepath.Join(srcB, ".marmot"), "--vault-id", projBVault); err != nil {
		t.Fatalf("import %s: %v\n%s", projB, err, out)
	}
	if out, err := runCLI(warrenRoot, "warren", "bridge", "add", selfProj, projB,
		"--warren-dir", ".", "--relations", "references"); err != nil {
		t.Fatalf("warren bridge add: %v\n%s", err, out)
	}

	// The live vault moves on after the import: the snapshot is now stale.
	writeSummaryNode(t, filepath.Join(consumer, ".marmot"), selfNodeID, selfLiveText, "")
	if out, err := runCLI(consumer, "index", "--dir", ".marmot"); err != nil {
		t.Fatalf("re-index consumer: %v\n%s", err, out)
	}
	return warrenRoot, consumer
}

// TestWarrenLocalIdentity is the R2 scenario (extending R1's self-mount
// alias test): a warren project whose vault_id matches the workspace IS the
// workspace — identity is derived, so bridges involving it activate with
// ONLY the foreign endpoint mounted, `mount self` is a recorded-nothing
// no-op, status shows the identity state, doctor reports self_identity, and
// R1-era self-mount state migrates cleanly (redundancy info + unmount).
func TestWarrenLocalIdentity(t *testing.T) {
	warrenRoot, consumer := seedSelfAliasWarren(t)

	registerOut, err := runCLI(consumer, "warren", "register", "--dir", ".marmot", selfWarrenID, warrenRoot)
	if err != nil {
		t.Fatalf("warren register: %v\n%s", err, registerOut)
	}
	if !strings.Contains(registerOut, "matches this workspace's vault ID") {
		t.Errorf("register did not announce the identity match:\n%s", registerOut)
	}

	// Mount ONLY the foreign endpoint: the self project is never mounted.
	if out, mountErr := runCLI(consumer, "warren", "mount", "--dir", ".marmot", "--warren", selfWarrenID, projB); mountErr != nil {
		t.Fatalf("warren mount %s: %v\n%s", projB, mountErr, out)
	}

	// A query entering the foreign project traverses the manifest bridge into
	// @consumer-vault/... and must land on the LIVE node, never the snapshot —
	// with self never mounted.
	queryOut, err := runCLI(consumer, "query", "--dir", ".marmot", "--query", bridgeheadQuery)
	if err != nil {
		t.Fatalf("query: %v\n%s", err, queryOut)
	}
	if !strings.Contains(queryOut, "@"+projBVault+"/"+bridgeheadID) {
		t.Errorf("query missing the bridge entry @%s/%s:\n%s", projBVault, bridgeheadID, queryOut)
	}
	if !strings.Contains(queryOut, "@"+consumerVault+"/"+selfNodeID) {
		t.Errorf("bridge traversal missing the self-qualified node @%s/%s:\n%s", consumerVault, selfNodeID, queryOut)
	}
	if !strings.Contains(queryOut, "crimson addendum") {
		t.Errorf("@%s/%s did not resolve to the LIVE vault:\n%s", consumerVault, selfNodeID, queryOut)
	}
	if strings.Contains(queryOut, "original snapshot") {
		t.Errorf("query served the STALE warren snapshot:\n%s", queryOut)
	}

	// warren status shows the identity state for the never-mounted self.
	statusOut, err := runCLI(consumer, "warren", "status", "--dir", ".marmot", "--warren", selfWarrenID)
	if err != nil {
		t.Fatalf("warren status: %v\n%s", err, statusOut)
	}
	if !regexp.MustCompile(`(?m)^` + selfProj + `\s+identity\s`).MatchString(statusOut) {
		t.Errorf("status missing the identity state for %q:\n%s", selfProj, statusOut)
	}

	// An explicit mount of the identified project is a no-op with the note.
	mountSelfOut, err := runCLI(consumer, "warren", "mount", "--dir", ".marmot", "--warren", selfWarrenID, selfProj)
	if err != nil {
		t.Fatalf("warren mount %s: %v\n%s", selfProj, err, mountSelfOut)
	}
	if !strings.Contains(mountSelfOut, "identity is automatic") {
		t.Errorf("mount self missing the no-op identity note:\n%s", mountSelfOut)
	}
	listOut, err := runCLI(consumer, "warren", "list", "--dir", ".marmot", "--json")
	if err != nil {
		t.Fatalf("warren list --json: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, `"identified_projects"`) || !strings.Contains(listOut, `"`+selfProj+`"`) {
		t.Errorf("list --json missing identified_projects:\n%s", listOut)
	}
	var listResp struct {
		Warrens map[string]struct {
			ActiveProjects []string `json:"active_projects"`
		} `json:"Warrens"`
	}
	if jsonErr := json.Unmarshal([]byte(listOut[strings.Index(listOut, "{"):]), &listResp); jsonErr != nil {
		t.Fatalf("parse list JSON: %v\n%s", jsonErr, listOut)
	}
	if got := listResp.Warrens[selfWarrenID].ActiveProjects; len(got) != 1 || got[0] != projB {
		t.Errorf("mount self recorded state: active = %v, want only %q", got, projB)
	}

	// Identified projects can never be editable or materialized.
	editOut, editErr := runCLI(consumer, "warren", "edit", "--dir", ".marmot", "--warren", selfWarrenID, selfProj)
	if editErr == nil {
		t.Fatalf("warren edit on an identified project succeeded:\n%s", editOut)
	}
	if !strings.Contains(editOut, "alias of the live vault") {
		t.Errorf("edit refusal missing the alias message:\n%s", editOut)
	}
	burrowOut, burrowErr := runCLI(consumer, "warren", "burrow", "--dir", ".marmot", "--warren", selfWarrenID, "--materialize", selfProj)
	if burrowErr == nil {
		t.Fatalf("warren burrow --materialize on an identified project succeeded:\n%s", burrowOut)
	}
	if !strings.Contains(burrowOut, "cannot be materialized") {
		t.Errorf("burrow refusal missing the self-alias message:\n%s", burrowOut)
	}

	// Doctor: healthy (exit 0), self_identity info, no redundancy (nothing
	// recorded), never a collision.
	doctorOut, doctorErr := runCLI(consumer, "warren", "doctor", "--workspace", "--dir", ".marmot", "--json")
	if doctorErr != nil {
		t.Fatalf("warren doctor --workspace on an identified project failed: %v\n%s", doctorErr, doctorOut)
	}
	if !strings.Contains(doctorOut, "self_identity") {
		t.Errorf("doctor JSON missing self_identity:\n%s", doctorOut)
	}
	if strings.Contains(doctorOut, "self_alias_mount") {
		t.Errorf("doctor reported a redundant mount that was never recorded:\n%s", doctorOut)
	}
	if strings.Contains(doctorOut, "vault_id_collision_workspace") {
		t.Errorf("doctor reported the identity as a collision:\n%s", doctorOut)
	}

	// --- Migration leg: hand-written R1-era state (self in active_projects).
	stateYAML := "---\nwarrens:\n    " + selfWarrenID + ":\n        path: \"" + warrenRoot + "\"\n        active_projects:\n            - " + projB + "\n            - " + selfProj + "\n---\n"
	if writeErr := os.WriteFile(filepath.Join(consumer, ".marmot", "_warren.md"), []byte(stateYAML), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	// Behavior identical: bridge traversal still serves the live vault.
	queryOut, err = runCLI(consumer, "query", "--dir", ".marmot", "--query", bridgeheadQuery)
	if err != nil {
		t.Fatalf("query with R1-era state: %v\n%s", err, queryOut)
	}
	if !strings.Contains(queryOut, "crimson addendum") || strings.Contains(queryOut, "original snapshot") {
		t.Errorf("R1-era self entry changed behavior:\n%s", queryOut)
	}
	// Doctor shows the redundancy info (still healthy)...
	doctorOut, doctorErr = runCLI(consumer, "warren", "doctor", "--workspace", "--dir", ".marmot", "--json")
	if doctorErr != nil {
		t.Fatalf("doctor with R1-era state failed: %v\n%s", doctorErr, doctorOut)
	}
	if !strings.Contains(doctorOut, "self_alias_mount") || !strings.Contains(doctorOut, "redundant self-mount") {
		t.Errorf("doctor missing the redundancy info for R1-era state:\n%s", doctorOut)
	}
	// ...and unmount cleans it; queries still live afterwards.
	if out, unmountErr := runCLI(consumer, "warren", "unmount", "--dir", ".marmot", "--warren", selfWarrenID, selfProj); unmountErr != nil {
		t.Fatalf("warren unmount self (R1-era cleanup): %v\n%s", unmountErr, out)
	}
	queryOut, err = runCLI(consumer, "query", "--dir", ".marmot", "--query", bridgeheadQuery)
	if err != nil {
		t.Fatalf("query after cleanup: %v\n%s", err, queryOut)
	}
	if !strings.Contains(queryOut, "crimson addendum") || strings.Contains(queryOut, "original snapshot") {
		t.Errorf("cleanup changed behavior:\n%s", queryOut)
	}
}

// TestWarrenSelfMountAliasLiveOwner is the liveness variant (template:
// TestWarrenMountWhileOwnerLive): with a daemon owner serving the consumer
// vault, a second process mounts the self project (plus the foreign one) —
// the owner's reload must NOT shadow the live vault: once the foreign mount
// is visible (proof the reload ran), bridge traversal through the daemon
// still returns the live content.
func TestWarrenSelfMountAliasLiveOwner(t *testing.T) {
	warrenRoot, consumer := seedSelfAliasWarren(t)
	owner := startMCPDaemon(t, consumer)

	// Baseline: nothing mounted, no @pb-vault results yet.
	baseline := owner.callTool(950, "context_query", map[string]any{
		"query": bridgeheadQuery, "budget": 6000,
	})
	if strings.Contains(baseline, "@"+projBVault+"/") {
		t.Fatalf("warren results visible before any mount:\n%s", baseline)
	}

	// Register + mount + refresh from separate CLI processes while the owner
	// keeps serving.
	if out, err := runCLI(consumer, "warren", "register", "--dir", ".marmot", selfWarrenID, warrenRoot); err != nil {
		t.Fatalf("warren register: %v\n%s", err, out)
	}
	out, err := runCLI(consumer, "warren", "mount", "--dir", ".marmot", "--warren", selfWarrenID, "--all")
	if err != nil {
		t.Fatalf("warren mount --all: %v\n%s", err, out)
	}
	if !strings.Contains(out, "identity is automatic") {
		t.Fatalf("mount output missing the identity no-op note:\n%s", out)
	}
	if out, err := runCLI(consumer, "warren", "refresh", "--dir", ".marmot", "--warren", selfWarrenID); err != nil {
		t.Fatalf("warren refresh: %v\n%s", err, out)
	}

	// Poll until the owner's reload shows the foreign mount, then hold it to
	// the alias contract: the self node resolves LIVE, never the snapshot.
	deadline := time.Now().Add(20 * time.Second)
	id := 951
	for {
		res, qerr := owner.callToolErr(id, "context_query", map[string]any{
			"query": bridgeheadQuery, "budget": 6000,
		}, 10*time.Second)
		id++
		if qerr == nil && strings.Contains(res, "@"+projBVault+"/"+bridgeheadID) {
			if !strings.Contains(res, "crimson addendum") {
				t.Fatalf("owner reload shadowed the live vault (no live content):\n%s", res)
			}
			if strings.Contains(res, "original snapshot") {
				t.Fatalf("owner served the STALE warren snapshot after reload:\n%s", res)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("live owner never picked up the warren mount (last err %v):\n%s", qerr, res)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
