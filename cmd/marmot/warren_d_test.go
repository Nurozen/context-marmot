package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// gitTestEnv makes every git invocation in the test (including the ones the
// CLI under test spawns) hermetic: no system/user gitconfig, deterministic
// identity and default branch.
func gitTestEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	content := "[user]\n\tname = Marmot Test\n\temail = marmot@test.invalid\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// gitRun runs git in dir, failing the test on error, returning trimmed stdout.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOutput(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func gitInitCommit(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "initial")
}

// ---------------------------------------------------------------------------
// D1 — warren refresh --pull
// ---------------------------------------------------------------------------

// TestWarrenRefreshPullNonGit (D1): --pull on a plain-directory warren
// refuses and names the no-pull fallback; plain refresh keeps working.
func TestWarrenRefreshPullNonGit(t *testing.T) {
	gitTestEnv(t)
	marmotDir, _ := registeredWorkspace(t, "wp", "project-a")
	_, stderr, code := captureRunBoth(t, []string{"warren", "refresh", "--dir", marmotDir, "--warren", "wp", "--pull"})
	if code != 1 || !strings.Contains(stderr, "not a git checkout") || !strings.Contains(stderr, "without --pull") {
		t.Fatalf("refresh --pull on non-git = code %d stderr %q, want git refusal", code, stderr)
	}
	if code := run([]string{"warren", "refresh", "--dir", marmotDir, "--warren", "wp"}); code != 0 {
		t.Fatalf("plain refresh exit code = %d, want 0", code)
	}
}

// TestWarrenRefreshPullDirtyCheckout (D1): a dirty warren checkout (editable
// -mount edits live there) is refused, never stashed or forced.
func TestWarrenRefreshPullDirtyCheckout(t *testing.T) {
	gitTestEnv(t)
	marmotDir, warrenRoot := registeredWorkspace(t, "wp", "project-a")
	gitInitCommit(t, warrenRoot)
	dirtyFile := filepath.Join(warrenRoot, "projects", "project-a", ".marmot", "notes.md")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := captureRunBoth(t, []string{"warren", "refresh", "--dir", marmotDir, "--warren", "wp", "--pull"})
	if code != 1 || !strings.Contains(stderr, "uncommitted change") {
		t.Fatalf("refresh --pull on dirty checkout = code %d stderr %q, want dirty refusal", code, stderr)
	}
	// Refusal must not have touched the user's work.
	if data, err := os.ReadFile(dirtyFile); err != nil || string(data) != "uncommitted edit\n" {
		t.Fatalf("dirty file was modified: %q err=%v", data, err)
	}
}

