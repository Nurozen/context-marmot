package warren

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestRoundTripPreservesBody(t *testing.T) {
	root := t.TempDir()
	body := "# Warren\n\nNotes stay below frontmatter.\n"
	manifest := &Manifest{
		WarrenID: "product-platform",
		Projects: []Project{
			{ProjectID: "project-b", Path: "projects/project-b/.marmot"},
			{ProjectID: "project-a", Path: "projects/project-a/.marmot", Aliases: []string{"svc-a", "svc-a"}},
		},
		Bridges: []Bridge{{Source: "project-a", Target: "project-b", Relations: []string{"calls"}}},
	}

	if err := SaveManifest(root, manifest, body); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	got, gotBody, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if got.WarrenID != "product-platform" {
		t.Fatalf("WarrenID = %q", got.WarrenID)
	}
	if got.Version != 1 {
		t.Fatalf("Version = %d, want 1", got.Version)
	}
	if len(got.Projects) != 2 || got.Projects[0].ProjectID != "project-a" {
		t.Fatalf("projects not normalized: %+v", got.Projects)
	}
	if strings.Join(got.Projects[0].Aliases, ",") != "svc-a" {
		t.Fatalf("aliases not normalized: %+v", got.Projects[0].Aliases)
	}
	if gotBody != body {
		t.Fatalf("body = %q, want %q", gotBody, body)
	}
	if entries := tempEntries(t, root); len(entries) != 0 {
		t.Fatalf("left temp files behind: %v", entries)
	}
}

func TestLoadManifestBackfillsLegacyFields(t *testing.T) {
	root := t.TempDir()
	data := []byte("---\nversion: 1\nprojects:\n  - project_id: api\n---\nlegacy body\n")
	if err := os.WriteFile(filepath.Join(root, "_warren.md"), data, 0o644); err != nil {
		t.Fatalf("write legacy manifest: %v", err)
	}

	got, body, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest legacy: %v", err)
	}
	if got.WarrenID != GenerateProjectID(filepath.Base(root)) {
		t.Fatalf("WarrenID = %q, want slug from root", got.WarrenID)
	}
	if len(got.Projects) != 1 || got.Projects[0].Path != "projects/api/.marmot" {
		t.Fatalf("project path was not backfilled: %+v", got.Projects)
	}
	if body != "legacy body\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestProjectMetadataRoundTripDefaultsVaultID(t *testing.T) {
	marmotDir := filepath.Join(t.TempDir(), ".marmot")
	meta := &ProjectMetadata{
		ProjectID: "project-a",
		WarrenID:  "product-platform",
		Aliases:   []string{"svc-a"},
	}
	if err := SaveProjectMetadata(marmotDir, meta, "project body\n"); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	got, body, err := LoadProjectMetadata(marmotDir)
	if err != nil {
		t.Fatalf("LoadProjectMetadata: %v", err)
	}
	if got.VaultID != "project-a" {
		t.Fatalf("VaultID = %q, want project-a", got.VaultID)
	}
	if body != "project body\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestAuthoringInitAddRenameRemoveProjectPreservesBody(t *testing.T) {
	root := t.TempDir()
	body := "# Product Platform\n\nKeep this prose intact.\n"

	manifest, err := Init(root, "product-platform")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if manifest.WarrenID != "product-platform" || manifest.Version != 1 {
		t.Fatalf("unexpected init manifest: %+v", manifest)
	}
	if err := SaveManifest(root, manifest, body); err != nil {
		t.Fatalf("SaveManifest body: %v", err)
	}

	if _, err := AddProject(root, Project{
		ProjectID: "api",
		Aliases:   []string{" service-api ", "service-api", "backend"},
	}); err != nil {
		t.Fatalf("AddProject api: %v", err)
	}
	if _, err := AddProject(root, Project{ProjectID: "web"}); err != nil {
		t.Fatalf("AddProject web: %v", err)
	}
	projects, err := ListProjects(root)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 || projects[0].ProjectID != "api" || projects[0].Path != "projects/api/.marmot" {
		t.Fatalf("projects not normalized: %+v", projects)
	}
	if strings.Join(projects[0].Aliases, ",") != "backend,service-api" {
		t.Fatalf("aliases not normalized: %+v", projects[0].Aliases)
	}
	meta, metaBody, err := LoadProjectMetadata(filepath.Join(root, "projects", "api", ".marmot"))
	if err != nil {
		t.Fatalf("LoadProjectMetadata api: %v", err)
	}
	if meta.ProjectID != "api" || meta.WarrenID != "product-platform" || meta.VaultID != "api" || metaBody != "" {
		t.Fatalf("unexpected project metadata: %+v body=%q", meta, metaBody)
	}

	if _, err := AddBridge(root, Bridge{Source: "api", Target: "web", Relations: []string{"references", "calls", "calls"}}); err != nil {
		t.Fatalf("AddBridge: %v", err)
	}
	if _, err := RenameProject(root, "api", "api-service"); err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	bridges, err := ListBridges(root)
	if err != nil {
		t.Fatalf("ListBridges: %v", err)
	}
	if len(bridges) != 1 || bridges[0].Source != "api-service" || strings.Join(bridges[0].Relations, ",") != "calls,references" {
		t.Fatalf("bridge not renamed/normalized: %+v", bridges)
	}
	meta, _, err = LoadProjectMetadata(filepath.Join(root, "projects", "api", ".marmot"))
	if err != nil {
		t.Fatalf("LoadProjectMetadata renamed: %v", err)
	}
	if meta.ProjectID != "api-service" {
		t.Fatalf("renamed metadata project ID = %q", meta.ProjectID)
	}

	if _, err := RemoveProject(root, "api-service"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	manifest, gotBody, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest after remove: %v", err)
	}
	if gotBody != body {
		t.Fatalf("body = %q, want %q", gotBody, body)
	}
	if len(manifest.Projects) != 1 || manifest.Projects[0].ProjectID != "web" {
		t.Fatalf("unexpected projects after remove: %+v", manifest.Projects)
	}
	if len(manifest.Bridges) != 0 {
		t.Fatalf("bridges should be pruned after project remove: %+v", manifest.Bridges)
	}
}

