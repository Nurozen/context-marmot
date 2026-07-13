package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// captureRunBoth runs the CLI capturing stdout and stderr separately.
func captureRunBoth(t *testing.T, args []string) (stdout, stderr string, code int) {
	t.Helper()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	code = run(args)
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	outData, _ := io.ReadAll(outR)
	errData, _ := io.ReadAll(errR)
	_ = outR.Close()
	_ = errR.Close()
	return string(outData), string(errData), code
}

// registeredWorkspace registers a fixture warren and returns the workspace
// marmot dir plus the warren root.
func registeredWorkspace(t *testing.T, warrenID string, projects ...string) (marmotDir, warrenRoot string) {
	t.Helper()
	workspace := t.TempDir()
	marmotDir = filepath.Join(workspace, ".marmot")
	warrenRoot = testWarrenRoot(t, warrenID, projects...)
	if code := run([]string{"warren", "register", "--dir", marmotDir, warrenID, warrenRoot}); code != 0 {
		t.Fatalf("register exit code = %d", code)
	}
	return marmotDir, warrenRoot
}

func loadCLIState(t *testing.T, marmotDir string) *warrenpkg.WorkspaceState {
	t.Helper()
	state, _, err := warrenpkg.LoadWorkspaceStateFromMarmot(marmotDir)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	return state
}

// TestWarrenMountBareRefusalLeavesStateUnwritten (C3): a zero-arg mount
// without --all errors and mounts nothing; --all plus explicit IDs errors.
func TestWarrenMountBareRefusalLeavesStateUnwritten(t *testing.T) {
	marmotDir, _ := registeredWorkspace(t, "wp", "project-a", "project-b")

	_, stderr, code := captureRunBoth(t, []string{"warren", "mount", "--dir", marmotDir, "--warren", "wp"})
	if code != 1 || !strings.Contains(stderr, "--all") {
		t.Fatalf("bare mount = code %d stderr %q, want refusal mentioning --all", code, stderr)
	}
	if got := loadCLIState(t, marmotDir).Warrens["wp"].ActiveProjects; len(got) != 0 {
		t.Fatalf("bare mount refusal still mounted %v", got)
	}

	_, stderr, code = captureRunBoth(t, []string{"warren", "mount", "--dir", marmotDir, "--warren", "wp", "--all", "project-a"})
	if code != 1 || !strings.Contains(stderr, "cannot combine") {
		t.Fatalf("mount --all + IDs = code %d stderr %q", code, stderr)
	}

	if code := run([]string{"warren", "mount", "--dir", marmotDir, "--warren", "wp", "--all"}); code != 0 {
		t.Fatalf("mount --all exit code = %d", code)
	}
	if got := loadCLIState(t, marmotDir).Warrens["wp"].ActiveProjects; len(got) != 2 {
		t.Fatalf("mount --all mounted %v, want 2 projects", got)
	}
}

// TestBurrowImpliesMaterialize (C2): bare burrow creates the cache — burrow
// always materializes. The retired --materialize compat flag is rejected as
// unknown.
func TestBurrowImpliesMaterialize(t *testing.T) {
	marmotDir, _ := registeredWorkspace(t, "wp", "project-a")

	out, code := captureRun([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "project-a"})
	if code != 0 {
		t.Fatalf("burrow exit code = %d (%s)", code, out)
	}
	cache := filepath.Join(marmotDir, ".marmot-data", "warrens", "wp", "projects", "project-a", ".marmot")
	if fi, err := os.Stat(cache); err != nil || !fi.IsDir() {
		t.Fatalf("bare burrow did not create the cache at %s: %v", cache, err)
	}
	if !loadCLIState(t, marmotDir).Warrens["wp"].Materialized {
		t.Fatal("burrow did not set the Materialized flag")
	}

	if code := run([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "--materialize", "project-a"}); code != 1 {
		t.Fatalf("retired --materialize flag exit code = %d, want 1 (unknown flag)", code)
	}
}