// TestWarrenRefreshPullRematerializesStale (D1 step 5 + D2): a burrow cache
// is skipped while its provenance pins the checkout HEAD, re-copied when the
// provenance is missing (crash fallback), and re-copied after the checkout
// fast-forwards to a new commit.
func TestWarrenRefreshPullRematerializesStale(t *testing.T) {
	gitTestEnv(t)
	src := testWarrenRoot(t, "wp", "project-a")
	noteSrc := filepath.Join(src, "projects", "project-a", ".marmot", "notes.md")
	if err := os.WriteFile(noteSrc, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInitCommit(t, src)
	clone := filepath.Join(t.TempDir(), "clone")
	gitRun(t, filepath.Dir(clone), "clone", src, clone)

	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", clone}); code != 0 {
		t.Fatal("register failed")
	}
	if code := run([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatal("burrow failed")
	}
	head := gitRun(t, clone, "rev-parse", "HEAD")
	prov, err := warrenpkg.LoadBurrowProvenance(marmotDir, "wp", "project-a")
	if err != nil || prov.SourceCommit != head {
		t.Fatalf("provenance after burrow = (%+v, %v), want pinned to %s", prov, err, head)
	}

	// Up-to-date pin: nothing re-materialized.
	stdout, _, code := captureRunBoth(t, []string{"warren", "refresh", "--dir", marmotDir, "--warren", "wp", "--pull"})
	if code != 0 || strings.Contains(stdout, "Re-materialized") {
		t.Fatalf("refresh --pull with fresh pin = code %d stdout %q, want no re-materialize", code, stdout)
	}

	// Missing provenance = stale (crash between swap and provenance write).
	cacheDir := filepath.Join(marmotDir, ".marmot-data", "warrens", "wp", "projects", "project-a")
	if err := os.Remove(filepath.Join(cacheDir, "provenance.md")); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = captureRunBoth(t, []string{"warren", "refresh", "--dir", marmotDir, "--warren", "wp", "--pull"})
	if code != 0 || !strings.Contains(stdout, "Re-materialized burrow cache(s): project-a") {
		t.Fatalf("refresh --pull without provenance = code %d stdout %q, want re-materialize", code, stdout)
	}
	if prov, err = warrenpkg.LoadBurrowProvenance(marmotDir, "wp", "project-a"); err != nil || prov.SourceCommit != head {
		t.Fatalf("provenance not restored: (%+v, %v)", prov, err)
	}

	// Upstream moves: pull fast-forwards and the cache follows.
	if err := os.WriteFile(noteSrc, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, "add", "-A")
	gitRun(t, src, "commit", "-m", "update notes")
	stdout, _, code = captureRunBoth(t, []string{"warren", "refresh", "--dir", marmotDir, "--warren", "wp", "--pull"})
	if code != 0 || !strings.Contains(stdout, "checkout pulled") || !strings.Contains(stdout, "Re-materialized burrow cache(s): project-a") {
		t.Fatalf("refresh --pull after upstream commit = code %d stdout %q", code, stdout)
	}
	newHead := gitRun(t, clone, "rev-parse", "HEAD")
	if newHead == head {
		t.Fatal("pull did not fast-forward the clone")
	}
	cachedNote := filepath.Join(cacheDir, ".marmot", "notes.md")
	if data, err := os.ReadFile(cachedNote); err != nil || string(data) != "v2\n" {
		t.Fatalf("burrow cache not refreshed: %q err=%v", data, err)
	}
	if prov, err = warrenpkg.LoadBurrowProvenance(marmotDir, "wp", "project-a"); err != nil || prov.SourceCommit != newHead {
		t.Fatalf("provenance = (%+v, %v), want pinned to %s", prov, err, newHead)
	}
}

// ---------------------------------------------------------------------------
// D3 — warren propose
// ---------------------------------------------------------------------------

// TestWarrenProposeFlow (D3): propose branches exactly the project's
// changes, returns to the original branch, and leaves unrelated dirty files
// alone; a clean project then has nothing to propose.
func TestWarrenProposeFlow(t *testing.T) {
	gitTestEnv(t)
	warrenRoot := testWarrenRoot(t, "wp", "project-a", "project-b")
	if err := os.WriteFile(filepath.Join(warrenRoot, "README.md"), []byte("readme v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInitCommit(t, warrenRoot)

	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}
	if code := run([]string{"warren", "edit", "--dir", marmotDir, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatal("edit failed")
	}

	// Simulated editable-mount edit inside project-a, plus unrelated dirt.
	if err := os.WriteFile(filepath.Join(warrenRoot, "projects", "project-a", ".marmot", "notes.md"), []byte("proposed edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(warrenRoot, "README.md"), []byte("readme dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No project argument: the sole editable project is the default.
	stdout, _, code := captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp"})
	if code != 0 || !strings.Contains(stdout, "Created branch") || !strings.Contains(stdout, "marmot never pushes") {
		t.Fatalf("propose = code %d stdout %q", code, stdout)
	}

	if branch := gitRun(t, warrenRoot, "symbolic-ref", "--short", "HEAD"); branch != "main" {
		t.Fatalf("current branch = %q, want back on main", branch)
	}
	branches := gitRun(t, warrenRoot, "branch", "--list", "marmot/propose/project-a-*", "--format", "%(refname:short)")
	if branches == "" {
		t.Fatal("propose branch not created")
	}
	branch := strings.Fields(branches)[0]
	// Exactly one commit ahead of main, touching only project-a files.
	if count := gitRun(t, warrenRoot, "rev-list", "--count", "main.."+branch); count != "1" {
		t.Fatalf("commits ahead = %s, want 1", count)
	}
	for _, file := range strings.Split(gitRun(t, warrenRoot, "diff", "--name-only", "main", branch), "\n") {
		if !strings.HasPrefix(file, "projects/project-a/") {
			t.Fatalf("proposal swept in unrelated file %q", file)
		}
	}
	// The unrelated dirty file survived in the working tree.
	porcelain := gitRun(t, warrenRoot, "status", "--porcelain")
	if !strings.Contains(porcelain, "README.md") {
		t.Fatalf("status = %q, want dirty README.md untouched", porcelain)
	}
	if strings.Contains(porcelain, "projects/project-a/") {
		t.Fatalf("status = %q, want project-a changes moved onto the branch", porcelain)
	}

	// Second propose: the project is clean now.
	stdout, _, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "project-a"})
	if code != 0 || !strings.Contains(stdout, "nothing to propose") {
		t.Fatalf("clean propose = code %d stdout %q, want nothing-to-propose exit 0", code, stdout)
	}
}

// TestWarrenProposeRefusals (D3): detached HEAD, ambiguous/no editable
// project, and unknown project all refuse with actionable messages.
func TestWarrenProposeRefusals(t *testing.T) {
	gitTestEnv(t)
	warrenRoot := testWarrenRoot(t, "wp", "project-a", "project-b")
	gitInitCommit(t, warrenRoot)
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}

	// No editable projects and no explicit argument.
	_, stderr, code := captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp"})
	if code != 1 || !strings.Contains(stderr, "no editable projects") {
		t.Fatalf("propose without editable = code %d stderr %q", code, stderr)
	}
	// Unknown project.
	_, stderr, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "ghost"})
	if code != 1 || !strings.Contains(stderr, "not registered") {
		t.Fatalf("propose unknown project = code %d stderr %q", code, stderr)
	}
	// Ambiguous: two editable projects, no argument.
	for _, project := range []string{"project-a", "project-b"} {
		if code := run([]string{"warren", "edit", "--dir", marmotDir, "--warren", "wp", project}); code != 0 {
			t.Fatal("edit failed")
		}
	}
	_, stderr, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp"})
	if code != 1 || !strings.Contains(stderr, "editable projects") {
		t.Fatalf("ambiguous propose = code %d stderr %q", code, stderr)
	}
	// Detached HEAD: propose could not return to a branch.
	gitRun(t, warrenRoot, "checkout", "--detach")
	_, stderr, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "project-a"})
	if code != 1 || !strings.Contains(stderr, "detached HEAD") {
		t.Fatalf("detached propose = code %d stderr %q", code, stderr)
	}
}

