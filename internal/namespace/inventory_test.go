package namespace

import (
	"os"
	"path/filepath"
	"testing"
)

// writeNode writes a minimal node file with the given namespace into nsDir.
func writeNode(t *testing.T, nsDir, id, ns string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(nsDir, id+".md")), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: " + id + "\ntype: concept\nnamespace: " + ns + "\nstatus: active\n---\n\nNode " + id + ".\n"
	if err := os.WriteFile(filepath.Join(nsDir, id+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("writeNode(%s): %v", id, err)
	}
}

func TestInventory(t *testing.T) {
	dir := t.TempDir()

	// alpha: has manifest and nodes.
	writeMinimalNamespace(t, dir, "alpha")
	writeNode(t, filepath.Join(dir, "alpha"), "one", "alpha")
	writeNode(t, filepath.Join(dir, "alpha"), "two", "alpha")

	// beta: has nodes but no manifest (implicit namespace).
	os.MkdirAll(filepath.Join(dir, "beta"), 0o755)
	writeNode(t, filepath.Join(dir, "beta"), "solo", "beta")

	items, err := Inventory(dir)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}

	byName := make(map[string]InventoryItem)
	for _, item := range items {
		byName[item.Name] = item
	}

	a, ok := byName["alpha"]
	if !ok {
		t.Fatal("expected alpha in inventory")
	}
	if a.NodeCount != 2 {
		t.Errorf("alpha NodeCount = %d, want 2", a.NodeCount)
	}
	if !a.HasManifest {
		t.Error("alpha should have a manifest")
	}

	b, ok := byName["beta"]
	if !ok {
		t.Fatal("expected beta in inventory")
	}
	if b.NodeCount != 1 {
		t.Errorf("beta NodeCount = %d, want 1", b.NodeCount)
	}
	if b.HasManifest {
		t.Error("beta should not have a manifest")
	}

	// Results are sorted by name.
	for i := 1; i < len(items); i++ {
		if items[i-1].Name > items[i].Name {
			t.Errorf("inventory not sorted: %q before %q", items[i-1].Name, items[i].Name)
		}
	}
}

func TestInventoryEmptyVault(t *testing.T) {
	dir := t.TempDir()
	items, err := Inventory(dir)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(items) != 1 || items[0].Name != "default" {
		t.Fatalf("expected single default item, got %+v", items)
	}
}

func TestInventoryDefaultNamespace(t *testing.T) {
	dir := t.TempDir()
	// A node at the vault root with no namespace maps to "default".
	writeNode(t, dir, "rootnode", "")

	items, err := Inventory(dir)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	byName := make(map[string]InventoryItem)
	for _, item := range items {
		byName[item.Name] = item
	}
	d, ok := byName["default"]
	if !ok {
		t.Fatal("expected default namespace")
	}
	if d.NodeCount != 1 {
		t.Errorf("default NodeCount = %d, want 1", d.NodeCount)
	}
}

func TestDoctorMissingManifestWarning(t *testing.T) {
	dir := t.TempDir()
	// beta has nodes but no _namespace.md manifest.
	os.MkdirAll(filepath.Join(dir, "beta"), 0o755)
	writeNode(t, filepath.Join(dir, "beta"), "solo", "beta")

	issues, err := Doctor(dir)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}

	var found bool
	for _, iss := range issues {
		if iss.Namespace == "beta" && iss.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning about beta missing manifest, got %+v", issues)
	}
}

func TestDoctorManifestNameMismatch(t *testing.T) {
	dir := t.TempDir()
	// Directory "alpha" but the manifest declares a different name.
	nsDir := filepath.Join(dir, "alpha")
	os.MkdirAll(nsDir, 0o755)
	content := "---\nname: not-alpha\n---\n\nMismatched.\n"
	if err := os.WriteFile(filepath.Join(nsDir, "_namespace.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	issues, err := Doctor(dir)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}

	var found bool
	for _, iss := range issues {
		if iss.Namespace == "alpha" && iss.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error about name mismatch, got %+v", issues)
	}
}

func TestDoctorUnloadableManifest(t *testing.T) {
	dir := t.TempDir()
	// A _namespace.md with no name field fails to parse.
	nsDir := filepath.Join(dir, "broken")
	os.MkdirAll(nsDir, 0o755)
	content := "---\nroot_path: /tmp\n---\n\nNo name.\n"
	if err := os.WriteFile(filepath.Join(nsDir, "_namespace.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	issues, err := Doctor(dir)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}

	var found bool
	for _, iss := range issues {
		if iss.Namespace == "broken" && iss.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error about unloadable manifest, got %+v", issues)
	}
}

func TestDoctorBridgeUnknownNamespace(t *testing.T) {
	dir := t.TempDir()
	writeMinimalNamespace(t, dir, "alpha")
	// Bridge points at "ghost" which does not exist.
	if _, err := CreateBridge(dir, "alpha", "ghost", []string{"calls"}); err != nil {
		t.Fatal(err)
	}

	issues, err := Doctor(dir)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}

	var found bool
	for _, iss := range issues {
		if iss.Namespace == "ghost" && iss.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error about bridge referencing unknown namespace, got %+v", issues)
	}
}

func TestDoctorClean(t *testing.T) {
	dir := t.TempDir()
	writeMinimalNamespace(t, dir, "alpha")
	writeMinimalNamespace(t, dir, "beta")
	writeNode(t, filepath.Join(dir, "alpha"), "one", "alpha")
	if _, err := CreateBridge(dir, "alpha", "beta", []string{"calls"}); err != nil {
		t.Fatal(err)
	}

	issues, err := Doctor(dir)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected no issues for a clean vault, got %+v", issues)
	}
}
