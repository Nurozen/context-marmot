package namespace

import (
	"fmt"
	"sync"
	"time"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// RemoteVault holds a lazily-loaded remote vault's graph and store.
type RemoteVault struct {
	VaultID   string
	VaultDir  string
	NodeStore *node.Store
	Graph     *graph.Graph
	Config    *config.VaultConfig
	LoadedAt  time.Time
}

// VaultRegistry manages lazy loading and caching of remote vault graphs
// for cross-vault bridge traversal.
type VaultRegistry struct {
	mu           sync.RWMutex
	localVaultID string
	localDir     string
	vaults       map[string]*RemoteVault // vault_id -> loaded vault
	pathToID     map[string]string       // vault_path -> vault_id
}

// NewVaultRegistry creates a registry seeded with cross-vault bridge paths.
func NewVaultRegistry(localVaultID, localDir string, bridges []*Bridge) *VaultRegistry {
	r := &VaultRegistry{
		localVaultID: localVaultID,
		localDir:     localDir,
		vaults:       make(map[string]*RemoteVault),
		pathToID:     make(map[string]string),
	}
	// Pre-register vault paths from bridge manifests.
	for _, b := range bridges {
		if !b.IsCrossVault() {
			continue
		}
		if b.SourceVaultID != "" && b.SourceVaultPath != "" {
			r.pathToID[b.SourceVaultPath] = b.SourceVaultID
		}
		if b.TargetVaultID != "" && b.TargetVaultPath != "" {
			r.pathToID[b.TargetVaultPath] = b.TargetVaultID
		}
	}
	return r
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

// ResolveGraph returns the graph for a remote vault, loading it lazily.
func (r *VaultRegistry) ResolveGraph(vaultID string) (*graph.Graph, error) {
	// Fast path: read lock.
	r.mu.RLock()
	if rv, ok := r.vaults[vaultID]; ok {
		r.mu.RUnlock()
		return rv.Graph, nil
	}
	r.mu.RUnlock()

	// Slow path: write lock, load vault.
	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock.
	if rv, ok := r.vaults[vaultID]; ok {
		return rv.Graph, nil
	}

	// Find vault path from registered bridges.
	var vaultDir string
	for path, id := range r.pathToID {
		if id == vaultID {
			vaultDir = path
			break
		}
	}
	if vaultDir == "" {
		return nil, fmt.Errorf("unknown vault %q: no bridge registered", vaultID)
	}

	return r.loadVaultLocked(vaultID, vaultDir)
}

// loadVaultLocked loads a remote vault (caller must hold write lock).
func (r *VaultRegistry) loadVaultLocked(vaultID, vaultDir string) (*graph.Graph, error) {
	cfg, err := config.Load(vaultDir)
	if err != nil {
		return nil, fmt.Errorf("load config for vault %q at %s: %w", vaultID, vaultDir, err)
	}

	store := node.NewStore(vaultDir)
	g, err := graph.LoadGraph(store)
	if err != nil {
		return nil, fmt.Errorf("load graph for vault %q at %s: %w", vaultID, vaultDir, err)
	}

	rv := &RemoteVault{
		VaultID:   vaultID,
		VaultDir:  vaultDir,
		NodeStore: store,
		Graph:     g,
		Config:    cfg,
		LoadedAt:  time.Now(),
	}
	r.vaults[vaultID] = rv

	return g, nil
}

// Refresh reloads a specific vault's graph (e.g., after detecting staleness).
func (r *VaultRegistry) Refresh(vaultID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.vaults[vaultID]
	if !ok {
		return fmt.Errorf("vault %q not loaded", vaultID)
	}

	_, err := r.loadVaultLocked(vaultID, existing.VaultDir)
	return err
}

// KnownVaultIDs returns all vault IDs registered from bridge manifests.
func (r *VaultRegistry) KnownVaultIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var ids []string
	for _, id := range r.pathToID {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// Close is a no-op currently but provides a hook for future cleanup.
func (r *VaultRegistry) Close() {}
