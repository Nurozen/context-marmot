//go:build integration

package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/namespace"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeVaultConfig writes a minimal _config.md with the given vault_id.
func writeVaultConfig(t *testing.T, dir, vaultID string) {
	t.Helper()
	content := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("writeVaultConfig(%s): %v", vaultID, err)
	}
}

// newEngineWithNS creates an Engine in the given directory with a mock embedder,
// an NSManager, and (optionally) a VaultRegistry.
func newEngineWithNS(t *testing.T, dir string, registry *namespace.VaultRegistry) *mcpserver.Engine {
	t.Helper()
	embedder := embedding.NewMockEmbedder("test-model")
	eng, err := mcpserver.NewEngine(dir, embedder)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	mgr, err := namespace.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	eng.WithNamespaceManager(mgr)
	if registry != nil {
		eng.WithVaultRegistry(registry)
	}
	return eng
}

// ---------------------------------------------------------------------------
// Test 1: TestCrossVaultBridgeCreateAndVerify
// ---------------------------------------------------------------------------

func TestCrossVaultBridgeCreateAndVerify(t *testing.T) {
	vaultADir := t.TempDir()
	vaultBDir := t.TempDir()

	// Write _config.md with vault_id to each vault.
	writeVaultConfig(t, vaultADir, "vault-a")
	writeVaultConfig(t, vaultBDir, "vault-b")

	// Create the cross-vault bridge.
	bridge, err := namespace.CreateCrossVaultBridge(vaultADir, vaultBDir, []string{"references", "calls"})
	if err != nil {
		t.Fatalf("CreateCrossVaultBridge: %v", err)
	}

	// Verify bridge fields are populated correctly.
	if bridge.SourceVaultID != "vault-a" {
		t.Errorf("SourceVaultID = %q, want vault-a", bridge.SourceVaultID)
	}
	if bridge.TargetVaultID != "vault-b" {
		t.Errorf("TargetVaultID = %q, want vault-b", bridge.TargetVaultID)
	}
	if !bridge.IsCrossVault() {
		t.Error("IsCrossVault() = false, want true")
	}

	// Verify bridge manifest exists in _bridges/ of both vaults.
	expectedFilename := "@vault-a--@vault-b.md"
	for _, dir := range []string{vaultADir, vaultBDir} {
		bridgePath := filepath.Join(dir, "_bridges", expectedFilename)
		if _, err := os.Stat(bridgePath); os.IsNotExist(err) {
			t.Fatalf("bridge file missing in %s: %s", dir, bridgePath)
		}
	}

	// Load a Manager from vault A and verify CrossVaultBridges is populated.
	mgr, err := namespace.NewManager(vaultADir)
	if err != nil {
		t.Fatalf("NewManager(vaultA): %v", err)
	}
	if len(mgr.CrossVaultBridges) == 0 {
		t.Fatal("CrossVaultBridges is empty after loading Manager from vault A")
	}

	// ValidateCrossVaultEdge: allowed relation "references".
	if err := mgr.ValidateCrossVaultEdge("vault-a", "vault-b", "references"); err != nil {
		t.Errorf("ValidateCrossVaultEdge(references) unexpected error: %v", err)
	}

	// ValidateCrossVaultEdge: allowed relation "calls".
	if err := mgr.ValidateCrossVaultEdge("vault-a", "vault-b", "calls"); err != nil {
		t.Errorf("ValidateCrossVaultEdge(calls) unexpected error: %v", err)
	}

	// ValidateCrossVaultEdge: disallowed relation "contains".
	if err := mgr.ValidateCrossVaultEdge("vault-a", "vault-b", "contains"); err == nil {
		t.Error("ValidateCrossVaultEdge(contains) should have returned error for disallowed relation")
	}

	// ValidateCrossVaultEdge: no bridge to vault-c.
	if err := mgr.ValidateCrossVaultEdge("vault-a", "vault-c", "references"); err == nil {
		t.Error("ValidateCrossVaultEdge(vault-c) should have returned error for missing bridge")
	}
}

// ---------------------------------------------------------------------------
// Test 2: TestCrossVaultTraversalViaMCPEngine
// ---------------------------------------------------------------------------

