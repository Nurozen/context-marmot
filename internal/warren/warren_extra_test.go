package warren

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Direct unit tests for low-level helpers ---

func TestValidateProjectIDRejectsUnsafe(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"too long":     strings.Repeat("a", 129),
		"null byte":    "ab\x00cd",
		"slash":        "a/b",
		"backslash":    `a\b`,
		"dot prefix":   ".hidden",
		"under prefix": "_hidden",
		"dotdot":       "a..b",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateProjectID(id); err == nil {
				t.Fatalf("expected error for %q", id)
			}
		})
	}
	if err := ValidateProjectID("good-id"); err != nil {
		t.Fatalf("valid ID rejected: %v", err)
	}
}

func TestValidateWarrenIDRewritesLabel(t *testing.T) {
	err := ValidateWarrenID("")
	if err == nil || !strings.Contains(err.Error(), "Warren ID") {
		t.Fatalf("expected Warren ID error, got %v", err)
	}
}

func TestValidateNonEmptyPath(t *testing.T) {
	if err := validateNonEmptyPath("root", ""); err == nil {
		t.Fatal("expected empty path error")
	}
	if err := validateNonEmptyPath("root", "a\x00b"); err == nil {
		t.Fatal("expected null byte error")
	}
	if err := validateNonEmptyPath("root", "/ok/path"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRelation(t *testing.T) {
	if err := validateRelation(""); err == nil {
		t.Fatal("expected empty relation error")
	}
	if err := validateRelation("not-a-real-relation"); err == nil {
		t.Fatal("expected unknown relation error")
	}
	for _, rel := range []string{"calls", "reads", "writes", "references", "imports", "contains", "extends", "implements"} {
		if err := validateRelation(rel); err != nil {
			t.Fatalf("relation %q should be valid: %v", rel, err)
		}
	}
}

func TestGenerateProjectIDFromPath(t *testing.T) {
	if got := generateProjectIDFromPath(filepath.Join("some", "Cool Project", ".marmot")); got != "cool-project" {
		t.Fatalf("marmot dir path = %q", got)
	}
	if got := generateProjectIDFromPath(filepath.Join("some", "Plain Dir")); got != "plain-dir" {
		t.Fatalf("plain dir path = %q", got)
	}
	if got := generateProjectIDFromPath(""); got != "project" {
		t.Fatalf("empty path = %q", got)
	}
}

func TestSanitizePlainConfig(t *testing.T) {
	in := "version: 1\n" +
		"openai_api_key: sk-secret-value-abcdefghij\n" +
		"normal: kept\n" +
		"raw_secret_line sk-1234567890abcdefghij\n" +
		"safe: value\n"
	out := string(sanitizePlainConfig([]byte(in)))
	if strings.Contains(out, "openai_api_key") {
		t.Fatalf("secret key not stripped:\n%s", out)
	}
	if strings.Contains(out, "sk-1234567890abcdefghij") {
		t.Fatalf("api-key-looking value not stripped:\n%s", out)
	}
	if !strings.Contains(out, "normal: kept") || !strings.Contains(out, "safe: value") {
		t.Fatalf("normal fields dropped:\n%s", out)
	}
}

func TestSanitizeConfigValueTypes(t *testing.T) {
	// map[any]any with a non-string key (dropped) and a secret key (dropped).
	mapAny := map[any]any{
		"normal":         "value",
		42:               "int-key-dropped",
		"openai_api_key": "sk-should-be-dropped-1234567",
		"nested": map[any]any{
			"api_key": "sk-nested-secret-1234567890",
			"keep":    "ok",
		},
	}
	sanitized, ok := sanitizeConfigValue(mapAny)
	if !ok {
		t.Fatal("expected map to be kept")
	}
	m := sanitized.(map[string]any)
	if _, exists := m["openai_api_key"]; exists {
		t.Fatal("secret key survived")
	}
	if len(m) != 2 { // normal + nested
		t.Fatalf("unexpected map keys: %+v", m)
	}

	// String that looks like an API key is dropped.
	if _, ok := sanitizeConfigValue("sk-abcdefghijklmnopqrstuvwxyz"); ok {
		t.Fatal("api key string should be dropped")
	}
	// Plain string kept.
	if v, ok := sanitizeConfigValue("plain"); !ok || v.(string) != "plain" {
		t.Fatalf("plain string mishandled: %v %v", v, ok)
	}
	// Slice with a secret element filtered.
	slice, ok := sanitizeConfigValue([]any{"safe", "sk-abcdefghijklmnopqrstuvwxyz"})
	if !ok {
		t.Fatal("slice should be kept")
	}
	if len(slice.([]any)) != 1 {
		t.Fatalf("secret slice item not filtered: %+v", slice)
	}
	// Default type (int) passes through.
	if v, ok := sanitizeConfigValue(4096); !ok || v.(int) != 4096 {
		t.Fatalf("int value mishandled: %v %v", v, ok)
	}
}

func TestLooksLikeAPIKey(t *testing.T) {
	if looksLikeAPIKey("short") {
		t.Fatal("short value should not look like key")
	}
	if !looksLikeAPIKey("sk-abcdefghijklmnopqrst") {
		t.Fatal("sk- prefix long value should look like key")
	}
	if looksLikeAPIKey("this-is-long-but-no-prefix-value") {
		t.Fatal("no known prefix should not look like key")
	}
}

func TestPreferredProjectPath(t *testing.T) {
	wsMarmot := t.TempDir()
	warrenRoot := t.TempDir()
	project := Project{ProjectID: "project-a", Path: "projects/project-a/.marmot"}
	entry := WorkspaceWarren{Path: warrenRoot, Materialized: false}

	// Non-materialized returns source path under warren root.
	got := preferredProjectPath(wsMarmot, "w", entry, project)
	want := filepath.Join(warrenRoot, filepath.FromSlash(project.Path))
	if got != want {
		t.Fatalf("non-materialized path = %q want %q", got, want)
	}

	// Materialized but no cache dir yet falls back to source path.
	entry.Materialized = true
	if got := preferredProjectPath(wsMarmot, "w", entry, project); got != want {
		t.Fatalf("materialized-no-cache path = %q want %q", got, want)
	}

	// Materialized with existing cache dir returns cache.
	cached := materializedProjectPath(wsMarmot, "w", project.ProjectID)
	if err := os.MkdirAll(cached, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if got := preferredProjectPath(wsMarmot, "w", entry, project); got != cached {
		t.Fatalf("materialized path = %q want cache %q", got, cached)
	}
}

// --- Save/Load workspace-state to explicit marmot dir ---

func TestSaveAndLoadWorkspaceStateToMarmot(t *testing.T) {
	if err := SaveWorkspaceStateToMarmot("", nil, ""); err == nil {
		t.Fatal("expected empty marmot dir error")
	}
	if _, _, err := LoadWorkspaceStateFromMarmot(""); err == nil {
		t.Fatal("expected empty marmot dir error on load")
	}

	marmotDir := filepath.Join(t.TempDir(), ".marmot")
	warrenRoot := t.TempDir()
	state := &WorkspaceState{Warrens: map[string]WorkspaceWarren{
		"product-platform": {Path: warrenRoot, ActiveProjects: []string{"api"}},
	}}
	if err := SaveWorkspaceStateToMarmot(marmotDir, state, "body\n"); err != nil {
		t.Fatalf("SaveWorkspaceStateToMarmot: %v", err)
	}
	got, body, err := LoadWorkspaceStateFromMarmot(marmotDir)
	if err != nil {
		t.Fatalf("LoadWorkspaceStateFromMarmot: %v", err)
	}
	if body != "body\n" {
		t.Fatalf("body = %q", body)
	}
	if got.Warrens["product-platform"].Path != warrenRoot {
		t.Fatalf("unexpected state: %+v", got.Warrens)
	}

	// nil state saves an empty default.
	marmotDir2 := filepath.Join(t.TempDir(), ".marmot")
	if err := SaveWorkspaceStateToMarmot(marmotDir2, nil, ""); err != nil {
		t.Fatalf("SaveWorkspaceStateToMarmot nil: %v", err)
	}
	got2, _, err := LoadWorkspaceStateFromMarmot(marmotDir2)
	if err != nil {
		t.Fatalf("load nil-state: %v", err)
	}
	if len(got2.Warrens) != 0 {
		t.Fatalf("expected empty warrens, got %+v", got2.Warrens)
	}
}

func TestSaveWorkspaceStateToMarmotRejectsInvalidState(t *testing.T) {
	marmotDir := filepath.Join(t.TempDir(), ".marmot")
	// Warren entry with empty path fails validateWorkspaceState.
	state := &WorkspaceState{Warrens: map[string]WorkspaceWarren{
		"product-platform": {Path: ""},
	}}
	if err := SaveWorkspaceStateToMarmot(marmotDir, state, ""); err == nil {
		t.Fatal("expected invalid workspace state to be rejected")
	}
}

// --- SaveProjectMetadata error paths ---

func TestSaveProjectMetadataErrors(t *testing.T) {
	if err := SaveProjectMetadata("", &ProjectMetadata{ProjectID: "a", WarrenID: "b", VaultID: "c"}, ""); err == nil {
		t.Fatal("expected empty marmot dir error")
	}
	marmotDir := filepath.Join(t.TempDir(), ".marmot")
	// Invalid project ID rejected before write.
	if err := SaveProjectMetadata(marmotDir, &ProjectMetadata{ProjectID: "../bad", WarrenID: "w", VaultID: "v"}, ""); err == nil {
		t.Fatal("expected invalid metadata rejection")
	}
	// nil metadata is rejected (empty project ID after defaulting).
	if err := SaveProjectMetadata(marmotDir, nil, ""); err == nil {
		t.Fatal("expected nil metadata rejection")
	}
}

// --- preflight/ensure project metadata ---

func TestPreflightProjectMetadata(t *testing.T) {
	root := t.TempDir()
	project := Project{ProjectID: "api", Path: "projects/api/.marmot"}

	// No metadata present -> nil (not-exist swallowed).
	if err := preflightProjectMetadata(root, "product-platform", project); err != nil {
		t.Fatalf("preflight with no metadata: %v", err)
	}

	// Valid metadata present -> validates OK.
	marmotDir := filepath.Join(root, "projects", "api", ".marmot")
	if err := SaveProjectMetadata(marmotDir, &ProjectMetadata{ProjectID: "api", WarrenID: "product-platform", VaultID: "api"}, ""); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	if err := preflightProjectMetadata(root, "product-platform", project); err != nil {
		t.Fatalf("preflight valid metadata: %v", err)
	}

	// Corrupt metadata (non not-exist read error) -> error surfaced.
	badDir := filepath.Join(root, "projects", "bad", ".marmot")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, ManifestFileName), []byte("no frontmatter here\n"), 0o644); err != nil {
		t.Fatalf("write bad metadata: %v", err)
	}
	if err := preflightProjectMetadata(root, "product-platform", Project{ProjectID: "bad", Path: "projects/bad/.marmot"}); err == nil {
		t.Fatal("expected preflight to surface parse error")
	}
}

func TestEnsureProjectMetadataMergesAliases(t *testing.T) {
	root := t.TempDir()
	marmotDir := filepath.Join(root, "projects", "api", ".marmot")
	if err := SaveProjectMetadata(marmotDir, &ProjectMetadata{ProjectID: "api", WarrenID: "product-platform", VaultID: "api-vault", Aliases: []string{"svc"}}, "keep body\n"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	project := Project{ProjectID: "api", Path: "projects/api/.marmot", Aliases: []string{"backend"}}
	if err := ensureProjectMetadata(root, "product-platform", project); err != nil {
		t.Fatalf("ensureProjectMetadata: %v", err)
	}
	meta, body, err := LoadProjectMetadata(marmotDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if strings.Join(meta.Aliases, ",") != "backend,svc" {
		t.Fatalf("aliases = %+v", meta.Aliases)
	}
	if meta.VaultID != "api-vault" || body != "keep body\n" {
		t.Fatalf("metadata not preserved: %+v body=%q", meta, body)
	}
}

// --- Init error paths ---

func TestInitErrors(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "../bad"); err == nil {
		t.Fatal("expected invalid warren ID error")
	}

	// Already initialized with a different ID.
	if _, err := Init(root, "first-id"); err != nil {
		t.Fatalf("Init first: %v", err)
	}
	if _, err := Init(root, "second-id"); err == nil {
		t.Fatal("expected already-initialized mismatch error")
	}

	// Malformed existing manifest surfaces a non not-exist load error.
	root2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(root2, ManifestFileName), []byte("---\nversion: [broken\n---\n"), 0o644); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	if _, err := Init(root2, "any-id"); err == nil {
		t.Fatal("expected malformed manifest error")
	}

	// Empty warren ID defaults to slug from base dir.
	root3 := filepath.Join(t.TempDir(), "My Warren")
	if err := os.MkdirAll(root3, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m, err := Init(root3, "")
	if err != nil {
		t.Fatalf("Init default id: %v", err)
	}
	if m.WarrenID != "my-warren" {
		t.Fatalf("default warren ID = %q", m.WarrenID)
	}
}

// --- AddProject with derived project ID from path ---

func TestAddProjectDerivesIDFromPath(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "product-platform"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Empty ProjectID with a path -> generateProjectIDFromPath.
	if _, err := AddProject(root, Project{Path: "projects/derived-api/.marmot"}); err != nil {
		t.Fatalf("AddProject derived: %v", err)
	}
	projects, err := ListProjects(root)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ProjectID != "derived-api" {
		t.Fatalf("unexpected derived project: %+v", projects)
	}
	// Duplicate ID rejected.
	if _, err := AddProject(root, Project{ProjectID: "derived-api"}); err == nil {
		t.Fatal("expected duplicate project error")
	}
	// Invalid project ID rejected.
	if _, err := AddProject(root, Project{ProjectID: "../escape"}); err == nil {
		t.Fatal("expected invalid project ID error")
	}
	// Load error on missing root manifest.
	if _, err := AddProject(t.TempDir(), Project{ProjectID: "x"}); err == nil {
		t.Fatal("expected load error on uninitialized root")
	}
}

// --- RemoveProject / RenameProject error paths ---

func TestRemoveProjectErrors(t *testing.T) {
	root := t.TempDir()
	writeWarrenFixture(t, root, "product-platform", "api")
	if _, err := RemoveProject(root, "../bad"); err == nil {
		t.Fatal("expected invalid project ID error")
	}
	if _, err := RemoveProject(root, "ghost"); err == nil {
		t.Fatal("expected not-found error")
	}
	if _, err := RemoveProject(t.TempDir(), "api"); err == nil {
		t.Fatal("expected load error")
	}
}

func TestRenameProjectErrors(t *testing.T) {
	root := t.TempDir()
	writeWarrenFixture(t, root, "product-platform", "api", "web")
	if _, err := RenameProject(root, "../bad", "ok"); err == nil {
		t.Fatal("expected invalid old ID error")
	}
	if _, err := RenameProject(root, "api", "../bad"); err == nil {
		t.Fatal("expected invalid new ID error")
	}
	if _, err := RenameProject(root, "api", "api"); err == nil {
		t.Fatal("expected same-ID error")
	}
	if _, err := RenameProject(root, "api", "web"); err == nil {
		t.Fatal("expected new-ID-already-exists error")
	}
	if _, err := RenameProject(root, "ghost", "new"); err == nil {
		t.Fatal("expected old-ID-not-found error")
	}
	if _, err := RenameProject(t.TempDir(), "api", "new"); err == nil {
		t.Fatal("expected load error")
	}
}

// --- AddBridge / RemoveBridge error paths ---

func TestAddBridgeErrors(t *testing.T) {
	root := t.TempDir()
	writeWarrenFixture(t, root, "product-platform", "api", "web")
	if _, err := AddBridge(root, Bridge{Source: "api", Target: "web", Relations: []string{"bogus-relation"}}); err == nil {
		t.Fatal("expected invalid relation error")
	}
	if _, err := AddBridge(root, Bridge{Source: "ghost", Target: "web", Relations: []string{"calls"}}); err == nil {
		t.Fatal("expected missing source error")
	}
	if _, err := AddBridge(root, Bridge{Source: "api", Target: "ghost", Relations: []string{"calls"}}); err == nil {
		t.Fatal("expected missing target error")
	}
	if _, err := AddBridge(t.TempDir(), Bridge{Source: "a", Target: "b", Relations: []string{"calls"}}); err == nil {
		t.Fatal("expected load error")
	}
}

func TestRemoveBridgeErrors(t *testing.T) {
	root := t.TempDir()
	writeWarrenFixture(t, root, "product-platform", "api", "web")
	if _, err := AddBridge(root, Bridge{Source: "api", Target: "web", Relations: []string{"calls"}}); err != nil {
		t.Fatalf("AddBridge: %v", err)
	}
	if _, err := RemoveBridge(root, "../bad", "web"); err == nil {
		t.Fatal("expected invalid source error")
	}
	if _, err := RemoveBridge(root, "api", "../bad"); err == nil {
		t.Fatal("expected invalid target error")
	}
	if _, err := RemoveBridge(root, "api", "web", "bogus-relation"); err == nil {
		t.Fatal("expected invalid relation error")
	}
	if _, err := RemoveBridge(root, "api", "unknown-target"); err == nil {
		t.Fatal("expected bridge-not-found error")
	}
	if _, err := RemoveBridge(t.TempDir(), "api", "web"); err == nil {
		t.Fatal("expected load error")
	}
}

// --- Format error path ---

func TestFormatError(t *testing.T) {
	if _, err := Format(t.TempDir()); err == nil {
		t.Fatal("expected Format load error on missing manifest")
	}
}

// --- RegisterWorkspaceWarren error paths ---

func TestRegisterWorkspaceWarrenErrors(t *testing.T) {
	workspace := t.TempDir()
	// warrenRoot has no manifest.
	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", t.TempDir()); err == nil {
		t.Fatal("expected load error")
	}
	// Manifest ID mismatch.
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "api")
	if _, err := RegisterWorkspaceWarren(workspace, "wrong-id", warrenRoot); err == nil {
		t.Fatal("expected warren ID mismatch error")
	}
}

