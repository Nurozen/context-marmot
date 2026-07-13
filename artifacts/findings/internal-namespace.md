# internal/namespace

## internal/namespace

### registry.go:221-241
Workstream 1 (quick fix): a second, independent call site that opens `embeddings.db` via `embedding.NewStore` — the WAL/busy_timeout fix in internal/embedding/store.go automatically covers it, but note this means remote vaults' SQLite files are opened by every serve process that does cross-vault queries, multiplying the multi-process lock contention (a `marmot serve` for vault B holds a writer store on the same file this registry opens read-only from vault A's process). Also relevant to workstream 2: in a single-owner daemon, the owner of vault A still opens vault B's DB directly rather than proxying to vault B's owner.

```go
221		// Open embedding store.
222		dbPath := filepath.Join(vaultDir, ".marmot-data", "embeddings.db")
223		store, err := embedding.NewStore(dbPath)
224		if err != nil {
225			return nil, fmt.Errorf("open embedding store for vault %q: %w", vaultID, err)
226		}
...
239		rv.EmbStore = store
240		return store, nil
```

### registry.go:75-142
Workstream 2 (daemon): another instance of the stale in-memory graph problem. Remote vault graphs are loaded once via `graph.LoadGraph` and cached in `r.vaults` forever (`LoadedAt` recorded at line 137 but nothing checks staleness automatically); `Refresh` (144-159) exists but is only invoked by callers on explicit staleness detection. A single-owner daemon design must decide whether the registry keeps caching remote graphs or delegates to remote owners.

```go
 76	func (r *VaultRegistry) ResolveGraph(vaultID string) (*graph.Graph, error) {
 77		// Fast path: read lock.
 78		r.mu.RLock()
 79		if rv, ok := r.vaults[vaultID]; ok {
 80			r.mu.RUnlock()
 81			return rv.Graph, nil
 82		}
...
126		g, err := graph.LoadGraph(store)
...
137			LoadedAt:  time.Now(),
138		}
139		r.vaults[vaultID] = rv
```

### registry.go:243-252
Lifecycle hook: `Close()` closes cached embedding stores. In workstream 2, owner shutdown/handoff must call this (and the proxy must never own one). Also relevant to workstream 1: with WAL, closing releases -wal/-shm cleanly; a killed process leaving these files behind is the failure path to test.

```go
244	func (r *VaultRegistry) Close() {
245		r.mu.Lock()
246		defer r.mu.Unlock()
247		for _, rv := range r.vaults {
248			if rv.EmbStore != nil {
249				_ = rv.EmbStore.Close()
250			}
251		}
252	}
```

### registry_embstore_test.go:1-102
Test impact (both workstreams): exercises `ResolveEmbeddingStore` caching and `Close()` against a real on-disk embeddings.db (`embedding.NewStore` path). Will exercise the new WAL/busy_timeout open pragmas and the upgraded driver; any v0.33.x API change in embedding.Store surfaces here too (lines 42, 51, 96 assert store caching and close behavior).

### namespace.go:23-24
Config surface note only: `NamespaceSettings.SummaryRegenerationInterval` and `EmbeddingModel` are per-namespace YAML settings feeding the summary.Scheduler / embedding pipeline. Workstream 2 must ensure only the daemon owner reads these to run schedulers; proxies must not.

```go
23	EmbeddingModel              string   `yaml:"embedding_model,omitempty"`
24	SummaryRegenerationInterval string   `yaml:"summary_regeneration_interval,omitempty"`
```

No process/signal/stdio handling, lock files, unix sockets, background goroutines, or MCP transport code in this package; concurrency is in-process only (sync.RWMutex).
