// Package packager bundles a .marmot vault into a sharable archive
// (tar.gz or zip). The bundle is "agent-native": readers drop the archive
// at their project root and immediately have read-only graph access via MCP.
//
// The packager is intentionally defensive about secrets — files like
// .marmot-data/.env and .obsidian/workspace.json are *always* excluded,
// regardless of caller flags.
package packager

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/node"
)

// PackageVersion is the version stamp written into _package.md manifests.
const PackageVersion = "1"

// archiveRoot is the top-level directory inside the archive. Extracting at the
// project root therefore creates `.marmot/`.
const archiveRoot = ".marmot"

// Options controls how a vault is packaged.
type Options struct {
	SourceDir    string // path to .marmot directory
	OutPath      string // archive output path
	Zip          bool   // true = .zip; false = .tar.gz
	IncludeHeat  bool   // include _heat/*.md
	NoObsidian   bool   // exclude .obsidian/ entirely
	GeneratorTag string // e.g. "marmot package-docs v0.1.4"
}

// Manifest is the YAML frontmatter written to .marmot/_package.md.
type Manifest struct {
	PackageVersion    string    `yaml:"package_version"`
	Created           time.Time `yaml:"created"`
	SourceVaultID     string    `yaml:"source_vault_id"`
	SourceVaultPath   string    `yaml:"source_vault_path"`
	EmbeddingProvider string    `yaml:"embedding_provider"`
	EmbeddingModel    string    `yaml:"embedding_model"`
	NodeCount         int       `yaml:"node_count"`
	EdgeCount         int       `yaml:"edge_count"`
	Namespaces        []string  `yaml:"namespaces"`
	ReadOnly          bool      `yaml:"read_only"`
	Generator         string    `yaml:"generator"`
}

// alwaysExcluded is the set of relative paths (slash-separated) that the
// packager refuses to include even if upstream flags would otherwise allow
// them. Keep this list minimal but explicit.
var alwaysExcluded = map[string]bool{
	".marmot-data/.env":              true,
	".marmot-data/embeddings.db-wal": true,
	".marmot-data/embeddings.db-shm": true,
	".obsidian/workspace.json":       true,
	".obsidian/workspace-mobile.json": true,
}

