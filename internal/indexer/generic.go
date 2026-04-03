// Package indexer — generic.go provides a generic file indexer for languages
// without dedicated AST-level parsers. It creates one module-level or file-level
// node per file with best-effort import and symbol counting.
package indexer

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nurozen/context-marmot/internal/verify"
)

// maxContextLines is the maximum number of source lines stored in the Context field.
const maxContextLines = 100

// binaryProbeSize is the number of bytes read to detect binary files.
const binaryProbeSize = 512

// languageNames maps file extensions to human-readable language names.
var languageNames = map[string]string{
	".py":      "Python",
	".rb":      "Ruby",
	".java":    "Java",
	".c":       "C",
	".h":       "C Header",
	".cpp":     "C++",
	".hpp":     "C++ Header",
	".rs":      "Rust",
	".swift":   "Swift",
	".kt":      "Kotlin",
	".scala":   "Scala",
	".php":     "PHP",
	".lua":     "Lua",
	".sh":      "Shell",
	".bash":    "Bash",
	".zsh":     "Zsh",
	".r":       "R",
	".R":       "R",
	".sql":     "SQL",
	".proto":   "Protocol Buffers",
	".graphql": "GraphQL",
	".yaml":    "YAML",
	".yml":     "YAML",
	".json":    "JSON",
	".toml":    "TOML",
	".xml":     "XML",
	".html":    "HTML",
	".css":     "CSS",
	".scss":    "SCSS",
	".less":    "Less",
	".md":      "Markdown",
	".txt":     "Text",
}

// moduleExtensions lists extensions that produce "module" type nodes rather than "file".
var moduleExtensions = map[string]bool{
	".py":    true,
	".rb":    true,
	".java":  true,
	".c":     true,
	".cpp":   true,
	".h":     true,
	".hpp":   true,
	".rs":    true,
	".swift": true,
	".kt":    true,
	".scala": true,
	".php":   true,
	".lua":   true,
	".sh":    true,
	".bash":  true,
	".zsh":   true,
	".r":     true,
	".R":     true,
	".sql":   true,
}

// Import patterns per language.
var (
	rePyImport       = regexp.MustCompile(`^\s*import\s+(\S+)`)
	rePyFromImport   = regexp.MustCompile(`^\s*from\s+(\S+)\s+import`)
	reJavaImport     = regexp.MustCompile(`^\s*import\s+(?:static\s+)?([A-Za-z0-9_.]+)`)
	reRubyRequire    = regexp.MustCompile(`^\s*require\s+['"]([^'"]+)['"]`)
	reRubyRequireRel = regexp.MustCompile(`^\s*require_relative\s+['"]([^'"]+)['"]`)
	reRustUse        = regexp.MustCompile(`^\s*use\s+([A-Za-z0-9_:]+)`)
	reCInclude       = regexp.MustCompile(`^\s*#\s*include\s+[<"]([^>"]+)[>"]`)
)

// Function/class counting patterns per language.
var (
	rePyDef     = regexp.MustCompile(`^\s*def\s+\w+\(`)
	rePyClass   = regexp.MustCompile(`^\s*class\s+\w+`)
	reJavaClass = regexp.MustCompile(`(?:public|private|protected)?\s*(?:static\s+)?(?:abstract\s+)?class\s+\w+`)
	reJavaFunc  = regexp.MustCompile(`(?:public|private|protected)\s+(?:static\s+)?[\w<>\[\]]+\s+\w+\s*\(`)
	reRubyDef   = regexp.MustCompile(`^\s*def\s+\w+`)
	reRubyClass = regexp.MustCompile(`^\s*class\s+\w+`)
	reRustFn    = regexp.MustCompile(`^\s*(?:pub\s+)?fn\s+\w+\(`)
	reRustStrt  = regexp.MustCompile(`^\s*(?:pub\s+)?struct\s+\w+`)
	reRustImpl  = regexp.MustCompile(`^\s*impl\s+\w+`)
)

// GenericIndexer provides basic file-level indexing for languages that lack a
// dedicated AST parser. It extracts one entity per file (module or file type),
// best-effort import edges, and function/class counts for the summary.
type GenericIndexer struct{}

// NewGenericIndexer returns a ready-to-use GenericIndexer.
func NewGenericIndexer() *GenericIndexer {
	return &GenericIndexer{}
}

// Name returns the human-readable name of this indexer.
func (g *GenericIndexer) Name() string { return "generic" }

// SupportedExtensions returns every extension the generic indexer handles.
// All unique extensions are returned as-is (case-sensitive) so that the
// Registry performs exact-match lookups correctly (e.g., both ".r" and ".R").
func (g *GenericIndexer) SupportedExtensions() []string {
	seen := make(map[string]bool, len(languageNames))
	out := make([]string, 0, len(languageNames))
	for ext := range languageNames {
		if seen[ext] {
			continue
		}
		seen[ext] = true
		out = append(out, ext)
	}
	return out
}

