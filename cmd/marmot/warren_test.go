package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/node"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

func TestWarrenRegisterMountEditStatus(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenRoot := testWarrenRoot(t, "product-platform", "project-a", "project-b")

	if code := run([]string{"warren", "register", "--dir", marmotDir, "product-platform", warrenRoot}); code != 0 {
		t.Fatalf("register exit code = %d", code)
	}
	if code := run([]string{"warren", "mount", "--dir", marmotDir, "--warren", "product-platform", "project-a", "project-b"}); code != 0 {
		t.Fatalf("mount exit code = %d", code)
	}
	if code := run([]string{"warren", "edit", "--dir", marmotDir, "--warren", "product-platform", "project-a"}); code != 0 {
		t.Fatalf("edit exit code = %d", code)
	}
	if code := run([]string{"warren", "status", "--dir", marmotDir, "--warren", "product-platform"}); code != 0 {
		t.Fatalf("status exit code = %d", code)
	}

	state, _, err := warrenpkg.LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	if len(entry.ActiveProjects) != 2 {
		t.Fatalf("active projects = %+v", entry.ActiveProjects)
	}
	if len(entry.EditableProjects) != 1 || entry.EditableProjects[0] != "project-a" {
		t.Fatalf("editable projects = %+v", entry.EditableProjects)
	}

	if code := run([]string{"warren", "edit", "--off", "--dir", marmotDir, "--warren", "product-platform", "project-a"}); code != 0 {
		t.Fatalf("edit --off exit code = %d", code)
	}
	state, _, err = warrenpkg.LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState after off: %v", err)
	}
	if len(state.Warrens["product-platform"].EditableProjects) != 0 {
		t.Fatalf("expected no editable projects after off, got %+v", state.Warrens["product-platform"].EditableProjects)
	}
}

func TestWarrenBurrowMaterializeCachesVaults(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenRoot := testWarrenRoot(t, "product-platform", "project-a")

	if code := run([]string{"warren", "register", "--dir", marmotDir, "product-platform", warrenRoot}); code != 0 {
		t.Fatalf("register exit code = %d", code)
	}
	if code := run([]string{"warren", "burrow", "--materialize", "--dir", marmotDir, "--warren", "product-platform", "project-a"}); code != 0 {
		t.Fatalf("burrow exit code = %d", code)
	}

	cached := filepath.Join(marmotDir, ".marmot-data", "warrens", "product-platform", "projects", "project-a", ".marmot", "_warren.md")
	if _, err := os.Stat(cached); err != nil {
		t.Fatalf("expected materialized cache file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(marmotDir, "project-a")); err == nil {
		t.Fatalf("burrow --materialize should not copy project under .marmot/project-a")
	}
}

func TestWarrenCommandRequiresTargetWhenAmbiguous(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenA := testWarrenRoot(t, "product", "project-a")
	warrenB := testWarrenRoot(t, "devex", "tooling")

	if code := run([]string{"warren", "register", "--dir", marmotDir, "product", warrenA}); code != 0 {
		t.Fatalf("register product exit code = %d", code)
	}
	if code := run([]string{"warren", "register", "--dir", marmotDir, "devex", warrenB}); code != 0 {
		t.Fatalf("register devex exit code = %d", code)
	}
	if code := run([]string{"warren", "status", "--dir", marmotDir}); code == 0 {
		t.Fatalf("status without --warren should fail when multiple Warrens exist")
	}
}

func TestWarrenAuthoringProjectCommands(t *testing.T) {
	root := t.TempDir()

	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "product-platform"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	if code := run([]string{"warren", "project", "add", "project-a", "--warren-dir", root, "--path", "projects/project-a/.marmot", "--vault-id", "project-a-vault", "--alias", "svc-a", "--alias", "api"}); code != 0 {
		t.Fatalf("project add exit code = %d", code)
	}
	if code := run([]string{"warren", "project", "list", "--warren-dir", root}); code != 0 {
		t.Fatalf("project list exit code = %d", code)
	}
	output, code := captureRun([]string{"warren", "project", "list", "--warren-dir", root, "--json"})
	if code != 0 {
		t.Fatalf("project list --json exit code = %d output=%s", code, output)
	}
	var listed []warrenpkg.Project
	if err := json.Unmarshal([]byte(output), &listed); err != nil {
		t.Fatalf("project list --json invalid JSON: %v\n%s", err, output)
	}
	if !strings.Contains(output, "project_id") || strings.Contains(output, "ProjectID") {
		t.Fatalf("project list JSON should use snake_case keys, got %s", output)
	}

	manifest, _, err := warrenpkg.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if manifest.WarrenID != "product-platform" {
		t.Fatalf("WarrenID = %q", manifest.WarrenID)
	}
	if len(manifest.Projects) != 1 || manifest.Projects[0].ProjectID != "project-a" {
		t.Fatalf("projects after add = %+v", manifest.Projects)
	}
	if got := strings.Join(manifest.Projects[0].Aliases, ","); got != "api,svc-a" {
		t.Fatalf("aliases = %q", got)
	}
	meta, _, err := warrenpkg.LoadProjectMetadata(filepath.Join(root, "projects", "project-a", ".marmot"))
	if err != nil {
		t.Fatalf("LoadProjectMetadata: %v", err)
	}
	if meta.VaultID != "project-a-vault" {
		t.Fatalf("VaultID = %q", meta.VaultID)
	}

	if code := run([]string{"warren", "project", "rename", "project-a", "project-api", "--warren-dir", root}); code != 0 {
		t.Fatalf("project rename exit code = %d", code)
	}
	manifest, _, err = warrenpkg.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest after rename: %v", err)
	}
	if len(manifest.Projects) != 1 || manifest.Projects[0].ProjectID != "project-api" {
		t.Fatalf("projects after rename = %+v", manifest.Projects)
	}

	if code := run([]string{"warren", "project", "remove", "project-api", "--warren-dir", root}); code != 0 {
		t.Fatalf("project remove exit code = %d", code)
	}
	manifest, _, err = warrenpkg.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest after remove: %v", err)
	}
	if len(manifest.Projects) != 0 {
		t.Fatalf("projects after remove = %+v", manifest.Projects)
	}
}

