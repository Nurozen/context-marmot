## internal/codemode

Package is a goja JS sandbox ("code mode") that wraps an injected `*mcpserver.Engine`. It never opens SQLite itself and has no process/signal/stdio/socket/lock-file code — but it is a direct consumer of the engine's in-memory graph and the embedding store, so it is affected by both workstreams (WS1: SearchActive hits embeddings.db; WS2: it reads the per-process cached graph, and its writes go through in-process-only namespace locks).

### internal/codemode/client_api.go:415-435
WS1 + WS2. `searchEntryNodes` (backing `client.search` and `client.query` fallback) calls `engine.EmbeddingStore.SearchActive`, i.e. a read against embeddings.db. Under the current v0.17.1 no-WAL setup this is one of the readers that can hold a SHARED lock while another process COMMITs (the PENDING-lock freeze), and the driver upgrade must keep this call path working. Note it also silently swallows errors (`err != nil` -> return nil), so "database is locked" errors here are invisible today. It also snapshots `engine.GetGraph()` — the per-process stale graph in the multi-process scenario.

```go
418	func searchEntryNodes(ctx context.Context, engine *mcpserver.Engine, query string, limit int) []ClientNode {
419		if engine == nil || engine.Embedder == nil || engine.EmbeddingStore == nil {
420			return nil
421		}
...
425		g := engine.GetGraph()
426		vec, err := embedWithContext(ctx, engine.Embedder, query)
...
433		results, err := engine.EmbeddingStore.SearchActive(vec, limit, engine.Embedder.Model())
434		if err != nil || len(results) == 0 {
435			return nil
436		}
```

### internal/codemode/client_api.go:66-72,138-142,186-297
WS2. Nearly every read method (`getNode`, `getNeighbors`, `getGraph`, `getNodesByTag/Type`, `getStats`, `getNamespaces`, `getOrphans`) is served from `engine.GetGraph()` — the in-memory graph loaded once per process. In the daemon design, code-mode must run inside the single owner (or the proxy must forward it), otherwise its answers go stale as other processes write nodes.

```go
70		engine := scope.engine
71		if engine == nil {
72			return fmt.Errorf("nil engine")
73		}
...
193			g := engine.GetGraph()
```

### internal/codemode/client_writes.go:281-309
WS2. Write mutations serialize via `engine.NamespaceLock(ns)` — a per-process in-memory mutex. This gives zero cross-process protection today (racing writes across multiple `marmot serve` processes); it only becomes correct once a single daemon owns the vault. Mutations flow through `curator.ExecuteCommand(..., scope.engine, ...)` (lines 135, 183), so file writes + graph updates are engine-scoped.

```go
281	func (s *runScope) lockNamespaces(ids []string) func() {
282		if s.engine == nil {
283			return func() {}
284		}
...
301		for _, ns := range ordered {
302			s.engine.NamespaceLock(ns).Lock()
303		}
```

### internal/codemode/executor.go:74-96,58-70
WS2. `NewExecutor(engine *mcpserver.Engine)` — code mode is constructed around whatever engine the caller (MCP curator chat) built. The daemon workstream must ensure only the owner process constructs this. `WriteContext.ReadOnly` (lines 68-70) is the existing knob for `marmot serve --read-only`; a proxy-mode serve could reuse it or must route code-mode writes to the owner.

```go
77	type Executor struct {
78		engine  *mcpserver.Engine
...
95	func NewExecutor(engine *mcpserver.Engine) *Executor {
96		return &Executor{engine: engine, timeout: DefaultTimeout}
97	}
```

### internal/codemode/executor.go:166-173
WS2 (minor). Each execution spawns a timeout goroutine + `time.AfterFunc` interrupting the goja runtime — background goroutines that must be drained if the owning daemon hands off/shuts down mid-execution.

```go
166		timer := time.AfterFunc(timeout, func() { rt.Interrupt("execution timeout") })
167		defer timer.Stop()
...
170		go func() {
```

### internal/codemode/writes_test.go:415-468 and executor_test.go:141
WS1 test impact. Tests build real engines via `mcpserver.NewEngine(marmotDir, emb)` and call `engine.EmbeddingStore.Upsert(...)` directly — so the driver upgrade (v0.17.1 -> v0.33.x) and WAL/busy_timeout change in internal/embedding/store.go is exercised (and could break) here. Also `client_api_test.go:334` relies on seeded embeddings for search. Single-connection use; WAL should be transparent, but any Upsert/Search API drift in the store surfaces in these tests.

```go
438		emb := embedding.NewMockEmbedder("mock-test")
439		engine, err := mcpserver.NewEngine(marmotDir, emb)
...
465			if err := engine.EmbeddingStore.Upsert(n.ID, vec, hash, engine.Embedder.Model()); err != nil {
```
