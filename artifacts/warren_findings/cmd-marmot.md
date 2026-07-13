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