func TestWarrenProjectAddInvalidVaultIDDoesNotRegisterProject(t *testing.T) {
	root := t.TempDir()

	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "product-platform"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	if code := run([]string{"warren", "project", "add", "api", "--warren-dir", root, "--path", "projects/api/.marmot", "--vault-id", "../bad"}); code == 0 {
		t.Fatal("expected invalid vault ID add to fail")
	}
	manifest, _, err := warrenpkg.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Projects) != 0 {
		t.Fatalf("invalid vault ID should not register project, got %+v", manifest.Projects)
	}
}

func TestWarrenProjectImportCommand(t *testing.T) {
	root := t.TempDir()
	source := writeCLIImportSourceVault(t, filepath.Join(t.TempDir(), "api", ".marmot"), "source-vault")

	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "product-platform"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	if code := run([]string{"warren", "project", "import", "project-a", source, "--warren-dir", root, "--vault-id", "project-a-vault", "--alias", "api"}); code != 0 {
		t.Fatalf("project import exit code = %d", code)
	}
	meta, _, err := warrenpkg.LoadProjectMetadata(filepath.Join(root, "projects", "project-a", ".marmot"))
	if err != nil {
		t.Fatalf("LoadProjectMetadata: %v", err)
	}
	if meta.ProjectID != "project-a" || meta.WarrenID != "product-platform" || meta.VaultID != "project-a-vault" || strings.Join(meta.Aliases, ",") != "api" {
		t.Fatalf("unexpected imported metadata: %+v", meta)
	}
	output, code := captureRun([]string{"warren", "project", "list", "--warren-dir", root, "--json"})
	if code != 0 {
		t.Fatalf("project list --json exit code = %d output=%s", code, output)
	}
	var listed []warrenpkg.Project
	if err := json.Unmarshal([]byte(output), &listed); err != nil {
		t.Fatalf("project list --json invalid JSON: %v\n%s", err, output)
	}
	if len(listed) != 1 || listed[0].ProjectID != "project-a" || !strings.Contains(output, "project_id") || strings.Contains(output, "ProjectID") {
		t.Fatalf("unexpected project list JSON: %s", output)
	}
}

