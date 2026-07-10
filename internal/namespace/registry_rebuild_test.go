package namespace

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/flock"
	"github.com/nurozen/context-marmot/internal/routes"
)

// routesFor builds a routing table mapping vault IDs to dirs.
func routesFor(pairs map[string]string) *routes.RoutingTable {
	rt := &routes.RoutingTable{Vaults: make(map[string]routes.VaultEntry)}
	for id, dir := range pairs {
		rt.Set(id, dir)
	}
	return rt
}

// TestRebuildKeepsUnchangedVaults: a Rebuild with the same routing keeps the
// cached embedding store (same pointer); re-routing the vault to a new dir
// evicts and closes the old store, and the next resolve loads the new dir.
func TestRebuildKeepsUnchangedVaults(t *testing.T) {
	vaultDir := setupRemoteVault(t, "rb-vault")
	seedRemoteEmbeddingDB(t, vaultDir)

	r := NewVaultRegistry("local", t.TempDir(), nil, routesFor(map[string]string{"rb-vault": vaultDir}))
	defer r.Close()

	store, err := r.ResolveEmbeddingStore("rb-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore: %v", err)
	}

	// Rebuild with the same routing: cache retained.
	r.Rebuild(nil, routesFor(map[string]string{"rb-vault": vaultDir}))
	store2, err := r.ResolveEmbeddingStore("rb-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore after same-route Rebuild: %v", err)
	}
	if store2 != store {
		t.Error("expected cached store pointer to survive a Rebuild with unchanged routing")
	}

	// Rebuild with the vault re-routed to a new dir: old store closed,
	// next resolve loads the new dir.
	newDir := setupRemoteVault(t, "rb-vault")
	seedRemoteEmbeddingDB(t, newDir)
	r.Rebuild(nil, routesFor(map[string]string{"rb-vault": newDir}))

	emb := embedding.NewMockEmbedder("mock-test")
	vec, _ := emb.Embed("A concept.")
	if _, err := store.SearchActive(vec, 1, emb.Model()); err == nil {
		t.Error("expected evicted store to be closed after re-route Rebuild")
	}

	store3, err := r.ResolveEmbeddingStore("rb-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore after re-route: %v", err)
	}
	if store3 == store {
		t.Error("expected a fresh store for the re-routed dir")
	}
	if _, err := store3.SearchActive(vec, 1, emb.Model()); err != nil {
		t.Errorf("search on re-routed store: %v", err)
	}

	// Rebuild with the vault gone entirely: evicted, resolve now errors.
	r.Rebuild(nil, routes.EmptyTable())
	if _, err := r.ResolveEmbeddingStore("rb-vault"); err == nil {
		t.Error("expected error resolving an unmounted vault after Rebuild")
	}
}

