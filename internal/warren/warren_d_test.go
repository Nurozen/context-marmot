package warren

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// D2 — burrow commit-pinning / provenance
// ---------------------------------------------------------------------------

// TestMaterializeWritesProvenance (D2): a burrow records what it was copied
// from, the record round-trips, and dropping the cache deletes it too.
func TestMaterializeWritesProvenance(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")
	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, true); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	project := Project{ProjectID: "project-a", Path: "projects/project-a/.marmot"}
	if _, err := Materialize(marmotDir, "product-platform", project, warrenRoot, "deadbeefcafe"); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	prov, err := LoadBurrowProvenance(marmotDir, "product-platform", "project-a")
	if err != nil {
		t.Fatalf("LoadBurrowProvenance: %v", err)
	}
	if prov.SourceCommit != "deadbeefcafe" {
		t.Errorf("SourceCommit = %q, want deadbeefcafe", prov.SourceCommit)
	}
	if prov.SourcePath != "projects/project-a/.marmot" {
		t.Errorf("SourcePath = %q", prov.SourcePath)
	}
	if prov.ManifestVersion != CurrentManifestVersion {
		t.Errorf("ManifestVersion = %d, want %d", prov.ManifestVersion, CurrentManifestVersion)
	}
	if _, err := time.Parse(time.RFC3339, prov.MaterializedAt); err != nil {
		t.Errorf("MaterializedAt %q is not RFC3339: %v", prov.MaterializedAt, err)
	}
	// The provenance file is a sibling of the cache, not vault content.
	mustExist(t, filepath.Join(marmotDir, ".marmot-data", "warrens", "product-platform", "projects", "project-a", "provenance.md"))

	// Non-git warrens pin an empty commit: staleness degrades to always-stale.
	if _, err := Materialize(marmotDir, "product-platform", project, warrenRoot, ""); err != nil {
		t.Fatalf("re-Materialize: %v", err)
	}
	prov, err = LoadBurrowProvenance(marmotDir, "product-platform", "project-a")
	if err != nil {
		t.Fatalf("LoadBurrowProvenance after empty-commit: %v", err)
	}
	if prov.SourceCommit != "" {
		t.Errorf("SourceCommit = %q, want empty for a non-git warren", prov.SourceCommit)
	}

	// C1 x D2: dropping the cache removes the provenance with it.
	if err := DropMaterialized(marmotDir, workspace, "product-platform", []string{"project-a"}); err != nil {
		t.Fatalf("DropMaterialized: %v", err)
	}
	mustNotExist(t, burrowProvenancePath(marmotDir, "product-platform", "project-a"))
	if _, err := LoadBurrowProvenance(marmotDir, "product-platform", "project-a"); !os.IsNotExist(err) && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("LoadBurrowProvenance after drop err = %v, want not-exist", err)
	}
}

// ---------------------------------------------------------------------------
// D4 — manifest read-only policy
// ---------------------------------------------------------------------------

func writeReadonlyWarren(t *testing.T, warrenRoot string) {
	t.Helper()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a", "project-b")
	if _, err := SetProjectReadOnly(warrenRoot, "project-a", true); err != nil {
		t.Fatalf("SetProjectReadOnly: %v", err)
	}
}

// TestSetProjectReadOnlyBumpsVersion (D4+D6): flipping the policy persists
// and lifts the manifest to schema version 2.
func TestSetProjectReadOnlyBumpsVersion(t *testing.T) {
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	manifest, _, err := LoadManifest(warrenRoot)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if manifest.Version != 1 {
		t.Fatalf("pre-policy Version = %d, want 1", manifest.Version)
	}

	if _, err := SetProjectReadOnly(warrenRoot, "project-a", true); err != nil {
		t.Fatalf("SetProjectReadOnly: %v", err)
	}
	manifest, _, err = LoadManifest(warrenRoot)
	if err != nil {
		t.Fatalf("LoadManifest v2: %v", err)
	}
	if manifest.Version != 2 {
		t.Errorf("Version = %d, want 2 once readonly is used", manifest.Version)
	}
	if !manifest.Projects[0].ReadOnly {
		t.Error("project-a should be readonly")
	}

	if _, err := SetProjectReadOnly(warrenRoot, "ghost", true); err == nil {
		t.Error("SetProjectReadOnly on unknown project must fail")
	}
	if _, err := SetProjectReadOnly(warrenRoot, "project-a", false); err != nil {
		t.Fatalf("SetProjectReadOnly --off: %v", err)
	}
	manifest, _, _ = LoadManifest(warrenRoot)
	if manifest.Projects[0].ReadOnly {
		t.Error("readonly should be cleared")
	}
}

