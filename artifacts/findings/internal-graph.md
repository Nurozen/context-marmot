# Findings

## internal/graph

No SQLite usage, process/signal handling, lock files, sockets, schedulers, or persistence exists in this package. It is a pure in-memory data structure. Relevance is indirect but real for Workstream 2 (single-owner daemon): the Graph is process-local state with no cross-process invalidation, which is the root of the "stale in-memory graph per serve process" bug. No impact on Workstream 1 (SQLite/WAL/driver upgrade) — this package never touches the DB.

### graph.go:23-32
Workstream 2: Graph is guarded only by an in-process sync.RWMutex. All mutation methods (AddNode/UpsertNode/RemoveNode/AddEdge/SupersedeNode) update only this process's maps; there is no persistence hook or cross-process notification. Any process that mutates via these methods diverges from every other process holding its own copy — exactly the staleness the daemon design fixes by making one process the sole owner of the Graph.

```go
23	// Graph is the in-memory graph engine. All methods are safe for concurrent
24	// read access, but writes must be externally serialised (or use the embedded
25	// mutex).
26	type Graph struct {
27		mu          sync.RWMutex
28		nodes       map[string]*node.Node // ALL nodes (active + superseded)
29		activeNodes map[string]*node.Node // active nodes only (Status == "active" or "")
30		outEdges    map[string][]node.Edge // source ID -> outbound edges
31		inEdges     map[string][]node.Edge // target ID -> inbound edges (with Target set to source)
32	}
```

### loader.go:15-20
Workstream 2: LoadGraph is a one-shot filesystem walk with no watch/reload mechanism. Called from mcp/engine.go:149 (once at NewEngine — the stale-graph source in each serve process), api/watcher.go:101 (full re-load on FS change, only in the process that runs the watcher), api/handlers.go:859, namespace/registry.go:126, and cmd/marmot/pipeline.go:757/853/1077. In the daemon design, only the owner should call this; proxy processes must not build their own graph. Also note LoadGraph on a nonexistent dir returns an error via filepath.Walk (relied on by namespace/registry_routes_test.go:129) — proxies skipping engine construction avoid that path entirely.

```go
15	func LoadGraph(store *node.Store) (*Graph, error) {
16		g := NewGraph()
17	
18		basePath := store.BasePath()
19	
20		err := filepath.Walk(basePath, func(path string, info os.FileInfo, walkErr error) error {
```

### loader.go:26-28
Minor, Workstream 2: the walk deliberately skips `.marmot-data` (where embeddings.db lives) and `_`-prefixed dirs (`_bridges`, `_heat`). The daemon's lock file / unix socket can safely live under `.marmot-data` or another dot-dir without polluting the graph.

```go
26			// Skip hidden dirs (.obsidian, .marmot-data) and system dirs (_bridges, _heat).
27			if path != basePath && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
28				return filepath.SkipDir
29			}
```

### graph.go:326-345
Workstream 2: SupersedeNode mutates node status only in memory and explicitly delegates disk persistence to callers. In multi-process mode a supersede done in one serve process is invisible to others until their next full LoadGraph; under single-owner this becomes safe because only the owner mutates and persists.

```go
326	// SupersedeNode marks oldID as superseded by newNode.ID, then upserts newNode into the graph.
327	// It updates the in-memory status of oldID and adds the new node.
328	// Callers are responsible for persisting both nodes to disk via the node store.
```

Tests in this folder (graph_test.go, temporal_test.go) are pure in-memory unit tests with no engine/DB/process dependencies; neither workstream should break them.
