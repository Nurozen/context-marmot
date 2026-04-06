package routes

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Concurrent read/write stress
// ---------------------------------------------------------------------------

func TestStressConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")

	// Seed a table on disk so Load doesn't start from nothing every time.
	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	rt.Set("seed", "/seed/path")
	if err := SaveTo(rt, path); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	const goroutines = 20
	const opsPerGoroutine = 50
	var wg sync.WaitGroup

	// Mix of writers (Set+Save) and readers (Load+Get).
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				vaultID := fmt.Sprintf("vault-%d-%d", id, j)
				if id%2 == 0 {
					// Writer: load, mutate, save.
					tbl, err := LoadFrom(path)
					if err != nil {
						t.Errorf("goroutine %d: load: %v", id, err)
						return
					}
					tbl.Set(vaultID, "/path/"+vaultID)
					if err := SaveTo(tbl, path); err != nil {
						t.Errorf("goroutine %d: save: %v", id, err)
						return
					}
				} else {
					// Reader: load + get.
					tbl, err := LoadFrom(path)
					if err != nil {
						t.Errorf("goroutine %d: load: %v", id, err)
						return
					}
					_ = tbl.List()
					_, _ = tbl.Get("seed")
				}
			}
		}(i)
	}
	wg.Wait()

	// Final read should succeed and contain the seed entry.
	final, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("final load: %v", err)
	}
	if _, ok := final.Get("seed"); !ok {
		t.Error("seed entry disappeared after concurrent writes")
	}
}

// ---------------------------------------------------------------------------
// Override path isolation
// ---------------------------------------------------------------------------

func TestStressOverridePathIsolation(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	pathA := filepath.Join(dirA, "routes.yml")
	pathB := filepath.Join(dirB, "routes.yml")

	// Write distinct tables to each path.
	rtA := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	rtA.Set("vault-a", "/a")
	if err := SaveTo(rtA, pathA); err != nil {
		t.Fatalf("save A: %v", err)
	}

	rtB := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	rtB.Set("vault-b", "/b")
	if err := SaveTo(rtB, pathB); err != nil {
		t.Fatalf("save B: %v", err)
	}

	// Point override to A.
	SetOverridePath(pathA)
	loaded, err := Load()
	if err != nil {
		t.Fatalf("load with override A: %v", err)
	}
	if _, ok := loaded.Get("vault-a"); !ok {
		t.Error("expected vault-a when override points to A")
	}
	if _, ok := loaded.Get("vault-b"); ok {
		t.Error("vault-b should not appear when override points to A")
	}

	// Switch override to B.
	SetOverridePath(pathB)
	loaded, err = Load()
	if err != nil {
		t.Fatalf("load with override B: %v", err)
	}
	if _, ok := loaded.Get("vault-b"); !ok {
		t.Error("expected vault-b when override points to B")
	}

	// Reset to default (empty string).
	SetOverridePath("")
	dp := DefaultPath()
	if dp == pathA || dp == pathB {
		t.Errorf("after reset, DefaultPath should not be A or B, got %s", dp)
	}

	// Cleanup: ensure subsequent tests don't inherit the override.
	t.Cleanup(func() { SetOverridePath("") })
}

// ---------------------------------------------------------------------------
// Round-trip with special characters in vault IDs
// ---------------------------------------------------------------------------

func TestStressSpecialCharacterVaultIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")

	ids := []string{
		"simple",
		"vault-with-hyphens",
		"vault_with_underscores",
		"vault123",
		"UPPER-case",
		"mix_123-ABC",
		"a",
		"vault--double-hyphen",
		"__leading_underscores",
		"trailing---",
		"123-numeric-start",
		"vault.with.dots",
	}

	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	for i, id := range ids {
		rt.Set(id, fmt.Sprintf("/path/%d", i))
	}

	if err := SaveTo(rt, path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	for i, id := range ids {
		expected := fmt.Sprintf("/path/%d", i)
		got, ok := loaded.Get(id)
		if !ok {
			t.Errorf("vault ID %q not found after round-trip", id)
		} else if got != expected {
			t.Errorf("vault ID %q: expected path %q, got %q", id, expected, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Empty table operations
// ---------------------------------------------------------------------------

func TestStressEmptyTableOperations(t *testing.T) {
	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}

	// Get on empty table.
	if _, ok := rt.Get("anything"); ok {
		t.Error("Get on empty table should return false")
	}

	// Remove on empty table.
	if rt.Remove("anything") {
		t.Error("Remove on empty table should return false")
	}

	// List on empty table.
	list := rt.List()
	if len(list) != 0 {
		t.Errorf("List on empty table should be empty, got %d", len(list))
	}

	// Nil vaults map.
	rt2 := &RoutingTable{}
	if _, ok := rt2.Get("x"); ok {
		t.Error("Get on nil Vaults should return false")
	}
	if rt2.Remove("x") {
		t.Error("Remove on nil Vaults should return false")
	}

	// Nil table pointer.
	var rt3 *RoutingTable
	if _, ok := rt3.Get("x"); ok {
		t.Error("Get on nil *RoutingTable should return false")
	}
}

// ---------------------------------------------------------------------------
// Save to nested nonexistent directory
// ---------------------------------------------------------------------------

func TestStressSaveToNestedDir(t *testing.T) {
	base := t.TempDir()
	// Three levels deep, none existing yet.
	path := filepath.Join(base, "a", "b", "c", "routes.yml")

	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	rt.Set("deep", "/deep/path")

	if err := SaveTo(rt, path); err != nil {
		t.Fatalf("SaveTo nested dir: %v", err)
	}

	// Verify the intermediate directories were created.
	info, err := os.Stat(filepath.Join(base, "a", "b", "c"))
	if err != nil {
		t.Fatalf("nested dir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory, got file")
	}

	// Verify round-trip.
	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load from nested: %v", err)
	}
	if p, ok := loaded.Get("deep"); !ok || p != "/deep/path" {
		t.Errorf("expected deep -> /deep/path, got %q (ok=%v)", p, ok)
	}
}

// ---------------------------------------------------------------------------
// Corrupt YAML
// ---------------------------------------------------------------------------

func TestStressCorruptYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")

	corruptData := []byte("vaults:\n  - this is not valid:\n    [broken yaml {{{\n")
	if err := os.WriteFile(path, corruptData, 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected error loading corrupt YAML, got nil")
	}
}

func TestStressCorruptYAMLVariants(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"truncated", "vaults:\n  alpha:\n    path: /a\n  beta:\n    path:"},
		{"tabs_and_garbage", "\t\t\tgarbage not yaml at all !!@#$%"},
		{"binary_like", "\x00\x01\x02\x03"},
		{"empty_file", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "routes.yml")
			if err := os.WriteFile(path, []byte(tc.data), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			rt, err := LoadFrom(path)
			// We accept either a clean empty table or an error—but never a panic.
			if err != nil {
				return // error is acceptable for corrupt data
			}
			// If no error, table must be usable.
			_ = rt.List()
		})
	}
}

// ---------------------------------------------------------------------------
// Large table round-trip
// ---------------------------------------------------------------------------

func TestStressLargeTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")

	const count = 500
	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	for i := 0; i < count; i++ {
		rt.Set(fmt.Sprintf("vault-%04d", i), fmt.Sprintf("/projects/vault-%04d/.marmot", i))
	}

	if err := SaveTo(rt, path); err != nil {
		t.Fatalf("save large table: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load large table: %v", err)
	}

	if len(loaded.Vaults) != count {
		t.Fatalf("expected %d entries, got %d", count, len(loaded.Vaults))
	}

	// Spot-check several entries.
	for _, i := range []int{0, 1, 99, 250, 499} {
		id := fmt.Sprintf("vault-%04d", i)
		expected := fmt.Sprintf("/projects/vault-%04d/.marmot", i)
		got, ok := loaded.Get(id)
		if !ok {
			t.Errorf("vault %s missing", id)
		} else if got != expected {
			t.Errorf("vault %s: expected %s, got %s", id, expected, got)
		}
	}

	// Remove half and verify.
	for i := 0; i < count; i += 2 {
		loaded.Remove(fmt.Sprintf("vault-%04d", i))
	}
	remaining := loaded.List()
	if len(remaining) != count/2 {
		t.Errorf("expected %d remaining after removing evens, got %d", count/2, len(remaining))
	}
}

// ---------------------------------------------------------------------------
// Concurrent Set/Get on the same RoutingTable (in-memory races)
// ---------------------------------------------------------------------------

// TestStressConcurrentTableAccess_BugDocumented verifies that RoutingTable's
// in-memory methods (Get/Set/List/Remove) are safe for concurrent use.
//
// Previously, these methods had no synchronization, which caused fatal
// "concurrent map read and map write" panics when a *RoutingTable was shared
// across goroutines (e.g., via VaultRegistry). Fixed by adding a sync.RWMutex
// to RoutingTable that guards all map access in Get, Set, List, and Remove.
func TestStressConcurrentTableAccess_BugDocumented(t *testing.T) {

	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}

	const goroutines = 50
	const ops = 100
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				key := fmt.Sprintf("v-%d", j%10) // shared keys to cause contention
				switch id % 3 {
				case 0:
					rt.Set(key, fmt.Sprintf("/path/%d/%d", id, j))
				case 1:
					_, _ = rt.Get(key)
				default:
					_ = rt.List()
				}
			}
		}(i)
	}
	wg.Wait()
}