// --- Status error / materialized fallback paths ---

func TestStatusErrors(t *testing.T) {
	workspace := t.TempDir()
	// Not registered.
	if _, err := Status(workspace, "product-platform"); err == nil {
		t.Fatal("expected unregistered warren error")
	}
}

func TestStatusFallsBackToMaterializedWhenSourceGone(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	project := Project{ProjectID: "project-a", Path: "projects/project-a/.marmot"}
	if _, err := Materialize(marmotDir, "product-platform", project, warrenRoot, ""); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, true); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := os.RemoveAll(warrenRoot); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	statuses, err := Status(workspace, "product-platform")
	if err != nil {
		t.Fatalf("Status after source removal: %v", err)
	}
	if len(statuses) != 1 || statuses[0].ProjectID != "project-a" || !statuses[0].Materialized {
		t.Fatalf("expected materialized status, got %+v", statuses)
	}
}

// --- Materialize / copyDir error paths ---

func TestMaterializeCopyDirErrors(t *testing.T) {
	marmotDir := t.TempDir()
	// Source path does not exist.
	if _, err := Materialize(marmotDir, "w", Project{ProjectID: "p", Path: "projects/p/.marmot"}, t.TempDir(), ""); err == nil {
		t.Fatal("expected copyFilteredTree stat error for missing source")
	}

	// copyFilteredTree with a file source (not a directory).
	filePath := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := copyFilteredTree(filePath, filepath.Join(t.TempDir(), "dest"), nil, nil, nil); err == nil {
		t.Fatal("expected error copying non-directory source")
	}

	// Successful copyFilteredTree round-trip preserving nested files.
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyFilteredTree(src, dst, nil, nil, nil); err != nil {
		t.Fatalf("copyFilteredTree: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sub", "f.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("nested file not copied: %v %q", err, b)
	}
}

// --- writeMarkdownYAML error via read-only directory ---

func TestSaveManifestFailsOnReadOnlyRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	// Confirm we truly cannot write (skip if running with elevated perms).
	if f, err := os.CreateTemp(root, "probe"); err == nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		t.Skip("directory still writable; cannot exercise permission error")
	}
	err := SaveManifest(root, &Manifest{WarrenID: "product-platform"}, "")
	if err == nil {
		t.Fatal("expected write error on read-only root")
	}
}

