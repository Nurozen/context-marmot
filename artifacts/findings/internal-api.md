# internal/api

## internal/api

### api.go:39-49
Workstream 2. The API server takes an already-built `*mcpserver.Engine` (constructed by CLI `ui` command). In a single-owner daemon design, this server must be constructed only in the owner process (or the daemon must expose these routes over the unix socket). Also `codemode.NewExecutor(engine)` binds a mutating executor to that engine instance.

```go
39	func NewServer(engine *mcpserver.Engine, assets fs.FS) *Server {
40		s := &Server{
41			engine:       engine,
42			assets:       assets,
43			undoStack:    curator.NewUndoStack(),
44			codeExecutor: codemode.NewExecutor(engine),
45		}
```

### api.go:113-115
Workstream 2. Blocking `http.ListenAndServe` with no `http.Server`/Shutdown handle — no graceful shutdown path for owner handoff; port binding also acts as an implicit single-instance constraint for the `ui` command only.

```go
113	func (s *Server) ListenAndServe(addr string) error {
114		return http.ListenAndServe(addr, s.Handler())
115	}
```

### watcher.go:19-95
Workstream 2. Background fsnotify watcher goroutine per Server that debounces .md changes and reloads the graph. Only the `ui` server has this; `marmot serve` MCP processes do not — root cause of stale in-memory graphs. In the daemon design this watcher should run once in the owner. Also note it only watches vault dir + immediate subdirs added at start (new subdirs created later are not watched).

```go
19	func (s *Server) StartWatcher(vaultDir string) (stop func(), err error) {
20		fw, err := fsnotify.NewWatcher()
...
44		stopCh := make(chan struct{})
45		go func() {
...
88				pending = false
89				s.reloadGraph(vaultDir)
```

### watcher.go:99-109
Workstream 2. `reloadGraph` rebuilds the whole graph from disk and swaps it via `engine.SetGraph` — the existing pattern for keeping in-memory graph fresh; the daemon owner can reuse this, but concurrent writers in other serve processes would still race with it today.

```go
99	func (s *Server) reloadGraph(vaultDir string) {
100		store := node.NewStore(vaultDir)
101		newGraph, err := graph.LoadGraph(store)
...
106		s.engine.SetGraph(newGraph)
...
108		s.NotifyChange()
109	}
```

### handlers.go:368-384
Workstream 1. Node update handler re-embeds and writes to the shared embeddings.db via `s.engine.EmbeddingStore.Upsert` — a write path that hits the "database is locked" failure when MCP serve processes hold connections. Error is silently discarded (`_ =`), so a lock failure loses the embedding update.

```go
368	if embeddingChanged && s.engine.Embedder != nil {
...
378		vec, err := s.engine.Embedder.Embed(embedText)
379		if err == nil {
380			h := sha256.Sum256([]byte(embedText))
381			summaryHash := hex.EncodeToString(h[:])
382			_ = s.engine.EmbeddingStore.Upsert(diskNode.ID, vec, summaryHash, s.engine.Embedder.Model())
383		}
```

### handlers.go:448-456
Workstream 1. Warren node update opens a *separate short-lived* `embedding.NewStore` on the mounted vault's embeddings.db, upserts, and closes. Another SQLite open site that inherits whatever pragmas `embedding.NewStore` sets — the WAL/busy_timeout quick fix in store.go automatically covers it, but note this can contend with the mounted project's own marmot processes.

```go
450	embStore, storeErr := embedding.NewStore(filepath.Join(mount.Path, ".marmot-data", "embeddings.db"))
451	if storeErr == nil {
452		h := sha256.Sum256([]byte(embedText))
453		_ = embStore.Upsert(diskNode.ID, vec, hex.EncodeToString(h[:]), s.engine.Embedder.Model())
454		_ = embStore.Close()
455	}
```

### handlers.go:560-580
Workstream 1. Search fans out reads to remote vault embedding stores via `VaultRegistry.ResolveEmbeddingStore(...).SearchActive(...)`. These are long-lived reader connections into other vaults' embeddings.db — exactly the reader/SHARED-lock population that a writer's COMMIT can wedge on under the current non-WAL setup. Errors are swallowed (`continue`), so lock errors silently drop results.

```go
565	remoteStore, err := s.engine.VaultRegistry.ResolveEmbeddingStore(vaultID)
566	if err != nil {
567		continue
568	}
569	remoteResults, err := remoteStore.SearchActive(vec, limit, s.engine.Embedder.Model())
```

### handlers.go:854-862
Workstream 2. Warren graph endpoint loads mounted vaults' graphs from disk on every request (`graph.LoadGraph` per mount) — no caching, but relevant as an existing pattern of per-request fresh reads vs the local vault's cached engine graph.

```go
854	for _, mount := range mounts {
855		if mount.WarrenID != id || !mount.Available {
856			continue
857		}
858		store := node.NewStore(mount.Path)
859		g, err := graph.LoadGraph(store)
```

### handlers.go:982-1033
Workstream 2. SSE machinery (`handleSSE`, `NotifyChange`) pushes graph-changed events to browser clients. In the daemon design, change notifications must originate from the single owner (watcher/scheduler) so UI clients of any process see updates; also a model for owner->proxy change signaling.

```go
1023	func (s *Server) NotifyChange() {
1024		s.version.Add(1)
1025		s.sseClients.Range(func(key, _ any) bool {
```

### chat_handlers.go:163-188
Workstream 2. Namespace locking uses `s.engine.NamespaceLock(ns)` — purely in-process sync.Mutex. Provides no cross-process protection today; under the single-owner daemon it becomes sufficient only because all mutations funnel through the owner. Any proxy design must not perform mutations locally or these locks are bypassed.

```go
180	for _, ns := range ordered {
181		s.engine.NamespaceLock(ns).Lock()
182	}
```

### chat_handlers.go:95-106
Workstream 2. Chat slash-commands mutate nodes through the curator and then call `s.NotifyChange()` — another mutation path (besides MCP tools and PUT /api/node) that must be owner-only in the daemon design.

```go
95	// If the command mutated nodes, push undo entry and notify SSE clients.
...
106		s.NotifyChange()
```

### api_test.go:94-98, 180-184, 344-348
Both workstreams (test impact). Tests build a real engine via `mcpserver.NewEngine(marmotDir, embedder)` and open `embedding.NewStore` directly on `.marmot-data/embeddings.db` *while the engine holds its own connection to the same file* (lines 180 and 1169 are in the same vault). Today that works because tests are read-mostly; after the WAL/busy_timeout change behavior stays fine, but a driver upgrade to v0.33.x could change error text/locking semantics these tests implicitly rely on. Daemon lock-file election must not block `NewEngine` in tests.

```go
94	engine, err := mcpserver.NewEngine(marmotDir, embedder)
...
180	embStore, err := embedding.NewStore(filepath.Join(marmotDir, ".marmot-data", "embeddings.db"))
```

No direct sqlite3 driver imports, lock files, PIDs, unix sockets, signal handling, heatmap saves, or summary schedulers exist in this package — those live in internal/embedding, internal/mcp, internal/summary, and cmd.