// proposeJSONEnvelope mirrors the schema:1 propose contract for decoding in
// tests (see testdata/contracts/warren_propose.v1.json).
type proposeJSONEnvelope struct {
	Schema           int      `json:"schema"`
	WarrenID         string   `json:"warren_id"`
	ProjectID        string   `json:"project_id"`
	Branch           string   `json:"branch"`
	Commit           string   `json:"commit"`
	Committed        bool     `json:"committed"`
	NothingToPropose bool     `json:"nothing_to_propose"`
	PushCommand      string   `json:"push_command"`
	Warnings         []string `json:"warnings"`
	Error            *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

func decodeProposeEnvelope(t *testing.T, stdout string) proposeJSONEnvelope {
	t.Helper()
	var env proposeJSONEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\nstdout: %q", err, stdout)
	}
	if env.Schema != 1 {
		t.Fatalf("schema = %d, want 1\nstdout: %q", env.Schema, stdout)
	}
	return env
}

// TestWarrenProposeJSONSuccess (--json): a committed proposal emits the full
// schema:1 success envelope on stdout with a real sha and push command, and
// a subsequent clean run is nothing_to_propose SUCCESS (exit 0), never an
// error — stave treats propose failure as fatal.
func TestWarrenProposeJSONSuccess(t *testing.T) {
	gitTestEnv(t)
	warrenRoot := testWarrenRoot(t, "wp", "project-a")
	gitInitCommit(t, warrenRoot)
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}
	if code := run([]string{"warren", "edit", "--dir", marmotDir, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatal("edit failed")
	}
	if err := os.WriteFile(filepath.Join(warrenRoot, "projects", "project-a", ".marmot", "notes.md"), []byte("proposed edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "--json"})
	if code != 0 {
		t.Fatalf("propose --json = code %d stdout %q", code, stdout)
	}
	env := decodeProposeEnvelope(t, stdout)
	if env.WarrenID != "wp" || env.ProjectID != "project-a" {
		t.Fatalf("envelope ids = %q/%q, want wp/project-a", env.WarrenID, env.ProjectID)
	}
	if !env.Committed || env.NothingToPropose {
		t.Fatalf("envelope = %+v, want committed=true nothing_to_propose=false", env)
	}
	if !strings.HasPrefix(env.Branch, "marmot/propose/project-a-") {
		t.Fatalf("branch = %q", env.Branch)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(env.Commit) {
		t.Fatalf("commit = %q, want 40-hex sha", env.Commit)
	}
	if head := gitRun(t, warrenRoot, "rev-parse", "refs/heads/"+env.Branch); head != env.Commit {
		t.Fatalf("commit = %q, branch tip = %q", env.Commit, head)
	}
	wantPush := "git -C " + warrenRoot + " push -u origin " + env.Branch
	if env.PushCommand != wantPush {
		t.Fatalf("push_command = %q, want %q", env.PushCommand, wantPush)
	}
	if env.Warnings == nil {
		t.Fatal("warnings must be an empty array, not null")
	}
	// stdout must be envelope-only: no human text alongside the JSON.
	if strings.Contains(stdout, "Created branch") || strings.Contains(stdout, "marmot never pushes") {
		t.Fatalf("human text leaked into --json stdout: %q", stdout)
	}

	// Clean tree: nothing to propose is SUCCESS with the documented shape.
	stdout, _, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "--json", "project-a"})
	if code != 0 {
		t.Fatalf("clean propose --json = code %d stdout %q, want exit 0", code, stdout)
	}
	env = decodeProposeEnvelope(t, stdout)
	if env.Committed || !env.NothingToPropose {
		t.Fatalf("clean envelope = %+v, want committed=false nothing_to_propose=true", env)
	}
	if env.Branch != "" || env.Commit != "" || env.PushCommand != "" {
		t.Fatalf("clean envelope must have empty branch/commit/push_command: %+v", env)
	}
	if env.WarrenID != "wp" || env.ProjectID != "project-a" || env.Warnings == nil {
		t.Fatalf("clean envelope = %+v", env)
	}
}

// TestWarrenProposeJSONErrors (--json): refusals emit schema:1 error
// envelopes on stdout with stable codes and exit 1.
func TestWarrenProposeJSONErrors(t *testing.T) {
	gitTestEnv(t)
	warrenRoot := testWarrenRoot(t, "wp", "project-a")
	gitInitCommit(t, warrenRoot)
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}

	// No editable projects and no explicit argument.
	stdout, _, code := captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "--json"})
	if code != 1 {
		t.Fatalf("propose --json without editable = code %d stdout %q", code, stdout)
	}
	env := decodeProposeEnvelope(t, stdout)
	if env.Error == nil || env.Error.Code != "invalid_args" || env.Error.Message == "" {
		t.Fatalf("error envelope = %+v", env.Error)
	}

	// Detached HEAD.
	gitRun(t, warrenRoot, "checkout", "--detach")
	stdout, _, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "--json", "project-a"})
	if code != 1 {
		t.Fatalf("detached propose --json = code %d stdout %q", code, stdout)
	}
	env = decodeProposeEnvelope(t, stdout)
	if env.Error == nil || env.Error.Code != "detached_head" {
		t.Fatalf("error envelope = %+v, want detached_head", env.Error)
	}

	// Unreachable warren: the registered checkout path is gone.
	if err := os.RemoveAll(warrenRoot); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "--json", "project-a"})
	if code != 1 {
		t.Fatalf("unreachable propose --json = code %d stdout %q", code, stdout)
	}
	env = decodeProposeEnvelope(t, stdout)
	if env.Error == nil || env.Error.Code != "warren_unreachable" || env.Error.Message == "" {
		t.Fatalf("error envelope = %+v, want warren_unreachable", env.Error)
	}
}

