package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesDirectoryStructure(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, ".marmot")

	err := runInit(vault)
	if err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	// Check top-level vault dir exists.
	assertDirExists(t, vault)

	// Check subdirectories.
	assertDirExists(t, filepath.Join(vault, ".marmot-data"))
	assertDirExists(t, filepath.Join(vault, ".obsidian"))

	// Check files.
	assertFileExists(t, filepath.Join(vault, "_config.md"))
	assertFileExists(t, filepath.Join(vault, ".obsidian", "graph.json"))

	// Verify _config.md has content.
	data, err := os.ReadFile(filepath.Join(vault, "_config.md"))
	if err != nil {
		t.Fatalf("read _config.md: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("_config.md is empty")
	}

	// Verify graph.json content.
	data, err = os.ReadFile(filepath.Join(vault, ".obsidian", "graph.json"))
	if err != nil {
		t.Fatalf("read graph.json: %v", err)
	}
	if string(data) != "{}\n" {
		t.Fatalf("graph.json unexpected content: %q", string(data))
	}
}

func TestInitFailsOnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, ".marmot")

	// First init should succeed.
	if err := runInit(vault); err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	// Second init should fail.
	err := runInit(vault)
	if err == nil {
		t.Fatal("expected error on second init, got nil")
	}
}

func TestVerifyOnEmptyVault(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, ".marmot")

	// Initialize the vault.
	if err := runInit(vault); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// Verify should succeed with no issues (no nodes to check).
	err := runVerifyEnhanced(vault, "", false, false)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestRunNoArgs(t *testing.T) {
	code := run(nil)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	code := run([]string{"foobar"})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

func TestQueryMissingFlag(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, ".marmot")
	if err := runInit(vault); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	code := run([]string{"query", "--dir", vault})
	if code != 1 {
		t.Fatalf("expected exit code 1 for missing --query, got %d", code)
	}
}

func TestQueryOnEmptyVault(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, ".marmot")
	if err := runInit(vault); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// Query on empty vault should succeed (just return empty result).
	err := runQuery(vault, "test query", 2, 4096)
	if err != nil {
		t.Fatalf("query on empty vault failed: %v", err)
	}
}

func TestServeRequiresVault(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "nonexistent")

	err := runServe(vault)
	if err == nil {
		t.Fatal("expected error for nonexistent vault, got nil")
	}
}

func TestVerifyRequiresVault(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "nonexistent")

	err := runVerifyEnhanced(vault, "", false, false)
	if err == nil {
		t.Fatal("expected error for nonexistent vault, got nil")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertDirExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("directory does not exist: %s", path)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory, got file: %s", path)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file does not exist: %s", path)
	}
	if info.IsDir() {
		t.Fatalf("expected file, got directory: %s", path)
	}
}
