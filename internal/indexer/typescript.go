// Package indexer provides static analysis indexers that extract source code
// entities and their relationships from files. Each indexer targets a specific
// language family and produces a uniform IndexResult.
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

// TypeScriptIndexer extracts entities from TypeScript and JavaScript source
// files using regex-based pattern matching. It handles .ts, .tsx, .js, and
// .jsx extensions and recognises modules, classes, interfaces, functions,
// type aliases, methods, and import relationships.
type TypeScriptIndexer struct{}

// NewTypeScriptIndexer returns a ready-to-use TypeScript/JavaScript indexer.
func NewTypeScriptIndexer() *TypeScriptIndexer {
	return &TypeScriptIndexer{}
}

// Name returns a human-readable identifier for this indexer.
func (t *TypeScriptIndexer) Name() string { return "typescript" }

// SupportedExtensions returns the file extensions this indexer can process.
func (t *TypeScriptIndexer) SupportedExtensions() []string {
	return []string{".ts", ".tsx", ".js", ".jsx"}
}

// ---------------------------------------------------------------------------
// Compiled regex patterns
// ---------------------------------------------------------------------------

var (
	// Import patterns.
	reImportFrom   = regexp.MustCompile(`(?m)^\s*import\s+(?:(?:type\s+)?(?:\{[^}]*\}|[^;{]+)\s+from\s+)['"]([^'"]+)['"]`)
	reRequire      = regexp.MustCompile(`(?m)^\s*(?:const|let|var)\s+\w+\s*=\s*require\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	reImportEquals = regexp.MustCompile(`(?m)^\s*import\s+\w+\s*=\s*require\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	// Re-export patterns: export { ... } from '...'; export * from '...'
	reReExportFrom = regexp.MustCompile(`(?m)^\s*export\s+(?:\{[^}]*\}|\*)\s+from\s+['"]([^'"]+)['"]`)

	// Class: optional export/abstract, capture name, optional extends, optional implements.
	reClass = regexp.MustCompile(`(?m)^(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+(\w+)(?:\s+extends\s+(\w+))?(?:\s+implements\s+([\w\s,]+))?\s*\{?`)

	// Interface: optional export, capture name, optional extends.
	reInterface = regexp.MustCompile(`(?m)^(?:export\s+)?interface\s+(\w+)(?:\s+extends\s+([\w\s,]+))?\s*\{?`)

	// Function: optional export/default/async, capture name.
	reFunction = regexp.MustCompile(`(?m)^(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s+(\w+)\s*[\(<]`)

	// Top-level arrow / function expression assigned to const/let/var.
	// Optional return type annotation `: ReturnType` is allowed between params and =>.
	reArrow = regexp.MustCompile(`(?m)^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\([^)]*\)\s*(?::\s*\S+\s*)?=>`)

	// Type alias.
	reType = regexp.MustCompile(`(?m)^(?:export\s+)?type\s+(\w+)\s*(?:<[^>]*>)?\s*=`)

	// Method inside a class body (simplified).
	reMethod = regexp.MustCompile(`(?m)^\s+(?:(?:public|private|protected|static|abstract|async|readonly|override|get|set)\s+)*(\w+)\s*[\(<]`)

	// JSDoc comment immediately preceding a line. We search backwards for it.
	reJSDocBlock = regexp.MustCompile(`(?s)/\*\*(.*?)\*/`)
)

// ---------------------------------------------------------------------------
// IndexFile
// ---------------------------------------------------------------------------

// IndexFile parses the file at the given absolute path and returns an
// IndexResult containing all extracted entities and their edges. relPath is
// the repository-relative path used for ID derivation; namespace is prepended
// when non-empty.
func (t *TypeScriptIndexer) IndexFile(path string, relPath string, namespace string) (*IndexResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("typescript indexer: read %s: %w", path, err)
	}

	content := string(raw)
	lines := tsSplitLines(content)

	if len(lines) == 0 {
		return &IndexResult{}, nil
	}

	// Build a "cleaned" version of the source that masks strings and comments
	// so that regex patterns do not match inside them.
	cleaned := maskStringsAndComments(content)
	cleanedLines := tsSplitLines(cleaned)

	// Derive module ID.
	moduleID := deriveModuleID(relPath, namespace)

	// ---- Module entity ----
	moduleHash, _ := verify.ComputeSourceHash(path, [2]int{0, 0})
	modSummary := fmt.Sprintf("Module %s", filepath.Base(relPath))
	if cmt := firstComment(lines); cmt != "" {
		modSummary = fmt.Sprintf("Module %s — %s", filepath.Base(relPath), cmt)
	}

	moduleEntity := SourceEntity{
		ID:      moduleID,
		Type:    "module",
		Name:    filepath.Base(relPath),
		Summary: modSummary,
		Context: truncateContext(content, 500),
		Source: SourceRef{
			Path:  path,
			Lines: [2]int{1, len(lines)},
			Hash:  moduleHash,
		},
	}

	var entities []SourceEntity

	// ---- Imports → module edges ----
	moduleEntity.Edges = append(moduleEntity.Edges, extractImportEdges(lines, relPath, namespace)...)

	// ---- Classes ----
	classEntities := extractClasses(cleanedLines, lines, path, relPath, moduleID)
	for i := range classEntities {
		moduleEntity.Edges = append(moduleEntity.Edges, EntityEdge{
			Target:   classEntities[i].ID,
			Relation: "contains",
		})
	}
	entities = append(entities, classEntities...)

	// ---- Interfaces ----
	ifaceEntities := extractInterfaces(cleanedLines, lines, path, relPath, moduleID)
	for i := range ifaceEntities {
		moduleEntity.Edges = append(moduleEntity.Edges, EntityEdge{
			Target:   ifaceEntities[i].ID,
			Relation: "contains",
		})
	}
	entities = append(entities, ifaceEntities...)

	// ---- Functions ----
	funcEntities := extractFunctions(cleanedLines, lines, path, relPath, moduleID)
	for i := range funcEntities {
		moduleEntity.Edges = append(moduleEntity.Edges, EntityEdge{
			Target:   funcEntities[i].ID,
			Relation: "contains",
		})
	}
	entities = append(entities, funcEntities...)

	// ---- Type aliases ----
	typeEntities := extractTypes(cleanedLines, lines, path, relPath, moduleID)
	for i := range typeEntities {
		moduleEntity.Edges = append(moduleEntity.Edges, EntityEdge{
			Target:   typeEntities[i].ID,
			Relation: "contains",
		})
	}
	entities = append(entities, typeEntities...)

	// Prepend the module entity.
	result := &IndexResult{
		Entities: append([]SourceEntity{moduleEntity}, entities...),
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Import extraction
// ---------------------------------------------------------------------------

// extractImportEdges parses import statements from the cleaned source lines
// and returns "imports" edges targeting the resolved module paths.
func extractImportEdges(cleanedLines []string, relPath string, namespace string) []EntityEdge {
	joined := strings.Join(cleanedLines, "\n")
	var edges []EntityEdge
	seen := map[string]bool{}

	addImport := func(raw string) {
		resolved := resolveImportPath(raw, relPath)
		// Prepend namespace to match how entity IDs are derived via deriveModuleID.
		if namespace != "" {
			resolved = namespace + "/" + resolved
		}
		if !seen[resolved] {
			seen[resolved] = true
			edges = append(edges, EntityEdge{
				Target:   resolved,
				Relation: "imports",
			})
		}
	}

	for _, m := range reImportFrom.FindAllStringSubmatch(joined, -1) {
		addImport(m[1])
	}
	for _, m := range reRequire.FindAllStringSubmatch(joined, -1) {
		addImport(m[1])
	}
	for _, m := range reImportEquals.FindAllStringSubmatch(joined, -1) {
		addImport(m[1])
	}
	for _, m := range reReExportFrom.FindAllStringSubmatch(joined, -1) {
		addImport(m[1])
	}

	return edges
}

// resolveImportPath turns an import specifier into a module ID string.
// Relative paths are resolved against the directory of the importing file;
// package imports are returned as-is.
func resolveImportPath(raw string, importerRelPath string) string {
	if !strings.HasPrefix(raw, ".") {
		return raw
	}
	dir := filepath.Dir(importerRelPath)
	joined := filepath.Join(dir, raw)
	return filepath.ToSlash(joined)
}

// ---------------------------------------------------------------------------
// Class extraction
// ---------------------------------------------------------------------------

func extractClasses(cleanedLines, origLines []string, absPath, relPath, moduleID string) []SourceEntity {
	var entities []SourceEntity
	for i, cl := range cleanedLines {
		m := reClass.FindStringSubmatch(cl)
		if m == nil {
			continue
		}
		name := m[1]
		lineNum := i + 1 // 1-indexed
		endLine := findBlockEnd(origLines, cleanedLines, lineNum)

		id := moduleID + "/" + name
		src := makeSourceRef(absPath, relPath, lineNum, endLine)
		ctx := extractContext(origLines, lineNum, endLine)
		summary := jsDocBefore(origLines, lineNum)
		if summary == "" {
			summary = fmt.Sprintf("Class %s in module %s", name, filepath.Base(relPath))
		}

		entity := SourceEntity{
			ID:      id,
			Type:    "class",
			Name:    name,
			Summary: summary,
			Context: ctx,
			Source:  src,
		}

		// extends edge
		if m[2] != "" {
			entity.Edges = append(entity.Edges, EntityEdge{
				Target:   m[2],
				Relation: "extends",
			})
		}

		// implements edges
		if m[3] != "" {
			for _, iface := range splitCSV(m[3]) {
				entity.Edges = append(entity.Edges, EntityEdge{
					Target:   iface,
					Relation: "implements",
				})
			}
		}

		// Extract methods within the class body.
		methods := extractMethods(cleanedLines, origLines, absPath, relPath, moduleID, name, lineNum, endLine)
		for j := range methods {
			entity.Edges = append(entity.Edges, EntityEdge{
				Target:   methods[j].ID,
				Relation: "contains",
			})
		}
		entities = append(entities, entity)
		entities = append(entities, methods...)
	}
	return entities
}

// ---------------------------------------------------------------------------
// Method extraction (within a class body)
// ---------------------------------------------------------------------------

func extractMethods(cleanedLines, origLines []string, absPath, relPath, moduleID, className string, classStart, classEnd int) []SourceEntity {
	var entities []SourceEntity
	skip := map[string]bool{"constructor": true, "if": true, "for": true, "while": true, "switch": true, "return": true, "catch": true, "else": true}

	// Only search within the class body (classStart..classEnd), skipping the
	// declaration line itself.
	for i := classStart; i < classEnd && i < len(cleanedLines); i++ {
		m := reMethod.FindStringSubmatch(cleanedLines[i])
		if m == nil {
			continue
		}
		name := m[1]
		// Skip keywords that look like methods but aren't.
		if skip[name] {
			continue
		}
		// Skip if the line also matches a class/function/interface/type declaration
		// (which would be an inner declaration, not a method).
		if reClass.MatchString(cleanedLines[i]) || reFunction.MatchString(cleanedLines[i]) {
			continue
		}

		lineNum := i + 1
		endLine := findBlockEnd(origLines, cleanedLines, lineNum)
		// Clamp method end to class end.
		if endLine > classEnd {
			endLine = classEnd
		}

		id := fmt.Sprintf("%s/%s.%s", moduleID, className, name)
		src := makeSourceRef(absPath, relPath, lineNum, endLine)
		ctx := extractContext(origLines, lineNum, endLine)
		summary := jsDocBefore(origLines, lineNum)
		if summary == "" {
			summary = fmt.Sprintf("Method %s.%s in module %s", className, name, filepath.Base(relPath))
		}

		entities = append(entities, SourceEntity{
			ID:      id,
			Type:    "method",
			Name:    name,
			Summary: summary,
			Context: ctx,
			Source:  src,
		})
	}
	return entities
}

// ---------------------------------------------------------------------------
// Interface extraction
// ---------------------------------------------------------------------------

func extractInterfaces(cleanedLines, origLines []string, absPath, relPath, moduleID string) []SourceEntity {
	var entities []SourceEntity
	for i, cl := range cleanedLines {
		m := reInterface.FindStringSubmatch(cl)
		if m == nil {
			continue
		}
		name := m[1]
		lineNum := i + 1
		endLine := findBlockEnd(origLines, cleanedLines, lineNum)

		id := moduleID + "/" + name
		src := makeSourceRef(absPath, relPath, lineNum, endLine)
		ctx := extractContext(origLines, lineNum, endLine)
		summary := jsDocBefore(origLines, lineNum)
		if summary == "" {
			summary = fmt.Sprintf("Interface %s in module %s", name, filepath.Base(relPath))
		}

		entity := SourceEntity{
			ID:      id,
			Type:    "interface",
			Name:    name,
			Summary: summary,
			Context: ctx,
			Source:  src,
		}

		// extends edges
		if m[2] != "" {
			for _, base := range splitCSV(m[2]) {
				entity.Edges = append(entity.Edges, EntityEdge{
					Target:   base,
					Relation: "extends",
				})
			}
		}

		entities = append(entities, entity)
	}
	return entities
}

// ---------------------------------------------------------------------------
// Function extraction
// ---------------------------------------------------------------------------

func extractFunctions(cleanedLines, origLines []string, absPath, relPath, moduleID string) []SourceEntity {
	var entities []SourceEntity
	seen := map[string]bool{}

	addFunc := func(name string, lineNum int) {
		if seen[name] {
			return
		}
		seen[name] = true

		endLine := findBlockEnd(origLines, cleanedLines, lineNum)
		id := moduleID + "/" + name
		src := makeSourceRef(absPath, relPath, lineNum, endLine)
		ctx := extractContext(origLines, lineNum, endLine)
		summary := jsDocBefore(origLines, lineNum)
		if summary == "" {
			summary = fmt.Sprintf("Function %s in module %s", name, filepath.Base(relPath))
		}
		entities = append(entities, SourceEntity{
			ID:      id,
			Type:    "function",
			Name:    name,
			Summary: summary,
			Context: ctx,
			Source:  src,
		})
	}

	for i, cl := range cleanedLines {
		// Standard function declarations.
		if m := reFunction.FindStringSubmatch(cl); m != nil {
			addFunc(m[1], i+1)
			continue
		}
		// Arrow / function-expression assignments at top level (no leading whitespace).
		if m := reArrow.FindStringSubmatch(cl); m != nil {
			// Only consider top-level: line must start with optional export then const/let/var.
			if !strings.HasPrefix(strings.TrimSpace(cl), "//") {
				addFunc(m[1], i+1)
			}
		}
	}
	return entities
}

// ---------------------------------------------------------------------------
// Type alias extraction
// ---------------------------------------------------------------------------

func extractTypes(cleanedLines, origLines []string, absPath, relPath, moduleID string) []SourceEntity {
	var entities []SourceEntity
	for i, cl := range cleanedLines {
		m := reType.FindStringSubmatch(cl)
		if m == nil {
			continue
		}
		name := m[1]
		lineNum := i + 1

		// Type aliases can span multiple lines; look for a semicolon or find
		// block end if it's a mapped/conditional type with braces.
		endLine := findTypeEnd(origLines, lineNum)

		id := moduleID + "/" + name
		src := makeSourceRef(absPath, relPath, lineNum, endLine)
		ctx := extractContext(origLines, lineNum, endLine)
		summary := jsDocBefore(origLines, lineNum)
		if summary == "" {
			summary = fmt.Sprintf("Type %s in module %s", name, filepath.Base(relPath))
		}

		entities = append(entities, SourceEntity{
			ID:      id,
			Type:    "type",
			Name:    name,
			Summary: summary,
			Context: ctx,
			Source:  src,
		})
	}
	return entities
}

// ---------------------------------------------------------------------------
// Helpers: block/scope detection
// ---------------------------------------------------------------------------

// findBlockEnd scans forward from startLine (1-indexed) counting braces to
// locate the matching closing brace. Returns the 1-indexed line of the closing
// brace, or startLine if no opening brace is found.
// maskedLines is used for brace depth tracking (strings/comments masked out)
// while origLines determines the total line count for fallback.
func findBlockEnd(origLines []string, maskedLines []string, startLine int) int {
	depth := 0
	foundOpen := false
	for i := startLine - 1; i < len(maskedLines); i++ {
		for _, ch := range maskedLines[i] {
			if ch == '{' {
				depth++
				foundOpen = true
			}
			if ch == '}' {
				depth--
				if foundOpen && depth == 0 {
					return i + 1 // 1-indexed
				}
			}
		}
	}
	if !foundOpen {
		return startLine
	}
	return len(origLines) // fallback: ran off the end
}

// findTypeEnd locates the end of a type alias which may or may not use braces.
// It looks for the first semicolon at brace-depth 0 after the start line, or
// falls back to brace tracking if braces are present.
func findTypeEnd(lines []string, startLine int) int {
	depth := 0
	for i := startLine - 1; i < len(lines); i++ {
		for _, ch := range lines[i] {
			if ch == '{' {
				depth++
			}
			if ch == '}' {
				depth--
			}
			if ch == ';' && depth <= 0 {
				return i + 1
			}
		}
	}
	return startLine
}

// ---------------------------------------------------------------------------
// Helpers: source ref, context, summary
// ---------------------------------------------------------------------------

func makeSourceRef(absPath, relPath string, startLine, endLine int) SourceRef {
	hash, _ := verify.ComputeSourceHash(absPath, [2]int{startLine, endLine})
	return SourceRef{
		Path:  absPath,
		Lines: [2]int{startLine, endLine},
		Hash:  hash,
	}
}

// extractContext returns the source code from startLine to endLine (1-indexed,
// inclusive). If the block is very large, it truncates to a reasonable limit.
func extractContext(lines []string, startLine, endLine int) string {
	if startLine < 1 {
		startLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > endLine {
		return ""
	}
	block := lines[startLine-1 : endLine]

	const maxContextLines = 80
	if len(block) > maxContextLines {
		truncated := make([]string, maxContextLines+1)
		copy(truncated, block[:maxContextLines])
		truncated[maxContextLines] = "// ... truncated"
		return strings.Join(truncated, "\n")
	}
	return strings.Join(block, "\n")
}

// truncateContext limits a string to approximately n bytes for module context.
func truncateContext(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n// ... truncated"
}

// jsDocBefore searches backwards from lineNum (1-indexed) for a JSDoc
// comment (/** ... */) and returns the first line of its text content.
func jsDocBefore(lines []string, lineNum int) string {
	if lineNum < 2 {
		return ""
	}

	// Walk backwards from the line before the declaration looking for the end
	// of a JSDoc block. We accept at most 20 lines of gap/comments.
	var blockLines []string
	inBlock := false
	for i := lineNum - 2; i >= 0 && i >= lineNum-22; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if !inBlock {
			if strings.HasSuffix(trimmed, "*/") {
				inBlock = true
				blockLines = append(blockLines, lines[i])
			} else if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "export") {
				// Allow blank lines, single-line comments, decorators, and
				// export keywords between the JSDoc and the declaration.
				continue
			} else {
				break
			}
		} else {
			blockLines = append(blockLines, lines[i])
			if strings.Contains(trimmed, "/**") {
				break
			}
		}
	}

	if len(blockLines) == 0 {
		return ""
	}

	// Reverse blockLines (they were collected bottom-up).
	for i, j := 0, len(blockLines)-1; i < j; i, j = i+1, j-1 {
		blockLines[i], blockLines[j] = blockLines[j], blockLines[i]
	}

	raw := strings.Join(blockLines, "\n")
	m := reJSDocBlock.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	// Clean up the content: strip leading * and whitespace per line.
	var parts []string
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "*")
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "@") {
			parts = append(parts, line)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

// firstComment returns the text of the first single-line comment or JSDoc
// block found in the source lines, used as a module summary fallback.
func firstComment(lines []string) string {
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "//") {
			text := strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))
			if text != "" && !strings.HasPrefix(text, "!") {
				return text
			}
		}
		if strings.HasPrefix(trimmed, "/**") {
			// Attempt inline JSDoc: /** summary */
			m := reJSDocBlock.FindStringSubmatch(trimmed)
			if m != nil {
				cleaned := strings.TrimSpace(strings.Trim(m[1], "* "))
				if cleaned != "" {
					return cleaned
				}
			}
		}
		// Skip blank lines, shebangs, and 'use strict'.
		if trimmed == "" || strings.HasPrefix(trimmed, "#!") || strings.HasPrefix(trimmed, "'use strict'") || strings.HasPrefix(trimmed, "\"use strict\"") {
			continue
		}
		// If we hit actual code before any comment, stop.
		if !strings.HasPrefix(trimmed, "/*") && !strings.HasPrefix(trimmed, "*") {
			break
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Helpers: string/comment masking
// ---------------------------------------------------------------------------

// maskStringsAndComments replaces the contents of string literals, template
// literals, and comments with spaces so that regex extraction does not match
// tokens inside them. Line structure (number of newlines) is preserved.
func maskStringsAndComments(src string) string {
	out := make([]byte, len(src))
	i := 0
	for i < len(src) {
		ch := src[i]

		// Single-line comment.
		if ch == '/' && i+1 < len(src) && src[i+1] == '/' {
			out[i] = ' '
			out[i+1] = ' '
			i += 2
			for i < len(src) && src[i] != '\n' {
				out[i] = ' '
				i++
			}
			continue
		}

		// Block comment.
		if ch == '/' && i+1 < len(src) && src[i+1] == '*' {
			out[i] = ' '
			out[i+1] = ' '
			i += 2
			for i < len(src) {
				if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					break
				}
				if src[i] == '\n' {
					out[i] = '\n' // preserve line count
				} else {
					out[i] = ' '
				}
				i++
			}
			continue
		}

		// Template literal.
		if ch == '`' {
			out[i] = ' '
			i++
			for i < len(src) && src[i] != '`' {
				if src[i] == '\\' && i+1 < len(src) {
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					continue
				}
				if src[i] == '\n' {
					out[i] = '\n'
				} else {
					out[i] = ' '
				}
				i++
			}
			if i < len(src) {
				out[i] = ' '
				i++
			}
			continue
		}

		// Single or double quoted string.
		if ch == '\'' || ch == '"' {
			quote := ch
			out[i] = ' '
			i++
			for i < len(src) && src[i] != quote && src[i] != '\n' {
				if src[i] == '\\' && i+1 < len(src) {
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					continue
				}
				out[i] = ' '
				i++
			}
			if i < len(src) && src[i] == quote {
				out[i] = ' '
				i++
			}
			continue
		}

		// Regular expression literal — simple heuristic: after = or ( or , or ;
		// or line start, a / that is not /* or //.
		if ch == '/' && i > 0 && isRegexPreceder(src, i) {
			out[i] = ' '
			i++
			for i < len(src) && src[i] != '/' && src[i] != '\n' {
				if src[i] == '\\' && i+1 < len(src) {
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					continue
				}
				out[i] = ' '
				i++
			}
			if i < len(src) && src[i] == '/' {
				out[i] = ' '
				i++
				// Consume flags.
				for i < len(src) && (src[i] >= 'a' && src[i] <= 'z') {
					out[i] = ' '
					i++
				}
			}
			continue
		}

		out[i] = ch
		i++
	}
	return string(out)
}

