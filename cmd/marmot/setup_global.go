package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Global (user-scope) setup: `marmot setup --global` writes per-harness MCP
// configs into the user's home config files with command `marmot serve` and
// NO embedded vault path. Vault resolution happens at serve time via the
// reverse-route table / `.marmot-vault` pointer (see docs/dens.md), so one
// global entry covers every registered project on the machine.

// globalTarget mirrors setupTarget for user-scope config generation.
type globalTarget struct {
	Name     string
	Flag     string // shares CLI flag names with the project-local targets
	Detect   func() bool
	Generate func(binaryPath string, dryRun bool) error
}

var globalTargets = []globalTarget{
	{
		Name:     "Claude Code",
		Flag:     "claude",
		Detect:   detectClaudeGlobal,
		Generate: generateClaudeGlobal,
	},
	{
		Name:     "Codex",
		Flag:     "codex",
		Detect:   detectCodexGlobal,
		Generate: generateCodexGlobal,
	},
	{
		Name:     "VS Code",
		Flag:     "vscode",
		Detect:   detectVSCodeGlobal,
		Generate: generateVSCodeGlobal,
	},
	{
		Name:     "Cursor",
		Flag:     "cursor",
		Detect:   detectCursorGlobal,
		Generate: generateCursorGlobal,
	},
}

// runGlobalSetup writes user-scope MCP configs. If requestedFlags is empty,
// installed harnesses are auto-detected from their home-scope config
// footprints; if none are detected, Claude Code is the predictable default
// (mirrors the project-local setup fallback).
func runGlobalSetup(requestedFlags map[string]bool, dryRun bool) error {
	binaryPath, err := findBinary()
	if err != nil {
		return err
	}

	var toRun []globalTarget
	selection := "requested"
	for _, t := range globalTargets {
		if requestedFlags[t.Flag] {
			toRun = append(toRun, t)
		}
	}
	if len(toRun) == 0 {
		selection = "auto-detected"
		for _, t := range globalTargets {
			if t.Detect() {
				toRun = append(toRun, t)
			}
		}
		if len(toRun) == 0 {
			selection = "default; no supported tools detected"
			toRun = []globalTarget{globalTargets[0]}
		}
	}

	names := make([]string, len(toRun))
	for i, t := range toRun {
		names[i] = t.Name
	}
	fmt.Printf("Global setup targets (%s): %s\n", selection, strings.Join(names, ", "))

	var firstErr error
	for _, t := range toRun {
		if err := t.Generate(binaryPath, dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %s: %v\n", t.Name, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", t.Name, err)
			}
			continue
		}
		if dryRun {
			fmt.Printf("  %s config previewed (dry-run, nothing written).\n", t.Name)
		} else {
			fmt.Printf("  %s global config written.\n", t.Name)
		}
	}
	return firstErr
}

// globalServerEntry is the user-scope MCP server payload: bare `marmot serve`,
// no --dir. Serve resolves the vault per-project at launch time.
func globalServerEntry(binaryPath string, withType bool) map[string]any {
	entry := map[string]any{
		"command": binaryPath,
		"args":    []any{"serve"},
	}
	if withType {
		entry["type"] = "stdio"
	}
	return entry
}

// ---------------------------------------------------------------------------
// Home-relative paths
// ---------------------------------------------------------------------------

func userHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return home, nil
}

func claudeGlobalConfigPath() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

func codexGlobalConfigPath() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

func cursorGlobalConfigPath() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor", "mcp.json"), nil
}

// vscodeUserSettingsPath returns the VS Code user settings.json location
// per-OS: macOS ~/Library/Application Support/Code/User/settings.json,
// Linux $XDG_CONFIG_HOME/Code/User/settings.json (default ~/.config),
// Windows %APPDATA%\Code\User\settings.json.
func vscodeUserSettingsPath() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Code", "User", "settings.json"), nil
	default:
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(home, ".config")
		}
		return filepath.Join(cfg, "Code", "User", "settings.json"), nil
	}
}

// ---------------------------------------------------------------------------
// Detection (home-scope config footprint only)
//
// Deliberately no PATH probing (exec.LookPath): global setup must be
// predictable from the user's home directory alone, so a clean HOME always
// resolves to the Claude Code default instead of whichever binaries happen
// to be installed machine-wide.
// ---------------------------------------------------------------------------