// IndexFile reads the file at path and returns an IndexResult with a single
// entity representing the entire file. relPath is the path relative to the
// source root; namespace is unused by the generic indexer but accepted to
// satisfy the Indexer interface.
func (g *GenericIndexer) IndexFile(path string, relPath string, namespace string) (*IndexResult, error) {
	// Normalise relPath separators.
	relPath = filepath.ToSlash(relPath)

	// Detect binary files early.
	if isBinary(path) {
		return &IndexResult{}, nil
	}

	lines, err := readLines(path)
	if err != nil {
		// Gracefully return empty result on read errors.
		return &IndexResult{}, nil
	}

	// Empty files produce no entities.
	if len(lines) == 0 {
		return &IndexResult{}, nil
	}

	ext := filepath.Ext(relPath)
	lang := languageNames[ext]
	if lang == "" {
		lang = "Unknown"
	}

	entityType := "file"
	if moduleExtensions[ext] {
		entityType = "module"
	}

	nameNoExt := fileNameNoExt(relPath)
	entityID := pathWithoutExt(relPath)

	// Compute source hash for the entire file.
	hash, _ := verify.ComputeSourceHash(path, [2]int{0, 0})

	// Build context from first N lines.
	contextLines := lines
	if len(contextLines) > maxContextLines {
		contextLines = contextLines[:maxContextLines]
	}
	context := strings.Join(contextLines, "\n")

	// Detect imports.
	imports := detectImports(ext, lines, relPath)

	// Count functions and classes.
	funcCount, classCount := countSymbols(ext, lines)

	// Build summary.
	summary := buildSummary(entityType, nameNoExt, lang, lines, funcCount, classCount)

	// Build edges.
	edges := make([]EntityEdge, 0, len(imports))
	for _, imp := range imports {
		edges = append(edges, EntityEdge{
			Target:   imp,
			Relation: "imports",
		})
	}

	entity := SourceEntity{
		ID:      entityID,
		Type:    entityType,
		Name:    nameNoExt,
		Summary: summary,
		Context: context,
		Source: SourceRef{
			Path:  path,
			Lines: [2]int{1, len(lines)},
			Hash:  hash,
		},
		Edges: edges,
	}

	return &IndexResult{Entities: []SourceEntity{entity}}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isBinary returns true if the file appears to contain binary content.
// It probes the first binaryProbeSize bytes for null bytes.
func isBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, binaryProbeSize)
	n, err := f.Read(buf)
	if n == 0 {
		return false
	}
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
	}
	_ = err // EOF is fine
	return false
}

// readLines reads a file into a slice of lines, stripping the trailing newline
// from each. Returns an error if the file cannot be opened.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return lines, err
	}
	return lines, nil
}

// fileNameNoExt returns the base filename without its extension.
func fileNameNoExt(relPath string) string {
	base := filepath.Base(relPath)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

// pathWithoutExt returns the relPath with the file extension stripped and
// forward-slash normalised.
func pathWithoutExt(relPath string) string {
	ext := filepath.Ext(relPath)
	trimmed := strings.TrimSuffix(relPath, ext)
	return filepath.ToSlash(trimmed)
}

// buildSummary generates a one-line summary for the entity.
// It prefers extracting information from the first comment block or shebang
// line; otherwise falls back to a default description.
func buildSummary(entityType, name, lang string, lines []string, funcCount, classCount int) string {
	// Try to extract a description from the first comment or shebang.
	desc := extractLeadingComment(lines)
	if desc != "" {
		return appendSymbolCounts(fmt.Sprintf("%s %s — %s", capitalize(entityType), name, desc), funcCount, classCount)
	}

	base := fmt.Sprintf("%s %s (%s)", capitalize(entityType), name, lang)
	return appendSymbolCounts(base, funcCount, classCount)
}

// appendSymbolCounts appends function/class counts to a summary string when
// the counts are nonzero.
func appendSymbolCounts(base string, funcCount, classCount int) string {
	parts := make([]string, 0, 2)
	if funcCount > 0 {
		parts = append(parts, fmt.Sprintf("%d functions", funcCount))
	}
	if classCount > 0 {
		parts = append(parts, fmt.Sprintf("%d classes", classCount))
	}
	if len(parts) == 0 {
		return base
	}
	return base + " — " + strings.Join(parts, ", ")
}

// extractLeadingComment tries to find a meaningful description from the first
// comment block or shebang line at the top of the file.
func extractLeadingComment(lines []string) string {
	if len(lines) == 0 {
		return ""
	}

	// Shebang: extract the interpreter name.
	if strings.HasPrefix(lines[0], "#!") {
		return strings.TrimSpace(lines[0])
	}

	// Collect first contiguous block of single-line comments.
	var commentLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(commentLines) > 0 {
				break // end of leading comment block
			}
			continue // skip leading blank lines
		}
		if isComment(trimmed) {
			commentLines = append(commentLines, stripCommentPrefix(trimmed))
		} else {
			break
		}
	}

	if len(commentLines) == 0 {
		return ""
	}

	// Return the first non-empty comment line, capped at a reasonable length.
	for _, cl := range commentLines {
		cl = strings.TrimSpace(cl)
		if cl != "" {
			if len(cl) > 120 {
				cl = cl[:120] + "..."
			}
			return cl
		}
	}
	return ""
}

