package warren

import (
	"bytes"
	"fmt"
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

// TestActiveMountsSynthesizesIdentity (R2.3): a registered warren with an
// identified project and ZERO mounts yields exactly one identity status —
// live path, active and available, never editable or materialized.
func TestActiveMountsSynthesizesIdentity(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a", "project-b")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	marmotDir := workspaceMarmotDir(workspace)

	mounts, err := ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts = %+v, want exactly one identity entry", mounts)
	}
	got := mounts[0]
	if !got.SelfAlias || got.ProjectID != "project-a" {
		t.Fatalf("identity entry = %+v, want SelfAlias project-a", got)
	}
	if got.Path != marmotDir {
		t.Fatalf("identity Path = %q, want live vault %q", got.Path, marmotDir)
	}
	if !got.Active || !got.Available {
		t.Fatalf("identity entry = %+v, want Active && Available", got)
	}
	if got.Editable || got.Materialized {
		t.Fatalf("identity entry = %+v, want !Editable && !Materialized", got)
	}
	if got.VaultID != "project-a-vault" {
		t.Fatalf("identity VaultID = %q, want the local vault ID", got.VaultID)
	}
}

// TestActiveMountsDedupesR1SelfMount: an R1-era self entry in ActiveProjects
// (legacy editable flag included) produces exactly one identity-shaped
// status — live path, not the warren-copy path — while foreign mounts are
// untouched. (Merges R1.1's TestActiveMountsMarksSelfAlias.)
func TestActiveMountsDedupesR1SelfMount(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a", "project-b")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	if _, err := Mount(workspace, "product-platform", []string{"project-b"}, false); err != nil {
		t.Fatalf("Mount project-b: %v", err)
	}
	// Hand-write R1-era state: the self project recorded as an alias mount by
	// an R1 binary, plus legacy editable state from an even older one.
	state, body, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	entry.ActiveProjects = addName(entry.ActiveProjects, "project-a")
	entry.EditableProjects = []string{"project-a"}
	state.Warrens["product-platform"] = entry
	if err := SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	marmotDir := workspaceMarmotDir(workspace)
	mounts, err := ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 2 {
		t.Fatalf("mounts = %+v, want 2 (identity + foreign mount, deduped)", mounts)
	}
	byProject := make(map[string]ProjectStatus, len(mounts))
	for _, mount := range mounts {
		byProject[mount.ProjectID] = mount
	}
	self := byProject["project-a"]
	if !self.SelfAlias {
		t.Fatalf("project-a mount = %+v, want SelfAlias=true", self)
	}
	if self.Path != marmotDir {
		t.Fatalf("R1-era self mount Path = %q, want identity-shaped live path %q", self.Path, marmotDir)
	}
	if self.Editable {
		t.Fatalf("identity entry reports Editable=true despite legacy state: %+v", self)
	}
	other := byProject["project-b"]
	if other.SelfAlias {
		t.Fatalf("project-b mount = %+v, want SelfAlias=false", other)
	}
}