func TestGenerateProjectID(t *testing.T) {
	tests := map[string]string{
		" Product Platform API ": "product-platform-api",
		"!!!":                    "project",
		"API_v2/Worker":          "api-v2-worker",
	}
	for input, want := range tests {
		if got := GenerateProjectID(input); got != want {
			t.Fatalf("GenerateProjectID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBridgeAddMergeAndRemoveRelations(t *testing.T) {
	root := t.TempDir()
	writeWarrenFixture(t, root, "product-platform", "api", "web")

	if _, err := AddBridge(root, Bridge{Source: "web", Target: "api", Relations: []string{"reads"}}); err != nil {
		t.Fatalf("AddBridge reads: %v", err)
	}
	if _, err := AddBridge(root, Bridge{Source: "web", Target: "api", Relations: []string{"calls", "reads"}}); err != nil {
		t.Fatalf("AddBridge merge: %v", err)
	}
	bridges, err := ListBridges(root)
	if err != nil {
		t.Fatalf("ListBridges: %v", err)
	}
	if len(bridges) != 1 || strings.Join(bridges[0].Relations, ",") != "calls,reads" {
		t.Fatalf("bridge relations not merged: %+v", bridges)
	}
	if _, err := RemoveBridge(root, "web", "api", "reads"); err != nil {
		t.Fatalf("RemoveBridge relation: %v", err)
	}
	bridges, err = ListBridges(root)
	if err != nil {
		t.Fatalf("ListBridges after relation remove: %v", err)
	}
	if len(bridges) != 1 || strings.Join(bridges[0].Relations, ",") != "calls" {
		t.Fatalf("bridge relation not removed: %+v", bridges)
	}
	if _, err := RemoveBridge(root, "web", "api"); err != nil {
		t.Fatalf("RemoveBridge full: %v", err)
	}
	bridges, err = ListBridges(root)
	if err != nil {
		t.Fatalf("ListBridges after remove: %v", err)
	}
	if len(bridges) != 0 {
		t.Fatalf("expected no bridges after full remove, got %+v", bridges)
	}
}

func TestSaveManifestRejectsMalformedBridgeWithoutDroppingIt(t *testing.T) {
	root := t.TempDir()
	manifest := &Manifest{
		WarrenID: "product-platform",
		Projects: []Project{
			{ProjectID: "api", Path: "projects/api/.marmot"},
			{ProjectID: "web", Path: "projects/web/.marmot"},
		},
		Bridges: []Bridge{{Source: "api", Target: "web"}},
	}
	if err := SaveManifest(root, manifest, ""); err == nil {
		t.Fatal("expected SaveManifest to reject bridge with empty relations")
	}
	if _, err := os.Stat(filepath.Join(root, "_warren.md")); !os.IsNotExist(err) {
		t.Fatalf("manifest should not be written after invalid bridge, stat err=%v", err)
	}
}

func TestAddProjectDoesNotMutateManifestWhenMetadataIsInvalid(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "product-platform"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	marmotDir := filepath.Join(root, "projects", "api", ".marmot")
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatalf("mkdir metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marmotDir, "_warren.md"), []byte("---\nproject_id: ../bad\nwarren_id: product-platform\nvault_id: api\n---\n"), 0o644); err != nil {
		t.Fatalf("write bad metadata: %v", err)
	}
	if _, err := AddProject(root, Project{ProjectID: "api", Path: "projects/api/.marmot"}); err == nil {
		t.Fatal("expected AddProject to reject invalid existing metadata")
	}
	projects, err := ListProjects(root)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("project should not be registered after metadata preflight failure: %+v", projects)
	}
}

func TestDoctorAndFormat(t *testing.T) {
	root := t.TempDir()
	writeWarrenFixture(t, root, "product-platform", "api")

	report, err := Doctor(root)
	if err != nil {
		t.Fatalf("Doctor healthy: %v", err)
	}
	if !report.OK() {
		t.Fatalf("expected healthy report, got %+v", report)
	}

	meta := &ProjectMetadata{ProjectID: "other", WarrenID: "product-platform"}
	if err := SaveProjectMetadata(filepath.Join(root, "projects", "api", ".marmot"), meta, "metadata body\n"); err != nil {
		t.Fatalf("SaveProjectMetadata mismatch: %v", err)
	}
	report, err = Doctor(root)
	if err != nil {
		t.Fatalf("Doctor mismatch: %v", err)
	}
	if report.OK() || report.Issues[0].Code != "project_id_mismatch" {
		t.Fatalf("expected project_id_mismatch, got %+v", report)
	}

	body := "Body survives format.\n"
	manifest := &Manifest{
		WarrenID: "product-platform",
		Projects: []Project{
			{ProjectID: "web", Path: "projects/web/.marmot", Aliases: []string{"ui", "ui"}},
			{ProjectID: "api", Path: "projects/api/.marmot"},
		},
		Bridges: []Bridge{
			{Source: "web", Target: "api", Relations: []string{"reads", "calls", "reads"}},
			{Source: "web", Target: "api", Relations: []string{"writes"}},
		},
	}
	if err := SaveManifest(root, manifest, body); err != nil {
		t.Fatalf("SaveManifest before Format: %v", err)
	}
	formatted, err := Format(root)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	_, gotBody, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest after Format: %v", err)
	}
	if gotBody != body {
		t.Fatalf("format body = %q, want %q", gotBody, body)
	}
	if formatted.Projects[0].ProjectID != "api" || len(formatted.Bridges) != 1 || strings.Join(formatted.Bridges[0].Relations, ",") != "calls,reads,writes" {
		t.Fatalf("manifest not formatted: %+v", formatted)
	}
}

func TestWorkspaceStateRoundTripAndEditToggle(t *testing.T) {
	workspace := t.TempDir()
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a", "project-b")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("RegisterWorkspaceWarren: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-b", "project-a", "project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if _, err := SetEditable(workspace, "product-platform", "project-a", true); err != nil {
		t.Fatalf("SetEditable on: %v", err)
	}

	state, _, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	if strings.Join(entry.ActiveProjects, ",") != "project-a,project-b" {
		t.Fatalf("active projects = %+v", entry.ActiveProjects)
	}
	if strings.Join(entry.EditableProjects, ",") != "project-a" {
		t.Fatalf("editable projects = %+v", entry.EditableProjects)
	}

	if _, err := SetEditable(workspace, "product-platform", "project-a", false); err != nil {
		t.Fatalf("SetEditable off: %v", err)
	}
	statuses, err := Status(workspace, "product-platform")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	for _, status := range statuses {
		if status.ProjectID == "project-a" && status.Editable {
			t.Fatalf("project-a should not be editable after off: %+v", status)
		}
	}
}

func TestMountAndEditRejectUnregisteredProjects(t *testing.T) {
	workspace := t.TempDir()
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("RegisterWorkspaceWarren: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"ghost-project"}, false); err == nil {
		t.Fatal("expected Mount to reject unregistered project")
	}
	if _, err := SetEditable(workspace, "product-platform", "ghost-project", true); err == nil {
		t.Fatal("expected SetEditable to reject unregistered project")
	}
}

func TestActiveMountsReturnsOnlyMountedProjects(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a", "project-b")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("RegisterWorkspaceWarren: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-b"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if _, err := SetEditable(workspace, "product-platform", "project-b", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}

	mounts, err := ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("expected 1 active mount, got %+v", mounts)
	}
	if mounts[0].ProjectID != "project-b" || !mounts[0].Editable || mounts[0].VaultID != "project-b-vault" {
		t.Fatalf("unexpected mount: %+v", mounts[0])
	}
}

