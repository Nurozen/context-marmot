# internal/graph findings

## internal/graph

Small, self-contained in-memory graph engine (graph.go 394 lines, loader.go 58, cycle.go 42). No warren, embedding, manifest, mount, copyDir, or daemon symbols exist here. Its relevance to the warren plan is indirect: it is what remote-vault registries cache (Tier 2 cache TTL / refresh-under-search) and its loader defines the skip rules the daemon watcher mirrors.

### graph.go:23-42 — Graph struct is internally RWMutex-protected (Tier 2: "refresh safe under concurrent search")
All read methods (GetNode, GetEdges, GetNeighbors, AllNodes, AllActiveNodes, NodeCount, EdgeCount) take `g.mu.RLock()`; mutators take `g.mu.Lock()`. So swapping node content in-place is safe, BUT there is no atomic whole-graph replace API — refresh implementations replace the *pointer* to a Graph (as daemon/owner.go:335 does with a freshly built graph), which is only safe if the holder field is itself synchronized. The plan's reloadWarrenState should build a new Graph via LoadGraph and atomically swap the reference, not mutate a live graph.

```go
23  // Graph is the in-memory graph engine. All methods are safe for concurrent
24  // read access, but writes must be externally serialised (or use the embedded
25  // mutex).
26  type Graph struct {
27      mu          sync.RWMutex
28      nodes       map[string]*node.Node // ALL nodes (active + superseded)
29      activeNodes map[string]*node.Node // active nodes only (Status == "active" or "")
30      outEdges    map[string][]node.Edge // source ID -> outbound edges
31      inEdges     map[string][]node.Edge // target ID -> inbound edges (with Target set to source)
32  }
```

### loader.go:15-58 — LoadGraph signature and skip rules (Tier 2 refresh; daemon watcher skip-rule parity)
Signature: `func LoadGraph(store *node.Store) (*Graph, error)`. Skips hidden dirs (`.`-prefixed), `_`-prefixed dirs and `_`-prefixed files (so `_warren.md` at vault root is never loaded as a node — this is the loader-side counterpart of the daemon owner-watcher skip at internal/daemon/owner.go:297-303 cited by the review; the two rule sets should stay consistent). Parse failures are swallowed with a log line and skipped — cheap and idempotent, safe to call repeatedly from a refresh endpoint.

```go
15  func LoadGraph(store *node.Store) (*Graph, error) {
...
26          // Skip hidden dirs (.obsidian, .marmot-data) and system dirs (_bridges, _heat).
27          if path != basePath && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
28              return filepath.SkipDir
29          }
...
32          // Skip underscore-prefixed files (e.g., _summary.md).
33          if strings.HasPrefix(info.Name(), "_") {
34              return nil
35          }
```

### Call sites of graph.LoadGraph (non-test) — where any refresh/TTL change must be wired
The `LoadedAt`/cache-forever behavior the review cites (warren_review.md:13, :92, :103) lives in the *callers*, not this package:

- internal/namespace/registry.go:126 — the remote-vault registry path; this is the "remote graphs cached forever" site (Tier 2 cache TTL fix lands here, not in internal/graph).
- internal/daemon/owner.go:335 — `newGraph, err := graph.LoadGraph(node.NewStore(dir))` — daemon owner already rebuilds+swaps; template for reloadWarrenState.
- internal/mcp/engine.go:149, cmd/marmot/pipeline.go:975/1077/1301, internal/api/handlers.go:859 — other loaders; unaffected but confirm no signature change is needed (plan should NOT change LoadGraph's signature; 7 call sites).

### Review-doc accuracy check
warren_review.md contains no direct claims about internal/graph itself; its remote-graph-cache claims correctly point at namespace/daemon code. No line drift or misread semantics attributable to this folder.

### Tests worth reusing
graph_test.go / temporal_test.go are pure in-memory unit tests (no filesystem/flock/e2e harness patterns) — nothing reusable for the warren test program beyond LoadGraph hermetic-vault setup: tests build vaults with `node.NewStore(t.TempDir())` style setup, a pattern usable for hermetic warren vault fixtures.
