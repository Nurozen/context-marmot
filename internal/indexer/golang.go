package indexer

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nurozen/context-marmot/internal/verify"
)

// GoIndexer extracts entities from Go source files using the standard library
// AST parser.
type GoIndexer struct {
	modulePath string    // go.mod module path for import resolution
	moduleOnce sync.Once // protects lazy resolution of modulePath
}

// NewGoIndexer creates a GoIndexer. The module path is resolved lazily on
// first use from the go.mod file in the source root.
func NewGoIndexer() *GoIndexer {
	return &GoIndexer{}
}

// Name returns the human-readable name for this indexer.
func (g *GoIndexer) Name() string { return "go" }

// SupportedExtensions returns the file extensions this indexer handles.
func (g *GoIndexer) SupportedExtensions() []string { return []string{".go"} }

// IndexFile parses a single Go source file and extracts entities.
// It produces a file-level entity (type "file") instead of a package entity,
// avoiding ID collisions when multiple files belong to the same Go package.
func (g *GoIndexer) IndexFile(path string, relPath string, namespace string) (*IndexResult, error) {
	// Ensure module path is resolved (thread-safe lazy init).
	g.moduleOnce.Do(func() {
		g.modulePath = resolveModulePath(path)
	})

	// Read file content for Context extraction.
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	lines := splitLines(string(content))

	// Parse the file.
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, content, parser.ParseComments)
	if err != nil {
		// Skip unparseable files gracefully.
		return &IndexResult{}, nil
	}

	relDir := filepath.Dir(relPath)
	if relDir == "." {
		relDir = ""
	}
	relDir = filepath.ToSlash(relDir)

	pkgName := file.Name.Name

	// Compute file entity ID from the relative path without .go extension.
	// e.g., "internal/auth/user.go" → "internal/auth/user"
	//       "main.go" → "main"
	fileBase := strings.TrimSuffix(filepath.Base(relPath), ".go")
	var fileID string
	if relDir == "" {
		fileID = fileBase
	} else {
		fileID = relDir + "/" + fileBase
	}

	result := &IndexResult{}

	// Collect interfaces in this file for implements detection.
	interfaceMethods := make(map[string]map[string]bool) // iface name -> method set
	// Collect type methods for implements detection.
	typeMethods := make(map[string]map[string]bool) // type name -> method set

	// First pass: collect interfaces and type declarations for implements edges.
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if iface, ok := ts.Type.(*ast.InterfaceType); ok {
				methods := make(map[string]bool)
				if iface.Methods != nil {
					for _, m := range iface.Methods.List {
						for _, n := range m.Names {
							methods[n.Name] = true
						}
					}
				}
				interfaceMethods[ts.Name.Name] = methods
			}
		}
	}

	// Collect methods on types (from function declarations with receivers).
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		recvType := receiverTypeName(fd.Recv.List[0].Type)
		if recvType == "" {
			continue
		}
		if typeMethods[recvType] == nil {
			typeMethods[recvType] = make(map[string]bool)
		}
		typeMethods[recvType][fd.Name.Name] = true
	}

	// Build import map: alias/package name -> import path.
	importMap := buildImportMap(file)

	// --- File entity (replaces the old package entity) ---
	pkgDoc := docFirstSentence(file.Doc)
	fileName := filepath.Base(relPath)
	fileSummary := fmt.Sprintf("Go file %s in package %s", fileName, pkgName)
	if pkgDoc != "" {
		fileSummary += " — " + pkgDoc
	}

	fileEntity := SourceEntity{
		ID:      fileID,
		Type:    "file",
		Name:    fileName,
		Summary: fileSummary,
		Source: SourceRef{
			Path:  path,
			Lines: [2]int{1, len(lines)},
		},
	}

	// Add import edges from file node.
	for _, imp := range file.Imports {
		impPath := strings.Trim(imp.Path.Value, `"`)
		target := resolveImportTarget(impPath, g.modulePath)
		fileEntity.Edges = append(fileEntity.Edges, EntityEdge{
			Target:   target,
			Relation: "imports",
		})
	}

	// Compute file source hash (entire file).
	if h, err := verify.ComputeSourceHash(path, [2]int{0, 0}); err == nil {
		fileEntity.Source.Hash = h
	}

	// --- Process declarations ---
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			entity := g.extractFunc(d, fset, path, fileID, lines, importMap)
			if entity != nil {
				fileEntity.Edges = append(fileEntity.Edges, EntityEdge{
					Target:   entity.ID,
					Relation: "contains",
				})
				result.Entities = append(result.Entities, *entity)
			}

		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				entities := g.extractTypeSpec(ts, d, fset, path, fileID, lines, interfaceMethods, typeMethods)
				for _, entity := range entities {
					fileEntity.Edges = append(fileEntity.Edges, EntityEdge{
						Target:   entity.ID,
						Relation: "contains",
					})
					result.Entities = append(result.Entities, entity)
				}
			}
		}
	}

	result.Entities = append([]SourceEntity{fileEntity}, result.Entities...)
	return result, nil
}

