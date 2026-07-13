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
