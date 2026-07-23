package namespace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/routes"
)

// setupTestVaultNode mirrors setupTestVault but with a caller-chosen node id,
// so eviction tests can distinguish which directory a vault resolved from.
func setupTestVaultNode(t *testing.T, vaultID, nodeID string) string {
	t.Helper()
	dir := t.TempDir()
	configContent := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}
	nodeContent := "---\nid: " + nodeID + "\ntype: concept\nstatus: active\n---\n\nTest node.\n"
	if err := os.WriteFile(filepath.Join(dir, nodeID+".md"), []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDenVaultsPrecedenceOverRoutes: an explicit den link WINS over a
// routing-table entry for the same vault id (routes point at a stale/empty
// dir; the den link points at the real vault).
func TestDenVaultsPrecedenceOverRoutes(t *testing.T) {
	denDir := setupTestVaultNode(t, "shared-vault", "node-1")
	staleDir := t.TempDir() // empty — loading from here would fail

	rt := routes.EmptyTable()
	rt.Set("shared-vault", staleDir)
	reg := NewVaultRegistry("local", "/tmp/local", nil, rt)
	reg.SetDenVaults(map[string]string{"shared-vault": denDir})

	n, ok := reg.Resolve("shared-vault", "node-1")
	if !ok || n.ID != "node-1" {
		t.Fatalf("Resolve via den vault dir = (%v, %v), want node-1 from the den link path", n, ok)
	}
}

// TestDenVaultsInKnownVaultIDs: den-link-only vaults (no route, no bridge)
// must appear in KnownVaultIDs so the query fan-out reaches them.
func TestDenVaultsInKnownVaultIDs(t *testing.T) {
	denDir := setupTestVaultNode(t, "den-only", "node-1")
	reg := NewVaultRegistry("local", "/tmp/local", nil, routes.EmptyTable())
	reg.SetDenVaults(map[string]string{"den-only": denDir})

	found := false
	for _, id := range reg.KnownVaultIDs() {
		if id == "den-only" {
			found = true
		}
	}
	if !found {
		t.Fatalf("KnownVaultIDs = %v, want den-only included", reg.KnownVaultIDs())
	}
}

// TestDenVaultsSurviveRebuild: Rebuild replaces bridge paths and the routing
// table but must preserve the den vault set AND the cached vault loaded
// through it (its resolved directory is unchanged).
func TestDenVaultsSurviveRebuild(t *testing.T) {
	denDir := setupTestVaultNode(t, "den-vault", "node-1")
	reg := NewVaultRegistry("local", "/tmp/local", nil, routes.EmptyTable())
	reg.SetDenVaults(map[string]string{"den-vault": denDir})
	if _, err := reg.ResolveGraph("den-vault"); err != nil {
		t.Fatalf("seed ResolveGraph: %v", err)
	}

	reg.Rebuild(nil, routes.EmptyTable())

	found := false
	for _, id := range reg.KnownVaultIDs() {
		if id == "den-vault" {
			found = true
		}
	}
	if !found {
		t.Fatalf("KnownVaultIDs after Rebuild = %v, want den-vault preserved", reg.KnownVaultIDs())
	}
	if n, ok := reg.Resolve("den-vault", "node-1"); !ok || n.ID != "node-1" {
		t.Fatalf("Resolve after Rebuild = (%v, %v), want node-1 (cached vault not evicted)", n, ok)
	}
}

// TestSetDenVaultsEvictsChangedDir: repointing a den vault id at a different
// directory evicts the cached vault so the next resolve loads the new dir.
func TestSetDenVaultsEvictsChangedDir(t *testing.T) {
	dirA := setupTestVaultNode(t, "moving-vault", "node-a")
	dirB := setupTestVaultNode(t, "moving-vault", "node-b")
	reg := NewVaultRegistry("local", "/tmp/local", nil, routes.EmptyTable())

	reg.SetDenVaults(map[string]string{"moving-vault": dirA})
	if n, ok := reg.Resolve("moving-vault", "node-a"); !ok || n.ID != "node-a" {
		t.Fatalf("Resolve dirA = (%v, %v), want node-a", n, ok)
	}

	reg.SetDenVaults(map[string]string{"moving-vault": dirB})
	if _, ok := reg.Resolve("moving-vault", "node-a"); ok {
		t.Fatal("node-a still resolves after repoint — stale cached vault not evicted")
	}
	if n, ok := reg.Resolve("moving-vault", "node-b"); !ok || n.ID != "node-b" {
		t.Fatalf("Resolve dirB = (%v, %v), want node-b", n, ok)
	}
}

// TestSetDenVaultsRejectsLocalID: a den link resolving to the local vault id
// must never register (it would shadow the live vault with a read-only copy).
func TestSetDenVaultsRejectsLocalID(t *testing.T) {
	dir := setupTestVaultNode(t, "local", "node-1")
	reg := NewVaultRegistry("local", "/tmp/local", nil, routes.EmptyTable())
	reg.SetDenVaults(map[string]string{"local": dir, "": dir, "no-dir": ""})
	if ids := reg.KnownVaultIDs(); len(ids) != 0 {
		t.Fatalf("KnownVaultIDs = %v, want empty (local/empty entries rejected)", ids)
	}
}

// TestResolveConfigViaDenVault: ResolveConfig surfaces the remote vault's
// parsed _config.md (the per-link embedding federation input).
func TestResolveConfigViaDenVault(t *testing.T) {
	denDir := setupTestVaultNode(t, "cfg-vault", "node-1")
	reg := NewVaultRegistry("local", "/tmp/local", nil, routes.EmptyTable())
	reg.SetDenVaults(map[string]string{"cfg-vault": denDir})

	cfg, err := reg.ResolveConfig("cfg-vault")
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.VaultID != "cfg-vault" || cfg.EmbeddingProvider != "mock" {
		t.Fatalf("config = vault_id=%q provider=%q, want cfg-vault/mock", cfg.VaultID, cfg.EmbeddingProvider)
	}
	if _, err := reg.ResolveConfig("nope"); err == nil {
		t.Fatal("ResolveConfig on unknown vault should error")
	}
}
