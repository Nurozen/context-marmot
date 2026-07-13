# internal/traversal

Pure in-memory BFS/compaction library. No SQLite usage, no engine construction, no processes/signals/stdio, no lock files, sockets, goroutines, schedulers, or persistence. Nothing here needs code changes for either workstream, but two interfaces define how the (soon single-owner) engine's cached state is consumed, so they matter for the daemon design.

### traversal.go:15-33
Workstream 2 relevance: traversal reads the graph exclusively through the `GraphResolver` interface and heat exclusively through `TraversalConfig.HeatWeights` (a plain map). The stale-graph and heatmap last-writer-wins bugs live in whoever constructs these (mcp.NewEngine / heatmap persistence), not here — the daemon can fix them by supplying a fresh resolver/weights per query with no changes to this package.

```go
15	// GraphResolver abstracts graph node and edge resolution, allowing traversal
16	// across vault boundaries via cross-vault bridges.
17	type GraphResolver interface {
18		GetNode(id string) (*node.Node, bool)
19		GetEdges(id string, direction graph.Direction) []node.Edge
20	}
...
32		HeatWeights       map[string]float64 // PairKey -> weight; optional, nil = no heat boost
```

### bridged.go:10-22
Workstream 2 relevance: `BridgedGraphResolver` pulls remote-vault graphs via `VaultGraphProvider.ResolveGraph` (implemented by namespace.VaultRegistry). In the single-owner daemon, this provider is another in-memory graph cache whose refresh/ownership must live in the daemon; proxy processes must not build their own registry or the cross-vault graphs go stale independently.

```go
10	// VaultGraphProvider resolves graphs for remote vaults by vault ID.
11	// Implemented by namespace.VaultRegistry.
12	type VaultGraphProvider interface {
13		ResolveGraph(vaultID string) (*graph.Graph, error)
14	}
...
19	type BridgedGraphResolver struct {
20		Local  *graph.Graph
21		Vaults VaultGraphProvider // nil = single-vault mode
22	}
```

No other relevant findings: compact.go and all *_test.go files operate on in-memory graphs/heatmap maps only (no DB, no engine lifecycle); tests are unaffected by the SQLite/WAL upgrade or the daemon change.
