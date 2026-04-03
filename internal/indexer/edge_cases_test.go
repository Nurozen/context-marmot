package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ═══════════════════════════════════════════════════════════════════════════════
// 1. Go Indexer Edge Cases
// ═══════════════════════════════════════════════════════════════════════════════

func TestEdge_Go_PackageClauseOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "only_pkg.go", "package onlypkg\n")

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "only_pkg.go"), "only_pkg.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || len(result.Entities) == 0 {
		t.Fatal("expected at least a file entity")
	}
	fileEnt := result.Entities[0]
	if fileEnt.Type != "file" {
		t.Errorf("expected Type=file, got %s", fileEnt.Type)
	}
	if fileEnt.Name != "only_pkg.go" {
		t.Errorf("expected Name=only_pkg.go, got %s", fileEnt.Name)
	}
	// Should only have the file entity, no functions/types
	if len(result.Entities) != 1 {
		t.Errorf("expected 1 entity (file), got %d", len(result.Entities))
	}
}

func TestEdge_Go_InitFunction(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

func init() {
	fmt.Println("init")
}

func ExportedFunc() {}
`
	writeFile(t, dir, "init.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "init.go"), "init.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that init is extracted (it's unexported but should still be indexed)
	var foundInit, foundExported bool
	for _, e := range result.Entities {
		if e.Name == "init" {
			foundInit = true
		}
		if e.Name == "ExportedFunc" {
			foundExported = true
		}
	}
	// The code indexes all functions (even unexported) for call edges
	if !foundInit {
		t.Error("init() function was not extracted")
	}
	if !foundExported {
		t.Error("ExportedFunc was not extracted")
	}
}

func TestEdge_Go_BidirectionalCalls(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

func Alpha() {
	Beta()
}

func Beta() {
	Alpha()
}
`
	writeFile(t, dir, "calls.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "calls.go"), "calls.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find Alpha and Beta
	var alphaEdges, betaEdges []EntityEdge
	for _, e := range result.Entities {
		if e.Name == "Alpha" {
			alphaEdges = e.Edges
		}
		if e.Name == "Beta" {
			betaEdges = e.Edges
		}
	}

	// Alpha should have a "calls" edge to Beta
	foundAlphaToBeta := false
	for _, e := range alphaEdges {
		if e.Relation == "calls" && e.Target == "calls/Beta" {
			foundAlphaToBeta = true
		}
	}
	if !foundAlphaToBeta {
		t.Error("Alpha should have a 'calls' edge to Beta")
	}

	// Beta should have a "calls" edge to Alpha
	foundBetaToAlpha := false
	for _, e := range betaEdges {
		if e.Relation == "calls" && e.Target == "calls/Alpha" {
			foundBetaToAlpha = true
		}
	}
	if !foundBetaToAlpha {
		t.Error("Beta should have a 'calls' edge to Alpha")
	}
}

func TestEdge_Go_PointerVsValueReceiver(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

type MyStruct struct{}

func (s MyStruct) ValueMethod() {}
func (s *MyStruct) PointerMethod() {}
`
	writeFile(t, dir, "receivers.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "receivers.go"), "receivers.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var foundValue, foundPointer bool
	for _, e := range result.Entities {
		if e.Name == "ValueMethod" && e.Type == "method" {
			foundValue = true
			if !strings.Contains(e.ID, "MyStruct.ValueMethod") {
				t.Errorf("ValueMethod ID should contain MyStruct.ValueMethod, got %s", e.ID)
			}
		}
		if e.Name == "PointerMethod" && e.Type == "method" {
			foundPointer = true
			if !strings.Contains(e.ID, "MyStruct.PointerMethod") {
				t.Errorf("PointerMethod ID should contain MyStruct.PointerMethod, got %s", e.ID)
			}
		}
	}

	if !foundValue {
		t.Error("value receiver method not found")
	}
	if !foundPointer {
		t.Error("pointer receiver method not found")
	}
}

func TestEdge_Go_EmbeddedStructFields(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

type Base struct{}

type Derived struct {
	Base
	Name string
}
`
	writeFile(t, dir, "embed.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "embed.go"), "embed.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Derived should have an "extends" edge to Base
	var derived *SourceEntity
	for i, e := range result.Entities {
		if e.Name == "Derived" {
			derived = &result.Entities[i]
		}
	}
	if derived == nil {
		t.Fatal("Derived type not found")
	}

	foundExtends := false
	for _, edge := range derived.Edges {
		if edge.Relation == "extends" && edge.Target == "embed/Base" {
			foundExtends = true
		}
	}
	if !foundExtends {
		t.Error("Derived should have an 'extends' edge to Base")
	}
}

func TestEdge_Go_BuildConstraints(t *testing.T) {
	dir := t.TempDir()
	src := `//go:build linux

package mypkg

func LinuxOnly() {}
`
	writeFile(t, dir, "build_tag.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "build_tag.go"), "build_tag.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || len(result.Entities) == 0 {
		t.Fatal("expected entities from file with build constraints")
	}
	// Should still parse the file and extract the function
	foundFunc := false
	for _, e := range result.Entities {
		if e.Name == "LinuxOnly" {
			foundFunc = true
		}
	}
	if !foundFunc {
		t.Error("LinuxOnly function not extracted from build-constrained file")
	}
}

func TestEdge_Go_TypeAlias(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

type Foo = string
type Bar = int
`
	writeFile(t, dir, "alias.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "alias.go"), "alias.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Type aliases should be extracted via the default case in extractTypeSpec
	var foundFoo, foundBar bool
	for _, e := range result.Entities {
		if e.Name == "Foo" && e.Type == "type" {
			foundFoo = true
		}
		if e.Name == "Bar" && e.Type == "type" {
			foundBar = true
		}
	}
	if !foundFoo {
		t.Error("type alias Foo not extracted")
	}
	if !foundBar {
		t.Error("type alias Bar not extracted")
	}
}

func TestEdge_Go_LargeFunction(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	sb.WriteString("package mypkg\n\nfunc BigFunc() {\n")
	for i := 0; i < 1005; i++ {
		sb.WriteString(fmt.Sprintf("\t_ = %d\n", i))
	}
	sb.WriteString("}\n")
	writeFile(t, dir, "large.go", sb.String())

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "large.go"), "large.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var bigFunc *SourceEntity
	for i, e := range result.Entities {
		if e.Name == "BigFunc" {
			bigFunc = &result.Entities[i]
		}
	}
	if bigFunc == nil {
		t.Fatal("BigFunc not found")
	}

	// Line range should cover the entire function
	if bigFunc.Source.Lines[0] != 3 {
		t.Errorf("expected start line 3, got %d", bigFunc.Source.Lines[0])
	}
	// 3 (func line) + 1005 body lines + 1 closing brace = line 1009
	expectedEnd := 3 + 1005 + 1 // line 1009
	if bigFunc.Source.Lines[1] != expectedEnd {
		t.Errorf("expected end line %d, got %d", expectedEnd, bigFunc.Source.Lines[1])
	}
}

func TestEdge_Go_NoPackageDoc(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

func Hello() {}
`
	writeFile(t, dir, "nodoc.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "nodoc.go"), "nodoc.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pkg := result.Entities[0]
	if pkg.Type != "file" {
		t.Fatal("first entity should be file")
	}
	// Summary should still be reasonable
	if !strings.Contains(pkg.Summary, "mypkg") {
		t.Errorf("expected summary to contain 'mypkg', got %q", pkg.Summary)
	}
}

func TestEdge_Go_MultipleFilesInSamePackage(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkg")
	os.MkdirAll(pkgDir, 0o755)

	writeFile(t, pkgDir, "a.go", "package pkg\n\nfunc FuncA() {}\n")
	writeFile(t, pkgDir, "b.go", "package pkg\n\nfunc FuncB() {}\n")

	idx := NewGoIndexer()

	resultA, err := idx.IndexFile(filepath.Join(pkgDir, "a.go"), "pkg/a.go", "")
	if err != nil {
		t.Fatalf("indexing a.go: %v", err)
	}
	resultB, err := idx.IndexFile(filepath.Join(pkgDir, "b.go"), "pkg/b.go", "")
	if err != nil {
		t.Fatalf("indexing b.go: %v", err)
	}

	// Each file produces a file entity with its own unique ID
	fileA := resultA.Entities[0]
	fileB := resultB.Entities[0]

	if fileA.ID != "pkg/a" {
		t.Errorf("expected fileA.ID=pkg/a, got %s", fileA.ID)
	}
	if fileB.ID != "pkg/b" {
		t.Errorf("expected fileB.ID=pkg/b, got %s", fileB.ID)
	}
	// Each Go file now produces a unique file entity, avoiding ID collisions.
}

func TestEdge_Go_InterfaceImplements(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

type Sayer interface {
	Say() string
}

type Dog struct{}

func (d Dog) Say() string { return "woof" }
`
	writeFile(t, dir, "iface.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "iface.go"), "iface.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var dog *SourceEntity
	for i, e := range result.Entities {
		if e.Name == "Dog" && e.Type == "type" {
			dog = &result.Entities[i]
		}
	}
	if dog == nil {
		t.Fatal("Dog type not found")
	}

	foundImplements := false
	for _, edge := range dog.Edges {
		if edge.Relation == "implements" && edge.Target == "iface/Sayer" {
			foundImplements = true
		}
	}
	if !foundImplements {
		t.Error("Dog should implement Sayer interface")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 2. TypeScript Indexer Edge Cases
// ═══════════════════════════════════════════════════════════════════════════════

func TestEdge_TS_TemplateLiteralWithDeclarations(t *testing.T) {
	dir := t.TempDir()
	src := "const template = `\nfunction fakeFunc() {\n  class FakeClass {}\n}\n`;\n\nexport function realFunc() {\n  return template;\n}\n"
	writeFile(t, dir, "template.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "template.ts"), "template.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should extract realFunc but NOT fakeFunc or FakeClass from inside template literal
	var foundReal, foundFake, foundFakeClass bool
	for _, e := range result.Entities {
		if e.Name == "realFunc" {
			foundReal = true
		}
		if e.Name == "fakeFunc" {
			foundFake = true
		}
		if e.Name == "FakeClass" {
			foundFakeClass = true
		}
	}
	if !foundReal {
		t.Error("realFunc should be extracted")
	}
	if foundFake {
		t.Error("fakeFunc inside template literal should NOT be extracted")
	}
	if foundFakeClass {
		t.Error("FakeClass inside template literal should NOT be extracted")
	}
}

func TestEdge_TS_NestedClasses(t *testing.T) {
	dir := t.TempDir()
	src := `export class Outer {
  innerMethod() {
    class Inner {
      innerInnerMethod() {}
    }
    return new Inner();
  }
}
`
	writeFile(t, dir, "nested.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "nested.ts"), "nested.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Outer class should be found
	var foundOuter bool
	for _, e := range result.Entities {
		if e.Name == "Outer" && e.Type == "class" {
			foundOuter = true
		}
	}
	if !foundOuter {
		t.Error("Outer class should be extracted")
	}
}

func TestEdge_TS_ExportDefaultClassNoName(t *testing.T) {
	dir := t.TempDir()
	src := `export default class {
  method() {
    return 42;
  }
}
`
	writeFile(t, dir, "default.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "default.ts"), "default.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not crash; default class with no name may not be extracted by regex
	// which requires a name group, and that's OK
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// At minimum, the module entity should exist
	if len(result.Entities) == 0 {
		t.Fatal("at least module entity expected")
	}
}

func TestEdge_TS_Decorators(t *testing.T) {
	dir := t.TempDir()
	src := `@Component({
  selector: 'app-root',
  template: '<h1>Hello</h1>'
})
export class AppComponent {
  title = 'app';
}
`
	writeFile(t, dir, "component.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "component.ts"), "component.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var foundClass bool
	for _, e := range result.Entities {
		if e.Name == "AppComponent" {
			foundClass = true
		}
	}
	if !foundClass {
		t.Error("decorated class AppComponent should be extracted")
	}
}

func TestEdge_TS_DynamicImports(t *testing.T) {
	dir := t.TempDir()
	src := `const mod = await import('./lazy-module');
import { Foo } from './bar';

export function main() {
  return mod;
}
`
	writeFile(t, dir, "dynamic.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "dynamic.ts"), "dynamic.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Static import from './bar' should be found
	module := result.Entities[0]
	foundBar := false
	for _, edge := range module.Edges {
		if edge.Relation == "imports" && strings.Contains(edge.Target, "bar") {
			foundBar = true
		}
	}
	if !foundBar {
		t.Error("static import from './bar' should produce an imports edge")
	}
}

func TestEdge_TS_ReExports(t *testing.T) {
	dir := t.TempDir()
	src := `export { foo } from './bar';
export { default as baz } from './qux';
`
	writeFile(t, dir, "reexport.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "reexport.ts"), "reexport.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	module := result.Entities[0]
	importTargets := make(map[string]bool)
	for _, edge := range module.Edges {
		if edge.Relation == "imports" {
			importTargets[edge.Target] = true
		}
	}
	// Re-exports should be treated as imports
	foundBar := false
	foundQux := false
	for target := range importTargets {
		if strings.Contains(target, "bar") {
			foundBar = true
		}
		if strings.Contains(target, "qux") {
			foundQux = true
		}
	}
	if !foundBar {
		t.Error("re-export from './bar' should produce imports edge")
	}
	if !foundQux {
		t.Error("re-export from './qux' should produce imports edge")
	}
}

func TestEdge_TS_TypeOnlyImports(t *testing.T) {
	dir := t.TempDir()
	src := `import type { Foo } from './types';

export function useFoo(f: Foo): void {}
`
	writeFile(t, dir, "typeimport.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "typeimport.ts"), "typeimport.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	module := result.Entities[0]
	foundImport := false
	for _, edge := range module.Edges {
		if edge.Relation == "imports" && strings.Contains(edge.Target, "types") {
			foundImport = true
		}
	}
	if !foundImport {
		t.Error("type-only import should still be extracted as an imports edge")
	}
}

func TestEdge_TS_EmptyTSXFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "empty.tsx", "")

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "empty.tsx"), "empty.tsx", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty file should return empty result
	if len(result.Entities) != 0 {
		t.Errorf("expected 0 entities for empty tsx file, got %d", len(result.Entities))
	}
}

func TestEdge_TS_MultilineStringWithFunctionKeyword(t *testing.T) {
	dir := t.TempDir()
	src := "const str = `\nThis is a multiline string.\nIt has the word function in it.\nAnd also class and interface.\n`;\n\nexport function realFunction() {\n  return str;\n}\n"
	writeFile(t, dir, "multiline.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "multiline.ts"), "multiline.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	funcCount := 0
	for _, e := range result.Entities {
		if e.Type == "function" {
			funcCount++
		}
	}
	// Only realFunction should be extracted
	if funcCount != 1 {
		t.Errorf("expected 1 function (realFunction), got %d", funcCount)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 3. Generic Indexer Edge Cases
// ═══════════════════════════════════════════════════════════════════════════════

func TestEdge_Generic_PythonRelativeImport(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkg", "sub")
	os.MkdirAll(pkgDir, 0o755)

	src := `from . import foo
from ..utils import helper
`
	writeFile(t, pkgDir, "mod.py", src)

	idx := NewGenericIndexer()
	result, err := idx.IndexFile(filepath.Join(pkgDir, "mod.py"), "pkg/sub/mod.py", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Entities) == 0 {
		t.Fatal("expected at least one entity")
	}

	entity := result.Entities[0]
	importTargets := make(map[string]bool)
	for _, edge := range entity.Edges {
		if edge.Relation == "imports" {
			importTargets[edge.Target] = true
		}
	}

	// from . import foo -> resolves to pkg/sub (current package)
	// from ..utils import helper -> resolves to pkg/utils
	foundRelative := false
	for target := range importTargets {
		if strings.Contains(target, "pkg") {
			foundRelative = true
		}
	}
	if !foundRelative {
		t.Errorf("expected relative imports to resolve, got targets: %v", importTargets)
	}
}

func TestEdge_Generic_Shebang(t *testing.T) {
	dir := t.TempDir()
	src := `#!/usr/bin/env python3
# A simple script
import sys

def main():
    print("hello")
`
	writeFile(t, dir, "script.py", src)

	idx := NewGenericIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "script.py"), "script.py", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entity := result.Entities[0]
	// Summary should include shebang info
	if !strings.Contains(entity.Summary, "#!/usr/bin/env python3") {
		t.Errorf("expected summary to include shebang, got %q", entity.Summary)
	}
}

func TestEdge_Generic_Exactly100Lines(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	sb.WriteString("# File with exactly 100 lines\n")
	for i := 2; i <= 100; i++ {
		sb.WriteString(fmt.Sprintf("line_%d = %d\n", i, i))
	}
	writeFile(t, dir, "hundred.py", sb.String())

	idx := NewGenericIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "hundred.py"), "hundred.py", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entity := result.Entities[0]
	// Context should include all 100 lines (not truncated)
	contextLines := strings.Split(entity.Context, "\n")
	if len(contextLines) != 100 {
		t.Errorf("expected 100 context lines (exact boundary), got %d", len(contextLines))
	}
}

func TestEdge_Generic_101Lines_ShouldCap(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	sb.WriteString("# File with 101 lines\n")
	for i := 2; i <= 101; i++ {
		sb.WriteString(fmt.Sprintf("line_%d = %d\n", i, i))
	}
	writeFile(t, dir, "hundredone.py", sb.String())

	idx := NewGenericIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "hundredone.py"), "hundredone.py", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entity := result.Entities[0]
	// Context should be capped at 100 lines
	contextLines := strings.Split(entity.Context, "\n")
	if len(contextLines) > 100 {
		t.Errorf("expected context capped at 100 lines, got %d", len(contextLines))
	}
	// Source lines should still reflect the full 101 lines
	if entity.Source.Lines[1] != 101 {
		t.Errorf("expected source end line 101, got %d", entity.Source.Lines[1])
	}
}

func TestEdge_Generic_RustUse(t *testing.T) {
	dir := t.TempDir()
	src := `use crate::config;
use std::io::Read;

fn main() {
    println!("hello");
}
`
	writeFile(t, dir, "main.rs", src)

	idx := NewGenericIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "main.rs"), "main.rs", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entity := result.Entities[0]
	importTargets := make(map[string]bool)
	for _, edge := range entity.Edges {
		if edge.Relation == "imports" {
			importTargets[edge.Target] = true
		}
	}

	if !importTargets["crate::config"] {
		t.Errorf("expected 'crate::config' import, got targets: %v", importTargets)
	}
	if !importTargets["std::io::Read"] {
		t.Errorf("expected 'std::io::Read' import, got targets: %v", importTargets)
	}
}

func TestEdge_Generic_RubyRequireRelative(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "lib")
	os.MkdirAll(subDir, 0o755)

	src := `require_relative 'helper'
require 'json'

class MyClass
  def initialize
    @data = {}
  end
end
`
	writeFile(t, subDir, "main.rb", src)

	idx := NewGenericIndexer()
	result, err := idx.IndexFile(filepath.Join(subDir, "main.rb"), "lib/main.rb", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entity := result.Entities[0]
	importTargets := make(map[string]bool)
	for _, edge := range entity.Edges {
		if edge.Relation == "imports" {
			importTargets[edge.Target] = true
		}
	}

	if !importTargets["json"] {
		t.Errorf("expected 'json' import, got: %v", importTargets)
	}
	// require_relative 'helper' in lib/ -> lib/helper
	if !importTargets["lib/helper"] {
		t.Errorf("expected 'lib/helper' resolved import, got: %v", importTargets)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 4. Ignore Matcher Edge Cases
// ═══════════════════════════════════════════════════════════════════════════════

func TestEdge_Ignore_WindowsLineEndings(t *testing.T) {
	dir := t.TempDir()
	// Write .gitignore with \r\n line endings
	content := "*.log\r\nbuild/\r\n*.tmp\r\n"
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644)

	m := NewIgnoreMatcher(dir, nil)

	if !m.ShouldIgnore("test.log", false) {
		t.Error("*.log pattern should ignore test.log even with \\r\\n line endings")
	}
	if !m.ShouldIgnore("test.tmp", false) {
		t.Error("*.tmp pattern should ignore test.tmp even with \\r\\n line endings")
	}
}

func TestEdge_Ignore_TrailingWhitespace(t *testing.T) {
	dir := t.TempDir()
	content := "*.log   \nbuild/  \n"
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644)

	m := NewIgnoreMatcher(dir, nil)

	if !m.ShouldIgnore("test.log", false) {
		t.Error("*.log with trailing spaces should still work")
	}
	if !m.ShouldIgnore("build", true) {
		t.Error("build/ with trailing spaces should still work")
	}
}

func TestEdge_Ignore_LeadingSlashPattern(t *testing.T) {
	dir := t.TempDir()
	content := "/build\n"
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644)

	m := NewIgnoreMatcher(dir, nil)

	// /build should match at root only
	if !m.ShouldIgnore("build", true) {
		t.Error("/build should match 'build' at root")
	}
	// Should NOT match nested build
	if m.ShouldIgnore("src/build", true) {
		t.Error("/build should NOT match 'src/build' (nested)")
	}
}

func TestEdge_Ignore_EmptyGitignore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(""), 0o644)

	m := NewIgnoreMatcher(dir, nil)

	// Should not crash; no patterns means nothing extra is ignored
	if m.ShouldIgnore("test.go", false) {
		t.Error("empty .gitignore should not ignore any files")
	}
}

func TestEdge_Ignore_NonExistentGitignore(t *testing.T) {
	dir := t.TempDir()
	// No .gitignore file

	m := NewIgnoreMatcher(dir, nil)

	// Should not crash
	if m.ShouldIgnore("test.go", false) {
		t.Error("non-existent .gitignore should not ignore any files")
	}
	// Built-in ignores should still work
	if !m.ShouldIgnore("node_modules", true) {
		t.Error("node_modules should be always-ignored")
	}
}

func TestEdge_Ignore_AlwaysIgnore(t *testing.T) {
	dir := t.TempDir()
	m := NewIgnoreMatcher(dir, nil)

	for _, d := range []string{".git", "node_modules", "vendor", "__pycache__", ".marmot"} {
		if !m.ShouldIgnore(d, true) {
			t.Errorf("%s should be always-ignored", d)
		}
	}
	if !m.ShouldIgnore(".DS_Store", false) {
		t.Error(".DS_Store should be always-ignored")
	}
}

func TestEdge_Ignore_NegationPattern(t *testing.T) {
	dir := t.TempDir()
	content := "*.log\n!important.log\n"
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644)

	m := NewIgnoreMatcher(dir, nil)

	if !m.ShouldIgnore("debug.log", false) {
		t.Error("debug.log should be ignored by *.log")
	}
	if m.ShouldIgnore("important.log", false) {
		t.Error("important.log should NOT be ignored due to negation")
	}
}

func TestEdge_Ignore_DoubleStarPattern(t *testing.T) {
	dir := t.TempDir()
	content := "**/test_data\n"
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644)

	m := NewIgnoreMatcher(dir, nil)

	if !m.ShouldIgnore("test_data", true) {
		t.Error("**/test_data should match test_data at root")
	}
	if !m.ShouldIgnore("pkg/test_data", true) {
		t.Error("**/test_data should match pkg/test_data")
	}
	if !m.ShouldIgnore("a/b/c/test_data", true) {
		t.Error("**/test_data should match deeply nested test_data")
	}
}

func TestEdge_Ignore_DirOnlyPattern(t *testing.T) {
	dir := t.TempDir()
	content := "logs/\n"
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644)

	m := NewIgnoreMatcher(dir, nil)

	// Should ignore directories named "logs"
	if !m.ShouldIgnore("logs", true) {
		t.Error("logs/ pattern should ignore directories named 'logs'")
	}
	// Should NOT ignore files named "logs"
	if m.ShouldIgnore("logs", false) {
		t.Error("logs/ pattern should NOT ignore files named 'logs'")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// 5. Runner Edge Cases
// ═══════════════════════════════════════════════════════════════════════════════

func TestEdge_Runner_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	registry := NewDefaultRegistry()
	ns := newMockNodeStore()
	es := newMockEmbedStore()
	emb := &mockEmbedder{}

	runner := NewRunner(
		RunnerConfig{SrcDir: dir, Namespace: "test"},
		registry, ns, es, emb, nil, nil,
	)

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("expected 0 total for empty dir, got %d", result.Total)
	}
	if result.Errors != 0 {
		t.Errorf("expected 0 errors for empty dir, got %d", result.Errors)
	}
}

func TestEdge_Runner_NonExistentDirectory(t *testing.T) {
	registry := NewDefaultRegistry()
	ns := newMockNodeStore()
	es := newMockEmbedStore()
	emb := &mockEmbedder{}

	runner := NewRunner(
		RunnerConfig{SrcDir: "/nonexistent/path/that/does/not/exist", Namespace: "test"},
		registry, ns, es, emb, nil, nil,
	)

	_, err := runner.Run(context.Background())
	if err == nil {
		t.Error("expected error for non-existent directory")
	}
}

func TestEdge_Runner_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	// Create many files to increase chance of hitting cancellation
	for i := 0; i < 50; i++ {
		writeFile(t, dir, fmt.Sprintf("file_%d.py", i),
			fmt.Sprintf("# file %d\ndef func_%d():\n    pass\n", i, i))
	}

	registry := NewDefaultRegistry()
	ns := newMockNodeStore()
	es := newMockEmbedStore()
	emb := &mockEmbedder{}

	runner := NewRunner(
		RunnerConfig{SrcDir: dir, Namespace: "test"},
		registry, ns, es, emb, nil, nil,
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := runner.Run(ctx)
	// Should either return the cancellation error or process partially
	if err != nil {
		if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("expected context canceled error, got: %v", err)
		}
	}
}

func TestEdge_Runner_FileWithEmptyResult(t *testing.T) {
	dir := t.TempDir()
	// An empty .tsx file produces empty result from TypeScript indexer
	writeFile(t, dir, "empty.tsx", "")
	// Add a real file too
	writeFile(t, dir, "real.py", "# hello\ndef func():\n    pass\n")

	registry := NewDefaultRegistry()
	ns := newMockNodeStore()
	es := newMockEmbedStore()
	emb := &mockEmbedder{}

	runner := NewRunner(
		RunnerConfig{SrcDir: dir, Namespace: "test"},
		registry, ns, es, emb, nil, nil,
	)

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// real.py should have produced an entity; empty.tsx should not
	if result.Total == 0 {
		t.Error("expected at least one entity from real.py")
	}
	if result.Errors != 0 {
		t.Errorf("expected 0 errors, got %d", result.Errors)
	}
}

func TestEdge_Runner_DeeplyNestedDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := dir
	for i := 0; i < 10; i++ {
		nested = filepath.Join(nested, fmt.Sprintf("level_%d", i))
	}
	os.MkdirAll(nested, 0o755)
	writeFile(t, nested, "deep.py", "# deeply nested\ndef deep_func():\n    pass\n")

	registry := NewDefaultRegistry()
	ns := newMockNodeStore()
	es := newMockEmbedStore()
	emb := &mockEmbedder{}

	runner := NewRunner(
		RunnerConfig{SrcDir: dir, Namespace: "test"},
		registry, ns, es, emb, nil, nil,
	)

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total == 0 {
		t.Error("expected at least one entity from deeply nested file")
	}
}

func TestEdge_Runner_IncrementalDeletedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keep.py", "# keep\ndef keep():\n    pass\n")
	deletePath := writeFile(t, dir, "delete.py", "# delete\ndef delete():\n    pass\n")

	registry := NewDefaultRegistry()
	ns := newMockNodeStore()
	es := newMockEmbedStore()
	emb := &mockEmbedder{}

	// First run
	runner := NewRunner(
		RunnerConfig{SrcDir: dir, Namespace: "test", Incremental: false},
		registry, ns, es, emb, nil, nil,
	)
	_, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}

	// Delete a file
	os.Remove(deletePath)

	// Second run (incremental)
	runner2 := NewRunner(
		RunnerConfig{SrcDir: dir, Namespace: "test", Incremental: true},
		registry, ns, es, emb, nil, nil,
	)
	result2, err := runner2.Run(context.Background())
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}
	// The deleted file's node is still in the store but the runner should not
	// error on it. The run should proceed with remaining files.
	_ = result2
}

// ═══════════════════════════════════════════════════════════════════════════════
// 6. Race Condition Tests — Run with -race flag
// ═══════════════════════════════════════════════════════════════════════════════

func TestEdge_Race_ConcurrentGoIndexer(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		src := fmt.Sprintf("package pkg%d\n\nfunc Func%d() {}\n", i, i)
		writeFile(t, dir, fmt.Sprintf("file_%d.go", i), src)
	}

	// Create multiple indexers — but GoIndexer has shared modulePath state
	idx := NewGoIndexer()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			path := filepath.Join(dir, fmt.Sprintf("file_%d.go", n))
			relPath := fmt.Sprintf("file_%d.go", n)
			_, err := idx.IndexFile(path, relPath, "")
			if err != nil {
				t.Errorf("concurrent indexing error: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestEdge_Race_ConcurrentTSIndexer(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		src := fmt.Sprintf("export function func%d() { return %d; }\n", i, i)
		writeFile(t, dir, fmt.Sprintf("file_%d.ts", i), src)
	}

	idx := NewTypeScriptIndexer()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			path := filepath.Join(dir, fmt.Sprintf("file_%d.ts", n))
			relPath := fmt.Sprintf("file_%d.ts", n)
			_, err := idx.IndexFile(path, relPath, "")
			if err != nil {
				t.Errorf("concurrent indexing error: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestEdge_Race_ConcurrentGenericIndexer(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		src := fmt.Sprintf("# file %d\ndef func_%d():\n    pass\n", i, i)
		writeFile(t, dir, fmt.Sprintf("file_%d.py", i), src)
	}

	idx := NewGenericIndexer()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			path := filepath.Join(dir, fmt.Sprintf("file_%d.py", n))
			relPath := fmt.Sprintf("file_%d.py", n)
			_, err := idx.IndexFile(path, relPath, "")
			if err != nil {
				t.Errorf("concurrent indexing error: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestEdge_Race_ConcurrentRegistryLookup(t *testing.T) {
	reg := NewDefaultRegistry()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			exts := []string{".go", ".ts", ".py", ".rs", ".rb", ".js", ".unknown"}
			ext := exts[n%len(exts)]
			_, _ = reg.IndexerFor(ext)
		}(i)
	}
	wg.Wait()
}

func TestEdge_Race_ConcurrentRunner(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, dir, fmt.Sprintf("file_%d.py", i),
			fmt.Sprintf("# file %d\ndef func_%d():\n    pass\n", i, i))
	}

	registry := NewDefaultRegistry()

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ns := newMockNodeStore()
			es := newMockEmbedStore()
			emb := &mockEmbedder{}
			runner := NewRunner(
				RunnerConfig{SrcDir: dir, Namespace: "test"},
				registry, ns, es, emb, nil, nil,
			)
			_, err := runner.Run(context.Background())
			if err != nil {
				t.Errorf("concurrent runner error: %v", err)
			}
		}()
	}
	wg.Wait()
}

// ═══════════════════════════════════════════════════════════════════════════════
// Additional Edge Cases
// ═══════════════════════════════════════════════════════════════════════════════

func TestEdge_Go_EmptyInterfaceImplements(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

type Empty interface{}

type Anything struct{}
`
	writeFile(t, dir, "empty_iface.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "empty_iface.go"), "empty_iface.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Empty interface has no methods, so implementsInterface check with
	// len(ifaceMethods) == 0 should be skipped — Anything should NOT get
	// an implements edge.
	var anything *SourceEntity
	for i, e := range result.Entities {
		if e.Name == "Anything" {
			anything = &result.Entities[i]
		}
	}
	if anything == nil {
		t.Fatal("Anything type not found")
	}
	for _, edge := range anything.Edges {
		if edge.Relation == "implements" && edge.Target == "Empty" {
			t.Error("Anything should NOT implement Empty (empty interface check is skipped)")
		}
	}
}

func TestEdge_Go_SyntaxError(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

func Broken( {
	this is not valid go
}
`
	writeFile(t, dir, "syntax.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "syntax.go"), "syntax.go", "")
	// Should return gracefully, not crash
	if err != nil {
		t.Fatalf("should not error on syntax error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// Result might be empty for unparseable files
}

func TestEdge_TS_ClassExtendsAndImplements(t *testing.T) {
	dir := t.TempDir()
	src := `export class Animal {
  name: string;
  speak() { return "..."; }
}

export interface Swimmer {
  swim(): void;
}

export interface Runner {
  run(): void;
}

export class Dog extends Animal implements Swimmer, Runner {
  swim() { console.log("swimming"); }
  run() { console.log("running"); }
}
`
	writeFile(t, dir, "multi.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "multi.ts"), "multi.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var dog *SourceEntity
	for i, e := range result.Entities {
		if e.Name == "Dog" {
			dog = &result.Entities[i]
		}
	}
	if dog == nil {
		t.Fatal("Dog class not found")
	}

	hasExtends := false
	implementsTargets := make(map[string]bool)
	for _, edge := range dog.Edges {
		if edge.Relation == "extends" && edge.Target == "Animal" {
			hasExtends = true
		}
		if edge.Relation == "implements" {
			implementsTargets[edge.Target] = true
		}
	}
	if !hasExtends {
		t.Error("Dog should extend Animal")
	}
	if !implementsTargets["Swimmer"] {
		t.Error("Dog should implement Swimmer")
	}
	if !implementsTargets["Runner"] {
		t.Error("Dog should implement Runner")
	}
}

func TestEdge_Generic_BinaryFile(t *testing.T) {
	dir := t.TempDir()
	// Write a file with null bytes (binary)
	binaryContent := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x00, 0x00}
	os.WriteFile(filepath.Join(dir, "image.py"), binaryContent, 0o644)

	idx := NewGenericIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "image.py"), "image.py", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Entities) != 0 {
		t.Error("binary file should produce no entities")
	}
}

func TestEdge_Ignore_CommentLines(t *testing.T) {
	dir := t.TempDir()
	content := "# This is a comment\n*.log\n# Another comment\n*.tmp\n"
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644)

	m := NewIgnoreMatcher(dir, nil)

	if !m.ShouldIgnore("test.log", false) {
		t.Error("*.log should work even with comment lines in .gitignore")
	}
	if !m.ShouldIgnore("test.tmp", false) {
		t.Error("*.tmp should work even with comment lines in .gitignore")
	}
	if m.ShouldIgnore("test.go", false) {
		t.Error("test.go should not be ignored")
	}
}

func TestEdge_Ignore_ExtraPatterns(t *testing.T) {
	dir := t.TempDir()
	// No .gitignore, but extra patterns
	m := NewIgnoreMatcher(dir, []string{"*.tmp", "build/"})

	if !m.ShouldIgnore("test.tmp", false) {
		t.Error("extra pattern *.tmp should be applied")
	}
	if !m.ShouldIgnore("build", true) {
		t.Error("extra pattern build/ should be applied")
	}
}

func TestEdge_Ignore_NestedPathComponent(t *testing.T) {
	dir := t.TempDir()
	m := NewIgnoreMatcher(dir, nil)

	// Always-ignore patterns should match nested path components
	if !m.ShouldIgnore("deep/nested/node_modules/pkg/file.js", false) {
		t.Error("node_modules in nested path should be ignored")
	}
	if !m.ShouldIgnore("a/.git/config", false) {
		t.Error(".git in nested path should be ignored")
	}
}

func TestEdge_Go_MethodOnGenericType(t *testing.T) {
	dir := t.TempDir()
	src := `package mypkg

type Stack[T any] struct {
	items []T
}

func (s *Stack[T]) Push(item T) {
	s.items = append(s.items, item)
}

func (s *Stack[T]) Pop() T {
	var zero T
	if len(s.items) == 0 {
		return zero
	}
	item := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return item
}
`
	writeFile(t, dir, "generic.go", src)

	idx := NewGoIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "generic.go"), "generic.go", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var foundPush, foundPop bool
	for _, e := range result.Entities {
		if e.Name == "Push" && e.Type == "method" {
			foundPush = true
			if !strings.Contains(e.ID, "Stack.Push") {
				t.Errorf("Push ID should contain Stack.Push, got %s", e.ID)
			}
		}
		if e.Name == "Pop" && e.Type == "method" {
			foundPop = true
		}
	}
	if !foundPush {
		t.Error("Push method on generic type not found")
	}
	if !foundPop {
		t.Error("Pop method on generic type not found")
	}
}

func TestEdge_ExtractLines_BoundaryConditions(t *testing.T) {
	lines := []string{"line1", "line2", "line3"}

	// start < 1 should be clamped
	if got := extractLines(lines, 0, 3); got != "line1\nline2\nline3" {
		t.Errorf("start<1: got %q", got)
	}

	// end > len should be clamped
	if got := extractLines(lines, 1, 100); got != "line1\nline2\nline3" {
		t.Errorf("end>len: got %q", got)
	}

	// start > end should return empty
	if got := extractLines(lines, 3, 1); got != "" {
		t.Errorf("start>end: got %q", got)
	}

	// start > len should return empty
	if got := extractLines(lines, 10, 20); got != "" {
		t.Errorf("start>len: got %q", got)
	}

	// empty lines slice
	if got := extractLines(nil, 1, 1); got != "" {
		t.Errorf("nil lines: got %q", got)
	}
}

func TestEdge_DocFirstSentence_EdgeCases(t *testing.T) {
	// Test the internal helper
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{"empty", "", ""},
		{"dot only", ".", ""},
		{"sentence", "Hello world.", "Hello world"},
		{"no period", "No period here", "No period here"},
		{"newline terminates", "First line\nSecond line.", "First line"},
	}
	// We can't directly test docFirstSentence since it takes *ast.CommentGroup
	// but we can verify behavior through the indexer output
	_ = tests
}

func TestEdge_SplitLines_EdgeCases(t *testing.T) {
	if got := splitLines(""); len(got) != 0 {
		t.Errorf("empty string: got %d lines", len(got))
	}
	if got := splitLines("one"); len(got) != 1 || got[0] != "one" {
		t.Errorf("single line: got %v", got)
	}
	if got := splitLines("a\nb\nc"); len(got) != 3 {
		t.Errorf("three lines: got %d lines", len(got))
	}
}

func TestEdge_MaskStringsAndComments(t *testing.T) {
	src := `const x = "function fake()";
const y = 'class Fake {}';
const z = ` + "`template ${literal}`" + `;
// function commentFunc()
/* class BlockComment {} */
export function real() {}
`
	masked := maskStringsAndComments(src)
	lines := tsSplitLines(masked)

	// The real function should survive masking
	foundReal := false
	for _, line := range lines {
		if strings.Contains(line, "function real") {
			foundReal = true
		}
		// Fake declarations inside strings/comments should be masked
		if strings.Contains(line, "function fake") {
			t.Error("function fake() inside string should be masked")
		}
		if strings.Contains(line, "class Fake") {
			t.Error("class Fake inside string should be masked")
		}
		if strings.Contains(line, "function commentFunc") {
			t.Error("function in single-line comment should be masked")
		}
		if strings.Contains(line, "class BlockComment") {
			t.Error("class in block comment should be masked")
		}
	}
	if !foundReal {
		t.Error("real function should survive masking")
	}
}

func TestEdge_DeriveModuleID(t *testing.T) {
	tests := []struct {
		relPath   string
		namespace string
		want      string
	}{
		{"src/foo.ts", "", "src/foo"},
		{"src/foo.ts", "ns", "ns/src/foo"},
		{"bar.js", "", "bar"},
		{"nested/deep/file.tsx", "myns", "myns/nested/deep/file"},
	}
	for _, tt := range tests {
		got := deriveModuleID(tt.relPath, tt.namespace)
		if got != tt.want {
			t.Errorf("deriveModuleID(%q, %q) = %q, want %q", tt.relPath, tt.namespace, got, tt.want)
		}
	}
}

func TestEdge_ResolveRelativeImport(t *testing.T) {
	tests := []struct {
		dir        string
		importPath string
		want       string
	}{
		{"pkg/sub", ".foo", "pkg/sub/foo"},
		{"pkg/sub", "..utils", "pkg/utils"},
		{"pkg", ".module", "pkg/module"},
		{".", ".foo", "foo"},
	}
	for _, tt := range tests {
		got := resolveRelativeImport(tt.dir, tt.importPath)
		if got != tt.want {
			t.Errorf("resolveRelativeImport(%q, %q) = %q, want %q", tt.dir, tt.importPath, got, tt.want)
		}
	}
}

func TestEdge_PathWithoutExt(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"foo.py", "foo"},
		{"pkg/mod.py", "pkg/mod"},
		{"no_ext", "no_ext"},
		{"deep/nested/file.rb", "deep/nested/file"},
	}
	for _, tt := range tests {
		got := pathWithoutExt(tt.in)
		if got != tt.want {
			t.Errorf("pathWithoutExt(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEdge_Runner_OnlyGoFiles(t *testing.T) {
	dir := t.TempDir()
	// Create a go.mod so the Go indexer can resolve the module path
	writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.21\n")
	writeFile(t, dir, "main.go", `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`)

	registry := NewDefaultRegistry()
	ns := newMockNodeStore()
	es := newMockEmbedStore()
	emb := &mockEmbedder{}

	runner := NewRunner(
		RunnerConfig{SrcDir: dir, Namespace: "test"},
		registry, ns, es, emb, nil, nil,
	)

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total == 0 {
		t.Error("expected entities from Go file")
	}
	if result.Errors != 0 {
		t.Errorf("expected 0 errors, got %d", result.Errors)
	}
}

func TestEdge_Registry_UnknownExtension(t *testing.T) {
	reg := NewDefaultRegistry()

	// Unknown extension should fall back to generic indexer
	idx, ok := reg.IndexerFor(".unknown_ext_xyz")
	if !ok {
		t.Error("unknown extension should fall back to generic indexer")
	}
	if idx.Name() != "generic" {
		t.Errorf("expected generic indexer for unknown ext, got %s", idx.Name())
	}
}

func TestEdge_Registry_GoExtension(t *testing.T) {
	reg := NewDefaultRegistry()

	idx, ok := reg.IndexerFor(".go")
	if !ok {
		t.Error(".go should have an indexer")
	}
	if idx.Name() != "go" {
		t.Errorf("expected 'go' indexer for .go, got %s", idx.Name())
	}
}

func TestEdge_Registry_TSExtensions(t *testing.T) {
	reg := NewDefaultRegistry()

	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
		idx, ok := reg.IndexerFor(ext)
		if !ok {
			t.Errorf("%s should have an indexer", ext)
			continue
		}
		if idx.Name() != "typescript" {
			t.Errorf("expected 'typescript' indexer for %s, got %s", ext, idx.Name())
		}
	}
}

func TestEdge_TS_JSDocSummary(t *testing.T) {
	dir := t.TempDir()
	src := `/**
 * Handles user authentication and session management.
 * @module auth
 */

/**
 * Validates user credentials against the database.
 * @param username - The user's login name
 * @param password - The user's password
 */
export function validateUser(username: string, password: string): boolean {
  return true;
}
`
	writeFile(t, dir, "auth.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "auth.ts"), "auth.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var validateFunc *SourceEntity
	for i, e := range result.Entities {
		if e.Name == "validateUser" {
			validateFunc = &result.Entities[i]
		}
	}
	if validateFunc == nil {
		t.Fatal("validateUser function not found")
	}

	// Should have JSDoc as summary
	if !strings.Contains(validateFunc.Summary, "Validates user credentials") {
		t.Errorf("expected JSDoc summary, got %q", validateFunc.Summary)
	}
}

func TestEdge_TS_InterfaceExtends(t *testing.T) {
	dir := t.TempDir()
	src := `export interface Base {
  id: string;
}

export interface Derived extends Base {
  name: string;
}
`
	writeFile(t, dir, "iface.ts", src)

	idx := NewTypeScriptIndexer()
	result, err := idx.IndexFile(filepath.Join(dir, "iface.ts"), "iface.ts", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var derived *SourceEntity
	for i, e := range result.Entities {
		if e.Name == "Derived" {
			derived = &result.Entities[i]
		}
	}
	if derived == nil {
		t.Fatal("Derived interface not found")
	}

	hasExtends := false
	for _, edge := range derived.Edges {
		if edge.Relation == "extends" && edge.Target == "Base" {
			hasExtends = true
		}
	}
	if !hasExtends {
		t.Error("Derived should extend Base")
	}
}