// extractFunc extracts a function or method entity from a FuncDecl.
// fileID is the file entity ID (e.g., "internal/auth/user" or "main") used as
// the prefix for all child entity IDs.
func (g *GoIndexer) extractFunc(
	fd *ast.FuncDecl,
	fset *token.FileSet,
	path string,
	fileID string,
	lines []string,
	importMap map[string]string,
) *SourceEntity {
	if fd.Name == nil {
		return nil
	}

	startLine := fset.Position(fd.Pos()).Line
	endLine := fset.Position(fd.End()).Line
	name := fd.Name.Name

	isMethod := fd.Recv != nil && len(fd.Recv.List) > 0
	var recvName string
	var entityType, entityID string

	if isMethod {
		recvName = receiverTypeName(fd.Recv.List[0].Type)
		entityType = "method"
		entityID = fmt.Sprintf("%s/%s.%s", fileID, recvName, name)
	} else {
		entityType = "function"
		entityID = fmt.Sprintf("%s/%s", fileID, name)
	}

	doc := docFirstSentence(fd.Doc)
	var summary string
	if isMethod {
		summary = fmt.Sprintf("Method %s.%s", recvName, name)
	} else {
		summary = fmt.Sprintf("Function %s", name)
	}
	if doc != "" {
		summary += " — " + doc
	}

	ctx := extractLines(lines, startLine, endLine)

	entity := &SourceEntity{
		ID:      entityID,
		Type:    entityType,
		Name:    name,
		Summary: summary,
		Context: ctx,
		Source: SourceRef{
			Path:  path,
			Lines: [2]int{startLine, endLine},
		},
	}

	// Compute source hash.
	if h, err := verify.ComputeSourceHash(path, [2]int{startLine, endLine}); err == nil {
		entity.Source.Hash = h
	}

	// Extract call edges from the function body.
	if fd.Body != nil {
		calls := extractCallEdges(fd.Body, fileID, importMap, g.modulePath)
		entity.Edges = append(entity.Edges, calls...)
	}

	return entity
}

