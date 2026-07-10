package warren

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetEditableRefusesMaterialized: enabling edit on a burrowed project is
// a contract violation (edits would land in the cache and never sync back),
// so SetEditable must refuse and leave the state file untouched.
func TestSetEditableRefusesMaterialized(t *testing.T) {
	workspace := t.TempDir()
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, true); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	project := Project{ProjectID: "project-a", Path: "projects/project-a/.marmot"}
	if _, err := Materialize(workspaceMarmotDir(workspace), "product-platform", project, warrenRoot); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	statePath := filepath.Join(workspace, ".marmot", "_warren.md")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	_, err = SetEditable(workspace, "product-platform", "project-a", true)
	if err == nil || !strings.Contains(err.Error(), "materialized") {
		t.Fatalf("SetEditable err = %v, want materialized refusal", err)
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after: %v", err)
	}
	if string(before) != string(after) {
		t.Error("refused SetEditable still mutated the state file")
	}

	// Disabling (--off) stays allowed regardless.
	if _, err := SetEditable(workspace, "product-platform", "project-a", false); err != nil {
		t.Fatalf("SetEditable --off: %v", err)
	}
}

// TestMountMaterializedRefusesEditable is the mirror image: materializing a
// mount that includes an editable project must refuse.
func TestMountMaterializedRefusesEditable(t *testing.T) {
	workspace := t.TempDir()
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a", "project-b")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if _, err := SetEditable(workspace, "product-platform", "project-a", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}

	_, err := Mount(workspace, "product-platform", []string{"project-a"}, true)
	if err == nil || !strings.Contains(err.Error(), "editable") {
		t.Fatalf("Mount --materialize err = %v, want editable refusal", err)
	}

	// Materializing a different, non-editable project stays allowed.
	if _, err := Mount(workspace, "product-platform", []string{"project-b"}, true); err != nil {
		t.Fatalf("Mount project-b --materialize: %v", err)
	}
}

// TestPreferredPathEditableWinsOverStaleFlags: a hand-crafted state carrying
// both flags (pre-refusal binaries could write this) must resolve the
// editable project to the checkout path, not the burrow cache.
func TestPreferredPathEditableWinsOverStaleFlags(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	project := Project{ProjectID: "project-a", Path: "projects/project-a/.marmot"}
	if _, err := Materialize(marmotDir, "product-platform", project, warrenRoot); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	// Hand-craft both-flags state, bypassing the refusal guards.
	state := &WorkspaceState{Warrens: map[string]WorkspaceWarren{
		"product-platform": {
			Path:             warrenRoot,
			ActiveProjects:   []string{"project-a"},
			EditableProjects: []string{"project-a"},
			Materialized:     true,
		},
	}}
	if err := SaveWorkspaceState(workspace, state, ""); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	mounts, err := ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts = %+v, want 1", mounts)
	}
	checkout := filepath.Join(warrenRoot, "projects", "project-a", ".marmot")
	if mounts[0].Path != checkout {
		t.Errorf("mount path = %q, want checkout %q (editable must win over stale materialized flag)", mounts[0].Path, checkout)
	}
	if !mounts[0].Editable {
		t.Error("mount should be editable")
	}
	if mounts[0].Materialized {
		t.Error("mount resolved to the checkout must not report Materialized")
	}
}