// TestBurrowDropCLI (C1): --drop deletes caches, clears the flag on the last
// one, and is refused on plain mount.
func TestBurrowDropCLI(t *testing.T) {
	marmotDir, _ := registeredWorkspace(t, "wp", "project-a", "project-b")
	if code := run([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "--all"}); code != 0 {
		t.Fatalf("burrow --all exit code = %d", code)
	}

	_, stderr, code := captureRunBoth(t, []string{"warren", "mount", "--dir", marmotDir, "--warren", "wp", "--drop", "project-a"})
	if code != 1 || !strings.Contains(stderr, "only valid with") {
		t.Fatalf("mount --drop = code %d stderr %q", code, stderr)
	}

	out, code := captureRun([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "--drop", "project-a"})
	if code != 0 || !strings.Contains(out, "Dropped burrow cache") {
		t.Fatalf("burrow --drop = code %d out %q", code, out)
	}
	if !loadCLIState(t, marmotDir).Warrens["wp"].Materialized {
		t.Fatal("Materialized cleared while project-b cache remains")
	}

	if code := run([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "--drop", "--all"}); code != 0 {
		t.Fatalf("burrow --drop --all exit code = %d", code)
	}
	if loadCLIState(t, marmotDir).Warrens["wp"].Materialized {
		t.Fatal("Materialized flag survived dropping every cache")
	}
	if dirs, _ := os.ReadDir(filepath.Join(marmotDir, ".marmot-data", "warrens", "wp", "projects")); len(dirs) != 0 {
		t.Fatalf("cache dirs survived --drop --all: %v", dirs)
	}

	// Idempotent: --all with nothing left reports and exits 0.
	out, code = captureRun([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "--drop", "--all"})
	if code != 0 || !strings.Contains(out, "No burrow caches") {
		t.Fatalf("empty --drop --all = code %d out %q", code, out)
	}
}

// TestMountMaterializeFailureClearsMaterializedFlag: Mount sets the
// warren-level Materialized flag before the CLI's Materialize loop runs; a
// first-project materialize failure rolls the mount back and must clear the
// flag too — stranded, it would make ActiveMounts' unreadable-manifest
// branch silently serve (empty) materializedStatuses instead of the A6
// "mounts skipped" warning, and no drop verb could ever reset it.
func TestMountMaterializeFailureClearsMaterializedFlag(t *testing.T) {
	marmotDir, warrenRoot := registeredWorkspace(t, "wp", "project-a")
	// The project vault vanishes from the checkout while the manifest still
	// lists it: Mount succeeds, Materialize fails.
	if err := os.RemoveAll(filepath.Join(warrenRoot, "projects", "project-a")); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := captureRunBoth(t, []string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "project-a"})
	if code != 1 || !strings.Contains(stderr, "materialize project-a") {
		t.Fatalf("burrow with missing source = code %d stderr %q, want materialize failure", code, stderr)
	}
	entry := loadCLIState(t, marmotDir).Warrens["wp"]
	if len(entry.ActiveProjects) != 0 {
		t.Fatalf("rollback left project(s) mounted: %v", entry.ActiveProjects)
	}
	if entry.Materialized {
		t.Fatal("Materialized flag stranded after the rolled-back materialize failure")
	}
}