func TestMaterializeCopiesProjectVaultToCache(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")
	project := Project{ProjectID: "project-a", Path: "projects/project-a/.marmot"}

	target, err := Materialize(marmotDir, "product-platform", project, warrenRoot)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "_warren.md")); err != nil {
		t.Fatalf("materialized _warren.md missing: %v", err)
	}
	if strings.Contains(target, filepath.Join(marmotDir, "project-a")) {
		t.Fatalf("materialized target should stay under .marmot-data, got %s", target)
	}

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("RegisterWorkspaceWarren: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, true); err != nil {
		t.Fatalf("Mount materialized: %v", err)
	}
	if err := os.RemoveAll(warrenRoot); err != nil {
		t.Fatalf("remove source Warren: %v", err)
	}
	mounts, err := ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts from materialized cache: %v", err)
	}
	if len(mounts) != 1 || !mounts[0].Available || !mounts[0].Materialized || mounts[0].Path != target {
		t.Fatalf("expected materialized mount from cache, got %+v", mounts)
	}
}

func TestValidationRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	cases := []Manifest{
		{WarrenID: "../bad"},
		{WarrenID: "product", Projects: []Project{{ProjectID: "a/b", Path: "projects/a/.marmot"}}},
		{WarrenID: "product", Projects: []Project{{ProjectID: "a", Path: "../escape"}}},
		{WarrenID: "product", Projects: []Project{{ProjectID: "a", Path: ""}}},
	}
	for i, manifest := range cases {
		if err := SaveManifest(root, &manifest, ""); err == nil {
			t.Fatalf("case %d: expected unsafe manifest rejection", i)
		}
	}
}

