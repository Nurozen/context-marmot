package home

import (
	"path/filepath"
	"testing"
)

func TestDirDefaultAndEnv(t *testing.T) {
	SetOverride("")
	t.Cleanup(func() { SetOverride("") })

	tmp := t.TempDir()
	t.Setenv("MARMOT_HOME", tmp)
	if got := Dir(); got != tmp {
		t.Fatalf("Dir() = %q, want %q", got, tmp)
	}
	if got := DensDir(); got != filepath.Join(tmp, "dens") {
		t.Fatalf("DensDir() = %q", got)
	}
	if got := RoutesPath(); got != filepath.Join(tmp, "routes.yml") {
		t.Fatalf("RoutesPath() = %q", got)
	}
}

func TestSetOverrideWinsOverEnv(t *testing.T) {
	override := filepath.Join(t.TempDir(), "override")
	t.Setenv("MARMOT_HOME", t.TempDir())
	SetOverride(override)
	t.Cleanup(func() { SetOverride("") })
	if got := Dir(); got != override {
		t.Fatalf("Dir() = %q, want override %q", got, override)
	}
}

func TestWarrenCacheDir(t *testing.T) {
	tmp := t.TempDir()
	SetOverride(tmp)
	t.Cleanup(func() { SetOverride("") })
	got := WarrenCacheDir()
	want := filepath.Join(tmp, "warren-cache")
	if got != want {
		t.Fatalf("WarrenCacheDir = %q, want %q", got, want)
	}
}

func TestDirFallsBackToUserHome(t *testing.T) {
	SetOverride("")
	t.Cleanup(func() { SetOverride("") })
	t.Setenv("MARMOT_HOME", "")
	// Should resolve to ~/.marmot without error (user home exists on CI/dev).
	got := Dir()
	if got == "" {
		t.Fatal("Dir() empty when user home available")
	}
	if filepath.Base(got) != ".marmot" {
		t.Fatalf("Dir() = %q, want */.marmot", got)
	}
}