func TestWarrenProjectImportGenerateID(t *testing.T) {
	root := t.TempDir()
	source := writeCLIImportSourceVault(t, filepath.Join(t.TempDir(), "billing", ".marmot"), "")
	if err := warrenpkg.SaveProjectMetadata(source, &warrenpkg.ProjectMetadata{
		ProjectID: "billing-svc",
		WarrenID:  "product-platform",
		VaultID:   "billing-vault",
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata source: %v", err)
	}

	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "product-platform"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	if code := run([]string{"warren", "project", "import", "--generate-id", source, "--warren-dir", root}); code != 0 {
		t.Fatalf("project import --generate-id exit code = %d", code)
	}
	manifest, _, err := warrenpkg.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Projects) != 1 || manifest.Projects[0].ProjectID != "billing-svc" {
		t.Fatalf("expected generated ID billing-svc, got %+v", manifest.Projects)
	}
}

func TestWarrenProjectImportInvalidInputDoesNotRegisterProject(t *testing.T) {
	root := t.TempDir()
	source := writeCLIImportSourceVault(t, filepath.Join(t.TempDir(), "api", ".marmot"), "")

	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "product-platform"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	if code := run([]string{"warren", "project", "import", "api", source, "--warren-dir", root, "--vault-id", "../bad"}); code == 0 {
		t.Fatal("expected invalid vault ID import to fail")
	}
	if code := run([]string{"warren", "project", "import", "--warren-dir", root}); code == 0 {
		t.Fatal("expected missing args to fail")
	}
	if code := run([]string{"warren", "project", "import", "--help"}); code != 0 {
		t.Fatalf("project import --help exit code = %d, want 0", code)
	}
	output, code := captureRun([]string{"warren", "project", "list", "--warren-dir", root, "--json"})
	if code != 0 {
		t.Fatalf("project list --json exit code = %d output=%s", code, output)
	}
	if strings.TrimSpace(output) != "[]" {
		t.Fatalf("empty project list JSON = %q, want []", output)
	}
	manifest, _, err := warrenpkg.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Projects) != 0 {
		t.Fatalf("invalid import should not register project, got %+v", manifest.Projects)
	}
}

func TestWarrenAuthoringBridgeDoctorAndFormatCommands(t *testing.T) {
	root := t.TempDir()

	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "product-platform"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	for _, projectID := range []string{"project-a", "project-b"} {
		if code := run([]string{"warren", "project", "add", "--warren-dir", root, "--path", "projects/" + projectID + "/.marmot", projectID}); code != 0 {
			t.Fatalf("project add %s exit code = %d", projectID, code)
		}
	}
	if code := run([]string{"warren", "bridge", "add", "project-a", "project-b", "--warren-dir", root, "--relations", "calls,reads"}); code != 0 {
		t.Fatalf("bridge add exit code = %d", code)
	}
	if code := run([]string{"warren", "bridge", "list", "--warren-dir", root}); code != 0 {
		t.Fatalf("bridge list exit code = %d", code)
	}
	output, code := captureRun([]string{"warren", "bridge", "list", "--warren-dir", root, "--json"})
	if code != 0 {
		t.Fatalf("bridge list --json exit code = %d output=%s", code, output)
	}
	if !strings.Contains(output, "relations") || strings.Contains(output, "Relations") {
		t.Fatalf("bridge list JSON should use snake_case keys, got %s", output)
	}
	if code := run([]string{"warren", "doctor", "--warren-dir", root, "--json"}); code != 0 {
		t.Fatalf("doctor exit code = %d", code)
	}
	if code := run([]string{"warren", "format", "--warren-dir", root}); code != 0 {
		t.Fatalf("format exit code = %d", code)
	}

	manifest, _, err := warrenpkg.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(manifest.Bridges) != 1 {
		t.Fatalf("bridges after add = %+v", manifest.Bridges)
	}
	bridge := manifest.Bridges[0]
	if bridge.Source != "project-a" || bridge.Target != "project-b" || strings.Join(bridge.Relations, ",") != "calls,reads" {
		t.Fatalf("unexpected bridge = %+v", bridge)
	}

	if code := run([]string{"warren", "bridge", "remove", "project-a", "project-b", "--warren-dir", root}); code != 0 {
		t.Fatalf("bridge remove exit code = %d", code)
	}
	manifest, _, err = warrenpkg.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest after remove: %v", err)
	}
	if len(manifest.Bridges) != 0 {
		t.Fatalf("bridges after remove = %+v", manifest.Bridges)
	}
}