// TestSetEditableRefusesReadOnly (D4 enforcement point 1): the consumer verb
// refuses an author-side readonly project; --off stays allowed.
func TestSetEditableRefusesReadOnly(t *testing.T) {
	workspace := t.TempDir()
	warrenRoot := t.TempDir()
	writeReadonlyWarren(t, warrenRoot)
	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := SetEditable(workspace, "product-platform", "project-a", true)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("SetEditable err = %v, want author-side readonly refusal", err)
	}
	// A sibling project without the policy is unaffected.
	if _, err := SetEditable(workspace, "product-platform", "project-b", true); err != nil {
		t.Fatalf("SetEditable project-b: %v", err)
	}
	// Disabling never needs policy permission.
	if _, err := SetEditable(workspace, "product-platform", "project-a", false); err != nil {
		t.Fatalf("SetEditable --off: %v", err)
	}
}

// TestActiveMountsAndStatusHideReadOnlyEditable (D4 enforcement point 3): a
// stale editable flag granted before the author flipped readonly must not
// surface Editable=true (the UI save button and MCP rejection text key off
// this).
func TestActiveMountsAndStatusHideReadOnlyEditable(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")
	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Editable granted first, policy flipped after: stale state.
	if _, err := SetEditable(workspace, "product-platform", "project-a", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}
	if _, err := SetProjectReadOnly(warrenRoot, "project-a", true); err != nil {
		t.Fatalf("SetProjectReadOnly: %v", err)
	}

	mounts, err := ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0].Editable {
		t.Fatalf("mounts = %+v, want one non-editable mount", mounts)
	}
	if mounts[0].WarrenPath != warrenRoot {
		t.Fatalf("WarrenPath = %q, want %q", mounts[0].WarrenPath, warrenRoot)
	}
	statuses, err := Status(workspace, "product-platform")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Editable {
		t.Fatalf("statuses = %+v, want one non-editable row", statuses)
	}
}

// TestWriteEditableNodeReadOnlyBackstop (D4 enforcement point 2): even a
// mount status that still says Editable (stale cache in a live engine) is
// refused at write time by re-reading the manifest; an unreadable manifest
// fails closed.
func TestWriteEditableNodeReadOnlyBackstop(t *testing.T) {
	warrenRoot := t.TempDir()
	writeReadonlyWarren(t, warrenRoot)
	projectDir := filepath.Join(warrenRoot, "projects", "project-a", ".marmot")
	n := &node.Node{ID: "service/api", Type: "module", Namespace: "default", Status: node.StatusActive, Summary: "API"}

	stale := ProjectStatus{ProjectID: "project-a", Path: projectDir, Editable: true, WarrenPath: warrenRoot}
	if _, err := WriteEditableNode(stale, n, nil, "", ""); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("WriteEditableNode err = %v, want author-side readonly refusal", err)
	}

	// Fail-closed: a write against an unreadable manifest refuses.
	if err := os.WriteFile(filepath.Join(warrenRoot, ManifestFileName), []byte("---\nwarren_id: product-pla"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteEditableNode(stale, n, nil, "", ""); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("WriteEditableNode err = %v, want fail-closed manifest refusal", err)
	}

	// Non-readonly sibling with a healthy manifest still writes.
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a", "project-b")
	okMount := ProjectStatus{ProjectID: "project-b", Path: filepath.Join(warrenRoot, "projects", "project-b", ".marmot"), Editable: true, WarrenPath: warrenRoot}
	if _, err := WriteEditableNode(okMount, n, nil, "", ""); err != nil {
		t.Fatalf("WriteEditableNode project-b: %v", err)
	}
}

// ---------------------------------------------------------------------------
// D6 — manifest version discipline
// ---------------------------------------------------------------------------