// isComment returns true if the trimmed line looks like a single-line comment.
func isComment(trimmed string) bool {
	return strings.HasPrefix(trimmed, "#") ||
		strings.HasPrefix(trimmed, "//") ||
		strings.HasPrefix(trimmed, "--") ||
		strings.HasPrefix(trimmed, ";")
}

// stripCommentPrefix removes the leading comment marker from a trimmed line.
func stripCommentPrefix(trimmed string) string {
	for _, prefix := range []string{"///", "//!", "//", "##", "#!", "#", "--", ";"} {
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return trimmed
}

// capitalize returns the string with its first letter uppercased.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---------------------------------------------------------------------------
// Import detection
// ---------------------------------------------------------------------------

// detectImports returns a deduplicated list of import targets found in the file.
func detectImports(ext string, lines []string, relPath string) []string {
	seen := make(map[string]bool)
	var imports []string

	add := func(target string) {
		target = strings.TrimSpace(target)
		if target == "" || seen[target] {
			return
		}
		seen[target] = true
		imports = append(imports, target)
	}

	dir := filepath.ToSlash(filepath.Dir(relPath))

	for _, line := range lines {
		switch ext {
		case ".py":
			if m := rePyImport.FindStringSubmatch(line); m != nil {
				add(m[1])
			}
			if m := rePyFromImport.FindStringSubmatch(line); m != nil {
				target := m[1]
				if strings.HasPrefix(target, ".") {
					// Relative import: resolve against file directory.
					target = resolveRelativeImport(dir, target)
				}
				add(target)
			}

		case ".java", ".kt", ".scala":
			if m := reJavaImport.FindStringSubmatch(line); m != nil {
				add(m[1])
			}

		case ".rb":
			if m := reRubyRequire.FindStringSubmatch(line); m != nil {
				add(m[1])
			}
			if m := reRubyRequireRel.FindStringSubmatch(line); m != nil {
				// Resolve relative to file directory.
				resolved := filepath.ToSlash(filepath.Join(dir, m[1]))
				add(resolved)
			}

		case ".rs":
			if m := reRustUse.FindStringSubmatch(line); m != nil {
				// Take the first path segment as the crate name.
				target := m[1]
				add(target)
			}

		case ".c", ".h", ".cpp", ".hpp":
			if m := reCInclude.FindStringSubmatch(line); m != nil {
				add(m[1])
			}
		}
	}

	return imports
}

// resolveRelativeImport resolves a Python-style relative import (e.g., ".foo"
// or "..foo") against the given directory path.
func resolveRelativeImport(dir string, importPath string) string {
	dots := 0
	for dots < len(importPath) && importPath[dots] == '.' {
		dots++
	}
	module := importPath[dots:]

	// Go up (dots-1) directories from dir.
	base := dir
	for i := 1; i < dots; i++ {
		base = filepath.ToSlash(filepath.Dir(base))
	}

	if module == "" {
		return base
	}
	// Replace Python dotted path with slash-separated path.
	module = strings.ReplaceAll(module, ".", "/")
	if base == "." || base == "" {
		return module
	}
	return base + "/" + module
}

// ---------------------------------------------------------------------------
// Symbol counting
// ---------------------------------------------------------------------------

// countSymbols counts the number of function-like and class-like declarations
// in the file. It is best-effort and based on simple regex patterns.
func countSymbols(ext string, lines []string) (funcCount, classCount int) {
	for _, line := range lines {
		switch ext {
		case ".py":
			if rePyDef.MatchString(line) {
				funcCount++
			}
			if rePyClass.MatchString(line) {
				classCount++
			}
		case ".java", ".kt", ".scala":
			if reJavaFunc.MatchString(line) {
				funcCount++
			}
			if reJavaClass.MatchString(line) {
				classCount++
			}
		case ".rb":
			if reRubyDef.MatchString(line) {
				funcCount++
			}
			if reRubyClass.MatchString(line) {
				classCount++
			}
		case ".rs":
			if reRustFn.MatchString(line) {
				funcCount++
			}
			if reRustStrt.MatchString(line) || reRustImpl.MatchString(line) {
				classCount++
			}
		}
	}
	return
}
