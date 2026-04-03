// Package main provides the ContextMarmot CLI.
//
// Commands:
//
//	marmot init       [--dir .marmot]                            Create a new vault
//	marmot configure  [--dir .marmot]                            Configure vault settings
//	marmot setup      [--dir .marmot]                            Generate MCP tool configs
//	marmot index      [--dir .marmot] [--force] [<path>] [--incremental]  Index nodes or source code
//	marmot query      --query "..." [flags]                      Query the knowledge graph
//	marmot serve      [--dir .marmot]                            Start MCP server on stdio
//	marmot verify     [--dir .marmot] [--namespace] [--staleness] Run integrity checks
//	marmot status     [--dir .marmot]                            Show vault statistics
//	marmot watch      [--dir .marmot]                            Watch for file changes and auto-reindex
//	marmot bridge     <ns-a> <ns-b> [--relations ...] [--dir]   Create cross-namespace bridge
//	marmot summarize  [--namespace ...] [--dir .marmot]          Regenerate namespace summary
//	marmot reembed    [--namespace ...] [--dir .marmot]          Rebuild all embeddings
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Build-time variables set via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
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
	case "version", "--version", "-v":
		fmt.Printf("marmot %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "init":
		return cmdInit(cmdArgs)
	case "configure":
		return cmdConfigure(cmdArgs)
	case "setup":
		return cmdSetup(cmdArgs)
	case "index":
		return cmdIndex(cmdArgs)
	case "query":
		return cmdQuery(cmdArgs)
	case "serve":
		return cmdServe(cmdArgs)
	case "verify":
		return cmdVerify(cmdArgs)
	case "status":
		return cmdStatus(cmdArgs)
	case "watch":
		return cmdWatch(cmdArgs)
	case "bridge":
		return cmdBridge(cmdArgs)
	case "summarize":
		return cmdSummarize(cmdArgs)
	case "reembed":
		return cmdReembed(cmdArgs)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", command)
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: marmot <command> [flags]")
	fmt.Fprintln(os.Stderr, "commands: version, init, configure, setup, index, query, serve, verify, status, watch, bridge, summarize, reembed")
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

	// Auto-run configure so the user is fully set up in one step.
	fmt.Println()
	if err := runConfigure(*dir, os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "configure: %v\n", err)
		return 1
	}

	// Auto-run setup to generate MCP configs for detected tools.
	fmt.Println()
	if err := runSetup(*dir, nil); err != nil {
		fmt.Fprintf(os.Stderr, "setup: %v\n", err)
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
	incremental := fs.Bool("incremental", false, "skip unchanged files during static analysis indexing")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}

	// Check for positional arg (source path).
	// Go's flag.FlagSet stops parsing at the first non-flag argument, so
	// flags placed after the positional path (e.g., `marmot index ./src --incremental`)
	// end up in the remaining args. We scan for them manually.
	remaining := fs.Args()
	if len(remaining) > 0 {
		var srcDir string
		for _, arg := range remaining {
			switch arg {
			case "--incremental", "-incremental":
				inc := true
				incremental = &inc
			case "--force", "-force":
				f := true
				force = &f
			default:
				if srcDir == "" {
					srcDir = arg
				}
			}
		}
		if srcDir != "" {
			// Static analysis indexing of a source directory.
			if err := runStaticIndex(*dir, srcDir, *incremental); err != nil {
				fmt.Fprintf(os.Stderr, "index: %v\n", err)
				return 1
			}
			return 0
		}
	}

	if err := runIndex(*dir, *force); err != nil {
		fmt.Fprintf(os.Stderr, "index: %v\n", err)
		return 1
	}
	return 0
}

func runStaticIndex(dir, srcDir string, incremental bool) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", dir)
	}

	return runStaticIndexPipeline(dir, srcDir, incremental)
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
	ns := fs.String("namespace", "", "namespace to verify (default: all)")
	staleness := fs.Bool("staleness", false, "also check source staleness (requires source.path in nodes)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}

	if err := runVerifyEnhanced(*dir, *ns, *staleness); err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	if err := runStatusPipeline(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// watch
// ---------------------------------------------------------------------------

func cmdWatch(args []string) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	if err := runWatchPipeline(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "watch: %v\n", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// bridge
// ---------------------------------------------------------------------------

func cmdBridge(args []string) int {
	fs := flag.NewFlagSet("bridge", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	relations := fs.String("relations", "calls,reads,writes,references,cross_project,associated", "comma-separated list of allowed relations")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	remaining := fs.Args()
	if len(remaining) != 2 {
		fmt.Fprintln(os.Stderr, "bridge: requires exactly two namespace names: marmot bridge <ns-a> <ns-b>")
		return 1
	}
	// Validate namespace names to prevent path traversal.
	for _, ns := range remaining {
		if ns == "" || strings.Contains(ns, "/") || strings.Contains(ns, "\\") || strings.Contains(ns, "..") || strings.HasPrefix(ns, ".") || strings.HasPrefix(ns, "_") {
			fmt.Fprintf(os.Stderr, "bridge: invalid namespace name %q (must be a simple identifier)\n", ns)
			return 1
		}
	}
	if err := runBridgePipeline(*dir, remaining[0], remaining[1], *relations); err != nil {
		fmt.Fprintf(os.Stderr, "bridge: %v\n", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// summarize
// ---------------------------------------------------------------------------

func cmdSummarize(args []string) int {
	fs := flag.NewFlagSet("summarize", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	ns := fs.String("namespace", "", "namespace to summarize (default: from config)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	if err := runSummarizePipeline(*dir, *ns); err != nil {
		fmt.Fprintf(os.Stderr, "summarize: %v\n", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// reembed
// ---------------------------------------------------------------------------

func cmdReembed(args []string) int {
	fs := flag.NewFlagSet("reembed", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	ns := fs.String("namespace", "", "namespace to reembed (currently applies to all; reserved for future use)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	if *ns != "" {
		fmt.Fprintf(os.Stderr, "reembed: --namespace flag is reserved for future use; rebuilding all embeddings\n")
	}
	if err := runReembedPipeline(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "reembed: %v\n", err)
		return 1
	}
	return 0
}
