package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/packager"
)

// cmdPackageDocs implements `marmot package-docs`.
//
//	marmot package-docs [--dir .marmot] [--out vault.tar.gz]
//	                    [--zip] [--include-heat] [--no-obsidian]
func cmdPackageDocs(args []string) int {
	fs := flag.NewFlagSet("package-docs", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	out := fs.String("out", "", "output archive path (default: <vault-id-or-dirname>.tar.gz)")
	zipOut := fs.Bool("zip", false, "emit a .zip archive instead of .tar.gz")
	includeHeat := fs.Bool("include-heat", false, "include _heat/ usage telemetry (excluded by default)")
	noObsidian := fs.Bool("no-obsidian", false, "exclude the entire .obsidian/ directory")

	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}

	// Validate vault before deriving defaults — surface common errors early.
	info, err := os.Stat(*dir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "package-docs: vault %q not found (run from a project containing .marmot/)\n", *dir)
		return 1
	}

	if *out == "" {
		*out = defaultOutPath(*dir, *zipOut)
	}

	opts := packager.Options{
		SourceDir:    *dir,
		OutPath:      *out,
		Zip:          *zipOut,
		IncludeHeat:  *includeHeat,
		NoObsidian:   *noObsidian,
		GeneratorTag: fmt.Sprintf("marmot package-docs %s", version),
	}

	mf, err := packager.Package(opts)
	if err != nil {
		// Distinguish "vault unsuitable" (exit 2) from other errors (exit 1).
		msg := err.Error()
		if strings.Contains(msg, "embeddings database") || strings.Contains(msg, "missing _config.md") {
			fmt.Fprintf(os.Stderr, "package-docs: %v\n", err)
			return 2
		}
		fmt.Fprintf(os.Stderr, "package-docs: %v\n", err)
		return 1
	}

	size := humanSize(*out)
	fmt.Printf("Packaged %d nodes (%d edges, %d namespaces) to %s (%s)\n",
		mf.NodeCount, mf.EdgeCount, len(mf.Namespaces), *out, size)
	return 0
}

// defaultOutPath returns the default archive filename based on vault_id (if
// set) or the directory's parent name, with the appropriate extension.
func defaultOutPath(vaultDir string, asZip bool) string {
	base := ""
	if cfg, err := config.Load(vaultDir); err == nil && cfg != nil && cfg.VaultID != "" {
		base = cfg.VaultID
	}
	if base == "" {
		// Use the parent directory name (since vaultDir is typically `.marmot`,
		// using its own name would always produce ".marmot.tar.gz").
		abs, err := filepath.Abs(vaultDir)
		if err == nil {
			parent := filepath.Base(filepath.Dir(abs))
			if parent != "" && parent != "." && parent != string(filepath.Separator) {
				base = parent
			}
		}
	}
	if base == "" {
		base = "marmot-bundle"
	}
	ext := ".tar.gz"
	if asZip {
		ext = ".zip"
	}
	return base + ext
}

// humanSize returns a short human-readable size for path. Returns "?" on
// stat error — display only.
func humanSize(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "?"
	}
	n := info.Size()
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1fGB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1fMB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1fKB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
