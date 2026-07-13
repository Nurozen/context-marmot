# Findings

## internal/indexer

No direct SQLite opens, process/signal handling, lock files, sockets, schedulers, or MCP transport in this folder. The indexer is fully dependency-injected; relevance is indirect (write paths that hit the shared SQLite embedding store, and CLI `index` engine construction that the daemon workstream must reconcile).

### runner.go:26-31
Workstream 1+2. Runner depends on an `EmbeddingStore` interface satisfied by `embedding.Store` (the SQLite-backed store). Every `marmot index` run performs writes (`Upsert`) against embeddings.db, so a concurrently running `marmot serve` process can trigger "database is locked" here today. WAL+busy_timeout in `internal/embedding/store.go` fixes this without any code change in this folder — the interface is decoupled from the driver, so the v0.17.1 -> v0.33.x upgrade does NOT break anything in internal/indexer.

```go
26	// EmbeddingStore is the subset of embedding.Store needed by the Runner.
27	type EmbeddingStore interface {
28		Upsert(nodeID string, emb []float32, summaryHash string, model string) error
29		StaleCheck(nodeID string, currentHash string) (bool, error)
30		FindSimilar(queryEmbedding []float32, threshold float64, model string) ([]embedding.ScoredResult, error)
31	}
```

### runner.go:87-107
Workstream 2. `NewRunner` is how the CLI `index` command builds its own indexing pipeline (nodeStore, embStore, embedder, classifier, graph) independent of any serve process. Under the single-owner daemon design, a standalone `marmot index` still opens embeddings.db for writing and mutates node files behind the owner's back — the owner's in-memory graph goes stale and the two processes contend on SQLite. The daemon plan must either proxy `index` to the owner or rely on WAL + file-watch invalidation.

```go
89	func NewRunner(
90		config RunnerConfig,
91		registry *Registry,
92		nodeStore NodeStore,
93		embStore EmbeddingStore,
94		embedder Embedder,
95		classifier Classifier,
96		graph GraphReader,
97	) *Runner {
```

### runner.go:358-372
Workstream 1. Upsert failures are silently counted, not surfaced per-node — with the current locked-DB failure mode an entire index run "succeeds" with `Errors > 0` and missing embeddings. After adding busy_timeout these become transient-retryable; worth noting for the quick fix's verification (RunResult.Errors is the only signal).

```go
358			if upsertErr := r.embStore.Upsert(item.nodeID, vec, summaryHash, r.embedder.Model()); upsertErr != nil {
359				errCount++
360			}
...
370			if upsertErr := r.embStore.Upsert(batch[j].nodeID, vec, summaryHash, r.embedder.Model()); upsertErr != nil {
371				errCount++
372			}
```

### runner.go:45-48, 84
Workstream 2. Runner's `GraphReader` is a read-only snapshot handed in at construction (used only by the classifier). If the daemon owns the live graph, a proxied index request should use the owner's graph rather than a freshly loaded one; no cache invalidation exists here.

```go
45	// GraphReader looks up existing nodes by ID (used by the Classifier).
46	type GraphReader interface {
47		GetNode(id string) (*node.Node, bool)
48	}
```

### indexer_test.go:59-76 (and edge_cases_test.go NewRunner call sites)
Test impact: all indexer tests use an in-memory `mockEmbedStore` (indexer_test.go:59) and mock embedders — none open real SQLite. Neither the WAL/upgrade change nor the daemon change should break these tests; only e2e tests that run real `marmot index` alongside `marmot serve` are affected.

```go
59	type mockEmbedStore struct {
...
76	func (m *mockEmbedStore) FindSimilar(queryEmbedding []float32, threshold float64, model string) ([]embedding.ScoredResult, error) {
```
