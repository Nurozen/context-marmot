package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// denLinkFixture creates a den (with identity vault) plus a warren checkout
// registered in the den vault's warren workspace state, and returns the vault
// dir and warren root. Registration writes the vault's _warren.md directly —
// the state-level equivalent of `warren register <id> <path> --dir <vault>`,
// whose den-vault routing is covered by warren_denvault_test.go.
func denLinkFixture(t *testing.T, denID, warrenID string, projects ...string) (vaultDir, warrenRoot string) {
	t.Helper()
	if _, err := den.Create(denID, den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatalf("den.Create: %v", err)
	}
	vaultDir = den.VaultPath(denID)
	warrenRoot = testWarrenRoot(t, warrenID, projects...)
	state, body, err := warrenpkg.LoadWorkspaceStateFromMarmot(vaultDir)
	if err != nil {
		t.Fatalf("LoadWorkspaceStateFromMarmot: %v", err)
	}
	if state.Warrens == nil {
		state.Warrens = map[string]warrenpkg.WorkspaceWarren{}
	}
	entry := state.Warrens[warrenID]
	entry.Path = warrenRoot
	state.Warrens[warrenID] = entry
	if err := warrenpkg.SaveWorkspaceStateToMarmot(vaultDir, state, body); err != nil {
		t.Fatalf("SaveWorkspaceStateToMarmot: %v", err)
	}
	return vaultDir, warrenRoot
}

type denLinkEnvelope struct {
	Schema int    `json:"schema"`
	DenID  string `json:"den_id"`
	Link   struct {
		Target  string `json:"target"`
		Mode    string `json:"mode"`
		Warren  string `json:"warren"`
		Project string `json:"project"`
	} `json:"link"`
	Warnings []string `json:"warnings"`
}

func TestDenLinkEditHappyPathAndIdempotency(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, _ := denLinkFixture(t, "link-den", "product-platform", "project-a")

	out, code := captureRun([]string{"den", "link", "link-den", "--edit", "product-platform/project-a", "--json"})
	if code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}
	var env denLinkEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope parse: %v out=%s", err, out)
	}
	if env.Schema != 1 || env.DenID != "link-den" {
		t.Fatalf("envelope: %+v", env)
	}
	if env.Link.Target != "product-platform/project-a" || env.Link.Mode != "edit" ||
		env.Link.Warren != "product-platform" || env.Link.Project != "project-a" {
		t.Fatalf("link body: %+v", env.Link)
	}
	if env.Warnings == nil || len(env.Warnings) != 0 {
		t.Fatalf("warnings must be present and empty: %+v", env.Warnings)
	}

	// The point of the verb: ActiveMounts on the den vault sees an editable mount.
	mounts, err := warrenpkg.ActiveMounts(vaultDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	found := false
	for _, m := range mounts {
		if m.WarrenID == "product-platform" && m.ProjectID == "project-a" {
			found = true
			if !m.Active || !m.Editable {
				t.Fatalf("mount not active+editable: %+v", m)
			}
		}
	}
	if !found {
		t.Fatalf("project-a not in ActiveMounts: %+v", mounts)
	}

	// Den manifest carries the edit link (what den contribute checks).
	info, err := den.Status("link-den")
	if err != nil {
		t.Fatalf("den.Status: %v", err)
	}
	if len(info.Links) != 1 || info.Links[0].Mode != "edit" || info.Links[0].Target != "product-platform/project-a" {
		t.Fatalf("den links: %+v", info.Links)
	}

	// Idempotent: second run succeeds with a warning and no duplicate link.
	out, code = captureRun([]string{"den", "link", "link-den", "--edit", "product-platform/project-a", "--json"})
	if code != 0 {
		t.Fatalf("second den link: code=%d out=%s", code, out)
	}
	env = denLinkEnvelope{}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("second envelope: %v out=%s", err, out)
	}
	if len(env.Warnings) != 1 || env.Warnings[0] != "already linked" {
		t.Fatalf("expected already-linked warning: %+v", env.Warnings)
	}
	info, err = den.Status("link-den")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Links) != 1 {
		t.Fatalf("links deduped: %+v", info.Links)
	}
}

func TestDenLinkPlainText(t *testing.T) {
	hermeticDenCLI(t)
	denLinkFixture(t, "plain-link", "w", "p")
	out, code := captureRun([]string{"den", "link", "plain-link", "--edit", "w/p"})
	if code != 0 || !strings.Contains(out, "Linked den") {
		t.Fatalf("plain link: code=%d out=%s", code, out)
	}
}

