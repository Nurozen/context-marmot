package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupClaudeCode(t *testing.T) {
	projectRoot := t.TempDir()
	vaultDir := filepath.Join(projectRoot, ".marmot")
	setupVault(t, vaultDir)

	err := runSetup(vaultDir, []setupTarget{targets[0]}) // claude
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(projectRoot, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}

	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected mcpServers key")
	}
	server, ok := servers["context-marmot"].(map[string]any)
	if !ok {
		t.Fatal("expected context-marmot server")
	}
	args, ok := server["args"].([]any)
	if !ok || len(args) < 1 {
		t.Fatal("expected args array")
	}
	if args[0] != "serve" {
		t.Errorf("expected first arg 'serve', got %v", args[0])
	}
}

func TestSetupCodex(t *testing.T) {
	projectRoot := t.TempDir()
	vaultDir := filepath.Join(projectRoot, ".marmot")
	setupVault(t, vaultDir)

	err := runSetup(vaultDir, []setupTarget{targets[1]}) // codex
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(projectRoot, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "[mcp_servers.context-marmot]") {
		t.Error("expected [mcp_servers.context-marmot] section")
	}
	if !strings.Contains(content, "enabled = true") {
		t.Error("expected enabled = true")
	}
	if !strings.Contains(content, `"serve"`) {
		t.Error("expected serve in args")
	}
}

func TestSetupCodexPreservesExisting(t *testing.T) {
	projectRoot := t.TempDir()
	vaultDir := filepath.Join(projectRoot, ".marmot")
	setupVault(t, vaultDir)

	// Pre-create .codex/config.toml with other content.
	codexDir := filepath.Join(projectRoot, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "[some_other_section]\nfoo = \"bar\"\n"
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runSetup(vaultDir, []setupTarget{targets[1]}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "[some_other_section]") {
		t.Error("existing content was not preserved")
	}
	if !strings.Contains(content, "[mcp_servers.context-marmot]") {
		t.Error("marmot section was not appended")
	}
}

func TestSetupCodexSkipsDuplicate(t *testing.T) {
	projectRoot := t.TempDir()
	vaultDir := filepath.Join(projectRoot, ".marmot")
	setupVault(t, vaultDir)

	// Run twice.
	if err := runSetup(vaultDir, []setupTarget{targets[1]}); err != nil {
		t.Fatal(err)
	}
	if err := runSetup(vaultDir, []setupTarget{targets[1]}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(projectRoot, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(data), "[mcp_servers.context-marmot]")
	if count != 1 {
		t.Errorf("expected 1 marmot section, got %d", count)
	}
}

func TestSetupVSCode(t *testing.T) {
	projectRoot := t.TempDir()
	vaultDir := filepath.Join(projectRoot, ".marmot")
	setupVault(t, vaultDir)

	err := runSetup(vaultDir, []setupTarget{targets[2]}) // vscode
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(projectRoot, ".vscode", "mcp.json"))
	if err != nil {
		t.Fatalf("read .vscode/mcp.json: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	servers, ok := cfg["servers"].(map[string]any)
	if !ok {
		t.Fatal("expected 'servers' key (VS Code format)")
	}
	if _, ok := servers["context-marmot"]; !ok {
		t.Fatal("expected context-marmot server")
	}
}

func TestSetupCursor(t *testing.T) {
	projectRoot := t.TempDir()
	vaultDir := filepath.Join(projectRoot, ".marmot")
	setupVault(t, vaultDir)

	err := runSetup(vaultDir, []setupTarget{targets[3]}) // cursor
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(projectRoot, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatalf("read .cursor/mcp.json: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected 'mcpServers' key (Cursor format)")
	}
	if _, ok := servers["context-marmot"]; !ok {
		t.Fatal("expected context-marmot server")
	}
}

func TestSetupAutoDetectClaude(t *testing.T) {
	projectRoot := t.TempDir()
	vaultDir := filepath.Join(projectRoot, ".marmot")
	setupVault(t, vaultDir)

	// Create .claude dir to trigger detection.
	if err := os.MkdirAll(filepath.Join(projectRoot, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	// nil requested = auto-detect.
	if err := runSetup(vaultDir, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(projectRoot, ".mcp.json")); err != nil {
		t.Error("expected .mcp.json to be generated via auto-detect")
	}
}

func TestSetupNonexistentVault(t *testing.T) {
	err := runSetup("/nonexistent/vault", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent vault")
	}
}

func TestRelOrAbs(t *testing.T) {
	root := "/project"
	vault := "/project/.marmot"
	got := relOrAbs(root, vault)
	if got != ".marmot" {
		t.Errorf("expected .marmot, got %q", got)
	}
}

// setupVault creates a minimal vault for testing.
func setupVault(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\nembedding_model: \"\"\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
