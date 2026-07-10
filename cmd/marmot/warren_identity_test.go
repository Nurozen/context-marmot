package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// writeIdentityConfig gives the workspace's live vault a vault_id so warren
// projects carrying the same ID become identified with it.
func writeIdentityConfig(t *testing.T, marmotDir, vaultID string) {
	t.Helper()
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWarrenRegisterAnnouncesIdentity (R2.4): registering a warren that
// contains this workspace's own project announces the identity match —
// identity is automatic, register is when it becomes discoverable. A warren
// without a match stays quiet.
func TestWarrenRegisterAnnouncesIdentity(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	writeIdentityConfig(t, marmotDir, "self-proj")
	warrenRoot := testWarrenRoot(t, "wp", "self-proj", "other-proj")

	stdout, _, code := captureRunBoth(t, []string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot})
	if code != 0 {
		t.Fatalf("register exit code = %d", code)
	}
	if !strings.Contains(stdout, `project "self-proj" in warren "wp" matches this workspace's vault ID "self-proj"`) {
		t.Fatalf("register did not announce identity: %q", stdout)
	}
	if !strings.Contains(stdout, "activate once their other endpoint is mounted") {
		t.Fatalf("identity note missing the bridge-activation hint: %q", stdout)
	}
	if strings.Contains(stdout, `"other-proj" in warren "wp" matches`) {
		t.Fatalf("foreign project announced as identity: %q", stdout)
	}

	// A warren with no matching project registers silently.
	foreignRoot := testWarrenRoot(t, "wq", "unrelated")
	stdout, _, code = captureRunBoth(t, []string{"warren", "register", "--dir", marmotDir, "wq", foreignRoot})
	if code != 0 {
		t.Fatalf("register wq exit code = %d", code)
	}
	if strings.Contains(stdout, "matches this workspace's vault ID") {
		t.Fatalf("identity announced without a match: %q", stdout)
	}
}

// TestWarrenStatusShowsIdentityState (R2.6): the status table's STATE column
// reports "identity" for identified projects (mounted or not) with the live
// vault path, while foreign dormant projects stay "dormant".
func TestWarrenStatusShowsIdentityState(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	writeIdentityConfig(t, marmotDir, "self-proj")
	warrenRoot := testWarrenRoot(t, "wp", "self-proj", "other-proj")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}

	stdout, _, code := captureRunBoth(t, []string{"warren", "status", "--dir", marmotDir, "--warren", "wp"})
	if code != 0 {
		t.Fatalf("status exit code = %d", code)
	}
	var selfLine, otherLine string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "self-proj") {
			selfLine = line
		}
		if strings.HasPrefix(line, "other-proj") {
			otherLine = line
		}
	}
	if !strings.Contains(selfLine, "identity") {
		t.Fatalf("self-proj row = %q, want identity state (full output %q)", selfLine, stdout)
	}
	if !strings.Contains(selfLine, marmotDir) {
		t.Fatalf("self-proj row = %q, want the live vault path %q", selfLine, marmotDir)
	}
	if !strings.Contains(otherLine, "dormant") {
		t.Fatalf("other-proj row = %q, want dormant", otherLine)
	}

	// --json carries the additive self_alias flag and the corrected path.
	stdout, _, code = captureRunBoth(t, []string{"warren", "status", "--dir", marmotDir, "--warren", "wp", "--json"})
	if code != 0 {
		t.Fatalf("status --json exit code = %d", code)
	}
	if !strings.Contains(stdout, `"self_alias": true`) {
		t.Fatalf("status --json missing self_alias: %q", stdout)
	}
}

// TestWarrenListIdentifiedColumn (R2.6): warren list renders the IDENTITY
// column ("-" when none) and --json grafts the additive identified_projects
// field.
func TestWarrenListIdentifiedColumn(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	writeIdentityConfig(t, marmotDir, "self-proj")
	warrenRoot := testWarrenRoot(t, "wp", "self-proj", "other-proj")
	foreignRoot := testWarrenRoot(t, "wq", "unrelated")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register wp failed")
	}
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wq", foreignRoot}); code != 0 {
		t.Fatal("register wq failed")
	}

	stdout, _, code := captureRunBoth(t, []string{"warren", "list", "--dir", marmotDir})
	if code != 0 {
		t.Fatalf("list exit code = %d", code)
	}
	if !strings.Contains(stdout, "IDENTITY") {
		t.Fatalf("list table missing IDENTITY column: %q", stdout)
	}
	var wpLine, wqLine string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "wp") {
			wpLine = line
		}
		if strings.HasPrefix(line, "wq") {
			wqLine = line
		}
	}
	if !strings.Contains(wpLine, "self-proj") {
		t.Fatalf("wp row = %q, want identified project listed", wpLine)
	}
	if !strings.HasSuffix(strings.TrimSpace(wqLine), "-") {
		t.Fatalf("wq row = %q, want '-' identity placeholder", wqLine)
	}

	stdout, _, code = captureRunBoth(t, []string{"warren", "list", "--dir", marmotDir, "--json"})
	if code != 0 {
		t.Fatalf("list --json exit code = %d", code)
	}
	if !strings.Contains(stdout, `"identified_projects"`) || !strings.Contains(stdout, `"self-proj"`) {
		t.Fatalf("list --json missing identified_projects: %q", stdout)
	}
}