// Package walks SourceDir, applies exclusions, sanitizes _config.md,
// generates _package.md, and writes the archive to OutPath. Returns the
// generated Manifest on success.
func Package(opts Options) (*Manifest, error) {
	// --- Preconditions -----------------------------------------------------
	if opts.SourceDir == "" {
		return nil, fmt.Errorf("packager: SourceDir is required")
	}
	info, err := os.Stat(opts.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("packager: source dir %q: %w", opts.SourceDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("packager: source %q is not a directory", opts.SourceDir)
	}
	cfgPath := filepath.Join(opts.SourceDir, "_config.md")
	if _, err := os.Stat(cfgPath); err != nil {
		return nil, fmt.Errorf("packager: missing _config.md in %q: %w", opts.SourceDir, err)
	}
	embPath := filepath.Join(opts.SourceDir, ".marmot-data", "embeddings.db")
	embInfo, err := os.Stat(embPath)
	if err != nil {
		return nil, fmt.Errorf("packager: embeddings database missing — run 'marmot index' first (looked at %s)", embPath)
	}
	if embInfo.Size() == 0 {
		return nil, fmt.Errorf("packager: embeddings database is empty — run 'marmot index' first (%s)", embPath)
	}
	if opts.OutPath == "" {
		return nil, fmt.Errorf("packager: OutPath is required")
	}

	// --- Sanitize _config.md and load original config ----------------------
	sanitizedConfig, configBody, sourceCfg, err := sanitizeConfig(opts.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("packager: sanitize config: %w", err)
	}

	// --- Build manifest ----------------------------------------------------
	nodeCount, edgeCount, err := countNodesAndEdges(opts.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("packager: count nodes: %w", err)
	}
	namespaces, err := discoverNamespaces(opts.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("packager: discover namespaces: %w", err)
	}
	absSource, _ := filepath.Abs(opts.SourceDir)
	manifest := &Manifest{
		PackageVersion:    PackageVersion,
		Created:           time.Now().UTC(),
		SourceVaultID:     sourceCfg.VaultID,
		SourceVaultPath:   absSource,
		EmbeddingProvider: sourceCfg.EmbeddingProvider,
		EmbeddingModel:    sourceCfg.EmbeddingModel,
		NodeCount:         nodeCount,
		EdgeCount:         edgeCount,
		Namespaces:        namespaces,
		ReadOnly:          true,
		Generator:         opts.GeneratorTag,
	}
	manifestBytes, err := renderPackageMD(manifest)
	if err != nil {
		return nil, fmt.Errorf("packager: render manifest: %w", err)
	}

	// --- Walk and collect archive entries ----------------------------------
	entries, err := collectEntries(opts)
	if err != nil {
		return nil, fmt.Errorf("packager: collect entries: %w", err)
	}

	// --- Write archive atomically ------------------------------------------
	tmpPath := opts.OutPath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(opts.OutPath), 0o755); err != nil {
		return nil, fmt.Errorf("packager: prepare out dir: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmpPath) }

	if opts.Zip {
		if err := writeZip(tmpPath, entries, sanitizedConfig, manifestBytes, configBody); err != nil {
			cleanup()
			return nil, err
		}
	} else {
		if err := writeTarGz(tmpPath, entries, sanitizedConfig, manifestBytes, configBody); err != nil {
			cleanup()
			return nil, err
		}
	}
	if err := os.Rename(tmpPath, opts.OutPath); err != nil {
		cleanup()
		return nil, fmt.Errorf("packager: rename out: %w", err)
	}
	return manifest, nil
}

// fileEntry describes a real file on disk to be copied into the archive.
type fileEntry struct {
	rel  string // path relative to SourceDir, slash-separated
	abs  string // absolute path on disk
	size int64
	mode os.FileMode
	mod  time.Time
}

// collectEntries walks SourceDir and returns all files that should be included
// in the archive. The special files _config.md and _package.md are NOT
// returned here — they are written separately from in-memory buffers.
func collectEntries(opts Options) ([]fileEntry, error) {
	var out []fileEntry
	root := filepath.Clean(opts.SourceDir)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relSlash := filepath.ToSlash(rel)

		// Directory-level filtering.
		if d.IsDir() {
			if shouldSkipDir(relSlash, opts) {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip special files we re-emit ourselves.
		if relSlash == "_config.md" || relSlash == "_package.md" {
			return nil
		}

		// Always-exclude list and flag-driven exclusions.
		if shouldSkipFile(relSlash, opts) {
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		// Plain files only (no symlinks, sockets, devices).
		if !info.Mode().IsRegular() {
			return nil
		}
		out = append(out, fileEntry{
			rel:  relSlash,
			abs:  path,
			size: info.Size(),
			mode: info.Mode().Perm(),
			mod:  info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Stable ordering for deterministic archives.
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// shouldSkipDir returns true for directories that should be pruned during walk.
func shouldSkipDir(relSlash string, opts Options) bool {
	if opts.NoObsidian && (relSlash == ".obsidian" || strings.HasPrefix(relSlash, ".obsidian/")) {
		return true
	}
	if !opts.IncludeHeat && (relSlash == "_heat" || strings.HasPrefix(relSlash, "_heat/")) {
		return true
	}
	return false
}

// shouldSkipFile returns true for individual files that should not be archived.
func shouldSkipFile(relSlash string, opts Options) bool {
	if alwaysExcluded[relSlash] {
		return true
	}
	if opts.NoObsidian && strings.HasPrefix(relSlash, ".obsidian/") {
		return true
	}
	if !opts.IncludeHeat && strings.HasPrefix(relSlash, "_heat/") {
		return true
	}
	return false
}

// sanitizeConfig loads the source vault config, forces read_only=true, strips
// any inline secret-bearing fields (defensive), and returns the sanitized
// YAML+body bytes plus the original parsed config (for manifest fields).
func sanitizeConfig(sourceDir string) (sanitized []byte, body string, original *config.VaultConfig, err error) {
	cfg, body, err := config.LoadRaw(sourceDir)
	if err != nil {
		return nil, "", nil, err
	}
	if cfg == nil {
		return nil, "", nil, fmt.Errorf("config not found")
	}

	// Re-parse the raw frontmatter into a generic map so we can
	//   (a) drop any unexpected secret-looking fields,
	//   (b) inject `read_only: true` even though VaultConfig may not yet have
	//       a typed field for it.
	rawPath := filepath.Join(sourceDir, "_config.md")
	rawBytes, err := os.ReadFile(rawPath)
	if err != nil {
		return nil, "", nil, err
	}
	fmYAML, _, err := splitFrontmatter(rawBytes)
	if err != nil {
		// Fall back to typed config if the frontmatter is unusual.
		fmYAML = nil
	}

	out := map[string]any{}
	if len(fmYAML) > 0 {
		if err := yaml.Unmarshal(fmYAML, &out); err != nil {
			return nil, "", nil, fmt.Errorf("parse config yaml: %w", err)
		}
	}

	// Defensively strip any obviously secret-bearing fields. Keys that match
	// well-known env-var names or contain "api_key" / "secret" / "token" are
	// dropped. Embedding-related fields explicitly do *not* match.
	for k := range out {
		if isSecretKey(k) {
			delete(out, k)
		}
	}
	// Strip values that look like raw API keys regardless of key name.
	for k, v := range out {
		if s, ok := v.(string); ok && looksLikeAPIKey(s) {
			delete(out, k)
		}
	}

	// Force read_only.
	out["read_only"] = true

	// Backfill required fields from the typed config in case the frontmatter
	// was missing or malformed.
	if _, ok := out["version"]; !ok {
		out["version"] = cfg.Version
	}
	if _, ok := out["namespace"]; !ok {
		out["namespace"] = cfg.Namespace
	}
	if _, ok := out["embedding_provider"]; !ok {
		out["embedding_provider"] = cfg.EmbeddingProvider
	}
	if _, ok := out["embedding_model"]; !ok {
		out["embedding_model"] = cfg.EmbeddingModel
	}
	if cfg.VaultID != "" {
		if _, ok := out["vault_id"]; !ok {
			out["vault_id"] = cfg.VaultID
		}
	}

	yamlBytes, err := yaml.Marshal(out)
	if err != nil {
		return nil, "", nil, fmt.Errorf("marshal sanitized config: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString(body)
	}
	return []byte(buf.String()), body, cfg, nil
}

// splitFrontmatter extracts YAML frontmatter bytes from a markdown file.
// Returns (yaml-bytes, body-string, error).
func splitFrontmatter(data []byte) ([]byte, string, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---") {
		return nil, s, fmt.Errorf("no frontmatter")
	}
	rest := s[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return nil, s, fmt.Errorf("unterminated frontmatter")
	}
	fm := strings.TrimLeft(rest[:idx], "\n")
	body := rest[idx+4:]
	body = strings.TrimLeft(body, "\r\n")
	return []byte(fm), body, nil
}

// isSecretKey returns true if a config key name looks like a credential field.
// Embedding model/provider fields and vault_id are explicitly safe.
func isSecretKey(k string) bool {
	lower := strings.ToLower(k)
	switch lower {
	case "openai_api_key", "anthropic_api_key", "voyage_api_key":
		return true
	}
	if strings.Contains(lower, "api_key") {
		return true
	}
	if strings.Contains(lower, "secret") {
		return true
	}
	if strings.Contains(lower, "password") {
		return true
	}
	// Be conservative with "token" — token_budget is a legitimate field.
	if strings.Contains(lower, "token") && !strings.Contains(lower, "budget") {
		return true
	}
	return false
}

// looksLikeAPIKey returns true if a string value plausibly contains an API
// key. Conservative heuristic: long strings starting with common provider
// prefixes.
func looksLikeAPIKey(s string) bool {
	if len(s) < 20 {
		return false
	}
	prefixes := []string{"sk-", "sk_", "pk-", "pk_", "Bearer ", "voyage-"}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// countNodesAndEdges parses every node *.md file (excluding underscore-
// prefixed special files) and returns total node and edge counts.
func countNodesAndEdges(sourceDir string) (int, int, error) {
	nodeCount := 0
	edgeCount := 0
	root := filepath.Clean(sourceDir)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip data/system directories and any underscore-prefixed
			// reserved directory (e.g. _heat, _bridges).
			rel, _ := filepath.Rel(root, path)
			relSlash := filepath.ToSlash(rel)
			if relSlash == "." {
				return nil
			}
			top := relSlash
			if i := strings.Index(top, "/"); i >= 0 {
				top = top[:i]
			}
			if top == ".marmot-data" || top == ".obsidian" || strings.HasPrefix(top, "_") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "_") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		n, parseErr := node.ParseNode(data, path)
		if parseErr != nil {
			// Non-fatal: skip files that don't parse as nodes (e.g. notes).
			return nil
		}
		nodeCount++
		edgeCount += len(n.Edges)
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return nodeCount, edgeCount, nil
}

// discoverNamespaces returns a sorted list of top-level directories that
// contain a _namespace.md file. Defaults to ["default"] when none are found.
func discoverNamespaces(sourceDir string) ([]string, error) {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, err
	}
	var ns []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip system/special directories.
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(sourceDir, name, "_namespace.md")); err == nil {
			ns = append(ns, name)
		}
	}
	sort.Strings(ns)
	if len(ns) == 0 {
		ns = []string{"default"}
	}
	return ns, nil
}

// renderPackageMD turns a manifest into the bytes of _package.md (frontmatter
// + the canned how-to-use body).
func renderPackageMD(m *Manifest) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	buf.WriteString(packageMDBody)
	return []byte(buf.String()), nil
}

// packageMDBody is the static documentation body appended to _package.md.
const packageMDBody = `
# ContextMarmot Documentation Bundle

This is a packaged ContextMarmot vault. It contains pre-built embeddings and
read-only graph data for agentic documentation lookup.

## Drop-in usage

1. Extract the archive at the root of your project (creates ` + "`.marmot/`" + `).
2. Run ` + "`marmot setup --read-only`" + ` to register the bundle with your agent.
3. Your agent can now call ` + "`context_query`" + `, ` + "`context_verify`" + `, and read tools.

## API key (optional)

Without a key, ` + "`context_query`" + ` falls back to lexical search across node
summaries and content. To enable semantic search, set ` + "`OPENAI_API_KEY`" + ` (or
the matching key for the bundle's embedding provider) and restart your agent.

## Read-only mode

In read-only mode the following MCP tools are disabled:

- ` + "`context_write`" + `
- ` + "`context_tag`" + ` (write side)
- ` + "`context_delete`" + `

This protects the bundle from accidental mutation and makes the contract
obvious to the agent.
`

// ---------------------------------------------------------------------------
// Archive writers
// ---------------------------------------------------------------------------

func writeTarGz(outPath string, entries []fileEntry, sanitizedConfig, manifest []byte, _ string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz := gzip.NewWriter(f)
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()

	now := time.Now().UTC()
	// Top-level directory entry.
	if err := tw.WriteHeader(&tar.Header{
		Name:     archiveRoot + "/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
		ModTime:  now,
	}); err != nil {
		return err
	}

	// Synthetic files first.
	if err := tarWriteBytes(tw, archiveRoot+"/_package.md", manifest, now); err != nil {
		return err
	}
	if err := tarWriteBytes(tw, archiveRoot+"/_config.md", sanitizedConfig, now); err != nil {
		return err
	}

	// Track directories already written so we emit them once, in order.
	dirSeen := map[string]bool{archiveRoot: true}

	for _, e := range entries {
		// Ensure parent directories are present.
		parts := strings.Split(e.rel, "/")
		for i := 0; i < len(parts)-1; i++ {
			dir := archiveRoot + "/" + strings.Join(parts[:i+1], "/")
			if dirSeen[dir] {
				continue
			}
			dirSeen[dir] = true
			if err := tw.WriteHeader(&tar.Header{
				Name:     dir + "/",
				Mode:     0o755,
				Typeflag: tar.TypeDir,
				ModTime:  e.mod,
			}); err != nil {
				return err
			}
		}
		if err := tarWriteFile(tw, archiveRoot+"/"+e.rel, e); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}

func tarWriteBytes(tw *tar.Writer, name string, data []byte, mod time.Time) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(data)),
		ModTime:  mod,
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func tarWriteFile(tw *tar.Writer, name string, e fileEntry) error {
	mode := e.mode
	if mode == 0 {
		mode = 0o644
	}
	hdr := &tar.Header{
		Name:     name,
		Mode:     int64(mode),
		Size:     e.size,
		ModTime:  e.mod,
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	src, err := os.Open(e.abs)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	_, err = io.Copy(tw, src)
	return err
}

func writeZip(outPath string, entries []fileEntry, sanitizedConfig, manifest []byte, _ string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	defer func() { _ = zw.Close() }()

	now := time.Now().UTC()
	if err := zipWriteBytes(zw, archiveRoot+"/_package.md", manifest, now); err != nil {
		return err
	}
	if err := zipWriteBytes(zw, archiveRoot+"/_config.md", sanitizedConfig, now); err != nil {
		return err
	}
	for _, e := range entries {
		if err := zipWriteFile(zw, archiveRoot+"/"+e.rel, e); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

func zipWriteBytes(zw *zip.Writer, name string, data []byte, mod time.Time) error {
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mod}
	hdr.SetMode(0o644)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func zipWriteFile(zw *zip.Writer, name string, e fileEntry) error {
	mode := e.mode
	if mode == 0 {
		mode = 0o644
	}
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: e.mod}
	hdr.SetMode(mode)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	src, err := os.Open(e.abs)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	_, err = io.Copy(w, src)
	return err
}
