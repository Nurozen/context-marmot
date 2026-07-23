package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/home"
	nodepkg "github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/routes"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

func hermeticDenCLI(t *testing.T) (homeRoot string) {
	t.Helper()
	homeRoot = t.TempDir()
	home.SetOverride(homeRoot)
	t.Cleanup(func() { home.SetOverride("") })
	t.Setenv("MARMOT_HOME", homeRoot)
	routes.SetOverridePath(filepath.Join(homeRoot, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })
	return homeRoot
}

func TestDenContributeEditLinkRequired(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("contrib-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "contribute", "contrib-den", "--json"})
	if code == 0 {
		t.Fatalf("expected failure, out=%s", out)
	}
	if !strings.Contains(out, "edit_link_required") {
		t.Fatalf("expected edit_link_required, got %s", out)
	}

	// With explicit link ref still fails without edit mode.
	out, code = captureRun([]string{"den", "contribute", "contrib-den", "w/p", "--json"})
	if code == 0 || !strings.Contains(out, "edit_link_required") {
		t.Fatalf("link ref path: code=%d out=%s", code, out)
	}

	// Missing den-id
	out, code = captureRun([]string{"den", "contribute", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("missing id: code=%d out=%s", code, out)
	}

	// Missing den
	out, code = captureRun([]string{"den", "contribute", "no-such", "--json"})
	if code == 0 || !strings.Contains(out, "den_not_found") {
		t.Fatalf("missing den: code=%d out=%s", code, out)
	}

	// Plain text path (no --json)
	if code := run([]string{"den", "contribute", "contrib-den"}); code == 0 {
		t.Fatal("plain contribute should fail")
	}
}

// TestDenContributeLinkUnresolved: an edit link whose warren was never
// registered/mounted in the den vault resolves to no active editable mount.
func TestDenContributeLinkUnresolved(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("edit-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	m, body, err := den.LoadManifest("edit-den")
	if err != nil {
		t.Fatal(err)
	}
	m.Links = []den.Link{{Target: "w/p", Mode: "edit", Warren: "w", Project: "p"}}
	if err := den.SaveManifest("edit-den", m, body); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{"den", "contribute", "edit-den", "w/p", "--json"})
	if code == 0 || !strings.Contains(out, "link_unresolved") {
		t.Fatalf("link_unresolved: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "marmot den link edit-den --edit w/p") {
		t.Fatalf("expected re-link hint: %s", out)
	}
	// Plain-text path.
	if code := run([]string{"den", "contribute", "edit-den"}); code == 0 {
		t.Fatal("plain link_unresolved should fail")
	}
}

// gitCLI runs git in dir for test fixtures, failing the test on error.
func gitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOutput(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return out
}

// denContributeFixture: den + git-initialized warren checkout + edit link.
func denContributeFixture(t *testing.T, denID, warrenID, projectID string) (vaultDir, warrenRoot string) {
	t.Helper()
	vaultDir, warrenRoot = denLinkFixture(t, denID, warrenID, projectID)
	gitCLI(t, warrenRoot, "init", "-q")
	gitCLI(t, warrenRoot, "checkout", "-q", "-b", "main")
	gitCLI(t, warrenRoot, "config", "user.email", "test@example.com")
	gitCLI(t, warrenRoot, "config", "user.name", "Test")
	gitCLI(t, warrenRoot, "add", "-A")
	gitCLI(t, warrenRoot, "commit", "-q", "-m", "init warren")
	if out, code := captureRun([]string{"den", "link", denID, "--edit", warrenID + "/" + projectID, "--json"}); code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}
	return vaultDir, warrenRoot
}

// writeDenNode writes a node markdown file into the den's identity vault.
func writeDenNode(t *testing.T, vaultDir, id, summary, context string) {
	t.Helper()
	st := nodepkg.NewStore(vaultDir)
	n := &nodepkg.Node{ID: id, Type: "concept", Namespace: "default", Status: nodepkg.StatusActive, Summary: summary, Context: context}
	if err := st.SaveNode(n); err != nil {
		t.Fatalf("SaveNode %s: %v", id, err)
	}
}

type denContributeEnvelope struct {
	Schema int    `json:"schema"`
	DenID  string `json:"den_id"`
	Link   struct {
		Target  string `json:"target"`
		Mode    string `json:"mode"`
		Warren  string `json:"warren"`
		Project string `json:"project"`
	} `json:"link"`
	Branch      string `json:"branch"`
	Commit      string `json:"commit"`
	Committed   bool   `json:"committed"`
	Checkout    string `json:"checkout"`
	PushCommand string `json:"push_command"`
	Contributed struct {
		Added      int `json:"added"`
		Updated    int `json:"updated"`
		Superseded int `json:"superseded"`
		Noop       int `json:"noop"`
	} `json:"contributed"`
	Warnings []string `json:"warnings"`
}

func parseContributeEnvelope(t *testing.T, out string) denContributeEnvelope {
	t.Helper()
	var env denContributeEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("contribute envelope parse: %v out=%s", err, out)
	}
	return env
}

func TestDenContributeEndToEnd(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, warrenRoot := denContributeFixture(t, "c-den", "w", "p")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "Alpha context")
	writeDenNode(t, vaultDir, "notes/beta", "Beta summary", "")
	const branch = "marmot/edit/c-den/p"

	out, code := captureRun([]string{"den", "contribute", "c-den", "--json"})
	if code != 0 {
		t.Fatalf("contribute: code=%d out=%s", code, out)
	}
	env := parseContributeEnvelope(t, out)
	if env.Schema != 1 || env.DenID != "c-den" {
		t.Fatalf("envelope head: %+v", env)
	}
	if env.Link.Target != "w/p" || env.Link.Mode != "edit" || env.Link.Warren != "w" || env.Link.Project != "p" {
		t.Fatalf("link body: %+v", env.Link)
	}
	if env.Branch != branch || !env.Committed || env.Commit == "" {
		t.Fatalf("branch/commit: %+v", env)
	}
	if env.Contributed.Added != 2 || env.Contributed.Updated != 0 || env.Contributed.Superseded != 0 || env.Contributed.Noop != 0 {
		t.Fatalf("counts: %+v", env.Contributed)
	}
	if env.Warnings == nil {
		t.Fatalf("warnings must be present (even empty): %s", out)
	}
	// Terminal packaging verb: a committed contribute hands over the
	// publishing data itself (warren propose would see a clean tree).
	if env.Checkout != warrenRoot {
		t.Fatalf("checkout = %q, want %q", env.Checkout, warrenRoot)
	}
	if !strings.Contains(env.PushCommand, warrenRoot) || !strings.HasSuffix(env.PushCommand, "push -u origin "+branch) {
		t.Fatalf("push_command = %q", env.PushCommand)
	}

	// prevBranch restored; edit branch exists with exactly one new commit.
	if head := gitCLI(t, warrenRoot, "symbolic-ref", "--short", "HEAD"); head != "main" {
		t.Fatalf("HEAD = %q, want main", head)
	}
	if n := gitCLI(t, warrenRoot, "rev-list", "--count", "main.."+branch); n != "1" {
		t.Fatalf("rev-list count = %s, want 1", n)
	}
	// Committed files: the node files, never .marmot-data sidecars.
	files := gitCLI(t, warrenRoot, "diff", "--name-only", "main", branch)
	if !strings.Contains(files, "projects/p/.marmot/notes/alpha.md") || !strings.Contains(files, "projects/p/.marmot/notes/beta.md") {
		t.Fatalf("committed files: %s", files)
	}
	if strings.Contains(files, ".marmot-data") {
		t.Fatalf(".marmot-data leaked into the commit: %s", files)
	}
	// The commit sha in the envelope is the branch tip.
	if tip := gitCLI(t, warrenRoot, "rev-parse", branch); tip != env.Commit {
		t.Fatalf("commit sha %q != branch tip %q", env.Commit, tip)
	}

	// Idempotency: second contribute is all NOOP, commits nothing.
	out, code = captureRun([]string{"den", "contribute", "c-den", "--json"})
	if code != 0 {
		t.Fatalf("second contribute: code=%d out=%s", code, out)
	}
	env = parseContributeEnvelope(t, out)
	if env.Committed || env.Commit != "" {
		t.Fatalf("second run committed: %+v", env)
	}
	if env.Contributed.Noop != 2 || env.Contributed.Added != 0 || env.Contributed.Updated != 0 {
		t.Fatalf("second run counts: %+v", env.Contributed)
	}
	if env.Checkout != "" || env.PushCommand != "" {
		t.Fatalf("uncommitted run must not carry publish data: %+v", env)
	}
	if n := gitCLI(t, warrenRoot, "rev-list", "--count", "main.."+branch); n != "1" {
		t.Fatalf("second run added a commit: rev-list count = %s", n)
	}

	// Dry-run with the edit branch in existence discloses the divergence:
	// the real run plans against the branch, the dry-run against this tree.
	out, code = captureRun([]string{"den", "contribute", "c-den", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("dry-run after commit: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "edit branch "+branch+" exists") {
		t.Fatalf("dry-run must disclose the existing edit branch: %s", out)
	}

	// Update path: modify one den node -> one update, one noop, new commit
	// appended to the SAME branch.
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary v2", "Alpha context v2")
	out, code = captureRun([]string{"den", "contribute", "c-den", "--json"})
	if code != 0 {
		t.Fatalf("third contribute: code=%d out=%s", code, out)
	}
	env = parseContributeEnvelope(t, out)
	if !env.Committed || env.Contributed.Updated != 1 || env.Contributed.Noop != 1 || env.Contributed.Added != 0 {
		t.Fatalf("third run: %+v", env)
	}
	if n := gitCLI(t, warrenRoot, "rev-list", "--count", "main.."+branch); n != "2" {
		t.Fatalf("expected 2 commits on the edit branch, got %s", n)
	}
	if head := gitCLI(t, warrenRoot, "symbolic-ref", "--short", "HEAD"); head != "main" {
		t.Fatalf("HEAD after third run = %q, want main", head)
	}
}

func TestDenContributeZeroChangesNoBranch(t *testing.T) {
	hermeticDenCLI(t)
	_, warrenRoot := denContributeFixture(t, "empty-den", "w", "p")

	// Empty den vault: success, committed:false, and NO branch created.
	out, code := captureRun([]string{"den", "contribute", "empty-den", "--json"})
	if code != 0 {
		t.Fatalf("empty contribute: code=%d out=%s", code, out)
	}
	env := parseContributeEnvelope(t, out)
	if env.Committed || env.Commit != "" || env.Contributed != (denContributeEnvelope{}).Contributed {
		t.Fatalf("empty contribute env: %+v", env)
	}
	if _, err := gitOutput(warrenRoot, "rev-parse", "--verify", "--quiet", "refs/heads/marmot/edit/empty-den/p"); err == nil {
		t.Fatal("zero-change contribute must not create the edit branch")
	}
}

func TestDenContributeDryRunTouchesNothing(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, warrenRoot := denContributeFixture(t, "dr-den", "w", "p")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	before := gitCLI(t, warrenRoot, "status", "--porcelain")

	out, code := captureRun([]string{"den", "contribute", "dr-den", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("dry-run: code=%d out=%s", code, out)
	}
	var env struct {
		Schema int      `json:"schema"`
		DryRun bool     `json:"dry_run"`
		Ops    []string `json:"ops"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("dry-run envelope: %v out=%s", err, out)
	}
	if env.Schema != 1 || !env.DryRun {
		t.Fatalf("dry-run env: %+v", env)
	}
	wantOps := map[string]bool{"add node notes/alpha": false, "commit on marmot/edit/dr-den/p": false}
	for _, op := range env.Ops {
		if _, ok := wantOps[op]; ok {
			wantOps[op] = true
		}
	}
	for op, seen := range wantOps {
		if !seen {
			t.Fatalf("missing op %q in %+v", op, env.Ops)
		}
	}
	// Nothing touched: no target node file, no branch, git status unchanged.
	if _, err := os.Stat(filepath.Join(warrenRoot, "projects", "p", ".marmot", "notes", "alpha.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote target node (stat err=%v)", err)
	}
	if _, err := gitOutput(warrenRoot, "rev-parse", "--verify", "--quiet", "refs/heads/marmot/edit/dr-den/p"); err == nil {
		t.Fatal("dry-run must not create the edit branch")
	}
	if after := gitCLI(t, warrenRoot, "status", "--porcelain"); after != before {
		t.Fatalf("dry-run dirtied the checkout: before=%q after=%q", before, after)
	}

	// Plain dry-run prints dry-run: lines.
	out, code = captureRun([]string{"den", "contribute", "dr-den", "--dry-run"})
	if code != 0 || !strings.Contains(out, "dry-run: add node notes/alpha") {
		t.Fatalf("plain dry-run: code=%d out=%s", code, out)
	}
}

func TestDenContributeReadonlyRefused(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, warrenRoot := denContributeFixture(t, "ro-den2", "w", "p")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	if _, err := warrenpkg.SetProjectReadOnly(warrenRoot, "p", true); err != nil {
		t.Fatalf("SetProjectReadOnly: %v", err)
	}

	out, code := captureRun([]string{"den", "contribute", "ro-den2", "--json"})
	if code == 0 || !strings.Contains(out, "readonly_refused") {
		t.Fatalf("readonly refusal: code=%d out=%s", code, out)
	}
	// No branch, no writes.
	if _, err := gitOutput(warrenRoot, "rev-parse", "--verify", "--quiet", "refs/heads/marmot/edit/ro-den2/p"); err == nil {
		t.Fatal("readonly refusal must not create the edit branch")
	}
	if _, err := os.Stat(filepath.Join(warrenRoot, "projects", "p", ".marmot", "notes", "alpha.md")); !os.IsNotExist(err) {
		t.Fatalf("readonly refusal wrote target node (stat err=%v)", err)
	}
}

func TestDenContributeWarrenUnreachable(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, warrenRoot := denContributeFixture(t, "gone-den", "w", "p")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	if err := os.RemoveAll(warrenRoot); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "contribute", "gone-den", "--json"})
	if code == 0 || !strings.Contains(out, "warren_unreachable") {
		t.Fatalf("warren_unreachable: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "marmot warren register w") {
		t.Fatalf("expected re-register hint: %s", out)
	}
}

// TestDenContributeDetachedHead: a detached-HEAD warren checkout refuses
// with the documented code before any branch or write happens.
func TestDenContributeDetachedHead(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, warrenRoot := denContributeFixture(t, "det-den", "w", "p")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	gitCLI(t, warrenRoot, "checkout", "-q", "--detach")

	out, code := captureRun([]string{"den", "contribute", "det-den", "--json"})
	if code == 0 || !strings.Contains(out, "detached_head") {
		t.Fatalf("detached_head: code=%d out=%s", code, out)
	}
	// No edit branch created, no node written.
	if _, err := gitOutput(warrenRoot, "rev-parse", "--verify", "--quiet", "refs/heads/marmot/edit/det-den/p"); err == nil {
		t.Fatal("detached refusal must not create the edit branch")
	}
	if _, err := os.Stat(filepath.Join(warrenRoot, "projects", "p", ".marmot", "notes", "alpha.md")); !os.IsNotExist(err) {
		t.Fatalf("detached refusal wrote target node (stat err=%v)", err)
	}
}

func TestDenContributePlainTextSuccess(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, warrenRoot := denContributeFixture(t, "plain-c", "w", "p")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")

	out, code := captureRun([]string{"den", "contribute", "plain-c"})
	if code != 0 {
		t.Fatalf("plain contribute: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "+1 added") || !strings.Contains(out, "marmot/edit/plain-c/p") {
		t.Fatalf("plain output: %s", out)
	}
	if !strings.Contains(out, "git -C "+warrenRoot+" push -u origin marmot/edit/plain-c/p") {
		t.Fatalf("expected push command: %s", out)
	}
}

// TestDenContributeDirtyCheckoutRefused: pre-existing user work under the
// project path refuses with checkout_dirty BEFORE any engine write or git
// branch op — a pathspec-limited `git add` would otherwise sweep it into the
// contribute commit and the checkout-back would drop it from the tree.
func TestDenContributeDirtyCheckoutRefused(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, warrenRoot := denContributeFixture(t, "dirty-den", "w", "p")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	// Pre-existing untracked user file inside the project scope.
	stray := filepath.Join(warrenRoot, "projects", "p", ".marmot", "user-notes.md")
	if err := os.WriteFile(stray, []byte("precious uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{"den", "contribute", "dirty-den", "--json"})
	if code == 0 || !strings.Contains(out, "checkout_dirty") {
		t.Fatalf("dirty refusal: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "warren propose") {
		t.Fatalf("expected propose hint: %s", out)
	}
	// The refusal happened before any mutation: user file intact, no edit
	// branch, no engine-written node.
	if data, err := os.ReadFile(stray); err != nil || string(data) != "precious uncommitted work\n" {
		t.Fatalf("user file mutated: %v %q", err, data)
	}
	if _, err := gitOutput(warrenRoot, "rev-parse", "--verify", "--quiet", "refs/heads/marmot/edit/dirty-den/p"); err == nil {
		t.Fatal("dirty refusal must not create the edit branch")
	}
	if _, err := os.Stat(filepath.Join(warrenRoot, "projects", "p", ".marmot", "notes", "alpha.md")); !os.IsNotExist(err) {
		t.Fatal("dirty refusal must not write engine nodes")
	}

	// Dry-run refuses under the identical rule (its plan would otherwise
	// judge a tree the real run refuses to touch).
	out, code = captureRun([]string{"den", "contribute", "dirty-den", "--dry-run", "--json"})
	if code == 0 || !strings.Contains(out, "checkout_dirty") {
		t.Fatalf("dry-run dirty refusal: code=%d out=%s", code, out)
	}

	// Cleaning the stray file unblocks; .marmot-data sidecar noise is
	// excluded from the dirty scope and never refuses.
	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(warrenRoot, "projects", "p", ".marmot", ".marmot-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "embeddings.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code = captureRun([]string{"den", "contribute", "dirty-den", "--json"})
	if code != 0 {
		t.Fatalf("contribute with only .marmot-data noise: code=%d out=%s", code, out)
	}
	if env := parseContributeEnvelope(t, out); !env.Committed || env.Contributed.Added != 1 {
		t.Fatalf("unblocked contribute: %+v", env)
	}
}

// TestDenContributeCommitFailureRecovery: a failing commit (pre-commit hook)
// must not poison retries — recovery removes the engine-written files,
// deletes the edit branch this run created, and returns to the previous
// branch, so the retry performs a REAL contribute instead of NOOPing against
// leftovers while the knowledge is committed nowhere.
func TestDenContributeCommitFailureRecovery(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, warrenRoot := denContributeFixture(t, "hook-den", "w", "p")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "Alpha context")
	const branch = "marmot/edit/hook-den/p"

	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, warrenRoot, "config", "core.hooksPath", hooks)

	out, code := captureRun([]string{"den", "contribute", "hook-den", "--json"})
	if code == 0 || !strings.Contains(out, "contribute_failed") {
		t.Fatalf("hooked contribute: code=%d out=%s", code, out)
	}
	// Recovery invariants: back on main, tree clean, edit branch gone.
	if head := gitCLI(t, warrenRoot, "symbolic-ref", "--short", "HEAD"); head != "main" {
		t.Fatalf("HEAD after failure = %q, want main", head)
	}
	if dirty := gitCLI(t, warrenRoot, "status", "--porcelain"); dirty != "" {
		t.Fatalf("tree not clean after recovery: %q", dirty)
	}
	if _, err := gitOutput(warrenRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		t.Fatal("failed run's edit branch must be deleted")
	}
	if _, err := os.Stat(filepath.Join(warrenRoot, "projects", "p", ".marmot", "notes", "alpha.md")); !os.IsNotExist(err) {
		t.Fatalf("engine-written file survived recovery (stat err=%v)", err)
	}

	// Retry after fixing the cause: a REAL contribute, not an all-noop.
	gitCLI(t, warrenRoot, "config", "--unset", "core.hooksPath")
	out, code = captureRun([]string{"den", "contribute", "hook-den", "--json"})
	if code != 0 {
		t.Fatalf("retry: code=%d out=%s", code, out)
	}
	env := parseContributeEnvelope(t, out)
	if !env.Committed || env.Contributed.Added != 1 || env.Contributed.Noop != 0 {
		t.Fatalf("retry must be a real contribute: %+v", env)
	}
	if n := gitCLI(t, warrenRoot, "rev-list", "--count", "main.."+branch); n != "1" {
		t.Fatalf("retry commit count = %s, want 1", n)
	}
}

// TestDenContributeInvalidBranchComponent: a link whose project id cannot
// form a safe git ref component is refused with a structured code before any
// git operation.
func TestDenContributeInvalidBranchComponent(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("refsafe-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	m, body, err := den.LoadManifest("refsafe-den")
	if err != nil {
		t.Fatal(err)
	}
	m.Links = []den.Link{{Target: "w/p..evil", Mode: "edit", Warren: "w", Project: "p..evil"}}
	if err := den.SaveManifest("refsafe-den", m, body); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "contribute", "refsafe-den", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_branch_component") {
		t.Fatalf("hostile project id: code=%d out=%s", code, out)
	}
	// Plain-text path.
	if code := run([]string{"den", "contribute", "refsafe-den"}); code == 0 {
		t.Fatal("plain hostile id should fail")
	}
}

func TestIsGitRefComponentSafe(t *testing.T) {
	for _, ok := range []string{"demo", "project-a", "A.b_c-9", "v1.2.3"} {
		if !isGitRefComponentSafe(ok) {
			t.Errorf("%q should be safe", ok)
		}
	}
	for _, bad := range []string{
		"", ".hidden", "-flag", "a..b", "a b", "a:b", "a~b", "a^b", "a?b",
		"a*b", "a[b", `a"b`, "a/b", "a\\b", "trailing.", "name.lock", "é",
	} {
		if isGitRefComponentSafe(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestShellQuoteArg(t *testing.T) {
	cases := map[string]string{
		"/plain/path":   "/plain/path",
		"/has space/x":  "'/has space/x'",
		"/it's/here":    `'/it'\''s/here'`,
		"/dollar/$HOME": "'/dollar/$HOME'",
		"":              "''",
		"/safe:colon/y": "/safe:colon/y",
	}
	for in, want := range cases {
		if got := shellQuoteArg(in); got != want {
			t.Errorf("shellQuoteArg(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDenContributeAndLinkExactArity: extra positionals are structured
// refusals, and the --json=<value> spellings reach the parse-fail envelope.
func TestDenContributeAndLinkExactArity(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("arity-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{"den", "contribute", "arity-den", "w/p", "extra", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("contribute extra positional: code=%d out=%s", code, out)
	}
	if code := run([]string{"den", "contribute", "arity-den", "w/p", "extra"}); code == 0 {
		t.Fatal("plain contribute extra positional should fail")
	}

	out, code = captureRun([]string{"den", "link", "arity-den", "extra", "--edit", "w/p", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("link extra positional: code=%d out=%s", code, out)
	}

	// --json=TRUE / --json=1 on the flag-parse-failure path still honor the
	// stdout envelope contract (strconv.ParseBool spellings).
	for _, jsonForm := range []string{"--json=TRUE", "--json=1"} {
		out, code = captureRun([]string{"den", "contribute", "--bogus", jsonForm})
		if code != 1 || !strings.Contains(out, "invalid_args") {
			t.Fatalf("parse-fail with %s: code=%d out=%s", jsonForm, code, out)
		}
	}
	// --json=false must NOT emit an envelope.
	out, code = captureRun([]string{"den", "contribute", "--bogus", "--json=false"})
	if code != 1 || strings.Contains(out, "invalid_args") {
		t.Fatalf("--json=false must stay plain: code=%d out=%s", code, out)
	}
}

func TestDenAdoptCLI(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()
	seedAdoptProjectVault(t, proj)

	out, code := captureRun([]string{
		"den", "adopt", "--from", proj, "--id", "adopted-cli", "--dry-run", "--json",
	})
	if code != 0 {
		t.Fatalf("adopt dry-run: %d %s", code, out)
	}
	if !strings.Contains(out, `"dry_run"`) {
		t.Fatalf("envelope: %s", out)
	}

	out, code = captureRun([]string{
		"den", "adopt", "--from", proj, "--id", "adopted-cli", "--json",
	})
	if code != 0 {
		t.Fatalf("adopt: %d %s", code, out)
	}
	if !strings.Contains(out, `"den_id"`) || !strings.Contains(out, "adopted-cli") {
		t.Fatalf("adopt envelope: %s", out)
	}
	// Plain adopt second id
	proj2 := t.TempDir()
	seedAdoptProjectVault(t, proj2)
	if code := run([]string{"den", "adopt", "--from", proj2, "--id", "plain-adopt"}); code != 0 {
		t.Fatalf("plain adopt: %d", code)
	}
	// Failure path: same id again (den + vault already exist).
	proj3 := t.TempDir()
	seedAdoptProjectVault(t, proj3)
	out, code = captureRun([]string{"den", "adopt", "--from", proj3, "--id", "adopted-cli", "--json"})
	if code == 0 {
		t.Fatalf("duplicate adopt should fail: %s", out)
	}
	if !strings.Contains(out, "den_vault_exists") {
		t.Fatalf("error code: %s", out)
	}
}

func TestDenCreateStatusDestroyBranches(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()

	// plain create (no json)
	if code := run([]string{
		"den", "create", "plain-den",
		"--lifetime", "task",
		"--project", proj,
		"--no-vault",
	}); code != 0 {
		t.Fatalf("plain create: %d", code)
	}

	// status plain
	out, code := captureRun([]string{"den", "status", "plain-den"})
	if code != 0 || !strings.Contains(out, "plain-den") {
		t.Fatalf("status plain: %d %s", code, out)
	}

	// list --json
	out, code = captureRun([]string{"den", "list", "--json"})
	if code != 0 {
		t.Fatalf("list json: %d %s", code, out)
	}
	var listEnv struct {
		Schema int      `json:"schema"`
		Dens   []string `json:"dens"`
	}
	if err := json.Unmarshal([]byte(out), &listEnv); err != nil {
		t.Fatalf("list json parse: %v out=%s", err, out)
	}
	if listEnv.Schema != 1 || len(listEnv.Dens) == 0 {
		t.Fatalf("list env: %+v", listEnv)
	}

	// destroy dry-run
	out, code = captureRun([]string{"den", "destroy", "plain-den", "--dry-run", "--json"})
	if code != 0 || !strings.Contains(out, "dry_run") {
		t.Fatalf("destroy dry-run: %d %s", code, out)
	}

	// destroy plain
	if code := run([]string{"den", "destroy", "plain-den"}); code != 0 {
		t.Fatalf("destroy plain: %d", code)
	}

	// create missing den-id
	out, code = captureRun([]string{"den", "create", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("missing id: %d %s", code, out)
	}

	// destroy missing
	out, code = captureRun([]string{"den", "destroy", "gone", "--json"})
	if code == 0 || !strings.Contains(out, "den_not_found") {
		t.Fatalf("destroy missing: %d %s", code, out)
	}

	// destroy dry-run missing
	out, code = captureRun([]string{"den", "destroy", "gone", "--dry-run", "--json"})
	if code == 0 {
		t.Fatalf("destroy dry-run missing should fail: %s", out)
	}

	// create failure duplicate via json
	if code := run([]string{"den", "create", "dup-cli", "--no-pointer", "--project", t.TempDir()}); code != 0 {
		t.Fatal(code)
	}
	out, code = captureRun([]string{"den", "create", "dup-cli", "--json", "--project", t.TempDir()})
	if code == 0 || !strings.Contains(out, "den_create_failed") {
		t.Fatalf("dup create: %d %s", code, out)
	}

	// unknown subcommand
	if code := run([]string{"den", "bogus"}); code == 0 {
		t.Fatal("unknown subcommand")
	}

	// empty list plain
	// destroy remaining dens first
	for _, id := range []string{"dup-cli"} {
		_ = run([]string{"den", "destroy", id})
	}
	out, code = captureRun([]string{"den", "list"})
	if code != 0 {
		t.Fatal(code)
	}
	// Hermetic MARMOT_HOME: after destroying throwaway dens the list is empty.
	if !strings.Contains(out, "No dens") {
		t.Fatalf("den list after cleanup = %q, want empty-state message", out)
	}
}

func TestDenStatusFromCWD(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()
	if _, err := den.Create("cwd-den", den.CreateOptions{
		Lifetime: den.LifetimeTask,
		Projects: []string{proj},
	}); err != nil {
		t.Fatal(err)
	}
	// Chdir into project so status without id resolves via pointer.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	out, code := captureRun([]string{"den", "status", "--json"})
	if code != 0 {
		t.Fatalf("status from cwd: %d %s", code, out)
	}
	if !strings.Contains(out, "cwd-den") {
		t.Fatalf("expected cwd-den: %s", out)
	}
}

func TestDenStatusNoResolution(t *testing.T) {
	hermeticDenCLI(t)
	// Chdir to empty temp with no pointer/route.
	empty := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	out, code := captureRun([]string{"den", "status", "--json"})
	if code == 0 {
		t.Fatalf("expected fail: %s", out)
	}
	if !strings.Contains(out, "den_not_found") {
		t.Fatalf("envelope: %s", out)
	}
}

func TestDenCreateNoVaultPlain(t *testing.T) {
	hermeticDenCLI(t)
	out, code := captureRun([]string{
		"den", "create", "links-only-cli",
		"--no-vault", "--no-pointer",
		"--project", t.TempDir(),
	})
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	if !strings.Contains(out, "links-only") && !strings.Contains(out, "Created den") {
		t.Fatalf("output: %s", out)
	}
}

// TestDenParseFailJSONEnvelope: every den verb honors the --json stdout
// contract on the flag-parse-failure path (denParseFail scans the raw args,
// since the parsed --json value never gets set when Parse errors): a bad
// flag plus --json yields a schema:1 error envelope with code invalid_args.
func TestDenParseFailJSONEnvelope(t *testing.T) {
	hermeticDenCLI(t)
	for _, verb := range []string{"create", "status", "destroy", "list", "adopt", "link", "contribute"} {
		t.Run(verb, func(t *testing.T) {
			out, code := captureRun([]string{"den", verb, "--bogus", "--json"})
			if code != 1 {
				t.Fatalf("den %s --bogus --json = code %d stdout %q", verb, code, out)
			}
			var env struct {
				Schema int `json:"schema"`
				Error  struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(out), &env); err != nil {
				t.Fatalf("den %s parse-fail stdout is not a JSON envelope: %v (%q)", verb, err, out)
			}
			if env.Schema != 1 || env.Error.Code != "invalid_args" || env.Error.Message == "" {
				t.Fatalf("den %s envelope = %+v, want schema:1 invalid_args", verb, env)
			}

			// Without --json the contract is unchanged: bare exit 1, no stdout envelope.
			out, code = captureRun([]string{"den", verb, "--bogus"})
			if code != 1 || strings.Contains(out, "invalid_args") {
				t.Fatalf("den %s --bogus (no --json) = code %d stdout %q", verb, code, out)
			}
		})
	}
}
