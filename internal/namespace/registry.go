package namespace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/flock"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/routes"
)

// ErrNotLoaded reports a Refresh on a vault the registry has never loaded.
// Callers must treat it as "nothing cached, nothing to do", not a failure.
var ErrNotLoaded = errors.New("vault not loaded")

// defaultGraphTTL bounds how stale a cached remote graph may go before the
// next access reloads it. It is only the backstop for out-of-band changes
// (git pull in a warren checkout, another workspace's re-index) — in-band
// changes go through Engine.ReloadWarrenState or Refresh and take effect
// immediately.
const defaultGraphTTL = 60 * time.Second

// RemoteVault holds a lazily-loaded remote vault's graph and store.
type RemoteVault struct {
	VaultID   string
	VaultDir  string
	NodeStore *node.Store
	Graph     *graph.Graph
	Config    *config.VaultConfig
	EmbStore  *embedding.Store // lazily opened; used for cross-vault query bridging
	LoadedAt  time.Time

	// readLockRelease drops the shared vault.read.lock held while EmbStore
	// is open, advertising the open DB so a foreign `index --force` refuses
	// to delete it. Released alongside EmbStore in Close/Rebuild/Refresh.
	readLockRelease func()
}

// VaultRegistry manages lazy loading and caching of remote vault graphs
// for cross-vault bridge traversal. Resolution priority:
//  1. Den link vaults (explicit _den.md links — see SetDenVaults)
//  2. Global routing table (~/.marmot/routes.yml)
//  3. Bridge manifest paths (fallback)
//
// Cached graphs expire after graphTTL (lazily: the next access reloads). A
// cached embedding store is dropped alongside the graph on that same reload
// (and on Refresh) so a re-pinned checkout's new embeddings.db is picked up;
// the next ResolveEmbeddingStore reopens the current DB. Between reloads a
// search is a live SQLite read against the cached handle.
type VaultRegistry struct {
	mu           sync.RWMutex
	localVaultID string
	localDir     string
	vaults       map[string]*RemoteVault // vault_id -> loaded vault
	pathToID     map[string]string       // vault_path -> vault_id (from bridges)
	denVaults    map[string]string       // vault_id -> vault dir (den links; survives Rebuild)
	routingTable *routes.RoutingTable    // global routing table; may be nil
	graphTTL     time.Duration           // 0 = cached graphs never expire
}

// NewVaultRegistry creates a registry seeded with cross-vault bridge paths
// and an optional global routing table. The remote-graph TTL defaults to
// defaultGraphTTL and can be overridden with MARMOT_WARREN_TTL (a Go
// duration; "0"/"off"/"none" disables expiry).
func NewVaultRegistry(localVaultID, localDir string, bridges []*Bridge, rt *routes.RoutingTable) *VaultRegistry {
	r := &VaultRegistry{
		localVaultID: localVaultID,
		localDir:     localDir,
		vaults:       make(map[string]*RemoteVault),
		pathToID:     make(map[string]string),
		routingTable: rt,
		graphTTL:     graphTTLFromEnv(),
	}
	r.seedBridgePathsLocked(bridges)
	return r
}

// graphTTLFromEnv reads MARMOT_WARREN_TTL once (mirroring the MARMOT_ROUTES
// override pattern): empty = default, "0"/"off"/"none" = never expire, any
// other value is parsed as a Go duration (invalid values fall back to the
// default with a warning).
func graphTTLFromEnv() time.Duration {
	switch env := os.Getenv("MARMOT_WARREN_TTL"); env {
	case "":
		return defaultGraphTTL
	case "0", "off", "none":
		return 0
	default:
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			return d
		}
		fmt.Fprintf(os.Stderr, "warning: invalid MARMOT_WARREN_TTL %q; using default %s\n", env, defaultGraphTTL)
		return defaultGraphTTL
	}
}

// seedBridgePathsLocked pre-registers vault paths from bridge manifests.
// Caller must hold the write lock (or be the constructor). The local vault's
// own ID is never seeded: warren self-alias bridges carry the workspace's
// live .marmot as an endpoint path, and seeding it would let the registry
// resolve the local vault as a "remote" (a second read-only copy with TTL
// staleness) through the bridge fallback in dirForLocked.
func (r *VaultRegistry) seedBridgePathsLocked(bridges []*Bridge) {
	for _, b := range bridges {
		if !b.IsCrossVault() {
			continue
		}
		if b.SourceVaultID != "" && b.SourceVaultID != r.localVaultID && b.SourceVaultPath != "" {
			r.pathToID[b.SourceVaultPath] = b.SourceVaultID
		}
		if b.TargetVaultID != "" && b.TargetVaultID != r.localVaultID && b.TargetVaultPath != "" {
			r.pathToID[b.TargetVaultPath] = b.TargetVaultID
		}
	}
}

