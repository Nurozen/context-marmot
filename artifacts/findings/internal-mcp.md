# Findings

## internal/mcp

### engine.go:139-173
Both workstreams. `NewEngine` is the single engine construction point: it loads the graph **once** into memory (stale-graph bug across processes) and opens the SQLite embedding store via `embedding.NewStore(dbPath)` — the store that the quick fix must switch to WAL+busy_timeout. Daemon workstream: this is what the single owner constructs; proxies must NOT call it.

```go
149	g, err := graph.LoadGraph(ns)
...
159	dbPath := filepath.Join(dataDir, "embeddings.db")
160	es, err := embedding.NewStore(dbPath)
161	if err != nil {
162		return nil, fmt.Errorf("engine: open embedding store: %w", err)
163	}
...
171	eng.SetGraph(g)
```

### engine.go:30-52
Daemon workstream. Engine holds process-local state that is invalid under multi-process sharing: `atomic.Pointer[graph.Graph]` (in-memory graph cached per process), per-namespace `sync.Map` write locks (`nsMu`) that only serialize writes *within one process*, plus optional `SummaryScheduler`, `HeatMap`, `UpdateEngine`. All of these must live only in the daemon owner.

```go
32	graph            atomic.Pointer[graph.Graph]
...
40	SummaryScheduler *summary.Scheduler       // optional; nil = no async summaries
...
45	nsMu         sync.Map // map[string]*sync.Mutex — per-namespace write locks
```

### engine.go:96-134
Daemon workstream. `reindexNeighbors` spawns background goroutines (`e.reindexWG.Add(1); go ...`) calling `UpdateEngine.Reindex` with a 30s timeout — a background writer to the embedding DB from every process; also the quick-fix concern (concurrent writes → "database is locked" without WAL/busy_timeout).

```go
124	e.reindexWG.Add(1)
125	go func() {
126		defer e.reindexWG.Done()
...
132		_ = e.UpdateEngine.Reindex(ctx, neighborIDs)
133	}()
```

### engine.go:334-348
Both workstreams. `Engine.Close` drains reindexes then closes the EmbeddingStore and the VaultRegistry (which itself lazily opens *remote* embedding SQLite DBs — those also need WAL/busy_timeout). Daemon: owner shutdown/handoff must route through this.

```go
334	func (e *Engine) Close() error {
335		e.closing.Store(true)
...
339		if e.EmbeddingStore != nil {
340			if err := e.EmbeddingStore.Close(); err != nil {
...
344		if e.VaultRegistry != nil {
345			e.VaultRegistry.Close()
```

### server.go:246-259
Daemon workstream. The MCP transport surface: `ListenStdio(ctx, stdin, stdout)` takes arbitrary reader/writer and blocks until ctx cancel or EOF — this is the exact hook the proxy design needs (owner can serve a unix-socket conn as the reader/writer; proxy relays os.Stdin/Stdout to the socket). `Serve` is the plain os.Stdin/Stdout convenience wrapper used by the CLI today.

```go
248	func (s *Server) ListenStdio(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
249		stdio := server.NewStdioServer(s.mcpServer)
250		stdio.SetErrorLogger(log.New(io.Discard, "", 0))
251		return stdio.Listen(ctx, stdin, stdout)
252	}
...
256	func (s *Server) Serve(ctx context.Context) error {
257		stdio := server.NewStdioServer(s.mcpServer)
258		return stdio.Listen(ctx, nil, nil)
259	}
```

### handlers.go:60-100
Quick fix. Every `context_query` hits the embedding SQLite store (`Search`/`SearchActive`) — these are the readers whose SHARED lock a committing writer parks on (PENDING-lock freeze). Cross-vault path additionally opens/reads *other vaults'* embeddings.db via `VaultRegistry.ResolveEmbeddingStore(vid)` — a second SQLite open path that also needs WAL/busy_timeout.

```go
64		results, err = e.EmbeddingStore.Search(queryVec, topK, e.Embedder.Model())
...
80			remoteStore, err := e.VaultRegistry.ResolveEmbeddingStore(vid)
...
85				remoteResults, _ = remoteStore.Search(queryVec, 3, e.Embedder.Model())
```

