package den

import (
	"os"
	"path/filepath"
	"testing"
)

func relSet(rels []string) map[string]bool {
	m := make(map[string]bool, len(rels))
	for _, r := range rels {
		m[r] = true
	}
	return m
}

func TestAddListBridge(t *testing.T) {
	t.Setenv("MARMOT_HOME", t.TempDir())

	b, added, err := AddBridge("d1", "@web", "@api", []string{"calls", "reads"})
	if err != nil {
		t.Fatalf("AddBridge: %v", err)
	}
	if !added {
		t.Fatal("first AddBridge should report added=true")
	}
	// The leading '@' is stripped into the stored vault ids.
	if b.SourceVaultID != "web" || b.TargetVaultID != "api" {
		t.Fatalf("bridge ids = %q/%q, want web/api", b.SourceVaultID, b.TargetVaultID)
	}
	if !b.IsCrossVault() {
		t.Fatal("den bridge must be cross-vault shaped")
	}

	// The manifest lands in the den DIR's _bridges/, sibling of vault/.
	wantFile := filepath.Join(BridgesDir("d1"), "@web--@api.md")
	if _, err := os.Stat(wantFile); err != nil {
		t.Fatalf("bridge manifest not at %s: %v", wantFile, err)
	}

	bridges, err := ListBridges("d1")
	if err != nil {
		t.Fatalf("ListBridges: %v", err)
	}
	if len(bridges) != 1 {
		t.Fatalf("ListBridges = %d, want 1", len(bridges))
	}
	if got := relSet(bridges[0].AllowedRelations); !got["calls"] || !got["reads"] {
		t.Fatalf("relations = %v", bridges[0].AllowedRelations)
	}
}

func TestAddBridgeIdempotentAndMerge(t *testing.T) {
	t.Setenv("MARMOT_HOME", t.TempDir())

	if _, added, err := AddBridge("d1", "web", "api", []string{"calls", "reads"}); err != nil || !added {
		t.Fatalf("initial add: added=%v err=%v", added, err)
	}

	// Exact re-add: idempotent no-op (added=false, same relations).
	b, added, err := AddBridge("d1", "web", "api", []string{"reads", "calls"})
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if added {
		t.Fatal("exact re-add must report added=false")
	}
	if len(b.AllowedRelations) != 2 {
		t.Fatalf("re-add must not grow relations: %v", b.AllowedRelations)
	}

	// Re-add with a new relation: merged, added=false.
	b, added, err = AddBridge("d1", "web", "api", []string{"writes"})
	if err != nil {
		t.Fatalf("merge add: %v", err)
	}
	if added {
		t.Fatal("merge into existing bridge must report added=false")
	}
	got := relSet(b.AllowedRelations)
	if !got["calls"] || !got["reads"] || !got["writes"] {
		t.Fatalf("merged relations = %v, want calls+reads+writes", b.AllowedRelations)
	}

	// A reverse-orientation add merges into the same bridge (no second file).
	if _, added, err := AddBridge("d1", "api", "web", []string{"references"}); err != nil || added {
		t.Fatalf("reverse add: added=%v err=%v (want added=false)", added, err)
	}
	bridges, err := ListBridges("d1")
	if err != nil {
		t.Fatalf("ListBridges: %v", err)
	}
	if len(bridges) != 1 {
		t.Fatalf("reverse add created a duplicate: %d bridges", len(bridges))
	}
}

func TestRemoveBridgeBothDirections(t *testing.T) {
	t.Setenv("MARMOT_HOME", t.TempDir())

	if _, _, err := AddBridge("d1", "web", "api", []string{"calls"}); err != nil {
		t.Fatalf("AddBridge: %v", err)
	}

	// Removing the reverse orientation still matches the @web--@api.md file.
	removed, err := RemoveBridge("d1", "@api", "@web")
	if err != nil {
		t.Fatalf("RemoveBridge: %v", err)
	}
	if !removed {
		t.Fatal("reverse-direction rm should match and remove the bridge")
	}
	bridges, err := ListBridges("d1")
	if err != nil {
		t.Fatalf("ListBridges: %v", err)
	}
	if len(bridges) != 0 {
		t.Fatalf("bridge not removed: %d remain", len(bridges))
	}

	// Removing a missing bridge is a no-op, not an error.
	removed, err = RemoveBridge("d1", "web", "api")
	if err != nil {
		t.Fatalf("rm missing: %v", err)
	}
	if removed {
		t.Fatal("rm of a missing bridge must report removed=false")
	}
}

func TestAddBridgeValidation(t *testing.T) {
	t.Setenv("MARMOT_HOME", t.TempDir())

	cases := []struct{ from, to string }{
		{"", "api"},
		{"web", ""},
		{"web", "web"},  // same source/target
		{"@web", "web"}, // same after stripping '@'
		{"a/b", "api"},  // path separator
		{"web", "a\\b"}, // backslash
		{"web", "a..b"}, // traversal token
		{"web", "a\tb"}, // control char
	}
	for _, tc := range cases {
		if _, _, err := AddBridge("d1", tc.from, tc.to, []string{"calls"}); err == nil {
			t.Errorf("AddBridge(%q, %q) should have failed", tc.from, tc.to)
		}
	}
}

func TestListBridgesSkipsMalformed(t *testing.T) {
	t.Setenv("MARMOT_HOME", t.TempDir())

	if _, _, err := AddBridge("d1", "web", "api", []string{"calls"}); err != nil {
		t.Fatalf("AddBridge: %v", err)
	}
	// Drop a malformed manifest alongside the valid one.
	bad := filepath.Join(BridgesDir("d1"), "@broken--@junk.md")
	if err := os.WriteFile(bad, []byte("not a manifest, no frontmatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bridges, err := ListBridges("d1")
	if err != nil {
		t.Fatalf("ListBridges: %v", err)
	}
	if len(bridges) != 1 {
		t.Fatalf("ListBridges = %d, want 1 (malformed skipped)", len(bridges))
	}
}

func TestListBridgesAtEmpty(t *testing.T) {
	dir := t.TempDir()
	bridges, err := ListBridgesAt(dir)
	if err != nil {
		t.Fatalf("ListBridgesAt on a den root without _bridges: %v", err)
	}
	if len(bridges) != 0 {
		t.Fatalf("expected no bridges, got %d", len(bridges))
	}
}

func TestAddBridgeVariadicNoDefault(t *testing.T) {
	// AddBridge itself applies no default relations; the CLI layer does. An
	// add with no relations writes an empty allowed-relations set.
	t.Setenv("MARMOT_HOME", t.TempDir())
	b, added, err := AddBridge("d1", "web", "api", nil)
	if err != nil || !added {
		t.Fatalf("AddBridge(nil rels): added=%v err=%v", added, err)
	}
	if len(b.AllowedRelations) != 0 {
		t.Fatalf("expected empty relations, got %v", b.AllowedRelations)
	}
}
