package main

// CLI tests for the shared warren cache verbs (§5.2): warren add / warren
// sync plus the cache-backed resolution fallbacks (register-free mount,
// status, list [cache] marking) and the deprecation notes. Hermetic: every
// test runs against a temp MARMOT_HOME (hermeticDenCLI) and a local
// git-initialized "remote" warren repo — no network, real git.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
	"github.com/nurozen/context-marmot/internal/warrenreg"
)

// cacheRemoteWarren builds a local "remote": a git-initialized warren repo
// (manifest + projects on branch main) that warren add can clone by path.
func cacheRemoteWarren(t *testing.T, warrenID string, projects ...string) string {
	t.Helper()
	root := testWarrenRoot(t, warrenID, projects...)
	gitCLI(t, root, "init", "-q")
	gitCLI(t, root, "checkout", "-q", "-b", "main")
	gitCLI(t, root, "config", "user.email", "test@example.com")
	gitCLI(t, root, "config", "user.name", "Test")
	gitCLI(t, root, "add", "-A")
	gitCLI(t, root, "commit", "-q", "-m", "init warren")
	return root
}

// commitRemoteChange adds a commit to the remote warren so sync has
// something to fetch.
func commitRemoteChange(t *testing.T, remote, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(remote, name), []byte("update\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	gitCLI(t, remote, "add", "-A")
	gitCLI(t, remote, "commit", "-q", "-m", "update "+name)
	return gitCLI(t, remote, "rev-parse", "HEAD")
}

type warrenAddEnvelope struct {
	Schema        int      `json:"schema"`
	WarrenID      string   `json:"warren_id"`
	URL           string   `json:"url"`
	CachePath     string   `json:"cache_path"`
	CheckoutPath  string   `json:"checkout_path"`
	DefaultBranch string   `json:"default_branch"`
	PinnedCommit  string   `json:"pinned_commit"`
	Warnings      []string `json:"warnings"`
}

type warrenSyncEnvelope struct {
	Schema  int `json:"schema"`
	Warrens []struct {
		ID             string `json:"id"`
		Fetched        bool   `json:"fetched"`
		PreviousCommit string `json:"previous_commit"`
		PinnedCommit   string `json:"pinned_commit"`
		Updated        bool   `json:"updated"`
		Error          string `json:"error"`
	} `json:"warrens"`
	Warnings []string `json:"warnings"`
}

func TestWarrenAddCacheCLI(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "cachewarren", "project-a")
	remoteHead := gitCLI(t, remote, "rev-parse", "HEAD")

	out, code := captureRun([]string{"warren", "add", remote, "--id", "cachewarren", "--json"})
	if code != 0 {
		t.Fatalf("warren add exit code = %d, out=%s", code, out)
	}
	var env warrenAddEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("add envelope parse: %v out=%s", err, out)
	}
	barePath := filepath.Join(homeRoot, "warren-cache", "cachewarren.git")
	checkoutPath := filepath.Join(homeRoot, "warren-cache", "checkouts", "cachewarren")
	if env.Schema != 1 || env.WarrenID != "cachewarren" || env.URL != remote {
		t.Fatalf("envelope identity fields: %+v", env)
	}
	if env.CachePath != barePath || env.CheckoutPath != checkoutPath {
		t.Fatalf("envelope paths: %+v (want bare %s checkout %s)", env, barePath, checkoutPath)
	}
	if env.DefaultBranch != "main" {
		t.Fatalf("default_branch = %q, want main", env.DefaultBranch)
	}
	if env.PinnedCommit != remoteHead {
		t.Fatalf("pinned_commit = %q, want remote head %q", env.PinnedCommit, remoteHead)
	}
	if env.Warnings == nil {
		t.Fatal("warnings must be present (empty array, not null)")
	}
	// Bare mirror exists with the remote-tracking refspec rewritten.
	if fi, err := os.Stat(barePath); err != nil || !fi.IsDir() {
		t.Fatalf("bare mirror missing at %s: %v", barePath, err)
	}
	if refspec := gitCLI(t, barePath, "config", "remote.origin.fetch"); refspec != "+refs/heads/*:refs/remotes/origin/*" {
		t.Fatalf("refspec = %q", refspec)
	}
	// Shared read checkout is a detached worktree holding the warren manifest.
	if _, err := os.Stat(filepath.Join(checkoutPath, "_warren.md")); err != nil {
		t.Fatalf("checkout manifest missing: %v", err)
	}
	if head := gitCLI(t, checkoutPath, "rev-parse", "HEAD"); head != remoteHead {
		t.Fatalf("checkout HEAD = %q, want %q", head, remoteHead)
	}
	// Pin sidecar records the pinned commit.
	if pin := warrenpkg.ReadCachePin("cachewarren"); pin != remoteHead {
		t.Fatalf("pin = %q, want %q", pin, remoteHead)
	}
	// Registry saved last, with url + default branch (bare paths derived).
	reg, err := warrenreg.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reg.Warrens["cachewarren"]
	if !ok || entry.URL != remote || entry.DefaultBranch != "main" {
		t.Fatalf("registry entry = %+v ok=%t", entry, ok)
	}

	// Duplicate id refused before any fs op.
	out, code = captureRun([]string{"warren", "add", remote, "--id", "cachewarren", "--json"})
	if code != 1 || !strings.Contains(out, "duplicate_warren") {
		t.Fatalf("duplicate add: code=%d out=%s", code, out)
	}
}

