package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/routes"
)

func TestRoutePointerWriteAndRemove(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	proj := t.TempDir()
	out, code := captureRun([]string{"route", "pointer", "--write", proj, "den-x"})
	if code != 0 {
		t.Fatalf("pointer write: %d %s", code, out)
	}
	if !strings.Contains(out, "Wrote pointer") {
		t.Fatalf("output: %s", out)
	}
	id, err := den.ReadPointer(proj)
	if err != nil || id != "den-x" {
		t.Fatalf("pointer = %q err=%v", id, err)
	}

	// default write without --write flag
	proj2 := t.TempDir()
	if code := run([]string{"route", "pointer", proj2, "den-y"}); code != 0 {
		t.Fatal(code)
	}

	out, code = captureRun([]string{"route", "pointer", "--remove", proj})
	if code != 0 {
		t.Fatalf("remove: %d %s", code, out)
	}
	if !strings.Contains(out, "Removed pointer") {
		t.Fatalf("output: %s", out)
	}
	if id, _ := den.ReadPointer(proj); id != "" {
		t.Fatalf("still present: %q", id)
	}

	// usage errors
	if code := run([]string{"route", "pointer"}); code == 0 {
		t.Fatal("usage")
	}
	if code := run([]string{"route", "pointer", "--remove"}); code == 0 {
		t.Fatal("remove usage")
	}
	if code := run([]string{"route", "pointer", "--write", proj}); code == 0 {
		t.Fatal("write needs id")
	}
}

func TestRouteSetProjectJSON(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	from := t.TempDir()
	to := t.TempDir()
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.SetProject(from, "move-me")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{
		"route", "set-project", "--from", from, "--to", to, "--json",
	})
	if code != 0 {
		t.Fatalf("set-project: %d %s", code, out)
	}
	if !strings.Contains(out, "move-me") || !strings.Contains(out, `"schema"`) {
		t.Fatalf("envelope: %s", out)
	}
	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := rt.GetProject(to); !ok || id != "move-me" {
		t.Fatalf("to mapping: %q ok=%v", id, ok)
	}
	if _, ok := rt.GetProject(from); ok {
		t.Fatal("from should be removed")
	}

	// missing flags
	if code := run([]string{"route", "set-project"}); code == 0 {
		t.Fatal("usage")
	}
	// from not found
	if code := run([]string{"route", "set-project", "--from", t.TempDir(), "--to", t.TempDir()}); code == 0 {
		t.Fatal("missing project")
	}
}

func TestRouteAddProjectJSON(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	proj := t.TempDir()
	out, code := captureRun([]string{"route", "add", "--json", "--project", proj, "den-z"})
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	if !strings.Contains(out, "project_path") {
		t.Fatalf("%s", out)
	}

	out, code = captureRun([]string{"route", "rm", "--json", "--project", proj})
	if code != 0 {
		t.Fatalf("rm: %d %s", code, out)
	}
	if !strings.Contains(out, `"removed"`) {
		t.Fatalf("%s", out)
	}

	// list with projects
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.SetProject(proj, "den-z")
		rt.Set("v1", t.TempDir())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, code = captureRun([]string{"route"})
	if code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out, "den-z") {
		t.Fatalf("list projects: %s", out)
	}

	// add --project missing id
	if code := run([]string{"route", "add", "--project", proj}); code == 0 {
		t.Fatal("usage")
	}
}

func TestRoutePointerWriteError(t *testing.T) {
	// Force WritePointer error: project path empty after parse? use file as parent
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"route", "pointer", filepath.Join(blocker, "child"), "id"}); code == 0 {
		t.Fatal("expected write failure")
	}
}

func TestRouteSetProjectUpdatesDenManifestThenDestroyCleans(t *testing.T) {
	// End-to-end CLI: create den → set-project → status shows new path → destroy
	// leaves zero projects: entries for that den.
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	from := t.TempDir()
	to := t.TempDir()

	out, code := captureRun([]string{
		"den", "create", "arch-den",
		"--lifetime", "task",
		"--project", from,
		"--no-pointer",
		"--json",
	})
	if code != 0 {
		t.Fatalf("create: %d %s", code, out)
	}

	out, code = captureRun([]string{
		"route", "set-project", "--from", from, "--to", to, "--json",
	})
	if code != 0 {
		t.Fatalf("set-project: %d %s", code, out)
	}

	// den status must list the NEW path only
	out, code = captureRun([]string{"den", "status", "arch-den", "--json"})
	if code != 0 {
		t.Fatalf("status: %d %s", code, out)
	}
	if !strings.Contains(out, filepath.Base(to)) && !strings.Contains(out, to) {
		// paths may be /private-prefixed on macOS — check via Status API instead
		st, err := den.Status("arch-den")
		if err != nil {
			t.Fatal(err)
		}
		want, _ := routes.NormalizeProjectKey(to)
		ok := false
		for _, p := range st.Projects {
			got, _ := routes.NormalizeProjectKey(p)
			if got == want {
				ok = true
			}
			old, _ := routes.NormalizeProjectKey(from)
			if got == old {
				t.Fatalf("status still has old path: %#v", st.Projects)
			}
		}
		if !ok {
			t.Fatalf("status missing new path: %#v (want %s)", st.Projects, want)
		}
	}

	out, code = captureRun([]string{"den", "destroy", "arch-den", "--json"})
	if code != 0 {
		t.Fatalf("destroy: %d %s", code, out)
	}

	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := rt.GetProject(to); ok {
		t.Fatalf("orphan route after destroy: %s -> %s", to, id)
	}
	if id, ok := rt.GetProject(from); ok {
		t.Fatalf("old route remains: %s -> %s", from, id)
	}
	// no projects: entry for this den anywhere
	for path, id := range rt.ListProjects() {
		if id == "arch-den" {
			t.Fatalf("projects still maps %s -> arch-den", path)
		}
	}
}