// TestWarrenProposeRefusesIdentified (R2.5): an explicit propose of the
// identified project refuses — its live context never lands in the warren
// checkout, so a pathspec-limited commit would be meaningless.
func TestWarrenProposeRefusesIdentified(t *testing.T) {
	gitTestEnv(t)
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	writeIdentityConfig(t, marmotDir, "self-proj")
	warrenRoot := testWarrenRoot(t, "wp", "self-proj", "other-proj")
	gitInitCommit(t, warrenRoot)
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatal("register failed")
	}

	_, stderr, code := captureRunBoth(t, []string{"warren", "propose", "--dir", marmotDir, "--warren", "wp", "self-proj"})
	if code != 1 {
		t.Fatalf("propose identified project exit = %d, want 1 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, `project "self-proj" is this workspace`) || !strings.Contains(stderr, "project remove + project import") {
		t.Fatalf("propose refusal = %q, want identity message with re-import remediation", stderr)
	}
	// The refusal happened before any branch was created.
	branches := gitRun(t, warrenRoot, "branch", "--list", "marmot/propose/*")
	if strings.TrimSpace(branches) != "" {
		t.Fatalf("propose refusal left branches behind: %q", branches)
	}
}

// TestWarrenRefreshPullSkipsLegacySelfCache (R2.5): R1-era state — the self
// project recorded active with a legacy burrow cache — must not brick
// refresh --pull after upstream moves: the self cache is skipped with the
// drop hint while foreign caches re-materialize.
func TestWarrenRefreshPullSkipsLegacySelfCache(t *testing.T) {
	gitTestEnv(t)
	src := testWarrenRoot(t, "wp", "self-proj", "other-proj")
	noteSrc := filepath.Join(src, "projects", "other-proj", ".marmot", "notes.md")
	if err := os.WriteFile(noteSrc, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInitCommit(t, src)
	clone := filepath.Join(t.TempDir(), "clone")
	gitRun(t, filepath.Dir(clone), "clone", src, clone)

	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	writeIdentityConfig(t, marmotDir, "self-proj")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", clone}); code != 0 {
		t.Fatal("register failed")
	}
	if code := run([]string{"warren", "burrow", "--dir", marmotDir, "--warren", "wp", "other-proj"}); code != 0 {
		t.Fatal("burrow other-proj failed")
	}
	// Hand-write the R1-era legacy shape: the self project active with a
	// burrow cache (Mount and Materialize both refuse to create it today).
	selfCache := filepath.Join(marmotDir, ".marmot-data", "warrens", "wp", "projects", "self-proj", ".marmot")
	if err := os.MkdirAll(selfCache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := warrenpkg.SaveProjectMetadata(selfCache, &warrenpkg.ProjectMetadata{
		ProjectID: "self-proj",
		WarrenID:  "wp",
		VaultID:   "self-proj",
	}, ""); err != nil {
		t.Fatal(err)
	}
	state, body, err := warrenpkg.LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatal(err)
	}
	entry := state.Warrens["wp"]
	entry.ActiveProjects = append(entry.ActiveProjects, "self-proj")
	state.Warrens["wp"] = entry
	if err := warrenpkg.SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatal(err)
	}

	// Upstream moves so every cache is stale.
	if err := os.WriteFile(noteSrc, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, "add", "-A")
	gitRun(t, src, "commit", "-m", "update notes")

	stdout, stderr, code := captureRunBoth(t, []string{"warren", "refresh", "--dir", marmotDir, "--warren", "wp", "--pull"})
	if code != 0 {
		t.Fatalf("refresh --pull with legacy self cache exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, `burrow cache for "self-proj" shadows this workspace's own vault`) || !strings.Contains(stderr, "burrow --drop") {
		t.Fatalf("missing legacy self-cache skip warning: %q", stderr)
	}
	if !strings.Contains(stdout, "Re-materialized burrow cache(s): other-proj") {
		t.Fatalf("foreign cache not re-materialized: %q", stdout)
	}
	cachedNote := filepath.Join(marmotDir, ".marmot-data", "warrens", "wp", "projects", "other-proj", ".marmot", "notes.md")
	if data, err := os.ReadFile(cachedNote); err != nil || string(data) != "v2\n" {
		t.Fatalf("other-proj cache not refreshed: %q err=%v", data, err)
	}
}
