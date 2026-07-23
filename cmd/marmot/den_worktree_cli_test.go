package main

// CLI tests for cache-backed den edit links (plan §5.4/§15.3): `den link
// --edit` against a warren added via `warren add` creates a dedicated edit
// worktree at $MARMOT_HOME/warren-cache/edits/<warren>/<den> on branch
// marmot/edit/<den>/<warren>, contribute commits there without touching the
// shared read checkout or any user clone, MCP-style writes auto-commit, and
// destroy refuses on unpushed edits (worktree removed, branch preserved).
// Hermetic: temp MARMOT_HOME + local git "remote", no network.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	nodepkg "github.com/nurozen/context-marmot/internal/node"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// denWorktreeLinkEnvelope is denLinkEnvelope plus the additive cache-backed
// fields (worktree, branch).
type denWorktreeLinkEnvelope struct {
	Schema int    `json:"schema"`
	DenID  string `json:"den_id"`
	Link   struct {
		Target  string `json:"target"`
		Mode    string `json:"mode"`
		Warren  string `json:"warren"`
		Project string `json:"project"`
	} `json:"link"`
	Worktree string   `json:"worktree"`
	Branch   string   `json:"branch"`
	Warnings []string `json:"warnings"`
}

// cacheEditFixture: cache-backed warren (local git remote + `warren add`) and
// a den with an identity vault, linked --edit to projectID. Returns the den
// vault dir, the local remote path, and the edit worktree path.
func cacheEditFixture(t *testing.T, denID, warrenID, projectID string, extraProjects ...string) (vaultDir, remote, worktree string) {
	t.Helper()
	remote = cacheRemoteWarren(t, warrenID, append([]string{projectID}, extraProjects...)...)
	if out, code := captureRun([]string{"warren", "add", remote, "--id", warrenID, "--json"}); code != 0 {
		t.Fatalf("warren add: code=%d out=%s", code, out)
	}
	if _, err := den.Create(denID, den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatalf("den.Create: %v", err)
	}
	out, code := captureRun([]string{"den", "link", denID, "--edit", warrenID + "/" + projectID, "--json"})
	if code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}
	return den.VaultPath(denID), remote, warrenpkg.CacheEditWorktreePath(warrenID, denID)
}

