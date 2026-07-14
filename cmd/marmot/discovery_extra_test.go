package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/routes"
)

func TestProjectRootForVaultDirAfterRelocate(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	from := t.TempDir()
	to := t.TempDir()
	if _, err := den.Create("pr-den", den.CreateOptions{
		Lifetime:  den.LifetimeTask,
		Projects:  []string{from},
		NoPointer: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := den.RelocateProject(from, to); err != nil {
		t.Fatal(err)
	}

	vault := filepath.Join(den.Path("pr-den"), den.VaultDirName)
	got := projectRootForVaultDir(vault)
	want, _ := routes.NormalizeProjectKey(to)
	gotKey, _ := routes.NormalizeProjectKey(got)
	if gotKey != want {
		t.Fatalf("projectRoot = %q (key %q), want %q", got, gotKey, want)
	}
	// Must not return a non-existent path
	if st, err := os.Stat(got); err != nil || !st.IsDir() {
		t.Fatalf("projectRoot must exist: %v", err)
	}
}

func TestProjectRootForVaultDirSkipsMissingManifestPaths(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })

	// Create den with a real project, then delete the project dir and ensure
	// we don't return the missing path when routes also lack a live path.
	proj := t.TempDir()
	if _, err := den.Create("miss-pr", den.CreateOptions{
		Lifetime:  den.LifetimeTask,
		Projects:  []string{proj},
		NoPointer: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(proj); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(den.Path("miss-pr"), den.VaultDirName)
	got := projectRootForVaultDir(vault)
	if got != "" {
		// Empty is correct when no existing path remains.
		if st, err := os.Stat(got); err == nil && st.IsDir() {
			t.Fatalf("unexpected existing root %q", got)
		}
		t.Fatalf("projectRootForVaultDir returned missing path %q; want empty", got)
	}
}
