package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hermeticHome points HOME (and XDG/APPDATA equivalents) at a temp dir so
// global setup generators never touch the real user configs.
func hermeticHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	}
	return home
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

// assertGlobalServeArgs checks a generated server entry runs bare `serve`
// with no embedded --dir / vault path.
func assertGlobalServeArgs(t *testing.T, server map[string]any) {
	t.Helper()
	cmd, ok := server["command"].(string)
	if !ok || !filepath.IsAbs(cmd) {
		t.Errorf("expected absolute binary path command, got %v", server["command"])
	}
	args, ok := server["args"].([]any)
	if !ok {
		t.Fatalf("expected args array, got %T", server["args"])
	}
	if len(args) != 1 || args[0] != "serve" {
		t.Errorf("expected args [serve] (no --dir), got %v", args)
	}
}

// --- Claude ----------------------------------------------------------------

func TestGlobalSetupClaudeCreatesMinimal(t *testing.T) {
	home := hermeticHome(t)

	if err := generateClaudeGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readJSONFile(t, filepath.Join(home, ".claude.json"))
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected mcpServers key")
	}
	server, ok := servers["context-marmot"].(map[string]any)
	if !ok {
		t.Fatal("expected context-marmot server")
	}
	assertGlobalServeArgs(t, server)
}