// TestBurrowDropAllClearsStrandedFlag: `burrow --drop --all` with zero
// caches on disk is the recovery verb for a stranded Materialized flag
// (e.g. a crash between Mount's state write and the materialize loop) — it
// must clear the flag instead of returning before the state update.
func TestBurrowDropAllClearsStrandedFlag(t *testing.T) {
	marmotDir, _ := registeredWorkspace(t, "wp", "project-a")
	// Strand the flag directly: Materialized=true with no cache dirs.
	state, body, err := warrenpkg.LoadWorkspaceStateFromMarmot(marmotDir)
	if err != nil {
		t.Fatalf("LoadWorkspaceStateFromMarmot: %v", err)
	}
	entry := state.Warrens["wp"]
	entry.Materialized = true
	state.Warrens["wp"] = entry
	if err := warrenpkg.SaveWorkspaceStateToMarmot(marmotDir, state, body); err != nil {
		t.Fatalf("SaveWorkspaceStateToMarmot: %v", err)
	}

	out, code := captureRun([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "--drop", "--all"})
	if code != 0 || !strings.Contains(out, "No burrow caches") {
		t.Fatalf("burrow --drop --all = code %d out %q", code, out)
	}
	if loadCLIState(t, marmotDir).Warrens["wp"].Materialized {
		t.Fatal("stranded Materialized flag survived burrow --drop --all")
	}
}

// TestWarrenUnmountCLI (C1): unmount keeps caches (and says so), --all
// expands from active projects, and works with the checkout deleted.
func TestWarrenUnmountCLI(t *testing.T) {
	marmotDir, warrenRoot := registeredWorkspace(t, "wp", "project-a", "project-b")
	if code := run([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "--all"}); code != 0 {
		t.Fatalf("burrow exit code = %d", code)
	}

	out, code := captureRun([]string{"warren", "unmount", "--dir", marmotDir, "--warren", "wp", "project-a"})
	if code != 0 || !strings.Contains(out, `Unmounted "project-a"`) || !strings.Contains(out, "burrow --drop") {
		t.Fatalf("unmount = code %d out %q, want unmount line + caches-kept note", code, out)
	}
	state := loadCLIState(t, marmotDir)
	if got := state.Warrens["wp"].ActiveProjects; len(got) != 1 || got[0] != "project-b" {
		t.Fatalf("active after unmount = %v", got)
	}
	if !dirExistsCLI(filepath.Join(marmotDir, ".marmot-data", "warrens", "wp", "projects", "project-a", ".marmot")) {
		t.Fatal("unmount deleted the burrow cache")
	}

	// Escape hatch: checkout gone, unmount --all still works.
	if err := os.RemoveAll(warrenRoot); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"warren", "unmount", "--dir", marmotDir, "--warren", "wp", "--all"}); code != 0 {
		t.Fatalf("unmount --all with checkout gone exit code = %d", code)
	}
	if got := loadCLIState(t, marmotDir).Warrens["wp"].ActiveProjects; len(got) != 0 {
		t.Fatalf("active after unmount --all = %v", got)
	}
}

// TestWarrenUnregisterCLI (C1): refusal names the blocking state; --force
// removes entry and cache tree.
func TestWarrenUnregisterCLI(t *testing.T) {
	marmotDir, _ := registeredWorkspace(t, "wp", "project-a")
	if code := run([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "--all"}); code != 0 {
		t.Fatalf("burrow exit code = %d", code)
	}

	_, stderr, code := captureRunBoth(t, []string{"warren", "unregister", "--dir", marmotDir, "--warren", "wp"})
	if code != 1 || !strings.Contains(stderr, "warren unmount") {
		t.Fatalf("unregister refusal = code %d stderr %q", code, stderr)
	}

	out, code := captureRun([]string{"warren", "unregister", "--dir", marmotDir, "--warren", "wp", "--force"})
	if code != 0 || !strings.Contains(out, `Unregistered Warren "wp"`) {
		t.Fatalf("unregister --force = code %d out %q", code, out)
	}
	if len(loadCLIState(t, marmotDir).Warrens) != 0 {
		t.Fatal("entry survived unregister --force")
	}
	if dirExistsCLI(filepath.Join(marmotDir, ".marmot-data", "warrens", "wp")) {
		t.Fatal("cache tree survived unregister --force")
	}
}

