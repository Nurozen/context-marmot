package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/routes"
)

func TestResolveDenID_WithVault(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	res, err := den.Create("serve-den", den.CreateOptions{
		Lifetime: den.LifetimeTask,
		Projects: []string{t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveDenID("serve-den")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(res.DenPath, den.VaultDirName)
	if got != want {
		t.Fatalf("resolveDenID = %q, want vault %q", got, want)
	}
}

func TestResolveDenID_NoVault(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	res, err := den.Create("links", den.CreateOptions{
		Lifetime: den.LifetimeTask,
		NoVault:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveDenID("links")
	if err != nil {
		t.Fatal(err)
	}
	if got != res.DenPath {
		t.Fatalf("resolveDenID links-only = %q, want den root %q", got, res.DenPath)
	}
}

func TestResolveDenID_Missing(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	if _, err := resolveDenID("nope"); err == nil {
		t.Fatal("expected error for missing den")
	}
}

func TestCmdServeDenFlagResolves(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	if _, err := den.Create("cli-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	// serve will try to start MCP — we only verify flag parsing + resolve via mutual exclusion path
	// and missing den error path (no hang on real serve).
	if code := run([]string{"serve", "--den", "missing-xyz"}); code == 0 {
		t.Fatal("serve --den missing should fail")
	}
	// Mutual exclusion
	if code := run([]string{"serve", "--den", "cli-den", "--dir", root}); code == 0 {
		t.Fatal("serve --den and --dir together should fail")
	}
	// Ensure vault path exists for resolveDenID
	p, err := resolveDenID("cli-den")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestResolveVaultDir_ReverseRouteBeatsLegacy(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	// Project tree with an ancestor .marmot that legacy walk-up would find.
	proj := filepath.Join(root, "workspace", "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "workspace", ".marmot")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := den.Create("route-den", den.CreateOptions{
		Lifetime:  den.LifetimeTask,
		Projects:  []string{proj},
		NoPointer: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantVault := filepath.Join(res.DenPath, den.VaultDirName)

	// Run resolution from project cwd.
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}

	got := resolveVaultDir("")
	if got != wantVault {
		t.Fatalf("resolveVaultDir = %q, want den vault %q (not legacy %q)", got, wantVault, legacy)
	}
	// Explicit --dir still wins.
	if got := resolveVaultDir("/explicit"); got != "/explicit" {
		t.Fatalf("explicit dir = %q", got)
	}
}

func TestResolveVaultDir_PointerWhenNoRoute(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	proj := t.TempDir()
	res, err := den.Create("ptr-den", den.CreateOptions{
		Lifetime: den.LifetimeTask,
		Projects: []string{proj},
		// with pointer
	})
	if err != nil {
		t.Fatal(err)
	}
	// Drop reverse route so only pointer remains.
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.RemoveProject(proj)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	got := resolveVaultDir("")
	want := filepath.Join(res.DenPath, den.VaultDirName)
	if got != want {
		t.Fatalf("pointer resolve = %q, want %q", got, want)
	}
}
