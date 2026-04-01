// Package main provides the ContextMarmot CLI.
//
// Commands:
//
//	marmot init    [--dir .marmot]              Create a new vault
//	marmot index   [--dir .marmot]              Index all nodes into embedding store
//	marmot query   --query "..." [flags]        Query the knowledge graph
//	marmot serve   [--dir .marmot]              Start MCP server on stdio
//	marmot verify  [--dir .marmot]              Run integrity checks
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const defaultDir = ".marmot"

// discoverVault walks up from the current directory looking for a .marmot/
// directory. Returns the path if found, or defaultDir if not.
func discoverVault() string {
	dir, err := os.Getwd()
	if err != nil {
		return defaultDir
	}
	for {
		candidate := filepath.Join(dir, ".marmot")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return defaultDir
		}
		dir = parent
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the testable entry point. It returns an exit code.
func run(args []string) int {
	if len(args) < 1 {
		usage()
		return 1
	}

	command := args[0]
	cmdArgs := args[1:]

	switch command {
	case "init":
		return cmdInit(cmdArgs)
	case "index":
		return cmdIndex(cmdArgs)
	case "query":
		return cmdQuery(cmdArgs)
	case "serve":
		return cmdServe(cmdArgs)
	case "verify":
		return cmdVerify(cmdArgs)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", command)
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: marmot <command> [flags]")
	fmt.Fprintln(os.Stderr, "commands: init, index, query, serve, verify")
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = defaultDir // init always creates in cwd, no walk-up
	}

	if err := runInit(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		return 1
	}
	return 0
}

func runInit(dir string) error {
	// Fail if the directory already exists.
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("directory %q already exists; vault already initialised", dir)
	}

	// Create directory tree.
	dirs := []string{
		dir,
		filepath.Join(dir, ".marmot-data"),
		filepath.Join(dir, ".obsidian"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	// Write _config.md.
	configContent := `---
version: "1"
namespace: default
embedding_provider: mock
embedding_model: ""
---
# ContextMarmot Vault

This is the root configuration for a ContextMarmot vault.
# To use OpenAI embeddings, set embedding_provider to "openai"
# and set OPENAI_API_KEY in your environment.
`
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(configContent), 0o644); err != nil {
		return fmt.Errorf("write _config.md: %w", err)
	}

	// Write .obsidian/graph.json.
	if err := os.WriteFile(filepath.Join(dir, ".obsidian", "graph.json"), []byte("{}\n"), 0o644); err != nil {
		return fmt.Errorf("write graph.json: %w", err)
	}

	fmt.Printf("Initialised ContextMarmot vault at %s\n", dir)
	return nil
}

// ---------------------------------------------------------------------------
// index
// ---------------------------------------------------------------------------

func cmdIndex(args []string) int {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	force := fs.Bool("force", false, "clear and rebuild all embeddings (use after changing embedding provider)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}

	if err := runIndex(*dir, *force); err != nil {
		fmt.Fprintf(os.Stderr, "index: %v\n", err)
		return 1
	}
	return 0
}

func runIndex(dir string, force bool) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", dir)
	}

	return runIndexPipeline(dir, force)
}

// ---------------------------------------------------------------------------
// query
// ---------------------------------------------------------------------------

func cmdQuery(args []string) int {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	query := fs.String("query", "", "search query (required)")
	depth := fs.Int("depth", 2, "traversal depth")
	budget := fs.Int("budget", 4096, "token budget for compaction")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}

	if *query == "" {
		fmt.Fprintln(os.Stderr, "query: --query flag is required")
		return 1
	}

	if err := runQuery(*dir, *query, *depth, *budget); err != nil {
		fmt.Fprintf(os.Stderr, "query: %v\n", err)
		return 1
	}
	return 0
}

func runQuery(dir, query string, depth, budget int) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", dir)
	}

	// Lazy imports — keep them here so the binary still compiles even if
	// packages are not yet linked during early development.
	return runQueryPipeline(dir, query, depth, budget)
}

// ---------------------------------------------------------------------------
// serve
// ---------------------------------------------------------------------------

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}

	if err := runServe(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}

func runServe(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", dir)
	}

	return runServePipeline(dir)
}

// ---------------------------------------------------------------------------
// verify
// ---------------------------------------------------------------------------

func cmdVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}

	if err := runVerify(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return 1
	}
	return 0
}

func runVerify(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", dir)
	}

	return runVerifyPipeline(dir)
}
