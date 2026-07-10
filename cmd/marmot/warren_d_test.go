package main

import (
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
