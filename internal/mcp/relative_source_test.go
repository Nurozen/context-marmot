package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRelativeSourcePath_InsideProjectRoot verifies that HandleContextWrite
// converts an absolute source.path inside the project root to a relative path.
func TestRelativeSourcePath_InsideProjectRoot(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// The project root is the parent of MarmotDir (which is a temp dir).
	projectRoot := filepath.Dir(eng.MarmotDir)

	// Create a real source file inside the project root.
	srcFile := filepath.Join(projectRoot, "src", "handler.go")
	if err := os.MkdirAll(filepath.Dir(srcFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcFile, []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "test/relsrc",
		"type":    "function",
		"summary": "Relative source test",
		"source": map[string]any{
			"path": srcFile, // absolute path inside project root
			"hash": "abc123",
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("write error: %s", resultText(t, res))
	}

	var wr WriteResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &wr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Check that the node in the graph has a RELATIVE source path.
	n, ok := eng.Graph.GetNode("test/relsrc")
	if !ok {
		t.Fatal("node not found in graph")
	}
	if filepath.IsAbs(n.Source.Path) {
		t.Errorf("expected relative source.path, got absolute: %q", n.Source.Path)
	}
	// The relative path should be "src/handler.go" (relative to projectRoot).
	want := filepath.Join("src", "handler.go")
	if n.Source.Path != want {
		t.Errorf("source.path = %q, want %q", n.Source.Path, want)
	}
}

// TestRelativeSourcePath_OutsideProjectRoot verifies that HandleContextWrite
// keeps an absolute source.path that is OUTSIDE the project root unchanged.
func TestRelativeSourcePath_OutsideProjectRoot(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Create a source file in a directory that is truly outside the project
	// root. t.TempDir() siblings share a parent with MarmotDir so we create
	// a nested temp dir under a completely separate base to guarantee the
	// relative path will start with "..".
	outsideDir, err := os.MkdirTemp("", "outside-project-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(outsideDir) })

	srcFile := filepath.Join(outsideDir, "external.go")
	if err := os.WriteFile(srcFile, []byte("package ext\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "test/extsrc",
		"type":    "function",
		"summary": "External source test",
		"source": map[string]any{
			"path": srcFile, // absolute path OUTSIDE project root
			"hash": "def456",
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("write error: %s", resultText(t, res))
	}

	// Check that the node kept the absolute path (filepath.Rel would produce ".." prefix).
	n, ok := eng.Graph.GetNode("test/extsrc")
	if !ok {
		t.Fatal("node not found in graph")
	}
	// The path should remain absolute because it is outside the project root.
	if !filepath.IsAbs(n.Source.Path) {
		t.Errorf("expected absolute source.path for outside path, got: %q", n.Source.Path)
	}
	if n.Source.Path != srcFile {
		t.Errorf("source.path = %q, want %q", n.Source.Path, srcFile)
	}
}

// TestRelativeSourcePath_AlreadyRelative verifies that a path that is already
// relative passes through unchanged.
func TestRelativeSourcePath_AlreadyRelative(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeReq := makeCallToolRequest("context_write", map[string]any{
		"id":      "test/alreadyrel",
		"type":    "function",
		"summary": "Already relative test",
		"source": map[string]any{
			"path": "internal/pkg/foo.go",
			"hash": "ghi789",
		},
	})

	res, err := eng.HandleContextWrite(ctx, writeReq)
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("write error: %s", resultText(t, res))
	}

	n, ok := eng.Graph.GetNode("test/alreadyrel")
	if !ok {
		t.Fatal("node not found in graph")
	}
	if n.Source.Path != "internal/pkg/foo.go" {
		t.Errorf("relative path should be unchanged, got %q", n.Source.Path)
	}
	if strings.HasPrefix(n.Source.Path, "/") {
		t.Errorf("path should stay relative, got %q", n.Source.Path)
	}
}
