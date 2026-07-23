package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// TestWarrenRegisterDenVaultDir: `warren register --dir <den-vault>` must
// write the workspace state directly into the vault (<vault>/_warren.md, the
// file warren.ActiveMounts reads for den serves) — not to
// <den-root>/.marmot/_warren.md, which nothing ever reads. This is the
// registration step behind `den link --edit`'s warren_not_registered hint.
func TestWarrenRegisterDenVaultDir(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("reg-vault-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatalf("den.Create: %v", err)
	}
	vault := den.VaultPath("reg-vault-den")
	if _, err := os.Stat(filepath.Join(vault, "_config.md")); err != nil {
		t.Fatalf("fixture: vault has no _config.md: %v", err)
	}
	warrenRoot := testWarrenRoot(t, "wp", "project-a")

	if code := run([]string{"warren", "register", "--dir", vault, "wp", warrenRoot}); code != 0 {
		t.Fatalf("warren register --dir <vault>: exit %d", code)
	}

	// State lands directly in the vault.
	if _, err := os.Stat(filepath.Join(vault, "_warren.md")); err != nil {
		t.Fatalf("state not written at <vault>/_warren.md: %v", err)
	}
	// And NOT at <den-root>/.marmot/_warren.md (the old, dead location).
	strayState := filepath.Join(filepath.Dir(vault), ".marmot", "_warren.md")
	if _, err := os.Stat(strayState); err == nil {
		t.Fatalf("stray state planted at %s", strayState)
	}

	// The engine-side reader sees the registration.
	state, _, err := warrenpkg.LoadWorkspaceStateFromMarmot(vault)
	if err != nil {
		t.Fatalf("LoadWorkspaceStateFromMarmot: %v", err)
	}
	entry, ok := state.Warrens["wp"]
	if !ok {
		t.Fatalf("warren wp not in vault state: %+v", state.Warrens)
	}
	absRoot, err := filepath.Abs(warrenRoot)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path != absRoot {
		t.Fatalf("entry path = %q, want %q", entry.Path, absRoot)
	}

	// warren list against the vault dir sees it too.
	out, code := captureRun([]string{"warren", "list", "--dir", vault})
	if code != 0 {
		t.Fatalf("warren list --dir <vault>: exit %d, out=%s", code, out)
	}
	if !strings.Contains(out, "wp") || !strings.Contains(out, warrenRoot) {
		t.Fatalf("warren list output missing registration: %s", out)
	}
}