func TestLoadRejectsMalformedFrontmatter(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "_warren.md"), []byte("---\nversion: [broken\n---\n"), 0o644); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}
	if _, _, err := LoadManifest(root); err == nil {
		t.Fatal("expected malformed manifest error")
	}

	workspaceRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".marmot"), 0o755); err != nil {
		t.Fatalf("mkdir .marmot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".marmot", "_warren.md"), []byte("no frontmatter\n"), 0o644); err != nil {
		t.Fatalf("write malformed state: %v", err)
	}
	if _, _, err := LoadWorkspaceState(workspaceRoot); err == nil {
		t.Fatal("expected malformed state error")
	}
}

func writeWarrenFixture(t *testing.T, root, warrenID string, projects ...string) {
	t.Helper()
	manifest := &Manifest{WarrenID: warrenID}
	for _, projectID := range projects {
		marmotDir := filepath.Join(root, "projects", projectID, ".marmot")
		meta := &ProjectMetadata{
			ProjectID: projectID,
			WarrenID:  warrenID,
			VaultID:   projectID + "-vault",
		}
		if err := SaveProjectMetadata(marmotDir, meta, ""); err != nil {
			t.Fatalf("SaveProjectMetadata %s: %v", projectID, err)
		}
		manifest.Projects = append(manifest.Projects, Project{
			ProjectID: projectID,
			Path:      filepath.ToSlash(filepath.Join("projects", projectID, ".marmot")),
		})
	}
	if err := SaveManifest(root, manifest, ""); err != nil {
		t.Fatalf("SaveManifest fixture: %v", err)
	}
}

func tempEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var temps []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".warren-") || strings.HasSuffix(entry.Name(), ".tmp") {
			temps = append(temps, entry.Name())
		}
	}
	return temps
}