// extractTypeSpec extracts a type (struct or interface) entity.
// fileID is the file entity ID used as prefix for all child entity IDs.
func (g *GoIndexer) extractTypeSpec(
	ts *ast.TypeSpec,
	gd *ast.GenDecl,
	fset *token.FileSet,
	path string,
	fileID string,
	lines []string,
	interfaceMethods map[string]map[string]bool,
	typeMethods map[string]map[string]bool,
) []SourceEntity {
	name := ts.Name.Name
	startLine := fset.Position(ts.Pos()).Line
	endLine := fset.Position(ts.End()).Line

	// Use the GenDecl position if it has a doc comment (covers the full type block).
	if gd.Doc != nil {
		declStart := fset.Position(gd.Pos()).Line
		if declStart < startLine {
			startLine = declStart
		}
	}

	var entities []SourceEntity

	switch t := ts.Type.(type) {
	case *ast.StructType:
		entityID := fmt.Sprintf("%s/%s", fileID, name)

		doc := docFirstSentence(gd.Doc)
		summary := fmt.Sprintf("Type %s", name)
		if doc != "" {
			summary += " — " + doc
		}

		ctx := extractLines(lines, startLine, endLine)

		entity := SourceEntity{
			ID:      entityID,
			Type:    "type",
			Name:    name,
			Summary: summary,
			Context: ctx,
			Source: SourceRef{
				Path:  path,
				Lines: [2]int{startLine, endLine},
			},
		}

		// Compute source hash.
		if h, err := verify.ComputeSourceHash(path, [2]int{startLine, endLine}); err == nil {
			entity.Source.Hash = h
		}

		// Add contains edges for methods on this type.
		if methods, ok := typeMethods[name]; ok {
			for methodName := range methods {
				methodID := fmt.Sprintf("%s/%s.%s", fileID, name, methodName)
				entity.Edges = append(entity.Edges, EntityEdge{
					Target:   methodID,
					Relation: "contains",
				})
			}
		}

		// Add extends edges for embedded types.
		if t.Fields != nil {
			for _, field := range t.Fields.List {
				if len(field.Names) == 0 {
					// Anonymous field = embedded type.
					embeddedName := typeExprName(field.Type)
					if embeddedName != "" {
						embeddedID := fmt.Sprintf("%s/%s", fileID, embeddedName)
						entity.Edges = append(entity.Edges, EntityEdge{
							Target:   embeddedID,
							Relation: "extends",
						})
					}
				}
			}
		}

		// Check for implements edges (best-effort same-package).
		if methods, ok := typeMethods[name]; ok {
			for ifaceName, ifaceMethods := range interfaceMethods {
				if len(ifaceMethods) == 0 {
					continue
				}
				if implementsInterface(methods, ifaceMethods) {
					ifaceID := fmt.Sprintf("%s/%s", fileID, ifaceName)
					entity.Edges = append(entity.Edges, EntityEdge{
						Target:   ifaceID,
						Relation: "implements",
					})
				}
			}
		}

		entities = append(entities, entity)

	case *ast.InterfaceType:
		entityID := fmt.Sprintf("%s/%s", fileID, name)

		doc := docFirstSentence(gd.Doc)
		summary := fmt.Sprintf("Interface %s", name)
		if doc != "" {
			summary += " — " + doc
		}

		ctx := extractLines(lines, startLine, endLine)

		entity := SourceEntity{
			ID:      entityID,
			Type:    "interface",
			Name:    name,
			Summary: summary,
			Context: ctx,
			Source: SourceRef{
				Path:  path,
				Lines: [2]int{startLine, endLine},
			},
		}

		// Compute source hash.
		if h, err := verify.ComputeSourceHash(path, [2]int{startLine, endLine}); err == nil {
			entity.Source.Hash = h
		}

		_ = t // suppress unused warning
		entities = append(entities, entity)

	default:
		// Other type kinds (aliases, etc.) — extract as generic type.
		entityID := fmt.Sprintf("%s/%s", fileID, name)

		doc := docFirstSentence(gd.Doc)
		summary := fmt.Sprintf("Type %s", name)
		if doc != "" {
			summary += " — " + doc
		}

		ctx := extractLines(lines, startLine, endLine)

		entity := SourceEntity{
			ID:      entityID,
			Type:    "type",
			Name:    name,
			Summary: summary,
			Context: ctx,
			Source: SourceRef{
				Path:  path,
				Lines: [2]int{startLine, endLine},
			},
		}

		if h, err := verify.ComputeSourceHash(path, [2]int{startLine, endLine}); err == nil {
			entity.Source.Hash = h
		}

		entities = append(entities, entity)
	}

	return entities
}

// goBuiltins is the set of Go built-in functions that should not generate call
// edges, since they are language primitives rather than user-defined functions.
var goBuiltins = map[string]bool{
	"append": true, "cap": true, "clear": true, "close": true,
	"complex": true, "copy": true, "delete": true, "imag": true,
	"len": true, "make": true, "max": true, "min": true,
	"new": true, "panic": true, "print": true, "println": true,
	"real": true, "recover": true,
}

