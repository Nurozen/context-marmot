package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
)

// warrenEngine builds a hermetic engine whose workspace can mount warrens:
// MARMOT_ROUTES=off keeps the developer's real routing table out, the local
// vault gets a vault_id so LocalVaultID resolves, and the registry is
// created empty exactly as buildEngine now does.
func warrenEngine(t *testing.T) *Engine {
	t.Helper()
	t.Setenv("MARMOT_ROUTES", "off")
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: local-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(marmotDir, embedding.NewMockEmbedder("mock-test"))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	eng.WithVaultRegistry(namespace.NewVaultRegistry("local-vault", marmotDir, nil, routes.EmptyTable()))
	return eng
}

// warrenFixture creates a warren root with one project vault (config with
// vault_id, one node, a seeded real embeddings DB) and registers + mounts it
// in the engine's workspace.
func warrenFixture(t *testing.T, eng *Engine, warrenID, projectID, vaultID string) string {
	t.Helper()
	workspaceRoot := filepath.Dir(eng.MarmotDir)
	warrenRoot := t.TempDir()
	projDir := filepath.Join(warrenRoot, "projects", projectID, ".marmot")
	if err := os.MkdirAll(filepath.Join(projDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(projDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := warren.SaveProjectMetadata(projDir, &warren.ProjectMetadata{
		ProjectID: projectID,
		WarrenID:  warrenID,
		VaultID:   vaultID,
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	nodeContent := "---\nid: service/api\ntype: module\nnamespace: default\nstatus: active\n---\n\nService API\n"
	if err := os.MkdirAll(filepath.Join(projDir, "service"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "service", "api.md"), []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}
	seedEmbeddingDB(t, projDir, "service/api", "Service API")
	manifest := &warren.Manifest{
		WarrenID: warrenID,
		Projects: []warren.Project{{
			ProjectID: projectID,
			Path:      filepath.ToSlash(filepath.Join("projects", projectID, ".marmot")),
		}},
	}
	if err := warren.SaveManifest(warrenRoot, manifest, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if _, err := warren.RegisterWorkspaceWarren(workspaceRoot, warrenID, warrenRoot); err != nil {
		t.Fatalf("RegisterWorkspaceWarren: %v", err)
	}
	if _, err := warren.Mount(workspaceRoot, warrenID, []string{projectID}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return warrenRoot
}

func seedEmbeddingDB(t *testing.T, marmotDir, nodeID, text string) {
	t.Helper()
	embedder := embedding.NewMockEmbedder("mock-test")
	vec, err := embedder.Embed(text)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	store, err := embedding.NewStore(filepath.Join(marmotDir, ".marmot-data", "embeddings.db"))
	if err != nil {
		t.Fatalf("open embeddings db: %v", err)
	}
	defer func() { _ = store.Close() }()
	h := sha256.Sum256([]byte(text))
	if err := store.Upsert(nodeID, vec, hex.EncodeToString(h[:]), embedder.Model()); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
}

func knownVaults(eng *Engine) map[string]bool {
	out := make(map[string]bool)
	for _, id := range eng.VaultRegistry.KnownVaultIDs() {
		out[id] = true
	}
	return out
}

// TestReloadWarrenStateMountUnmount pins the freshness model: a mount made
// after engine startup becomes queryable after ReloadWarrenState, and an
// unmount disappears on the next reload.
func TestReloadWarrenStateMountUnmount(t *testing.T) {
	eng := warrenEngine(t)

	// Before any mounts: reload is a no-op, registry stays empty.
	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState (empty): %v", err)
	}
	if len(eng.VaultRegistry.KnownVaultIDs()) != 0 {
		t.Fatalf("expected no known vaults, got %v", eng.VaultRegistry.KnownVaultIDs())
	}

	warrenFixture(t, eng, "wp", "proj-a", "proj-a-vault")
	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState (mounted): %v", err)
	}
	if !knownVaults(eng)["proj-a-vault"] {
		t.Fatalf("expected proj-a-vault in registry after reload, got %v", eng.VaultRegistry.KnownVaultIDs())
	}

	// A context_query on the live engine reaches the newly mounted vault.
	res, err := eng.HandleContextQuery(context.Background(), makeCallToolRequest("context_query", map[string]any{
		"query": "Service API",
	}))
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "@proj-a-vault/service/api") {
		t.Fatalf("expected warren-mounted result in query output, got:\n%s", text)
	}

	// Unmount (rewrite the workspace _warren.md) and reload: gone.
	state, body, err := warren.LoadWorkspaceStateFromMarmot(eng.MarmotDir)
	if err != nil {
		t.Fatalf("LoadWorkspaceStateFromMarmot: %v", err)
	}
	entry := state.Warrens["wp"]
	entry.ActiveProjects = nil
	state.Warrens["wp"] = entry
	if err := warren.SaveWorkspaceStateToMarmot(eng.MarmotDir, state, body); err != nil {
		t.Fatalf("SaveWorkspaceStateToMarmot: %v", err)
	}
	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState (unmounted): %v", err)
	}
	if knownVaults(eng)["proj-a-vault"] {
		t.Fatalf("expected proj-a-vault evicted after unmount reload, got %v", eng.VaultRegistry.KnownVaultIDs())
	}
}

// TestReloadWarrenStateBridgeIdempotent: repeated reloads must not duplicate
// warren runtime bridges in NSManager.CrossVaultBridges (buildEngine used to
// append once; the reload path recomposes instead).
func TestReloadWarrenStateBridgeIdempotent(t *testing.T) {
	eng := warrenEngine(t)
	workspaceRoot := filepath.Dir(eng.MarmotDir)

	warrenRoot := warrenFixture(t, eng, "wp", "proj-a", "proj-a-vault")
	// Add a second project and a manifest bridge between them.
	projDir := filepath.Join(warrenRoot, "projects", "proj-b", ".marmot")
	if err := os.MkdirAll(filepath.Join(projDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: proj-b-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(projDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := warren.SaveProjectMetadata(projDir, &warren.ProjectMetadata{
		ProjectID: "proj-b", WarrenID: "wp", VaultID: "proj-b-vault",
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	manifest, body, err := warren.LoadManifest(warrenRoot)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	manifest.Projects = append(manifest.Projects, warren.Project{
		ProjectID: "proj-b",
		Path:      filepath.ToSlash(filepath.Join("projects", "proj-b", ".marmot")),
	})
	manifest.Bridges = []warren.Bridge{{Source: "proj-a", Target: "proj-b", Relations: []string{"references"}}}
	if err := warren.SaveManifest(warrenRoot, manifest, body); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if _, err := warren.Mount(workspaceRoot, "wp", []string{"proj-b"}, false); err != nil {
		t.Fatalf("Mount proj-b: %v", err)
	}

	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState #1: %v", err)
	}
	_, crossVault1 := eng.BridgeSnapshot()
	if len(crossVault1) != 1 {
		t.Fatalf("expected 1 cross-vault bridge after first reload, got %d", len(crossVault1))
	}
	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState #2: %v", err)
	}
	_, crossVault2 := eng.BridgeSnapshot()
	if len(crossVault2) != len(crossVault1) {
		t.Fatalf("cross-vault bridges not idempotent across reloads: %d then %d", len(crossVault1), len(crossVault2))
	}
}

// TestReloadWarrenStateNilRegistry: zero-value engines (unit tests) and
// engines without a registry are a safe no-op, not a nil panic.
func TestReloadWarrenStateNilRegistry(t *testing.T) {
	eng := &Engine{}
	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState on zero-value engine: %v", err)
	}
}

// TestReloadWarrenStateSerialized: reloads must be mutually exclusive.
// ReloadWarrenState is invoked concurrently from HTTP handler goroutines
// (the refresh endpoint) and the daemon owner's _warren.md watcher; without
// serialization, a reload that read pre-unmount state can apply its stale
// routing table after the reload that read post-unmount state, leaving an
// unmounted vault routable indefinitely. Pinned directly: a reload started
// while another holds the reload mutex must wait for it.
func TestReloadWarrenStateSerialized(t *testing.T) {
	eng := warrenEngine(t)
	warrenFixture(t, eng, "wp", "proj-a", "proj-a-vault")

	eng.reloadMu.Lock()
	done := make(chan error, 1)
	go func() { done <- eng.ReloadWarrenState() }()
	select {
	case err := <-done:
		t.Fatalf("ReloadWarrenState completed while another reload held the reload mutex (err=%v)", err)
	case <-time.After(150 * time.Millisecond):
		// Still blocked — serialized as required.
	}
	eng.reloadMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReloadWarrenState after unlock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReloadWarrenState never completed after the reload mutex was released")
	}
	if !knownVaults(eng)["proj-a-vault"] {
		t.Fatalf("expected proj-a-vault after serialized reload, got %v", eng.VaultRegistry.KnownVaultIDs())
	}
}

// TestReloadWarrenStateConcurrentQuery: reload in a loop while queries run —
// must be race-free (run under -race) and never panic.
func TestReloadWarrenStateConcurrentQuery(t *testing.T) {
	eng := warrenEngine(t)
	warrenFixture(t, eng, "wp", "proj-a", "proj-a-vault")
	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = eng.HandleContextQuery(context.Background(), makeCallToolRequest("context_query", map[string]any{
				"query": "Service API",
			}))
		}
	}()
	for i := 0; i < 25; i++ {
		if err := eng.ReloadWarrenState(); err != nil {
			t.Errorf("ReloadWarrenState #%d: %v", i, err)
			break
		}
	}
	close(stop)
	wg.Wait()
}

// addFixtureProject registers one more project vault (config + metadata +
// one node) in an existing warren fixture's manifest, without mounting it.
func addFixtureProject(t *testing.T, warrenRoot, warrenID, projectID, vaultID string) {
	t.Helper()
	projDir := filepath.Join(warrenRoot, "projects", projectID, ".marmot")
	if err := os.MkdirAll(filepath.Join(projDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(projDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := warren.SaveProjectMetadata(projDir, &warren.ProjectMetadata{
		ProjectID: projectID, WarrenID: warrenID, VaultID: vaultID,
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	manifest, body, err := warren.LoadManifest(warrenRoot)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	manifest.Projects = append(manifest.Projects, warren.Project{
		ProjectID: projectID,
		Path:      filepath.ToSlash(filepath.Join("projects", projectID, ".marmot")),
	})
	if err := warren.SaveManifest(warrenRoot, manifest, body); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
}

// setFixtureBridges replaces a warren fixture manifest's bridge declarations.
func setFixtureBridges(t *testing.T, warrenRoot string, bridges []warren.Bridge) {
	t.Helper()
	manifest, body, err := warren.LoadManifest(warrenRoot)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	manifest.Bridges = bridges
	if err := warren.SaveManifest(warrenRoot, manifest, body); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
}

// TestReloadWarrenStateSelfAliasSkipsRoute (R1.3): a mounted project whose
// vault_id equals the live vault's claims no route, and a previously poisoned
// registry entry (the pre-alias bug: local ID resolved to the warren copy)
// is evicted by the reload's Rebuild.
func TestReloadWarrenStateSelfAliasSkipsRoute(t *testing.T) {
	eng := warrenEngine(t)
	warrenRoot := warrenFixture(t, eng, "wp", "self-proj", "local-vault")

	// Simulate the poisoned pre-alias state: the registry resolves the local
	// vault ID to the warren copy and has it cached.
	copyPath := filepath.Join(warrenRoot, "projects", "self-proj", ".marmot")
	poisoned := routes.EmptyTable()
	poisoned.Set("local-vault", copyPath)
	eng.VaultRegistry.Rebuild(nil, poisoned)
	if _, err := eng.VaultRegistry.ResolveGraph("local-vault"); err != nil {
		t.Fatalf("seed poisoned vault: %v", err)
	}

	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState: %v", err)
	}
	// The self-alias mount claims no route...
	if knownVaults(eng)["local-vault"] {
		t.Fatalf("self-alias mount routed local-vault: %v", eng.VaultRegistry.KnownVaultIDs())
	}
	// ...and the cached poisoned self-vault was evicted (Rebuild eviction:
	// dirForLocked no longer resolves the local ID to the warren copy).
	if _, err := eng.VaultRegistry.ResolveGraph("local-vault"); err == nil {
		t.Fatal("registry still resolves the local vault after reload (stale warren-copy shadow)")
	}
}

// TestWarrenRuntimeBridgesSelfAliasEndpoint (R1.3): a manifest bridge between
// a self-alias and a foreign project synthesizes a runtime bridge whose alias
// endpoint is the workspace's own .marmot (live), while the vault IDs stay
// unchanged so edge validation still matches by ID.
func TestWarrenRuntimeBridgesSelfAliasEndpoint(t *testing.T) {
	eng := warrenEngine(t)
	workspaceRoot := filepath.Dir(eng.MarmotDir)
	warrenRoot := warrenFixture(t, eng, "wp", "proj-a", "proj-a-vault")
	addFixtureProject(t, warrenRoot, "wp", "self-proj", "local-vault")
	setFixtureBridges(t, warrenRoot, []warren.Bridge{{Source: "self-proj", Target: "proj-a", Relations: []string{"references"}}})
	if _, err := warren.Mount(workspaceRoot, "wp", []string{"self-proj"}, false); err != nil {
		t.Fatalf("Mount self-proj: %v", err)
	}

	mounts, err := warren.ActiveMounts(eng.MarmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	bridges, declared := warrenRuntimeBridges(eng.MarmotDir, mounts)
	if !declared {
		t.Fatal("manifest bridges must force policy enforcement on")
	}
	if len(bridges) != 1 {
		t.Fatalf("bridges = %+v, want exactly 1", bridges)
	}
	b := bridges[0]
	absMarmot, err := filepath.Abs(eng.MarmotDir)
	if err != nil {
		t.Fatal(err)
	}
	if b.SourceVaultID != "local-vault" {
		t.Fatalf("SourceVaultID = %q, want the (unchanged) local vault ID", b.SourceVaultID)
	}
	if b.SourceVaultPath != absMarmot {
		t.Fatalf("SourceVaultPath = %q, want the live workspace .marmot %q", b.SourceVaultPath, absMarmot)
	}
	if b.TargetVaultID != "proj-a-vault" || b.TargetVaultPath == absMarmot {
		t.Fatalf("target endpoint = %q/%q, want the warren copy of proj-a", b.TargetVaultID, b.TargetVaultPath)
	}
}

// TestWarrenRuntimeBridgesSkipsSelfToSelf (R1.3): a manifest bridge whose two
// endpoints both carry the local vault ID resolves to one vault — a
// self-bridge is meaningless and synthesizes nothing.
func TestWarrenRuntimeBridgesSkipsSelfToSelf(t *testing.T) {
	eng := warrenEngine(t)
	workspaceRoot := filepath.Dir(eng.MarmotDir)
	warrenRoot := warrenFixture(t, eng, "wp", "self-one", "local-vault")
	addFixtureProject(t, warrenRoot, "wp", "self-two", "local-vault")
	setFixtureBridges(t, warrenRoot, []warren.Bridge{{Source: "self-one", Target: "self-two", Relations: []string{"references"}}})
	// Two aliases of one live vault are coherent, not a conflict.
	if _, err := warren.Mount(workspaceRoot, "wp", []string{"self-two"}, false); err != nil {
		t.Fatalf("Mount self-two: %v", err)
	}

	mounts, err := warren.ActiveMounts(eng.MarmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	bridges, declared := warrenRuntimeBridges(eng.MarmotDir, mounts)
	if !declared {
		t.Fatal("manifest bridges must force policy enforcement on")
	}
	if len(bridges) != 0 {
		t.Fatalf("bridges = %+v, want none (self-to-self skipped)", bridges)
	}
}

func TestRuntimeBridgeKeyOrdering(t *testing.T) {
	if runtimeBridgeKey("a", "b") != runtimeBridgeKey("b", "a") {
		t.Fatal("runtimeBridgeKey should be order-independent")
	}
}

func TestEmptyNamespaceManager(t *testing.T) {
	mgr := emptyNamespaceManager("/tmp/vault")
	if mgr.VaultDir != "/tmp/vault" || mgr.Namespaces == nil || mgr.Bridges == nil {
		t.Fatalf("unexpected empty namespace manager: %+v", mgr)
	}
}

// TestWarrenRuntimeBridgesWarnsOnUnreadableManifest (A6 #5, moved here with
// the function in B2): an unreadable warren manifest used to silently remove
// cross-vault bridge policy enforcement; the fail-open control flow stays,
// but the dropped enforcement must be announced on stderr.
func TestWarrenRuntimeBridgesWarnsOnUnreadableManifest(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenRoot := t.TempDir()
	projDir := filepath.Join(warrenRoot, "projects", "project-a", ".marmot")
	if err := warren.SaveProjectMetadata(projDir, &warren.ProjectMetadata{
		ProjectID: "project-a", WarrenID: "wp", VaultID: "project-a",
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	if err := warren.SaveManifest(warrenRoot, &warren.Manifest{
		WarrenID: "wp",
		Projects: []warren.Project{{ProjectID: "project-a", Path: "projects/project-a/.marmot"}},
	}, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if _, err := warren.RegisterWorkspaceWarren(workspace, "wp", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := warren.Mount(workspace, "wp", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	mounts, err := warren.ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	// Truncate the warren manifest after the mounts were resolved.
	if err := os.WriteFile(filepath.Join(warrenRoot, "_warren.md"), []byte("---\nwarren_id: wp"), 0o644); err != nil {
		t.Fatalf("corrupt manifest: %v", err)
	}

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	var bridges []*namespace.Bridge
	var declared bool
	func() {
		defer func() { os.Stderr = old }()
		bridges, declared = warrenRuntimeBridges(marmotDir, mounts)
		_ = w.Close()
	}()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}

	// Fail-open control flow unchanged...
	if len(bridges) != 0 || declared {
		t.Fatalf("bridges = %v declared = %v, want fail-open empty", bridges, declared)
	}
	// ...but no longer silent.
	if !strings.Contains(string(out), "bridge manifest unreadable") || !strings.Contains(string(out), "NOT enforced") {
		t.Fatalf("expected bridge-policy warning on stderr, got %q", out)
	}
}