// TestActiveMountsNoLocalIDSkipsDormantProbes (R2.3): without a workspace
// vault_id identity is impossible, so dormant manifest projects are never
// probed — an unreadable dormant metadata file emits no warning and no
// entry. With a (non-matching) vault_id the dormant probe runs but stays
// silent: only active mounts use the loud metadata loader.
func TestActiveMountsNoLocalIDSkipsDormantProbes(t *testing.T) {
	workspace, warrenRoot := registerAndMount(t, "project-a", "project-b")
	if _, err := Mount(workspace, "product-platform", []string{"project-b"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	// Corrupt the dormant project's checkout metadata.
	metaPath := filepath.Join(warrenRoot, "projects", "project-a", ".marmot", "_warren.md")
	if err := os.WriteFile(metaPath, []byte("---\n\t: not yaml\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var warned bytes.Buffer
	oldWarn := warnWriter
	warnWriter = &warned
	defer func() { warnWriter = oldWarn }()

	mounts, err := ActiveMounts(workspaceMarmotDir(workspace))
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0].ProjectID != "project-b" {
		t.Fatalf("mounts = %+v, want only the active project-b", mounts)
	}
	if warned.Len() != 0 {
		t.Fatalf("dormant project was probed without a local vault_id: %q", warned.String())
	}

	// With a non-matching vault_id the probe runs, silently.
	writeSelfVaultConfig(t, workspace, "unrelated-vault")
	mounts, err = ActiveMounts(workspaceMarmotDir(workspace))
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0].ProjectID != "project-b" {
		t.Fatalf("mounts = %+v, want only the active project-b", mounts)
	}
	if warned.Len() != 0 {
		t.Fatalf("dormant metadata probe warned: %q", warned.String())
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

// TestSetEditableRefusesSelfAlias: warren edit refuses on an identified
// project with the edit-locally message; --off stays allowed (the
// legacy-state escape hatch) and, under R2 identity, records no mount —
// there is nothing to mount.
func TestSetEditableRefusesSelfAlias(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")

	_, err := SetEditable(workspace, "product-platform", "project-a", true)
	if err == nil || !strings.Contains(err.Error(), "alias of the live vault") {
		t.Fatalf("SetEditable err = %v, want self-alias refusal", err)
	}

	// --off is allowed and stays a pure no-op on state (no auto-mount).
	state, err := SetEditable(workspace, "product-platform", "project-a", false)
	if err != nil {
		t.Fatalf("SetEditable --off on self project: %v", err)
	}
	entry := state.Warrens["product-platform"]
	if len(entry.EditableProjects) != 0 {
		t.Fatalf("--off left editable flags: %v", entry.EditableProjects)
	}
	if len(entry.ActiveProjects) != 0 {
		t.Fatalf("--off re-recorded self state: active = %v, want none (identity is derived)", entry.ActiveProjects)
	}
}

// TestSetEditableOffSelfClearsWithoutMount (R2.4): legacy EditableProjects
// state on an identified project is cleared by --off without writing a mount
// record — the addName skip is what keeps R1-era self state from being
// re-recorded on every --off.
func TestSetEditableOffSelfClearsWithoutMount(t *testing.T) {
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

	after, err := SetEditable(workspace, "product-platform", "project-a", false)
	if err != nil {
		t.Fatalf("SetEditable --off: %v", err)
	}
	got := after.Warrens["product-platform"]
	if len(got.EditableProjects) != 0 {
		t.Fatalf("--off left editable flags: %v", got.EditableProjects)
	}
	if len(got.ActiveProjects) != 0 {
		t.Fatalf("--off wrote a mount record: %v", got.ActiveProjects)
	}
}

// TestMountAllSkipsSelf (R2.4): a mount list containing the identified
// project (what `mount --all` expands to) mounts the foreign projects,
// prints the identity note for self, and records nothing for it.
func TestMountAllSkipsSelf(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a", "project-b")
	writeSelfVaultConfig(t, workspace, "project-a-vault")

	var warned bytes.Buffer
	oldWarn := warnWriter
	warnWriter = &warned
	defer func() { warnWriter = oldWarn }()

	state, err := Mount(workspace, "product-platform", []string{"project-a", "project-b"}, false)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if !strings.Contains(warned.String(), "identity is automatic") {
		t.Fatalf("expected identity note, got %q", warned.String())
	}
	entry := state.Warrens["product-platform"]
	if len(entry.ActiveProjects) != 1 || entry.ActiveProjects[0] != "project-b" {
		t.Fatalf("active = %v, want only project-b (self skipped)", entry.ActiveProjects)
	}
}

// TestUnmountCleansR1SelfMount (R2.8): unmount is the cleanup path for
// R1-era self entries; afterwards unmounting the (never really mounted)
// identified project errors with the identity clause.
func TestUnmountCleansR1SelfMount(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	state, body, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	entry.ActiveProjects = []string{"project-a"}
	state.Warrens["product-platform"] = entry
	if err := SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	after, err := Unmount(workspace, "product-platform", []string{"project-a"})
	if err != nil {
		t.Fatalf("Unmount R1-era self entry: %v", err)
	}
	if got := after.Warrens["product-platform"].ActiveProjects; len(got) != 0 {
		t.Fatalf("unmount left the self entry: %v", got)
	}

	_, err = Unmount(workspace, "product-platform", []string{"project-a"})
	if err == nil || !strings.Contains(err.Error(), "identity is derived from vault_id, not a mount") {
		t.Fatalf("Unmount err = %v, want not-mounted error with the identity clause", err)
	}

	// A foreign never-mounted project keeps the plain message.
	_, err = Unmount(workspace, "product-platform", []string{"ghost"})
	if err == nil || strings.Contains(err.Error(), "identity is derived") {
		t.Fatalf("Unmount ghost err = %v, want plain not-mounted error", err)
	}
}

// TestStatusIdentityRow (R2.6): Status reports identified projects with the
// live vault path (never the checkout path) and Available=true, mounted or
// not.
func TestStatusIdentityRow(t *testing.T) {
	workspace, warrenRoot := registerAndMount(t, "project-a", "project-b")
	writeSelfVaultConfig(t, workspace, "project-a-vault")

	statuses, err := Status(workspace, "product-platform")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	byProject := make(map[string]ProjectStatus, len(statuses))
	for _, status := range statuses {
		byProject[status.ProjectID] = status
	}
	self := byProject["project-a"]
	if !self.SelfAlias {
		t.Fatalf("project-a status = %+v, want SelfAlias", self)
	}
	if want := workspaceMarmotDir(workspace); self.Path != want {
		t.Fatalf("identity status Path = %q, want live vault %q", self.Path, want)
	}
	if !self.Available {
		t.Fatalf("identity status = %+v, want Available", self)
	}
	other := byProject["project-b"]
	if other.SelfAlias || other.Path == workspaceMarmotDir(workspace) {
		t.Fatalf("project-b status = %+v, want checkout-path row", other)
	}
	if !strings.HasPrefix(other.Path, warrenRoot) {
		t.Fatalf("project-b Path = %q, want under warren root %q", other.Path, warrenRoot)
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

// TestDoctorWorkspaceSelfAliasHealthy (reshaped for R2): an identified
// project is healthy with zero mounts — one self_identity info, no
// self_alias_mount (nothing redundant is recorded), no collision.
func TestDoctorWorkspaceSelfAliasHealthy(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")

	report, err := DoctorWorkspace(workspaceMarmotDir(workspace), workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	if !report.OK() {
		t.Fatalf("identified project must be healthy, got %+v", report.Issues)
	}
	if findIssue(report, "vault_id_collision_workspace") != nil {
		t.Fatalf("identity reported as collision: %+v", report.Issues)
	}
	issue := findIssue(report, "self_identity")
	if issue == nil {
		t.Fatalf("issues = %+v, want self_identity info", report.Issues)
	}
	if issue.Severity != "info" || !strings.Contains(issue.Message, "serves from the live vault") {
		t.Fatalf("self_identity issue = %+v", issue)
	}
	if findIssue(report, "self_alias_mount") != nil {
		t.Fatalf("no mount recorded, so nothing is redundant: %+v", report.Issues)
	}
}

// TestDoctorWorkspaceRedundantSelfMount (R2.7): an R1-era self entry in
// ActiveProjects stays healthy but doctor reports the redundancy with the
// unmount cleanup, alongside the self_identity info.
func TestDoctorWorkspaceRedundantSelfMount(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	state, body, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	entry.ActiveProjects = []string{"project-a"}
	state.Warrens["product-platform"] = entry
	if err := SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	report, err := DoctorWorkspace(workspaceMarmotDir(workspace), workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	if !report.OK() {
		t.Fatalf("redundant self-mount must stay healthy, got %+v", report.Issues)
	}
	if findIssue(report, "self_identity") == nil {
		t.Fatalf("issues = %+v, want self_identity info", report.Issues)
	}
	issue := findIssue(report, "self_alias_mount")
	if issue == nil {
		t.Fatalf("issues = %+v, want self_alias_mount redundancy info", report.Issues)
	}
	if issue.Severity != "info" || !strings.Contains(issue.Message, "redundant self-mount") || !strings.Contains(issue.Message, "marmot warren unmount --warren product-platform project-a") {
		t.Fatalf("self_alias_mount issue = %+v, want redundancy message with unmount cleanup", issue)
	}
}

// TestDoctorWorkspaceSelfAliasEditableError: legacy editable self-mount state
// is the split-brain error, with the --off remediation.
func TestDoctorWorkspaceSelfAliasEditableError(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")
	// Hand-write R1-era state (mount is a no-op under R2 identity): the self
	// project active and marked editable by an older binary.
	state, body, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	entry.ActiveProjects = []string{"project-a"}
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
	// Hand-write R1-era state (mount is a no-op under R2 identity): the self
	// project recorded active by an older binary.
	state, body, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	entry.ActiveProjects = []string{"project-a"}
	state.Warrens["product-platform"] = entry
	if err := SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
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

// BenchmarkActiveMountsDormantIdentityScan measures the R2 identity scan's
// cost (defer criterion 3 of the identity plan): with a workspace vault_id,
// ActiveMounts probes every dormant manifest project's metadata once per
// call. The NoVaultID variant is the pre-R2 baseline (the early-continue
// skips all dormant probes). Run with a realistically large warren:
//
//	go test -bench BenchmarkActiveMountsDormant -run ^$ ./internal/warren/
func BenchmarkActiveMountsDormantIdentityScan(b *testing.B) {
	for _, tc := range []struct {
		name    string
		vaultID string
	}{
		{"NoVaultID", ""},           // pre-R2 baseline: dormant probes skipped
		{"WithVaultID", "no-match"}, // identity scan: one metadata read per dormant project
	} {
		b.Run(tc.name, func(b *testing.B) {
			workspace := b.TempDir()
			warrenRoot := b.TempDir()
			manifest := &Manifest{WarrenID: "bench-warren"}
			for i := 0; i < 200; i++ {
				projectID := fmt.Sprintf("proj-%03d", i)
				marmotDir := filepath.Join(warrenRoot, "projects", projectID, ".marmot")
				if err := SaveProjectMetadata(marmotDir, &ProjectMetadata{
					ProjectID: projectID,
					WarrenID:  "bench-warren",
					VaultID:   projectID + "-vault",
				}, ""); err != nil {
					b.Fatal(err)
				}
				manifest.Projects = append(manifest.Projects, Project{
					ProjectID: projectID,
					Path:      filepath.ToSlash(filepath.Join("projects", projectID, ".marmot")),
				})
			}
			if err := SaveManifest(warrenRoot, manifest, ""); err != nil {
				b.Fatal(err)
			}
			if _, err := RegisterWorkspaceWarren(workspace, "bench-warren", warrenRoot); err != nil {
				b.Fatal(err)
			}
			if tc.vaultID != "" {
				marmotDir := workspaceMarmotDir(workspace)
				cfg := "---\nversion: \"1\"\nvault_id: " + tc.vaultID + "\nnamespace: default\nembedding_provider: mock\n---\n"
				if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(cfg), 0o644); err != nil {
					b.Fatal(err)
				}
			}
			marmotDir := workspaceMarmotDir(workspace)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := ActiveMounts(marmotDir); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