// --- Doctor comprehensive issue coverage ---

func TestDoctorReportsAllIssueClasses(t *testing.T) {
	root := t.TempDir()

	// Materialized cache inside the warren -> warning.
	if err := os.MkdirAll(filepath.Join(root, ".marmot-data", "warrens"), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}

	// project "file" whose path is a regular file -> project_not_directory.
	fileProjDir := filepath.Join(root, "projects", "fileproj")
	if err := os.MkdirAll(fileProjDir, 0o755); err != nil {
		t.Fatalf("mkdir fileproj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileProjDir, ".marmot"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file marmot: %v", err)
	}

	// project "noread" dir exists but metadata is unreadable -> metadata_unreadable.
	if err := os.MkdirAll(filepath.Join(root, "projects", "noread", ".marmot"), 0o755); err != nil {
		t.Fatalf("mkdir noread: %v", err)
	}

	// project "warnmismatch" with metadata whose warren ID differs -> warren_id_mismatch.
	if err := SaveProjectMetadata(filepath.Join(root, "projects", "warnmismatch", ".marmot"),
		&ProjectMetadata{ProjectID: "warnmismatch", WarrenID: "other-warren", VaultID: "wm"}, ""); err != nil {
		t.Fatalf("seed warnmismatch: %v", err)
	}

	// two projects sharing a vault ID -> duplicate_vault_id.
	if err := SaveProjectMetadata(filepath.Join(root, "projects", "dupa", ".marmot"),
		&ProjectMetadata{ProjectID: "dupa", WarrenID: "product-platform", VaultID: "shared"}, ""); err != nil {
		t.Fatalf("seed dupa: %v", err)
	}
	if err := SaveProjectMetadata(filepath.Join(root, "projects", "dupb", ".marmot"),
		&ProjectMetadata{ProjectID: "dupb", WarrenID: "product-platform", VaultID: "shared"}, ""); err != nil {
		t.Fatalf("seed dupb: %v", err)
	}

	manifest := &Manifest{
		WarrenID: "product-platform",
		Projects: []Project{
			{ProjectID: "missing", Path: "projects/missing/.marmot"},
			{ProjectID: "fileproj", Path: "projects/fileproj/.marmot"},
			{ProjectID: "noread", Path: "projects/noread/.marmot"},
			{ProjectID: "warnmismatch", Path: "projects/warnmismatch/.marmot"},
			{ProjectID: "dupa", Path: "projects/dupa/.marmot"},
			{ProjectID: "dupb", Path: "projects/dupb/.marmot"},
		},
		Bridges: []Bridge{
			{Source: "ghost-src", Target: "ghost-tgt", Relations: []string{"calls"}},
		},
	}
	if err := SaveManifest(root, manifest, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	report, err := Doctor(root)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.OK() {
		t.Fatalf("expected issues, report OK; %+v", report)
	}
	codes := map[string]bool{}
	for _, issue := range report.Issues {
		codes[issue.Code] = true
	}
	for _, want := range []string{
		"materialized_cache_in_warren",
		"project_missing",
		"project_not_directory",
		"metadata_unreadable",
		"warren_id_mismatch",
		"duplicate_vault_id",
		"embeddings_missing",
		"bridge_source_missing",
		"bridge_target_missing",
	} {
		if !codes[want] {
			t.Fatalf("missing doctor code %q; got %v", want, codes)
		}
	}

	// Doctor on missing manifest returns error.
	if _, err := Doctor(t.TempDir()); err == nil {
		t.Fatal("expected Doctor load error")
	}
}

// --- Mount error path: not registered / invalid project id ---

func TestMountErrors(t *testing.T) {
	workspace := t.TempDir()
	if _, err := Mount(workspace, "unregistered", []string{"api"}, false); err == nil {
		t.Fatal("expected unregistered warren error")
	}

	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "api")
	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"../bad"}, false); err == nil {
		t.Fatal("expected invalid project ID error")
	}
}