// SetDenVaults replaces the registry's den-link vault set (vault_id -> vault
// dir), the highest-precedence resolution source: an explicit den link WINS
// over a routing-table entry or bridge path for the same id (the den author
// pinned that vault deliberately; routes are ambient machine state). The set
// survives Rebuild — den links are engine-construction state, not warren
// reload state. Cached vaults whose resolved directory changes are evicted
// swap-then-close, mirroring Rebuild. The local vault id is never accepted
// (a den link to self must not shadow the live vault with a read-only copy).
func (r *VaultRegistry) SetDenVaults(vaults map[string]string) {
	var toClose []*RemoteVault
	r.mu.Lock()
	r.denVaults = make(map[string]string, len(vaults))
	for id, dir := range vaults {
		if id == "" || dir == "" || id == r.localVaultID {
			continue
		}
		r.denVaults[id] = dir
	}
	for id, rv := range r.vaults {
		if r.dirForLocked(id) != rv.VaultDir { // moved or dropped
			toClose = append(toClose, rv)
			delete(r.vaults, id)
		}
	}
	r.mu.Unlock()
	for _, rv := range toClose {
		closeRemoteVault(rv)
	}
}

// dirForLocked resolves a vault ID to its directory: den link vaults first,
// routing table second, bridge manifest paths last. Empty when unknown.
// Caller must hold a lock.
func (r *VaultRegistry) dirForLocked(vaultID string) string {
	if p, ok := r.denVaults[vaultID]; ok {
		return p
	}
	if r.routingTable != nil {
		if p, ok := r.routingTable.Get(vaultID); ok {
			return p
		}
	}
	for path, id := range r.pathToID {
		if id == vaultID {
			return path
		}
	}
	return ""
}

// expiredLocked reports whether a cached vault's graph is past the TTL.
func (r *VaultRegistry) expiredLocked(rv *RemoteVault) bool {
	return r.graphTTL > 0 && time.Since(rv.LoadedAt) > r.graphTTL
}

// Rebuild atomically replaces the registry's resolution inputs (bridge path
// hints + routing table) and evicts cached vaults whose directory changed or
// disappeared. Vaults whose directory is unchanged keep their cached graph
// and embedding store. Evicted embedding stores are closed AFTER the lock is
// released (swap-then-close) so a search that already resolved a store is
// never handed a just-closed handle by the registry itself. A search still
// holding an evicted store finishes safely (Store.Close serializes on the
// store's own mutex); a later search on that stale handle gets a clear
// closed-conn error, surfaced by the once-per-vault warnings — refcounting
// was rejected as not worth the complexity for a rare, bounded race.
func (r *VaultRegistry) Rebuild(bridges []*Bridge, rt *routes.RoutingTable) {
	var toClose []*RemoteVault
	r.mu.Lock()
	r.routingTable = rt
	r.pathToID = make(map[string]string)
	r.seedBridgePathsLocked(bridges)
	for id, rv := range r.vaults {
		if r.dirForLocked(id) != rv.VaultDir { // moved or unmounted
			toClose = append(toClose, rv)
			delete(r.vaults, id)
		}
	}
	r.mu.Unlock()
	for _, rv := range toClose {
		closeRemoteVault(rv)
	}
}

// closeRemoteVault releases an evicted vault's embedding store and its
// shared read lock. Must be called after the registry lock is released.
func closeRemoteVault(rv *RemoteVault) {
	if rv.EmbStore != nil {
		_ = rv.EmbStore.Close()
	}
	if rv.readLockRelease != nil {
		rv.readLockRelease()
	}
}

// Resolve returns the node for a cross-vault qualified ID.
// It lazily loads the remote vault's graph if not yet cached.
func (r *VaultRegistry) Resolve(vaultID, nodeID string) (*node.Node, bool) {
	g, err := r.ResolveGraph(vaultID)
	if err != nil {
		return nil, false
	}
	return g.GetNode(nodeID)
}

