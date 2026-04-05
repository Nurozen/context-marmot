package routes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromNonexistent(t *testing.T) {
	rt, err := LoadFrom("/nonexistent/routes.yml")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(rt.Vaults) != 0 {
		t.Fatalf("expected empty table, got %d entries", len(rt.Vaults))
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")

	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	rt.Set("alpha", "/projects/alpha/.marmot")
	rt.Set("beta", "/projects/beta/.marmot")

	if err := SaveTo(rt, path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	p, ok := loaded.Get("alpha")
	if !ok || p != "/projects/alpha/.marmot" {
		t.Errorf("expected alpha -> /projects/alpha/.marmot, got %q (ok=%v)", p, ok)
	}

	p, ok = loaded.Get("beta")
	if !ok || p != "/projects/beta/.marmot" {
		t.Errorf("expected beta -> /projects/beta/.marmot, got %q (ok=%v)", p, ok)
	}

	_, ok = loaded.Get("missing")
	if ok {
		t.Error("expected missing to not be found")
	}
}

func TestRemove(t *testing.T) {
	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	rt.Set("alpha", "/path/a")
	rt.Set("beta", "/path/b")

	if !rt.Remove("alpha") {
		t.Error("expected Remove to return true for existing entry")
	}
	if rt.Remove("alpha") {
		t.Error("expected Remove to return false for already-removed entry")
	}

	_, ok := rt.Get("alpha")
	if ok {
		t.Error("expected alpha to be gone after Remove")
	}

	p, ok := rt.Get("beta")
	if !ok || p != "/path/b" {
		t.Error("expected beta to still exist")
	}
}

func TestList(t *testing.T) {
	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	rt.Set("a", "/path/a")
	rt.Set("b", "/path/b")

	list := rt.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(list))
	}
	if list["a"] != "/path/a" || list["b"] != "/path/b" {
		t.Errorf("unexpected list contents: %v", list)
	}
}

func TestNilSafety(t *testing.T) {
	var rt *RoutingTable
	_, ok := rt.Get("x")
	if ok {
		t.Error("expected Get on nil table to return false")
	}

	rt = &RoutingTable{}
	_, ok = rt.Get("x")
	if ok {
		t.Error("expected Get on empty table to return false")
	}
	if rt.Remove("x") {
		t.Error("expected Remove on nil Vaults to return false")
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "routes.yml")

	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	rt.Set("test", "/test/path")

	// SaveTo should create intermediate directories.
	if err := SaveTo(rt, path); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify no tmp file left behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("expected tmp file to be cleaned up")
	}
}

func TestLoadFromEmptyPath(t *testing.T) {
	rt, err := LoadFrom("")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(rt.Vaults) != 0 {
		t.Fatalf("expected empty table, got %d entries", len(rt.Vaults))
	}
}
