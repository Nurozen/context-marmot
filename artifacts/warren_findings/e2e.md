# Findings

## e2e

Review's claim "zero warren coverage today (`grep -ri warren e2e/` = 0 hits)" is CONFIRMED — no warren mention anywhere in e2e/. The folder is a single-file harness (`e2e/e2e_test.go`, 1110 lines, `//go:build e2e`, run via `make e2e` = `go test -tags e2e -count=1 -v ./e2e/`) plus a static fixture. Everything below is reusable scaffolding for the review's test program (warren register→mount→query e2e, daemon+warren, editable write-back).

### e2e/e2e_test.go:34-65 — TestMain build harness (all test-program tiers)
Builds `./cmd/marmot` once into a temp binary; `MARMOT_E2E_BIN` env can point at a prebuilt binary for red-first baseline runs. Warren e2e tests should live in this same package and reuse `binPath`.
```go
34	func TestMain(m *testing.M) {
38		if pre := os.Getenv("MARMOT_E2E_BIN"); pre != "" {
39			binPath = pre
40			os.Exit(m.Run())
41		}
...
55		build := exec.Command("go", "build", "-o", binPath, "./cmd/marmot")
```

### e2e/e2e_test.go:69-125 — seedProject / copyDir / hermeticEnv / runCLI (hermetic vault setup template)
Hermetic vault template: copies `e2e/fixture/vault` (mock embedder, 4 nodes: auth, auth/login, auth/validate, db/users) into `t.TempDir()`, indexes it. A warren e2e can seed two such projects, `git init` a warren repo from one, and register it.
```go
69	func seedProject(t *testing.T) string {
72		copyDir(t, "fixture/vault", filepath.Join(proj, ".marmot"))
73		copyDir(t, "fixture/src", filepath.Join(proj, "src"))
76		os.MkdirAll(filepath.Join(proj, ".marmot", ".marmot-data"), 0o755)
80		out, err := runCLI(proj, "index", "--dir", ".marmot")
115	func hermeticEnv(dir string) []string {
116		return append(os.Environ(), "HOME="+dir)
117	}
119	func runCLI(dir string, args ...string) (string, error) {
```
CONSTRAINT / possible review gap: hermeticity here is achieved by `HOME=<proj>` (comment at 112-114 explicitly cites "~/.marmot state (e.g. routes.yml vault registrations)"), NOT by `MARMOT_ROUTES=off` (Tier 1.8's mechanism). The review's Tier 1.8 "MARMOT_ROUTES=off in every warren/pipeline test" applies to unit tests elsewhere; the e2e harness already isolates routes via HOME. Warren e2e tests actually NEED routes/registry to work, so they must use HOME isolation (this pattern), not MARMOT_ROUTES=off. Note also `hermeticEnv` puts warren state (`~/.marmot/...`) inside the project temp dir — warren e2e can inspect it there.

### e2e/e2e_test.go:230-406 — mcpSession harness (daemon+warren tests)
Full stdio JSON-RPC MCP driver: `startMCP` (l.241), `startMCPDaemon` (l.250, adds `MARMOT_DAEMON=1`), `startMCPEnv` (l.255, arbitrary env — use to add warren env vars), `closeAndWait(budget)` (l.302), `send`/`recv`/`recvErr` (l.319-357), `callTool`/`callToolErr` (l.360-406, per-call deadline, goroutine-safe). This is the template for "register→mount→query through a real serve" and "mount-while-owner-live" (Tier 2 freshness pinning).
```go
250	func startMCPDaemon(t *testing.T, proj string) *mcpSession {
252		return startMCPEnv(t, proj, append(hermeticEnv(proj), "MARMOT_DAEMON=1"))
255	func startMCPEnv(t *testing.T, proj string, env []string) *mcpSession {
257		cmd := exec.Command(binPath, "serve", "--dir", ".marmot")
```

### e2e/e2e_test.go:476-679 — multi-process contention tests (template for Refresh-under-search / cross-workspace index --force)
`TestConcurrentServes` (l.495) sustained query-loop vs write-burst across two processes with per-call deadlines and `"database is locked"` assertions on responses+stderr; `TestIndexDuringServe` (l.583) runs `marmot index` concurrently with serve writes and asserts no swallowed `warning:` upserts. Directly reusable shape for Tier 2 "refresh safe under concurrent search" and the "cross-workspace `index --force` hazard" e2e.

### e2e/e2e_test.go:681-982 — daemon lifecycle helpers (Tier 2 owner-watcher / reload tests)
`readDaemonInfo` (l.700, polls `.marmot/.marmot-data/daemon.info.json`), `splitOwnerProxy` (l.724), `assertVaultReleased` (l.741, flock-free check via `syscall.Flock LOCK_EX|LOCK_NB` — the flock exec-helper pattern the review says to mirror), `waitDaemonOwner` (l.827). A warren-daemon test (owner watcher triggering `reloadWarrenState`) plugs into these directly.
```go
761		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
762			t.Errorf("daemon.lock is still flocked after shutdown: %v", err)
```

### e2e/fixture/vault/_config.md:1-6 — mock embedder fixture (all warren e2e)
```yaml
embedding_provider: mock
token_budget: 8192
```
Warren e2e needs no API key: remote-project vaults built from this fixture index with the mock embedder. Constraint for Tier 4.5 (model-skew doctor check): to e2e-test skew you'd need a second mock model name; currently only one mock provider exists in the fixture.

### e2e/e2e_test.go:87-110 — test-local copyDir (NOT the production one)
The harness's `copyDir` is test-only (Walk + ReadFile/WriteFile, no symlink/FIFO handling). Do not confuse with the production `copyDir`/`copyMarmotVault` Tier 1.3 targets — those live in the warren package, not here. No call sites of production copyDir, embedding.NewStore, updateWorkspaceState, ActiveMounts, buildEngine, ensureWorkspace, or findWarrenMountByVault exist in e2e/.

### Makefile:27-35 — e2e wiring
`e2e: go test -tags e2e -count=1 -v ./e2e/`; separate `e2e-ui` (playwright via web/). New warren e2e tests need only the `e2e` build tag to be picked up by CI.