// ResolveGraph returns the graph for a remote vault, loading it lazily and
// reloading it when the cached copy is older than the graph TTL.
func (r *VaultRegistry) ResolveGraph(vaultID string) (*graph.Graph, error) {
	// Fast path: read lock.
	r.mu.RLock()
	if rv, ok := r.vaults[vaultID]; ok && !r.expiredLocked(rv) {
		r.mu.RUnlock()
		return rv.Graph, nil
	}
	r.mu.RUnlock()

	// Slow path: write lock, load (or TTL-reload) vault.
	r.mu.Lock()

	// Double-check after acquiring write lock.
	if rv, ok := r.vaults[vaultID]; ok && !r.expiredLocked(rv) {
		r.mu.Unlock()
		return rv.Graph, nil
	}

	vaultDir := r.dirForLocked(vaultID)
	if vaultDir == "" {
		r.mu.Unlock()
		return nil, fmt.Errorf("unknown vault %q: not in routing table or bridge manifests", vaultID)
	}

	g, evicted, err := r.loadVaultLocked(vaultID, vaultDir)
	r.mu.Unlock()
	if evicted != nil {
		closeRemoteVault(evicted)
	}
	return g, err
}

// loadVaultLocked loads (or TTL-reloads) a remote vault (caller must hold the
// write lock). It always builds a fresh entry with no embedding store: any
// store previously cached for this vault is returned as the evicted vault so
// the caller can close it AFTER releasing the lock (swap-then-close), and the
// next ResolveEmbeddingStore reopens the current DB. Dropping the store on
// reload is deliberate — after `marmot warren sync` re-pins a checkout and
// rewrites <checkout>/.marmot-data/embeddings.db, a carried-over handle would
// keep reading the stale (often unlinked) file forever. A search racing the
// swap keeps its own pointer and fails loudly with embedding.ErrStoreClosed
// once the handle closes, which is the accepted design. Old graphs are
// pointer-held by in-flight traversals and simply GC'd.
func (r *VaultRegistry) loadVaultLocked(vaultID, vaultDir string) (*graph.Graph, *RemoteVault, error) {
	cfg, err := config.Load(vaultDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load config for vault %q at %s: %w", vaultID, vaultDir, err)
	}

	store := node.NewStore(vaultDir)
	g, err := graph.LoadGraph(store)
	if err != nil {
		return nil, nil, fmt.Errorf("load graph for vault %q at %s: %w", vaultID, vaultDir, err)
	}

	rv := &RemoteVault{
		VaultID:   vaultID,
		VaultDir:  vaultDir,
		NodeStore: store,
		Graph:     g,
		Config:    cfg,
		LoadedAt:  time.Now(),
	}
	var evicted *RemoteVault
	if existing, ok := r.vaults[vaultID]; ok && (existing.EmbStore != nil || existing.readLockRelease != nil) {
		evicted = existing
	}
	r.vaults[vaultID] = rv

	return g, evicted, nil
}

// Refresh reloads a specific vault's graph (e.g., after an editable write or
// an explicit warren refresh). Swap-then-close: the replacement entry is
// built and swapped in under the lock, and the old embedding store is closed
// only after the lock is released, so the registry never hands a search a
// just-closed handle. The store reopens lazily on the next
// ResolveEmbeddingStore. A vault the registry never loaded returns
// ErrNotLoaded, which callers must treat as "nothing cached, nothing to do".
func (r *VaultRegistry) Refresh(vaultID string) error {
	r.mu.Lock()
	existing, ok := r.vaults[vaultID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("vault %q: %w", vaultID, ErrNotLoaded)
	}
	dir := existing.VaultDir
	delete(r.vaults, vaultID) // force loadVaultLocked to rebuild (no carry-over)
	_, _, err := r.loadVaultLocked(vaultID, dir)
	r.mu.Unlock()
	closeRemoteVault(existing)
	return err
}

