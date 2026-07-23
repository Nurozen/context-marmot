package warrenreg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setHome points the registry at a fresh temp MARMOT_HOME (hermetic — never
// touches ~/.marmot) and returns the expected registry path.
func setHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MARMOT_HOME", dir)
	return filepath.Join(dir, "warrens.yml")
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	setHome(t)
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg.Warrens == nil {
		t.Fatal("Warrens map is nil; want non-nil empty map")
	}
	if len(reg.Warrens) != 0 {
		t.Fatalf("want empty registry, got %d entries", len(reg.Warrens))
	}
	if reg.Version != CurrentVersion {
		t.Fatalf("Version = %d, want %d", reg.Version, CurrentVersion)
	}
}

func TestUpdateLoadRoundTrip(t *testing.T) {
	path := setHome(t)
	if err := Update(func(reg *Registry) error {
		reg.Warrens["team-docs"] = Entry{URL: "git@github.com:acme/team-docs.git", DefaultBranch: "main"}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("registry file not written: %v", err)
	}

	reg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e, ok := reg.Warrens["team-docs"]
	if !ok {
		t.Fatalf("entry missing after round-trip; have %v", reg.Warrens)
	}
	if e.URL != "git@github.com:acme/team-docs.git" || e.DefaultBranch != "main" {
		t.Fatalf("entry = %+v", e)
	}
	if reg.Version != CurrentVersion {
		t.Fatalf("Version = %d, want %d", reg.Version, CurrentVersion)
	}

	// Second Update sees the first entry (RMW, not overwrite).
	if err := Update(func(reg *Registry) error {
		if _, ok := reg.Warrens["team-docs"]; !ok {
			t.Error("Update fn did not see prior entry")
		}
		delete(reg.Warrens, "team-docs")
		return nil
	}); err != nil {
		t.Fatalf("Update (delete): %v", err)
	}
	reg, err = Load()
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if len(reg.Warrens) != 0 {
		t.Fatalf("want empty after delete, got %v", reg.Warrens)
	}
}

func TestUpdateFnErrorLeavesFileUntouched(t *testing.T) {
	path := setHome(t)
	if err := Update(func(reg *Registry) error {
		reg.Warrens["keep"] = Entry{URL: "https://example.com/keep.git"}
		return nil
	}); err != nil {
		t.Fatalf("seed Update: %v", err)
	}
	before, _ := os.ReadFile(path)

	wantErr := os.ErrPermission
	if err := Update(func(reg *Registry) error {
		reg.Warrens["dropped"] = Entry{URL: "https://example.com/dropped.git"}
		return wantErr
	}); err == nil {
		t.Fatal("Update should propagate fn error")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("file changed despite fn error:\nbefore: %s\nafter: %s", before, after)
	}
}

// TestVersionTolerance: a newer file with unknown fields loads permissively
// (known fields survive), but Update refuses to write it back — fixed-struct
// YAML would silently strip the unknown fields.
func TestVersionTolerance(t *testing.T) {
	path := setHome(t)
	newer := `version: 99
future_top_level: something
warrens:
  team-docs:
    url: https://example.com/team-docs.git
    default_branch: trunk
    future_entry_field: 42
`
	if err := os.WriteFile(path, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := Load()
	if err != nil {
		t.Fatalf("Load of newer version should be permissive: %v", err)
	}
	if reg.Version != 99 {
		t.Fatalf("Version = %d, want 99", reg.Version)
	}
	if e := reg.Warrens["team-docs"]; e.URL != "https://example.com/team-docs.git" || e.DefaultBranch != "trunk" {
		t.Fatalf("known fields not preserved on newer version: %+v", e)
	}

	err = Update(func(reg *Registry) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "newer than this marmot understands") {
		t.Fatalf("Update on newer version = %v, want version-ceiling refusal", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != newer {
		t.Fatalf("refused write still modified the file:\n%s", after)
	}
}

func TestVersionOmittedTreatedAsCurrent(t *testing.T) {
	path := setHome(t)
	// Additive tolerance in the other direction: an older/hand-written file
	// without a version line loads as CurrentVersion and stays writable.
	old := "warrens:\n  w1:\n    url: https://example.com/w1.git\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg.Version != CurrentVersion {
		t.Fatalf("Version = %d, want %d", reg.Version, CurrentVersion)
	}
	if err := Update(func(reg *Registry) error {
		reg.Warrens["w2"] = Entry{URL: "https://example.com/w2.git"}
		return nil
	}); err != nil {
		t.Fatalf("Update on version-less file: %v", err)
	}
	reg, err = Load()
	if err != nil {
		t.Fatalf("Load after Update: %v", err)
	}
	if len(reg.Warrens) != 2 || reg.Version != CurrentVersion {
		t.Fatalf("after Update: version=%d warrens=%v", reg.Version, reg.Warrens)
	}
}