// TestRefreshSwapThenClose: a goroutine hammers SearchActive while Refresh
// runs repeatedly. Every iteration must either succeed with the seeded row
// or fail loudly with a closed-store error — never panic, never silently
// return empty success while rows exist.
func TestRefreshSwapThenClose(t *testing.T) {
	vaultDir := setupRemoteVault(t, "swap-vault")
	seedRemoteEmbeddingDB(t, vaultDir)

	r := NewVaultRegistry("local", t.TempDir(), nil, routesFor(map[string]string{"swap-vault": vaultDir}))
	defer r.Close()

	emb := embedding.NewMockEmbedder("mock-test")
	vec, _ := emb.Embed("A concept.")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			store, err := r.ResolveEmbeddingStore("swap-vault")
			if err != nil {
				t.Errorf("ResolveEmbeddingStore during Refresh: %v", err)
				return
			}
			results, err := store.SearchActive(vec, 5, emb.Model())
			if err != nil {
				// A closed-store error is the accepted bounded race; a
				// silent empty success is not.
				continue
			}
			if len(results) != 1 || results[0].NodeID != "concept-a" {
				t.Errorf("silent wrong result during Refresh: %+v", results)
				return
			}
		}
	}()

	for i := 0; i < 50; i++ {
		if err := r.Refresh("swap-vault"); err != nil && !errors.Is(err, ErrNotLoaded) {
			t.Fatalf("Refresh #%d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}

// TestResolveGraphTTL: with a 50ms TTL a cached remote graph reloads on the
// next access after expiry; with MARMOT_WARREN_TTL=off it never reloads.
func TestResolveGraphTTL(t *testing.T) {
	t.Setenv("MARMOT_WARREN_TTL", "50ms")
	vaultDir := setupRemoteVault(t, "ttl-vault")
	r := NewVaultRegistry("local", t.TempDir(), nil, routesFor(map[string]string{"ttl-vault": vaultDir}))
	defer r.Close()

	g, err := r.ResolveGraph("ttl-vault")
	if err != nil {
		t.Fatalf("ResolveGraph: %v", err)
	}
	if _, ok := g.GetNode("concept-b"); ok {
		t.Fatal("concept-b should not exist yet")
	}

	// Add a node file to the remote vault.
	nodeContent := "---\nid: concept-b\ntype: concept\nstatus: active\n---\n\nAnother concept.\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "concept-b.md"), []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Within the TTL the cached (stale) graph is returned.
	g2, err := r.ResolveGraph("ttl-vault")
	if err != nil {
		t.Fatalf("ResolveGraph within TTL: %v", err)
	}
	if _, ok := g2.GetNode("concept-b"); ok {
		t.Error("graph reloaded within the TTL window")
	}

	// After expiry the next access reloads.
	time.Sleep(80 * time.Millisecond)
	g3, err := r.ResolveGraph("ttl-vault")
	if err != nil {
		t.Fatalf("ResolveGraph after TTL: %v", err)
	}
	if _, ok := g3.GetNode("concept-b"); !ok {
		t.Error("graph not reloaded after the TTL expired")
	}

	// TTL expiry must never close a cached embedding store (carry-over).
	seedRemoteEmbeddingDB(t, vaultDir)
	store, err := r.ResolveEmbeddingStore("ttl-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := r.ResolveGraph("ttl-vault"); err != nil {
		t.Fatalf("ResolveGraph reload with store cached: %v", err)
	}
	store2, err := r.ResolveEmbeddingStore("ttl-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore after TTL reload: %v", err)
	}
	if store2 != store {
		t.Error("TTL reload dropped the cached embedding store")
	}
	emb := embedding.NewMockEmbedder("mock-test")
	vec, _ := emb.Embed("A concept.")
	if _, err := store2.SearchActive(vec, 1, emb.Model()); err != nil {
		t.Errorf("cached store unusable after TTL reload: %v", err)
	}
}

func TestResolveGraphTTLOff(t *testing.T) {
	t.Setenv("MARMOT_WARREN_TTL", "off")
	vaultDir := setupRemoteVault(t, "nottl-vault")
	r := NewVaultRegistry("local", t.TempDir(), nil, routesFor(map[string]string{"nottl-vault": vaultDir}))
	defer r.Close()

	if _, err := r.ResolveGraph("nottl-vault"); err != nil {
		t.Fatalf("ResolveGraph: %v", err)
	}
	nodeContent := "---\nid: concept-b\ntype: concept\nstatus: active\n---\n\nAnother concept.\n"
	if err := os.WriteFile(filepath.Join(vaultDir, "concept-b.md"), []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	g, err := r.ResolveGraph("nottl-vault")
	if err != nil {
		t.Fatalf("ResolveGraph: %v", err)
	}
	if _, ok := g.GetNode("concept-b"); ok {
		t.Error("MARMOT_WARREN_TTL=off must never expire the cached graph")
	}
}

// TestResolveEmbeddingStoreHoldsReadLock (B4): while a registry has a remote
// store open, the shared vault.read.lock is held (so `index --force` in that
// vault refuses); Refresh and Close release it.
func TestResolveEmbeddingStoreHoldsReadLock(t *testing.T) {
	vaultDir := setupRemoteVault(t, "lock-vault")
	seedRemoteEmbeddingDB(t, vaultDir)
	lockPath := filepath.Join(vaultDir, ".marmot-data", "vault.read.lock")

	r := NewVaultRegistry("local", t.TempDir(), nil, routesFor(map[string]string{"lock-vault": vaultDir}))
	if _, err := r.ResolveEmbeddingStore("lock-vault"); err != nil {
		t.Fatalf("ResolveEmbeddingStore: %v", err)
	}
	if _, ok, err := flock.TryExclusive(lockPath); err != nil {
		t.Fatalf("TryExclusive: %v", err)
	} else if ok {
		t.Fatal("expected the registry's shared read lock to block an exclusive lock")
	}

	// Refresh swaps the entry and closes the old store: the lock is released
	// until the store is lazily reopened.
	if err := r.Refresh("lock-vault"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if rel, ok, err := flock.TryExclusive(lockPath); err != nil || !ok {
		t.Fatalf("expected read lock released after Refresh: ok=%v err=%v", ok, err)
	} else {
		rel()
	}

	// Re-resolve (lock retaken), then Close releases it for good.
	if _, err := r.ResolveEmbeddingStore("lock-vault"); err != nil {
		t.Fatalf("ResolveEmbeddingStore (again): %v", err)
	}
	if _, ok, err := flock.TryExclusive(lockPath); err != nil || ok {
		t.Fatalf("expected read lock held after re-resolve: ok=%v err=%v", ok, err)
	}
	r.Close()
	if rel, ok, err := flock.TryExclusive(lockPath); err != nil || !ok {
		t.Fatalf("expected read lock released after Close: ok=%v err=%v", ok, err)
	} else {
		rel()
	}
}

// TestRefreshNotLoadedTolerated pins the ErrNotLoaded sentinel so callers
// (editable-write refresh, the refresh endpoint) can gate on errors.Is.
func TestRefreshNotLoadedTolerated(t *testing.T) {
	r := NewVaultRegistry("local", t.TempDir(), nil, routes.EmptyTable())
	defer r.Close()
	err := r.Refresh("never-loaded")
	if err == nil {
		t.Fatal("expected an error refreshing a never-loaded vault")
	}
	if !errors.Is(err, ErrNotLoaded) {
		t.Fatalf("Refresh error = %v, want errors.Is(err, ErrNotLoaded)", err)
	}
}
