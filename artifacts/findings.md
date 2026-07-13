# Findings: marmot serve multi-process lock fix

## Table of Contents

- [cmd-marmot-eval](#cmd-marmot-eval)
- [cmd-marmot](#cmd-marmot)
- [e2e](#e2e)
- [internal-api](#internal-api)
- [internal-classifier](#internal-classifier)
- [internal-codemode](#internal-codemode)
- [internal-config](#internal-config)
- [internal-curator](#internal-curator)
- [internal-embedding](#internal-embedding)
- [internal-graph](#internal-graph)
- [internal-heatmap](#internal-heatmap)
- [internal-indexer](#internal-indexer)
- [internal-llm](#internal-llm)
- [internal-mcp](#internal-mcp)
- [internal-namespace](#internal-namespace)
- [internal-node](#internal-node)
- [internal-routes](#internal-routes)
- [internal-sdkgen](#internal-sdkgen)
- [internal-summary](#internal-summary)
- [internal-traversal](#internal-traversal)
- [internal-update](#internal-update)
- [internal-verify](#internal-verify)
- [internal-warren](#internal-warren)
- [scripts](#scripts)
- [web](#web)

## cmd-marmot-eval

# Findings

## cmd/marmot-eval

### seeder.go:41-67
Workstream 1 & 2: the seeder builds its own engine directly via `mcpserver.NewEngine(vaultDir, emb)` — this opens the embeddings SQLite DB through internal/embedding/store.go, so it inherits the no-WAL/no-busy_timeout open and any v0.33.x driver-upgrade API fallout. For workstream 2, this is another out-of-process engine owner: if an eval run overlaps with a live `marmot serve` daemon on the same vault, it is a second writer that bypasses the daemon's lock/socket ownership (same class as `index`/`query` CLI commands). Note `defer eng.Close()` at line 67 — engine lifecycle assumptions here must survive the daemon refactor.

```go
41	func seedVault(questions []EvalQuestion, repo string, workDir string) (string, error) {
42		vaultDir := filepath.Join(workDir, "vaults", repo, ".marmot")
...
63		eng, err := mcpserver.NewEngine(vaultDir, emb)
64		if err != nil {
65			return "", fmt.Errorf("create engine: %w", err)
66		}
67		defer eng.Close()
```

### seeder.go:107-147
Workstream 2: the seeder calls engine tool handlers in-process (`eng.HandleContextWrite`, `eng.GetGraph().NodeCount()`), not via MCP transport. Any signature/ownership change to Engine (e.g. engine constructed only inside the daemon owner, or handlers requiring a running server) breaks this file.

```go
117		if _, err := eng.HandleContextWrite(ctx, repoReq); err != nil {
...
142			if _, err := eng.HandleContextWrite(ctx, req); err != nil {
...
147		fmt.Printf("  [seed] %s: %d nodes seeded\n", repo, eng.GetGraph().NodeCount())
```

### runner.go:38-48 and 80-89
Workstream 2 (directly reproduces the freeze pattern): runMCP and runHybrid each write an MCP config that spawns `marmot serve --dir <vaultDir>` as a stdio subprocess of the claude CLI. Conditions B and C run sequentially per question but each spawns a fresh serve process against the same seeded vault, and any parallelization (or an eval overlapping a user's serve) yields multiple `marmot serve` processes on one vault — exactly the multi-process SQLite contention scenario. After the daemon change, these spawned serves become proxies; the eval must still work when the spawned process is the lock-owning daemon AND when it's a proxy, and daemon shutdown-on-client-disconnect must fire when claude exits so vaults are not left owned between questions.

```go
40		mcpConfig := map[string]any{
41			"mcpServers": map[string]any{
42				"context-marmot": map[string]any{
43					"command": marmotBinary,
44					"args":    []string{"serve", "--dir", vaultDir},
45				},
46			},
47		}
```
(identical block at runner.go:81-88 in runHybrid)

### main.go:36 and main.go:67-68
Workstream 1 & 2 (build/wiring): the eval requires the prebuilt binary at `bin/marmot` (`make build`); driver upgrade (wazero/embed changes in ncruces v0.33.x) or new daemon flags for `serve` must keep this binary path and the plain `serve --dir` invocation working, or this harness breaks.

```go
36		marmotBinary := filepath.Join(repoRoot, "bin", "marmot")
...
67		if _, err := os.Stat(marmotBinary); os.IsNotExist(err) {
68			fmt.Fprintf(os.Stderr, "marmot binary not found at %s; run 'make build' first\n", marmotBinary)
```

### seeder.go:49-61
Workstream 1 (minor): embedder selection (OpenAI vs `embedding.NewMockEmbedder("eval-model")`) feeds the embedding store; any store schema/API change from the driver upgrade surfaces here first during seeding.

```go
53			emb, err = embedding.NewEmbedder("openai", "text-embedding-3-small", apiKey)
...
59			emb = embedding.NewMockEmbedder("eval-model")
```

## cmd-marmot

# findings

## cmd/marmot

### cmd/marmot/main.go:314-337
CLI wiring for `serve`. `cmdServe` -> `runServe` -> `runServePipeline`. This is where workstream 2's lock-file election / proxy decision must be inserted (before building an engine). Only flag is `--dir` (auto-discover via `discoverVault()` at lines 46-62 walks up looking for `.marmot/`), so a daemon socket/lock path can be derived per-vault from `dir`. No socket/lock/PID handling exists anywhere in cmd/marmot today.
```go
314	func cmdServe(args []string) int {
315		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
316		dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
...
331	func runServe(dir string) error {
332		if _, err := os.Stat(dir); os.IsNotExist(err) {
333			return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", dir)
334		}
336		return runServePipeline(dir)
337	}
```

### cmd/marmot/pipeline.go:450-466
`runServePipeline` — the exact function each MCP client process runs. Every invocation builds its own full engine (stale in-memory graph), starts its own summary.Scheduler, and serves stdio via `srv.ListenStdio(ctx, os.Stdin, os.Stdout)`. `defer result.Cleanup()` runs on client EOF (heatmap save, engine close). Workstream 2 replaces this body with: try lock -> owner path (this code + unix-socket listener) or proxy path (stdio<->socket relay). Note `ctx := context.Background()` — no signal handling here at all; owner shutdown/handoff logic must be added.
```go
450	// runServePipeline starts the MCP server on stdio.
451	func runServePipeline(dir string) error {
452		result, err := buildEngine(dir)
...
456		defer result.Cleanup()
457
458		ctx := context.Background()
459		if result.Scheduler != nil {
460			result.Scheduler.Start(ctx)
461		}
462
463		srv := mcpserver.NewServer(result.Engine)
464		fmt.Fprintln(os.Stderr, "ContextMarmot MCP server ready on stdio")
465		return srv.ListenStdio(ctx, os.Stdin, os.Stdout)
466	}
```

### cmd/marmot/pipeline.go:194-362
`buildEngine` — the single construction point for the full engine (mcpserver.NewEngine loads graph once at line 201; embedding DB is opened inside NewEngine, not here). Wires namespace manager, Warren mounts, vault registry, heatmap load (267-271), LLM classifier, summary.Scheduler creation (324-325), update engine (332). Used by serve, query, and ui — so ALL these commands independently open embeddings.db and hold stale graphs (multi-process contention root cause). Under workstream 2, only the daemon owner should call this; `query`/`ui` interplay must be decided (proxy to owner vs read-only local).
```go
196	func buildEngine(dir string) (*engineResult, error) {
197		embedder, err := loadEmbedder(dir)
...
201		engine, err := mcpserver.NewEngine(dir, embedder)
...
324		sumScheduler = summary.NewScheduler(sumEngine, sConfig, dir, nsName, nodeLoader)
325		engine.WithSummaryScheduler(sumScheduler)
```

### cmd/marmot/pipeline.go:343-353
Cleanup closure: unconditional `heatmap.Save` on every engine teardown — the last-writer-wins heatmap bug. Every serve/query/ui process saves its own copy on exit. Workstream 2 must make only the owner save; workstream 1 unaffected but note engine.Close() closes the SQLite conn.
```go
343	cleanup := func() {
344		if sumScheduler != nil {
345			sumScheduler.Stop()
346		}
347		if hm != nil {
348			if saveErr := heatmap.Save(dir, hm); saveErr != nil {
349				fmt.Fprintf(os.Stderr, "heatmap: save error: %v\n", saveErr)
350			}
351		}
352		_ = engine.Close()
353	}
```

### cmd/marmot/pipeline.go:58-68 (also 766-771, 858-863, 1062-1067)
Four direct `embedding.NewStore(dbPath)` call sites in cmd/marmot: `runIndexPipeline` (58-68, with `os.Remove(dbPath)` on --force at line 61 — dangerous if a daemon has the DB open in WAL mode), `runStatusPipeline` (766-771, read-only Count), `watchLoop` (858-863), `runStaticIndexPipeline` (1062-1067). All go through internal/embedding/store.go's bare Open, so workstream 1 (WAL + busy_timeout + driver upgrade) fixes them centrally with no cmd/marmot code change — but the `--force` delete and these extra writers running concurrently with a daemon are workstream-2 interplay points. Also note WAL leaves `-wal`/`-shm` sidecar files, so `os.Remove(dbPath)` alone becomes insufficient after workstream 1.
```go
58	dbPath := filepath.Join(dir, ".marmot-data", "embeddings.db")
59	if force {
60		// Remove existing embeddings DB to start fresh (model may have changed).
61		_ = os.Remove(dbPath)
62	}
64	embStore, err := embedding.NewStore(dbPath)
```

### cmd/marmot/pipeline.go:468-529
`runUIPipeline` — builds its own engine + starts its own scheduler (479-481) + file watcher (497) + HTTP server; competes with any serve daemon for the DB, graph and heatmap. Has the only signal handling in the package: SIGINT/SIGTERM goroutine at 519-526 that calls `os.Exit(0)` after `cancel()` — note it exits before `defer result.Cleanup()`/`stopWatcher()` run (heatmap never saved on Ctrl+C here; relevant when redesigning shutdown for workstream 2).
```go
519	go func() {
520		sigCh := make(chan os.Signal, 1)
521		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
522		<-sigCh
523		fmt.Fprintln(os.Stderr, "\nShutting down UI server...")
524		cancel()
525		os.Exit(0)
526	}()
528	return apiServer.ListenAndServe(addr)
```

### cmd/marmot/pipeline.go:827-889
`runWatchPipeline`/`watchLoop` — standalone process that opens the embedding store, loads its own graph, and runs an update.Watcher writing nodes/embeddings. A concurrent `marmot watch` alongside serve daemons is a direct writer-vs-reader lock hazard (workstream 1) and duplicates the daemon's watcher role (workstream 2 should route or forbid it when an owner exists). Uses signal.Notify(os.Interrupt, SIGTERM) + context cancel (833-839).
```go
858	dbPath := filepath.Join(dir, ".marmot-data", "embeddings.db")
859	embStore, err := embedding.NewStore(dbPath)
...
870	updateEng := update.NewEngine(store, g, embStore, embedder)
876	watcher, err := update.NewWatcher(updateEng, watchCfg)
881	watcher.Start(ctx)
```

### cmd/marmot/pipeline.go:146-173
`runQueryPipeline` — every `marmot query` builds a full engine (opening the SQLite DB and starting nothing but still loading graph/heatmap) just to run one tool call, then Cleanup() saves the heatmap. A CLI query racing a serve daemon's COMMIT is exactly the reproduced lock failure. Workstream 2 candidate: forward queries to the daemon socket when one exists.
```go
148	func runQueryPipeline(dir, query string, depth, budget int) error {
149		result, err := buildEngine(dir)
...
153		defer result.Cleanup()
155		res, err := result.Engine.HandleContextQuery(context.Background(), mcp.CallToolRequest{
```

### cmd/marmot/surface_coverage_test.go:138-152
Tests directly affected by workstream 2: `TestServeCommandEOF` feeds EOF stdin and expects `run(["serve", ...])` to return 0 immediately. Under the daemon design, serve will additionally create a lock file + unix socket in the temp vault; the test must still terminate on client EOF (owner shutdown when its client disconnects) and clean up socket/lock. `TestServeCommandNoVault` (147-151) expects exit 1 before any lock acquisition.
```go
138	func TestServeCommandEOF(t *testing.T) {
139		vault := initTestVault(t)
140		withStdin(t, "", func() {
141			if code := run([]string{"serve", "--dir", vault}); code != 0 {
```

### cmd/marmot/surface_coverage_test.go:244-255 and 427-453
`TestUIInvalidPort` exercises full ui wiring (buildEngine + scheduler) and notes the leaked signal goroutine constraint ("no test in this package may send SIGINT/SIGTERM"); daemon signal handling added by workstream 2 must respect this. `watchLoop` tests (427-453) drive the watcher with a cancellable context — they open embeddings.db and would exercise the WAL-mode path after workstream 1 (temp dirs, should be fine, but WAL sidecar files will appear).
```go
246	// without blocking. Note: this leaves a signal-wait goroutine alive, so no test
247	// in this package may send SIGINT/SIGTERM to the process.
```

### cmd/marmot/warren_test.go:363-380 and pipeline_warren_test.go:44-46
Tests that call `embedding.NewStore` directly (warren_test.go:363, on a remote vault DB) and `buildEngine` (warren_test.go:380, pipeline_warren_test.go:44) — here the test process and the engine open the same embeddings.db sequentially (Upsert then close before buildEngine per current flow); after the v0.33.x upgrade these are the in-package call sites to re-verify for Upsert/Close API compatibility (though the API surface used is internal/embedding's, not the driver's, so likely no change needed here).
```go
363	embStore, err := embedding.NewStore(filepath.Join(remoteMarmot, ".marmot-data", "embeddings.db"))
...
380	result, err := buildEngine(marmotDir)
```

### cmd/marmot/main.go:214-259 (index) and pipeline.go:1046-1150 (static index)
`marmot index` (both node and static-analysis modes) is another independent writer process: opens embeddings.db, writes nodes via indexer.Runner, upserts embeddings. Running `marmot index` while serve daemons hold readers is the reproduced instant-"database is locked" case; workstream 1 gives it retry via busy_timeout, workstream 2 must define whether index runs standalone (owner absent) or is refused/proxied when a daemon owns the vault.
```go
1062	dbPath := filepath.Join(dir, ".marmot-data", "embeddings.db")
1063	embStore, err := embedding.NewStore(dbPath)
...
1141	runner := indexer.NewRunner(runnerCfg, registry, nodeStore, embStore, embedder, idxClassifier, g)
```

No SQLite driver imports, unix sockets, lock files, or PID files exist in cmd/marmot; all SQLite access is via internal/embedding. Files with no relevant content: configure.go, setup.go, namespace.go, route.go, warren.go, sdk.go, flags.go (flags.go's `reorderInterspersedFlags` only matters if serve gains new flags like `--no-daemon`).

## e2e

## e2e

### e2e/e2e_test.go:56-74
Workstream 1 & 2. `seedProject` runs `marmot index` against a fresh temp vault and manually creates `.marmot/.marmot-data` (where `embeddings.db` lives). The SQLite driver upgrade / WAL change must keep working with a pre-created empty data dir; WAL will add `-wal`/`-shm` files here. For workstream 2, `index` builds its own engine — this seed step runs before any `serve` owner exists, so daemon logic must tolerate CLI commands owning the DB briefly.
```go
58	func seedProject(t *testing.T) string {
61		copyDir(t, "fixture/vault", filepath.Join(proj, ".marmot"))
65		if err := os.MkdirAll(filepath.Join(proj, ".marmot", ".marmot-data"), 0o755); err != nil {
69		out, err := runCLI(proj, "index", "--dir", ".marmot")
```

### e2e/e2e_test.go:101-106
Workstream 2. `hermeticEnv` points HOME at the project dir so spawned marmot processes never touch real `~/.marmot` state. Lock-file / unix-socket paths for the daemon must be derived from the vault or a HOME-relative dir so these tests remain hermetic; socket paths under a deep temp dir may also hit the ~104-byte macOS `sun_path` limit.
```go
104	func hermeticEnv(dir string) []string {
105		return append(os.Environ(), "HOME="+dir)
106	}
```

### e2e/e2e_test.go:208-257
Workstream 2, directly affected. `startMCP` spawns exactly one `marmot serve` per test over stdio JSON-RPC and does the MCP initialize handshake. Under the single-owner daemon, this process becomes the lock owner; a second `startMCP` in the same project would become a proxy — currently no test exercises two concurrent serves (the freeze scenario). Also relevant: shutdown is "close stdin, wait 5s, then kill" (lines 239-249) — the daemon's owner-shutdown-on-client-disconnect must exit within 5s of stdin close or these tests will start force-killing (leaking the lock file / socket).
```go
210		cmd := exec.Command(binPath, "serve", "--dir", ".marmot")
239		t.Cleanup(func() {
240			_ = stdin.Close()
245			case <-time.After(5 * time.Second):
246				_ = cmd.Process.Kill()
251		s.send(`{"jsonrpc":"2.0","id":0,"method":"initialize",...`)
```

### e2e/e2e_test.go:320-386
Workstream 1 & 2. `TestMCPServer` performs write → query → verify → tag → delete through one serve process, exercising embedding-store writes (`context_write`, `context_tag`, `context_delete`) against embeddings.db. Any driver-upgrade behavior change (WAL, busy_timeout, v0.33 API) surfaces here. For workstream 2, this test assumes the serve process it spawned directly answers tool calls — a proxy indirection must preserve identical newline-delimited JSON-RPC framing on stdout (recv at 266-287 skips non-matching lines but fatals if the stream closes).
```go
334		writeOut := s.callTool(2, "context_write", map[string]any{
375		delOut := s.callTool(6, "context_delete", map[string]any{"id": "auth/logout"})
```

### e2e/e2e_test.go:401-442
Workstream 2. `TestUIServer` runs `marmot ui` as a separate long-lived process that builds its own engine and opens the same embeddings.db while other commands could run — the exact multi-process pattern the daemon design must handle (ui either proxies to the owner or coexists via WAL). Shutdown uses SIGINT then kill after 5s (lines 414-424), so signal handling changes in serve/ui must keep honoring SIGINT. The 30s readiness poll (430-443) also constrains daemon startup/lock-acquisition latency.
```go
406		cmd := exec.Command(binPath, "ui", "--dir", ".marmot", "--port", fmt.Sprint(port), "--no-open")
415		_ = cmd.Process.Signal(syscall.SIGINT)
430		for i := 0; i < 150; i++ {
431			resp, err := client.Get(base + "/api/version")
```

### e2e/fixture/vault/_config.md:1-6
Both workstreams. Fixture vault config uses `embedding_provider: mock`; config surface for new options (e.g. daemon on/off, socket path) would be added alongside these keys and any new required key would need fixture updates.
```yaml
1	---
2	version: "1"
3	namespace: default
4	embedding_provider: mock
5	token_budget: 8192
6	---
```

Gap note: no e2e test spawns two concurrent `marmot serve` processes against one vault — the reproduced freeze scenario. Both workstreams will need a new e2e case here (concurrent writes for WAL/busy_timeout; owner+proxy election and handoff for the daemon). No SQLite APIs are used directly in this folder; everything goes through the built binary, so driver upgrade impact is behavioral only.

## internal-api

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

## internal-classifier

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

## internal-codemode

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

## internal-config

# internal/config

## internal/config

No SQLite usage, engine construction, process/signal/stdio handling, lock files, sockets, or schedulers live here. The package is pure config surface (parse/save `_config.md`, `.marmot-data/.env` keys, embedder factory). Three peripheral findings relevant to the daemon workstream:

### config.go:66-92
Workstream 2 (daemon): `Save` uses a fixed tmp filename (`_config.md.tmp`) + rename. With multiple `marmot serve` processes on the same vault, concurrent saves race on the same tmp path (interleaved WriteFile/Rename → possible torn/lost writes). Under single-owner daemon this becomes moot for serve, but other CLI commands (e.g. `marmot init`/config edits) could still race the owner. Also relevant as the natural home for any new config keys (e.g. daemon socket path, lock behavior).

```go
82	configPath := filepath.Join(vaultDir, "_config.md")
83	tmpPath := configPath + ".tmp"
84	if err := os.WriteFile(tmpPath, []byte(buf.String()), 0o644); err != nil {
85		return fmt.Errorf("write tmp config: %w", err)
86	}
87	if err := os.Rename(tmpPath, configPath); err != nil {
```

### config.go:159-192
Workstream 2 (daemon): `SaveDotEnv` does a non-atomic read-modify-write of `.marmot-data/.env` (no tmp+rename, no lock) — same multi-process last-writer-wins class of bug as the heatmap save. `.marmot-data` is the same directory that holds `embeddings.db`, so any daemon lock file / socket placed there coexists with this code path.

```go
162	envPath := filepath.Join(vaultDir, ".marmot-data", ".env")
...
191	return os.WriteFile(envPath, []byte(buf.String()), 0o600)
```

### embedder.go:13-39
Both workstreams (context): `NewEmbedderFromVault` is called by every engine construction (each `marmot serve` builds its own embedder → its own embedding store → its own SQLite connection). It writes diagnostics to stderr only (safe for stdio MCP transport). Under the single-owner daemon, proxy processes should skip this entirely; note the callers in cmd/mcp when refactoring engine ownership.

```go
13	func NewEmbedderFromVault(cfg *VaultConfig) (embedding.Embedder, error) {
...
33		fmt.Fprintln(os.Stderr, "embedding: using mock embedder (lexical only)")
```

No SQLite opens, go-sqlite3 imports, or driver API usage in this package — workstream 1 (WAL/busy_timeout/driver upgrade) does not touch it. `config_test.go` exercises only parse/save/env logic and is unaffected by either change.

## internal-curator

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

## internal-embedding

# internal/embedding

## internal/embedding

### store.go:39-54
Workstream 1 (quick fix) — this is THE SQLite open site. Bare `sqlite3.Open(dbPath)`: no WAL, no busy_timeout, no open flags. The quick fix (journal_mode(WAL) + busy_timeout(5000)) goes here, either via PRAGMA Exec right after open or via `sqlite3.OpenFlags`/URI params. Also the only place `sqlite3.Open` is called in this package; call sites elsewhere (internal/namespace/registry.go:223, internal/mcp/engine.go:160, internal/api/handlers.go:450, cmd/marmot/pipeline.go:64/768/859/1063) all funnel through `NewStore`, so one change covers all processes.

```go
41	func NewStore(dbPath string) (*Store, error) {
42		db, err := sqlite3.Open(dbPath)
43		if err != nil {
44			return nil, fmt.Errorf("open sqlite: %w", err)
45		}
46
47		s := &Store{db: db}
48		if err := s.initSchema(); err != nil {
49			_ = db.Close()
50			return nil, fmt.Errorf("init schema: %w", err)
51		}
52
53		return s, nil
54	}
```

### store.go:3-14
Workstream 1 — imports to audit for the v0.17.1 → v0.33.x upgrade: `github.com/ncruces/go-sqlite3` plus the blank `embed` import (bundled WASM sqlite; new versions ship a newer wazero + sqlite build, so go.mod/go.sum both change). go.mod currently pins v0.17.1 (go.mod:9) with `ncruces/julianday v1.0.0` indirect.

```go
12		"github.com/ncruces/go-sqlite3"
13		_ "github.com/ncruces/go-sqlite3/embed"
14	)
```

### store.go:34-37
Workstream 1 — `*sqlite3.Conn` stored directly; a `sync.Mutex` guards ALL access because Conn is not goroutine-safe. This mutex only serializes within one process — it does nothing across the multiple `marmot serve` processes, which is why cross-process locking fails. Also relevant to workstream 2: with a single-owner daemon this in-process mutex becomes the sole serialization point (sufficient), so the daemon design removes the multi-process SQLite contention entirely.

```go
34	type Store struct {
35		db *sqlite3.Conn
36		mu sync.Mutex // sqlite3.Conn is not safe for concurrent use
37	}
```

### store.go:56-74
Workstream 1 — schema init runs `Exec` (CREATE TABLE + ALTER TABLE migration) immediately on every open. Under multi-process startup these are the first writes that hit "database is locked" with no busy_timeout; also the natural place to add the WAL/busy_timeout PRAGMAs (before or inside initSchema). Note the deliberately-ignored ALTER TABLE error at line 71 — after the driver upgrade verify the error kind is still safely ignorable.

```go
56	func (s *Store) initSchema() error {
57		err := s.db.Exec(`
...
71		_ = s.db.Exec(`ALTER TABLE embeddings ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`)
```

### store.go:110-123 (pattern repeated at 149-178, 208-236, 253-281, 290-305, 333-341, 414-422, 474-485, 496-510, 518-527)
Workstream 1 — the full inventory of driver API surface used, to check against v0.33.x: `Conn.Prepare` (3-return form `stmt, _, err`), `Stmt.Step`, `Stmt.Err`, `Stmt.Exec`, `Stmt.Close`, `Stmt.BindText`, `Stmt.BindBlob`, `Stmt.ColumnText`, `Stmt.ColumnInt`, `Stmt.ColumnRawBlob`, `Conn.Exec`, `Conn.Close`. `ColumnRawBlob` is the riskiest: it returns memory only valid until the next Step/Close; here the blob is fully consumed by `deserializeFloat32` before stepping (safe), but confirm the method still exists/behaves the same in v0.33.x.

```go
110		stmt, _, err := s.db.Prepare(`SELECT embedding FROM embeddings LIMIT 1`)
...
116		if !stmt.Step() {
117			return 0, nil // empty store
118		}
119		blob := stmt.ColumnRawBlob(0)
```

### store.go:216-233 and 344-367, 425-450
Workstream 1 — long-running read scans: `Search`/`SearchActive`/`FindSimilar` hold a read statement open while scanning the entire embeddings table in Go. These are exactly the readers whose SHARED lock a concurrent COMMIT from another process trips over (PENDING-lock parking / F_OFD_SETLKW hang in v0.17.1). WAL mode makes these readers non-blocking; the driver upgrade fixes the indefinite commit wait.

```go
216		for stmt.Step() {
217			nodeID := stmt.ColumnText(0)
218			blob := stmt.ColumnRawBlob(1)
```

### store.go:530-535
Workstream 2 — `Close()` is the only lifecycle hook; the daemon owner must ensure exactly one process opens/closes this. No file locking, PID, or socket logic exists in this package (correctly — election belongs in the serve layer).

```go
531	func (s *Store) Close() error {
532		s.mu.Lock()
533		defer s.mu.Unlock()
534		return s.db.Close()
535	}
```

### store_test.go:8-16 and coverage_test.go:282-307
Test impact for both workstreams — tests open stores via `NewStore(":memory:")` (store_test.go:10) and a file path (coverage_test.go:293). After the WAL change, verify `:memory:` still works (PRAGMA journal_mode=WAL on :memory: is a no-op returning "memory" — the code must not treat that as an error). `TestNewStore_OpenError` (coverage_test.go:282-289) asserts open fails for a path in a missing directory — behavior should persist across the driver upgrade. No test currently exercises multi-connection/multi-process concurrency; both workstreams need new tests here.

```go
282	func TestNewStore_OpenError(t *testing.T) {
283		// A path inside a non-existent directory cannot be opened.
284		badPath := filepath.Join(t.TempDir(), "no-such-dir", "store.db")
285		_, err := NewStore(badPath)
```

### embedder.go / provider.go / openai.go / mock.go / openai_test.go / temporal_test.go
No relevant findings — pure embedding-provider abstractions (HTTP OpenAI client, mock, interface) with no SQLite, process, lifecycle, socket, or scheduler code.

## internal-graph

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

## internal-heatmap

## internal/heatmap

No SQLite usage anywhere in this package (pure in-memory struct + YAML-frontmatter markdown file). Nothing here changes for the WAL/driver-upgrade quick fix. All findings concern Workstream 2 (single-owner daemon), where heatmap persistence is the known "last-writer-wins on exit" hazard.

### heatmap.go:200-231
Workstream 2: `Load` reads the whole heat map into memory once. Each `marmot serve` process that calls this (via engine construction) gets its own independent in-memory copy with no cross-process refresh — this is the mechanism behind the stale/last-writer-wins behavior. Under the daemon design only the owner should Load.

```go
200	// Load reads a heat map file from _heat/<namespace>.md under the given vault dir.
201	func Load(vaultDir, namespace string) (*HeatMap, error) {
202		path := FilePath(vaultDir, namespace)
203		data, err := os.ReadFile(path)
204		if err != nil {
205			if os.IsNotExist(err) {
206				return New(namespace), nil
207			}
208			return nil, fmt.Errorf("load heatmap: %w", err)
209		}
210		return parse(data, namespace)
211	}
```

### heatmap.go:233-281
Workstream 2: `Save` is atomic per-write (temp file + rename, lines 271-279) so a single writer never corrupts the file, but there is no cross-process coordination — two serve processes each saving their own copy on exit silently clobber each other (last rename wins). The `h.mu` lock at 236-237 is a `sync.Mutex`, in-process only. Under single-owner daemon this becomes safe with no code change here; alternatively a read-merge-write would be needed if multi-process persists.

```go
233	// Save writes the heat map to _heat/<namespace>.md under the given vault dir.
234	// The write is atomic (temp file + rename).
235	func Save(vaultDir string, h *HeatMap) error {
236		h.mu.Lock()
237		defer h.mu.Unlock()
...
271		path := FilePath(vaultDir, h.Namespace)
272		tmpPath := path + ".tmp"
273		if err := os.WriteFile(tmpPath, []byte(buf.String()), 0o644); err != nil {
274			return fmt.Errorf("write tmp heatmap: %w", err)
275		}
276		if err := os.Rename(tmpPath, path); err != nil {
277			_ = os.Remove(tmpPath)
278			return fmt.Errorf("rename heatmap: %w", err)
279		}
```

### heatmap.go:44-56
Workstream 2: all mutation (`RecordCoAccess`, `Decay`) is guarded only by the in-process `sync.Mutex` at line 55 — concurrency safety assumes a single process owning the map, which is exactly the daemon model's invariant. Also note `Decay` (lines 157-165) is invoked by the summary scheduler; duplicate schedulers in multiple processes double-decay their own copies before racing on Save.

```go
45	type HeatMap struct {
...
53		// Internal index for fast lookups (not serialized).
54		index map[string]int // PairKey -> index into Pairs
55		mu    sync.Mutex
56	}
```

### heatmap_test.go:175-220
Test impact: `TestSaveAndLoad` / `TestLoadNonexistent` exercise single-process file round-trips only; no multi-process/concurrent-Save test exists. The daemon workstream should add (or relocate) tests for concurrent Save/last-writer behavior; these existing tests use `t.TempDir()` and are unaffected by either change.

```go
175	func TestSaveAndLoad(t *testing.T) {
176		dir := t.TempDir()
177		h := New("test-ns")
178		h.RecordCoAccess([]string{"auth/login", "auth/validate", "db/users"}, 0.1)
179
180		if err := Save(dir, h); err != nil {
```

## internal-indexer

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

## internal-llm

# internal/llm

## internal/llm

### internal/llm/provider.go:23-27
Peripheral relevance to Workstream 2 only. The `Summarizer` interface is what `summary.Scheduler` calls; in the single-owner daemon design, only the owner process should hold a Summarizer-backed scheduler to stop duplicate LLM calls. No changes needed inside this package — it is pure interfaces plus stateless HTTP clients (Anthropic/OpenAI, 120s timeout, no SQLite, no goroutines, no sockets, no signal/stdio handling, no lock files, no engine construction).

```go
23	// Summarizer generates namespace-level summaries from node data.
24	// Separate from Provider so implementations are optional.
25	type Summarizer interface {
26		Summarize(ctx context.Context, req SummarizeRequest) (string, error)
27	}
```

No other relevant findings: the package contains only HTTP LLM clients (`anthropic.go`, `openai.go`), interfaces (`provider.go`, `chat.go`), a test mock (`mock.go`), and httptest-based unit tests — none touch SQLite, ncruces/go-sqlite3, process lifecycle, unix sockets, schedulers, graph/heatmap persistence, or MCP/CLI wiring, so neither the WAL/driver-upgrade quick fix nor the daemon refactor changes code here.

## internal-mcp

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

## internal-namespace

# internal/namespace

## internal/namespace

### registry.go:221-241
Workstream 1 (quick fix): a second, independent call site that opens `embeddings.db` via `embedding.NewStore` — the WAL/busy_timeout fix in internal/embedding/store.go automatically covers it, but note this means remote vaults' SQLite files are opened by every serve process that does cross-vault queries, multiplying the multi-process lock contention (a `marmot serve` for vault B holds a writer store on the same file this registry opens read-only from vault A's process). Also relevant to workstream 2: in a single-owner daemon, the owner of vault A still opens vault B's DB directly rather than proxying to vault B's owner.

```go
221		// Open embedding store.
222		dbPath := filepath.Join(vaultDir, ".marmot-data", "embeddings.db")
223		store, err := embedding.NewStore(dbPath)
224		if err != nil {
225			return nil, fmt.Errorf("open embedding store for vault %q: %w", vaultID, err)
226		}
...
239		rv.EmbStore = store
240		return store, nil
```

### registry.go:75-142
Workstream 2 (daemon): another instance of the stale in-memory graph problem. Remote vault graphs are loaded once via `graph.LoadGraph` and cached in `r.vaults` forever (`LoadedAt` recorded at line 137 but nothing checks staleness automatically); `Refresh` (144-159) exists but is only invoked by callers on explicit staleness detection. A single-owner daemon design must decide whether the registry keeps caching remote graphs or delegates to remote owners.

```go
 76	func (r *VaultRegistry) ResolveGraph(vaultID string) (*graph.Graph, error) {
 77		// Fast path: read lock.
 78		r.mu.RLock()
 79		if rv, ok := r.vaults[vaultID]; ok {
 80			r.mu.RUnlock()
 81			return rv.Graph, nil
 82		}
...
126		g, err := graph.LoadGraph(store)
...
137			LoadedAt:  time.Now(),
138		}
139		r.vaults[vaultID] = rv
```

### registry.go:243-252
Lifecycle hook: `Close()` closes cached embedding stores. In workstream 2, owner shutdown/handoff must call this (and the proxy must never own one). Also relevant to workstream 1: with WAL, closing releases -wal/-shm cleanly; a killed process leaving these files behind is the failure path to test.

```go
244	func (r *VaultRegistry) Close() {
245		r.mu.Lock()
246		defer r.mu.Unlock()
247		for _, rv := range r.vaults {
248			if rv.EmbStore != nil {
249				_ = rv.EmbStore.Close()
250			}
251		}
252	}
```

### registry_embstore_test.go:1-102
Test impact (both workstreams): exercises `ResolveEmbeddingStore` caching and `Close()` against a real on-disk embeddings.db (`embedding.NewStore` path). Will exercise the new WAL/busy_timeout open pragmas and the upgraded driver; any v0.33.x API change in embedding.Store surfaces here too (lines 42, 51, 96 assert store caching and close behavior).

### namespace.go:23-24
Config surface note only: `NamespaceSettings.SummaryRegenerationInterval` and `EmbeddingModel` are per-namespace YAML settings feeding the summary.Scheduler / embedding pipeline. Workstream 2 must ensure only the daemon owner reads these to run schedulers; proxies must not.

```go
23	EmbeddingModel              string   `yaml:"embedding_model,omitempty"`
24	SummaryRegenerationInterval string   `yaml:"summary_regeneration_interval,omitempty"`
```

No process/signal/stdio handling, lock files, unix sockets, background goroutines, or MCP transport code in this package; concurrency is in-process only (sync.RWMutex).

## internal-node

# internal/node

## internal/node

Package is pure filesystem markdown-node storage: no SQLite, no engine/process/signal/socket/scheduler code. Only two tangential findings relevant to the daemon workstream's multi-process story.

### store.go:126-174
Relevance: workstream 2 (single-owner daemon). Node persistence is already atomic per-file (temp + rename), so concurrent `SaveNode` from multiple `marmot serve` processes cannot corrupt a file — but it IS last-writer-wins per node, and there is no cross-process invalidation: another process's in-memory graph (loaded once in mcp.NewEngine) never sees these writes. This is part of the stale-graph problem the daemon fixes; no changes needed here, but it means node files are safe to leave as-is once a single owner performs writes.

```go
129	func (s *Store) SaveNode(node *Node) error {
...
146	// Atomic write: create temp file in the same directory, write, then rename.
147	tmp, err := os.CreateTemp(dir, ".node-*.md.tmp")
...
168	if err := os.Rename(tmpPath, target); err != nil {
169		return fmt.Errorf("save node: rename: %w", err)
170	}
```

### store.go:228-244
Relevance: both workstreams, informational. `ListNodes` (the input to graph loading) walks the vault and explicitly skips hidden dirs like `.marmot-data` (where `embeddings.db` lives) and `_`-prefixed system dirs/files (`_heat`, `_summary.md`). So SQLite WAL sidecar files (`embeddings.db-wal`, `embeddings.db-shm`) created by the quick fix, and any lock file / unix socket placed under `.marmot-data` for the daemon, will NOT pollute node listing or graph loads — safe location for both.

```go
231	err := filepath.Walk(s.basePath, func(path string, info os.FileInfo, walkErr error) error {
...
237	// Skip hidden dirs (.obsidian, .marmot-data) and system dirs (_bridges, _heat).
238	if path != s.basePath && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
239		return filepath.SkipDir
240	}
...
244	if strings.HasPrefix(info.Name(), "_") {
```

No SQLite usage, engine construction, background goroutines, lock files, sockets, MCP/CLI wiring, or affected tests in this package (parser.go, writer.go, types.go, node_test.go, temporal_test.go are pure in-memory/tempdir markdown parsing and rendering).

## internal-routes

## internal/routes

No SQLite, engine, MCP, stdio, signal, socket, or scheduler code lives here. The package manages the global vault routing table at `~/.marmot/routes.yml`. Two findings are relevant to Workstream 2 (single-owner daemon); nothing here touches Workstream 1 (SQLite/WAL/driver upgrade).

### routes.go:27-29, 129-178
Workstream 2: cross-process coordination here is *only* atomic tmp+rename — the package mutex is explicitly process-local. Concurrent `marmot serve` processes doing `Update()` (read-modify-write of routes.yml) can lose each other's writes (last-writer-wins), same class of bug as the heatmap save. A single-owner daemon would make this a non-issue for serve; other CLI commands (index, etc.) that register vaults would still race unless routed through the owner or given an OS-level file lock. Also note `~/.marmot/` already exists as the global per-user directory — a natural home for the daemon's lock file and unix socket alongside `routes.yml`.

```go
27	// mu protects Load/Save within a single process. Inter-process safety
28	// is handled by atomic writes (tmp + rename).
29	var mu sync.RWMutex
...
129	// Update performs an atomic read-modify-write cycle on the routing table.
130	// The provided function receives the current table and may modify it.
131	// If fn returns nil, the modified table is saved atomically.
132	func Update(fn func(rt *RoutingTable) error) error {
133		mu.Lock()
134		defer mu.Unlock()
...
143		data, err := os.ReadFile(path)
...
173		if err := os.Rename(tmp, path); err != nil {
```

### routes.go:118-125 and 169-176
Workstream 2 (minor): the temp file name is fixed (`path + ".tmp"`), so two *processes* saving simultaneously write the same tmp file; rename keeps the file valid but content interleaving/lost updates are possible. If the daemon becomes the sole writer for serve, remaining writers (CLI commands) should either go through the daemon or use unique tmp names / flock.

```go
118	tmp := path + ".tmp"
119	if err := os.WriteFile(tmp, data, 0o644); err != nil {
120		return fmt.Errorf("write routing table tmp: %w", err)
121	}
122	if err := os.Rename(tmp, path); err != nil {
```

### routes_test.go, routes_stress_test.go (whole files)
Test impact: tests use `SetOverridePath` to redirect away from `~/.marmot/routes.yml` and stress in-process concurrency (20-50 goroutines). Neither workstream changes this package's API, but daemon e2e tests that spawn real `marmot serve` processes should use the same override mechanism (or an env var) so they don't touch the developer's real `~/.marmot` routing table, lock file, or socket path if those are colocated.

## internal-sdkgen

## internal/sdkgen
No relevant findings. The package (generate.go, generate_test.go) is a pure string generator that emits a TypeScript client SDK for the HTTP API; it opens no SQLite connections, builds no engine, and has no process/socket/lock/scheduler/stdio code, so neither workstream touches it.

## internal-summary

# internal/summary

## internal/summary

No SQLite usage in this package (no imports of ncruces/go-sqlite3, no embedding store references) — Workstream 1 (WAL/busy_timeout/driver upgrade) does not touch this folder. All findings are for Workstream 2 (single-owner daemon): this package IS the per-process background scheduler that must become owner-only, plus the on-disk _summary.md persistence that currently races across processes.

### scheduler.go:28-58
Workstream 2. The Scheduler type: one instance per `marmot serve` process (constructed by the engine/CLI wiring). Under the daemon design only the lock-holding owner should construct/Start this. It holds a `nodeLoader` closure — in multi-process mode each process's loader reads from its own stale in-memory graph, so duplicate + stale summaries get generated concurrently.

```go
28	// Scheduler manages async summary regeneration.
29	type Scheduler struct {
30		engine     *Engine
31		config     SchedulerConfig
32		dir        string
33		namespace  string
34		nodeLoader func() ([]*node.Node, error) // function to load current nodes
...
48	func NewScheduler(engine *Engine, config SchedulerConfig, dir string, namespace string, nodeLoader func() ([]*node.Node, error)) *Scheduler {
```

### scheduler.go:62-93
Workstream 2. Start/Stop lifecycle: `Start(ctx)` spawns a background goroutine, `Stop()` blocks on doneCh and drains NotifyChange goroutines via `wg.Wait()`. Daemon handoff must call `Stop()` on the old owner before a new owner starts its own scheduler; proxy-mode `serve` processes must never call `Start`. Note `Stop()` can block for up to the 2-minute LLM regeneration timeout (see :185), which matters for owner shutdown/handoff latency.

```go
62	func (s *Scheduler) Start(ctx context.Context) {
...
73		go s.run(ctx)
74	}
...
78	func (s *Scheduler) Stop() {
...
86		close(s.stopCh)
87		<-s.doneCh
88		s.wg.Wait() // drain any NotifyChange-spawned goroutines
```

### scheduler.go:98-122
Workstream 2. `NotifyChange` deduplicates in-flight regenerations only *within one process* (the `regenerating` flag is in-memory). With N serve processes, N schedulers each fire regeneration -> duplicate LLM calls and racing WriteSummary calls. The single-owner daemon eliminates this by having exactly one scheduler; write-path RPCs from proxies should funnel NotifyChange into the owner.

```go
98	func (s *Scheduler) NotifyChange(currentNodeCount int) {
99		s.mu.Lock()
100		if s.regenerating {
101			s.mu.Unlock()
102			return // another regeneration is already in-flight
103		}
```

### scheduler.go:151-176
Workstream 2. Periodic ticker loop (default 30m from DefaultSchedulerConfig at :20-26): every serve process ticks independently today, multiplying LLM spend. Also responds to ctx cancellation — relevant to daemon shutdown wiring.

```go
163		ticker := time.NewTicker(s.config.Interval)
164		defer ticker.Stop()
165	
166		for {
167			select {
168			case <-s.stopCh:
169				return
170			case <-ctx.Done():
171			return
172		case <-ticker.C:
173			s.regenerate()
```

### scheduler.go:178-205
Workstream 2. `regenerate()` = nodeLoader -> LLM GenerateSummary (2-minute timeout, background context — it outlives the Start ctx) -> WriteSummary to disk. Each process regenerates from its own possibly-stale graph, then writes `_summary.md`: last-writer-wins across processes, same failure class as the heatmap. `lastNodeCount`/`lastGenerated` state is per-process, so processes disagree about staleness.

```go
185		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
186		defer cancel()
187	
188		result, err := s.engine.GenerateSummary(ctx, s.namespace, nodes)
...
194		if err := WriteSummary(s.dir, s.namespace, result); err != nil {
```

### summary.go:29-48
Workstream 2. summary.Engine serializes generation with an in-process mutex only (`mu sync.Mutex // protects generation`) — no cross-process protection. One Engine per serve process today; must be owner-only in the daemon.

```go
29	type Engine struct {
30		summarizer llm.Summarizer // nil = no generation possible
31		mu         sync.Mutex     // protects generation
32	}
```

### summary.go:104-152
Workstream 2. `WriteSummary` persists `_summary.md` via temp-file + rename. Atomic per write (no torn files) but no cross-process coordination — concurrent owners silently overwrite each other (last-writer-wins). Safe once only the daemon owner writes; no change needed to the write mechanics themselves.

```go
104	func WriteSummary(dir string, namespace string, result *SummaryResult) error {
...
125		// Atomic write: temp file in same dir, then rename.
126		tmp, err := os.CreateTemp(parent, ".summary-*.md.tmp")
...
146		if err := os.Rename(tmpPath, target); err != nil {
```

### summary_test.go:181-321
Workstream 2 test impact. Tests construct schedulers directly (NewScheduler, NotifyChange, Start/Stop no-op semantics: TestSchedulerNotifyChange :181, TestSchedulerNotifyChangeBelowThreshold :225, TestSchedulerMinNodes :261, TestSchedulerStartStop :293). These are unit-level and stay valid, but any refactor that moves scheduler ownership into a daemon/owner component (or changes Start/Stop signatures for handoff) will need corresponding updates; new tests will be needed for "scheduler only runs in owner process".

```go
293	func TestSchedulerStartStop(t *testing.T) {
...
314		sched.Start(ctx)
315		// Starting again should be a no-op.
316		sched.Start(ctx)
317	
318		sched.Stop()
319		// Stopping again should be a no-op.
320		sched.Stop()
```

## internal-traversal

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

## internal-update

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

## internal-verify

## internal/verify

No relevant findings.

The package (cycle.go, verifier.go, and its tests) is pure in-memory graph integrity logic — content/source hashing, staleness checks, edge/supersede validation, and cycle detection. It contains no SQLite usage, no engine construction or lifecycle, no goroutines/schedulers/watchers, no process/signal/stdio/socket/lock-file handling, and no persistence of heatmap/summary state; its only I/O is read-only `os.Open` of source files for hashing. Neither the WAL/driver-upgrade quick fix nor the single-owner daemon workstream touches this package or its tests.

## internal-warren

# internal/warren

## internal/warren

### warren.go:1317-1323
Workstream 1 (WAL quick fix): `ImportProject` already excludes SQLite WAL sidecar files when copying a vault into a Warren, so enabling `journal_mode(WAL)` will not break import. HOWEVER, copying a live `embeddings.db` without its `-wal` file while WAL mode is active can produce a stale/torn snapshot (uncheckpointed pages live only in the `-wal`). After the quick fix, import of an actively-used vault should checkpoint (or open the DB) before copying. Also note the copy reads the db file with plain `io.Copy` (no sqlite lock coordination) via `copyRegularFile` (lines 1393-1414).

```go
1317	var importAlwaysExcluded = map[string]bool{
1318		".marmot-data/.env":               true,
1319		".marmot-data/embeddings.db-wal":  true,
1320		".marmot-data/embeddings.db-shm":  true,
1321		".obsidian/workspace.json":        true,
1322		".obsidian/workspace-mobile.json": true,
1323	}
```

### warren.go:576-584
Workstream 1/2: `Doctor` stats `<project>/.marmot-data/embeddings.db` directly to warn when a project has no embedding database. Purely a filesystem stat — no sqlite open — so unaffected by driver upgrade, but this is the same DB path the shared-store/daemon work centers on; keep the path in sync if it moves.

```go
576			if _, err := os.Stat(filepath.Join(projectPath, ".marmot-data", "embeddings.db")); err != nil {
577				report.Issues = append(report.Issues, DoctorIssue{
578					Severity:  "warning",
579					Code:      "embeddings_missing",
```

### warren.go:1065-1077 and 1127-1166
Workstream 2 (daemon): workspace state `.marmot/_warren.md` is mutated via read-modify-write (`updateWorkspaceState`) with atomic temp-file rename (`writeMarkdownYAML`) but NO inter-process lock — two concurrent processes (e.g. daemon owner plus a CLI `warren mount`) can lose updates (last-writer-wins), same class of bug as the heatmap save. If the daemon owns the vault, warren workspace-state writes from other CLI invocations should be routed through it or file-locked.

```go
1065	func updateWorkspaceState(workspaceRoot string, fn func(*WorkspaceState) error) (*WorkspaceState, error) {
1066		state, body, err := LoadWorkspaceState(workspaceRoot)
...
1073		if err := SaveWorkspaceState(workspaceRoot, state, body); err != nil {
```

### warren.go:920-970
Workstream 2: `ActiveMounts(marmotDir)` resolves the set of mounted Warren project vaults for a `.marmot` dir — this is read at engine construction time (multi-vault engine). A single-owner daemon must decide whether mount changes made after startup are picked up (currently each process reads this once; contributes to the stale-in-memory-state problem alongside the graph).

```go
920	// ActiveMounts returns active Warren project vaults for a local .marmot dir.
921	func ActiveMounts(marmotDir string) ([]ProjectStatus, error) {
```

### warren_test.go:216-220, 746-749
Test impact: import tests assert `embeddings.db` is copied and `-wal`/`-shm` are excluded. These pass regardless of the WAL quick fix (they use fake file contents, not real sqlite), but must be kept in mind if import gains a checkpoint step or the exclusion list changes.

```go
216	mustExist(t, filepath.Join(dest, ".marmot-data", "embeddings.db"))
219	mustNotExist(t, filepath.Join(dest, ".marmot-data", "embeddings.db-wal"))
220	mustNotExist(t, filepath.Join(dest, ".marmot-data", "embeddings.db-shm"))
```

No SQLite opens, unix sockets, lock files, signal handling, schedulers, or MCP transport code in this package — it is pure YAML/manifest/file-copy logic.

## scripts

## scripts

No relevant findings. The folder contains a single file, `sign-darwin.sh` (18 lines), a macOS codesign helper for release binaries; it touches none of the SQLite, engine lifecycle, locking, socket/stdio, scheduler, or CLI concerns of either workstream.

## web

# findings

## web

### web/embed.go:1-6
Workstream 1 (dependency upgrade / go.mod hygiene): this is the only Go file in `web/` — it embeds the built UI into the binary via `embed.FS`. It does not touch SQLite or ncruces/go-sqlite3, but it is a `go:embed` consumer (used by the `ui` HTTP server, see `cmd/marmot/pipeline.go`), so `make build` requires `web/dist` to exist; any go.mod churn/rebuild during the upgrade must keep `dist/` built or compilation fails.

```go
1  package web
2
3  import "embed"
4
5  //go:embed all:dist
6  var Assets embed.FS
```

### web/e2e/serve.sh:22-36
Workstream 2 (single-owner daemon): the Playwright e2e harness spawns `marmot index` then `marmot ui` against a temp vault (with `HOME` overridden to isolate `~/.marmot` state). If daemon election / lock files / unix sockets land under `~/.marmot` or the vault dir, these two sequential processes (index, then ui) each build their own engine against the same vault — this script is a test path that must not block on the lock, and `HOME=$WORK` means any socket/lock path derived from `$HOME` lands in the temp dir (good), but a path derived from the vault dir would live in the copied `.marmot`. Also the `kill "$SERVER_PID"` teardown means the ui process must release its lock/socket cleanly on SIGTERM.

```sh
22  export HOME="$WORK"
23
24  cp -R "$ROOT/e2e/fixture/vault" "$WORK/.marmot"
25  cp -R "$ROOT/e2e/fixture/src" "$WORK/src"
26  mkdir -p "$WORK/.marmot/.marmot-data"
27
28  "$BIN" index --dir "$WORK/.marmot"
29
30  # Run the server as a child (not exec) so the EXIT trap still fires to remove
31  # the temp vault when Playwright terminates this script.
32  cd "$WORK"
33  "$BIN" ui --dir .marmot --port "$PORT" --no-open &
34  SERVER_PID=$!
35  trap 'kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null; rm -rf "$WORK"' EXIT INT TERM
36  wait "$SERVER_PID"
```

### web/playwright.config.ts:11-16
Workstream 2 (test impact): Playwright starts the ui server via serve.sh and health-checks `/api/version` with a 60s timeout. If `marmot ui` gains lock-election behavior (e.g. waiting on or deferring to a daemon owner), startup latency or refusal-to-start would break this webServer readiness check.

```ts
11  webServer: {
12    command: 'bash e2e/serve.sh 3299',
13    url: 'http://127.0.0.1:3299/api/version',
14    reuseExistingServer: false,
15    timeout: 60_000,
16  },
```

### web/vite.config.ts:9-17
Minor / workstream 2 config surface: the dev-server proxy hardcodes the ui backend at `http://localhost:3274`. If the daemon rework changes the ui command's default port or moves the API behind a unix socket, this proxy target must be updated (vite can only proxy to TCP).

```ts
 9  server: {
10    port: 5173,
11    proxy: {
12      '/api': {
13        target: 'http://localhost:3274',
14        changeOrigin: true,
15      },
16    },
17  },
```

No SQLite usage, engine construction, scheduler/watcher code, MCP transport, or lock/PID handling exists anywhere in `web/` — everything else (`src/*.ts`, `e2e/ui.spec.ts`) is browser-side UI code talking to `/api/*` over HTTP.