// TestWarrenProposeJSONErrorsMore (--json): the flag-parse failure path
// (proposeJSONRequested), a missing workspace, and a non-git warren checkout
// all emit schema:1 error envelopes with their documented codes.
func TestWarrenProposeJSONErrorsMore(t *testing.T) {
	gitTestEnv(t)

	// Flag-parse failure with --json in the raw args: envelope, not bare exit.
	stdout, _, code := captureRunBoth(t, []string{"warren", "propose", "--bogus", "--json"})
	if code != 1 {
		t.Fatalf("parse-fail propose --json = code %d stdout %q", code, stdout)
	}
	env := decodeProposeEnvelope(t, stdout)
	if env.Error == nil || env.Error.Code != "invalid_args" {
		t.Fatalf("error envelope = %+v, want invalid_args", env.Error)
	}

	// No workspace at --dir.
	missing := filepath.Join(t.TempDir(), "nowhere", ".marmot")
	stdout, _, code = captureRunBoth(t, []string{"warren", "propose", "--dir", missing, "--json"})
	if code != 1 {
		t.Fatalf("no-workspace propose --json = code %d stdout %q", code, stdout)
	}
	env = decodeProposeEnvelope(t, stdout)
	if env.Error == nil || env.Error.Code != "workspace_not_found" {
		t.Fatalf("error envelope = %+v, want workspace_not_found", env.Error)
	}

	// Registered warren checkout that is not a git repo.
	warrenRoot := testWarrenRoot(t, "wp", "project-a")
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}
	stdout, _, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "--json", "project-a"})
	if code != 1 {
		t.Fatalf("non-git propose --json = code %d stdout %q", code, stdout)
	}
	env = decodeProposeEnvelope(t, stdout)
	if env.Error == nil || env.Error.Code != "propose_failed" || !strings.Contains(env.Error.Message, "not a git checkout") {
		t.Fatalf("error envelope = %+v, want propose_failed not-a-git-checkout", env.Error)
	}
}

