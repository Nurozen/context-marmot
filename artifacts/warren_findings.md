# Findings: warren resolution

## Contents

- [cmd-marmot](#cmd-marmot)
- [docs](#docs)
- [e2e](#e2e)
- [internal-api](#internal-api)
- [internal-daemon](#internal-daemon)
- [internal-embedding](#internal-embedding)
- [internal-graph](#internal-graph)
- [internal-mcp](#internal-mcp)
- [internal-namespace](#internal-namespace)
- [internal-node](#internal-node)
- [internal-routes](#internal-routes)
- [internal-traversal](#internal-traversal)
- [internal-warren](#internal-warren)
- [web](#web)

---

## cmd-marmot

# Warren findings — scanner report

## cmd/marmot

Verified against branch `multiprocess-lock-fix` working tree (files match commit under review). All line numbers below are current and exact.

### cmd/marmot/warren.go:28-71 — warren subcommand dispatch (Tier 3 verbs)
Tier 3 (unmount/unregister/un-burrow verbs; mount-all --all). New verbs plug in here; `burrow` is literally `warrenMount(subArgs, true)` — "burrow implies materialize" means changing the `materialize` default when `isBurrow`, one line in `warrenMount`.

```go
50	case "mount":
51		return warrenMount(subArgs, false)
52	case "burrow":
53		return warrenMount(subArgs, true)
...
58	case "refresh":
59		return warrenRefresh(subArgs)
60	case "propose":
61		return warrenPropose(subArgs)
```

### cmd/marmot/warren.go:685-755 — warrenMount (Tier 3: mount-all, burrow-materialize, editable+materialized)
Signature: `func warrenMount(args []string, isBurrow bool) int`. Review's cite `warren.go:722-727` for "no projects mounts everything" is CORRECT at current lines:

```go
722	projects := fs.Args()
723	if len(projects) == 0 {
724		for _, project := range manifest.Projects {
725			projects = append(projects, project.ProjectID)
726		}
727	}
```

`--materialize` flag defined at 694 (`materialize := fs.Bool("materialize", false, ...)`) — "burrow implies materialize" fix: default it true when `isBurrow`. Materialize loop at 736-752 calls `warren.Materialize(marmotDir, *warrenID, project, entry.Path)` per project (line 747) AFTER `warren.Mount(workspaceRoot, ...)` at 732 — a mid-loop failure leaves the mount recorded but caches partial (Tier 1.2 checkpoint-before-copy also lands inside `warren.Materialize`, not here). Note `warren.Mount` takes `workspaceRoot` while `Materialize` takes `marmotDir` — two distinct roots threaded through this one function.

### cmd/marmot/warren.go:826-866 — refresh/propose stubs (Tier 2.reload + Tier 4.1/4.3)
Review cite `warren.go:826-866` CORRECT. Both are printf-only after `resolveWarrenID`:

```go
826	func warrenRefresh(args []string) int {
...
843		fmt.Printf("Warren %q is refreshed from git-managed files; run git pull in its checkout when needed.\n", id)
844		return 0
845	}
847	func warrenPropose(args []string) int {
...
864		fmt.Printf("Warren %q uses git for proposals; commit changes in its checkout and open a PR.\n", id)
```

Real `warren refresh` implementation slot: after `resolveWarrenID` (line 838) it has `workspaceRoot` and `id`; needs the warren checkout path (`state.Warrens[id].Path`, currently not loaded here — `resolveWarrenID` at 887-907 loads state but discards it, so refresh will re-load or `resolveWarrenID` should return the entry). Note: CLI refresh runs in a separate process from a daemon owner — it must ALSO poke the owner (via the daemon socket / refresh endpoint), not just its own short-lived state.

### cmd/marmot/warren.go:868-885 — ensureWorkspace (Tier 3: lazy ensureWorkspace; Tier 1 read-only mutation)
Review cite `cmd/marmot/warren.go:868-885` CORRECT. Every warren subcommand — including read-only `list`/`status`/`refresh`/`propose` — calls this, which MkdirAll's `.marmot/.marmot-data` and writes a mock-provider `_config.md`:

```go
868	func ensureWorkspace(dirFlag string) (marmotDir, workspaceRoot string, err error) {
869		if dirFlag == "" {
870			dirFlag = discoverVault()
871		}
872		marmotDir = dirFlag
873		workspaceRoot = filepath.Dir(marmotDir)
874		if err := os.MkdirAll(filepath.Join(marmotDir, ".marmot-data"), 0o755); err != nil {
...
878		if _, err := os.Stat(configPath); os.IsNotExist(err) {
879			content := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\nembedding_model: \"\"\n---\n"
880			if writeErr := os.WriteFile(configPath, []byte(content), 0o644); writeErr != nil {
```

Callers (all in warren.go): warrenRegister:632, warrenList:653, warrenMount:702, warrenStatus:765, warrenEdit:809, warrenRefresh:833, warrenPropose:854. A read-only variant (`locateWorkspace`) can replace it in list/status/refresh/propose; register/mount/edit keep the creating version. `discoverVault` is main.go:46-63 (walks up for `.marmot/`).

### cmd/marmot/warren.go:887-907 — resolveWarrenID (Tier 3: unreachable-warren surfacing)
`func resolveWarrenID(workspaceRoot, requested string) (string, error)`. Only checks registration, never `entry.Path` reachability. `warrenStatus` (757-794) prints AVAILABLE per project but never says "warren X unreachable at <path>" when the whole checkout is gone — surfacing belongs here or in `warrenStatus`.

### cmd/marmot/warren.go:601-619 — resolveWarrenRoot
Walks up from cwd for `warren.ManifestFileName` when `--warren-dir` is `.`. Warren-repo-side commands (init/project/bridge/doctor/format) use this, workspace-side commands use `ensureWorkspace` — two disjoint root-resolution paths; new verbs must pick the right one.

### cmd/marmot/pipeline.go:207-380 — buildEngine (Tier 2 core: load-once warren wiring, VaultRegistry conditional)
`func buildEngine(dir string) (*engineResult, error)`. This is the exact code Tier 2.1's `reloadWarrenState` must be extracted from. Review cite `pipeline.go:236-274` CORRECT:

```go
236	rt, _ := routes.Load() // best-effort; nil is fine
...
242	if mounts, mountErr := warren.ActiveMounts(dir); mountErr == nil {
243		for _, mount := range mounts {
244			if mount.VaultID != "" && mount.Available {
245				rt.Set(mount.VaultID, mount.Path)
246			}
247		}
248		if bridges, declared := warrenRuntimeBridges(dir, mounts); declared {
...
265	hasCrossVaultBridges := nsMgr != nil && len(nsMgr.CrossVaultBridges) > 0
266	hasRoutes := rt != nil && len(rt.List()) > 0
267	if hasCrossVaultBridges || hasRoutes {
...
272		vr := namespace.NewVaultRegistry(vaultID, dir, bridges, rt)
273		engine.WithVaultRegistry(vr)
```

Key constraint for Tier 2 "always-create VaultRegistry": today `WithVaultRegistry` is only called when routes/bridges exist at startup (267). A later `warren mount` cannot attach a registry to a running engine unless the registry is created unconditionally here. Extraction shape: lines 236-274 (rt load, ActiveMounts→rt.Set, warrenRuntimeBridges→nsMgr.CrossVaultBridges, NewVaultRegistry) are self-contained given `dir`, `vaultID`, and a target engine — a natural `reloadWarrenState(dir, engine)` body. `engineResult` (197-203) has no VaultRegistry/nsMgr fields; refresh callers reach them only through `engine`.

### cmd/marmot/pipeline.go:390-466 — warrenRuntimeBridges (Tier 2 reload input; Tier 1.6 error swallowing)
`func warrenRuntimeBridges(marmotDir string, mounts []warren.ProjectStatus) ([]*namespace.Bridge, bool)`. Swallows both `LoadWorkspaceStateFromMarmot` error (392-394 → `return nil, false`) and per-warren `LoadManifest` error (413-416 → `continue`) — same silent-drop family as `ActiveMounts` (Tier 1.6/3 unreachable-warren). Uses `state.Warrens[...].Path` directly, so any Tier 2 remote-graph-cache/manifest-cache layer must be consulted here too or refresh stays partial.

### cmd/marmot/pipeline.go:50-155 — runIndexPipeline --force guard (Tier 2.5 cross-workspace hazard)
Review says index --force deletion is unguarded when "the target vault is warren-mounted elsewhere". PARTIAL DRIFT: a daemon-owner guard ALREADY exists (66-72) — the remaining gap is exactly the warren-mounted-by-another-workspace / non-daemon-reader case:

```go
63	if force {
64		// Deleting the DB under a live owner's open WAL connection is not
65		// safe — the owner would keep writing into the unlinked file.
66		if info, alive := ownerAlive(dir); alive {
67			return fmt.Errorf("vault is served by marmot daemon (pid %d); index --force would delete the embeddings DB ...", info.PID)
68		}
70		_ = os.Remove(dbPath)
71		_ = os.Remove(dbPath + "-wal")
72		_ = os.Remove(dbPath + "-shm")
```

### cmd/marmot/pipeline.go:664-675 — ownerAlive (Tier 2 refresh plumbing template)
`func ownerAlive(dir string) (daemon.Info, bool)` — reads `daemon.ReadInfo(dir/.marmot-data)` and dials `info.Socket`. This is the existing CLI→owner discovery primitive; a real `warren refresh` that must poke a live owner reuses exactly this (plus a new control verb over the socket or a sidecar endpoint). Also used at 283 (heatmap detach), 693 (UI scheduler suppress), 1072 (watch refusal) — established pattern for "defer to owner" behavior.

### cmd/marmot/pipeline.go:483-544 + 575-645 — runServePipeline / runServeOwner (Tier 2 owner watcher hook)
`runServeOwner(dir string, lock *daemon.Lock, stdin io.Reader) error` starts the watcher at 592: `stopWatch, watchErr := daemon.StartGraphWatcher(dir, result.Engine)`. Tier 2's "owner watcher reacts to `_warren.md`" lands inside `internal/daemon.StartGraphWatcher`'s skip rules, not here — but the callback it needs (`reloadWarrenState`) must be exported from this package or moved to a shared package (cmd/marmot is `package main`, NOT importable — constraint: `reloadWarrenState` cannot live here if internal/daemon or internal/api must call it; it must move into internal/mcp, internal/namespace, or a new internal package, with buildEngine calling it).

### cmd/marmot/pipeline.go:75,986,1083,1287 + warren_test.go:363 — embedding.NewStore call sites in this folder
Tier 1.1 (read-only remote DB opens) — all four production call sites here open the LOCAL vault's DB read-write (index:75, status:986, watch:1083, static-index:1287); none opens a remote/warren-mounted DB. Remote opens happen in internal/namespace registry — an `embedding.NewStoreReadOnly` signature change does not touch cmd/marmot production code. warren_test.go:363 opens a fixture remote DB to seed embeddings (would keep RW `NewStore`).

### cmd/marmot/pipeline_warren_test.go:14,136-158 + warren_test.go:329,410-432 — test hermeticity gap CONFIRMED + reusable helpers (Tier 1.8, test program)
`grep MARMOT_ROUTES cmd/marmot/*_test.go` = 0 hits — review's Tier 1.8 claim is CORRECT: `TestBuildEngineEnforcesWarrenBridgesForActiveMounts` (pipeline_warren_test.go:14) and `TestBuildEngineQueriesActiveWarrenMount` (warren_test.go:329) call `buildEngine` → `routes.Load()` reading the developer's real `~/.marmot/routes.yml`. Fix is `t.Setenv("MARMOT_ROUTES", "off")` per test. Reusable hermetic-vault helpers for the warren e2e/test program:
- `writeTestConfig(t, marmotDir, vaultID)` pipeline_warren_test.go:136 — mock-provider `_config.md` + `.marmot-data`
- `saveWarrenProject(t, root, warrenID, projectID, vaultID)` pipeline_warren_test.go:147 — project metadata + config
- `testWarrenRoot(t, warrenID string, projects ...string) string` warren_test.go:410 — full warren fixture (manifest + N projects)
- `writeCLIImportSourceVault(t, marmotDir, vaultID)` warren_test.go:434 — import-source vault with node, `.marmot-data/embeddings.db` sentinel
- `captureRun(args)` warren_test.go:461 — stdout capture around `run()`
- `initTestVault(t)` cli_phase15_test.go:82 and `withStdin(t, content, fn)` surface_coverage_test.go:28 — serve/daemon harness (TestServeCommandEOFDaemon at surface_coverage_test.go:165 is the template for daemon+warren e2e: Setenv MARMOT_DAEMON=1, drive via stdin EOF, assert lock/info/socket cleanup; note the file's rule "no test in this package may send SIGINT/SIGTERM").

### cmd/marmot/flags.go:12-46 — reorderInterspersedFlags (constraint for new verbs)
`func reorderInterspersedFlags(args []string, valueFlags, boolFlags map[string]bool) []string`. Every warren subcommand that takes flags after positionals must pre-register its flag names in these maps (see warrenMount:690). New verbs (`unmount`, `burrow --drop`, `mount --all`) must add entries or interspersed parsing silently misfiles args. `--all` would go in boolFlags.

### cmd/marmot/main.go:46-63,110-111 — discoverVault + dispatch
`discoverVault()` walks up for `.marmot/` (46-63); `case "warren": return cmdWarren(cmdArgs)` (110-111). `run(args []string) int` (70) is the in-process CLI entry the whole test suite drives — warren e2e can reuse it instead of exec'ing a binary. `--no-daemon` serve flag at main.go:317.

### Review-accuracy notes (no line drift found, two clarifications)
1. All cmd/marmot line cites in warren_review.md (warren.go:722-727, 826-866, 868-885; pipeline.go:236-274; warren_test.go:329) are accurate against the current tree.
2. Clarification on Tier 2.5: pipeline.go:66-72 already blocks `index --force` under a live daemon owner; only the "warren-mounted by another workspace without a daemon" hole remains.
3. Constraint the plan must respect: `reloadWarrenState` cannot be a cmd/marmot function if internal/daemon's watcher or internal/api's refresh endpoint must call it — cmd/marmot is `package main`. The warren wiring in buildEngine:236-274 must be lifted into an importable internal package.
4. `warrenRefresh`/`warrenPropose` discard the workspace state loaded by `resolveWarrenID`; a real refresh needs `state.Warrens[id].Path`, so `resolveWarrenID` likely grows to return the entry (or callers re-load) — every caller is in this file (warrenStatus:770, warrenRefresh:838, warrenPropose:859).

---

## docs

# Findings

## docs

This folder is documentation only (no code, no tests). It contributes user-facing contracts the fix plan must either preserve or consciously update, plus pointers to the e2e harness the test program should extend. Six sections below; anything not listed (benchmark.md, bridges.md, crud-classifier.md, current_limitations.md, data-structures.md, embedding-providers.md, implementation_plan.md, language_comparison.md, spec-*.md, typescript-sdk.md) has zero warren/daemon/flock content and is irrelevant.

### docs/warrens.md:104-137 (import copy semantics — Tier 1.2/1.3 constraint)
Docs already promise the hardened-copy behavior for **import** (regular-files-only, skip symlinks/device/socket, secret stripping, WAL/SHM exclusion). Note: docs say `embeddings.db-wal`/`-shm` are *excluded* — so a checkpoint-before-copy fix (Tier 1.2) is required for import to not silently drop un-checkpointed WAL data; excluding WAL without checkpointing loses data. The plan's `copyDir` hardening for burrow (Tier 1.3) should be documented here too once fixed; today the docs describe import only, silently implying burrow differs.

```text
114  Import copies regular files only, skips symlinks and device/socket files, strips
115  obvious inline secret fields or API-key-looking values from `_config.md`, and
116  always excludes transient or sensitive files:
117
118  - `.marmot-data/.env`
119  - `.marmot-data/embeddings.db-wal`
120  - `.marmot-data/embeddings.db-shm`
```

### docs/warrens.md:229-236 (burrow flag — Tier 3 constraint)
Docs document `burrow --materialize` as an explicit flag; the Tier 3 fix "burrow implies materialize" changes a documented CLI contract — this file must be updated in the same change.

```text
232  marmot warren burrow --materialize --warren product-platform project-b
```

### docs/warrens.md:189-236 (no inverse verbs documented — Tier 3 confirmation)
The "Consume a Warren" section documents register/list/mount/status/edit (`edit --off` exists as the only inverse) and burrow — no unmount/unregister/un-burrow anywhere, confirming review Tier 3 "missing inverse verbs". New verbs need doc sections here. Also note mount syntax takes explicit project args (`warren mount --warren <id> p1 p2`), relevant to the "mount-all needs --all" fix.

```text
207  marmot warren mount --warren product-platform project-a project-b
226  marmot warren edit --off --warren product-platform project-a
```

### docs/warrens.md:266-299 (write policy + MCP asymmetry — Tier 1.4 / Tier 3 documented behavior)
The editable+materialized write-loss bug (Tier 1.4) directly contradicts documented behavior: docs promise editable writes go "back to that project's own `.marmot/` vault and embedding database." The MCP vs API @-write asymmetry (Tier 3) is *intentionally documented*, so removing the asymmetry is a doc-visible contract change, not just a bug fix.

```text
277  When a Warren node is editable, API/UI updates write back to that project's own
278  `.marmot/` vault and embedding database. Read-only Warren nodes show provenance
...
281  MCP `context_write` does not accept `@vault-id/...` node IDs directly. Use the
282  Warren-aware API/UI path for editable mounted nodes, or write local nodes as
283  usual.
```

### docs/warrens.md:303-317 + docs/architecture.md:376-382 (freshness model — Tier 2/4 constraint)
Docs state materialized-cache-else-live-checkout resolution and per-project embedding DBs (no merge). Any remote-graph cache TTL / refresh design (Tier 2) and burrow commit-pinning (Tier 4.2) must keep this documented resolution order. architecture.md:380-381 ("Only active mounts are added to the engine... dormant") is the semantic the daemon Rebuild/reloadWarrenState work must preserve.

```text
warrens.md 315  If no materialized cache exists, Marmot reads the project directly from the
warrens.md 316  registered Warren checkout.
architecture.md 380  Only active mounts are added to the engine. Registered but unmounted projects are
architecture.md 381  dormant and do not participate in query or UI results.
```

### docs/development.md:21-42 (e2e harness pointers — test program)
Documents the existing e2e harness the warren test program should reuse: `e2e/` Go package builds the binary itself against `e2e/fixture/` (CLI, MCP stdio JSON-RPC, embedded UI over HTTP); Playwright suite is `web/e2e/ui.spec.ts` + `web/e2e/serve.sh` on a temp fixture copy. Make targets: `make e2e`, `make e2e-ui`, `make e2e-all`; integration tests via `go test -race -tags integration -count=1 ./internal/`. No warren coverage exists in these docs — consistent with review's "zero warren e2e today". Note: development.md says "all six tools" for MCP; the deferred-tool list shows six `mcp__context-marmot__*` tools, so this is accurate.

```text
23  The `e2e/` package exercises the built binary against a static fixture vault
24  (`e2e/fixture/`): CLI flows (index, status, query, verify, sdk, staleness
25  detection), the MCP server over stdio JSON-RPC (all six tools), and the
26  embedded web UI over HTTP.
```

### Review-accuracy check
warren_review.md cites no docs/ paths, so no line drift to flag. No contradictions found between the review's issue descriptions and the docs, except the nuance above: the review frames Tier 3 MCP @-write asymmetry as a UX gap, but warrens.md:281-283 documents it as intended behavior — the plan should treat it as a deliberate contract to change, and update docs accordingly. Docs mention no daemon, no MARMOT_ROUTES, no flock anywhere — daemon-era behavior (Tier 2) is entirely undocumented and will need new doc sections.

---

## e2e

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

---

## internal-api

# internal/api findings (commit 1f14f3e, branch multiprocess-lock-fix)

## internal/api

### api.go:56-77 (route registration) — Tier 2.2a, Tier 3
All warren HTTP surface is registered here; the refresh endpoint already exists and only needs a real implementation behind it. Server holds `engine *mcpserver.Engine` (api.go:19) — a `reloadWarrenState(engine)` helper is directly callable from handlers.

```go
66	s.mux.HandleFunc("GET /api/warrens", s.handleWarrens)
67	s.mux.HandleFunc("GET /api/warren/{id}", s.handleWarrenStatus)
68	s.mux.HandleFunc("GET /api/warren/{id}/graph", s.handleWarrenGraph)
69	s.mux.HandleFunc("GET /api/warren/{id}/status", s.handleWarrenStatus)
70	s.mux.HandleFunc("POST /api/warren/{id}/refresh", s.handleWarrenRefresh)
```

### handlers.go:912-932 (handleWarrenRefresh) — Tier 2.2a (refresh stub)
Confirmed printf stub. It validates the warren exists via `warren.LoadWorkspaceStateFromMarmot(s.engine.MarmotDir)` then returns a static JSON body; never touches `VaultRegistry`. Review's cite "handlers.go:912-931" is correct (function ends at 932).

```go
912	// handleWarrenRefresh is a no-op refresh hook for git-backed Warren state.
913	func (s *Server) handleWarrenRefresh(w http.ResponseWriter, r *http.Request) {
...
928	writeJSON(w, http.StatusOK, map[string]string{
929		"warren_id": id,
930		"status":    "git-backed Warren state is read from disk",
931	})
```
Existing test `TestWarrenListStatusRefreshSuccess` (api_more_test.go:386-433) asserts the stub's 200 + `warren_id` echo — a real refresh must keep those assertions or update them (it checks `refreshResp["warren_id"]`, so changing the response shape breaks the test).

### handlers.go:396-468 (handleWarrenNodeUpdate) — Tier 1.1, 1.4, 1.6
Signature: `func (s *Server) handleWarrenNodeUpdate(w http.ResponseWriter, id string, req NodeUpdateRequest)`. Dispatched from `handleNodeUpdate` at handlers.go:319-322 (`strings.HasPrefix(id, "@")`). Writes node + embeddings to `mount.Path` (burrow cache when materialized → Tier 1.4 write loss). This is the API side of the MCP/API @-write asymmetry (Tier 3): HTTP allows editable @-writes, MCP rejects them.

Tier 1.6 swallowed errors (review cited :449-454; actual :448-456, one-line drift, harmless):
```go
448	vec, err := s.engine.Embedder.Embed(embedText)
449	if err == nil {
450		embStore, storeErr := embedding.NewStore(filepath.Join(mount.Path, ".marmot-data", "embeddings.db"))
451		if storeErr == nil {
452			h := sha256.Sum256([]byte(embedText))
453			_ = embStore.Upsert(diskNode.ID, vec, hex.EncodeToString(h[:]), s.engine.Embedder.Model())
454			_ = embStore.Close()
455		}
456	}
457	}
458	}
459	if s.engine.VaultRegistry != nil {
460		_ = s.engine.VaultRegistry.Refresh(vaultID)
461	}
```
Note for Tier 1.1: this `embedding.NewStore` at :450 is a legitimate WRITE open (editable mount) — do NOT convert it to `NewStoreReadOnly`; only registry reads change. Also `_ = s.engine.VaultRegistry.Refresh(vaultID)` (:460) is a close-in-place refresh call — Tier 2.3 swap-then-close semantics must keep this call safe.

### handlers.go:946-957 (findWarrenMountByVault) — Tier 1.4, Tier 3 vault-ID collision
Signature: `func (s *Server) findWarrenMountByVault(vaultID string) (warren.ProjectStatus, bool)`. First-match over `warren.ActiveMounts(s.engine.MarmotDir)` (error swallowed → false at :948-950). Call sites in this package: handlers.go:402 (node update, chooses write path — Tier 1.4 fix point "prefer checkout for editable") and handlers.go:596 (provenance in resolveSearchNode).

```go
946	func (s *Server) findWarrenMountByVault(vaultID string) (warren.ProjectStatus, bool) {
947		mounts, err := warren.ActiveMounts(s.engine.MarmotDir)
948		if err != nil {
949			return warren.ProjectStatus{}, false
950		}
951		for _, mount := range mounts {
952			if mount.VaultID == vaultID {
953				return mount, true
954			}
```

### handlers.go:537-581 (searchMountedVaults) — Tier 1.1, 2.1, 2.3
Calls `s.engine.VaultRegistry.ResolveEmbeddingStore(vaultID)` (:565) — the read-only-open (Tier 1.1) call site in this package. Nil-registry gate at :538-540 means API cross-vault search silently returns nothing when registry wasn't created at startup (Tier 2.1 always-create fixes this here for free). Errors from ResolveEmbeddingStore and SearchActive are `continue`-swallowed (:566-568, :570-572 — Tier 1.6). Semantic note the review glosses over: this function ONLY searches remote vaults when `ns` starts with `_warren/` (:541-544 early-return otherwise) — plain `/api/search` never hits remote stores; scope any freshness/e2e test accordingly.

```go
545	mounts, _ := warren.ActiveMounts(s.engine.MarmotDir)   // error swallowed
565	remoteStore, err := s.engine.VaultRegistry.ResolveEmbeddingStore(vaultID)
566	if err != nil { continue }
569	remoteResults, err := remoteStore.SearchActive(vec, limit, s.engine.Embedder.Model())
```
Also :569 passes the LOCAL embedder's model to remote SearchActive — the Tier 4.5 model-skew silent-empty behavior lives here for the API path.

### handlers.go:583-609 (resolveSearchNode) — Tier 2 freshness
`s.engine.VaultRegistry.Resolve(vaultID, nodeID)` (:592) serves nodes from the registry's forever-cached remote graph, while handleWarrenGraph (below) reads disk fresh — the exact "two views in one process" split, both inside this file.

### handlers.go:785-910 (handleWarrens/handleWarrenStatus/handleWarrenGraph) — Tier 2 (disk-fresh side), Tier 3 unreachable surfacing
All three re-read disk per request: `warren.LoadWorkspaceStateFromMarmot(s.engine.MarmotDir)` (:787, :832, :919), `warren.LoadWorkspaceState(workspaceRoot)` with `workspaceRoot := filepath.Dir(s.engine.MarmotDir)` (:802-803), `warren.Status(workspaceRoot, id)` (:813), `warren.ActiveMounts(s.engine.MarmotDir)` (:841). handleWarrenGraph opens each mount fresh via `node.NewStore(mount.Path)` + `graph.LoadGraph` (:858-859) and skips unavailable mounts silently (:855 `!mount.Available` → continue; per-mount LoadGraph errors `continue` at :860-861 — another Tier 1.6/Tier 3 silent-unreachable spot). API-side constraint: these handlers are why Tier 2 must not regress "UI is disk-fresh"; making them registry-backed would inherit staleness.

### handlers.go:934-944 (splitQualifiedVaultID) — reference
`func splitQualifiedVaultID(id string) (vaultID, nodeID string, ok bool)` — package-private; MCP has its own copy; Tier 3 MCP/API alignment could share it.

### watcher.go:14-16 — Tier 2.2c constraint
`func (s *Server) StartWatcher(vaultDir string) (stop func(), err error)` delegates to `daemon.StartGraphWatcherNotify(vaultDir, s.engine, s.NotifyChange)` — the API server uses the SAME daemon graph watcher whose skip-`_`-files rule Tier 2 changes; un-skipping `_warren.md` in internal/daemon automatically flows to the API serve path, and `s.NotifyChange` gives free SSE UI refresh on warren state change.

### ActiveMounts consumers in this package (for the Tier 2 reload plan)
handlers.go:545 (searchMountedVaults), :841 (handleWarrenGraph), :947 (findWarrenMountByVault), plus test helper api_test.go:193. None cache — all disk-per-call.

### Test templates worth reusing — test program
- `setupAPIWarren` (api_test.go:135-171): full hermetic warren fixture — temp warrenRoot, `_config.md` with vault_id, `SaveProjectMetadata`, `SaveManifest`, `RegisterWorkspaceWarren`, `Mount`. Best existing template for warren e2e/unit fixtures anywhere in the repo.
- `wireWarrenVaultRegistry` (api_test.go:191-202): manual registry wiring via `namespace.NewVaultRegistry("", engine.MarmotDir, nil, rt)` + `engine.WithVaultRegistry` — Tier 2.1 (always-create + Rebuild) should obsolete this helper; update it rather than leaving a second wiring path.
- `seedRemoteEmbedding` (api_test.go:173-189): real-SQLite remote embeddings.db seeding (useful for the Tier 1.1 read-only regression test: seed, open via registry, assert no `-wal` sidecar/journal_mode change).
- `setupTestEngine` (api_test.go:65-104) + `seedEmbeddings` + `doRequest` (api_test.go:206): hermetic engine/HTTP harness. NOTE: these tests never call buildEngine, so the Tier 1.8 MARMOT_ROUTES hermeticity bug does NOT apply here (no `Setenv("MARMOT_ROUTES", ...)` anywhere in internal/api tests, and none needed).

### Review corrections
- 1.6 cite "handlers.go:449-454" → actual swallow block is 448-456; trivial drift.
- 1.4 cite "handlers.go:412-457" → function body writing is 412-458 (registry refresh at 459-461); accurate enough.
- Not stated in review: `searchMountedVaults` only runs for `ns` prefixed `_warren/` — plain API search never touches remote stores; Tier 1.1/2.3 API-path tests must use a `_warren/<id>` ns filter.
- Not stated: `handleWarrenNodeUpdate`'s `embedding.NewStore` (:450) is an intentional write open; the read-only-store fix must not touch it.
- No `stat/refresh` line drift elsewhere; handlers.go:912-931 cite verified correct.

---

## internal-daemon

# internal/daemon findings (branch multiprocess-lock-fix @ 1f14f3e)

## internal/daemon

Package: single-owner election for `marmot serve` (flock on `<dataDir>/daemon.lock`, unix-socket MCP relay, graph watcher). Warren-relevant as (a) the flock utility the review's Tier 1 `_warren.md`/routes.yml RMW fix is told to reuse, (b) the watcher whose skip rules exclude warren state files (Tier 2 freshness — reloadWarrenState will need its own trigger), (c) the exec-helper flock test template Tier 1's concurrency tests should mirror. There are NO refresh stubs, no warren imports, and no `embedding.NewStore`/`copyDir`/manifest calls anywhere in this package.

### lock.go:75-96 — TryAcquire (the flock utility Tier 1 fix 1.5 says to reuse)
Relevance: Tier 1 "flock for `_warren.md`/routes.yml RMW". Note the constraint: `TryAcquire` is **non-blocking and dataDir-scoped** — it hardcodes `lockFileName = "daemon.lock"` (lock.go:43) and creates the whole dataDir. Reusing it for a sibling-lockfile RMW requires either generalizing the filename or (simpler) reusing only the platform primitive `tryFlock` (lock_unix.go:16-22) plus adding a *blocking* variant (`unix.LOCK_EX` without `LOCK_NB`), since an RMW writer should wait, not fail with ErrHeld.

```go
75	func TryAcquire(dataDir string) (*Lock, error) {
76		if err := os.MkdirAll(dataDir, 0o755); err != nil {
...
79		path := filepath.Join(dataDir, lockFileName)   // hardcoded "daemon.lock"
80		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
...
84		if err := tryFlock(f); err != nil {
...
95		return &Lock{dataDir: dataDir, file: f}, nil
96	}
```

### lock_unix.go:16-22 / lock_windows.go — tryFlock platform split
Relevance: Tier 1 flock reuse. The per-OS split already exists (`//go:build unix`); Windows returns `ErrUnsupported` (lock_windows.go, 14 lines). A warren RMW lock must decide its Windows story — daemon mode simply refuses on Windows, but warren edits presumably must still work there, so blind reuse of this package's semantics would break Windows warren writes.

```go
16	func tryFlock(f *os.File) error {
17		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
18		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
19			return ErrHeld
20		}
21		return err
22	}
```

### owner.go:296-303 and 326-330 — watcher skip rules (review cite `owner.go:297-303` is accurate, off by ~1 line)
Relevance: Tier 2 daemon-era freshness. The graph watcher ignores every `_`-prefixed .md file and never watches `_`-/`.`-prefixed dirs — so owner processes will NEVER see `_warren.md`, `routes.yml` (not .md at all, filtered at :297), or anything under `.marmot-data/warrens/`. Any reloadWarrenState wiring must add its own fsnotify watch or a socket refresh verb; it cannot piggyback on this loop without changing these filters.

```go
296		// Only react to .md files.
297		if !strings.HasSuffix(event.Name, ".md") {
298			continue
299		}
300		// Ignore underscore-prefixed files (_config.md, _summary.md, etc.)
301		if strings.HasPrefix(filepath.Base(event.Name), "_") {
302			continue
303		}
...
328	func skipDirName(name string) bool {
329		return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
330	}
```

### owner.go:228-236 — StartGraphWatcher / StartGraphWatcherNotify signatures
Relevance: Tier 2 — where a warren-refresh hook would attach; `internal/api/watcher.go:15` already consumes `StartGraphWatcherNotify(vaultDir, s.engine, s.NotifyChange)`, and `cmd/marmot/pipeline.go:592` consumes `StartGraphWatcher(dir, result.Engine)`. Signature changes here touch both.

```go
228	func StartGraphWatcher(dir string, eng *mcp.Engine) (stop func(), err error) {
229		return StartGraphWatcherNotify(dir, eng, nil)
230	}
236	func StartGraphWatcherNotify(dir string, eng *mcp.Engine, onReload func()) (stop func(), err error) {
```

### owner.go:334-343 — reloadGraph replaces only the node graph
Relevance: Tier 2. Reload path is `graph.LoadGraph(node.NewStore(dir))` → `eng.SetGraph(newGraph)` — it does NOT rebuild VaultRegistry, warren mounts, or routes. This is the concrete mechanism behind the review's "buildEngine load-once warren wiring is a real problem under the daemon": a long-lived owner refreshes nodes forever but its warren state is frozen at startup.

```go
334	func reloadGraph(dir string, eng *mcp.Engine) bool {
335		newGraph, err := graph.LoadGraph(node.NewStore(dir))
...
340		eng.SetGraph(newGraph)
```

### proxy.go:187-206 — no refresh verb on the socket protocol
Relevance: Tier 2 "real refresh endpoint/command". The socket carries raw newline-delimited MCP JSON-RPC only (`RunProxy(stdin io.Reader, stdout io.Writer, socketPath string) error` at :187; `RunProxySession` at :206 with handshake-replay resumption, `MARMOT_PROXY_NO_RESUME` at :214). There is no side-channel/control verb; a `marmot warren refresh` CLI hitting the owner would need either a new MCP tool routed through the normal engine or a second listener — the protocol itself has no room for out-of-band commands without framing changes.

### lock_test.go:106-195 — exec-helper flock test template (reuse for Tier 1 concurrency tests)
Relevance: the review's test program explicitly says "flock test mirrors internal/daemon's". Template: `TestFlockReleasedOnKill` spawns `os.Args[0]` with `-test.run=TestHelperLockHolder$`, gates the helper body on `MARMOT_DAEMON_LOCK_HELPER=1` env (t.Skip otherwise), passes the dir via env, synchronizes on a stdout sentinel (`HELPER_ACQUIRED`), then SIGKILLs and polls `TryAcquire` with a 5s deadline.

```go
109		cmd := exec.Command(os.Args[0], "-test.run=TestHelperLockHolder$", "-test.v")
110		cmd.Env = append(os.Environ(),
111			"MARMOT_DAEMON_LOCK_HELPER=1",
112			"MARMOT_DAEMON_TEST_DATADIR="+dataDir,
113		)
...
180	func TestHelperLockHolder(t *testing.T) {
181		if os.Getenv("MARMOT_DAEMON_LOCK_HELPER") != "1" {
182			t.Skip("helper process only")
```

### socket.go:12-30 — SocketPath and the 96-byte macOS cap
Relevance: constraint for any new per-warren daemon socket/lock: `maxSocketPathLen = 96` with a hashed fallback path; deep `.marmot-data/warrens/<id>/projects/<p>/.marmot/.marmot-data` dirs would routinely overflow, so warren-side sockets must go through `SocketPath` (or reuse the published `Info.Socket`, never re-derive — per lock.go:51-54 doc).

### Call sites outside this package (functions the plan may touch)
- `daemon.TryAcquire`: cmd/marmot/pipeline.go:498; cmd/marmot/surface_coverage_test.go:183,197,280
- `daemon.ReadInfo`: pipeline.go:505,665; surface_coverage_test.go:250
- `daemon.RunProxySession`: pipeline.go:511
- `daemon.StartGraphWatcher`: pipeline.go:592; `StartGraphWatcherNotify`: internal/api/watcher.go:15
- `daemon.NewOwner`: pipeline.go:600 (`runServeOwner(dir string, lock *daemon.Lock, stdin io.Reader) error` at pipeline.go:575)
- `daemon.BoundedStop`: pipeline.go:608,639
- `daemon.SocketPath`: surface_coverage_test.go:179,203,286

### Review-accuracy notes
- The review's `internal/daemon/owner.go:297-303` cite for underscore-file skipping is essentially correct (the .md filter is :297, underscore skip :301) — no meaningful drift.
- The review implies the daemon's flock utilities are drop-in for `_warren.md` RMW; they are not drop-in: TryAcquire is non-blocking (ErrHeld, not wait), hardcodes the `daemon.lock` filename, and is a no-op-refusal on Windows. The plan should extract/extend `tryFlock` with a blocking mode and parameterized path rather than call TryAcquire.
- No refresh stub exists in this package; any "wired to the owner watcher" language in Tier 2 must mean *new* code here, not modification of an existing hook.

---

## internal-embedding

# internal/embedding scanner findings

## internal/embedding

Files: store.go (611), store_wal_test.go (164), mock.go (111), embedder.go (13), provider.go (25), openai.go (227) + tests. This package is the direct target of two Tier-1 fixes (read-only remote opens; checkpoint-before-copy) and touches Tier-4 model-skew.

### store.go:39-70 — NewStore (Tier 1: read-only remote opens)
Review's cite `store.go:47-89` is accurate (NewStore body 47-70, initSchema 72-90). NewStore unconditionally sets busy_timeout + `PRAGMA journal_mode = WAL` + runs DDL, so opening a remote vault's embeddings.db flips it to WAL and creates -wal/-shm sidecars.

```go
47	func NewStore(dbPath string) (*Store, error) {
48		db, err := sqlite3.Open(dbPath)
...
55		if err := db.BusyTimeout(5 * time.Second); err != nil {
59		if err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
64		s := &Store{db: db}
65		if err := s.initSchema(); err != nil {
```

Constraints for `NewStoreReadOnly`:
- ncruces go-sqlite3 v0.33.2 provides `sqlite3.OpenFlags(filename, sqlite3.OPEN_READONLY)` (conn.go:51) — preferable to `file:...?mode=ro` URI, both work.
- A read-only conn CANNOT run initSchema (CREATE TABLE / ALTER at store.go:73-87 would fail with SQLITE_READONLY) and cannot exec `journal_mode = WAL` if the DB is not already WAL. NewStoreReadOnly must skip both; busy_timeout is fine.
- Read-only open of a WAL DB still needs -shm creation permission unless immutable=1; since remote vaults are local git checkouts this is fine, but note it for network mounts.
- `Store.db` is unexported and `Store.mu` guards every method — the read-only variant returns the same `*Store` type; all read methods (Search, SearchActive, FindSimilar, StaleCheck, Count, StoredDimension) work unchanged. Upsert/UpdateStatus/Delete would return SQLITE_READONLY errors at Exec time; acceptable, or add a `readOnly bool` guard for clearer errors.

### store.go:72-90 — initSchema (Tier 1 constraint)
Runs `CREATE TABLE IF NOT EXISTS` plus a blind `ALTER TABLE ... ADD COLUMN status` whose error is deliberately ignored (line 87-88). Any read-only path must bypass this entirely.

### Checkpoint-before-copy (Tier 1, import/burrow) — NO helper exists yet
There is no `Checkpoint`/`wal_checkpoint` anywhere in the repo. Because `db` is unexported, warren's copy path cannot checkpoint externally; the fix must add a Store method, e.g.:

```go
func (s *Store) Checkpoint() error {
	s.mu.Lock(); defer s.mu.Unlock()
	return s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
}
```

Call sites that copy embeddings.db and currently skip the sidecars instead of checkpointing: internal/warren/warren.go:1319-1320 (skip map for `-wal`/`-shm`), copyMarmotVault warren.go:1325, copyDir warren.go:1289, Materialize copy warren.go:976. Skipping the WAL without checkpointing loses all un-checkpointed writes — exactly the review's Tier-1 point; the fix is open via NewStore + Checkpoint() + Close() before copy.

### store.go:34-37 — Store struct (Tier 2: concurrent search safety)
`sync.Mutex mu` serializes all ops on one conn; safe for concurrent search within a process. Cross-process safety comes from WAL + 5s busy_timeout. A registry Rebuild that closes/reopens stores must not race in-flight SearchActive — Close() takes mu (line 547-551), so a search in progress finishes first, but a search started after Close gets a closed-conn error; refresh code should swap the pointer, not Close under readers.

### store.go:326-397 SearchActive, 268-298 checkModel, 349 WHERE model = ? (Tier 4 model-skew)
Correction to review line ~148: model skew does NOT "degrade silently" at the store level. checkModel (called by Search/SearchActive/FindSimilar) returns a hard error `model mismatch: query model %q does not match stored embeddings` when ANY stored row has a different model. The silent degradation happens at the callers that discard the error: internal/mcp/handlers.go:88 (`remoteResults, _ = remoteStore.SearchActive(...)`) and internal/api/handlers.go:569 area. Doctor's model-skew check can use checkModel semantics or `SELECT DISTINCT model`.

### NewStore call sites (complete list, non-test) — everything a NewStoreReadOnly split touches
- internal/namespace/registry.go:223 — `ResolveEmbeddingStore` (registry.go:184) opens REMOTE vault DBs; THE site to switch to read-only. Caches on `RemoteVault.EmbStore` (registry.go:23) forever — Tier-2 TTL issue lives here too.
- internal/api/handlers.go:450 — opens a mounted warren project's embeddings.db directly (`mount.Path/.marmot-data/embeddings.db`); ALSO a remote/read path the review's read-only fix must cover (review focuses on registry; don't miss this one).
- internal/mcp/engine.go:160 — buildEngine local vault store (must stay read-write).
- cmd/marmot/pipeline.go:75, 986, 1083, 1287 — local index/query pipeline (read-write; 986 and 1287 are query-side and could be RO candidates but are local).

### Method signatures the plan may reference
```go
store.go:143 func (s *Store) Upsert(nodeID string, embedding []float32, summaryHash string, model string) error
store.go:201 func (s *Store) Search(queryEmbedding []float32, topK int, model string) ([]ScoredResult, error)
store.go:326 func (s *Store) SearchActive(queryEmbedding []float32, topK int, model string) ([]ScoredResult, error)
store.go:403 func (s *Store) FindSimilar(queryEmbedding []float32, threshold float64, model string) ([]ScoredResult, error)
store.go:508 func (s *Store) StaleCheck(nodeID string, currentHash string) (bool, error)
store.go:302 func (s *Store) UpdateStatus(nodeID, status string) error
store.go:547 func (s *Store) Close() error
```

### store_wal_test.go:94-164 — reusable test template (Tier 1/2 tests)
`TestNewStore_ConcurrentConns` is the canonical two-connections-one-file concurrency harness (writer Upsert loop + reader SearchActive loop, 2s deadline, error recorder). Directly reusable for a "read-only open doesn't create -wal/-shm" test and for refresh-under-concurrent-search tests. `journalMode(t, s)` helper (lines 12-24) reads PRAGMA journal_mode via the unexported conn — useful for asserting a RO open does not flip journal mode. `TestNewStore_WALEnabled` (26-64) asserts -wal sidecar appears after Upsert — invert this for the RO test.

### mock.go:12-40 — MockEmbedder (hermetic test setup)
`NewMockEmbedder(model)` produces deterministic 1536-dim trigram-hash vectors (similar texts → similar vectors), no network. Standard hermetic vault fixture across the repo (see cmd/marmot/warren_test.go:363 which seeds a remote vault's embeddings.db with NewStore + MockEmbedder — template for warren e2e). provider.go:21 wires `mock`/`mock:<name>` provider names into NewEmbedder.

### Review-error flags
1. review line ~148: model skew is a hard error from checkModel, not silent empty results; silence is caller-side error-swallowing (mcp/handlers.go:88, api/handlers.go:450ff).
2. Review's read-only fix mentions only ResolveEmbeddingStore; internal/api/handlers.go:450 is a second remote-open of embeddings.db via plain NewStore that equally flips remote DBs to WAL.
3. `file:...?mode=ro` works, but `sqlite3.OpenFlags(path, sqlite3.OPEN_READONLY)` is the idiomatic ncruces call; either way initSchema and the WAL pragma must be skipped or the open fails.

---

## internal-graph

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

---

## internal-mcp

# internal/mcp findings

## internal/mcp

### engine.go:30-52 — Engine struct (Tier 2: VaultRegistry always-create + refresh safety)
`VaultRegistry` is a plain public field, set conditionally by pipeline; no mutex guards it (unlike `NSManager`, which has `nsMgrMu`). A daemon-era "always create + Rebuild" plan must either add locking or keep atomic-swap semantics — handlers read `e.VaultRegistry` unsynchronized on every query.
```go
30	type Engine struct {
31		NodeStore        *node.Store
32		graph            atomic.Pointer[graph.Graph]
33		EmbeddingStore   *embedding.Store
...
41		VaultRegistry    *namespace.VaultRegistry // optional; nil = single-vault mode
43		MarmotDir    string
44		LocalVaultID string   // cached from config; avoids repeated disk reads in handlers
45		nsMu         sync.Map // per-namespace write locks
46		nsMgrMu      sync.RWMutex
```

### engine.go:292-300 — WithVaultRegistry (Tier 2 wiring point)
Sets registry and caches `LocalVaultID` from config. Only production call site: `cmd/marmot/pipeline.go:273` (inside `if hasCrossVaultBridges || hasRoutes` — this conditional IS the Tier 2 "always-create" bug's origin; the fix lives in pipeline.go, but the signature here stays stable).
```go
292	func (e *Engine) WithVaultRegistry(vr *namespace.VaultRegistry) {
293		e.VaultRegistry = vr
294		if e.MarmotDir != "" {
295			if cfg, err := config.Load(e.MarmotDir); err == nil {
296				e.LocalVaultID = cfg.VaultID
297			}
298		}
299	}
```

### engine.go:334-348 — Engine.Close closes VaultRegistry (Tier 2 refresh-under-search constraint)
Any registry hot-swap/Rebuild must not race Close: `Close()` calls `e.VaultRegistry.Close()` after draining reindexes. A refresh that replaces the registry mid-flight must close the old one safely (remote embedding stores are cached inside the registry).
```go
344		if e.VaultRegistry != nil {
345			e.VaultRegistry.Close()
346		}
```

### engine.go:139-173 — NewEngine (embedding.NewStore call site, Tier 1 read-only remote opens)
The LOCAL store open — `embedding.NewStore(dbPath)` at line 160 — is correctly read-write; do not convert. The Tier 1 read-only fix targets `namespace.VaultRegistry.ResolveEmbeddingStore` (internal/namespace/registry.go:186), not this call.
```go
159		dbPath := filepath.Join(dataDir, "embeddings.db")
160		es, err := embedding.NewStore(dbPath)
```

### handlers.go:74-98 — remote-vault embedding search in HandleContextQuery (Tier 1 read-only DB opens; Tier 2 concurrent-refresh)
Review cited `handlers.go:81-88`; actual span is 74-98 (minor drift, semantics as described: errors swallowed best-effort at 82, and this is the consumer of `ResolveEmbeddingStore` that would open remote DBs read-write today). Iterates `KnownVaultIDs()` on every query — a registry Rebuild during this loop is the concurrency hazard.
```go
75	if e.VaultRegistry != nil {
76		for _, vid := range e.VaultRegistry.KnownVaultIDs() {
77			if vid == "" || vid == e.LocalVaultID {
78				continue
79			}
80			remoteStore, err := e.VaultRegistry.ResolveEmbeddingStore(vid)
81			if err != nil {
82				continue // best-effort
83			}
```

### handlers.go:287-290 — @-prefixed write rejection (Tier 3: MCP vs API @-write asymmetry)
Review cited 288-290; actual guard is 287-290 (accurate). Unconditional — rejects even when the vault is editable+materialized, which is exactly the asymmetry vs HTTP API. Fix must consult warren mount state (editable flag), which the Engine currently has no access to — a constraint: Engine would need warren manifest/mount info injected.
```go
287		if strings.HasPrefix(id, "@") {
288			return mcp.NewToolResultError("direct context_write to mounted Warren nodes is not supported; enable the project with marmot warren edit and use the Warren-aware API/UI write path"), nil
289		}
```

### namespace_handlers.go:115-125 — refreshNamespaceManager (Tier 2 reloadWarrenState template)
Existing in-process refresh pattern under `nsMgrMu`; the reloadWarrenState helper should follow this lock discipline (or extend it to cover VaultRegistry). Note: it merges a single namespace, does not reload bridges/routes.
```go
115	func (e *Engine) refreshNamespaceManager(ns *namespace.Namespace) {
116		e.nsMgrMu.Lock()
117		defer e.nsMgrMu.Unlock()
118		if e.NSManager != nil {
119			e.NSManager.Namespaces[ns.Name] = ns
120			return
121		}
```

### engine.go:64-72 + daemon consumer — SetGraph/GetGraph (Tier 2 owner-watcher wiring)
`atomic.Pointer` graph swap; `internal/daemon/owner.go:340` already calls `eng.SetGraph(newGraph)` — the template for hot-swapping warren state in the daemon owner.

### server_test.go:15-35 — testEngine/makeCallToolRequest/resultText (test templates, hermetic vault setup)
Standard hermetic harness: `t.TempDir()` + `embedding.NewMockEmbedder("test-model")` + `NewEngine` + Cleanup Close. Reusable for warren e2e units. NOTE: no mcp test sets `MARMOT_ROUTES=off` — these tests are hermetic only because `WithVaultRegistry` is never called with a routes table; any test that constructs a VaultRegistry via pipeline paths would read the global routing table. Confirms the review's hermeticity concern applies if warren tests are added here.

### server_test.go:217-236 — TestContextWriteRejectsMountedWarrenID (Tier 3 asymmetry test to update)
Asserts @-writes are always rejected; must be revised when editable+materialized writes are permitted.

### concurrency_test.go:13-55 — concurrent-write test template (Tier 1 flock RMW test pattern)
20-goroutine same-namespace write test with post-hoc graph assertions — direct template for the `_warren.md`/routes.yml flock RMW concurrency tests (single-process variant; multi-process still needs an exec-helper, which does not exist in this folder).

### Constraints / corrections summary
- Review line drift is minor only: handlers.go:81-88 → 74-98; handlers.go:288-290 → 287-290. No misread semantics found.
- No warren-specific code lives in internal/mcp beyond the two cited sites; copyDir/copyMarmotVault, updateWorkspaceState, ActiveMounts, ensureWorkspace, findWarrenMountByVault, buildEngine, refresh stubs are NOT in this package (buildEngine equivalent is cmd/marmot/pipeline.go; ActiveMounts consumer near pipeline.go:255).
- API stability: `Engine` public fields (VaultRegistry, LocalVaultID) and `With*` setters are used by cmd/marmot/pipeline.go and internal/daemon/owner.go; changes must keep those call sites compiling.
- mcp-go behavior: handlers return tool-result errors (`mcp.NewToolResultError`) not Go errors; warren-related failures surfaced to agents must follow that convention.

---

## internal-namespace

# internal/namespace

## internal/namespace

Review line citations for this folder verified accurate at commit 1f14f3e (`registry.go:76-142,186-241` matches). No line drift found. One semantic addition the review understates: `Refresh` (registry.go:145-159) closes the EmbStore in-place *before* reload — the Tier 2.3 "swap-then-close" hazard site is here, not only in cache-TTL logic.

### registry.go:186-241 — ResolveEmbeddingStore (Tier 1.1: read-only remote opens; Tier 2.3)
The exact site that must switch to a future `embedding.NewStoreReadOnly`. Note it also lazily calls `loadVaultLocked` if the graph isn't cached (line 232), so read-only opens must not break that path.

```go
186	func (r *VaultRegistry) ResolveEmbeddingStore(vaultID string) (*embedding.Store, error) {
...
222		dbPath := filepath.Join(vaultDir, ".marmot-data", "embeddings.db")
223		store, err := embedding.NewStore(dbPath)   // <- writes WAL pragma + schema into remote vault
224		if err != nil {
225			return nil, fmt.Errorf("open embedding store for vault %q: %w", vaultID, err)
226		}
...
239		rv.EmbStore = store
240		return store, nil
241	}
```

### registry.go:144-159 — Refresh (Tier 2.3: close-in-place under concurrent search)
Closes the cached EmbStore while a concurrent `context_query` may hold the pointer returned by ResolveEmbeddingStore (returned outside the lock). Fix must swap the `*RemoteVault` entry then close old store after, or refcount.

```go
145	func (r *VaultRegistry) Refresh(vaultID string) error {
146		r.mu.Lock()
147		defer r.mu.Unlock()
148		existing, ok := r.vaults[vaultID]
149		if !ok {
150			return fmt.Errorf("vault %q not loaded", vaultID)
151		}
153		if existing.EmbStore != nil {
154			_ = existing.EmbStore.Close()   // <- close-in-place; concurrent search can use closed DB
155		}
157		_, err := r.loadVaultLocked(vaultID, existing.VaultDir)
158		return err
159	}
```
Also: `Refresh` errors if the vault was never loaded — a "real refresh endpoint" calling it for every known vault must tolerate `not loaded`.

### registry.go:42-63 — NewVaultRegistry signature (Tier 2.1: always-create + Rebuild)
```go
42	func NewVaultRegistry(localVaultID, localDir string, bridges []*Bridge, rt *routes.RoutingTable) *VaultRegistry {
```
State to rebuild for the proposed `Rebuild(mounts, routes)`: `pathToID` (seeded only from `IsCrossVault()` bridges, lines 51-61), `routingTable`, plus flushing `vaults` cache (close EmbStores as in `Close()`, registry.go:244-252). `localVaultID`/`localDir` are immutable and unused after construction except as fields — Rebuild need not touch them.

Sole production constructor call site: `cmd/marmot/pipeline.go:272` inside the gate `hasCrossVaultBridges || hasRoutes` (pipeline.go:265-267) — confirms the review's "nil forever" claim. Other production consumers (must survive an always-non-nil registry): `internal/mcp/engine.go:41,251,291-293,304-307,344-345`; `internal/mcp/handlers.go:75-88` (KnownVaultIDs + ResolveEmbeddingStore, errors swallowed = Tier 1.6); `internal/api/handlers.go:459-460` (Refresh, error dropped), `538,565,589-592`; `internal/traversal/bridged.go:11-13` defines the interface `ResolveGraph(vaultID string) (*graph.Graph, error)` — Rebuild must not change that method signature.

### registry.go:119-142 — loadVaultLocked (Tier 2.3 TTL; Tier 1.6)
`LoadedAt` is set (line 137) but never read anywhere in the repo — confirms review's "never-checked LoadedAt". TTL/mtime check belongs in the ResolveGraph fast path (lines 78-83).

### namespace.go:362-381 — parseBridge (Tier 1.7 frontmatter parser)
Same defective `strings.Index(content[3:], "---")` pattern the review cites in warren.go:1110-1125 also exists here and in `extractEdgesFromFrontmatter` (namespace.go:540-548). The review only cites warren.go; the fix should cover these two sites too (any `---` in a YAML value or body truncates the frontmatter).

```go
363	func parseBridge(data []byte) (*Bridge, error) {
364		content := string(data)
365		if !strings.HasPrefix(content, "---") {
366			return nil, fmt.Errorf("missing YAML frontmatter")
367		}
368		end := strings.Index(content[3:], "---")   // <- matches "---" anywhere, not line-anchored
```
`parseNamespace` (namespace.go:166) uses the same pattern. Bridge parse errors are the pipeline.go:391-415 swallow site (Tier 1.6) — callers: `LoadBridge` (namespace.go:354) from `loadBridges` (namespace.go:124-138).

### namespace.go:601-703 — CreateCrossVaultBridge / writeCrossVaultBridgeTemp (Tier 1.5 flock scope check)
`CreateCrossVaultBridge(localVaultDir, remoteVaultDir string, allowedRelations []string) (*Bridge, error)` writes manifests into *both* vaults' `_bridges/` dirs via temp+rename (writeCrossVaultBridgeTemp, line 678). Write path is atomic-rename, not RMW, so it does NOT need the Tier 1.5 flock; only `_warren.md`/routes.yml do. No change needed here.

### inventory.go:71 — Doctor (Tier 4: doctor model-skew / collision checks)
```go
71	func Doctor(vaultDir string) ([]DoctorIssue, error) {
```
`DoctorIssue` struct at inventory.go top; natural home or template for the planned cross-warren vault-ID-collision and model-skew checks (those likely live in warren/doctor, but this is the existing per-vault doctor pattern to mirror).

### registry_embstore_test.go:11-23 — setupRemoteVault helper (test program: hermetic vault setup)
Reusable minimal-vault fixture: `t.TempDir()` + `_config.md` frontmatter with `vault_id` + one node file. Good template for warren e2e vault fabrication. Note: none of this package's tests set `MARMOT_ROUTES` — safe today because `NewVaultRegistry` takes `rt` explicitly and tests pass nil/explicit tables (no implicit `~/.marmot/routes.yml` read in this package; `routes.Load` is called only by callers). No hermeticity bug in this folder.

### Constraint summary
- `VaultRegistry` methods `ResolveGraph`/`Resolve`/`ResolveEmbeddingStore`/`Refresh`/`KnownVaultIDs`/`Close` are the public surface consumed by mcp, api, traversal; `ResolveGraph` signature frozen by `traversal.VaultResolver` interface (bridged.go:11-13).
- `embedding.NewStore` (internal/embedding/store.go:47) does BusyTimeout → WAL pragma → initSchema; a read-only variant must keep busy_timeout and skip the other two.
- Registry never writes remote vaults today (review's "nothing regresses" claim confirmed: no Upsert/write calls on `rv.EmbStore` in this package).

---

## internal-node

# internal/node

Small, warren-agnostic package (parser.go, writer.go, store.go, types.go + tests). No warren, daemon, embedding, manifest, flock, or routes references. It matters to the plan in three ways: (a) it contains a *second* frontmatter splitter with the same delimiter-anywhere family of bug as warren.go's `parseMarkdownYAML` (Tier 1.7 must fix both or the regression test suite is incomplete); (b) `node.NewStore` is instantiated at 12 non-test sites including remote/registry paths that Tier 2 Rebuild logic touches; (c) its atomic-write and hermetic t.TempDir test patterns are the templates the test program should reuse.

### parser.go:47-69 — splitFrontmatter (Tier 1.7 parallel bug)
Relevance: warren_review.md 1.7 cites only `warren.go:1110-1125` (`parseMarkdownYAML`). This package has an independent splitter with a related defect: the closing delimiter search `strings.Index(rest, "\n---")` (line 60) accepts any line *beginning* with `---` (e.g. `----`, `--- foo`, or a `---` line inside a YAML block scalar / multiline value) as the close, and `closingIdx+4` (line 66) assumes exactly `\n---` with body following. A frontmatter value containing a line starting with `---` truncates the YAML; a body is fine (body `---` occurs after the real close). Fix 1.7's anchored-regex approach should be applied/shared here; the review's claim that the bug is only in warren.go is incomplete for this folder.
```go
57	afterOpen := strings.Index(trimmed, "---") + 3
58	rest := trimmed[afterOpen:]
59
60	closingIdx := strings.Index(rest, "\n---")
61	if closingIdx < 0 {
62		return nil, "", fmt.Errorf("missing YAML frontmatter closing ---")
63	}
64
65	fm := rest[:closingIdx]
66	body := strings.TrimLeft(rest[closingIdx+4:], "\r\n")
```
Note: `RenderNode` (writer.go:68-70) emits `---\n<yaml>---\n`, so round-trip of a node whose Summary/Context/YAML value contains a line-leading `---` is the regression test the plan's "frontmatter `---`-in-body round-trip" item (review line 161) should target for BOTH parsers.

### parser.go:17-43, 130-145 — ParseNode / ParseNodeMeta signatures (API-stability constraint)
```go
17	func ParseNode(data []byte, filePath string) (*Node, error)
130	func ParseNodeMeta(data []byte, filePath string) (*NodeMeta, error)
```
Consumed widely (graph.LoadGraph via Store, api/handlers, mcp/engine, pipeline). Keep signatures stable; only splitFrontmatter internals need changing.

### store.go:59-61 — NewStore call sites (Tier 2 registry/Rebuild wiring)
```go
59	func NewStore(basePath string) *Store {
```
Non-test call sites the plan will touch when adding always-create VaultRegistry + Rebuild and refresh-under-search:
- internal/namespace/inventory.go:152, internal/namespace/registry.go:125 (registry paths)
- internal/mcp/engine.go:146 (buildEngine-side store)
- internal/daemon/owner.go:335 (`graph.LoadGraph(node.NewStore(dir))` — the owner watcher reload path; any reloadWarrenState helper will follow this pattern)
- internal/api/handlers.go:412, 858 (`node.NewStore(mount.Path)` — ActiveMounts/mount-path consumers; editable+materialized write-loss fix (Tier 1) lands where these stores write into mount paths)
- cmd/marmot/pipeline.go:51, 776, 955, 1076, 1209, 1283

### store.go:129-174 — SaveNode atomic temp+rename (Tier 1 copyDir/manifest-RMW template)
```go
129	func (s *Store) SaveNode(node *Node) error {
147	tmp, err := os.CreateTemp(dir, ".node-*.md.tmp")
168	if err := os.Rename(tmpPath, target); err != nil {
```
Relevance: the exact atomic-write pattern (temp in same dir, defer-cleanup `success` flag, rename) the Tier 1 fixes for `_warren.md`/routes.yml RMW and copyDir hardening should reuse. Note: no fsync — copyDir hardening may want to add it.

### store.go:228-270 — ListNodes silent-skip semantics (Tier 1.6 adjacent)
Files failing `ParseNodeMeta` are silently skipped (lines 256-259, `return nil // skip malformed files`), and dirs/files starting with `_` or `.` are skipped (238-245). Constraint: warren burrow/import copies must preserve `_`-prefixed control files even though this walker ignores them; also parser corruption from the 1.7 bug degrades silently here (no stderr warning), matching the review's swallowed-errors theme though it does not cite this site.

### node_test.go:321-401, 403-447, 593-630 — reusable test templates
Relevance: test program. `TestRoundtrip` (321) is the template for the `---`-in-body round-trip regression; `TestStore_SaveAndLoad` (403) and `TestAtomicWrite_NoPartialFile` (593) show the hermetic `t.TempDir()` vault-store setup with zero env dependence — no `MARMOT_ROUTES` isolation needed in this package (it never reads routes), so no hermeticity bug here.

### types.go:75-94 — NodeMeta / IsActive
`Status == "" || Status == StatusActive` (line 94) is the activeness rule remote/warren search must respect; no changes needed, listed as a constraint.

## Review-accuracy flags
- warren_review.md 1.7 scopes the frontmatter bug to `warren.go:parseMarkdownYAML` only; `internal/node/parser.go:60` has a sibling defect (closing `---` matched as prefix, not exact line; breaks on `---`-leading lines inside frontmatter values). Plan should fix/factor both, ideally into one shared anchored splitter.
- No line drift found for this folder (the review cites no internal/node lines directly).

---

## internal-routes

# Warren fanout findings

## internal/routes

Package is small (routes.go 246 lines + 2 test files). It owns `~/.marmot/routes.yml`. Directly relevant to Tier 1.5 (flock RMW), Tier 1.8 (MARMOT_ROUTES=off hermeticity), and Tier 2 (VaultRegistry always-create — the "hasRoutes" gate lives in the caller, cmd/marmot/pipeline.go).

### internal/routes/routes.go:34-45 — process-local mutex only (Tier 1.5)
Inter-process safety is *only* atomic rename; there is no flock. Review's claim is correct. `SetOverridePath` is test-only global state (not goroutine-parallel-test safe across packages, but used with `t.Cleanup` everywhere).

```go
32	// mu protects Load/Save within a single process. Inter-process safety
33	// is handled by atomic writes (tmp + rename).
34	var mu sync.RWMutex
36	// overridePath allows tests to redirect Load/Save away from ~/.marmot.
37	var overridePath string
41	func SetOverridePath(path string) {
```

### internal/routes/routes.go:53-70 — MARMOT_ROUTES precedence (Tier 1.8)
Env override already implemented exactly as review says: `off|none|0` disables (path==""), any other value redirects. `defaultPathLocked` requires mu held; note `SetOverridePath` beats env. One-line fix per test (`t.Setenv("MARMOT_ROUTES", "off")`) is accurate.

```go
53	func defaultPathLocked() string {
54		if overridePath != "" { return overridePath }
57		switch env := os.Getenv("MARMOT_ROUTES"); env {
58		case "":            // default location
60		case "off", "none", "0": return ""
62		default:            return env
```

### internal/routes/routes.go:112-145 — Save/SaveTo fixed .tmp name (Tier 1.5)
`tmp := path + ".tmp"` (line 136) — fixed name, so two *processes* saving concurrently can interleave WriteFile/Rename and one write is lost or a torn tmp is renamed. Same pattern duplicated in Update (line 187). Signatures: `func Save(rt *RoutingTable) error` (113), `func SaveTo(rt *RoutingTable, path string) error` (118).

### internal/routes/routes.go:150-196 — Update RMW (Tier 1.5)
Review cites `internal/routes/routes.go:150-196` — line numbers are ACCURATE, no drift. `func Update(fn func(rt *RoutingTable) error) error`. RMW is atomic *within one process* (holds package mu across read+write, inlining Load/Save to avoid re-lock) but NOT across processes: two marmot processes each read, modify, rename — last writer wins, dropping the other's Set/Remove. This is the natural site to add an flock (reuse internal/daemon flock utils) around lines 154-195; API needs no change.

```go
150	func Update(fn func(rt *RoutingTable) error) error {
151		mu.Lock()
152		defer mu.Unlock()
154		path := defaultPathLocked()
...
187		tmp := path + ".tmp"
191		if err := os.Rename(tmp, path); err != nil {
```

### internal/routes/routes.go:198-246 — table methods (Tier 2 context)
`(rt *RoutingTable) Get/Set/Remove/List` all take rt.mu; `Get` and the package are nil-safe (`Get` on nil rt returns "",false; List is NOT nil-safe). Warren mount wiring calls `rt.Set(mount.VaultID, mount.Path)` on an in-memory table only — mounts are never persisted to routes.yml (correct; persistence is only via `routes.Update` in namespace.go:666 bridge auto-registration).

### Call sites of routes API (functions the plan may change)
- `routes.Update`: single production caller — internal/namespace/namespace.go:666 (best-effort bridge auto-register, error swallowed with `_ =` — Tier 1.6 error-un-swallowing candidate).
- `routes.Load` + `routes.Save` RMW pairs (unprotected cross-process, same Tier 1.5 hazard as Update): cmd/marmot/route.go:89+97 and :114+125 (route add/remove commands). Plain Loads: route.go:36,142; cmd/marmot/pipeline.go:236.
- buildEngine warren wiring (Tier 2 "always-create VaultRegistry"): cmd/marmot/pipeline.go:236-276. VaultRegistry creation is gated at pipeline.go:266-267 `if hasCrossVaultBridges || hasRoutes` — with MARMOT_ROUTES=off and no bridges/mounts, no registry is created; the Tier-2 fix (always create + Rebuild) lands here, not in internal/routes. `warren.ActiveMounts(dir)` consumed at pipeline.go:242.
- `routes.SetOverridePath` test hygiene pattern (reusable for hermeticity fixes): internal/crossvault_integration_test.go:56-57,127-128,219-220,310-311; internal/namespace/namespace_test.go:440-441; cmd/marmot/surface_coverage_test.go:353-354,381-382.
- `namespace.NewVaultRegistry(localVaultID, localDir string, bridges []*Bridge, rt *routes.RoutingTable) *VaultRegistry` — registry.go:42; rt may be nil.

### Test templates worth reusing
- internal/routes/routes_test.go:56-113 — MARMOT_ROUTES env tests via `t.Setenv` (TestEnvOverrideDisables, TestEnvOverrideRedirectsPath, TestSetOverridePathWinsOverEnv): template for Tier 1.8 hermeticity assertions.
- internal/routes/routes_stress_test.go:15 TestStressConcurrentReadWrite and :362 TestStressConcurrentTableAccess_BugDocumented — goroutine-level concurrency templates; NOTE there is no multi-process (exec-helper) test here, so the flock fix needs a new exec-based test (borrow the daemon flock exec-helper pattern from internal/daemon).
- routes_stress_test.go:257-305 corrupt-YAML tests — template for the warren frontmatter-parser hardening tests (Tier 1.7).

### Review corrections
None substantive. Line cite 150-196 for Update is exact. One nuance the review under-states: the same fixed-`.tmp` last-writer-wins hazard also exists in the Load->Save pairs in cmd/marmot/route.go (add/remove commands), not just `routes.Update` — the flock fix should cover Save/SaveTo (or convert route.go to use Update) or the CLI paths stay racy.

---

## internal-traversal

# internal/traversal — warren-review facts

## internal/traversal

The warren_review.md inventory does not cite any line in this folder (grep for "traversal" in the review returns nothing), and no misreads were found. This package is nonetheless load-bearing for Tier 2 ("always-create VaultRegistry + Rebuild", refresh-safe-under-concurrent-search) because it defines the interface VaultRegistry must satisfy and it silently swallows resolve errors (relevant to Tier 1 error un-swallowing and Tier 3 unreachable-warren surfacing).

### bridged.go:10-22 — VaultGraphProvider interface + BridgedGraphResolver (API constraint for Tier 2)
Any "always-create VaultRegistry + Rebuild/TTL" change must keep `ResolveGraph(vaultID string) (*graph.Graph, error)` stable — this single-method interface is the only contract traversal has with `namespace.VaultRegistry` (registry.go:75-76 implements it). Swapping the registry instance atomically behind this interface is the safe refresh point for "refresh under concurrent search": `BridgedGraphResolver` is constructed per-request in mcp/engine.go, so replacing `Engine.VaultRegistry` (or making ResolveGraph internally rebuild) requires no traversal changes.

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

### bridged.go:27-50 and 55-63 — errors from ResolveGraph are swallowed (Tier 1 error un-swallowing / Tier 3 unreachable-warren surfacing)
`GetNode` returns `nil,false` and `GetEdges` returns `nil` when `ResolveGraph` errors — an unreachable/broken warren vault is indistinguishable from a nonexistent node. GraphResolver's signature (`GetNode(id) (*node.Node, bool)`) has no error channel, so surfacing must happen at the provider (VaultRegistry logging/collecting resolve failures) rather than here. Note GetNode deep-copies Edges (lines 43-48), so a registry Rebuild that replaces cached graphs cannot corrupt in-flight traversal results via edge mutation — supports concurrent-refresh safety.

```go
32	g, err := r.Vaults.ResolveGraph(vaultID)
33	if err != nil {
34		return nil, false
35	}
...
60	g, err := r.Vaults.ResolveGraph(vaultID)
61	if err != nil {
62		return nil
63	}
```

### bridged.go:82-92 — parseVaultPrefix (vault-ID grammar constraint)
`"@vault-id/node-id"` split on first `/`; vault IDs therefore cannot contain `/`. Relevant to the Tier 3 vault-ID collision refusal rules and the MCP-vs-API @-write asymmetry: whatever ID validation the plan adds must match this parser.

```go
82	func parseVaultPrefix(id string) (string, string) {
83		if !strings.HasPrefix(id, "@") {
84			return "", id
85		}
86		rest := id[1:]
87		idx := strings.Index(rest, "/")
88		if idx < 0 {
89			return "", id
90		}
91		return rest[:idx], rest[idx+1:]
92	}
```

### traversal.go:15-18 — GraphResolver interface (stability constraint)
`Traverse` (traversal.go:43) and `Compact` consume this; mcp/handlers.go:118-130 call `traversal.Traverse(resolver, cfg)` then `traversal.Compact(resolver, subgraph, budget)`. Plan should not need to touch these signatures.

```go
15	type GraphResolver interface {
16		GetNode(id string) (*node.Node, bool)
17		GetEdges(id string, direction graph.Direction) []node.Edge
18	}
```

### bridged_test.go:93-98 — mockVaultProvider (reusable test template)
In-memory `map[string]*graph.Graph` fake for VaultGraphProvider — the right template for hermetic warren traversal tests (no disk, no MARMOT_ROUTES). Used throughout bridged_test.go, bridged_integration_test.go, and bridged_stress_test.go (as `stressVaultProvider`); the multi-vault + superseded-node + budget-truncation scenarios in bridged_integration_test.go:533-664 are directly reusable patterns for the warren e2e test program.

```go
93	// mockVaultProvider implements VaultGraphProvider for testing.
94	type mockVaultProvider struct {
95		graphs map[string]*graph.Graph
96	}
98	func (m *mockVaultProvider) ResolveGraph(vaultID string) (*graph.Graph, error) {
```

## Consumers outside this folder (call sites the plan touches)
- cmd/marmot/pipeline.go:235-275 — buildEngine warren wiring: `warren.ActiveMounts(dir)` at :242, `rt.Set(mount.VaultID, mount.Path)` at :245 (gated on `mount.Available`), `warrenRuntimeBridges` at :248, and conditional `namespace.NewVaultRegistry(vaultID, dir, bridges, rt)` + `engine.WithVaultRegistry(vr)` at :272-273 — this is the exact conditional (`hasCrossVaultBridges || hasRoutes` at :266-267) Tier 2 "always-create VaultRegistry" removes.
- internal/mcp/engine.go:291-310 — `WithVaultRegistry(vr *namespace.VaultRegistry)` (:292) stores the registry and caches LocalVaultID; `graphResolver()` (:303-310) builds a fresh BridgedGraphResolver per call — natural hook for reloadWarrenState (swap `e.VaultRegistry`).
- internal/namespace/registry.go:75-76 — `func (r *VaultRegistry) ResolveGraph(vaultID string) (*graph.Graph, error)` is the concrete implementation (lazy load; where remote-graph cache TTL / Rebuild lands).

---

## internal-warren

# internal/warren

Folder = 1 source file (`warren.go`, 1829 lines) + 2 test files (`warren_test.go` 794, `warren_extra_test.go` 774). No DB/embedding code here — all `embedding.NewStore` opens (Tier 1.1) happen in `internal/namespace` / consumers, NOT this package. This package never opens SQLite; copies are byte-level.

## internal/warren/warren.go

### warren.go:1289-1315 — copyDir (Tier 1.2, 1.3)
Unfiltered copier used only by `Materialize` (burrow). Confirms review 1.3: no `IsRegular` check, copies `.env`/WAL sidecars, `d.IsDir()` false for a symlink-to-dir → `copyRegularFile` on it (io.Copy of dir open fails / symlink-to-file is followed), passes full `info.Mode()` (not `.Perm()`) to `os.OpenFile`, never clears stale target files on re-burrow.
```go
1289	func copyDir(source, target string) error {
...
1306			if d.IsDir() {
1307				return os.MkdirAll(dest, 0o755)
1308			}
1309			info, err := d.Info()
...
1313			return copyRegularFile(path, dest, info.Mode())
```

### warren.go:1317-1391 — importAlwaysExcluded + copyMarmotVault (Tier 1.2, 1.3 template)
The hardened copier to share: skip map at 1317-1323 (`.marmot-data/.env`, `-wal`, `-shm`, obsidian workspace files), `!info.Mode().IsRegular()` skip at 1352, `_config.md` sanitization at 1359, `.Perm()` at 1364/1366. No WAL checkpoint anywhere — review 1.2 confirmed for both import and burrow. Signature: `func copyMarmotVault(source, target string, opts ImportOptions) error`.

### warren.go:183-308 — ImportProject (Tier 1.2, 1.5)
`func ImportProject(root, sourceMarmotDir string, project Project, opts ImportOptions) (*Manifest, error)`. Copy happens via `copyMarmotVault(source, tmp, opts)` at :282 into a temp dir then `os.Rename` (:294) — checkpoint hook belongs before :282. Manifest RMW is Load(:184)→Save(:299) unlocked. Note review cited "1317-1368" for the import copy; the actual copy loop is 1325-1368 (line drift only, semantics correct).

### warren.go:972-980 — Materialize (Tier 1.2, 1.3, Tier 4.2 provenance)
```go
973	func Materialize(workspaceMarmotDir, warrenID string, project Project, warrenRoot string) (string, error) {
974		source := filepath.Join(warrenRoot, filepath.FromSlash(project.Path))
975		target := materializedProjectPath(workspaceMarmotDir, warrenID, project.ProjectID)
976		if err := copyDir(source, target); err != nil {
```
Natural place to write commit/mtime provenance (Tier 4.2) and clear target first. Sole caller: `cmd/marmot/warren.go:747`.

### warren.go:1065-1077 — updateWorkspaceState (Tier 1.5 flock point)
Confirmed unlocked Load→fn→Save; single choke point — flocking here covers `RegisterWorkspaceWarren` (:798-816), `Mount` (:819-844), `SetEditable` (:847-872).
```go
1065	func updateWorkspaceState(workspaceRoot string, fn func(*WorkspaceState) error) (*WorkspaceState, error) {
1066		state, body, err := LoadWorkspaceState(workspaceRoot)
...
1073		if err := SaveWorkspaceState(workspaceRoot, state, body); err != nil {
```
Manifest-side Load→Save pairs needing the same flock (all in this file): `Init` 131/140, `AddProject` 148/173, `ImportProject` 184/299, `RemoveProject` 327/351, `RenameProject` 368/395, `AddBridge` 406/432, `RemoveBridge` 464/488, `Format` 607/611. Lock key differs: manifest ops lock the warren root, workspace ops lock the workspace `.marmot`.

### warren.go:819-872 — Mount / SetEditable (Tier 1.4, 3)
`func Mount(workspaceRoot, warrenID string, projects []string, materialized bool)` — only ever adds (`addName` :836) and only sets `Materialized = true` (:838-840); confirms "no unmount / un-burrow" (Tier 3). `SetEditable` auto-mounts at :863 (`entry.ActiveProjects = addName(...)`) — review's :863 cite exact. Nothing forbids editable+materialized (Tier 1.4); the enforcement point for "refuse editable on materialized" is here (entry.Materialized is in scope).

### warren.go:920-970 — ActiveMounts (Tier 1.6, 2)
`func ActiveMounts(marmotDir string) ([]ProjectStatus, error)`. Swallow is at :928-933 (review said 929-934; off by one, same code): manifest load error → materialized fallback or silent `continue`, no warning. Also silent `LoadProjectMetadata` drops at :895, :945, :986 — all three cites exact. Consumers (must not break signature): `cmd/marmot/pipeline.go:242` (buildEngine wiring — the Tier 2 reloadWarrenState extraction source), `internal/api/handlers.go:545,841,947`. `findWarrenMountByVault` lives at `internal/api/handlers.go:946-`; its first-match-wins over this sorted slice + `preferredProjectPath` (:1011-1019, returns burrow cache when materialized) is the Tier 1.4 write-loss mechanism.

### warren.go:1110-1125 — parseMarkdownYAML (Tier 1.7)
Confirmed exactly as reviewed: `strings.Index(content[3:], "---")` matches `---` anywhere (even mid-line/mid-value), and `content[end+6:]` assumes the delimiter is exactly `---` with nothing else on the line. Callers: LoadManifest :657, LoadProjectMetadata :705, loadWorkspaceStatePath :758, sanitizedConfigBytes :1422, sourceVaultID :1514 — one fix covers all five. Writer counterpart `writeMarkdownYAML` (:1127-1166) emits `---\n` at line start, so anchored parsing (`\n---\n` / `^---$`) round-trips.

### warren.go:1127-1166 — writeMarkdownYAML (Tier 1.5 context)
Already atomic per-write (unique `os.CreateTemp` + rename), so the race is purely RMW-level, not torn writes. Note `routes.Update`'s fixed `.tmp` name problem does NOT exist here.

### warren.go:563-575 — Doctor duplicate_vault_id (Tier 3 collision)
Confirmed: collision detection is per-manifest only (`vaultIDs` map local to one warren, :502). Cross-warren detection needs workspace-level logic (ActiveMounts consumers or mount time), not this function. `func Doctor(root string) (DoctorReport, error)` — Tier 4.5 model-skew check would also live here (it already stats `embeddings.db` at :576 but never opens it; adding a model check means this package grows an embedding dep or the check moves to the caller).

### warren.go:875-918 — Status (Tier 3 unreachable-warren surfacing)
On manifest load failure with `entry.Materialized` false it returns the raw error (:889) — "warren X unreachable at <path>" messaging hooks here and in ActiveMounts.

### warren.go:1172-1183 — normalizeManifest version handling (Tier 4.6)
`if m.Version == 0 { m.Version = 1 }` — no upper-bound check anywhere; confirmed struct round-trip drops unknown YAML fields (plain `yaml.Unmarshal` into fixed structs).

## internal/warren/warren_test.go

### warren_test.go:722-772 — writeImportSourceVault helper (test program)
Reusable hermetic vault fixture: builds a `.marmot` with `_config.md` (incl. secret keys to assert sanitization), fake byte-file `embeddings.db` + `-wal`/`-shm`, `.env`, `_heat/`, `.obsidian/`. Confirms review: import tests use fake byte files, so a real-SQLite checkpoint test is genuinely missing. `mustNotExist` at :774.

### warren_test.go:318-341 — symlink-rejection test template
Pattern (symlink + `t.Skipf` when unsupported + assert manifest unmutated) is the template for the copyDir symlink/FIFO cases.

### Review error — Tier 1.8 cite "warren_test.go:329"
`internal/warren/warren_test.go:329` is inside `TestImportProjectRejectsSymlinkedDestinationParent` and never calls `buildEngine` or touches routes; this package's tests are already hermetic (pure t.TempDir, no env). The real offenders are `cmd/marmot/warren_test.go:380` and `cmd/marmot/pipeline_warren_test.go:44` (both call `buildEngine` with no `MARMOT_ROUTES` isolation — grep confirms zero `MARMOT_ROUTES`/`Setenv` in either file). The 1.8 fix belongs in cmd/marmot, not here.

## Constraints shaping fixes

- Package has no embedding/SQLite import today; adding `Store.Checkpoint()` calls for 1.2 either imports `internal/embedding` here (new dep, likely fine) or takes a `checkpoint func(path string) error` hook from callers.
- Exported API used by cmd/marmot + internal/api: `ActiveMounts`, `Status`, `Mount`, `SetEditable`, `Materialize`, `ImportProject`, `LoadWorkspaceState(FromMarmot)`, `RegisterWorkspaceWarren`, `Load/SaveManifest`, `Doctor` — signatures above; Tier 3 verbs (Unmount/Unregister/Dematerialize) are additive, no breakage.
- Flock reuse target: `internal/daemon/lock_unix.go:16 tryFlock(f *os.File)` (unix.Flock LOCK_EX|LOCK_NB) — currently unexported and non-blocking-only; RMW locking wants a blocking or retry variant, so either export a blocking helper from daemon or add a small lockfile util here. Exec-helper kill test template: `internal/daemon/lock_test.go:106-178`.
- `ProjectStatus.Path` semantics (checkout vs burrow cache, decided in `preferredProjectPath` :1011-1019) are load-bearing for `findWarrenMountByVault` and buildEngine routing — the 1.4 fix ("prefer checkout for editable") changes this one function.

---

## web

# web

## web

Frontend-only folder (TypeScript UI + Playwright e2e harness). No Go warren logic lives here, but it is a *consumer* of the warren HTTP API and the e2e harness is a reusable hermeticity template. No line drift found vs warren_review.md (the review does not cite web/ lines directly).

### web/src/api.ts:65-75
Consumer of the warren HTTP endpoints. Any Tier 2 refresh endpoint (`reloadWarrenState`) or Tier 3 verb changes must keep `/api/warrens` and `/api/warren/{id}/graph` response shapes stable, or update these fetchers. A new `/api/warren/refresh` endpoint could be wired to the existing `#refresh-btn` here.

```ts
65  export async function fetchWarrens(): Promise<WarrensResponse> {
66    const res = await fetch('/api/warrens');
67    if (!res.ok) throw new Error(`fetchWarrens: ${res.status}`);
68    return res.json();
69  }
70
71  export async function fetchWarrenGraph(warrenId: string): Promise<GraphResponse> {
72    const res = await fetch(`/api/warren/${encodeURIComponent(warrenId)}/graph`);
73    if (!res.ok) throw new Error(`fetchWarrenGraph: ${res.status}`);
74    return res.json();
75  }
```

### web/src/types.ts:42-75
API-stability constraint: these interfaces mirror the Go JSON shapes. `Provenance.editable`, `source` (`local | warren_mount | warren_materialized`), `warren_id`, `vault_id`, `qualified_id` and `WorkspaceWarren.{path,active_projects,editable_projects,materialized_projects}` are load-bearing for the UI. Tier 1 "editable+materialized write loss" and Tier 4 "manifest read-only policy" fixes must preserve or version these fields.

```ts
42  export interface Provenance {
43    source?: 'local' | 'warren_mount' | 'warren_materialized' | string;
44    warren_id?: string;
45    project_id?: string;
46    vault_id?: string;
47    marmot_dir?: string;
48    qualified_id?: string;
49    editable?: boolean;
50  }
...
66  export interface WorkspaceWarren {
67    path: string;
68    active_projects?: string[];
69    editable_projects?: string[];
70    materialized_projects?: string[];
71  }
72
73  export interface WarrensResponse {
74    warrens: Record<string, WorkspaceWarren>;
75  }
```

### web/src/detail-panel.ts:221-253
The UI already enforces read-only on non-editable warren nodes (relevant to Tier 3 "MCP vs API @-write asymmetry": the web UI gates writes on `provenance.editable`, so the API side is the place that must match). Any node lacking `provenance` is treated as writable; a node with `provenance` and falsy `editable` is blocked client-side only — server must enforce too.

```ts
221    const isReadOnlyWarrenNode = Boolean(node.provenance && !node.provenance.editable);
...
238    if (isReadOnlyWarrenNode) {
239      saveBtn.textContent = 'Read-only Warren Node';
240    }
...
242    const enableSave = () => {
243      if (isReadOnlyWarrenNode) return;
...
252      if (isReadOnlyWarrenNode) return;
```

### web/src/main.ts:167-199,246-291
Warren namespace selector uses `_warren/<id>` pseudo-namespace values; `loadGraph()` routes them to `fetchWarrenGraph`. `#refresh-btn` (line 197) just re-runs `loadGraph()` — it does NOT hit any server-side warren refresh; a Tier 2 real refresh endpoint would be a natural addition here. If `fetchWarrens()` throws, the whole namespace population falls into the catch (line 184) and degrades to a single 'default' option — Tier 3 "unreachable-warren surfacing" would want a softer failure than that.

```ts
167    const warrenData = await fetchWarrens();
...
179      opt.value = `_warren/${warrenId}`;
...
197    document.getElementById('refresh-btn')?.addEventListener('click', () => {
198      void loadGraph();
199    });
...
248    } else if (currentNamespace.startsWith('_warren/')) {
249      currentData = await fetchWarrenGraph(currentNamespace.slice('_warren/'.length));
```

### web/e2e/serve.sh:1-33
Reusable hermeticity template for the warren e2e test program (Tier 1 test-hermeticity + test-plan "zero warren e2e today"). Key trick: it isolates via `export HOME="$WORK"` rather than `MARMOT_ROUTES=off` — a warren e2e can copy this pattern and additionally set `MARMOT_ROUTES`. Requires prebuilt `bin/marmot`; fixture vault at repo `e2e/fixture/vault` copied to `$WORK/.marmot`, then `marmot index --dir` and `marmot ui --dir .marmot --port N --no-open`. Playwright drives it via `webServer` in playwright.config.ts (health URL `/api/version`).

```bash
19  # Isolate spawned marmot processes from the developer's real ~/.marmot state
20  # (e.g. routes.yml vault registrations) so the fixture server is hermetic.
21  export HOME="$WORK"
22
23  cp -R "$ROOT/e2e/fixture/vault" "$WORK/.marmot"
...
27  "$BIN" index --dir "$WORK/.marmot"
...
31  "$BIN" ui --dir .marmot --port "$PORT" --no-open &
```

### web/e2e/*.spec.ts (regressions.spec.ts, ui.spec.ts)
No warren coverage exists in the browser e2e suite (confirms review's "zero warren e2e today" for the UI layer too). regressions.spec.ts:161 shows the reusable pattern for asserting no dead API endpoints (`page.on('request', ...)` + console-error tracking) — directly reusable to validate a new `/api/warren/refresh` endpoint and warren dropdown behavior.

### Not present here
None of the plan's Go targets (embedding.NewStore, copyDir/copyMarmotVault, updateWorkspaceState, ActiveMounts, buildEngine, daemon watcher, ensureWorkspace, findWarrenMountByVault, refresh stubs, flock helpers, frontmatter parser) have call sites or definitions under web/; `web/embed.go` only exposes `//go:embed all:dist` as `web.Assets`.