// TestManifestVersionCeiling (D6): reads of a newer manifest warn and
// succeed; every mutating path refuses to save it.
func TestManifestVersionCeiling(t *testing.T) {
	warrenRoot := t.TempDir()
	future := "---\nwarren_id: product-platform\nversion: 4\nprojects:\n  - project_id: project-a\n    path: projects/project-a/.marmot\n---\n"
	if err := os.WriteFile(filepath.Join(warrenRoot, ManifestFileName), []byte(future), 0o644); err != nil {
		t.Fatal(err)
	}

	warnings := captureWarnings(t)
	manifest, _, err := LoadManifest(warrenRoot)
	if err != nil {
		t.Fatalf("LoadManifest on newer version must succeed best-effort: %v", err)
	}
	if manifest.Version != 4 {
		t.Fatalf("Version = %d, want 4 preserved", manifest.Version)
	}
	if !strings.Contains(warnings.String(), "do not edit with this binary") {
		t.Fatalf("warnings = %q, want newer-version warning", warnings.String())
	}

	if _, err := AddProject(warrenRoot, Project{ProjectID: "project-b"}); err == nil || !strings.Contains(err.Error(), "exceeds supported") {
		t.Fatalf("AddProject err = %v, want version-ceiling refusal", err)
	}
	if _, err := SetProjectReadOnly(warrenRoot, "project-a", true); err == nil || !strings.Contains(err.Error(), "exceeds supported") {
		t.Fatalf("SetProjectReadOnly err = %v, want version-ceiling refusal", err)
	}
	if _, err := Init(warrenRoot, "product-platform"); err == nil || !strings.Contains(err.Error(), "exceeds supported") {
		t.Fatalf("Init err = %v, want version-ceiling refusal", err)
	}
	source := writeImportSourceVault(t, filepath.Join(t.TempDir(), ".marmot"), "src-vault")
	if _, err := ImportProject(warrenRoot, source, Project{ProjectID: "imported"}, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "exceeds supported") {
		t.Fatalf("ImportProject err = %v, want version-ceiling refusal", err)
	}
	// The refusals must not have rewritten the file.
	data, err := os.ReadFile(filepath.Join(warrenRoot, ManifestFileName))
	if err != nil || string(data) != future {
		t.Fatalf("manifest was rewritten by a refusing binary: %q err=%v", data, err)
	}
}