func TestBuildEngineQueriesActiveWarrenMount(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if err := os.MkdirAll(filepath.Join(marmotDir, ".marmot-data"), 0o755); err != nil {
		t.Fatalf("mkdir local .marmot-data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte("---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\nembedding_model: test-model\n---\n"), 0o644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	warrenRoot := testWarrenRoot(t, "product-platform", "project-a")
	remoteMarmot := filepath.Join(warrenRoot, "projects", "project-a", ".marmot")
	if err := os.MkdirAll(filepath.Join(remoteMarmot, ".marmot-data"), 0o755); err != nil {
		t.Fatalf("mkdir remote .marmot-data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remoteMarmot, "_config.md"), []byte("---\nversion: \"1\"\nvault_id: project-a\nnamespace: default\nembedding_provider: mock\nembedding_model: test-model\n---\n"), 0o644); err != nil {
		t.Fatalf("write remote config: %v", err)
	}
	store := node.NewStore(remoteMarmot)
	remoteNode := &node.Node{
		ID:        "service/api",
		Type:      "module",
		Namespace: "default",
		Status:    node.StatusActive,
		Summary:   "payments service API gateway",
	}
	if err := store.SaveNode(remoteNode); err != nil {
		t.Fatalf("save remote node: %v", err)
	}
	embedder := embedding.NewMockEmbedder("test-model")
	vec, err := embedder.Embed(remoteNode.Summary)
	if err != nil {
		t.Fatalf("embed remote node: %v", err)
	}
	embStore, err := embedding.NewStore(filepath.Join(remoteMarmot, ".marmot-data", "embeddings.db"))
	if err != nil {
		t.Fatalf("open remote embedding store: %v", err)
	}
	h := sha256.Sum256([]byte(remoteNode.Summary))
	if err := embStore.Upsert(remoteNode.ID, vec, hex.EncodeToString(h[:]), embedder.Model()); err != nil {
		t.Fatalf("upsert remote embedding: %v", err)
	}
	_ = embStore.Close()

	if _, err := warrenpkg.RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("RegisterWorkspaceWarren: %v", err)
	}
	if _, err := warrenpkg.Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	result := hermeticEngine(t, marmotDir)
	query, err := result.Engine.HandleContextQuery(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_query",
			Arguments: map[string]any{
				"query":  "payments gateway",
				"depth":  1,
				"budget": 4096,
			},
		},
	})
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	if query.IsError {
		t.Fatalf("query error: %+v", query.Content)
	}
	text, ok := query.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %#v", query.Content[0])
	}
	if !strings.Contains(text.Text, "@project-a/service/api") {
		t.Fatalf("expected Warren-mounted result, got:\n%s", text.Text)
	}
}

func testWarrenRoot(t *testing.T, warrenID string, projects ...string) string {
	t.Helper()
	root := t.TempDir()
	manifest := &warrenpkg.Manifest{WarrenID: warrenID}
	for _, projectID := range projects {
		marmotDir := filepath.Join(root, "projects", projectID, ".marmot")
		if err := warrenpkg.SaveProjectMetadata(marmotDir, &warrenpkg.ProjectMetadata{
			ProjectID: projectID,
			WarrenID:  warrenID,
			VaultID:   projectID,
		}, ""); err != nil {
			t.Fatalf("SaveProjectMetadata: %v", err)
		}
		manifest.Projects = append(manifest.Projects, warrenpkg.Project{
			ProjectID: projectID,
			Path:      filepath.ToSlash(filepath.Join("projects", projectID, ".marmot")),
		})
	}
	if err := warrenpkg.SaveManifest(root, manifest, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	return root
}

func writeCLIImportSourceVault(t *testing.T, marmotDir, vaultID string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(marmotDir, "service"), 0o755); err != nil {
		t.Fatalf("mkdir service: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(marmotDir, ".marmot-data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	vaultLine := ""
	if vaultID != "" {
		vaultLine = "vault_id: " + vaultID + "\n"
	}
	config := "---\nversion: \"1\"\n" + vaultLine + "namespace: default\nembedding_provider: mock\nembedding_model: test-model\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(config), 0o644); err != nil {
		t.Fatalf("write _config.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marmotDir, "service", "api.md"), []byte("---\nid: service/api\ntype: function\nsummary: API\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write node: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marmotDir, ".marmot-data", "embeddings.db"), []byte("db"), 0o644); err != nil {
		t.Fatalf("write embeddings: %v", err)
	}
	return marmotDir
}

func captureRun(args []string) (string, int) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", 1
	}
	os.Stdout = w
	code := run(args)
	_ = w.Close()
	os.Stdout = oldStdout
	data, _ := io.ReadAll(r)
	_ = r.Close()
	return string(data), code
}