// KnownVaultIDs returns all vault IDs from den links, bridges, and the
// routing table.
func (r *VaultRegistry) KnownVaultIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var ids []string
	for id := range r.denVaults {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range r.pathToID {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if r.routingTable != nil {
		for id := range r.routingTable.List() {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// ResolveConfig returns a remote vault's parsed _config.md (used by the
// per-link embedding federation to build a query embedder that matches the
// remote store's model). It shares the lazy load/TTL path with ResolveGraph.
func (r *VaultRegistry) ResolveConfig(vaultID string) (*config.VaultConfig, error) {
	r.mu.RLock()
	if rv, ok := r.vaults[vaultID]; ok && !r.expiredLocked(rv) && rv.Config != nil {
		r.mu.RUnlock()
		return rv.Config, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	if rv, ok := r.vaults[vaultID]; ok && !r.expiredLocked(rv) && rv.Config != nil {
		r.mu.Unlock()
		return rv.Config, nil
	}
	vaultDir := r.dirForLocked(vaultID)
	if vaultDir == "" {
		r.mu.Unlock()
		return nil, fmt.Errorf("unknown vault %q: not a den link, routing table entry, or bridge manifest path", vaultID)
	}
	_, evicted, err := r.loadVaultLocked(vaultID, vaultDir)
	if err != nil {
		r.mu.Unlock()
		if evicted != nil {
			closeRemoteVault(evicted)
		}
		return nil, err
	}
	cfg := r.vaults[vaultID].Config
	r.mu.Unlock()
	if evicted != nil {
		closeRemoteVault(evicted)
	}
	return cfg, nil
}

// ResolveEmbeddingStore returns an embedding store for a remote vault,
// loading it lazily. The store is read-only (used for search, not write).
func (r *VaultRegistry) ResolveEmbeddingStore(vaultID string) (*embedding.Store, error) {
	r.mu.RLock()
	if rv, ok := r.vaults[vaultID]; ok && rv.EmbStore != nil {
		r.mu.RUnlock()
		return rv.EmbStore, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock.
	if rv, ok := r.vaults[vaultID]; ok && rv.EmbStore != nil {
		return rv.EmbStore, nil
	}

	// Find vault directory.
	vaultDir := r.dirForLocked(vaultID)
	if vaultDir == "" {
		return nil, fmt.Errorf("unknown vault %q: not in routing table or bridge manifests", vaultID)
	}

	// Advertise the upcoming open with a shared read lock so a foreign
	// `index --force` refuses to delete the DB under this connection. Only
	// taken when the DB exists — a missing DB errors below anyway, and the
	// lock's MkdirAll must never fabricate .marmot-data inside a remote
	// checkout that has none. NON-BLOCKING (TryShared, not Shared): this
	// runs under r.mu, and a foreign `index --force` holds the exclusive
	// lock across its entire reindex (network embedding calls included) — a
	// blocking acquire here would wedge every registry operation, including
	// the daemon watcher's reloads, for minutes. Best-effort: a busy or
	// unlockable lock degrades to today's unguarded behavior (a warned open
	// without the read guard), not a failed search.
	dbPath := filepath.Join(vaultDir, ".marmot-data", "embeddings.db")
	var release func()
	if _, statErr := os.Stat(dbPath); statErr == nil {
		rel, ok, lockErr := flock.TryShared(filepath.Join(vaultDir, ".marmot-data", "vault.read.lock"))
		switch {
		case lockErr != nil:
			fmt.Fprintf(os.Stderr, "warning: vault %q shared read lock unavailable: %v\n", vaultID, lockErr)
		case !ok:
			fmt.Fprintf(os.Stderr, "warning: vault %q is being reindexed (exclusive vault.read.lock held); opening without the read guard\n", vaultID)
		default:
			release = rel
		}
	}

	// Open embedding store READ-ONLY: this is someone else's vault (often a
	// git checkout). A read-write open would flip its journal mode to WAL,
	// create -wal/-shm sidecars, and run schema migrations inside the remote
	// checkout — a cross-vault *read* must never mutate the remote DB. A
	// missing DB is an error (the vault was never indexed), not an empty
	// file silently created in the checkout.
	store, err := embedding.NewStoreReadOnly(dbPath)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, fmt.Errorf("open embedding store for vault %q: %w", vaultID, err)
	}

	// Cache on the RemoteVault entry (create if needed).
	rv, ok := r.vaults[vaultID]
	if !ok {
		// Load the graph too if not already loaded. No entry exists yet, so
		// loadVaultLocked evicts nothing (the second return is always nil).
		_, _, loadErr := r.loadVaultLocked(vaultID, vaultDir)
		if loadErr != nil {
			_ = store.Close()
			if release != nil {
				release()
			}
			return nil, loadErr
		}
		rv = r.vaults[vaultID] // loadVaultLocked caches it
	}
	rv.EmbStore = store
	rv.readLockRelease = release
	return store, nil
}

// Close releases resources held by cached remote vaults (embedding stores
// and their shared read locks), after the registry lock is dropped.
func (r *VaultRegistry) Close() {
	r.mu.Lock()
	vaults := r.vaults
	r.vaults = make(map[string]*RemoteVault)
	r.mu.Unlock()
	for _, rv := range vaults {
		closeRemoteVault(rv)
	}
}