// TestWarrenRegisterDenVaultManifestMismatch: the direct-state register path
// must keep RegisterWorkspaceWarren's manifest validation (and its exact
// error wording).
func TestWarrenRegisterDenVaultManifestMismatch(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("mismatch-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatalf("den.Create: %v", err)
	}
	vault := den.VaultPath("mismatch-den")
	warrenRoot := testWarrenRoot(t, "actual-id", "p")

	_, stderr, code := captureRunBoth(t, []string{"warren", "register", "--dir", vault, "claimed-id", warrenRoot})
	if code == 0 {
		t.Fatal("register with mismatched warren ID should fail")
	}
	if !strings.Contains(stderr, "warren ID mismatch") {
		t.Fatalf("expected warren ID mismatch error, got: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(vault, "_warren.md")); err == nil {
		t.Fatal("failed register must not write vault state")
	}
}

// denVaultWithWarren builds the shared den-vault fixture: a hermetic den
// whose identity vault has warrenID registered (direct state at
// <vault>/_warren.md) pointing at a fresh test warren checkout.
func denVaultWithWarren(t *testing.T, denID, warrenID string, projects ...string) (vault, warrenRoot string) {
	t.Helper()
	hermeticDenCLI(t)
	if _, err := den.Create(denID, den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatalf("den.Create: %v", err)
	}
	vault = den.VaultPath(denID)
	warrenRoot = testWarrenRoot(t, warrenID, projects...)
	if code := run([]string{"warren", "register", "--dir", vault, warrenID, warrenRoot}); code != 0 {
		t.Fatalf("warren register --dir <vault>: exit %d", code)
	}
	return vault, warrenRoot
}

// denVaultState loads the direct state a den serve actually reads.
func denVaultState(t *testing.T, vault string) *warrenpkg.WorkspaceState {
	t.Helper()
	state, _, err := warrenpkg.LoadWorkspaceStateFromMarmot(vault)
	if err != nil {
		t.Fatalf("LoadWorkspaceStateFromMarmot: %v", err)
	}
	return state
}

// requireNoStrayState fails when a verb planted state at the dead classic
// location <den-root>/.marmot/_warren.md instead of the vault.
func requireNoStrayState(t *testing.T, vault string) {
	t.Helper()
	stray := filepath.Join(filepath.Dir(vault), ".marmot", "_warren.md")
	if _, err := os.Stat(stray); err == nil {
		t.Fatalf("stray state planted at %s", stray)
	}
}

// TestWarrenStatusDenVaultDir (W1 repro): status must read the same direct
// state list reads — a warren registered in the vault is visible to status,
// not reported as "not registered".
func TestWarrenStatusDenVaultDir(t *testing.T) {
	vault, _ := denVaultWithWarren(t, "status-den", "wp", "project-a")
	stdout, stderr, code := captureRunBoth(t, []string{"warren", "status", "--dir", vault, "--warren", "wp"})
	if code != 0 {
		t.Fatalf("warren status --dir <vault>: exit %d, stderr=%s", code, stderr)
	}
	if strings.Contains(stderr, "not registered") {
		t.Fatalf("status contradicts list about registration: %s", stderr)
	}
	if !strings.Contains(stdout, "project-a") || !strings.Contains(stdout, "dormant") {
		t.Fatalf("status output missing dormant project row: %s", stdout)
	}
}

// TestWarrenMountDenVaultDir: mount records ActiveProjects in the vault's own
// _warren.md, and status then reports the project mounted.
func TestWarrenMountDenVaultDir(t *testing.T) {
	vault, _ := denVaultWithWarren(t, "mount-den", "wp", "project-a")
	if code := run([]string{"warren", "mount", "--dir", vault, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatalf("warren mount --dir <vault>: exit %d", code)
	}
	requireNoStrayState(t, vault)
	state := denVaultState(t, vault)
	entry, ok := state.Warrens["wp"]
	if !ok {
		t.Fatalf("warren wp missing from vault state: %+v", state.Warrens)
	}
	if len(entry.ActiveProjects) != 1 || entry.ActiveProjects[0] != "project-a" {
		t.Fatalf("ActiveProjects = %v, want [project-a]", entry.ActiveProjects)
	}
	stdout, _, code := captureRunBoth(t, []string{"warren", "status", "--dir", vault, "--warren", "wp"})
	if code != 0 || !strings.Contains(stdout, "mounted") {
		t.Fatalf("status after mount = code %d stdout %q, want mounted row", code, stdout)
	}
}

// TestWarrenEditDenVaultDir: edit (set-editable) writes EditableProjects into
// the vault state (with the implied mount), and --off clears it again.
func TestWarrenEditDenVaultDir(t *testing.T) {
	vault, _ := denVaultWithWarren(t, "edit-den", "wp", "project-a")
	if code := run([]string{"warren", "edit", "--dir", vault, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatalf("warren edit --dir <vault>: exit %d", code)
	}
	requireNoStrayState(t, vault)
	entry := denVaultState(t, vault).Warrens["wp"]
	if len(entry.EditableProjects) != 1 || entry.EditableProjects[0] != "project-a" {
		t.Fatalf("EditableProjects = %v, want [project-a]", entry.EditableProjects)
	}
	if len(entry.ActiveProjects) != 1 || entry.ActiveProjects[0] != "project-a" {
		t.Fatalf("edit implies mount: ActiveProjects = %v, want [project-a]", entry.ActiveProjects)
	}
	if code := run([]string{"warren", "edit", "--dir", vault, "--warren", "wp", "--off", "project-a"}); code != 0 {
		t.Fatalf("warren edit --off --dir <vault>: exit %d", code)
	}
	if entry := denVaultState(t, vault).Warrens["wp"]; len(entry.EditableProjects) != 0 {
		t.Fatalf("EditableProjects after --off = %v, want empty", entry.EditableProjects)
	}
}

// TestWarrenUnmountDenVaultDir: unmount clears ActiveProjects in the vault
// state instead of failing against a phantom <den-root>/.marmot state.
func TestWarrenUnmountDenVaultDir(t *testing.T) {
	vault, _ := denVaultWithWarren(t, "unmount-den", "wp", "project-a")
	if code := run([]string{"warren", "mount", "--dir", vault, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatalf("mount: exit %d", code)
	}
	if code := run([]string{"warren", "unmount", "--dir", vault, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatalf("warren unmount --dir <vault>: exit %d", code)
	}
	requireNoStrayState(t, vault)
	if entry := denVaultState(t, vault).Warrens["wp"]; len(entry.ActiveProjects) != 0 {
		t.Fatalf("ActiveProjects after unmount = %v, want empty", entry.ActiveProjects)
	}
}

// TestWarrenUnregisterDenVaultDir: unregister refuses while a project is
// mounted (reading the vault state, not a phantom one) and then removes the
// entry from the vault state.
func TestWarrenUnregisterDenVaultDir(t *testing.T) {
	vault, _ := denVaultWithWarren(t, "unreg-den", "wp", "project-a")
	if code := run([]string{"warren", "mount", "--dir", vault, "--warren", "wp", "project-a"}); code != 0 {
		t.Fatalf("mount: exit %d", code)
	}
	_, stderr, code := captureRunBoth(t, []string{"warren", "unregister", "--dir", vault, "--warren", "wp"})
	if code == 0 || !strings.Contains(stderr, "mounted project(s)") {
		t.Fatalf("unregister with mounted project = code %d stderr %q, want refusal", code, stderr)
	}
	if code := run([]string{"warren", "unmount", "--dir", vault, "--warren", "wp", "--all"}); code != 0 {
		t.Fatalf("unmount --all: exit %d", code)
	}
	if code := run([]string{"warren", "unregister", "--dir", vault, "--warren", "wp"}); code != 0 {
		t.Fatalf("warren unregister --dir <vault>: exit %d", code)
	}
	requireNoStrayState(t, vault)
	if state := denVaultState(t, vault); len(state.Warrens) != 0 {
		t.Fatalf("Warrens after unregister = %+v, want empty", state.Warrens)
	}
}

// TestWarrenRegisterWorkspaceDirUnchanged: classic `--dir <repo>/.marmot`
// registration keeps resolving state through the workspace root — the same
// file both by-root and by-marmot-dir readers agree on.
func TestWarrenRegisterWorkspaceDirUnchanged(t *testing.T) {
	root := t.TempDir()
	marmotDir := filepath.Join(root, ".marmot")
	warrenRoot := testWarrenRoot(t, "wp", "project-a")

	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatalf("warren register: exit %d", code)
	}

	if _, err := os.Stat(filepath.Join(marmotDir, "_warren.md")); err != nil {
		t.Fatalf("state not at <root>/.marmot/_warren.md: %v", err)
	}
	state, _, err := warrenpkg.LoadWorkspaceState(root)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	if _, ok := state.Warrens["wp"]; !ok {
		t.Fatalf("warren wp not in workspace state: %+v", state.Warrens)
	}

	out, code := captureRun([]string{"warren", "list", "--dir", marmotDir})
	if code != 0 || !strings.Contains(out, "wp") {
		t.Fatalf("warren list: code=%d out=%s", code, out)
	}
}