func TestDenLinkEditCacheBackedCreatesWorktree(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "cachew", "project-a", "project-b")
	remoteHead := gitCLI(t, remote, "rev-parse", "HEAD")
	if out, code := captureRun([]string{"warren", "add", remote, "--id", "cachew", "--json"}); code != 0 {
		t.Fatalf("warren add: code=%d out=%s", code, out)
	}
	if _, err := den.Create("wt-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{"den", "link", "wt-den", "--edit", "cachew/project-a", "--json"})
	if code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}
	var env denWorktreeLinkEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v out=%s", err, out)
	}
	worktree := filepath.Join(homeRoot, "warren-cache", "edits", "cachew", "wt-den")
	const branch = "marmot/edit/wt-den/cachew"
	if env.Worktree != worktree || env.Branch != branch {
		t.Fatalf("envelope worktree/branch: %+v (want %s / %s)", env, worktree, branch)
	}
	if env.Worktree != warrenpkg.CacheEditWorktreePath("cachew", "wt-den") {
		t.Fatalf("worktree path derivation mismatch: %q", env.Worktree)
	}

	// The worktree exists, sits permanently on the edit branch, and forked
	// from the shared checkout's pin.
	if head := gitCLI(t, worktree, "symbolic-ref", "--short", "HEAD"); head != branch {
		t.Fatalf("worktree HEAD = %q, want %q", head, branch)
	}
	if commit := gitCLI(t, worktree, "rev-parse", "HEAD"); commit != remoteHead {
		t.Fatalf("worktree pinned at %q, want cache pin %q", commit, remoteHead)
	}
	bare := filepath.Join(homeRoot, "warren-cache", "cachew.git")
	gitCLI(t, bare, "show-ref", "--verify", "refs/heads/"+branch) // branch lives in the bare repo

	// The den vault's warren state routes through the worktree — the whole
	// point: MCP writes and contribute never touch the shared checkout.
	state, _, err := warrenpkg.LoadWorkspaceStateFromMarmot(den.VaultPath("wt-den"))
	if err != nil {
		t.Fatal(err)
	}
	entry := state.Warrens["cachew"]
	if entry.Path != worktree {
		t.Fatalf("state entry.Path = %q, want worktree %q", entry.Path, worktree)
	}
	mounts, err := warrenpkg.ActiveMounts(den.VaultPath("wt-den"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].WarrenPath != worktree || !mounts[0].Editable || !strings.HasPrefix(mounts[0].Path, worktree) {
		t.Fatalf("mounts = %+v", mounts)
	}

	// Second project of the same warren: same den, same worktree, both active.
	out, code = captureRun([]string{"den", "link", "wt-den", "--edit", "cachew/project-b", "--json"})
	if code != 0 {
		t.Fatalf("second link: code=%d out=%s", code, out)
	}
	env = denWorktreeLinkEnvelope{}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Worktree != worktree || env.Branch != branch {
		t.Fatalf("second project must share the (warren,den) worktree: %+v", env)
	}
	state, _, err = warrenpkg.LoadWorkspaceStateFromMarmot(den.VaultPath("wt-den"))
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Warrens["cachew"].ActiveProjects; len(got) != 2 {
		t.Fatalf("active projects = %v", got)
	}

	// Idempotent relink warns and changes nothing.
	out, code = captureRun([]string{"den", "link", "wt-den", "--edit", "cachew/project-a", "--json"})
	if code != 0 || !strings.Contains(out, "already linked") {
		t.Fatalf("relink: code=%d out=%s", code, out)
	}

	// The shared read checkout is untouched: clean, still at the pin.
	sharedCheckout := warrenpkg.CacheCheckoutPath("cachew")
	if dirty := gitCLI(t, sharedCheckout, "status", "--porcelain"); dirty != "" {
		t.Fatalf("shared checkout dirtied: %q", dirty)
	}
	if head := gitCLI(t, sharedCheckout, "rev-parse", "HEAD"); head != remoteHead {
		t.Fatalf("shared checkout moved to %q", head)
	}
}

func TestDenLinkCacheSecondDenGetsOwnWorktree(t *testing.T) {
	hermeticDenCLI(t)
	_, _, wtA := cacheEditFixture(t, "den-a", "sharedw", "project-a")
	if _, err := den.Create("den-b", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "link", "den-b", "--edit", "sharedw/project-a", "--json"})
	if code != 0 {
		t.Fatalf("den-b link: code=%d out=%s", code, out)
	}
	var env denWorktreeLinkEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	wtB := warrenpkg.CacheEditWorktreePath("sharedw", "den-b")
	if env.Worktree != wtB || wtB == wtA {
		t.Fatalf("den-b worktree = %q (den-a %q)", env.Worktree, wtA)
	}
	if env.Branch != "marmot/edit/den-b/sharedw" {
		t.Fatalf("den-b branch = %q", env.Branch)
	}
	for _, wt := range []string{wtA, wtB} {
		if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
			t.Fatalf("worktree %s missing: %v", wt, err)
		}
	}
	// Distinct branches: a commit in den-b's worktree is invisible in den-a's.
	if a, b := gitCLI(t, wtA, "symbolic-ref", "--short", "HEAD"), gitCLI(t, wtB, "symbolic-ref", "--short", "HEAD"); a == b {
		t.Fatalf("dens share a branch: %q", a)
	}
}