### handlers.go:135-146
Daemon workstream. Heatmap is persisted with `heatmap.Save(e.MarmotDir, e.HeatMap)` on **every query** with >=2 result nodes (not only at exit) — multi-process last-writer-wins clobbering happens continuously, not just on shutdown. Must be owned by the single daemon.

```go
142		if len(resultIDs) >= 2 {
143			e.HeatMap.RecordCoAccess(resultIDs, heatmap.DefaultLearningRate)
144			// Persist heat data to disk so it survives restarts.
145			_ = heatmap.Save(e.MarmotDir, e.HeatMap)
146		}
```

### handlers.go:420-495
Both workstreams. `context_write` path: read-only cycle check against the **in-process** graph (`WouldCreateCycle`, line 425), graph upsert (438), node file save (443), then embedding `Upsert` into SQLite (466) — this is the concurrent-write that instantly fails with "database is locked" under two processes. Also notifies the per-process `SummaryScheduler` (480-483) and kicks background `reindexNeighbors(id)` (487). All graph/scheduler state here assumes single ownership.

```go
425			if e.GetGraph().WouldCreateCycle(id, edge.Target) {
...
438	if err := e.GetGraph().UpsertNode(n); err != nil {
...
466		if err := e.EmbeddingStore.Upsert(id, vec, summaryHash, e.Embedder.Model()); err != nil {
...
480	if e.SummaryScheduler != nil {
481		if metas, err := e.NodeStore.ListNodes(); err == nil {
482			e.SummaryScheduler.NotifyChange(len(metas))
...
487	e.reindexNeighbors(id)
```

### handlers.go:678-699
Both workstreams. `context_delete` mutates the in-process graph and writes to the embedding DB (`UpdateStatus`), then notifies the per-process scheduler — same multi-process stale-graph + SQLite-write exposure as write.

```go
688	if err := e.GetGraph().UpsertNode(updated); err != nil {
...
693	_ = e.EmbeddingStore.UpdateStatus(id, node.StatusSuperseded)
...
696	if e.SummaryScheduler != nil {
```

### handlers.go:797-821
Both workstreams. `context_tag` bulk-updates: `SaveNode` per node, in-memory graph upsert, and per-node embedding `Upsert` into SQLite — another burst-write pattern that will contend under WAL-less concurrent access.

```go
797		if err := e.NodeStore.SaveNode(diskNode); err != nil {
...
802		_ = e.GetGraph().UpsertNode(diskNode)
...
821				_ = e.EmbeddingStore.Upsert(diskNode.ID, vec, summaryHash, e.Embedder.Model())
```

### server_test.go:10-25, classify_test.go:23, query_context_test.go:31
Test impact for both workstreams. `testEngine` (server_test.go:19) calls `NewEngine(t.TempDir(), ...)` — opens a real on-disk embeddings.db per test, so the driver upgrade + WAL pragmas are exercised here. `classify_test.go:23` uses `embedding.NewStore(":memory:")` — verify the v0.33.x upgrade still supports the `:memory:` path string.

```go
19	eng, err := NewEngine(dir, embedder)
...
23	embStore, err := embedding.NewStore(":memory:")
```

### concurrency_test.go:13-40
Test impact / quick-fix relevance. `TestConcurrentWrites_SameNamespace` runs 20 concurrent `HandleContextWrite` goroutines — but only **in-process** (shared Engine, so `nsMu` serializes). It does NOT reproduce the multi-process lock failure; the daemon workstream needs a new multi-process/multi-connection e2e test, and this test is the template.

```go
13	func TestConcurrentWrites_SameNamespace(t *testing.T) {
14		eng := newClassifyTestEngine(t)
...
21	for i := 0; i < n; i++ {
```

### transport_test.go:29-45 and coverage_test.go:780-789
Test impact for daemon workstream. `setupTransport` wires a real MCP SDK client to the server over in-memory `io.Pipe` pairs simulating stdio — this pattern directly reuses for testing the stdio<->unix-socket proxy relay. `TestListenStdioReturnsOnEOF` (coverage_test.go:783) pins the current shutdown-on-EOF behavior that the proxy/owner handoff logic must preserve.

```go
31	func setupTransport(t *testing.T) *client.Client {
...
39	serverStdinR, serverStdinW := io.Pipe()
```