func TestDenLinkWarrenNotRegistered(t *testing.T) {
	hermeticDenCLI(t)
	denLinkFixture(t, "unreg-den", "w", "p")
	out, code := captureRun([]string{"den", "link", "unreg-den", "--edit", "other/p", "--json"})
	if code == 0 || !strings.Contains(out, "warren_not_registered") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "marmot warren register other") {
		t.Fatalf("expected register hint: %s", out)
	}
	// Plain text path.
	if code := run([]string{"den", "link", "unreg-den", "--edit", "other/p"}); code == 0 {
		t.Fatal("plain warren_not_registered should fail")
	}
}

func TestDenLinkProjectNotFound(t *testing.T) {
	hermeticDenCLI(t)
	denLinkFixture(t, "proj-den", "w", "p")
	out, code := captureRun([]string{"den", "link", "proj-den", "--edit", "w/nope", "--json"})
	if code == 0 || !strings.Contains(out, "project_not_found") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "registered projects: p") {
		t.Fatalf("expected known-projects hint: %s", out)
	}
}

func TestDenLinkReadonlyProject(t *testing.T) {
	hermeticDenCLI(t)
	_, warrenRoot := denLinkFixture(t, "ro-den", "w", "p")
	if _, err := warrenpkg.SetProjectReadOnly(warrenRoot, "p", true); err != nil {
		t.Fatalf("SetProjectReadOnly: %v", err)
	}
	out, code := captureRun([]string{"den", "link", "ro-den", "--edit", "w/p", "--json"})
	if code == 0 || !strings.Contains(out, "readonly_refused") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// No state or manifest mutation happened.
	info, err := den.Status("ro-den")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Links) != 0 {
		t.Fatalf("readonly refusal must not append links: %+v", info.Links)
	}
}

func TestDenLinkVaultRequired(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("no-vault-den", den.CreateOptions{Lifetime: den.LifetimeTask, NoVault: true}); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "link", "no-vault-den", "--edit", "w/p", "--json"})
	if code == 0 || !strings.Contains(out, "vault_required") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "--no-vault") {
		t.Fatalf("expected no-vault hint: %s", out)
	}
	// Plain text path.
	if code := run([]string{"den", "link", "no-vault-den", "--edit", "w/p"}); code == 0 {
		t.Fatal("plain vault_required should fail")
	}
}

func TestDenLinkDryRunTouchesNothing(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, _ := denLinkFixture(t, "dry-den", "w", "p")

	out, code := captureRun([]string{"den", "link", "dry-den", "--edit", "w/p", "--dry-run", "--json"})
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
	if env.Schema != 1 || !env.DryRun || len(env.Ops) != 2 {
		t.Fatalf("dry-run env: %+v", env)
	}

	// Nothing was written: no active mounts, no den links.
	mounts, err := warrenpkg.ActiveMounts(vaultDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 0 {
		t.Fatalf("dry-run must not mount: %+v", mounts)
	}
	info, err := den.Status("dry-den")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Links) != 0 {
		t.Fatalf("dry-run must not append links: %+v", info.Links)
	}

	// Plain dry-run prints dry-run: lines.
	out, code = captureRun([]string{"den", "link", "dry-den", "--edit", "w/p", "--dry-run"})
	if code != 0 || !strings.Contains(out, "dry-run:") {
		t.Fatalf("plain dry-run: code=%d out=%s", code, out)
	}
}

func TestDenLinkInvalidArgs(t *testing.T) {
	hermeticDenCLI(t)

	// Missing den id.
	out, code := captureRun([]string{"den", "link", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("missing id: code=%d out=%s", code, out)
	}

	// Missing --edit.
	denLinkFixture(t, "args-den", "w", "p")
	out, code = captureRun([]string{"den", "link", "args-den", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("missing --edit: code=%d out=%s", code, out)
	}

	// Malformed targets.
	for _, target := range []string{"wp", "w/", "/p", "a/b/c"} {
		out, code = captureRun([]string{"den", "link", "args-den", "--edit", target, "--json"})
		if code == 0 || !strings.Contains(out, "invalid_args") {
			t.Fatalf("target %q: code=%d out=%s", target, code, out)
		}
	}

	// Missing den.
	out, code = captureRun([]string{"den", "link", "no-such-den", "--edit", "w/p", "--json"})
	if code == 0 || !strings.Contains(out, "den_not_found") {
		t.Fatalf("missing den: code=%d out=%s", code, out)
	}

	// Plain-text missing --edit.
	if code := run([]string{"den", "link", "args-den"}); code == 0 {
		t.Fatal("plain missing --edit should fail")
	}
}
