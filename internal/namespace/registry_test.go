package namespace

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestNewVaultRegistry(t *testing.T) {
	bridges := []*Bridge{
		{SourceVaultID: "vault-a", TargetVaultID: "vault-b", SourceVaultPath: "/tmp/a", TargetVaultPath: "/tmp/b"},
	}
	r := NewVaultRegistry("vault-a", "/tmp/a", bridges, nil)
	ids := r.KnownVaultIDs()
	sort.Strings(ids)
	if len(ids) != 2 {
		t.Fatalf("expected 2 known vault IDs, got %d: %v", len(ids), ids)
	}
	if ids[0] != "vault-a" || ids[1] != "vault-b" {
		t.Fatalf("expected [vault-a vault-b], got %v", ids)
	}
}

func TestNewVaultRegistry_NilBridges(t *testing.T) {
	r := NewVaultRegistry("local", "/tmp/local", nil, nil)
	ids := r.KnownVaultIDs()
	if len(ids) != 0 {
		t.Fatalf("expected 0 known vault IDs with nil bridges, got %d", len(ids))
	}
}

func TestNewVaultRegistry_SkipsNonCrossVault(t *testing.T) {
	// A bridge without vault paths is not cross-vault and should be skipped.
	bridges := []*Bridge{
		{Source: "ns-a", Target: "ns-b"},
	}
	r := NewVaultRegistry("local", "/tmp/local", bridges, nil)
	ids := r.KnownVaultIDs()
	if len(ids) != 0 {
		t.Fatalf("expected 0 known vault IDs for non-cross-vault bridge, got %d", len(ids))
	}
}

func TestVaultRegistry_ResolveUnknownVault(t *testing.T) {
	r := NewVaultRegistry("local", "/tmp/local", nil, nil)
	_, ok := r.Resolve("unknown", "some-node")
	if ok {
		t.Fatal("expected Resolve to return false for unknown vault")
	}
}

func TestVaultRegistry_ResolveGraphUnknownVault(t *testing.T) {
	r := NewVaultRegistry("local", "/tmp/local", nil, nil)
	_, err := r.ResolveGraph("unknown")
	if err == nil {
		t.Fatal("expected error for unknown vault")
	}
}

func TestVaultRegistry_Resolve(t *testing.T) {
	// Set up two temp vault directories with configs and nodes.
	vaultBDir := t.TempDir()

	// Write _config.md with vault_id for vault B.
	configContent := "---\nversion: \"1\"\nvault_id: vault-b\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(vaultBDir, "_config.md"), []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a node in vault B.
	nodeContent := `---
id: remote-concept
type: concept
status: active
---

A remote concept node for testing.
`
	if err := os.WriteFile(filepath.Join(vaultBDir, "remote-concept.md"), []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a bridge pointing to vault B.
	bridges := []*Bridge{
		{
			SourceVaultID:   "vault-a",
			TargetVaultID:   "vault-b",
			SourceVaultPath: "/tmp/vault-a",
			TargetVaultPath: vaultBDir,
		},
	}

	r := NewVaultRegistry("vault-a", "/tmp/vault-a", bridges, nil)

	// Resolve a node from vault B.
	n, ok := r.Resolve("vault-b", "remote-concept")
	if !ok {
		t.Fatal("expected to resolve remote-concept from vault-b")
	}
	if n.ID != "remote-concept" {
		t.Fatalf("expected ID remote-concept, got %s", n.ID)
	}
	if n.Type != "concept" {
		t.Fatalf("expected type concept, got %s", n.Type)
	}
}

func TestVaultRegistry_ResolveGraph(t *testing.T) {
	vaultDir := t.TempDir()

	configContent := "---\nversion: \"1\"\nvault_id: test-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "_config.md"), []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	nodeContent := `---
id: node-a
type: function
status: active
edges:
  - target: node-b
    relation: calls
---

Node A summary.
`
	nodeBContent := `---
id: node-b
type: function
status: active
---

Node B summary.
`
	if err := os.WriteFile(filepath.Join(vaultDir, "node-a.md"), []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "node-b.md"), []byte(nodeBContent), 0o644); err != nil {
		t.Fatal(err)
	}

	bridges := []*Bridge{
		{
			SourceVaultID:   "local",
			TargetVaultID:   "test-vault",
			SourceVaultPath: "/tmp/local",
			TargetVaultPath: vaultDir,
		},
	}

	r := NewVaultRegistry("local", "/tmp/local", bridges, nil)

	g, err := r.ResolveGraph("test-vault")
	if err != nil {
		t.Fatalf("ResolveGraph: %v", err)
	}
	if g.NodeCount() != 2 {
		t.Fatalf("expected 2 nodes in graph, got %d", g.NodeCount())
	}
}

func TestVaultRegistry_ResolveMissingNode(t *testing.T) {
	vaultDir := t.TempDir()

	configContent := "---\nversion: \"1\"\nvault_id: rv\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "_config.md"), []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	bridges := []*Bridge{
		{SourceVaultID: "local", TargetVaultID: "rv", SourceVaultPath: "/tmp/local", TargetVaultPath: vaultDir},
	}
	r := NewVaultRegistry("local", "/tmp/local", bridges, nil)

	_, ok := r.Resolve("rv", "nonexistent")
	if ok {
		t.Fatal("expected Resolve to return false for missing node in known vault")
	}
}

func TestVaultRegistry_Refresh(t *testing.T) {
	vaultDir := t.TempDir()

	configContent := "---\nversion: \"1\"\nvault_id: refresh-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "_config.md"), []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	nodeContent := `---
id: orig
type: concept
status: active
---

Original node.
`
	if err := os.WriteFile(filepath.Join(vaultDir, "orig.md"), []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}

	bridges := []*Bridge{
		{SourceVaultID: "local", TargetVaultID: "refresh-vault", SourceVaultPath: "/tmp/local", TargetVaultPath: vaultDir},
	}
	r := NewVaultRegistry("local", "/tmp/local", bridges, nil)

	// Load vault first.
	_, err := r.ResolveGraph("refresh-vault")
	if err != nil {
		t.Fatalf("initial ResolveGraph: %v", err)
	}

	// Add a new node to disk.
	newNodeContent := `---
id: added
type: concept
status: active
---

Added after initial load.
`
	if err := os.WriteFile(filepath.Join(vaultDir, "added.md"), []byte(newNodeContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Refresh and verify the new node is visible.
	if err := r.Refresh("refresh-vault"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	n, ok := r.Resolve("refresh-vault", "added")
	if !ok {
		t.Fatal("expected to find 'added' node after refresh")
	}
	if n.ID != "added" {
		t.Fatalf("expected ID 'added', got %s", n.ID)
	}
}

func TestVaultRegistry_RefreshUnloaded(t *testing.T) {
	r := NewVaultRegistry("local", "/tmp/local", nil, nil)
	err := r.Refresh("not-loaded")
	if err == nil {
		t.Fatal("expected error when refreshing unloaded vault")
	}
}
