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