func TestGlobalSetupClaudePreservesUnrelatedKeys(t *testing.T) {
	home := hermeticHome(t)
	path := filepath.Join(home, ".claude.json")
	existing := `{
  "numStartups": 42,
  "theme": "dark",
  "mcpServers": {
    "other-server": {"command": "/bin/other", "args": ["run"]}
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generateClaudeGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readJSONFile(t, path)
	if doc["numStartups"] != float64(42) || doc["theme"] != "dark" {
		t.Errorf("unrelated top-level keys not preserved: %v", doc)
	}
	servers := doc["mcpServers"].(map[string]any)
	if _, ok := servers["other-server"]; !ok {
		t.Error("sibling MCP server was not preserved")
	}
	if _, ok := servers["context-marmot"]; !ok {
		t.Error("context-marmot server was not added")
	}
}

func TestGlobalSetupClaudeIdempotent(t *testing.T) {
	home := hermeticHome(t)
	if err := generateClaudeGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatal(err)
	}
	if err := generateClaudeGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatal(err)
	}
	doc := readJSONFile(t, filepath.Join(home, ".claude.json"))
	servers := doc["mcpServers"].(map[string]any)
	if len(servers) != 1 {
		t.Errorf("expected exactly one server after double run, got %v", servers)
	}
}

func TestGlobalSetupClaudeRefusesUnparseable(t *testing.T) {
	home := hermeticHome(t)
	path := filepath.Join(home, ".claude.json")
	garbage := "{ not json at all"
	if err := os.WriteFile(path, []byte(garbage), 0o644); err != nil {
		t.Fatal(err)
	}

	err := generateClaudeGlobal("/usr/local/bin/marmot", false)
	if err == nil {
		t.Fatal("expected refusal error for unparseable file")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("expected clear refusal message, got: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != garbage {
		t.Error("unparseable file was modified")
	}
}

// --- Codex -----------------------------------------------------------------

func TestGlobalSetupCodexAppendsAndPreserves(t *testing.T) {
	home := hermeticHome(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(path, []byte("[model]\nname = \"gpt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generateCodexGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "[model]") {
		t.Error("existing content was not preserved")
	}
	if !strings.Contains(content, "[mcp_servers.context-marmot]") {
		t.Error("marmot section was not appended")
	}
	if !strings.Contains(content, `args = ["serve"]`) {
		t.Errorf("expected bare serve args, got:\n%s", content)
	}
	if strings.Contains(content, "--dir") {
		t.Error("global config must not embed --dir")
	}
}

func TestGlobalSetupCodexIdempotent(t *testing.T) {
	home := hermeticHome(t)
	if err := generateCodexGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatal(err)
	}
	if err := generateCodexGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "[mcp_servers.context-marmot]"); n != 1 {
		t.Errorf("expected 1 marmot section, got %d", n)
	}
}

// --- Cursor ----------------------------------------------------------------

func TestGlobalSetupCursor(t *testing.T) {
	home := hermeticHome(t)

	if err := generateCursorGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readJSONFile(t, filepath.Join(home, ".cursor", "mcp.json"))
	server, ok := doc["mcpServers"].(map[string]any)["context-marmot"].(map[string]any)
	if !ok {
		t.Fatal("expected context-marmot server")
	}
	assertGlobalServeArgs(t, server)
}

func TestGlobalSetupCursorPreservesSiblingsAndRefusesGarbage(t *testing.T) {
	home := hermeticHome(t)
	cursorDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cursorDir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers": {"keepme": {"command": "/bin/x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generateCursorGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatal(err)
	}
	doc := readJSONFile(t, path)
	servers := doc["mcpServers"].(map[string]any)
	if _, ok := servers["keepme"]; !ok {
		t.Error("sibling server was not preserved")
	}

	// Garbage file → refusal.
	if err := os.WriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generateCursorGlobal("/usr/local/bin/marmot", false); err == nil {
		t.Fatal("expected refusal for unparseable mcp.json")
	}
}

// --- VS Code ---------------------------------------------------------------

func TestGlobalSetupVSCodePreservesSettings(t *testing.T) {
	hermeticHome(t)
	path, err := vscodeUserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"editor.fontSize": 14, "mcp": {"servers": {"existing": {"command": "/bin/e"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generateVSCodeGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readJSONFile(t, path)
	if doc["editor.fontSize"] != float64(14) {
		t.Error("unrelated settings key was not preserved")
	}
	servers := doc["mcp"].(map[string]any)["servers"].(map[string]any)
	if _, ok := servers["existing"]; !ok {
		t.Error("existing MCP server was not preserved")
	}
	server, ok := servers["context-marmot"].(map[string]any)
	if !ok {
		t.Fatal("expected context-marmot server under mcp.servers")
	}
	if server["type"] != "stdio" {
		t.Errorf("expected type stdio, got %v", server["type"])
	}
	assertGlobalServeArgs(t, server)
}

func TestGlobalSetupVSCodeRefusesUnparseable(t *testing.T) {
	hermeticHome(t)
	path, err := vscodeUserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// JSONC-style comment: valid for VS Code, not strict JSON — must refuse.
	jsonc := "{\n  // my settings\n  \"editor.fontSize\": 14\n}"
	if err := os.WriteFile(path, []byte(jsonc), 0o644); err != nil {
		t.Fatal(err)
	}

	err = generateVSCodeGlobal("/usr/local/bin/marmot", false)
	if err == nil {
		t.Fatal("expected refusal for unparseable settings.json")
	}
	data, _ := os.ReadFile(path)
	if string(data) != jsonc {
		t.Error("settings.json was modified despite refusal")
	}
}

func TestGlobalSetupVSCodeCreatesWhenMissing(t *testing.T) {
	hermeticHome(t)
	if err := generateVSCodeGlobal("/usr/local/bin/marmot", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	path, _ := vscodeUserSettingsPath()
	doc := readJSONFile(t, path)
	if _, ok := doc["mcp"].(map[string]any)["servers"].(map[string]any)["context-marmot"]; !ok {
		t.Error("expected context-marmot under mcp.servers in fresh settings.json")
	}
}

// --- dry-run ---------------------------------------------------------------

func TestGlobalSetupDryRunWritesNothing(t *testing.T) {
	home := hermeticHome(t)

	for name, gen := range map[string]func(string, bool) error{
		"claude": generateClaudeGlobal,
		"codex":  generateCodexGlobal,
		"cursor": generateCursorGlobal,
		"vscode": generateVSCodeGlobal,
	} {
		if err := gen("/usr/local/bin/marmot", true); err != nil {
			t.Fatalf("%s dry-run: %v", name, err)
		}
	}

	vscodePath, _ := vscodeUserSettingsPath()
	for _, p := range []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(home, ".cursor", "mcp.json"),
		vscodePath,
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("dry-run created %s", p)
		}
	}
}

// --- CLI wiring ------------------------------------------------------------

func TestCmdSetupGlobalFlag(t *testing.T) {
	home := hermeticHome(t)

	if code := run([]string{"setup", "--global", "--claude"}); code != 0 {
		t.Fatalf("setup --global exit code = %d, want 0", code)
	}
	doc := readJSONFile(t, filepath.Join(home, ".claude.json"))
	if _, ok := doc["mcpServers"].(map[string]any)["context-marmot"]; !ok {
		t.Error("expected context-marmot in ~/.claude.json")
	}
}

// TestCmdSetupGlobalBareDefaultsToClaude: bare `setup --global` in a clean
// HOME must not probe PATH for installed harnesses — it predictably falls
// back to Claude Code only, writes ~/.claude.json and nothing else, and
// prints a line naming the selected targets.
func TestCmdSetupGlobalBareDefaultsToClaude(t *testing.T) {
	home := hermeticHome(t)

	out, code := captureRun([]string{"setup", "--global"})
	if code != 0 {
		t.Fatalf("bare setup --global exit code = %d, want 0\noutput:\n%s", code, out)
	}

	if !strings.Contains(out, "Global setup targets") || !strings.Contains(out, "Claude Code") {
		t.Errorf("expected selection line naming Claude Code, got:\n%s", out)
	}

	doc := readJSONFile(t, filepath.Join(home, ".claude.json"))
	if _, ok := doc["mcpServers"].(map[string]any)["context-marmot"]; !ok {
		t.Error("expected context-marmot in ~/.claude.json")
	}

	// No other harness config may appear in a clean HOME.
	vscodePath, _ := vscodeUserSettingsPath()
	for _, p := range []string{
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(home, ".cursor", "mcp.json"),
		vscodePath,
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("bare setup --global in clean HOME wrote %s", p)
		}
	}
}

// TestCmdSetupGlobalAutoDetectsFromHome: with a codex home footprint present,
// bare `setup --global` selects exactly the detected harnesses.
func TestCmdSetupGlobalAutoDetectsFromHome(t *testing.T) {
	home := hermeticHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{"setup", "--global"})
	if code != 0 {
		t.Fatalf("setup --global exit code = %d, want 0\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "auto-detected") || !strings.Contains(out, "Codex") {
		t.Errorf("expected auto-detected selection line naming Codex, got:\n%s", out)
	}
	if strings.Contains(out, "Claude Code") {
		t.Errorf("Claude Code selected despite no home footprint:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Errorf("expected codex config written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Error("claude config written despite not detected")
	}
}

func TestCmdSetupGlobalDryRun(t *testing.T) {
	home := hermeticHome(t)

	if code := run([]string{"setup", "--global", "--dry-run", "--claude"}); code != 0 {
		t.Fatalf("setup --global --dry-run exit code = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Error("dry-run wrote ~/.claude.json")
	}
}

func TestCmdSetupDryRunRequiresGlobal(t *testing.T) {
	hermeticHome(t)
	if code := run([]string{"setup", "--dry-run"}); code != 1 {
		t.Fatalf("setup --dry-run without --global exit code = %d, want 1", code)
	}
}

func TestCmdSetupGlobalUnparseableExitsNonzero(t *testing.T) {
	home := hermeticHome(t)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"setup", "--global", "--claude"}); code != 1 {
		t.Fatalf("setup --global with unparseable target exit code = %d, want 1", code)
	}
}
