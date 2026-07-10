package warren

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/routes"
)

// --- R1.1: the alias predicate ---------------------------------------------

// TestLocalVaultID: the exported probe returns the trimmed vault_id, and ""
// on a missing file, unparseable frontmatter, or an absent key — the
// empty-string edge case is load-bearing (an empty local ID aliases nothing).
func TestLocalVaultID(t *testing.T) {
	dir := t.TempDir()
	if got := LocalVaultID(dir); got != "" {
		t.Fatalf("missing _config.md: LocalVaultID = %q, want \"\"", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte("no frontmatter here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LocalVaultID(dir); got != "" {
		t.Fatalf("unparseable _config.md: LocalVaultID = %q, want \"\"", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte("---\nversion: \"1\"\nnamespace: default\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LocalVaultID(dir); got != "" {
		t.Fatalf("absent vault_id key: LocalVaultID = %q, want \"\"", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte("---\nversion: \"1\"\nvault_id: \" my-vault \"\nnamespace: default\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LocalVaultID(dir); got != "my-vault" {
		t.Fatalf("LocalVaultID = %q, want trimmed \"my-vault\"", got)
	}
}

// TestActiveMountsMarksSelfAlias: the mount whose vault_id matches the local
// vault reports SelfAlias=true and Editable=false even when listed in
// EditableProjects (legacy state); all other mounts stay SelfAlias=false.
func TestActiveMountsMarksSelfAlias(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a", "project-b")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	if _, err := Mount(workspace, "product-platform", []string{"project-a", "project-b"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	// Legacy editable state written by an older binary: hand-edit past
	// SetEditable's refusal.
	state, body, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	entry.EditableProjects = []string{"project-a"}
	state.Warrens["product-platform"] = entry
	if err := SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	mounts, err := ActiveMounts(workspaceMarmotDir(workspace))
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 2 {
		t.Fatalf("mounts = %+v, want 2", mounts)
	}
	byProject := make(map[string]ProjectStatus, len(mounts))
	for _, mount := range mounts {
		byProject[mount.ProjectID] = mount
	}
	self := byProject["project-a"]
	if !self.SelfAlias {
		t.Fatalf("project-a mount = %+v, want SelfAlias=true", self)
	}
	if self.Editable {
		t.Fatalf("self-alias mount reports Editable=true despite the alias forcing: %+v", self)
	}
	other := byProject["project-b"]
	if other.SelfAlias {
		t.Fatalf("project-b mount = %+v, want SelfAlias=false", other)
	}
}

// --- R1.2: alias-aware gating, unconditional refusal ------------------------

// TestMountSelfAliasRefusesMaterialize: a self-alias serves from the live
// vault; a materialized cache would be a stale shadow, so --materialize
// refuses.
func TestMountSelfAliasRefusesMaterialize(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")

	_, err := Mount(workspace, "product-platform", []string{"project-a"}, true)
	if err == nil || !strings.Contains(err.Error(), "cannot be materialized") {
		t.Fatalf("Mount --materialize err = %v, want self-alias refusal", err)
	}
	state, _, loadErr := LoadWorkspaceState(workspace)
	if loadErr != nil {
		t.Fatalf("LoadWorkspaceState: %v", loadErr)
	}
	if got := state.Warrens["product-platform"].ActiveProjects; len(got) != 0 {
		t.Fatalf("refused mount still activated projects: %v", got)
	}
}

// TestMountSelfAliasRefusesWhenEditable: legacy EditableProjects state on a
// self project refuses the mount and names the --off escape hatch.
func TestMountSelfAliasRefusesWhenEditable(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	state, body, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	entry.EditableProjects = []string{"project-a"}
	state.Warrens["product-platform"] = entry
	if err := SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	_, err = Mount(workspace, "product-platform", []string{"project-a"}, false)
	if err == nil || !strings.Contains(err.Error(), "--off") || !strings.Contains(err.Error(), "edit it directly in this workspace") {
		t.Fatalf("Mount err = %v, want editable self-alias refusal naming --off", err)
	}
}

// TestSetEditableRefusesSelfAlias: warren edit refuses on a self-alias with
// the edit-locally message; --off stays allowed (the legacy-state escape
// hatch) and still auto-mounts an unmounted self project as an alias.
func TestSetEditableRefusesSelfAlias(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")

	_, err := SetEditable(workspace, "product-platform", "project-a", true)
	if err == nil || !strings.Contains(err.Error(), "alias of the live vault") {
		t.Fatalf("SetEditable err = %v, want self-alias refusal", err)
	}

	// --off is allowed and becomes an alias mount (no collision refusal).
	state, err := SetEditable(workspace, "product-platform", "project-a", false)
	if err != nil {
		t.Fatalf("SetEditable --off on self project: %v", err)
	}
	entry := state.Warrens["product-platform"]
	if len(entry.EditableProjects) != 0 {
		t.Fatalf("--off left editable flags: %v", entry.EditableProjects)
	}
	if len(entry.ActiveProjects) != 1 || entry.ActiveProjects[0] != "project-a" {
		t.Fatalf("--off auto-mount missing: active = %v", entry.ActiveProjects)
	}
}

// TestMaterializeRefusesSelfAlias: the direct Materialize entry point (the
// CLI's post-mount loop and refresh --pull) refuses to cache the workspace's
// own vault.
func TestMaterializeRefusesSelfAlias(t *testing.T) {
	workspace, warrenRoot := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")

	project := Project{ProjectID: "project-a", Path: filepath.ToSlash(filepath.Join("projects", "project-a", ".marmot"))}
	_, err := Materialize(workspaceMarmotDir(workspace), "product-platform", project, warrenRoot, "")
	if err == nil || !strings.Contains(err.Error(), "stale shadow of the live vault") {
		t.Fatalf("Materialize err = %v, want self-alias refusal", err)
	}
	if dirExists(materializedProjectPath(workspaceMarmotDir(workspace), "product-platform", "project-a")) {
		t.Fatal("refused Materialize left a cache behind")
	}
}

// TestRefuseVaultIDCollisionUnconditional: the local claim (WarrenID == "")
// now refuses instead of warning — defense for callers that skip the alias
// short-circuit.
func TestRefuseVaultIDCollisionUnconditional(t *testing.T) {
	claimed := map[string]vaultClaim{
		"shared-vault": {ProjectID: "the local workspace vault"},
	}
	err := refuseVaultIDCollision(claimed, "shared-vault", "wp", "proj")
	if err == nil || !strings.Contains(err.Error(), "collides with the local workspace vault") {
		t.Fatalf("local-claim collision err = %v, want unconditional refusal", err)
	}

	// Cross-warren claims keep the warren/project owner formatting.
	claimed = map[string]vaultClaim{
		"shared-vault": {WarrenID: "warren-a", ProjectID: "project-x"},
	}
	err = refuseVaultIDCollision(claimed, "shared-vault", "wp", "proj")
	if err == nil || !strings.Contains(err.Error(), "collides with warren-a/project-x") {
		t.Fatalf("cross-warren collision err = %v", err)
	}

	// Same claimant and unclaimed IDs stay allowed.
	if err := refuseVaultIDCollision(claimed, "shared-vault", "warren-a", "project-x"); err != nil {
		t.Fatalf("self re-claim refused: %v", err)
	}
	if err := refuseVaultIDCollision(claimed, "fresh-vault", "wp", "proj"); err != nil {
		t.Fatalf("unclaimed vault refused: %v", err)
	}
}

// --- R1.6: DoctorWorkspace alignment ----------------------------------------

// TestDoctorWorkspaceSelfAliasHealthy: a plain self-alias mount is healthy —
// no error-severity issues, one self_alias_mount info.
func TestDoctorWorkspaceSelfAliasHealthy(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	report, err := DoctorWorkspace(workspaceMarmotDir(workspace), workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	if !report.OK() {
		t.Fatalf("plain self-alias must be healthy, got %+v", report.Issues)
	}
	if findIssue(report, "vault_id_collision_workspace") != nil {
		t.Fatalf("self-alias reported as collision: %+v", report.Issues)
	}
	issue := findIssue(report, "self_alias_mount")
	if issue == nil {
		t.Fatalf("issues = %+v, want self_alias_mount info", report.Issues)
	}
	if issue.Severity != "info" || !strings.Contains(issue.Message, "serves from the live vault") {
		t.Fatalf("self_alias_mount issue = %+v", issue)
	}
}

// TestDoctorWorkspaceSelfAliasEditableError: legacy editable self-mount state
// is the split-brain error, with the --off remediation.
func TestDoctorWorkspaceSelfAliasEditableError(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	state, body, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	entry.EditableProjects = []string{"project-a"}
	state.Warrens["product-platform"] = entry
	if err := SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	report, err := DoctorWorkspace(workspaceMarmotDir(workspace), workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	issue := findIssue(report, "self_alias_editable")
	if issue == nil {
		t.Fatalf("issues = %+v, want self_alias_editable", report.Issues)
	}
	if issue.Severity != "error" || !strings.Contains(issue.Message, "--off") {
		t.Fatalf("self_alias_editable issue = %+v, want error naming --off", issue)
	}
	if report.OK() {
		t.Error("editable self-alias must fail doctor")
	}
}

// TestDoctorWorkspaceSelfAliasMaterializedWarns: a legacy burrow cache
// shadowing the live vault warns (never errors) with the burrow --drop
// remediation.
func TestDoctorWorkspaceSelfAliasMaterializedWarns(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	// A legacy cache dir written before the Materialize refusal existed.
	cached := materializedProjectPath(workspaceMarmotDir(workspace), "product-platform", "project-a")
	if err := os.MkdirAll(cached, 0o755); err != nil {
		t.Fatal(err)
	}

	report, err := DoctorWorkspace(workspaceMarmotDir(workspace), workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	issue := findIssue(report, "self_alias_materialized")
	if issue == nil {
		t.Fatalf("issues = %+v, want self_alias_materialized", report.Issues)
	}
	if issue.Severity != "warning" || !strings.Contains(issue.Message, "burrow --drop") {
		t.Fatalf("self_alias_materialized issue = %+v, want warning naming burrow --drop", issue)
	}
	if !report.OK() {
		t.Errorf("stale cache shadow must warn, not error: %+v", report.Issues)
	}
}

// TestDoctorLocalRouteMismatch: a routes.yml entry mapping the local vault ID
// elsewhere warns (two checkouts of one repo legitimately share a vault_id);
// a route to this workspace's own .marmot stays silent.
func TestDoctorLocalRouteMismatch(t *testing.T) {
	routes.SetOverridePath(filepath.Join(t.TempDir(), "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "my-vault")
	marmotDir := workspaceMarmotDir(workspace)

	// Correct peer-facing route: silent.
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		abs, err := filepath.Abs(marmotDir)
		if err != nil {
			return err
		}
		rt.Set("my-vault", abs)
		return nil
	}); err != nil {
		t.Fatalf("routes.Update: %v", err)
	}
	report, err := DoctorWorkspace(marmotDir, workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	if findIssue(report, "local_route_mismatch") != nil {
		t.Fatalf("route to our own .marmot must not warn: %+v", report.Issues)
	}

	// Route pointing elsewhere: warning, never an error.
	elsewhere := t.TempDir()
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.Set("my-vault", elsewhere)
		return nil
	}); err != nil {
		t.Fatalf("routes.Update: %v", err)
	}
	report, err = DoctorWorkspace(marmotDir, workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	issue := findIssue(report, "local_route_mismatch")
	if issue == nil {
		t.Fatalf("issues = %+v, want local_route_mismatch", report.Issues)
	}
	if issue.Severity != "warning" {
		t.Fatalf("local_route_mismatch severity = %q, want warning", issue.Severity)
	}
	if !report.OK() {
		t.Error("route mismatch must stay a warning")
	}
}