// isRegexPreceder checks whether the character before position i suggests that
// a / at position i starts a regex literal rather than a division operator.
func isRegexPreceder(src string, i int) bool {
	// Walk backwards over whitespace.
	j := i - 1
	for j >= 0 && (src[j] == ' ' || src[j] == '\t') {
		j--
	}
	if j < 0 {
		return true
	}
	ch := src[j]
	return ch == '=' || ch == '(' || ch == ',' || ch == ';' || ch == ':' ||
		ch == '!' || ch == '&' || ch == '|' || ch == '?' || ch == '{' ||
		ch == '[' || ch == '\n' || ch == '+' || ch == '-'
}

// ---------------------------------------------------------------------------
// Helpers: ID derivation
// ---------------------------------------------------------------------------

// deriveModuleID produces the entity ID for a module. It strips the file
// extension from relPath and optionally prepends a namespace.
func deriveModuleID(relPath string, namespace string) string {
	normalized := filepath.ToSlash(relPath)
	// Strip extension.
	ext := filepath.Ext(normalized)
	if ext != "" {
		normalized = normalized[:len(normalized)-len(ext)]
	}
	if namespace != "" {
		return namespace + "/" + normalized
	}
	return normalized
}

// ---------------------------------------------------------------------------
// Helpers: misc
// ---------------------------------------------------------------------------

// tsSplitLines splits text by newline, preserving blank lines.
func tsSplitLines(s string) []string {
	if s == "" {
		return nil
	}
	scanner := bufio.NewScanner(strings.NewReader(s))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// splitCSV splits a comma-separated list and trims whitespace from each item.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