func TestDenLinkCacheDryRunTouchesNothing(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "dryw", "project-a")
	if out, code := captureRun([]string{"warren", "add", remote, "--id", "dryw", "--json"}); code != 0 {
		t.Fatalf("warren add: %s", out)
	}
	if _, err := den.Create("dry-wt", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "link", "dry-wt", "--edit", "dryw/project-a", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("dry-run: code=%d out=%s", code, out)
	}
	var env struct {
		Schema int      `json:"schema"`
		DryRun bool     `json:"dry_run"`
		Ops    []string `json:"ops"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Ops) != 3 || !strings.Contains(env.Ops[0], "git worktree add") || !strings.Contains(env.Ops[0], "marmot/edit/dry-wt/dryw") {
		t.Fatalf("dry-run ops: %+v", env.Ops)
	}
	if _, err := os.Stat(filepath.Join(homeRoot, "warren-cache", "edits")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the edits dir (err=%v)", err)
	}
	if info, err := den.Status("dry-wt"); err != nil || len(info.Links) != 0 {
		t.Fatalf("dry-run appended links: %+v err=%v", info, err)
	}
}

func TestDenContributeEditWorktree(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, remote, worktree := cacheEditFixture(t, "cw-den", "cachew", "project-a")
	remoteHead := gitCLI(t, remote, "rev-parse", "HEAD")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "Alpha context")
	const branch = "marmot/edit/cw-den/cachew"

	// Dry-run first: plans against the worktree tree (== branch tip), lists
	// the commit op, touches nothing.
	out, code := captureRun([]string{"den", "contribute", "cw-den", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("dry-run: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "add node notes/alpha") || !strings.Contains(out, "commit on "+branch) {
		t.Fatalf("dry-run ops: %s", out)
	}
	if dirty := gitCLI(t, worktree, "status", "--porcelain"); dirty != "" {
		t.Fatalf("dry-run dirtied the worktree: %q", dirty)
	}

	out, code = captureRun([]string{"den", "contribute", "cw-den", "--json"})
	if code != 0 {
		t.Fatalf("contribute: code=%d out=%s", code, out)
	}
	env := parseContributeEnvelope(t, out)
	if env.Branch != branch || !env.Committed || env.Commit == "" || env.Contributed.Added != 1 {
		t.Fatalf("envelope: %+v", env)
	}
	if env.Checkout != worktree {
		t.Fatalf("checkout = %q, want worktree %q", env.Checkout, worktree)
	}
	if !strings.Contains(env.PushCommand, worktree) || !strings.HasSuffix(env.PushCommand, "push -u origin "+branch) {
		t.Fatalf("push_command = %q", env.PushCommand)
	}

	// The commit landed on the worktree's branch; the worktree stayed on it.
	if head := gitCLI(t, worktree, "symbolic-ref", "--short", "HEAD"); head != branch {
		t.Fatalf("worktree HEAD = %q", head)
	}
	if n := gitCLI(t, worktree, "rev-list", "--count", remoteHead+".."+branch); n != "1" {
		t.Fatalf("commit count = %s, want 1", n)
	}
	files := gitCLI(t, worktree, "diff", "--name-only", remoteHead, branch)
	if !strings.Contains(files, "projects/project-a/.marmot/notes/alpha.md") || strings.Contains(files, ".marmot-data") {
		t.Fatalf("committed files: %s", files)
	}
	if tip := gitCLI(t, worktree, "rev-parse", branch); tip != env.Commit {
		t.Fatalf("commit sha %q != branch tip %q", env.Commit, tip)
	}

	// Quarantine: the shared checkout and the user remote saw NOTHING.
	sharedCheckout := warrenpkg.CacheCheckoutPath("cachew")
	if dirty := gitCLI(t, sharedCheckout, "status", "--porcelain"); dirty != "" {
		t.Fatalf("shared checkout dirtied: %q", dirty)
	}
	if head := gitCLI(t, sharedCheckout, "rev-parse", "HEAD"); head != remoteHead {
		t.Fatalf("shared checkout moved: %q", head)
	}
	if _, err := os.Stat(filepath.Join(sharedCheckout, "projects", "project-a", ".marmot", "notes", "alpha.md")); !os.IsNotExist(err) {
		t.Fatalf("engine write leaked into the shared checkout (err=%v)", err)
	}
	if head := gitCLI(t, remote, "rev-parse", "HEAD"); head != remoteHead {
		t.Fatalf("remote moved (marmot never pushes): %q", head)
	}
	if _, err := gitOutput(remote, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		t.Fatal("edit branch must not exist on the remote before an explicit push")
	}

	// Idempotent second contribute: all noop, no new commit.
	out, code = captureRun([]string{"den", "contribute", "cw-den", "--json"})
	if code != 0 {
		t.Fatalf("second contribute: code=%d out=%s", code, out)
	}
	env = parseContributeEnvelope(t, out)
	if env.Committed || env.Contributed.Noop != 1 {
		t.Fatalf("second run: %+v", env)
	}
	if n := gitCLI(t, worktree, "rev-list", "--count", remoteHead+".."+branch); n != "1" {
		t.Fatalf("second run added a commit: %s", n)
	}
}

// TestDenContributeEditWorktreeDirtyRefused: pre-existing NON-node dirt in
// the worktree's project scope (not marmot's own leftovers) refuses with
// checkout_dirty and the git-status hint; cleaning it unblocks. Node
// markdown dirt is handled by the sweep path instead (next test).
func TestDenContributeEditWorktreeDirtyRefused(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, _, worktree := cacheEditFixture(t, "dirty-wt", "cachew", "project-a")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	stray := filepath.Join(worktree, "projects", "project-a", ".marmot", "leftover.bin")
	if err := os.WriteFile(stray, []byte("failed-run leftover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "contribute", "dirty-wt", "--json"})
	if code == 0 || !strings.Contains(out, "checkout_dirty") {
		t.Fatalf("dirty refusal: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "git -C") || !strings.Contains(out, "status") {
		t.Fatalf("expected git status hint: %s", out)
	}
	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	out, code = captureRun([]string{"den", "contribute", "dirty-wt", "--json"})
	if code != 0 {
		t.Fatalf("unblocked contribute: code=%d out=%s", code, out)
	}
	if env := parseContributeEnvelope(t, out); !env.Committed || env.Contributed.Added != 1 {
		t.Fatalf("unblocked: %+v", env)
	}
}

// TestDenContributeEditWorktreeSweepsFailedAutoCommit (F3): a node .md left
// uncommitted in the worktree — exactly what a failed MCP auto-commit leaves
// behind ("the next den contribute will commit it") — must NOT wedge
// contribute behind checkout_dirty. Contribute sweeps it into its commit and
// reports the sweep in warnings; the worktree ends clean.
func TestDenContributeEditWorktreeSweepsFailedAutoCommit(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, _, worktree := cacheEditFixture(t, "sweep-wt", "cachew", "project-a")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	// Simulate a failed auto-commit: a durable node write with no commit.
	orphan := filepath.Join(worktree, "projects", "project-a", ".marmot", "notes", "orphan.md")
	if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("---\nid: notes/orphan\ntype: concept\nstatus: active\n---\n\nOrphaned MCP write.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{"den", "contribute", "sweep-wt", "--json"})
	if code != 0 {
		t.Fatalf("contribute with sweepable dirt: code=%d out=%s", code, out)
	}
	env := parseContributeEnvelope(t, out)
	if !env.Committed || env.Commit == "" {
		t.Fatalf("sweep run must commit: %+v", env)
	}
	foundSweep := false
	for _, w := range env.Warnings {
		if strings.Contains(w, "swept 1 uncommitted edit(s) from failed auto-commits") {
			foundSweep = true
		}
	}
	if !foundSweep {
		t.Fatalf("warnings missing sweep disclosure: %+v", env.Warnings)
	}
	// The orphan is in the commit and the worktree is clean again.
	files := gitCLI(t, worktree, "show", "--name-only", "--format=", env.Commit)
	if !strings.Contains(files, "projects/project-a/.marmot/notes/orphan.md") {
		t.Fatalf("swept file missing from commit: %s", files)
	}
	if dirty := gitCLI(t, worktree, "status", "--porcelain"); dirty != "" {
		t.Fatalf("worktree still dirty after sweep: %q", dirty)
	}

	// Sweep-only run: engine has nothing new, but a fresh orphan still forces
	// a commit (this is the exact wedge F3 fixes — a zero-change contribute
	// must not strand the leftover).
	orphan2 := filepath.Join(worktree, "projects", "project-a", ".marmot", "notes", "orphan2.md")
	if err := os.WriteFile(orphan2, []byte("---\nid: notes/orphan2\ntype: concept\nstatus: active\n---\n\nSecond orphaned write.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code = captureRun([]string{"den", "contribute", "sweep-wt", "--json"})
	if code != 0 {
		t.Fatalf("sweep-only contribute: code=%d out=%s", code, out)
	}
	env = parseContributeEnvelope(t, out)
	if !env.Committed {
		t.Fatalf("sweep-only run must still commit: %+v", env)
	}
	if dirty := gitCLI(t, worktree, "status", "--porcelain"); dirty != "" {
		t.Fatalf("worktree still dirty after sweep-only run: %q", dirty)
	}
}

// TestWriteEditableNodeAutoCommitAgainstLinkedWorktree: the MCP/API write
// path (warren.WriteEditableNode) against a den's real ActiveMounts entry —
// which points into the edit worktree — auto-commits the node file (OQ7).
func TestWriteEditableNodeAutoCommitAgainstLinkedWorktree(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, _, worktree := cacheEditFixture(t, "mcp-den", "cachew", "project-a")
	mounts, err := warrenpkg.ActiveMounts(vaultDir)
	if err != nil || len(mounts) != 1 {
		t.Fatalf("mounts = %+v err=%v", mounts, err)
	}
	before := gitCLI(t, worktree, "rev-list", "--count", "HEAD")
	n := &nodepkg.Node{ID: "notes/mcp", Type: "concept", Namespace: "default", Status: nodepkg.StatusActive, Summary: "via MCP"}
	warning, err := warrenpkg.WriteEditableNode(mounts[0], n, nil, "", "")
	if err != nil {
		t.Fatalf("WriteEditableNode: %v", err)
	}
	if warning != "" {
		t.Fatalf("warning: %q", warning)
	}
	after := gitCLI(t, worktree, "rev-list", "--count", "HEAD")
	if before == after {
		t.Fatal("MCP write did not auto-commit in the edit worktree")
	}
	if subject := gitCLI(t, worktree, "log", "-1", "--format=%s"); subject != "marmot edit: project-a/notes/mcp (write)" {
		t.Fatalf("commit subject = %q", subject)
	}
	if dirty := gitCLI(t, worktree, "status", "--porcelain"); dirty != "" {
		t.Fatalf("worktree dirty after auto-commit: %q", dirty)
	}
}

func TestDenDestroyRefusesUnpushedWorktreeEdits(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	vaultDir, _, worktree := cacheEditFixture(t, "dst-den", "cachew", "project-a")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	if out, code := captureRun([]string{"den", "contribute", "dst-den", "--json"}); code != 0 {
		t.Fatalf("contribute: %s", out)
	}
	const branch = "marmot/edit/dst-den/cachew"
	bare := filepath.Join(homeRoot, "warren-cache", "cachew.git")

	// Dry-run discloses the worktree removal (branch kept).
	out, code := captureRun([]string{"den", "destroy", "dst-den", "--dry-run", "--json"})
	if code != 0 || !strings.Contains(out, "git worktree remove") || !strings.Contains(out, branch) {
		t.Fatalf("destroy dry-run: code=%d out=%s", code, out)
	}

	// Unpushed commit -> structured refusal, den and worktree intact.
	out, code = captureRun([]string{"den", "destroy", "dst-den", "--json"})
	if code != 1 || !strings.Contains(out, "unpushed_edits") {
		t.Fatalf("refusal: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "--force") {
		t.Fatalf("hint must offer --force: %s", out)
	}
	if _, err := den.Status("dst-den"); err != nil {
		t.Fatalf("refused destroy removed the den: %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("refused destroy removed the worktree: %v", err)
	}

	// --force destroys: real unpushed count, worktree removed, branch (the
	// knowledge) survives in the bare repo.
	out, code = captureRun([]string{"den", "destroy", "dst-den", "--force", "--json"})
	if code != 0 {
		t.Fatalf("forced destroy: code=%d out=%s", code, out)
	}
	var env struct {
		Schema        int    `json:"schema"`
		DenID         string `json:"den_id"`
		Destroyed     bool   `json:"destroyed"`
		UnpushedEdits int    `json:"unpushed_edits"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Destroyed || env.UnpushedEdits != 1 {
		t.Fatalf("forced destroy envelope: %+v", env)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree must be removed (err=%v)", err)
	}
	gitCLI(t, bare, "show-ref", "--verify", "refs/heads/"+branch) // branch survives
	if _, err := den.Status("dst-den"); err == nil {
		t.Fatal("den must be gone after forced destroy")
	}
}