func detectClaudeGlobal() bool {
	home, err := userHomeDir()
	if err != nil {
		return false
	}
	for _, p := range []string{".claude", ".claude.json"} {
		if _, err := os.Stat(filepath.Join(home, p)); err == nil {
			return true
		}
	}
	return false
}

func detectCodexGlobal() bool {
	home, err := userHomeDir()
	if err != nil {
		return false
	}
	_, statErr := os.Stat(filepath.Join(home, ".codex"))
	return statErr == nil
}

func detectVSCodeGlobal() bool {
	p, err := vscodeUserSettingsPath()
	if err != nil {
		return false
	}
	_, statErr := os.Stat(filepath.Dir(p))
	return statErr == nil
}

func detectCursorGlobal() bool {
	home, err := userHomeDir()
	if err != nil {
		return false
	}
	_, statErr := os.Stat(filepath.Join(home, ".cursor"))
	return statErr == nil
}

// ---------------------------------------------------------------------------
// Generators
// ---------------------------------------------------------------------------

// generateClaudeGlobal upserts mcpServers.context-marmot in ~/.claude.json
// (Claude Code's user-scope MCP store). Unrelated keys are preserved; an
// existing-but-unparseable file is refused rather than clobbered because
// ~/.claude.json also carries unrelated Claude Code state.
func generateClaudeGlobal(binaryPath string, dryRun bool) error {
	path, err := claudeGlobalConfigPath()
	if err != nil {
		return err
	}
	return upsertJSONServer(path, []string{"mcpServers"}, globalServerEntry(binaryPath, false), dryRun)
}

// generateCodexGlobal appends [mcp_servers.context-marmot] to
// ~/.codex/config.toml, keeping the project generator's idempotence check.
func generateCodexGlobal(binaryPath string, dryRun bool) error {
	path, err := codexGlobalConfigPath()
	if err != nil {
		return err
	}

	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	}
	if strings.Contains(existing, "[mcp_servers.context-marmot]") {
		return nil // already configured
	}

	section := fmt.Sprintf(`
[mcp_servers.context-marmot]
enabled = true
command = %q
args = ["serve"]
`, binaryPath)

	if dryRun {
		fmt.Printf("dry-run: append %s\n%s", path, section)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(section)
	return err
}

// generateVSCodeGlobal writes the "mcp.servers" key into the VS Code user
// settings.json non-destructively. An unparseable settings.json (including
// JSONC with comments) is refused with a clear error rather than clobbered.
func generateVSCodeGlobal(binaryPath string, dryRun bool) error {
	path, err := vscodeUserSettingsPath()
	if err != nil {
		return err
	}
	return upsertJSONServer(path, []string{"mcp", "servers"}, globalServerEntry(binaryPath, true), dryRun)
}

// generateCursorGlobal upserts mcpServers.context-marmot in ~/.cursor/mcp.json.
func generateCursorGlobal(binaryPath string, dryRun bool) error {
	path, err := cursorGlobalConfigPath()
	if err != nil {
		return err
	}
	return upsertJSONServer(path, []string{"mcpServers"}, globalServerEntry(binaryPath, false), dryRun)
}

// ---------------------------------------------------------------------------
// JSON upsert helper
// ---------------------------------------------------------------------------

// upsertJSONServer loads the JSON document at path (creating a minimal one if
// the file does not exist), walks/creates the object chain named by keys, sets
// its "context-marmot" entry to server, and writes the document back
// atomically. All unrelated keys are preserved. If the file exists but does
// not parse as JSON, it refuses with an error instead of clobbering.
func upsertJSONServer(path string, keys []string, server map[string]any, dryRun bool) error {
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &doc); err != nil {
				return fmt.Errorf("%s exists but is not valid JSON (%v); refusing to overwrite — fix the file or add the context-marmot MCP server manually", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Walk/create nested objects for keys, preserving siblings.
	cur := doc
	for _, k := range keys {
		next, ok := cur[k]
		if !ok || next == nil {
			m := map[string]any{}
			cur[k] = m
			cur = m
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: key %q exists but is not an object; refusing to overwrite", path, k)
		}
		cur = m
	}
	cur["context-marmot"] = server

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if dryRun {
		fmt.Printf("dry-run: write %s\n%s", path, data)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWriteFile(path, data)
}

// atomicWriteFile writes data to path via a same-directory temp file + rename
// so user-scope config files are never left half-written.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
