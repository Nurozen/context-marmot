# internal/curator

## internal/curator

No direct SQLite opens, lock files, sockets, schedulers, signal handling, or process lifecycle code in this package. It is a pure library (UI slash commands, graph-quality suggestions, per-session undo) called from `internal/codemode` (client_writes.go) which is served by the UI/MCP process. Its relevance is indirect: it mutates nodes/graph through a passed-in `*mcp.Engine` and reads embeddings through a passed-in `*embedding.Store`, so both workstreams flow through it.

### commands.go:127-158

Workstream 2 (single-owner daemon). All curator mutations dispatch against a caller-supplied `*mcp.Engine` and mutate that engine's in-memory graph + node store on disk. In the multi-process world, a UI process running these commands updates only ITS engine's graph — other `marmot serve` processes keep stale graphs. Under the daemon design, `ExecuteCommand` must run in (or be proxied to) the owning daemon's engine; the API already takes the engine as a parameter, so no code change needed here, but callers must route to the owner.

```go
127	func ExecuteCommand(ctx context.Context, cmd *SlashCommand, engine *mcp.Engine, selectedNodes []string) (*CommandResult, error) {
...
138		case "tag":
139			return executeTag(ctx, cmd, engine, selectedNodes)
```

### commands.go:179-201 (pattern repeated at 237-257, 295-303, 332-427, 449-455, 493-514, 562-586)

Workstream 2. Every executor follows load-from-disk → mutate → `SaveNode` → `GetGraph().UpsertNode`. Disk write plus in-memory graph update on the local engine only; other processes' graphs and their embedding indexes are never invalidated. This is a concrete instance of the "stale in-memory graph" bug the daemon fixes. Also note `executeMerge` (316-427) does multi-node non-atomic writes (`SaveNode` on sources at 397, A at 415, `SoftDeleteNode` B at 421) — racy if two serve processes curate concurrently.

```go
179		diskNode, err := engine.NodeStore.LoadNode(engine.NodeStore.NodePath(n.ID))
...
198			if err := engine.NodeStore.SaveNode(diskNode); err != nil {
199				continue
200			}
201			_ = engine.GetGraph().UpsertNode(diskNode)
```

### suggestions.go:48 and 172-196

Workstream 1 (SQLite/WAL + driver upgrade). `Analyze` takes an `*embedding.Store` and `detectDuplicates` issues one `Embed` + `embedStore.Search(vec, 2, embedder.Model())` per node with a summary — a long burst of reads against embeddings.db. Under the current no-WAL/no-busy_timeout setup, this read loop is exactly the kind of long-held SHARED-lock reader that parks another process's COMMIT on the PENDING lock and wedges the vault. Errors are silently swallowed (`continue`), so lock errors just degrade suggestions with no signal. Uses only `Store.Search` — verify that method's Prepare/Bind/Step usage against the v0.33.x API during the upgrade.

```go
48	func Analyze(g *graph.Graph, ns *node.Store, embedStore *embedding.Store, embedder embedding.Embedder, opts AnalyzeOpts) []Suggestion {
...
173	func detectDuplicates(g *graph.Graph, nodes []*node.Node, embedStore *embedding.Store, embedder embedding.Embedder) []Suggestion {
174		if embedStore == nil || embedder == nil {
175			return nil
176		}
...
191		results, err := embedStore.Search(vec, 2, embedder.Model())
192		if err != nil {
193			continue
194		}
```

### undo.go:32-40

Workstream 2. `UndoStack` is purely in-memory, keyed by session ID, held per-process (constructed in `internal/codemode/executor.go:64` WriteContext). In the proxy model, undo state must live in the daemon that executes the writes; a proxy restart/handoff loses undo history — a lifecycle detail for the daemon design.

```go
32	type UndoStack struct {
33		mu     sync.Mutex
34		stacks map[string][]UndoEntry // keyed by session ID
35	}
```

### commands_test.go:208-224

Test impact for both workstreams. `setupTestEngine` constructs a bare `&mcp.Engine{...}` literal with `SetGraph` instead of `mcp.NewEngine`, and passes no embedding store (suggestions_test.go likewise calls `Analyze` with `nil` embedStore). These tests bypass SQLite entirely, so the WAL/driver upgrade won't break them; but if `mcp.Engine` construction changes for the daemon (e.g. fields become private or NewEngine gains lock/socket behavior), this struct-literal construction will need updating.

```go
208	func setupTestEngine(t *testing.T) *mcp.Engine {
...
219		eng := &mcp.Engine{
220			NodeStore: ns,
...
223		eng.SetGraph(g)
```