// TestWarrenEditAutoMountMessage (C4): edit of an unmounted project says the
// auto-mount out loud; a pre-mounted project keeps the plain message.
func TestWarrenEditAutoMountMessage(t *testing.T) {
	marmotDir, _ := registeredWorkspace(t, "wp", "project-a", "project-b")
	if code := run([]string{"warren", "mount", "--dir", marmotDir, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatalf("mount exit code = %d", code)
	}

	out, code := captureRun([]string{"warren", "edit", "--dir", marmotDir, "--warren", "wp", "project-a"})
	if code != 0 || strings.Contains(out, "edit implies mount") {
		t.Fatalf("edit pre-mounted = code %d out %q, want no auto-mount note", code, out)
	}
	out, code = captureRun([]string{"warren", "edit", "--dir", marmotDir, "--warren", "wp", "project-b"})
	if code != 0 || !strings.Contains(out, "edit implies mount") {
		t.Fatalf("edit unmounted = code %d out %q, want auto-mount note", code, out)
	}
}

// TestWarrenStatusAndListUnreachable (C6): a deleted checkout is surfaced by
// status (UNREACHABLE banner + degraded rows) and list (REACHABLE column and
// JSON reachable field) instead of vanishing or erroring opaquely.
func TestWarrenStatusAndListUnreachable(t *testing.T) {
	marmotDir, warrenRoot := registeredWorkspace(t, "wp", "project-a")
	if code := run([]string{"warren", "mount", "--dir", marmotDir, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatalf("mount exit code = %d", code)
	}
	if err := os.RemoveAll(warrenRoot); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := captureRunBoth(t, []string{"warren", "status", "--dir", marmotDir, "--warren", "wp"})
	if code != 0 {
		t.Fatalf("status exit code = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, "UNREACHABLE") || !strings.Contains(stderr, "unregister") {
		t.Fatalf("status stderr = %q, want UNREACHABLE banner naming the escape hatches", stderr)
	}
	if !strings.Contains(stdout, "project-a") || !strings.Contains(stdout, "false") {
		t.Fatalf("status stdout = %q, want degraded row with AVAILABLE=false", stdout)
	}

	stdout, _, code = captureRunBoth(t, []string{"warren", "list", "--dir", marmotDir})
	if code != 0 || !strings.Contains(stdout, "REACHABLE") {
		t.Fatalf("list stdout = %q code %d, want REACHABLE column", stdout, code)
	}
	jsonOut, _, code := captureRunBoth(t, []string{"warren", "list", "--dir", marmotDir, "--json"})
	if code != 0 {
		t.Fatalf("list --json exit code = %d", code)
	}
	var parsed struct {
		Warrens map[string]struct {
			Reachable bool `json:"reachable"`
		} `json:"Warrens"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("list --json parse: %v (%q)", err, jsonOut)
	}
	if entry, ok := parsed.Warrens["wp"]; !ok || entry.Reachable {
		t.Fatalf("list --json = %+v, want wp with reachable=false", parsed.Warrens)
	}
}

// TestWarrenProposeRequiresGit (D3, replacing C9's honest stub): propose is
// real now — it creates a branch — so on a non-git warren it refuses loudly
// instead of pretending.
func TestWarrenProposeRequiresGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	marmotDir, warrenRoot := registeredWorkspace(t, "wp", "project-a")
	_, stderr, code := captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp"})
	if code != 1 {
		t.Fatalf("propose exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "not a git checkout") || !strings.Contains(stderr, warrenRoot) {
		t.Fatalf("propose stderr = %q, want git refusal naming the checkout", stderr)
	}
}

// TestWarrenLazyWorkspaceVerbs (C5): read-only and state-inverse verbs never
// fabricate a workspace.
func TestWarrenLazyWorkspaceVerbs(t *testing.T) {
	bare := filepath.Join(t.TempDir(), ".marmot")
	for _, args := range [][]string{
		{"warren", "status", "--dir", bare, "--warren", "wp"},
		{"warren", "unmount", "--dir", bare, "--warren", "wp", "--all"},
		{"warren", "unregister", "--dir", bare, "--warren", "wp"},
		{"warren", "burrow", "--dir", bare, "--warren", "wp", "--drop", "--all"},
	} {
		if _, stderr, code := captureRunBoth(t, args); code != 1 || !strings.Contains(stderr, "no marmot workspace") {
			t.Fatalf("%v = code %d stderr %q, want lazy-workspace refusal", args, code, stderr)
		}
		if _, err := os.Stat(bare); !os.IsNotExist(err) {
			t.Fatalf("%v fabricated a workspace", args)
		}
	}
	// Mutating verbs still create the workspace.
	warrenRoot := testWarrenRoot(t, "wp", "project-a")
	if code := run([]string{"warren", "register", "--dir", bare, "wp", warrenRoot}); code != 0 {
		t.Fatal("register must create the workspace")
	}
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("register did not create the workspace: %v", err)
	}
}

// TestBurrowMidLoopFailureRollsBack (C2): a materialize failure unmounts the
// projects this command mounted but never cached, so no mounted-but-uncached
// state survives.
func TestBurrowMidLoopFailureRollsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: unreadable-file fixture has no effect")
	}
	marmotDir, warrenRoot := registeredWorkspace(t, "wp", "project-a", "project-b")
	// project-b's vault contains an unreadable file, so its copy fails.
	blocked := filepath.Join(warrenRoot, "projects", "project-b", ".marmot", "secret.md")
	if err := os.WriteFile(blocked, []byte("locked"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o644) })

	_, stderr, code := captureRunBoth(t, []string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "project-a", "project-b"})
	if code != 1 {
		t.Fatalf("burrow with unreadable source exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "unmounted not-yet-cached") || !strings.Contains(stderr, "project-b") {
		t.Fatalf("stderr = %q, want rollback note naming project-b", stderr)
	}
	state := loadCLIState(t, marmotDir)
	active := state.Warrens["wp"].ActiveProjects
	if len(active) != 1 || active[0] != "project-a" {
		t.Fatalf("active after rollback = %v, want only the cached project-a", active)
	}
	if !dirExistsCLI(filepath.Join(marmotDir, ".marmot-data", "warrens", "wp", "projects", "project-a", ".marmot")) {
		t.Fatal("project-a cache missing despite staying mounted")
	}
}

// TestWarrenProjectRenameOutput (U6): rename reports what happened to the
// project directory and states that vault_id (the identity key) is stable.
func TestWarrenProjectRenameOutput(t *testing.T) {
	root := testWarrenRoot(t, "wp", "api")

	stdout, _, code := captureRunBoth(t, []string{"warren", "project", "rename", "api", "api-service", "--warren-dir", root})
	if code != 0 {
		t.Fatalf("rename exit code = %d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, `Renamed project "api" -> "api-service" (moved projects/api -> projects/api-service)`) {
		t.Fatalf("rename output missing move line: %q", stdout)
	}
	if !strings.Contains(stdout, `note: vault_id "api" unchanged — vault identity is stable across renames; re-import with --vault-id to change it`) {
		t.Fatalf("rename output missing vault_id note: %q", stdout)
	}
	if !dirExistsCLI(filepath.Join(root, "projects", "api-service", ".marmot")) {
		t.Fatal("moved project dir missing")
	}
	if dirExistsCLI(filepath.Join(root, "projects", "api")) {
		t.Fatal("old project dir still present")
	}

	// --keep-path leaves the directory alone and says so.
	stdout, _, code = captureRunBoth(t, []string{"warren", "project", "rename", "api-service", "api-two", "--keep-path", "--warren-dir", root})
	if code != 0 {
		t.Fatalf("rename --keep-path exit code = %d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, `Renamed project "api-service" -> "api-two" (path projects/api-service/.marmot kept)`) {
		t.Fatalf("rename --keep-path output wrong: %q", stdout)
	}
	if !dirExistsCLI(filepath.Join(root, "projects", "api-service", ".marmot")) {
		t.Fatal("--keep-path moved the directory anyway")
	}
}