func TestCrossVaultTraversalViaMCPEngine(t *testing.T) {
	vaultADir := t.TempDir()
	vaultBDir := t.TempDir()

	// Write configs.
	writeVaultConfig(t, vaultADir, "vault-a")
	writeVaultConfig(t, vaultBDir, "vault-b")

	// Create cross-vault bridge.
	bridge, err := namespace.CreateCrossVaultBridge(vaultADir, vaultBDir, []string{"calls", "references"})
	if err != nil {
		t.Fatalf("CreateCrossVaultBridge: %v", err)
	}

	// Create engine for vault A with VaultRegistry.
	embedder := embedding.NewMockEmbedder("test-model")
	engA, err := mcpserver.NewEngine(vaultADir, embedder)
	if err != nil {
		t.Fatalf("NewEngine(vaultA): %v", err)
	}
	defer engA.Close()

	mgrA, err := namespace.NewManager(vaultADir)
	if err != nil {
		t.Fatalf("NewManager(vaultA): %v", err)
	}
	engA.WithNamespaceManager(mgrA)

	registry := namespace.NewVaultRegistry("vault-a", vaultADir, []*namespace.Bridge{bridge})
	engA.WithVaultRegistry(registry)

	// Write node in vault A with edge to vault B.
	resA := writeNode(t, engA, map[string]any{
		"id":      "service/auth",
		"type":    "module",
		"summary": "Authentication service handling login and token validation",
		"context": "package auth\n\nimport users \"@vault-b/service/users\"\n",
		"edges": []map[string]any{
			{"target": "@vault-b/service/users", "relation": "calls"},
		},
	})
	if resA.IsError {
		t.Fatalf("write service/auth failed: %s", text(t, resA))
	}

	// Write node in vault B directly (using its own engine).
	engB, err := mcpserver.NewEngine(vaultBDir, embedder)
	if err != nil {
		t.Fatalf("NewEngine(vaultB): %v", err)
	}
	defer engB.Close()

	resB := writeNode(t, engB, map[string]any{
		"id":      "service/users",
		"type":    "module",
		"summary": "User management service for CRUD operations on user accounts",
		"context": "package users\n\nfunc GetUser(id string) (*User, error) { ... }\n",
	})
	if resB.IsError {
		t.Fatalf("write service/users failed: %s", text(t, resB))
	}

	// Index embeddings for vault A node.
	// The engine already indexed during write. Now query.

	// Query vault A for the auth service.
	xml := queryNodes(t, engA, map[string]any{
		"query":  "authentication service login token",
		"depth":  2,
		"budget": 50000,
	})

	// The result MUST include the local service/auth node.
	if !strings.Contains(xml, "service/auth") {
		t.Errorf("query result missing local node 'service/auth', got:\n%s", xml)
	}

	// The result SHOULD include the cross-vault reference to @vault-b/service/users.
	// The BridgedGraphResolver will attempt to resolve the remote node.
	// Since VaultRegistry is wired, it should lazily load vault B's graph.
	if !strings.Contains(xml, "service/users") {
		t.Errorf("query result missing remote node 'service/users' (cross-vault traversal did not reach vault B), got:\n%s", xml)
	}
}

// ---------------------------------------------------------------------------
// Test 3: TestCrossVaultWriteValidation
// ---------------------------------------------------------------------------