// TestManifestVersionBackfillAndV2 (D6): version 0 still backfills to 1, and
// a version-2 manifest (readonly era) loads and saves cleanly.
func TestManifestVersionBackfillAndV2(t *testing.T) {
	warrenRoot := t.TempDir()
	v0 := "---\nwarren_id: product-platform\n---\n"
	if err := os.WriteFile(filepath.Join(warrenRoot, ManifestFileName), []byte(v0), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := LoadManifest(warrenRoot)
	if err != nil || manifest.Version != 1 {
		t.Fatalf("LoadManifest v0 = (%+v, %v), want backfill to 1", manifest, err)
	}

	v2 := "---\nwarren_id: product-platform\nversion: 2\nprojects:\n  - project_id: project-a\n    path: projects/project-a/.marmot\n    readonly: true\n---\n"
	if err := os.WriteFile(filepath.Join(warrenRoot, ManifestFileName), []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	warnings := captureWarnings(t)
	manifest, _, err = LoadManifest(warrenRoot)
	if err != nil {
		t.Fatalf("LoadManifest v2: %v", err)
	}
	if manifest.Version != 2 || !manifest.Projects[0].ReadOnly {
		t.Fatalf("manifest = %+v, want v2 readonly", manifest)
	}
	if warnings.Len() != 0 {
		t.Fatalf("unexpected warnings for a supported version: %q", warnings.String())
	}
	if _, err := Format(warrenRoot); err != nil {
		t.Fatalf("Format v2: %v", err)
	}
}

// ---------------------------------------------------------------------------
// D5 — doctor additions
// ---------------------------------------------------------------------------

func seedProjectEmbeddings(t *testing.T, projectMarmotDir, model string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectMarmotDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := embedding.NewStore(filepath.Join(projectMarmotDir, ".marmot-data", "embeddings.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Upsert("service/api", []float32{0.1, 0.2, 0.3}, "hash", model); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

// TestDoctorModelSkew (D5.1): projects indexed with different embedding
// models get a model_skew warning naming both; matching models stay silent.
func TestDoctorModelSkew(t *testing.T) {
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a", "project-b")
	seedProjectEmbeddings(t, filepath.Join(warrenRoot, "projects", "project-a", ".marmot"), "model-one")
	seedProjectEmbeddings(t, filepath.Join(warrenRoot, "projects", "project-b", ".marmot"), "model-two")

	report, err := Doctor(warrenRoot)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	issue := findIssue(report, "model_skew")
	if issue == nil {
		t.Fatalf("issues = %+v, want model_skew", report.Issues)
	}
	if !strings.Contains(issue.Message, "model-one") || !strings.Contains(issue.Message, "model-two") || !strings.Contains(issue.Message, "project-a") || !strings.Contains(issue.Message, "project-b") {
		t.Fatalf("model_skew message = %q, want both projects and models named", issue.Message)
	}
	if !report.OK() {
		t.Error("model_skew is a warning, not an error")
	}

	// Same model everywhere: no skew.
	seedProjectEmbeddings(t, filepath.Join(warrenRoot, "projects", "project-b", ".marmot"), "model-one")
	if err := os.Remove(filepath.Join(warrenRoot, "projects", "project-b", ".marmot", ".marmot-data", "embeddings.db")); err != nil {
		t.Fatal(err)
	}
	seedProjectEmbeddings(t, filepath.Join(warrenRoot, "projects", "project-b", ".marmot"), "model-one")
	report, err = Doctor(warrenRoot)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if findIssue(report, "model_skew") != nil {
		t.Fatalf("issues = %+v, want no model_skew for matching models", report.Issues)
	}
}

// TestDoctorSchemaStale (D5.2): a pre-migration DB (no status column) fails
// SearchActive at query time; doctor names it instead of leaving it silent.
func TestDoctorSchemaStale(t *testing.T) {
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")
	projectDir := filepath.Join(warrenRoot, "projects", "project-a", ".marmot")
	seedProjectEmbeddings(t, projectDir, "test-model")
	db, err := sqlite3.Open(filepath.Join(projectDir, ".marmot-data", "embeddings.db"))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if err := db.Exec(`ALTER TABLE embeddings DROP COLUMN status`); err != nil {
		_ = db.Close()
		t.Fatalf("drop status column: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := Doctor(warrenRoot)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	issue := findIssue(report, "schema_stale")
	if issue == nil {
		t.Fatalf("issues = %+v, want schema_stale", report.Issues)
	}
	if !strings.Contains(issue.Message, "re-import") {
		t.Fatalf("schema_stale message = %q, want re-import guidance", issue.Message)
	}
}

// TestDoctorAbsoluteProjectPath (D5.4): an absolute manifest path only
// resolves on the author's machine — warn.
func TestDoctorAbsoluteProjectPath(t *testing.T) {
	warrenRoot := t.TempDir()
	abs := filepath.ToSlash(filepath.Join(t.TempDir(), ".marmot"))
	manifest := "---\nwarren_id: product-platform\nprojects:\n  - project_id: project-a\n    path: " + abs + "\n---\n"
	if err := os.WriteFile(filepath.Join(warrenRoot, ManifestFileName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Doctor(warrenRoot)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if findIssue(report, "absolute_project_path") == nil {
		t.Fatalf("issues = %+v, want absolute_project_path", report.Issues)
	}
}

// TestDoctorLockfileGitignore (D5.5): a git-backed warren without the
// _warren.md.lock ignore entry gets an info note; non-git fixtures and
// covered repos stay quiet.
func TestDoctorLockfileGitignore(t *testing.T) {
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	report, err := Doctor(warrenRoot)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if findIssue(report, "lockfile_not_ignored") != nil {
		t.Fatal("non-git warren must not nag about .gitignore")
	}

	if err := os.MkdirAll(filepath.Join(warrenRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	report, _ = Doctor(warrenRoot)
	issue := findIssue(report, "lockfile_not_ignored")
	if issue == nil || issue.Severity != "info" {
		t.Fatalf("issues = %+v, want info lockfile_not_ignored", report.Issues)
	}
	if !report.OK() {
		t.Error("info issues must not fail doctor")
	}

	if err := os.WriteFile(filepath.Join(warrenRoot, ".gitignore"), []byte("_warren.md.lock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, _ = Doctor(warrenRoot)
	if findIssue(report, "lockfile_not_ignored") != nil {
		t.Fatal("covered .gitignore must not warn")
	}
}

// TestDoctorWorkspaceVaultIDCollision (D5.3): legacy state carrying the same
// vault ID in two warrens (written before the mount-time refusal) is
// reported as an error; a clean workspace is healthy.
func TestDoctorWorkspaceVaultIDCollision(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenA := t.TempDir()
	warrenB := t.TempDir()
	writeWarrenFixture(t, warrenA, "warren-a", "project-x")
	writeWarrenFixture(t, warrenB, "warren-b", "project-y")
	// Both projects claim the same vault ID (hand-edit the metadata, as an
	// old binary could have).
	for _, fix := range []struct{ root, project string }{{warrenA, "project-x"}, {warrenB, "project-y"}} {
		dir := filepath.Join(fix.root, "projects", fix.project, ".marmot")
		meta, body, err := LoadProjectMetadata(dir)
		if err != nil {
			t.Fatalf("LoadProjectMetadata: %v", err)
		}
		meta.VaultID = "shared-vault"
		if err := SaveProjectMetadata(dir, meta, body); err != nil {
			t.Fatalf("SaveProjectMetadata: %v", err)
		}
	}
	// Hand-written state bypasses C7's mount refusal, exactly like legacy
	// binaries did.
	state := &WorkspaceState{Warrens: map[string]WorkspaceWarren{
		"warren-a": {Path: warrenA, ActiveProjects: []string{"project-x"}},
		"warren-b": {Path: warrenB, ActiveProjects: []string{"project-y"}},
	}}
	if err := SaveWorkspaceState(workspace, state, ""); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	report, err := DoctorWorkspace(marmotDir, workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	issue := findIssue(report, "vault_id_collision_workspace")
	if issue == nil {
		t.Fatalf("issues = %+v, want vault_id_collision_workspace", report.Issues)
	}
	if !strings.Contains(issue.Message, "warren-a/project-x") || !strings.Contains(issue.Message, "warren-b/project-y") {
		t.Fatalf("message = %q, want both claimants named", issue.Message)
	}
	if report.OK() {
		t.Error("vault ID collision must be an error")
	}

	// Resolving the collision heals the report.
	if _, err := Unmount(workspace, "warren-b", []string{"project-y"}); err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	report, err = DoctorWorkspace(marmotDir, workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	if len(report.Issues) != 0 {
		t.Fatalf("issues = %+v, want healthy workspace", report.Issues)
	}
}

// TestDoctorWorkspaceWarrenUnreachable: a registered warren whose checkout
// vanished warns even with ZERO active projects — previously such a warren
// surfaced nowhere (graph skip toasts only fire for mounted projects).
// Reachable warrens stay silent, and the warning names both escape hatches.
func TestDoctorWorkspaceWarrenUnreachable(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenOK := t.TempDir()
	writeWarrenFixture(t, warrenOK, "warren-ok", "project-x")
	gonePath := filepath.Join(t.TempDir(), "moved-away")
	state := &WorkspaceState{Warrens: map[string]WorkspaceWarren{
		"w-ok": {Path: warrenOK, ActiveProjects: []string{"project-x"}},
		// Zero active projects: the exact previously-silent case.
		"w-gone": {Path: gonePath},
	}}
	if err := SaveWorkspaceState(workspace, state, ""); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	report, err := DoctorWorkspace(marmotDir, workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace: %v", err)
	}
	var unreachable []DoctorIssue
	for _, issue := range report.Issues {
		if issue.Code == "warren_unreachable" {
			unreachable = append(unreachable, issue)
		}
	}
	if len(unreachable) != 1 {
		t.Fatalf("warren_unreachable issues = %+v, want exactly one (reachable warrens must not warn)", report.Issues)
	}
	issue := unreachable[0]
	if issue.Severity != "warning" {
		t.Errorf("severity = %q, want warning", issue.Severity)
	}
	if issue.Path != gonePath {
		t.Errorf("path = %q, want %q", issue.Path, gonePath)
	}
	for _, want := range []string{`"w-gone"`, gonePath, "warren register w-gone", "warren unregister --warren w-gone"} {
		if !strings.Contains(issue.Message, want) {
			t.Errorf("message = %q, want it to contain %q", issue.Message, want)
		}
	}
	if !report.OK() {
		t.Error("warren_unreachable must be a warning, not an error")
	}

	// Restoring the checkout heals the warning.
	writeWarrenFixture(t, gonePath, "w-gone")
	report, err = DoctorWorkspace(marmotDir, workspace)
	if err != nil {
		t.Fatalf("DoctorWorkspace after restore: %v", err)
	}
	if found := findIssue(report, "warren_unreachable"); found != nil {
		t.Fatalf("restored warren still warns: %+v", found)
	}
}

func findIssue(report DoctorReport, code string) *DoctorIssue {
	for i := range report.Issues {
		if report.Issues[i].Code == code {
			return &report.Issues[i]
		}
	}
	return nil
}
