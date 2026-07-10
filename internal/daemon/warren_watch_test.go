package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
)

// TestGraphWatcherReloadsWarrenState (B3.3): the owner's fsnotify watcher no
// longer skips the workspace _warren.md — a mount performed by another
// process becomes visible in the live engine's vault registry within the
// debounce window, while unrelated underscore files never trigger a warren
// reload.
func TestGraphWatcherReloadsWarrenState(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".marmot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: local-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := mcp.NewEngine(dir, embedding.NewMockEmbedder("mock-test"))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	// Seed the registry with a stale route so the test can distinguish
	// "warren state rebuilt" (stale evicted, mount added) from "nothing
	// happened".
	rt := routes.EmptyTable()
	rt.Set("stale-vault", filepath.Join(workspace, "nonexistent"))
	eng.WithVaultRegistry(namespace.NewVaultRegistry("local-vault", dir, nil, rt))

	stop, err := StartGraphWatcher(dir, eng)
	if err != nil {
		t.Fatalf("StartGraphWatcher: %v", err)
	}
	t.Cleanup(stop)

	known := func() map[string]bool {
		out := make(map[string]bool)
		for _, id := range eng.VaultRegistry.KnownVaultIDs() {
			out[id] = true
		}
		return out
	}

	// Negative: an unrelated underscore file never triggers a warren reload.
	if err := os.WriteFile(filepath.Join(dir, "_summary.md"), []byte("# summary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // > debounce
	if !known()["stale-vault"] {
		t.Fatal("underscore file write must not rebuild the vault registry")
	}

	// Build a warren fixture and mount it — from the engine's point of view
	// this is another process writing dir/_warren.md.
	warrenRoot := t.TempDir()
	projDir := filepath.Join(warrenRoot, "projects", "proj-a", ".marmot")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projCfg := "---\nversion: \"1\"\nvault_id: proj-a-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(projDir, "_config.md"), []byte(projCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := warren.SaveProjectMetadata(projDir, &warren.ProjectMetadata{
		ProjectID: "proj-a", WarrenID: "wp", VaultID: "proj-a-vault",
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	if err := warren.SaveManifest(warrenRoot, &warren.Manifest{
		WarrenID: "wp",
		Projects: []warren.Project{{ProjectID: "proj-a", Path: "projects/proj-a/.marmot"}},
	}, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if _, err := warren.RegisterWorkspaceWarren(workspace, "wp", warrenRoot); err != nil {
		t.Fatalf("RegisterWorkspaceWarren: %v", err)
	}
	if _, err := warren.Mount(workspace, "wp", []string{"proj-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// The watcher must pick up the _warren.md write and reload warren state
	// within the debounce window (poll with a generous deadline for CI).
	deadline := time.Now().Add(10 * time.Second)
	for {
		k := known()
		if k["proj-a-vault"] && !k["stale-vault"] {
			return // reloaded: mount visible, stale route swapped out
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher never reloaded warren state; known vaults: %v", eng.VaultRegistry.KnownVaultIDs())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestGraphWatcherSelfAliasMountAddsNoRoute (R1.3): mounting a project whose
// vault_id equals the live vault's while the owner is live must NOT give the
// registry a route for the local vault ID — the alias serves from the live
// vault, so shadowing it with the warren copy is exactly the bug the skip
// removes. A foreign project mounted in the same write proves the reload
// itself fired.
func TestGraphWatcherSelfAliasMountAddsNoRoute(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".marmot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: local-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := mcp.NewEngine(dir, embedding.NewMockEmbedder("mock-test"))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	eng.WithVaultRegistry(namespace.NewVaultRegistry("local-vault", dir, nil, routes.EmptyTable()))

	stop, err := StartGraphWatcher(dir, eng)
	if err != nil {
		t.Fatalf("StartGraphWatcher: %v", err)
	}
	t.Cleanup(stop)

	// One warren with a foreign project (proves the reload) and a self
	// project (must be skipped).
	warrenRoot := t.TempDir()
	manifest := &warren.Manifest{WarrenID: "wp"}
	for projectID, vaultID := range map[string]string{"proj-a": "proj-a-vault", "self-proj": "local-vault"} {
		projDir := filepath.Join(warrenRoot, "projects", projectID, ".marmot")
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			t.Fatal(err)
		}
		projCfg := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\n---\n"
		if err := os.WriteFile(filepath.Join(projDir, "_config.md"), []byte(projCfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := warren.SaveProjectMetadata(projDir, &warren.ProjectMetadata{
			ProjectID: projectID, WarrenID: "wp", VaultID: vaultID,
		}, ""); err != nil {
			t.Fatalf("SaveProjectMetadata: %v", err)
		}
		manifest.Projects = append(manifest.Projects, warren.Project{
			ProjectID: projectID,
			Path:      filepath.ToSlash(filepath.Join("projects", projectID, ".marmot")),
		})
	}
	if err := warren.SaveManifest(warrenRoot, manifest, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if _, err := warren.RegisterWorkspaceWarren(workspace, "wp", warrenRoot); err != nil {
		t.Fatalf("RegisterWorkspaceWarren: %v", err)
	}
	if _, err := warren.Mount(workspace, "wp", []string{"proj-a", "self-proj"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	known := func() map[string]bool {
		out := make(map[string]bool)
		for _, id := range eng.VaultRegistry.KnownVaultIDs() {
			out[id] = true
		}
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		k := known()
		if k["proj-a-vault"] {
			// The reload happened: the foreign mount routed, the self-alias
			// did not (inverse of the proj-a-vault assertion).
			if k["local-vault"] {
				t.Fatalf("self-alias mount routed local-vault: %v", eng.VaultRegistry.KnownVaultIDs())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher never reloaded warren state; known vaults: %v", eng.VaultRegistry.KnownVaultIDs())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
