# Findings

## internal/classifier

### classifier.go:23-37 — indirect embedding-store/graph consumer (context for both workstreams)
The classifier does not open SQLite itself; it consumes the embedding store via a narrow interface (`FindSimilar`) and the in-memory graph via `GraphReader`. Relevance: (1) Quick fix — any WAL/busy_timeout or v0.33.x driver changes are confined to `internal/embedding/store.go`; classifier code compiles unchanged as long as `FindSimilar`'s signature and `embedding.ScoredResult` are preserved. (2) Daemon — because it reads the graph through `GraphReader` (backed by the per-process in-memory graph loaded in `mcp.NewEngine`), classification in a non-owner process can resolve candidates against a stale graph; in the single-owner design the classifier must run only in the owner process.

```go
23	// EmbeddingStore is the subset of embedding.Store used by the classifier.
24	type EmbeddingStore interface {
25		FindSimilar(queryEmbedding []float32, threshold float64, model string) ([]embedding.ScoredResult, error)
26	}
...
34	// GraphReader allows the classifier to look up existing nodes by ID.
35	type GraphReader interface {
36		GetNode(id string) (*node.Node, bool)
37	}
```

### classifier.go:71-74 — SQLite errors silently swallowed as ADD
A `FindSimilar` failure (e.g. today's un-retried "database is locked" from the shared embeddings.db) is treated identically to "no similar nodes" and returns `ActionADD`. In the multi-process freeze scenario this converts lock contention into silently duplicated nodes rather than surfaced errors. Both workstreams should note this masking; after WAL/busy_timeout it becomes rare but is still not distinguished from an empty result.

```go
71	candidates, err := c.Store.FindSimilar(vec, SimilaritySearchThreshold, c.Embedder.Model())
72	if err != nil || len(candidates) == 0 {
73		return llm.ClassifyResult{Action: llm.ActionADD, Reasoning: "no similar nodes found"}, nil
74	}
```

### classifier_test.go:15-28 — tests fully mocked, unaffected by either change
Tests use in-memory `mockStore`/`mockEmbedder`; no SQLite, files, goroutines, signals, or sockets. Neither the driver upgrade nor the daemon work should break this test file.

```go
15	// mockStore is an in-memory mock for EmbeddingStore.
16	type mockStore struct {
17		results []embedding.ScoredResult
18		err     error
19	}
```
