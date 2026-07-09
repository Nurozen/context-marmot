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