func TestSetEditableErrors(t *testing.T) {
	workspace := t.TempDir()
	if _, err := SetEditable(workspace, "unregistered", "api", true); err == nil {
		t.Fatal("expected unregistered warren error")
	}
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "api")
	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := SetEditable(workspace, "product-platform", "../bad", true); err == nil {
		t.Fatal("expected invalid project ID error")
	}
}

// --- ActiveMounts materialized fallback ---

func TestActiveMountsMaterializedFallback(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	project := Project{ProjectID: "project-a", Path: "projects/project-a/.marmot"}
	if _, err := Materialize(marmotDir, "product-platform", project, warrenRoot, ""); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, true); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := os.RemoveAll(warrenRoot); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	mounts, err := ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 1 || !mounts[0].Materialized {
		t.Fatalf("expected materialized fallback mount, got %+v", mounts)
	}
}

// --- LoadProjectMetadata error paths ---

func TestLoadProjectMetadataErrors(t *testing.T) {
	if _, _, err := LoadProjectMetadata(""); err == nil {
		t.Fatal("expected empty marmot dir error")
	}
	// Missing file.
	if _, _, err := LoadProjectMetadata(t.TempDir()); err == nil {
		t.Fatal("expected read error for missing metadata")
	}
	// Invalid metadata (bad project id) fails validation.
	marmotDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(marmotDir, ManifestFileName),
		[]byte("---\nproject_id: ../bad\nwarren_id: w\nvault_id: v\n---\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := LoadProjectMetadata(marmotDir); err == nil {
		t.Fatal("expected metadata validation error")
	}
}

// --- pathContains / samePath direct coverage ---

func TestPathContainsAndSamePath(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "a", "b")
	if !pathContains(base, child) {
		t.Fatal("expected base to contain child")
	}
	if pathContains(base, filepath.Join(filepath.Dir(base), "sibling")) {
		t.Fatal("did not expect base to contain sibling")
	}
	if !pathContains(base, base) {
		t.Fatal("expected path to contain itself")
	}
	if !samePath(base, base) {
		t.Fatal("expected samePath for identical path")
	}
	if samePath(base, child) {
		t.Fatal("did not expect samePath for different paths")
	}
}