func TestCrossVaultWriteValidation(t *testing.T) {
	vaultADir := t.TempDir()
	vaultBDir := t.TempDir()

	// Write configs.
	writeVaultConfig(t, vaultADir, "vault-a")
	writeVaultConfig(t, vaultBDir, "vault-b")

	// Create cross-vault bridge allowing only "references".
	bridge, err := namespace.CreateCrossVaultBridge(vaultADir, vaultBDir, []string{"references"})
	if err != nil {
		t.Fatalf("CreateCrossVaultBridge: %v", err)
	}

	// Create engine for vault A with NSManager and VaultRegistry.
	registry := namespace.NewVaultRegistry("vault-a", vaultADir, []*namespace.Bridge{bridge})
	engA := newEngineWithNS(t, vaultADir, registry)
	defer engA.Close()

	// Sub-test: allowed relation "references" should succeed.
	t.Run("allowed_relation_references", func(t *testing.T) {
		res := writeNode(t, engA, map[string]any{
			"id":      "lib/core",
			"type":    "module",
			"summary": "Core library module",
			"edges": []map[string]any{
				{"target": "@vault-b/some-node", "relation": "references"},
			},
		})
		if res.IsError {
			t.Fatalf("write with allowed relation 'references' failed: %s", text(t, res))
		}
	})

	// Sub-test: disallowed relation "calls" should be rejected.
	t.Run("disallowed_relation_calls", func(t *testing.T) {
		res, err := engA.HandleContextWrite(context.Background(), makeReq("context_write", map[string]any{
			"id":      "lib/net",
			"type":    "module",
			"summary": "Network library module",
			"edges": []map[string]any{
				{"target": "@vault-b/some-node", "relation": "calls"},
			},
		}))
		if err != nil {
			t.Fatalf("HandleContextWrite returned Go error: %v", err)
		}
		if !res.IsError {
			t.Fatal("expected cross-vault edge with disallowed relation 'calls' to be rejected, but write succeeded")
		}
		errText := text(t, res)
		if !strings.Contains(errText, "cross-vault edge rejected") {
			t.Errorf("expected 'cross-vault edge rejected' in error, got: %s", errText)
		}
	})

	// Sub-test: no bridge to vault-c should be rejected.
	t.Run("no_bridge_to_vault_c", func(t *testing.T) {
		res, err := engA.HandleContextWrite(context.Background(), makeReq("context_write", map[string]any{
			"id":      "lib/db",
			"type":    "module",
			"summary": "Database library module",
			"edges": []map[string]any{
				{"target": "@vault-c/some-node", "relation": "references"},
			},
		}))
		if err != nil {
			t.Fatalf("HandleContextWrite returned Go error: %v", err)
		}
		if !res.IsError {
			t.Fatal("expected cross-vault edge to unbridged vault-c to be rejected, but write succeeded")
		}
		errText := text(t, res)
		if !strings.Contains(errText, "cross-vault edge rejected") {
			t.Errorf("expected 'cross-vault edge rejected' in error, got: %s", errText)
		}
	})
}

// ---------------------------------------------------------------------------
// Test 4: TestCrossVaultVerifyBridges
//
// This test exercises runVerifyEnhanced from the cmd/marmot package.
// Since runVerifyEnhanced is in package main and not exported, we test the
// underlying mechanics directly: loading bridges, checking remote vault
// reachability, and verifying the bridge_unreachable detection logic.
// ---------------------------------------------------------------------------

func TestCrossVaultVerifyBridges(t *testing.T) {
	vaultADir := t.TempDir()
	vaultBDir := t.TempDir()

	writeVaultConfig(t, vaultADir, "vault-a")
	writeVaultConfig(t, vaultBDir, "vault-b")

	// Create cross-vault bridge.
	_, err := namespace.CreateCrossVaultBridge(vaultADir, vaultBDir, []string{"references"})
	if err != nil {
		t.Fatalf("CreateCrossVaultBridge: %v", err)
	}

	// Sub-test: both vaults exist -- bridge should be reachable.
	t.Run("bridge_reachable", func(t *testing.T) {
		mgr, err := namespace.NewManager(vaultADir)
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		if len(mgr.CrossVaultBridges) == 0 {
			t.Fatal("no cross-vault bridges found")
		}

		for _, b := range mgr.CrossVaultBridges {
			remoteDir := b.TargetVaultPath
			if b.SourceVaultPath != "" && b.SourceVaultID != "vault-a" {
				remoteDir = b.SourceVaultPath
			}
			if _, err := os.Stat(remoteDir); os.IsNotExist(err) {
				t.Errorf("bridge target path %s should exist but does not", remoteDir)
			}
		}
	})

	// Sub-test: delete vault B, bridge should become unreachable.
	t.Run("bridge_unreachable_after_delete", func(t *testing.T) {
		// Remove vault B entirely.
		if err := os.RemoveAll(vaultBDir); err != nil {
			t.Fatalf("RemoveAll(vaultB): %v", err)
		}

		mgr, err := namespace.NewManager(vaultADir)
		if err != nil {
			t.Fatalf("NewManager after delete: %v", err)
		}
		if len(mgr.CrossVaultBridges) == 0 {
			t.Fatal("no cross-vault bridges found after vault B deletion")
		}

		// Simulate the bridge_unreachable check that runVerifyEnhanced performs.
		foundUnreachable := false
		for _, b := range mgr.CrossVaultBridges {
			remoteDir := b.TargetVaultPath
			if b.SourceVaultPath != "" && b.SourceVaultID != "vault-a" {
				remoteDir = b.SourceVaultPath
			}
			if _, err := os.Stat(remoteDir); os.IsNotExist(err) {
				foundUnreachable = true
			}
		}
		if !foundUnreachable {
			t.Error("expected at least one bridge_unreachable issue after deleting vault B")
		}
	})
}

