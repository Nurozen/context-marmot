package namespace

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/nurozen/context-marmot/internal/routes"
)

// helper: create a minimal vault directory with _config.md and one node.
func setupTestVault(t *testing.T, vaultID string) string {
	t.Helper()
	dir := t.TempDir()

	configContent := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	nodeContent := "---\nid: node-1\ntype: concept\nstatus: active\n---\n\nTest node.\n"
	if err := os.WriteFile(filepath.Join(dir, "node-1.md"), []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}

	return dir
}

// ---------------------------------------------------------------------------
// Route priority over bridge path
// ---------------------------------------------------------------------------

func TestRoutesRoutePriorityOverBridge(t *testing.T) {
	// Create two vault dirs — one "correct" (routes), one "stale" (bridge).
	correctDir := setupTestVault(t, "shared-vault")
	staleDir := t.TempDir() // intentionally empty — would fail to load

	// Bridge points to staleDir.
	bridges := []*Bridge{
		{
			SourceVaultID:   "local",
			TargetVaultID:   "shared-vault",
			SourceVaultPath: "/tmp/local",
			TargetVaultPath: staleDir,
		},
	}

	// Routing table points to correctDir.
	rt := &routes.RoutingTable{Vaults: make(map[string]routes.VaultEntry)}
	rt.Set("shared-vault", correctDir)

	reg := NewVaultRegistry("local", "/tmp/local", bridges, rt)

	// Should resolve via the routing table path (correctDir), NOT the bridge path.
	n, ok := reg.Resolve("shared-vault", "node-1")
	if !ok {
		t.Fatal("expected to resolve node-1 from shared-vault via routing table")
	}
	if n.ID != "node-1" {
		t.Fatalf("expected ID node-1, got %s", n.ID)
	}
}

// ---------------------------------------------------------------------------
// Route fallback to bridge
// ---------------------------------------------------------------------------

func TestRoutesFallbackToBridge(t *testing.T) {
	vaultDir := setupTestVault(t, "bridge-only")

	// Bridge has the path. Routing table does NOT have this vault.
	bridges := []*Bridge{
		{
			SourceVaultID:   "local",
			TargetVaultID:   "bridge-only",
			SourceVaultPath: "/tmp/local",
			TargetVaultPath: vaultDir,
		},
	}

	rt := &routes.RoutingTable{Vaults: make(map[string]routes.VaultEntry)}
	// routing table is empty — no entry for "bridge-only".

	reg := NewVaultRegistry("local", "/tmp/local", bridges, rt)

	n, ok := reg.Resolve("bridge-only", "node-1")
	if !ok {
		t.Fatal("expected to resolve node-1 via bridge fallback")
	}
	if n.ID != "node-1" {
		t.Fatalf("expected ID node-1, got %s", n.ID)
	}
}

// ---------------------------------------------------------------------------
// Route-only vault (no bridge at all)
// ---------------------------------------------------------------------------

func TestRoutesRouteOnlyVault(t *testing.T) {
	vaultDir := setupTestVault(t, "route-only")

	// No bridges at all.
	rt := &routes.RoutingTable{Vaults: make(map[string]routes.VaultEntry)}
	rt.Set("route-only", vaultDir)

	reg := NewVaultRegistry("local", "/tmp/local", nil, rt)

	n, ok := reg.Resolve("route-only", "node-1")
	if !ok {
		t.Fatal("expected to resolve node-1 from route-only vault")
	}
	if n.ID != "node-1" {
		t.Fatalf("expected ID node-1, got %s", n.ID)
	}
}

// ---------------------------------------------------------------------------
// Stale route path (points to nonexistent directory)
// ---------------------------------------------------------------------------

