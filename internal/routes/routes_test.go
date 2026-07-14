package routes

import (
	"fmt"
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

// TestEnvOverrideDisables is the regression test for manual-test issue 9:
// a fresh scratch vault must be able to opt out of the user's global
// ~/.marmot/routes.yml via MARMOT_ROUTES=off (aliases: none, 0).
func TestEnvOverrideDisables(t *testing.T) {
	for _, val := range []string{"off", "none", "0"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("MARMOT_ROUTES", val)
			if got := DefaultPath(); got != "" {
				t.Errorf("expected empty path, got %q", got)
			}
			rt, err := Load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(rt.Vaults) != 0 {
				t.Errorf("expected empty table, got %d entries", len(rt.Vaults))
			}
			if err := Save(rt); err == nil {
				t.Error("expected Save to fail while routing is disabled")
			}
			if err := Update(func(rt *RoutingTable) error { return nil }); err == nil {
				t.Error("expected Update to fail while routing is disabled")
			}
		})
	}
}

func TestEnvOverrideRedirectsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.yml")
	t.Setenv("MARMOT_ROUTES", path)

	if got := DefaultPath(); got != path {
		t.Fatalf("expected DefaultPath %q, got %q", path, got)
	}

	if err := Update(func(rt *RoutingTable) error {
		rt.Set("alpha", "/projects/alpha/.marmot")
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	rt, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p, ok := rt.Get("alpha"); !ok || p != "/projects/alpha/.marmot" {
		t.Errorf("expected alpha route via env-redirected table, got %q (ok=%v)", p, ok)
	}
}

func TestSetOverridePathWinsOverEnv(t *testing.T) {
	override := filepath.Join(t.TempDir(), "override.yml")
	t.Setenv("MARMOT_ROUTES", "off")
	SetOverridePath(override)
	defer SetOverridePath("")

	if got := DefaultPath(); got != override {
		t.Errorf("expected SetOverridePath to win over MARMOT_ROUTES, got %q", got)
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

func TestUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")
	SetOverridePath(path)
	defer SetOverridePath("")

	// Seed an initial entry.
	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	rt.Set("existing", "/existing/path")
	if err := SaveTo(rt, path); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Update atomically adds a new entry.
	err := Update(func(rt *RoutingTable) error {
		rt.Set("new-vault", "/new/path")
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	// Verify both entries exist.
	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p, ok := loaded.Get("existing"); !ok || p != "/existing/path" {
		t.Errorf("existing entry lost: ok=%v p=%q", ok, p)
	}
	if p, ok := loaded.Get("new-vault"); !ok || p != "/new/path" {
		t.Errorf("new entry missing: ok=%v p=%q", ok, p)
	}
}

func TestUpdateError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")
	SetOverridePath(path)
	defer SetOverridePath("")

	// Update with error should not write.
	err := Update(func(rt *RoutingTable) error {
		rt.Set("should-not-persist", "/nowhere")
		return fmt.Errorf("abort")
	})
	if err == nil {
		t.Fatal("expected error from Update")
	}

	// File should not exist (nothing was ever saved).
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("expected no file after aborted Update")
	}
}

func TestProjectsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")
	SetOverridePath(path)
	defer SetOverridePath("")

	proj := t.TempDir()
	if err := Update(func(rt *RoutingTable) error {
		rt.SetProject(proj, "den-a")
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	id, ok := loaded.GetProject(proj)
	if !ok || id != "den-a" {
		t.Fatalf("GetProject = %q ok=%v", id, ok)
	}
	list := loaded.ListProjects()
	if len(list) != 1 {
		t.Fatalf("ListProjects = %v", list)
	}

	if err := Update(func(rt *RoutingTable) error {
		if !rt.RemoveProject(proj) {
			return fmt.Errorf("expected remove true")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _ = Load()
	if _, ok := loaded.GetProject(proj); ok {
		t.Fatal("project still present after remove")
	}
}

func TestNormalizeProjectKey(t *testing.T) {
	dir := t.TempDir()
	key, err := NormalizeProjectKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(key) {
		t.Fatalf("expected abs key, got %q", key)
	}
	// Relative form should resolve to same abs key.
	// Create a subdir and normalize via join.
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	k2, err := NormalizeProjectKey(sub)
	if err != nil {
		t.Fatal(err)
	}
	if k2 == key {
		t.Fatal("subdir key should differ from parent")
	}
}

func TestEmptyTableHasProjects(t *testing.T) {
	rt := EmptyTable()
	if rt.Projects == nil {
		t.Fatal("EmptyTable Projects must be non-nil")
	}
	if rt.Vaults == nil {
		t.Fatal("EmptyTable Vaults must be non-nil")
	}
}

func TestMARMOT_HOMEDefaultPath(t *testing.T) {
	// When MARMOT_ROUTES is unset and no override, DefaultPath uses MARMOT_HOME.
	SetOverridePath("")
	defer SetOverridePath("")
	t.Setenv("MARMOT_ROUTES", "")
	tmp := t.TempDir()
	t.Setenv("MARMOT_HOME", tmp)
	got := DefaultPath()
	want := filepath.Join(tmp, "routes.yml")
	if got != want {
		t.Fatalf("DefaultPath = %q, want %q", got, want)
	}
}

func TestNormalizeProjectKeyEmpty(t *testing.T) {
	if _, err := NormalizeProjectKey(""); err == nil {
		t.Fatal("expected empty path error")
	}
}

func TestNormalizeProjectKeyNonexistent(t *testing.T) {
	// Path does not exist yet — still returns cleaned abs.
	p := filepath.Join(t.TempDir(), "does-not-exist-yet")
	key, err := NormalizeProjectKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(key) {
		t.Fatalf("key = %q", key)
	}
}

func TestNormalizeProjectKeySymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	key, err := NormalizeProjectKey(link)
	if err != nil {
		t.Fatal(err)
	}
	// EvalSymlinks should resolve to real path.
	if key != real && key != filepath.Clean(real) {
		// On some systems real may also be resolved; just ensure not the link path.
		if key == link {
			t.Fatalf("expected symlink resolved, still %q", key)
		}
	}
}

func TestGetProjectNilAndEmpty(t *testing.T) {
	var rt *RoutingTable
	if id, ok := rt.GetProject("/x"); ok || id != "" {
		t.Fatalf("nil GetProject = %q ok=%v", id, ok)
	}
	rt = &RoutingTable{} // Projects nil
	if id, ok := rt.GetProject(t.TempDir()); ok || id != "" {
		t.Fatalf("nil Projects GetProject = %q ok=%v", id, ok)
	}
	// Empty path normalizes to error → false
	if _, ok := rt.GetProject(""); ok {
		t.Fatal("empty path should not match")
	}
}

func TestSetRemoveProjectNilMapsAndFallback(t *testing.T) {
	rt := &RoutingTable{} // nil maps
	rt.SetProject(t.TempDir(), "den-z")
	if len(rt.Projects) != 1 {
		t.Fatalf("SetProject should init map: %v", rt.Projects)
	}
	// Remove with nil Projects
	rt2 := &RoutingTable{}
	if rt2.RemoveProject("/nope") {
		t.Fatal("RemoveProject on nil Projects")
	}
	// Force fallback path: empty project path fails Normalize → Clean fallback
	rt.SetProject("", "fallback-id")
	if rt.Projects[filepath.Clean("")] != "fallback-id" && rt.Projects["."] != "fallback-id" {
		// Clean("") is "."
		if id, ok := rt.Projects["."]; !ok || id != "fallback-id" {
			// Accept either Clean form
			found := false
			for k, v := range rt.Projects {
				if v == "fallback-id" {
					found = true
					_ = k
				}
			}
			if !found {
				t.Fatalf("fallback SetProject missing: %v", rt.Projects)
			}
		}
	}
	// Remove via same fallback path (empty input normalizes to a clean key).
	// Either removes a matched entry or reports not found — must not panic.
	_ = rt.RemoveProject("")
}

func TestSetNilVaultsMap(t *testing.T) {
	rt := &RoutingTable{}
	rt.Set("v1", "/p")
	if p, ok := rt.Get("v1"); !ok || p != "/p" {
		t.Fatalf("Set on nil Vaults: %q ok=%v", p, ok)
	}
}

func TestNormalizeBothNil(t *testing.T) {
	rt := &RoutingTable{}
	rt.normalize()
	if rt.Vaults == nil || rt.Projects == nil {
		t.Fatal("normalize must init both maps")
	}
}

func TestLoadFromInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yml")
	if err := os.WriteFile(path, []byte(":::not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrom(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestUpdateParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yml")
	if err := os.WriteFile(path, []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetOverridePath(path)
	defer SetOverridePath("")
	if err := Update(func(rt *RoutingTable) error { return nil }); err == nil {
		t.Fatal("expected parse error in Update")
	}
}

func TestSaveNilTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.yml")
	// nil rt still marshals (yaml null) but normalize is skipped — should not panic
	if err := SaveTo(nil, path); err != nil {
		// yaml.Marshal(nil) succeeds with "null\n"; write should work
		t.Fatalf("SaveTo nil: %v", err)
	}
}

func TestListProjectsEmpty(t *testing.T) {
	rt := EmptyTable()
	if m := rt.ListProjects(); len(m) != 0 {
		t.Fatalf("ListProjects empty = %v", m)
	}
	// nil Projects
	rt2 := &RoutingTable{}
	// ListProjects on nil map: range is fine
	if m := rt2.ListProjects(); len(m) != 0 {
		t.Fatalf("ListProjects nil = %v", m)
	}
}