// extractCallEdges walks AST call expressions in a block statement and returns
// call edges to other entities. fileID is the file entity ID used as prefix
// for same-file call targets.
func extractCallEdges(body *ast.BlockStmt, fileID string, importMap map[string]string, modulePath string) []EntityEdge {
	var edges []EntityEdge
	seen := make(map[string]bool)

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		var target string

		switch fn := call.Fun.(type) {
		case *ast.Ident:
			// Direct call: foo() — skip Go built-in functions.
			if goBuiltins[fn.Name] {
				return true
			}
			target = fmt.Sprintf("%s/%s", fileID, fn.Name)

		case *ast.SelectorExpr:
			// Selector call: pkg.Foo() or obj.Method()
			if ident, ok := fn.X.(*ast.Ident); ok {
				pkgAlias := ident.Name
				funcName := fn.Sel.Name

				// Try to resolve via import map.
				if impPath, ok := importMap[pkgAlias]; ok {
					resolved := resolveImportTarget(impPath, modulePath)
					target = fmt.Sprintf("%s/%s", resolved, funcName)
				} else {
					// Might be a method call on a local variable — best effort.
					target = fmt.Sprintf("%s/%s.%s", fileID, pkgAlias, funcName)
				}
			}
		}

		if target != "" && !seen[target] {
			seen[target] = true
			edges = append(edges, EntityEdge{
				Target:   target,
				Relation: "calls",
			})
		}

		return true
	})

	return edges
}

// buildImportMap creates a mapping from package alias (or last path component)
// to the full import path.
func buildImportMap(file *ast.File) map[string]string {
	m := make(map[string]string)
	for _, imp := range file.Imports {
		impPath := strings.Trim(imp.Path.Value, `"`)
		var alias string
		if imp.Name != nil {
			alias = imp.Name.Name
		} else {
			// Use last component of the path.
			parts := strings.Split(impPath, "/")
			alias = parts[len(parts)-1]
		}
		if alias != "." && alias != "_" {
			m[alias] = impPath
		}
	}
	return m
}

// resolveImportTarget converts a full import path to a node ID. If the import
// path starts with the project module path, it strips the module prefix to get
// a relative path. External imports are returned as-is.
func resolveImportTarget(impPath string, modulePath string) string {
	if modulePath != "" && strings.HasPrefix(impPath, modulePath+"/") {
		return strings.TrimPrefix(impPath, modulePath+"/")
	}
	return impPath
}

// resolveModulePath finds and parses go.mod to extract the module path.
// It searches upward from the given file path.
func resolveModulePath(filePath string) string {
	dir := filepath.Dir(filePath)
	for {
		gomod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			return parseModulePath(string(data))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// parseModulePath extracts the module path from go.mod content.
func parseModulePath(content string) string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// receiverTypeName extracts the type name from a method receiver expression,
// handling both value (*T) and pointer (T) receivers.
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		// Generic receiver T[P]
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		// Generic receiver T[P, Q]
		return receiverTypeName(t.X)
	default:
		return ""
	}
}

// typeExprName extracts the type name from a type expression (for embedded fields).
func typeExprName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return typeExprName(t.X)
	case *ast.SelectorExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name + "." + t.Sel.Name
		}
		return t.Sel.Name
	default:
		return ""
	}
}

// docFirstSentence extracts the first sentence from a doc comment group.
// Returns empty string if the comment group is nil.
func docFirstSentence(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	text := cg.Text()
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Find the first sentence terminator.
	for i, r := range text {
		if r == '.' || r == '\n' {
			sentence := strings.TrimSpace(text[:i+1])
			// Don't return a dot-only "sentence".
			if sentence == "." {
				continue
			}
			return strings.TrimSuffix(sentence, ".")
		}
	}

	// No period found — return the whole first line, trimmed.
	if idx := strings.Index(text, "\n"); idx >= 0 {
		return strings.TrimSpace(text[:idx])
	}
	return text
}

// implementsInterface checks if the given type's method set is a superset of
// the interface's method set. This is a best-effort check limited to the
// same package.
func implementsInterface(typeMethods, ifaceMethods map[string]bool) bool {
	for method := range ifaceMethods {
		if !typeMethods[method] {
			return false
		}
	}
	return true
}

// splitLines splits content into individual lines.
func splitLines(content string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// extractLines returns lines[start-1:end] joined with newlines (1-indexed, inclusive).
func extractLines(lines []string, start, end int) string {
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end || start > len(lines) {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}