// ---------------------------------------------------------------------------
// Test 5: TestCrossVaultCLIBridgeDetection
//
// Tests the heuristics for detecting cross-vault vs namespace bridge mode.
// looksLikeVaultPath and cmdBridge are in cmd/marmot (package main), so we
// replicate the logic here to validate the detection rules.
// ---------------------------------------------------------------------------

func TestCrossVaultCLIBridgeDetection(t *testing.T) {
	// Replicate looksLikeVaultPath from cmd/marmot/main.go.
	looksLikeVaultPath := func(s string) bool {
		return strings.Contains(s, "/") || strings.Contains(s, "\\") ||
			strings.HasSuffix(s, ".marmot") || s == "." || s == ".."
	}

	t.Run("looksLikeVaultPath", func(t *testing.T) {
		cases := []struct {
			input string
			want  bool
		}{
			{"/Users/dev/project/.marmot", true},  // absolute path with /
			{"../other-project/.marmot", true},     // relative path with /
			{"project.marmot", true},               // ends in .marmot
			{".", true},                            // current dir
			{"..", true},                           // parent dir
			{"ns-alpha", false},                    // simple namespace name
			{"backend", false},                     // simple namespace name
			{"project-beta", false},                // simple namespace with hyphen
			{"C:\\Users\\dev\\.marmot", true},      // Windows path with backslash
			{"/tmp/vault-b", true},                 // path-like
		}
		for _, tc := range cases {
			got := looksLikeVaultPath(tc.input)
			if got != tc.want {
				t.Errorf("looksLikeVaultPath(%q) = %v, want %v", tc.input, got, tc.want)
			}
		}
	})

	// Test cmdBridge argument parsing logic (replicated here since it is in package main).
	t.Run("cmdBridge_detection_logic", func(t *testing.T) {
		// With 1 path-like arg: cross-vault mode.
		remaining := []string{"/path/to/other/.marmot"}
		if len(remaining) == 1 && looksLikeVaultPath(remaining[0]) {
			// Cross-vault detected correctly.
		} else {
			t.Error("expected 1-arg path-like to trigger cross-vault mode")
		}

		// With 2 namespace-like args: should NOT be cross-vault.
		remaining = []string{"ns-alpha", "ns-beta"}
		crossVault := false
		if len(remaining) == 2 {
			if looksLikeVaultPath(remaining[0]) || looksLikeVaultPath(remaining[1]) {
				crossVault = true
			}
		}
		if crossVault {
			t.Error("expected 2 namespace names to NOT trigger cross-vault mode")
		}

		// With 2 path-like args: cross-vault mode.
		remaining = []string{"/vault/a/.marmot", "/vault/b/.marmot"}
		crossVault = false
		if len(remaining) == 2 {
			if looksLikeVaultPath(remaining[0]) || looksLikeVaultPath(remaining[1]) {
				crossVault = true
			}
		}
		if !crossVault {
			t.Error("expected 2 path-like args to trigger cross-vault mode")
		}

		// With 2 args where one is path-like: cross-vault.
		remaining = []string{"ns-alpha", "/path/to/.marmot"}
		crossVault = false
		if len(remaining) == 2 {
			if looksLikeVaultPath(remaining[0]) || looksLikeVaultPath(remaining[1]) {
				crossVault = true
			}
		}
		if !crossVault {
			t.Error("expected mixed (namespace + path) args to trigger cross-vault mode")
		}
	})
}
