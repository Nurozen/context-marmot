package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/routes"
)

// TestLoadDenBridgesMergesAndValidates (plan §7): a den serving its identity
// vault with live links to two other den vaults and a den bridge between those
// two vault ids — LoadDenBridges must merge the bridge into
// NSManager.CrossVaultBridges, cross-vault edge validation must accept a
// declared relation and reject an undeclared one, and federated traversal must
// still resolve nodes in the bridged vaults.
func TestLoadDenBridgesMergesAndValidates(t *testing.T) {
	links := "  - target: alpha\n    mode: live\n" +
		"  - target: beta\n    mode: live\n"
	eng, home := denLinkEngine(t, "mock-test", links)
	seedRemoteVault(t, filepath.Join(home, "dens", "alpha", "vault"), "alpha-vault", "alpha/core", "Alpha core API", "mock-test")
	seedRemoteVault(t, filepath.Join(home, "dens", "beta", "vault"), "beta-vault", "beta/core", "Beta core API", "mock-test")

	// Declare a den bridge between the two linked vaults' ids under the den
	// DIR (dens/acme/_bridges), sibling of the served vault.
	if _, added, err := den.AddBridge("acme", "alpha-vault", "beta-vault", []string{"calls", "reads"}); err != nil || !added {
		t.Fatalf("AddBridge: added=%v err=%v", added, err)
	}

	if err := eng.LoadDenLinks(); err != nil {
		t.Fatalf("LoadDenLinks: %v", err)
	}
	if err := eng.LoadDenBridges(); err != nil {
		t.Fatalf("LoadDenBridges: %v", err)
	}

	// The den bridge is merged into the manager's cross-vault bridges.
	if eng.NSManager == nil {
		t.Fatal("NSManager nil after LoadDenBridges")
	}
	found := false
	for _, b := range eng.NSManager.CrossVaultBridges {
		if b.SourceVaultID == "alpha-vault" && b.TargetVaultID == "beta-vault" {
			found = true
		}
	}
	if !found {
		t.Fatalf("den bridge not merged into CrossVaultBridges: %+v", eng.NSManager.CrossVaultBridges)
	}

	// Cross-vault edge validation honors the bridge: a declared relation is
	// allowed, an undeclared one is rejected.
	if err := eng.NSManager.ValidateCrossVaultEdge("alpha-vault", "beta-vault", "calls"); err != nil {
		t.Fatalf("declared relation should be allowed: %v", err)
	}
	if err := eng.NSManager.ValidateCrossVaultEdge("alpha-vault", "beta-vault", "writes"); err == nil {
		t.Fatal("undeclared relation must be rejected by the den bridge")
	}
	// Reverse orientation resolves the same bridge.
	if err := eng.NSManager.ValidateCrossVaultEdge("beta-vault", "alpha-vault", "reads"); err != nil {
		t.Fatalf("reverse-direction declared relation should be allowed: %v", err)
	}

	// Federated traversal still resolves nodes in the bridged vaults, and a
	// query returns @vault-id results from a linked vault.
	if n, ok := eng.graphResolver().GetNode("@beta-vault/beta/core"); !ok || n == nil {
		t.Fatal("graphResolver().GetNode(@beta-vault/beta/core) did not resolve across the den link")
	}
	if text := queryText(t, eng, "Alpha core API"); !strings.Contains(text, "@alpha-vault/alpha/core") {
		t.Fatalf("federated query missing @alpha-vault/alpha/core:\n%s", text)
	}
}

// TestLoadDenBridgesNonDenServeNoop: LoadDenBridges on a plain (non-den) served
// dir leaves the manager untouched.
func TestLoadDenBridgesNonDenServeNoop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MARMOT_HOME", home)
	t.Setenv("MARMOT_ROUTES", "off")
	vaultDir := filepath.Join(home, "plainvault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: plain\nnamespace: default\nembedding_provider: mock\nembedding_model: \"mock-test\"\n---\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(vaultDir, embedding.NewMockEmbedder("mock-test"))
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if err := eng.LoadDenBridges(); err != nil {
		t.Fatalf("LoadDenBridges on non-den serve: %v", err)
	}
	if eng.NSManager != nil {
		t.Fatalf("non-den serve must not create a namespace manager, got %+v", eng.NSManager)
	}
}

// TestLinksOnlyDenUsesDenIDForCrossVaultValidation (F18b): a links-only den
// has no identity vault, so LocalVaultID stays empty — cross-vault edge
// validation must fall back to the den id as the local identity so den
// bridges (@<den-id>--@<vault>) still gate edges instead of silently
// disengaging.
func TestLinksOnlyDenUsesDenIDForCrossVaultValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MARMOT_HOME", home)
	t.Setenv("MARMOT_ROUTES", "off")

	// Links-only den: _den.md in the den root, NO vault/ and NO _config.md.
	denRoot := filepath.Join(home, "dens", "solo")
	if err := os.MkdirAll(denRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nden_id: solo\nversion: 1\nlifetime: durable\nlinks:\n" +
		"  - target: lib\n    mode: live\n---\n"
	if err := os.WriteFile(filepath.Join(denRoot, "_den.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	seedRemoteVault(t, filepath.Join(home, "dens", "lib", "vault"), "lib-vault", "lib/core", "Library core API", "mock-test")

	// Den bridge: solo (the den id) <-> lib-vault, calls only.
	if _, added, err := den.AddBridge("solo", "solo", "lib-vault", []string{"calls"}); err != nil || !added {
		t.Fatalf("AddBridge: added=%v err=%v", added, err)
	}

	eng, err := NewEngine(denRoot, embedding.NewMockEmbedder("mock-test"))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	eng.WithVaultRegistry(namespace.NewVaultRegistry("", denRoot, nil, routes.EmptyTable()))
	if eng.LocalVaultID != "" {
		t.Fatalf("links-only den must have no LocalVaultID, got %q", eng.LocalVaultID)
	}
	if err := eng.LoadDenLinks(); err != nil {
		t.Fatalf("LoadDenLinks: %v", err)
	}
	if err := eng.LoadDenBridges(); err != nil {
		t.Fatalf("LoadDenBridges: %v", err)
	}

	if got := eng.localIdentity(); got != "solo" {
		t.Fatalf("localIdentity = %q, want den id fallback \"solo\"", got)
	}
	edgesOK := []node.Edge{{Target: "@lib-vault/lib/core", Relation: "calls"}}
	if err := eng.validateCrossVaultEdges(edgesOK, "default"); err != nil {
		t.Fatalf("declared relation must pass under the den-id identity: %v", err)
	}
	edgesBad := []node.Edge{{Target: "@lib-vault/lib/core", Relation: "writes"}}
	if err := eng.validateCrossVaultEdges(edgesBad, "default"); err == nil {
		t.Fatal("undeclared relation must be rejected — den-bridge validation disengaged for the links-only den")
	}
}