func TestDenDestroyPushedWorktreeEditsNoRefusal(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	vaultDir, remote, worktree := cacheEditFixture(t, "push-den", "cachew", "project-a")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	if out, code := captureRun([]string{"den", "contribute", "push-den", "--json"}); code != 0 {
		t.Fatalf("contribute: %s", out)
	}
	const branch = "marmot/edit/push-den/cachew"
	// The user publishes the branch (the push_command contribute handed out).
	gitCLI(t, worktree, "push", "-q", "-u", "origin", branch)
	gitCLI(t, remote, "rev-parse", "--verify", "refs/heads/"+branch)

	out, code := captureRun([]string{"den", "destroy", "push-den", "--json"})
	if code != 0 {
		t.Fatalf("destroy after push: code=%d out=%s", code, out)
	}
	var env struct {
		Destroyed     bool `json:"destroyed"`
		UnpushedEdits int  `json:"unpushed_edits"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Destroyed || env.UnpushedEdits != 0 {
		t.Fatalf("pushed destroy envelope: %+v", env)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree must be removed (err=%v)", err)
	}
	bare := filepath.Join(homeRoot, "warren-cache", "cachew.git")
	gitCLI(t, bare, "show-ref", "--verify", "refs/heads/"+branch)
}

// TestDenDestroyFailsClosedOnUnknownUnpushed pins F9: when the unpushed count
// cannot be computed (degraded git environment — here the edit branch's
// upstream remote-tracking ref is deleted so AheadBehind errors), destroy must
// REFUSE with unpushed_unknown rather than fall through counting zero and
// discard un-published work. --force overrides.
func TestDenDestroyFailsClosedOnUnknownUnpushed(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	vaultDir, _, worktree := cacheEditFixture(t, "fc-den", "cachew", "project-a")
	writeDenNode(t, vaultDir, "notes/alpha", "Alpha summary", "")
	if out, code := captureRun([]string{"den", "contribute", "fc-den", "--json"}); code != 0 {
		t.Fatalf("contribute: %s", out)
	}
	const branch = "marmot/edit/fc-den/cachew"
	bare := filepath.Join(homeRoot, "warren-cache", "cachew.git")

	// Break the upstream: the edit branch was never pushed, so the count is
	// taken against origin/main; delete that remote-tracking ref so
	// `rev-list origin/main...branch` errors and the count is unknown.
	gitCLI(t, bare, "update-ref", "-d", "refs/remotes/origin/main")

	// Fail CLOSED: structured unpushed_unknown refusal, den and worktree intact.
	out, code := captureRun([]string{"den", "destroy", "fc-den", "--json"})
	if code != 1 || !strings.Contains(out, "unpushed_unknown") {
		t.Fatalf("must fail closed with unpushed_unknown: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "--force") {
		t.Fatalf("hint must offer --force: %s", out)
	}
	if _, err := den.Status("fc-den"); err != nil {
		t.Fatalf("refused destroy removed the den: %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("refused destroy removed the worktree: %v", err)
	}

	// --force overrides the unknown-count refusal and destroys; the branch
	// (the knowledge) survives in the bare repo.
	out, code = captureRun([]string{"den", "destroy", "fc-den", "--force", "--json"})
	if code != 0 {
		t.Fatalf("forced destroy after degraded count: code=%d out=%s", code, out)
	}
	var env struct {
		Destroyed bool     `json:"destroyed"`
		Warnings  []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Destroyed {
		t.Fatalf("forced destroy envelope: %+v", env)
	}
	if env.Warnings == nil {
		t.Fatal("destroy envelope must carry warnings[] (array, not null)")
	}
	gitCLI(t, bare, "show-ref", "--verify", "refs/heads/"+branch) // branch survives
	if _, err := den.Status("fc-den"); err == nil {
		t.Fatal("den must be gone after forced destroy")
	}
}
