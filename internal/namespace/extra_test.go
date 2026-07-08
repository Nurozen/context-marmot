package namespace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetNamespace(t *testing.T) {
	dir := t.TempDir()
	writeMinimalNamespace(t, dir, "alpha")
	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if ns := m.GetNamespace("alpha"); ns == nil || ns.Name != "alpha" {
		t.Errorf("GetNamespace(alpha) = %+v, want namespace named alpha", ns)
	}
	if ns := m.GetNamespace("missing"); ns != nil {
		t.Errorf("GetNamespace(missing) = %+v, want nil", ns)
	}
}

func TestNamespaceDir(t *testing.T) {
	m := &Manager{VaultDir: "/vault"}
	if got := m.NamespaceDir("alpha"); got != filepath.Join("/vault", "alpha") {
		t.Errorf("NamespaceDir = %q", got)
	}
}

func TestEnsureNamespaceNameMismatch(t *testing.T) {
	dir := t.TempDir()
	// Directory "alpha" holds a manifest declaring a different name.
	nsDir := filepath.Join(dir, "alpha")
	os.MkdirAll(nsDir, 0o755)
	content := "---\nname: other\n---\n\nMismatch.\n"
	if err := os.WriteFile(filepath.Join(nsDir, "_namespace.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := EnsureNamespace(dir, "alpha", "")
	if err == nil {
		t.Fatal("expected error when manifest name does not match directory")
	}
}

func TestLoadBridgeNonexistent(t *testing.T) {
	_, err := LoadBridge(filepath.Join(t.TempDir(), "nope.md"))
	if err == nil {
		t.Fatal("expected error loading nonexistent bridge")
	}
}

func TestLoadNamespaceNonexistent(t *testing.T) {
	_, err := LoadNamespace(t.TempDir())
	if err == nil {
		t.Fatal("expected error loading nonexistent namespace manifest")
	}
}

func TestParseBridgeNoFrontmatter(t *testing.T) {
	if _, err := parseBridge([]byte("no frontmatter here")); err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseBridgeUnterminated(t *testing.T) {
	if _, err := parseBridge([]byte("---\nsource: a\n")); err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

func TestParseNamespaceUnterminated(t *testing.T) {
	if _, err := parseNamespace([]byte("---\nname: a\n")); err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

func TestCreateBridgeEmptyFields(t *testing.T) {
	dir := t.TempDir()
	if _, err := CreateBridge(dir, "", "b", nil); err == nil {
		t.Fatal("expected error for empty source")
	}
	if _, err := CreateBridge(dir, "a", "", nil); err == nil {
		t.Fatal("expected error for empty target")
	}
}

func TestManagerSkipsMalformedBridge(t *testing.T) {
	dir := t.TempDir()
	bridgeDir := filepath.Join(dir, "_bridges")
	os.MkdirAll(bridgeDir, 0o755)
	// A malformed bridge file (missing target) is skipped, not fatal.
	os.WriteFile(filepath.Join(bridgeDir, "bad.md"), []byte("---\nsource: a\n---\n"), 0o644)
	// A non-.md file is ignored.
	os.WriteFile(filepath.Join(bridgeDir, "notes.txt"), []byte("ignore"), 0o644)

	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if len(m.Bridges) != 0 {
		t.Errorf("expected 0 valid bridges, got %d", len(m.Bridges))
	}
}
