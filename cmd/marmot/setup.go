package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// setupTarget represents an IDE/tool that supports MCP configuration.
type setupTarget struct {
	Name     string
	Flag     string // CLI flag name
	Detect   func(projectRoot string) bool
	Generate func(projectRoot, binaryPath, vaultDir string) error
}

var targets = []setupTarget{
	{
		Name:     "Claude Code",
		Flag:     "claude",
		Detect:   detectClaude,
		Generate: generateClaude,
	},
	{
		Name:     "Codex",
		Flag:     "codex",
		Detect:   detectCodex,
		Generate: generateCodex,
	},
	{
		Name:     "VS Code",
		Flag:     "vscode",
		Detect:   detectVSCode,
		Generate: generateVSCode,
	},
	{
		Name:     "Cursor",
		Flag:     "cursor",
		Detect:   detectCursor,
		Generate: generateCursor,
	},
}

func cmdSetup(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")

	// Add a bool flag per target.
	flagPtrs := make(map[string]*bool, len(targets))
	for _, t := range targets {
		flagPtrs[t.Flag] = fs.Bool(t.Flag, false, fmt.Sprintf("generate config for %s", t.Name))
	}

	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}

	// Collect explicitly requested targets.
	var requested []setupTarget
	for _, t := range targets {
		if *flagPtrs[t.Flag] {
			requested = append(requested, t)
		}
	}

	if err := runSetup(*dir, requested); err != nil {
		fmt.Fprintf(os.Stderr, "setup: %v\n", err)
		return 1
	}
	return 0
}

// runSetup generates MCP configs. If requested is empty, auto-detects tools.
func runSetup(vaultDir string, requested []setupTarget) error {
	if _, err := os.Stat(vaultDir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", vaultDir)
	}

	// Resolve paths.
	absVault, err := filepath.Abs(vaultDir)
	if err != nil {
		return fmt.Errorf("resolve vault path: %w", err)
	}
	projectRoot := filepath.Dir(absVault) // .marmot sits inside the project root

	binaryPath, err := findBinary()
	if err != nil {
		return err
	}

	toRun := requested
	if len(toRun) == 0 {
		// Auto-detect.
		for _, t := range targets {
			if t.Detect(projectRoot) {
				toRun = append(toRun, t)
			}
		}
		if len(toRun) == 0 {
			// No tools detected — generate Claude Code config as sensible default.
			fmt.Println("No supported tools detected; generating Claude Code config as default.")
			toRun = []setupTarget{targets[0]}
		}
	}

	for _, t := range toRun {
		if err := t.Generate(projectRoot, binaryPath, vaultDir); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %s: %v\n", t.Name, err)
			continue
		}
		fmt.Printf("  %s config written.\n", t.Name)
	}
	return nil
}

// findBinary returns the absolute path to the marmot binary, preferring
// the system PATH, then falling back to the binary adjacent to the running
// executable.
func findBinary() (string, error) {
	// Check PATH first.
	if p, err := exec.LookPath("marmot"); err == nil {
		return filepath.Abs(p)
	}
	// Fall back to the directory of the current executable.
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate marmot binary: %w", err)
	}
	return filepath.Abs(exe)
}

// ---------------------------------------------------------------------------
// Detection helpers
// ---------------------------------------------------------------------------

func detectClaude(projectRoot string) bool {
	// Claude Code: .claude/ dir or existing .mcp.json
	for _, p := range []string{".claude", ".mcp.json"} {
		if _, err := os.Stat(filepath.Join(projectRoot, p)); err == nil {
			return true
		}
	}
	return false
}

func detectCodex(projectRoot string) bool {
	// Codex: .codex/ dir or codex on PATH
	if _, err := os.Stat(filepath.Join(projectRoot, ".codex")); err == nil {
		return true
	}
	_, err := exec.LookPath("codex")
	return err == nil
}

func detectVSCode(projectRoot string) bool {
	_, err := os.Stat(filepath.Join(projectRoot, ".vscode"))
	return err == nil
}

func detectCursor(projectRoot string) bool {
	_, err := os.Stat(filepath.Join(projectRoot, ".cursor"))
	return err == nil
}

// ---------------------------------------------------------------------------
// Config generators
// ---------------------------------------------------------------------------

// generateClaude writes .mcp.json at the project root.
func generateClaude(projectRoot, binaryPath, vaultDir string) error {
	relVault := relOrAbs(projectRoot, vaultDir)

	cfg := map[string]any{
		"mcpServers": map[string]any{
			"context-marmot": map[string]any{
				"command": binaryPath,
				"args":    []string{"serve", "--dir", relVault},
			},
		},
	}
	return writeJSON(filepath.Join(projectRoot, ".mcp.json"), cfg)
}

// generateCodex writes .codex/config.toml at the project root.
func generateCodex(projectRoot, binaryPath, vaultDir string) error {
	relVault := relOrAbs(projectRoot, vaultDir)

	dir := filepath.Join(projectRoot, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	configPath := filepath.Join(dir, "config.toml")

	// Read existing content to preserve other settings.
	existing := ""
	if data, err := os.ReadFile(configPath); err == nil {
		existing = string(data)
	}

	// Check if context-marmot section already exists.
	if strings.Contains(existing, "[mcp_servers.context-marmot]") {
		return nil // already configured
	}

	section := fmt.Sprintf(`
[mcp_servers.context-marmot]
enabled = true
command = %q
args = ["serve", "--dir", %q]
`, binaryPath, relVault)

	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(section)
	return err
}

// generateVSCode writes .vscode/mcp.json at the project root.
func generateVSCode(projectRoot, binaryPath, vaultDir string) error {
	relVault := relOrAbs(projectRoot, vaultDir)

	dir := filepath.Join(projectRoot, ".vscode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	cfg := map[string]any{
		"servers": map[string]any{
			"context-marmot": map[string]any{
				"type":    "stdio",
				"command": binaryPath,
				"args":    []string{"serve", "--dir", relVault},
			},
		},
	}
	return writeJSON(filepath.Join(dir, "mcp.json"), cfg)
}

// generateCursor writes .cursor/mcp.json at the project root.
func generateCursor(projectRoot, binaryPath, vaultDir string) error {
	relVault := relOrAbs(projectRoot, vaultDir)

	dir := filepath.Join(projectRoot, ".cursor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	cfg := map[string]any{
		"mcpServers": map[string]any{
			"context-marmot": map[string]any{
				"command": binaryPath,
				"args":    []string{"serve", "--dir", relVault},
			},
		},
	}
	return writeJSON(filepath.Join(dir, "mcp.json"), cfg)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// relOrAbs returns vaultDir as relative to projectRoot if possible,
// otherwise returns the absolute path.
func relOrAbs(projectRoot, vaultDir string) string {
	abs, err := filepath.Abs(vaultDir)
	if err != nil {
		return vaultDir
	}
	rel, err := filepath.Rel(projectRoot, abs)
	if err != nil {
		return abs
	}
	return rel
}
