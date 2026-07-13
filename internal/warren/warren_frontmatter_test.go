package warren

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestManifestRoundTripBodyWithDashes guards the anchored frontmatter parser:
// a "---" line in the markdown body must survive Save -> Load without the
// parser mistaking it for the closing delimiter and corrupting the file.
func TestManifestRoundTripBodyWithDashes(t *testing.T) {
	root := t.TempDir()
	manifest := &Manifest{
		WarrenID: "product-platform",
		Projects: []Project{{ProjectID: "api", Path: "projects/api/.marmot"}},
	}
	body := "# Warren notes\n\nSection one\n\n---\n\nSection two after a horizontal rule.\n"
	if err := SaveManifest(root, manifest, body); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	for i := 0; i < 3; i++ {
		loaded, loadedBody, err := LoadManifest(root)
		if err != nil {
			t.Fatalf("LoadManifest cycle %d: %v", i, err)
		}
		if loadedBody != body {
			t.Fatalf("cycle %d body = %q, want %q", i, loadedBody, body)
		}
		if loaded.WarrenID != "product-platform" || len(loaded.Projects) != 1 || loaded.Projects[0].ProjectID != "api" {
			t.Fatalf("cycle %d manifest corrupted: %+v", i, loaded)
		}
		if err := SaveManifest(root, loaded, loadedBody); err != nil {
			t.Fatalf("SaveManifest cycle %d: %v", i, err)
		}
	}
}

// TestWorkspaceStateRoundTripBodyWithDashes: the workspace _warren.md body
// must survive an updateWorkspaceState cycle even when it contains "---".
func TestWorkspaceStateRoundTripBodyWithDashes(t *testing.T) {
	workspace := t.TempDir()
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Rewrite the state file with a body containing a bare --- line.
	state, _, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	body := "Local mount notes\n\n---\n\nMore notes.\n"
	if err := SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	// A mutation through updateWorkspaceState must preserve the body.
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	after, afterBody, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState after Mount: %v", err)
	}
	if afterBody != body {
		t.Fatalf("body after Mount = %q, want %q", afterBody, body)
	}
	if !reflect.DeepEqual(after.Warrens["product-platform"].ActiveProjects, []string{"project-a"}) {
		t.Fatalf("active projects = %+v", after.Warrens["product-platform"].ActiveProjects)
	}
}

// TestParseMarkdownYAMLDashesInValue: quoted "---" inside a YAML value must
// not close the frontmatter.
func TestParseMarkdownYAMLDashesInValue(t *testing.T) {
	root := t.TempDir()
	content := "---\nwarren_id: product-platform\nversion: 1\nprojects:\n  - project_id: api\n    path: projects/api/.marmot\n    aliases:\n      - \"---\"\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(root, ManifestFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, body, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if body != "body\n" {
		t.Fatalf("body = %q, want %q", body, "body\n")
	}
	if len(manifest.Projects) != 1 || len(manifest.Projects[0].Aliases) != 1 || manifest.Projects[0].Aliases[0] != "---" {
		t.Fatalf("aliases lost: %+v", manifest.Projects)
	}
}
