package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers specific to Phase 15 CLI tests.
// ---------------------------------------------------------------------------

// writeTestNode creates a minimal node markdown file inside the vault.
func writeTestNode(t *testing.T, dir, id, ns string) {
	t.Helper()
	nodePath := filepath.Join(dir, id+".md")
	if err := os.MkdirAll(filepath.Dir(nodePath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`---
id: %s
type: function
namespace: %s
status: active
edges: []
---

Test node %s.
`, id, ns, id)
	if err := os.WriteFile(nodePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTestNodeWithSource creates a node whose source.path points to a real file.
func writeTestNodeWithSource(t *testing.T, dir, id, ns, sourcePath, sourceHash string) {
	t.Helper()
	nodePath := filepath.Join(dir, id+".md")
	if err := os.MkdirAll(filepath.Dir(nodePath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`---
id: %s
type: function
namespace: %s
status: active
source:
  path: %s
  hash: %s
edges: []
---

Test node %s with source.
`, id, ns, sourcePath, sourceHash, id)
	if err := os.WriteFile(nodePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTestNamespace creates a namespace directory with a _namespace.md manifest.
func writeTestNamespace(t *testing.T, dir, name string) {
	t.Helper()
	nsDir := filepath.Join(dir, name)
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`---
name: %s
---

Namespace %s.
`, name, name)
	if err := os.WriteFile(filepath.Join(nsDir, "_namespace.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initTestVault creates a minimal vault directory structure (mirrors runInit
// but does not call the CLI so it stays isolated).
func initTestVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	vault := filepath.Join(dir, ".marmot")

	if err := runInit(vault); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	return vault
}

// ---------------------------------------------------------------------------
// 1. TestStatusCommand — success with populated vault
// ---------------------------------------------------------------------------

func TestStatusCommand(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")
	writeTestNode(t, vault, "node_b", "default")

	code := run([]string{"status", "--dir", vault})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 2. TestStatusCommandEmptyVault — success on empty vault (0 nodes)
// ---------------------------------------------------------------------------

func TestStatusCommandEmptyVault(t *testing.T) {
	vault := initTestVault(t)

	code := run([]string{"status", "--dir", vault})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 3. TestStatusCommandNoVault — non-existent dir should fail
// ---------------------------------------------------------------------------

func TestStatusCommandNoVault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")

	code := run([]string{"status", "--dir", dir})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 4. TestBridgeCommand — create bridge between two namespaces
// ---------------------------------------------------------------------------

func TestBridgeCommand(t *testing.T) {
	vault := initTestVault(t)
	writeTestNamespace(t, vault, "alpha")
	writeTestNamespace(t, vault, "beta")

	code := run([]string{"bridge", "--dir", vault, "alpha", "beta"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}

	// Verify bridge file was created.
	bridgePath := filepath.Join(vault, "_bridges", "alpha--beta.md")
	assertFileExists(t, bridgePath)
}

// ---------------------------------------------------------------------------
// 5. TestBridgeCommandWithRelations — bridge with explicit relation types
// ---------------------------------------------------------------------------

func TestBridgeCommandWithRelations(t *testing.T) {
	vault := initTestVault(t)
	writeTestNamespace(t, vault, "alpha")
	writeTestNamespace(t, vault, "beta")

	code := run([]string{"bridge", "--dir", vault, "--relations", "calls,reads", "alpha", "beta"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}

	// Verify bridge file contains the specified relations.
	bridgePath := filepath.Join(vault, "_bridges", "alpha--beta.md")
	assertFileExists(t, bridgePath)

	data, err := os.ReadFile(bridgePath)
	if err != nil {
		t.Fatalf("read bridge file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "calls") {
		t.Fatalf("bridge file missing 'calls' relation:\n%s", content)
	}
	if !strings.Contains(content, "reads") {
		t.Fatalf("bridge file missing 'reads' relation:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// 6. TestBridgeCommandMissingArgs — only one namespace arg should fail
// ---------------------------------------------------------------------------

func TestBridgeCommandMissingArgs(t *testing.T) {
	vault := initTestVault(t)

	code := run([]string{"bridge", "alpha", "--dir", vault})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 7. TestReembedCommand — re-index with force
// ---------------------------------------------------------------------------

func TestReembedCommand(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")
	writeTestNode(t, vault, "node_b", "default")

	// First index so there is something to re-embed.
	code := run([]string{"index", "--dir", vault})
	if code != 0 {
		t.Fatalf("index: expected exit code 0, got %d", code)
	}

	// Now run reembed.
	code = run([]string{"reembed", "--dir", vault})
	if code != 0 {
		t.Fatalf("reembed: expected exit code 0, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 8. TestReembedCommandNoVault — non-existent dir should fail
// ---------------------------------------------------------------------------

func TestReembedCommandNoVault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")

	code := run([]string{"reembed", "--dir", dir})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 9. TestVerifyWithNamespace — verify filtered to a specific namespace
// ---------------------------------------------------------------------------

func TestVerifyWithNamespace(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")
	writeTestNode(t, vault, "node_b", "other")

	code := run([]string{"verify", "--namespace", "default", "--dir", vault})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 10. TestVerifyWithStaleness — staleness check detects changed source
// ---------------------------------------------------------------------------

func TestVerifyWithStaleness(t *testing.T) {
	vault := initTestVault(t)

	// Create a source file and compute an initial hash (we use a fake hash
	// so it will not match the current file content -> stale).
	sourceDir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(sourceDir, "main.go")
	if err := os.WriteFile(sourceFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write node with a deliberately wrong hash so it registers as stale.
	writeTestNodeWithSource(t, vault, "stale_node", "default", sourceFile, "0000000000000000000000000000000000000000000000000000000000000000")

	code := run([]string{"verify", "--staleness", "--dir", vault})
	// Exit code 0 means verify ran successfully (staleness is reported, not a failure).
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 11. TestIndexWithPath — positional path arg should be rejected / warn
// ---------------------------------------------------------------------------

func TestIndexWithPath(t *testing.T) {
	vault := initTestVault(t)

	// Passing a positional path arg to index should produce exit code 1
	// (the enhanced index command now warns about unsupported positional args).
	code := run([]string{"index", "--dir", vault, "some/path"})
	if code != 1 {
		t.Fatalf("expected exit code 1 for positional arg, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 12. TestSummarizeCommandNoLLM — no LLM configured should fail
// ---------------------------------------------------------------------------

func TestSummarizeCommandNoLLM(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")

	code := run([]string{"summarize", "--dir", vault})
	if code != 1 {
		t.Fatalf("expected exit code 1 (no LLM provider), got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 13. TestWatchCommandNoVault — non-existent dir should fail immediately
// ---------------------------------------------------------------------------

func TestWatchCommandNoVault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")

	code := run([]string{"watch", "--dir", dir})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// 14. TestUnknownCommand — unknown sub-command should fail
// ---------------------------------------------------------------------------

func TestUnknownCommandPhase15(t *testing.T) {
	code := run([]string{"unknown"})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}
