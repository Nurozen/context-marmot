package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
	"github.com/nurozen/context-marmot/internal/warrenreg"
)

// denLinkEngine builds a hermetic den-shaped engine: MARMOT_HOME is a temp
// dir (so dens/ and warren-cache/ probes stay inside the fixture), the served
// dir is the den identity vault at <home>/dens/acme/vault, and _den.md one
// level up carries the given links YAML (indented two spaces per entry).
func denLinkEngine(t *testing.T, localModel, linksYAML string) (*Engine, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("MARMOT_HOME", home)
	t.Setenv("MARMOT_ROUTES", "off")

	denRoot := filepath.Join(home, "dens", "acme")
	vaultDir := filepath.Join(denRoot, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nden_id: acme\nversion: 1\nlifetime: durable\n"
	if linksYAML != "" {
		manifest += "links:\n" + linksYAML
	}
	manifest += "---\n"
	if err := os.WriteFile(filepath.Join(denRoot, "_den.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: acme-den\nnamespace: default\nembedding_provider: mock\nembedding_model: \"" + localModel + "\"\n---\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewEngine(vaultDir, embedding.NewMockEmbedder(localModel))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	eng.WithVaultRegistry(namespace.NewVaultRegistry("acme-den", vaultDir, nil, routes.EmptyTable()))
	return eng, home
}

// seedRemoteVault writes a minimal remote vault (config with the given
// embedding model, one node, a seeded embeddings DB tagged with that model)
// into vaultDir.
func seedRemoteVault(t *testing.T, vaultDir, vaultID, nodeID, text, model string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(vaultDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\nembedding_model: \"" + model + "\"\n---\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	nodePath := filepath.Join(vaultDir, filepath.FromSlash(nodeID)+".md")
	if err := os.MkdirAll(filepath.Dir(nodePath), 0o755); err != nil {
		t.Fatal(err)
	}
	nodeContent := "---\nid: " + nodeID + "\ntype: module\nnamespace: default\nstatus: active\n---\n\n" + text + "\n"
	if err := os.WriteFile(nodePath, []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}
	seedEmbeddingDBWithModel(t, vaultDir, nodeID, text, model)
}

// seedEmbeddingDBWithModel mirrors seedEmbeddingDB but tags rows with an
// arbitrary model string (mock vectors are model-independent, so cross-model
// hits are purely a function of the search's model tag — exactly what the
// per-link federation tests need to observe).
func seedEmbeddingDBWithModel(t *testing.T, marmotDir, nodeID, text, model string) {
	t.Helper()
	vec, err := embedding.NewMockEmbedder(model).Embed(text)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	store, err := embedding.NewStore(filepath.Join(marmotDir, ".marmot-data", "embeddings.db"))
	if err != nil {
		t.Fatalf("open embeddings db: %v", err)
	}
	defer func() { _ = store.Close() }()
	h := sha256.Sum256([]byte(text))
	if err := store.Upsert(nodeID, vec, hex.EncodeToString(h[:]), model); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
}

// seedCacheWarren builds a warren-cache-shaped shared checkout for warrenID
// with one project vault, and registers the warren in $MARMOT_HOME/warrens.yml
// so CacheWorkspaceWarren resolves it.
func seedCacheWarren(t *testing.T, home, warrenID, projectID, vaultID, nodeID, text, model string) string {
	t.Helper()
	checkout := filepath.Join(home, "warren-cache", "checkouts", warrenID)
	projVault := filepath.Join(checkout, "projects", projectID, ".marmot")
	seedRemoteVault(t, projVault, vaultID, nodeID, text, model)
	if err := warren.SaveProjectMetadata(projVault, &warren.ProjectMetadata{
		ProjectID: projectID, WarrenID: warrenID, VaultID: vaultID,
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	if err := warren.SaveManifest(checkout, &warren.Manifest{
		WarrenID: warrenID,
		Projects: []warren.Project{{
			ProjectID: projectID,
			Path:      "projects/" + projectID + "/.marmot",
		}},
	}, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if err := warrenreg.Update(func(reg *warrenreg.Registry) error {
		reg.Warrens[warrenID] = warrenreg.Entry{URL: "file:///dev/null"}
		return nil
	}); err != nil {
		t.Fatalf("warrenreg.Update: %v", err)
	}
	return checkout
}

func queryText(t *testing.T, eng *Engine, query string) string {
	t.Helper()
	res, err := eng.HandleContextQuery(context.Background(), makeCallToolRequest("context_query", map[string]any{
		"query": query,
	}))
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	return resultText(t, res)
}

// TestLoadDenLinksFederatesLiveAndPinned (§6): a live link to a second local
// den vault and a pinned link to a warren-cache checkout both feed the vault
// registry, and HandleContextQuery returns @vault-id results from both.
func TestLoadDenLinksFederatesLiveAndPinned(t *testing.T) {
	links := "  - target: lib\n    mode: live\n" +
		"  - target: wproto/protos\n    mode: link\n    warren: wproto\n    project: protos\n"
	eng, home := denLinkEngine(t, "mock-test", links)
	seedRemoteVault(t, filepath.Join(home, "dens", "lib", "vault"), "lib-vault", "lib/core", "Library core API", "mock-test")
	seedCacheWarren(t, home, "wproto", "protos", "proto-vault", "proto/schema", "Proto schema registry", "mock-test")

	if err := eng.LoadDenLinks(); err != nil {
		t.Fatalf("LoadDenLinks: %v", err)
	}
	vaults := knownVaults(eng)
	if !vaults["lib-vault"] || !vaults["proto-vault"] {
		t.Fatalf("KnownVaultIDs = %v, want lib-vault and proto-vault", eng.VaultRegistry.KnownVaultIDs())
	}

	if text := queryText(t, eng, "Library core API"); !strings.Contains(text, "@lib-vault/lib/core") {
		t.Fatalf("live-link result missing @lib-vault/lib/core:\n%s", text)
	}
	if text := queryText(t, eng, "Proto schema registry"); !strings.Contains(text, "@proto-vault/proto/schema") {
		t.Fatalf("pinned-link result missing @proto-vault/proto/schema:\n%s", text)
	}

	// Bridged traversal across the den link resolves qualified node IDs.
	if n, ok := eng.graphResolver().GetNode("@lib-vault/lib/core"); !ok || n == nil {
		t.Fatal("graphResolver().GetNode(@lib-vault/lib/core) did not resolve across the den link")
	}

	// Instructions carry the resolution state.
	snap := collectTopology(eng)
	if snap.Den == nil || len(snap.Den.Links) != 2 {
		t.Fatalf("topology den links = %+v, want 2", snap.Den)
	}
	for i, wantVID := range []string{"lib-vault", "proto-vault"} {
		if !snap.Den.Links[i].Resolved || snap.Den.Links[i].ResolvedVaultID != wantVID {
			t.Errorf("link %d = %+v, want resolved @%s", i, snap.Den.Links[i], wantVID)
		}
	}
	rendered := renderInstructions(snap)
	for _, want := range []string{
		"link: lib mode=live (resolved: @lib-vault)",
		"link: wproto/protos mode=link (resolved: @proto-vault)",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("instructions missing %q:\n%s", want, rendered)
		}
	}
}

// TestLoadDenLinksModelMismatchUsesRemoteEmbedder (§15.5): a remote store
// tagged with a different model returns nothing for the local query vector's
// model tag; the per-link path must re-embed the query with the remote
// vault's own configured model (mock vectors are text-only, so a hit proves
// the search ran under the remote model tag).
func TestLoadDenLinksModelMismatchUsesRemoteEmbedder(t *testing.T) {
	links := "  - target: lib\n    mode: live\n"
	eng, home := denLinkEngine(t, "mock-A", links)
	seedRemoteVault(t, filepath.Join(home, "dens", "lib", "vault"), "lib-vault", "lib/core", "Library core API", "mock-B")

	if err := eng.LoadDenLinks(); err != nil {
		t.Fatalf("LoadDenLinks: %v", err)
	}
	if text := queryText(t, eng, "Library core API"); !strings.Contains(text, "@lib-vault/lib/core") {
		t.Fatalf("model-mismatch federation missed @lib-vault/lib/core (per-link embedder not used):\n%s", text)
	}
}

// TestLoadDenLinksEmbeddingOverrideWins: a per-link embedding override
// (provider/model/key_ref env form) beats the remote vault's _config.md.
// The remote config declares mock-wrong; only the override's mock-C matches
// the store's tags, so a hit proves the override path was taken.
func TestLoadDenLinksEmbeddingOverrideWins(t *testing.T) {
	t.Setenv("MARMOT_TEST_EMBED_KEY", "unused-but-set")
	links := "  - target: lib\n    mode: live\n" +
		"    embedding:\n      provider: mock\n      model: mock-C\n      key_ref: env:MARMOT_TEST_EMBED_KEY\n"
	eng, home := denLinkEngine(t, "mock-A", links)
	remoteVault := filepath.Join(home, "dens", "lib", "vault")
	seedRemoteVault(t, remoteVault, "lib-vault", "lib/core", "Library core API", "mock-C")
	// Point the remote config at a model that would NOT match the store.
	cfg := "---\nversion: \"1\"\nvault_id: lib-vault\nnamespace: default\nembedding_provider: mock\nembedding_model: \"mock-wrong\"\n---\n"
	if err := os.WriteFile(filepath.Join(remoteVault, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := eng.LoadDenLinks(); err != nil {
		t.Fatalf("LoadDenLinks: %v", err)
	}
	if text := queryText(t, eng, "Library core API"); !strings.Contains(text, "@lib-vault/lib/core") {
		t.Fatalf("embedding override not honored (no @lib-vault hit):\n%s", text)
	}
}

// TestLoadDenLinksBadKeyRefSkipsVault: an unbuildable per-link embedder
// (unsupported key_ref form) degrades to skipping that vault — local results
// survive and no cross-vault rows appear for it.
func TestLoadDenLinksBadKeyRefSkipsVault(t *testing.T) {
	links := "  - target: lib\n    mode: live\n" +
		"    embedding:\n      provider: mock\n      model: mock-C\n      key_ref: file:/nope\n"
	eng, home := denLinkEngine(t, "mock-A", links)
	seedRemoteVault(t, filepath.Join(home, "dens", "lib", "vault"), "lib-vault", "lib/core", "Library core API", "mock-C")

	// Local node so the query has a surviving local result.
	if err := os.WriteFile(filepath.Join(eng.MarmotDir, "local-note.md"), []byte("---\nid: local-note\ntype: concept\nstatus: active\n---\n\nLibrary core API notes.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := graph.LoadGraph(eng.NodeStore)
	if err != nil {
		t.Fatalf("reload graph: %v", err)
	}
	eng.SetGraph(g)
	seedEmbeddingDBWithModel(t, eng.MarmotDir, "local-note", "Library core API notes.", "mock-A")

	if err := eng.LoadDenLinks(); err != nil {
		t.Fatalf("LoadDenLinks: %v", err)
	}
	text := queryText(t, eng, "Library core API")
	if strings.Contains(text, "@lib-vault/") {
		t.Fatalf("vault with unbuildable per-link embedder must be skipped:\n%s", text)
	}
	if !strings.Contains(text, "local-note") {
		t.Fatalf("local results must survive a skipped vault:\n%s", text)
	}
}

// TestLoadDenLinksUnresolvedShownInInstructions: a link whose target cannot
// be resolved stays out of the registry and renders as (unresolved).
func TestLoadDenLinksUnresolvedShownInInstructions(t *testing.T) {
	links := "  - target: ghost\n    mode: live\n"
	eng, _ := denLinkEngine(t, "mock-test", links)
	if err := eng.LoadDenLinks(); err != nil {
		t.Fatalf("LoadDenLinks: %v", err)
	}
	if ids := eng.VaultRegistry.KnownVaultIDs(); len(ids) != 0 {
		t.Fatalf("KnownVaultIDs = %v, want empty for an unresolved link", ids)
	}
	rendered := renderInstructions(collectTopology(eng))
	if !strings.Contains(rendered, "link: ghost mode=live (unresolved)") {
		t.Fatalf("instructions missing unresolved marker:\n%s", rendered)
	}
}

// TestLoadDenLinksEditModeUsesMountRouting: an edit-mode link resolves its
// vault id from the active warren mount for resolution state, but claims no
// den-vault registry entry — the editable mount participates in cross-vault
// reads through ReloadWarrenState's routing table instead.
func TestLoadDenLinksEditModeUsesMountRouting(t *testing.T) {
	links := "  - target: wsvc/svc\n    mode: edit\n    warren: wsvc\n    project: svc\n"
	eng, home := denLinkEngine(t, "mock-test", links)
	checkout := seedCacheWarren(t, home, "wsvc", "svc", "svc-vault", "svc/api", "Service API surface", "mock-test")

	// Register + activate the editable mount in the den vault's workspace
	// state (what `marmot den link --edit` records via EnsureEditableMount).
	if err := warren.UpdateWorkspaceStateInMarmot(eng.MarmotDir, func(state *warren.WorkspaceState) (bool, error) {
		state.Warrens["wsvc"] = warren.WorkspaceWarren{
			Path:             checkout,
			ActiveProjects:   []string{"svc"},
			EditableProjects: []string{"svc"},
		}
		return true, nil
	}); err != nil {
		t.Fatalf("UpdateWorkspaceStateInMarmot: %v", err)
	}

	if err := eng.LoadDenLinks(); err != nil {
		t.Fatalf("LoadDenLinks: %v", err)
	}
	if vid, ok := eng.DenLinkResolvedVaultID(den.Link{Target: "wsvc/svc", Mode: "edit", Warren: "wsvc", Project: "svc"}); !ok || vid != "svc-vault" {
		t.Fatalf("edit link resolution = (%q, %v), want svc-vault", vid, ok)
	}
	// No den-vault registry entry yet (extras skip edit mode)...
	if knownVaults(eng)["svc-vault"] {
		t.Fatalf("edit-mode link must not register a den vault: %v", eng.VaultRegistry.KnownVaultIDs())
	}
	// ...the mount reload path routes it, and queries reach it.
	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState: %v", err)
	}
	if !knownVaults(eng)["svc-vault"] {
		t.Fatalf("editable mount not routed after reload: %v", eng.VaultRegistry.KnownVaultIDs())
	}
	if text := queryText(t, eng, "Service API surface"); !strings.Contains(text, "@svc-vault/svc/api") {
		t.Fatalf("edit-mode vault missing from query results:\n%s", text)
	}
}

// TestResolveEmbeddingKeyRef pins the env-refs-only v1 contract.
func TestResolveEmbeddingKeyRef(t *testing.T) {
	t.Setenv("MARMOT_KEYREF_TEST", "sk-123")
	if got, err := resolveEmbeddingKeyRef("env:MARMOT_KEYREF_TEST"); err != nil || got != "sk-123" {
		t.Fatalf("env ref = (%q, %v), want sk-123", got, err)
	}
	if got, err := resolveEmbeddingKeyRef(""); err != nil || got != "" {
		t.Fatalf("empty ref = (%q, %v), want empty ok", got, err)
	}
	for _, bad := range []string{"file:/x", "env:", "keychain:foo"} {
		if _, err := resolveEmbeddingKeyRef(bad); err == nil {
			t.Errorf("ref %q should be rejected (env:VAR only)", bad)
		}
	}
	t.Setenv("MARMOT_KEYREF_UNSET", "")
	if _, err := resolveEmbeddingKeyRef("env:MARMOT_KEYREF_UNSET"); err == nil {
		t.Error("unset env var should be rejected with a clear error")
	}
}

// TestLoadDenLinksEditMountShadowsPinnedLink (F6): a den holding BOTH an
// edit link and a pinned link into the same warren project must serve the
// vault from the editable mount (the agent's own pending edits), not the
// shared-checkout snapshot the pinned link resolves to. LoadDenLinks skips
// the denVaults entry for the shadowed pinned link; the mount reload path
// routes the edit copy.
func TestLoadDenLinksEditMountShadowsPinnedLink(t *testing.T) {
	links := "  - target: wsvc/svc\n    mode: edit\n    warren: wsvc\n    project: svc\n" +
		"  - target: wsvc/svc\n    mode: link\n    warren: wsvc\n    project: svc\n"
	eng, home := denLinkEngine(t, "mock-test", links)
	// Shared cache checkout: the pinned snapshot (no pending node).
	seedCacheWarren(t, home, "wsvc", "svc", "svc-vault", "svc/api", "Service API surface", "mock-test")

	// Editable mount copy (simulating the edit worktree): same vault id, plus
	// a pending node that exists ONLY here.
	editCopy := filepath.Join(home, "warren-cache", "edits", "wsvc", "acme")
	projVault := filepath.Join(editCopy, "projects", "svc", ".marmot")
	seedRemoteVault(t, projVault, "svc-vault", "svc/api", "Service API surface", "mock-test")
	seedRemoteVault(t, projVault, "svc-vault", "svc/pending", "Pending edit only in the worktree", "mock-test")
	if err := warren.SaveProjectMetadata(projVault, &warren.ProjectMetadata{
		ProjectID: "svc", WarrenID: "wsvc", VaultID: "svc-vault",
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	if err := warren.SaveManifest(editCopy, &warren.Manifest{
		WarrenID: "wsvc",
		Projects: []warren.Project{{ProjectID: "svc", Path: "projects/svc/.marmot"}},
	}, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if err := warren.UpdateWorkspaceStateInMarmot(eng.MarmotDir, func(state *warren.WorkspaceState) (bool, error) {
		state.Warrens["wsvc"] = warren.WorkspaceWarren{
			Path:             editCopy,
			ActiveProjects:   []string{"svc"},
			EditableProjects: []string{"svc"},
		}
		return true, nil
	}); err != nil {
		t.Fatalf("UpdateWorkspaceStateInMarmot: %v", err)
	}

	if err := eng.LoadDenLinks(); err != nil {
		t.Fatalf("LoadDenLinks: %v", err)
	}
	// The pinned link still resolves (status/instructions), but claims NO
	// denVaults entry — the shared-checkout snapshot must not shadow the mount.
	if vid, ok := eng.DenLinkResolvedVaultID(den.Link{Target: "wsvc/svc", Mode: "link", Warren: "wsvc", Project: "svc"}); !ok || vid != "svc-vault" {
		t.Fatalf("pinned link resolution = (%q, %v), want svc-vault", vid, ok)
	}
	if knownVaults(eng)["svc-vault"] {
		t.Fatalf("shadowed pinned link registered a den vault: %v", eng.VaultRegistry.KnownVaultIDs())
	}

	// The mount reload path routes the EDIT copy; the pending edit is visible.
	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState: %v", err)
	}
	if !knownVaults(eng)["svc-vault"] {
		t.Fatalf("editable mount not routed after reload: %v", eng.VaultRegistry.KnownVaultIDs())
	}
	if text := queryText(t, eng, "Pending edit only in the worktree"); !strings.Contains(text, "@svc-vault/svc/pending") {
		t.Fatalf("pending edit invisible — the pinned snapshot shadowed the editable mount:\n%s", text)
	}
}