// TestWarrenProposeExcludesEmbeddingsDB (OQ7): embeddings DB sidecars under
// .marmot-data never enter a proposal — a DB-only dirty tree is
// nothing-to-propose (both modes), and a mixed tree commits only the real
// edits while the sidecars stay dirty in the worktree.
func TestWarrenProposeExcludesEmbeddingsDB(t *testing.T) {
	gitTestEnv(t)
	warrenRoot := testWarrenRoot(t, "wp", "project-a")
	dataDir := filepath.Join(warrenRoot, "projects", "project-a", ".marmot", ".marmot-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbFile := filepath.Join(dataDir, "embeddings.db")
	if err := os.WriteFile(dbFile, []byte("db v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInitCommit(t, warrenRoot) // DB tracked at v1
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}
	if code := run([]string{"warren", "edit", "--dir", marmotDir, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatal("edit failed")
	}

	// Dirty tracked DB plus an untracked WAL sidecar: nothing to propose.
	if err := os.WriteFile(dbFile, []byte("db v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbFile+"-wal", []byte("wal"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "--json"})
	if code != 0 {
		t.Fatalf("DB-only propose --json = code %d stdout %q, want success", code, stdout)
	}
	env := decodeProposeEnvelope(t, stdout)
	if env.Committed || !env.NothingToPropose {
		t.Fatalf("DB-only envelope = %+v, want nothing_to_propose", env)
	}
	// Same in human mode.
	stdout, _, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp"})
	if code != 0 || !strings.Contains(stdout, "nothing to propose") {
		t.Fatalf("DB-only human propose = code %d stdout %q, want nothing-to-propose", code, stdout)
	}

	// Mixed: a real edit alongside the dirty DB commits only the real edit.
	if err := os.WriteFile(filepath.Join(warrenRoot, "projects", "project-a", ".marmot", "notes.md"), []byte("real edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "--json"})
	if code != 0 {
		t.Fatalf("mixed propose --json = code %d stdout %q", code, stdout)
	}
	env = decodeProposeEnvelope(t, stdout)
	if !env.Committed {
		t.Fatalf("mixed envelope = %+v, want committed", env)
	}
	for _, file := range strings.Split(gitRun(t, warrenRoot, "diff", "--name-only", "main", env.Branch), "\n") {
		if strings.Contains(file, ".marmot-data") {
			t.Fatalf("proposal swept in DB sidecar %q", file)
		}
	}
	// The sidecars remain dirty in the worktree, untouched.
	porcelain := gitRun(t, warrenRoot, "status", "--porcelain")
	if !strings.Contains(porcelain, "embeddings.db") {
		t.Fatalf("status = %q, want DB sidecars still dirty", porcelain)
	}
	if data, err := os.ReadFile(dbFile); err != nil || string(data) != "db v2" {
		t.Fatalf("DB file modified: %q err=%v", data, err)
	}
}

// ---------------------------------------------------------------------------
// D4 — set-readonly CLI + edit refusal
// ---------------------------------------------------------------------------

// TestWarrenSetReadonlyCLI (D4): the author-side verb flips policy, the
// consumer-side edit verb respects it, and --off restores it.
func TestWarrenSetReadonlyCLI(t *testing.T) {
	marmotDir, warrenRoot := registeredWorkspace(t, "wp", "project-a")

	if _, code := captureRun([]string{"warren", "project", "set-readonly", "--warren-dir", warrenRoot, "project-a"}); code != 0 {
		t.Fatalf("set-readonly exit code = %d", code)
	}
	manifest, _, err := warrenpkg.LoadManifest(warrenRoot)
	if err != nil || !manifest.Projects[0].ReadOnly || manifest.Version != 2 {
		t.Fatalf("manifest = (%+v, %v), want readonly at version 2", manifest, err)
	}

	_, stderr, code := captureRunBoth(t, []string{"warren", "edit", "--dir", marmotDir, "--warren", "wp", "project-a"})
	if code != 1 || !strings.Contains(stderr, "read-only") {
		t.Fatalf("edit on readonly = code %d stderr %q, want policy refusal", code, stderr)
	}

	if _, code := captureRun([]string{"warren", "project", "set-readonly", "--warren-dir", warrenRoot, "--off", "project-a"}); code != 0 {
		t.Fatalf("set-readonly --off exit code = %d", code)
	}
	if code := run([]string{"warren", "edit", "--dir", marmotDir, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatal("edit after --off must succeed")
	}
}

// ---------------------------------------------------------------------------
// D5 — doctor --workspace + mount-time model warning
// ---------------------------------------------------------------------------

// TestWarrenDoctorWorkspaceCLI (D5.3): the workspace-side doctor mode reports
// legacy vault-ID collisions and exits 1; a clean workspace is healthy.
func TestWarrenDoctorWorkspaceCLI(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenA := testWarrenRoot(t, "warren-a", "project-x")
	warrenB := testWarrenRoot(t, "warren-b", "project-y")
	for _, fix := range []struct{ root, project string }{{warrenA, "project-x"}, {warrenB, "project-y"}} {
		dir := filepath.Join(fix.root, "projects", fix.project, ".marmot")
		meta, body, err := warrenpkg.LoadProjectMetadata(dir)
		if err != nil {
			t.Fatal(err)
		}
		meta.VaultID = "shared-vault"
		if err := warrenpkg.SaveProjectMetadata(dir, meta, body); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy state written before the mount-time refusal existed.
	state := &warrenpkg.WorkspaceState{Warrens: map[string]warrenpkg.WorkspaceWarren{
		"warren-a": {Path: warrenA, ActiveProjects: []string{"project-x"}},
		"warren-b": {Path: warrenB, ActiveProjects: []string{"project-y"}},
	}}
	if err := warrenpkg.SaveWorkspaceState(workspace, state, ""); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := captureRunBoth(t, []string{"warren", "doctor", "--workspace", "--dir", marmotDir})
	if code != 1 || !strings.Contains(stderr, "vault_id_collision_workspace") {
		t.Fatalf("doctor --workspace = code %d stderr %q, want collision error", code, stderr)
	}

	if code := run([]string{"warren", "unmount", "--dir", marmotDir, "--warren", "warren-b", "--all"}); code != 0 {
		t.Fatal("unmount failed")
	}
	stdout, _, code := captureRunBoth(t, []string{"warren", "doctor", "--workspace", "--dir", marmotDir})
	if code != 0 || !strings.Contains(stdout, "healthy") {
		t.Fatalf("healthy doctor --workspace = code %d stdout %q", code, stdout)
	}
}

// TestWarrenDoctorWorkspaceSelfAliasHealthyCLI (R2.7 companion): an
// identified project (project vault_id == workspace vault_id) is healthy
// with no mount at all — doctor exits 0 with the self_identity info; the
// explicit mount attempt is a no-op that records nothing, so no redundancy
// info appears either.
func TestWarrenDoctorWorkspaceSelfAliasHealthyCLI(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "---\nversion: \"1\"\nvault_id: self-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	warrenRoot := testWarrenRoot(t, "wp", "self-proj")
	dir := filepath.Join(warrenRoot, "projects", "self-proj", ".marmot")
	meta, body, err := warrenpkg.LoadProjectMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.VaultID = "self-vault"
	if err := warrenpkg.SaveProjectMetadata(dir, meta, body); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}
	// Explicit mount of the identified project: exit 0, nothing recorded.
	if code := run([]string{"warren", "mount", "--dir", marmotDir, "--warren", "wp", "self-proj"}); code != 0 {
		t.Fatal("self mount no-op failed")
	}
	state, _, err := warrenpkg.LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Warrens["wp"].ActiveProjects; len(got) != 0 {
		t.Fatalf("self mount recorded state: %v, want none", got)
	}

	stdout, _, code := captureRunBoth(t, []string{"warren", "doctor", "--workspace", "--dir", marmotDir, "--json"})
	if code != 0 {
		t.Fatalf("doctor --workspace on an identified project = code %d, want 0 (stdout %q)", code, stdout)
	}
	if !strings.Contains(stdout, "self_identity") {
		t.Fatalf("doctor JSON missing self_identity info: %q", stdout)
	}
	if strings.Contains(stdout, "self_alias_mount") {
		t.Fatalf("nothing recorded, so nothing is redundant: %q", stdout)
	}
	if strings.Contains(stdout, "vault_id_collision_workspace") {
		t.Fatalf("identity reported as collision: %q", stdout)
	}
}

// TestWarrenMountModelSkewWarning (D5.1 workspace side): mounting a project
// indexed with a different embedding model warns on stderr but stays legal;
// matching models stay quiet.
func TestWarrenMountModelSkewWarning(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\nembedding_model: workspace-model\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	warrenRoot := testWarrenRoot(t, "wp", "project-a", "project-b")
	seedCLIProjectEmbeddings(t, filepath.Join(warrenRoot, "projects", "project-a", ".marmot"), "other-model")
	seedCLIProjectEmbeddings(t, filepath.Join(warrenRoot, "projects", "project-b", ".marmot"), "workspace-model")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}

	_, stderr, code := captureRunBoth(t, []string{"warren", "mount", "--dir", marmotDir, "--warren", "wp", "project-a"})
	if code != 0 {
		t.Fatalf("mount exit code = %d (warning only, mount must stay legal)", code)
	}
	if !strings.Contains(stderr, "other-model") || !strings.Contains(stderr, "workspace-model") || !strings.Contains(stderr, "no results") {
		t.Fatalf("mount stderr = %q, want model-skew warning", stderr)
	}

	_, stderr, code = captureRunBoth(t, []string{"warren", "mount", "--dir", marmotDir, "--warren", "wp", "project-b"})
	if code != 0 || strings.Contains(stderr, "no results") {
		t.Fatalf("matching-model mount = code %d stderr %q, want no warning", code, stderr)
	}
}

func seedCLIProjectEmbeddings(t *testing.T, projectMarmotDir, model string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectMarmotDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := embedding.NewStore(filepath.Join(projectMarmotDir, ".marmot-data", "embeddings.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Upsert("service/api", []float32{0.1, 0.2}, "hash", model); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

// ---------------------------------------------------------------------------
// D2 — warren status burrow cache line
// ---------------------------------------------------------------------------

// TestWarrenStatusBurrowCacheLine (D2 consumer): status renders the cache's
// provenance (date form on a non-git warren) and degrades to a stale note
// when provenance is missing.
func TestWarrenStatusBurrowCacheLine(t *testing.T) {
	marmotDir, _ := registeredWorkspace(t, "wp", "project-a")
	if code := run([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatal("burrow failed")
	}
	stdout, _, code := captureRunBoth(t, []string{"warren", "status", "--dir", marmotDir, "--warren", "wp"})
	if code != 0 || !strings.Contains(stdout, `burrow cache for "project-a": cache from `) {
		t.Fatalf("status = code %d stdout %q, want date-form cache line", code, stdout)
	}

	if err := os.Remove(filepath.Join(marmotDir, ".marmot-data", "warrens", "wp", "projects", "project-a", "provenance.md")); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = captureRunBoth(t, []string{"warren", "status", "--dir", marmotDir, "--warren", "wp"})
	if code != 0 || !strings.Contains(stdout, "no provenance recorded") {
		t.Fatalf("status without provenance = code %d stdout %q", code, stdout)
	}
}

// TestWarrenDoctorSummaryLine (U3): the text doctor report ends with a
// severity summary tail so multi-issue reports are scannable.
func TestWarrenDoctorSummaryLine(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenA := testWarrenRoot(t, "warren-a", "project-x")
	warrenB := testWarrenRoot(t, "warren-b", "project-y")
	for _, fix := range []struct{ root, project string }{{warrenA, "project-x"}, {warrenB, "project-y"}} {
		dir := filepath.Join(fix.root, "projects", fix.project, ".marmot")
		meta, body, err := warrenpkg.LoadProjectMetadata(dir)
		if err != nil {
			t.Fatal(err)
		}
		meta.VaultID = "shared-vault"
		if err := warrenpkg.SaveProjectMetadata(dir, meta, body); err != nil {
			t.Fatal(err)
		}
	}
	state := &warrenpkg.WorkspaceState{Warrens: map[string]warrenpkg.WorkspaceWarren{
		"warren-a": {Path: warrenA, ActiveProjects: []string{"project-x"}},
		"warren-b": {Path: warrenB, ActiveProjects: []string{"project-y"}},
	}}
	if err := warrenpkg.SaveWorkspaceState(workspace, state, ""); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := captureRunBoth(t, []string{"warren", "doctor", "--workspace", "--dir", marmotDir})
	if code != 1 {
		t.Fatalf("doctor --workspace exit code = %d stderr=%q", code, stderr)
	}
	if !regexp.MustCompile(`doctor: [1-9]\d* error\(s\), \d+ warning\(s\), \d+ info\n`).MatchString(stderr) {
		t.Fatalf("doctor report missing severity summary tail: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// W2 — propose push_command quoting, --json= forms, positional arity
// ---------------------------------------------------------------------------

// TestWarrenProposeShellQuoteArg: safe strings pass through untouched (so
// existing push_command output never changes) and anything else gets POSIX
// single-quoting with the '\” escape.
func TestWarrenProposeShellQuoteArg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/srv/checkouts/acme-warren", "/srv/checkouts/acme-warren"},
		{"rel/path_1.2-x:y,z@%+=", "rel/path_1.2-x:y,z@%+="},
		{"/tmp/warren dir", "'/tmp/warren dir'"},
		{"/tmp/o'brien", `'/tmp/o'\''brien'`},
		{"a;rm -rf /", "'a;rm -rf /'"},
		{"tab\there", "'tab\there'"},
		{"", "''"},
	}
	for _, tc := range cases {
		if got := shellQuoteArg(tc.in); got != tc.want {
			t.Errorf("shellQuoteArg(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestWarrenProposePushCommandQuoted: a checkout path with a space is
// shell-quoted in both the --json envelope and the human publish line.
func TestWarrenProposePushCommandQuoted(t *testing.T) {
	gitTestEnv(t)
	warrenRoot := filepath.Join(t.TempDir(), "warren dir")
	if err := os.Rename(testWarrenRoot(t, "wp", "project-a"), warrenRoot); err != nil {
		t.Fatal(err)
	}
	gitInitCommit(t, warrenRoot)
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}
	if code := run([]string{"warren", "edit", "--dir", marmotDir, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatal("edit failed")
	}
	if err := os.WriteFile(filepath.Join(warrenRoot, "projects", "project-a", ".marmot", "notes.md"), []byte("edit one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "--json"})
	if code != 0 {
		t.Fatalf("propose --json = code %d stdout %q", code, stdout)
	}
	env := decodeProposeEnvelope(t, stdout)
	wantPush := "git -C '" + warrenRoot + "' push -u origin " + env.Branch
	if env.PushCommand != wantPush {
		t.Fatalf("push_command = %q, want %q", env.PushCommand, wantPush)
	}

	// Human mode: same quoting on the publish line. Drop the JSON run's branch
	// first — both runs can land in the same timestamp second, and propose
	// refuses an existing branch name.
	gitRun(t, warrenRoot, "branch", "-D", env.Branch)
	if err := os.WriteFile(filepath.Join(warrenRoot, "projects", "project-a", ".marmot", "notes.md"), []byte("edit two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp"})
	if code != 0 || !strings.Contains(stdout, "git -C '"+warrenRoot+"' push -u origin ") {
		t.Fatalf("human propose = code %d stdout %q, want quoted push command", code, stdout)
	}
}

// TestProposeJSONRequestedForms: the raw-arg scan honors every boolean spelling
// flag itself accepts (--json=TRUE, --json=1, ...), ignores false/unparseable
// values, and never reads past a bare "--" terminator.
func TestProposeJSONRequestedForms(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--json"}, true},
		{[]string{"-json"}, true},
		{[]string{"--json=true"}, true},
		{[]string{"--json=TRUE"}, true},
		{[]string{"--json=1"}, true},
		{[]string{"-json=T"}, true},
		{[]string{"--json=false"}, false},
		{[]string{"--json=0"}, false},
		{[]string{"--json=banana"}, false},
		{[]string{"--jsonx"}, false},
		{[]string{"--", "--json"}, false},
		{[]string{"--bogus", "--json"}, true},
		{[]string{}, false},
	}
	for _, tc := range cases {
		if got := proposeJSONRequested(tc.args); got != tc.want {
			t.Errorf("proposeJSONRequested(%v) = %t, want %t", tc.args, got, tc.want)
		}
	}
}

// TestWarrenProposeExtraPositionals: propose takes at most one positional
// (the project ID); extras are refused before any state or git access, in
// both human and JSON modes.
func TestWarrenProposeExtraPositionals(t *testing.T) {
	_, stderr, code := captureRunBoth(t, []string{"warren", "propose", "project-a", "project-b"})
	if code != 1 || !strings.Contains(stderr, "usage: marmot warren propose") {
		t.Fatalf("human extra positionals = code %d stderr %q, want usage refusal", code, stderr)
	}
	stdout, _, code := captureRunBoth(t, []string{"warren", "propose", "--json", "project-a", "project-b"})
	if code != 1 {
		t.Fatalf("json extra positionals = code %d stdout %q", code, stdout)
	}
	env := decodeProposeEnvelope(t, stdout)
	if env.Error == nil || env.Error.Code != "invalid_args" {
		t.Fatalf("error envelope = %+v, want invalid_args", env.Error)
	}
}
