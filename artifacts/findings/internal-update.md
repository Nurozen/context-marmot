# internal/update

## internal/update

No direct SQLite usage in this folder — it only touches the embedding DB through the `EmbeddingStore` interface (`Upsert`). Driver upgrade (workstream 1) does not break anything here (tests use mocks). Main relevance is to workstream 2: this package is the background watcher/reindexer that must run only in the single-owner daemon, and its writes go straight to the SQLite embedding store.

### update.go:60-63
Workstream 1/2: the only embedding-DB touchpoint. `Reindex` -> `Upsert` is a SQLite write path; in the multi-process setup, each `marmot serve` with an UpdateEngine/Watcher can issue these writes concurrently against `embeddings.db` (the "database is locked" trigger). Interface is stable across the driver upgrade — no ncruces API used here.

```go
60	// EmbeddingStore abstracts embedding persistence.
61	type EmbeddingStore interface {
62		Upsert(nodeID string, embedding []float32, summaryHash string, model string) error
63	}
```

### update.go:209-234
Workstream 2: `Reindex` performs a sequence of SQLite upserts plus node-file saves (`SaveNode` at line 283) — racing writes if multiple processes run batch updates. In the daemon design only the owner should run this.

```go
209	func (e *Engine) Reindex(ctx context.Context, nodeIDs []string) *ReindexResult {
...
221		if err := e.reindexNode(id); err != nil {
```

### update.go:229-231 (with WithOnChange at 90-94)
Workstream 2: `onChange` callback fans out to whatever the host wires (e.g. heatmap/summary invalidation, graph reload). In multi-process mode only the process running the watcher sees the change; other processes' in-memory graphs (loaded once in `mcp.NewEngine`) go stale. Daemon owner must be the single subscriber.

```go
229	if e.onChange != nil && len(result.Updated) > 0 {
230		e.onChange(len(result.Updated))
231	}
```

### watcher.go:30-74
Workstream 2: `Watcher` is a background fsnotify goroutine started via `Start(ctx)`. It is constructed in `cmd/marmot/pipeline.go:870-876` (the `watch` CLI path). If multiple serve/watch processes each run a Watcher on the same vault, each fires `RunBatchUpdate` -> duplicate embed calls and racing SQLite/node writes. In the daemon design, only the lock-holding owner should construct/start this.

```go
69	func (w *Watcher) Start(ctx context.Context) {
70		w.mu.Lock()
71		w.started = true
72		w.mu.Unlock()
73		go w.run(ctx)
74	}
```

### watcher.go:128-139
Workstream 2: debounced batch update — the concrete write burst (detect + reindex/upsert) that will hit the shared SQLite DB from every watching process.

```go
128	func (w *Watcher) executeBatchUpdate(ctx context.Context) {
129		result, err := w.engine.RunBatchUpdate(ctx, w.config.PropagateDepth)
```

### watcher.go:143-157
Workstream 2: `Stop()` blocks until the run goroutine exits, then closes fsnotify — relevant to owner shutdown/handoff ordering (watcher must be stopped before releasing the vault lock). Safe for multiple calls via `stopOnce`.

```go
143	func (w *Watcher) Stop() error {
144		var closeErr error
145		w.stopOnce.Do(func() {
```

### update_test.go:113-128 / watcher_run_test.go
Test impact: all tests here use in-memory mocks (`mockEmbeddingStore`, mock embedder/graph) — no real SQLite, no ncruces import. Neither the WAL/busy_timeout change nor the driver upgrade affects these tests. Daemon workstream may add tests asserting Watcher is only started by the lock owner (currently no lock awareness anywhere in this package).

```go
128	func (m *mockEmbeddingStore) Upsert(nodeID string, embedding []float32, summaryHash string, model string) error {
```