func TestRoutesStaleRoutePath(t *testing.T) {
	rt := &routes.RoutingTable{Vaults: make(map[string]routes.VaultEntry)}
	rt.Set("ghost-vault", "/nonexistent/path/that/does/not/exist")

	reg := NewVaultRegistry("local", "/tmp/local", nil, rt)

	// BUG FINDING: config.Load returns a default config (no error) when
	// _config.md is missing, and graph.LoadGraph on a nonexistent dir returns
	// an error only if Walk fails. On macOS/Linux, walking a nonexistent dir
	// errors. Let's verify the behavior — the system should handle this
	// gracefully (error or empty graph, but never panic).
	g, err := reg.ResolveGraph("ghost-vault")
	if err != nil {
		// Good: the system detected the stale path and returned an error.
		t.Logf("graceful error for stale route: %v", err)
	} else {
		// The system silently loaded an empty graph — this is a design concern.
		// It means stale routes are invisible to the user.
		t.Logf("WARNING: stale route to nonexistent dir produced empty graph with %d nodes (no error)", g.NodeCount())
	}

	// Resolve (bool variant) should return false for any specific node,
	// whether the graph load errored or returned empty.
	_, ok := reg.Resolve("ghost-vault", "any-node")
	if ok {
		t.Fatal("expected Resolve to return false for node in stale/empty vault")
	}
}

// ---------------------------------------------------------------------------
// KnownVaultIDs includes both bridge and route vault IDs
// ---------------------------------------------------------------------------

func TestRoutesKnownVaultIDsIncludesRoutes(t *testing.T) {
	bridges := []*Bridge{
		{
			SourceVaultID:   "vault-a",
			TargetVaultID:   "vault-b",
			SourceVaultPath: "/tmp/a",
			TargetVaultPath: "/tmp/b",
		},
	}

	rt := &routes.RoutingTable{Vaults: make(map[string]routes.VaultEntry)}
	rt.Set("vault-c", "/tmp/c")
	rt.Set("vault-d", "/tmp/d")
	// Also add vault-a to the routing table — should not duplicate.
	rt.Set("vault-a", "/tmp/a-route")

	reg := NewVaultRegistry("vault-a", "/tmp/a", bridges, rt)

	ids := reg.KnownVaultIDs()
	sort.Strings(ids)

	expected := []string{"vault-a", "vault-b", "vault-c", "vault-d"}
	if len(ids) != len(expected) {
		t.Fatalf("expected %d known vault IDs, got %d: %v", len(expected), len(ids), ids)
	}
	for i, id := range expected {
		if ids[i] != id {
			t.Errorf("position %d: expected %s, got %s", i, id, ids[i])
		}
	}
}

// ---------------------------------------------------------------------------
// KnownVaultIDs with nil routing table
// ---------------------------------------------------------------------------

func TestRoutesKnownVaultIDsNilRoutingTable(t *testing.T) {
	bridges := []*Bridge{
		{
			SourceVaultID:   "v1",
			TargetVaultID:   "v2",
			SourceVaultPath: "/p1",
			TargetVaultPath: "/p2",
		},
	}

	reg := NewVaultRegistry("v1", "/p1", bridges, nil)
	ids := reg.KnownVaultIDs()
	sort.Strings(ids)

	if len(ids) != 2 || ids[0] != "v1" || ids[1] != "v2" {
		t.Fatalf("expected [v1, v2], got %v", ids)
	}
}

// ---------------------------------------------------------------------------
// Route priority verified by checking cached vault dir
// ---------------------------------------------------------------------------

func TestRoutesRoutePriorityVerifyDir(t *testing.T) {
	// Ensure the registry uses the route path, not the bridge path, when both
	// are present. We verify by checking the cached RemoteVault.VaultDir.
	routeDir := setupTestVault(t, "pv")
	bridgeDir := setupTestVault(t, "pv") // separate dir with same vault_id content

	bridges := []*Bridge{
		{
			SourceVaultID:   "local",
			TargetVaultID:   "pv",
			SourceVaultPath: "/tmp/local",
			TargetVaultPath: bridgeDir,
		},
	}

	rt := &routes.RoutingTable{Vaults: make(map[string]routes.VaultEntry)}
	rt.Set("pv", routeDir)

	reg := NewVaultRegistry("local", "/tmp/local", bridges, rt)

	// Force load.
	_, err := reg.ResolveGraph("pv")
	if err != nil {
		t.Fatalf("ResolveGraph: %v", err)
	}

	// Check internal cache (package-level access since we're in the same package).
	reg.mu.RLock()
	rv := reg.vaults["pv"]
	reg.mu.RUnlock()

	if rv == nil {
		t.Fatal("expected vault to be cached")
	}
	if rv.VaultDir != routeDir {
		t.Errorf("expected VaultDir=%s (route), got %s (likely bridge path)", routeDir, rv.VaultDir)
	}
}
