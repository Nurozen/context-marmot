package verify

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// ResolveSourcePath unit tests
// ---------------------------------------------------------------------------

func TestResolveSourcePath_AbsoluteReturnsAsIs(t *testing.T) {
	abs := "/home/user/project/src/main.go"
	got := ResolveSourcePath(abs, "/some/other/root")
	if got != abs {
		t.Errorf("expected absolute path returned as-is, got %q", got)
	}
}

func TestResolveSourcePath_RelativeJoinsWithProjectRoot(t *testing.T) {
	rel := "src/main.go"
	root := "/home/user/project"
	got := ResolveSourcePath(rel, root)
	want := filepath.Join(root, rel)
	if got != want {
		t.Errorf("ResolveSourcePath(%q, %q) = %q, want %q", rel, root, got, want)
	}
}

func TestResolveSourcePath_EmptyProjectRootReturnsAsIs(t *testing.T) {
	rel := "src/main.go"
	got := ResolveSourcePath(rel, "")
	if got != rel {
		t.Errorf("expected path returned as-is with empty projectRoot, got %q", got)
	}
}

func TestResolveSourcePath_TraversalDoesNotEscapeProjectRoot(t *testing.T) {
	root := "/home/user/project"
	malicious := "../../etc/passwd"
	got := ResolveSourcePath(malicious, root)
	// Should return the raw relative path (not resolved to /etc/passwd)
	if got != malicious {
		t.Errorf("expected traversal path returned as-is %q, got %q", malicious, got)
	}
	if filepath.IsAbs(got) {
		t.Errorf("traversal path must not resolve to an absolute path outside projectRoot, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// VerifyStaleness with relative path
// ---------------------------------------------------------------------------

func TestVerifyStaleness_RelativePath(t *testing.T) {
	// Create a temp project structure: projectRoot/src/main.go
	projectRoot := t.TempDir()
	srcDir := filepath.Join(projectRoot, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "main.go")
	content := "package main\nfunc main() {}\n"
	if err := os.WriteFile(srcFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Compute the hash of the real file (using absolute path).
	absHash, err := ComputeSourceHash(srcFile, [2]int{0, 0})
	if err != nil {
		t.Fatal(err)
	}

	// Create a node with a RELATIVE source.path.
	n := &node.Node{
		ID: "test/relative-staleness",
		Source: node.Source{
			Path: "src/main.go", // relative
			Hash: absHash,
		},
	}

	// Verify staleness with the projectRoot -- should NOT be stale.
	status, err := VerifyStaleness(n, projectRoot)
	if err != nil {
		t.Fatalf("VerifyStaleness: %v", err)
	}
	if status.IsStale {
		t.Errorf("should not be stale; stored=%s current=%s", status.StoredHash, status.CurrentHash)
	}

	// Now modify the file -- should become stale.
	if err := os.WriteFile(srcFile, []byte("package main\nfunc changed() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err = VerifyStaleness(n, projectRoot)
	if err != nil {
		t.Fatalf("VerifyStaleness after change: %v", err)
	}
	if !status.IsStale {
		t.Error("should be stale after file modification")
	}
}

// ---------------------------------------------------------------------------
// VerifyStaleness with absolute path (backward compatibility)
// ---------------------------------------------------------------------------

func TestVerifyStaleness_AbsolutePath_BackwardCompat(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "source.go")
	content := "package pkg\n"
	if err := os.WriteFile(srcFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	hash, err := ComputeSourceHash(srcFile, [2]int{0, 0})
	if err != nil {
		t.Fatal(err)
	}

	// Node with an ABSOLUTE source.path -- old-style.
	n := &node.Node{
		ID: "test/absolute-staleness",
		Source: node.Source{
			Path: srcFile, // absolute
			Hash: hash,
		},
	}

	// Even with a projectRoot, absolute paths work because ResolveSourcePath
	// returns them as-is.
	status, err := VerifyStaleness(n, "/some/irrelevant/root")
	if err != nil {
		t.Fatalf("VerifyStaleness: %v", err)
	}
	if status.IsStale {
		t.Error("should not be stale when hash matches")
	}
}

// ---------------------------------------------------------------------------
// VerifyIntegrity with missing source (relative path)
// ---------------------------------------------------------------------------

func TestVerifyIntegrity_MissingSource_RelativePath(t *testing.T) {
	projectRoot := t.TempDir()

	nodes := []*node.Node{
		{
			ID: "test/missing-rel",
			Source: node.Source{
				Path: "nonexistent/file.go", // relative, does not exist
				Hash: "abc123",
			},
		},
	}

	issues := VerifyIntegrity(nodes, projectRoot)

	found := false
	for _, issue := range issues {
		if issue.IssueType == MissingSource && issue.NodeID == "test/missing-rel" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected MissingSource issue for relative path, got issues: %+v", issues)
	}
}

// ---------------------------------------------------------------------------
// VerifyIntegrity with existing source (relative path)
// ---------------------------------------------------------------------------

func TestVerifyIntegrity_ExistingSource_RelativePath(t *testing.T) {
	projectRoot := t.TempDir()
	srcDir := filepath.Join(projectRoot, "pkg")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "handler.go")
	content := "package pkg\n"
	if err := os.WriteFile(srcFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	hash, err := ComputeSourceHash(srcFile, [2]int{0, 0})
	if err != nil {
		t.Fatal(err)
	}

	nodes := []*node.Node{
		{
			ID: "test/existing-rel",
			Source: node.Source{
				Path: "pkg/handler.go", // relative
				Hash: hash,
			},
		},
	}

	issues := VerifyIntegrity(nodes, projectRoot)

	for _, issue := range issues {
		if issue.IssueType == MissingSource && issue.NodeID == "test/existing-rel" {
			t.Errorf("should NOT report MissingSource for existing relative path, got: %+v", issue)
		}
	}
}