func TestWarrenAddDefaultIDFromURL(t *testing.T) {
	hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "urlwarren", "project-a")
	// Move to a name ending in .git so basename derivation is exercised.
	renamed := filepath.Join(filepath.Dir(remote), "urlwarren.git")
	if err := os.Rename(remote, renamed); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"warren", "add", renamed, "--json"})
	if code != 0 {
		t.Fatalf("warren add: code=%d out=%s", code, out)
	}
	var env warrenAddEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.WarrenID != "urlwarren" {
		t.Fatalf("derived id = %q, want urlwarren", env.WarrenID)
	}
}

func TestWarrenAddNotAWarrenCleansUp(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	// A plain git repo without a _warren.md manifest.
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "README.md"), []byte("plain repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, remote, "init", "-q")
	gitCLI(t, remote, "checkout", "-q", "-b", "main")
	gitCLI(t, remote, "config", "user.email", "test@example.com")
	gitCLI(t, remote, "config", "user.name", "Test")
	gitCLI(t, remote, "add", "-A")
	gitCLI(t, remote, "commit", "-q", "-m", "init")

	out, code := captureRun([]string{"warren", "add", remote, "--id", "notawarren", "--json"})
	if code != 1 || !strings.Contains(out, "not_a_warren") {
		t.Fatalf("not_a_warren refusal: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "warren init") {
		t.Fatalf("expected warren init hint: %s", out)
	}
	// Everything created is removed: bare, checkout, pin — and no registry entry.
	for _, path := range []string{
		filepath.Join(homeRoot, "warren-cache", "notawarren.git"),
		filepath.Join(homeRoot, "warren-cache", "checkouts", "notawarren"),
		filepath.Join(homeRoot, "warren-cache", "checkouts", "notawarren.pin"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist after refusal (err=%v)", path, err)
		}
	}
	reg, err := warrenreg.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Warrens["notawarren"]; ok {
		t.Fatal("registry must not carry a refused warren")
	}
}

func TestWarrenAddDryRunTouchesNothing(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "drywarren", "project-a")

	stdout, _, code := captureRunBoth(t, []string{"warren", "add", remote, "--id", "drywarren", "--dry-run"})
	if code != 0 {
		t.Fatalf("dry-run exit code = %d", code)
	}
	for _, want := range []string{"dry-run:", "clone --bare", "worktree add --detach", "registry add drywarren"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run output missing %q: %s", want, stdout)
		}
	}
	// Nothing on disk: no cache dir, no registry, not even a lock sidecar.
	if _, err := os.Stat(filepath.Join(homeRoot, "warren-cache")); !os.IsNotExist(err) {
		t.Fatalf("warren-cache must not exist after dry-run (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(homeRoot, "warrens.yml")); !os.IsNotExist(err) {
		t.Fatalf("registry must not exist after dry-run (err=%v)", err)
	}

	// JSON dry-run uses the shared dry_run envelope.
	out, code := captureRun([]string{"warren", "add", remote, "--id", "drywarren", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("dry-run --json exit code = %d out=%s", code, out)
	}
	var env struct {
		Schema int      `json:"schema"`
		DryRun bool     `json:"dry_run"`
		Ops    []string `json:"ops"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Schema != 1 || !env.DryRun || len(env.Ops) == 0 {
		t.Fatalf("dry-run envelope: %+v", env)
	}
}

func TestWarrenSyncCacheCLI(t *testing.T) {
	hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "syncwarren", "project-a")
	if _, code := captureRun([]string{"warren", "add", remote, "--id", "syncwarren", "--json"}); code != 0 {
		t.Fatal("warren add failed")
	}
	oldHead := gitCLI(t, remote, "rev-parse", "HEAD")
	newHead := commitRemoteChange(t, remote, "notes.md")

	out, code := captureRun([]string{"warren", "sync", "--json"})
	if code != 0 {
		t.Fatalf("warren sync exit code = %d out=%s", code, out)
	}
	var env warrenSyncEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("sync envelope parse: %v out=%s", err, out)
	}
	if env.Schema != 1 || len(env.Warrens) != 1 || env.Warnings == nil {
		t.Fatalf("sync envelope shape: %+v", env)
	}
	res := env.Warrens[0]
	if res.ID != "syncwarren" || !res.Fetched || !res.Updated {
		t.Fatalf("sync result: %+v", res)
	}
	if res.PreviousCommit != oldHead || res.PinnedCommit != newHead {
		t.Fatalf("sync commits: %+v (want %s -> %s)", res, oldHead, newHead)
	}
	// The shared checkout was re-pinned and the pin sidecar follows.
	checkout := warrenpkg.CacheCheckoutPath("syncwarren")
	if head := gitCLI(t, checkout, "rev-parse", "HEAD"); head != newHead {
		t.Fatalf("checkout HEAD = %q, want %q", head, newHead)
	}
	if _, err := os.Stat(filepath.Join(checkout, "notes.md")); err != nil {
		t.Fatalf("synced file missing from checkout: %v", err)
	}
	if pin := warrenpkg.ReadCachePin("syncwarren"); pin != newHead {
		t.Fatalf("pin = %q, want %q", pin, newHead)
	}

	// Second sync: fetched, nothing to update.
	out, code = captureRun([]string{"warren", "sync", "syncwarren", "--json"})
	if code != 0 {
		t.Fatalf("second sync exit code = %d out=%s", code, out)
	}
	env = warrenSyncEnvelope{}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Warrens) != 1 || !env.Warrens[0].Fetched || env.Warrens[0].Updated {
		t.Fatalf("second sync result: %+v", env.Warrens)
	}

	// Text mode prints the up-to-date line.
	stdout, _, code := captureRunBoth(t, []string{"warren", "sync"})
	if code != 0 || !strings.Contains(stdout, "already up to date") {
		t.Fatalf("text sync: code=%d out=%s", code, stdout)
	}
}

func TestWarrenSyncEmptyRegistry(t *testing.T) {
	hermeticDenCLI(t)
	stdout, _, code := captureRunBoth(t, []string{"warren", "sync"})
	if code != 0 {
		t.Fatalf("empty sync exit code = %d", code)
	}
	if !strings.Contains(stdout, "No warrens registered.") {
		t.Fatalf("empty sync output = %q", stdout)
	}
	out, code := captureRun([]string{"warren", "sync", "--json"})
	if code != 0 {
		t.Fatalf("empty sync --json exit code = %d", code)
	}
	var env warrenSyncEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Warrens == nil || len(env.Warrens) != 0 || env.Warnings == nil {
		t.Fatalf("empty sync envelope: %+v", env)
	}
	// Unknown explicit id is a refusal, not an empty success.
	out, code = captureRun([]string{"warren", "sync", "no-such", "--json"})
	if code != 1 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("unknown id sync: code=%d out=%s", code, out)
	}
}

func TestWarrenSyncErrorDoesNotAbortLoopAllFailedExits1(t *testing.T) {
	hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "goodwarren", "project-a")
	if _, code := captureRun([]string{"warren", "add", remote, "--id", "goodwarren", "--json"}); code != 0 {
		t.Fatal("warren add failed")
	}
	// A registry entry whose bare cache is missing: per-warren error.
	if err := warrenreg.Update(func(reg *warrenreg.Registry) error {
		reg.Warrens["ghostwarren"] = warrenreg.Entry{URL: "/nonexistent/ghost.git", DefaultBranch: "main"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"warren", "sync", "--json"})
	if code != 0 {
		t.Fatalf("partial-failure sync must exit 0, got %d out=%s", code, out)
	}
	var env warrenSyncEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Warrens) != 2 {
		t.Fatalf("want 2 results, got %+v", env.Warrens)
	}
	byID := map[string]bool{}
	for _, res := range env.Warrens {
		byID[res.ID] = res.Error != ""
	}
	if byID["goodwarren"] || !byID["ghostwarren"] {
		t.Fatalf("error attribution wrong: %+v", env.Warrens)
	}
	// All failed -> exit 1.
	out, code = captureRun([]string{"warren", "sync", "ghostwarren", "--json"})
	if code != 1 {
		t.Fatalf("all-failed sync must exit 1, got %d out=%s", code, out)
	}
	if !strings.Contains(out, "bare cache missing") {
		t.Fatalf("expected bare-cache error in envelope: %s", out)
	}
}

// TestWarrenCacheRegisterFreeMountAndStatus: a cache-backed warren works with
// workspace verbs WITHOUT a prior 'warren register' — status reads through
// the shared checkout, and mount auto-records the checkout path into the
// workspace state.
func TestWarrenCacheRegisterFreeMountAndStatus(t *testing.T) {
	hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "freemount", "project-a")
	if _, code := captureRun([]string{"warren", "add", remote, "--id", "freemount", "--json"}); code != 0 {
		t.Fatal("warren add failed")
	}
	marmotDir := filepath.Join(t.TempDir(), ".marmot")

	// Register-free status (read verb): needs an existing workspace but no
	// warren registration.
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := captureRunBoth(t, []string{"warren", "status", "--dir", marmotDir, "--warren", "freemount"})
	if code != 0 {
		t.Fatalf("register-free status exit code = %d out=%s", code, stdout)
	}
	if !strings.Contains(stdout, "project-a") || !strings.Contains(stdout, "dormant") {
		t.Fatalf("register-free status output = %q", stdout)
	}

	// Register-free mount records the shared checkout into workspace state.
	stdout, stderr, code := captureRunBoth(t, []string{"warren", "mount", "--dir", marmotDir, "--warren", "freemount", "project-a"})
	if code != 0 {
		t.Fatalf("register-free mount: code=%d out=%s err=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `Mounted 1 project(s) from Warren "freemount"`) {
		t.Fatalf("mount output = %q", stdout)
	}
	if !strings.Contains(stderr, "resolved from the shared cache") {
		t.Fatalf("mount stderr should note the cache resolution: %q", stderr)
	}
	state, _, err := warrenpkg.LoadWorkspaceStateFromMarmot(marmotDir)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := state.Warrens["freemount"]
	if !ok || entry.Path != warrenpkg.CacheCheckoutPath("freemount") {
		t.Fatalf("workspace state entry = %+v ok=%t", entry, ok)
	}
	if len(entry.ActiveProjects) != 1 || entry.ActiveProjects[0] != "project-a" {
		t.Fatalf("active projects = %v", entry.ActiveProjects)
	}
	// Status now reflects the mount through the normal registered path.
	stdout, _, code = captureRunBoth(t, []string{"warren", "status", "--dir", marmotDir, "--warren", "freemount"})
	if code != 0 || !strings.Contains(stdout, "mounted") {
		t.Fatalf("post-mount status: code=%d out=%q", code, stdout)
	}
}

// TestWarrenListMarksCacheBacked: warren list shows registry warrens that are
// not workspace-registered, marked [cache] (text) / "cache": true (JSON).
func TestWarrenListMarksCacheBacked(t *testing.T) {
	hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "listcache", "project-a")
	if _, code := captureRun([]string{"warren", "add", remote, "--id", "listcache", "--json"}); code != 0 {
		t.Fatal("warren add failed")
	}
	marmotDir := filepath.Join(t.TempDir(), ".marmot")
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"warren", "list", "--dir", marmotDir})
	if code != 0 {
		t.Fatalf("warren list exit code = %d out=%s", code, out)
	}
	if !strings.Contains(out, "listcache [cache]") {
		t.Fatalf("list must mark cache-backed warren: %q", out)
	}
	out, code = captureRun([]string{"warren", "list", "--dir", marmotDir, "--json"})
	if code != 0 {
		t.Fatalf("warren list --json exit code = %d", code)
	}
	var doc struct {
		Warrens map[string]struct {
			Path  string `json:"path"`
			Cache bool   `json:"cache"`
		} `json:"Warrens"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	entry, ok := doc.Warrens["listcache"]
	if !ok || !entry.Cache || entry.Path != warrenpkg.CacheCheckoutPath("listcache") {
		t.Fatalf("json list entry = %+v ok=%t", entry, ok)
	}
}

// TestWarrenCacheDeprecationNotes pins the §5.2 deprecation posture: stderr
// notes only, stdout strings unchanged.
func TestWarrenCacheDeprecationNotes(t *testing.T) {
	hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "depwarren", "project-a")

	// warren register keeps working, with a legacy note on stderr.
	marmotDir := filepath.Join(t.TempDir(), ".marmot")
	stdout, stderr, code := captureRunBoth(t, []string{"warren", "register", "--dir", marmotDir, "depwarren", remote})
	if code != 0 {
		t.Fatalf("warren register: code=%d out=%s err=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `Registered Warren "depwarren"`) {
		t.Fatalf("register stdout changed: %q", stdout)
	}
	if !strings.Contains(stderr, "note: registering a user-managed checkout is legacy; prefer 'marmot warren add <url>' (shared cache)") {
		t.Fatalf("register deprecation note missing: %q", stderr)
	}

	// refresh --pull on a cache-backed warren hints at warren sync.
	if _, code := captureRun([]string{"warren", "add", remote, "--id", "depwarren", "--json"}); code != 0 {
		t.Fatal("warren add failed")
	}
	cacheDir := filepath.Join(t.TempDir(), ".marmot")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, stderr, _ = captureRunBoth(t, []string{"warren", "refresh", "--dir", cacheDir, "--warren", "depwarren", "--pull"})
	if !strings.Contains(stderr, "prefer 'marmot warren sync depwarren'") {
		t.Fatalf("refresh --pull cache hint missing: %q", stderr)
	}
}

func TestWarrenIDFromURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com/org/product-platform.git": "product-platform",
		"git@github.com:org/product-platform.git":      "product-platform",
		"/tmp/warrens/product-platform":                "product-platform",
		"file:///tmp/warrens/product-platform.git/":    "product-platform",
	}
	for url, want := range cases {
		if got := warrenIDFromURL(url); got != want {
			t.Errorf("warrenIDFromURL(%q) = %q, want %q", url, got, want)
		}
	}
}
