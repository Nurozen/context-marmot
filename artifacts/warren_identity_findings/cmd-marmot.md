# cmd/marmot scanner findings

## cmd/marmot

Branch verified: `multiprocess-lock-fix`. The CLI layer contains NO vault-ID resolution, LocalVaultID, or collision logic itself — all of that lives in internal/warren + internal/mcp; this folder is the thin verb/flag surface plus the tests that pin end-to-end behavior. Key R1/R2 hook points and heavy UX evidence below.

### main.go:78-126 (UX)
Top-level command dispatch and usage. `warren` is one of 18 commands; the doc comment (lines 3-23) is the only built-in command reference — there is no `marmot help <cmd>` and no onboarding/quickstart verb.
```go
123	func usage() {
124		fmt.Fprintln(os.Stderr, "usage: marmot <command> [flags]")
125		fmt.Fprintln(os.Stderr, "commands: version, init, configure, setup, index, query, serve, verify, status, watch, bridge, namespace, summarize, reembed, route, warren, ui, sdk")
126	}
```

### main.go:408-479 (R1/R2, UX)
`cmdBridge` cross-vault mode is path-heuristic (`looksLikeVaultPath`) and entirely separate from warren bridge activation — a second, older bridge surface a local-identity design must reconcile:
```go
410	func looksLikeVaultPath(s string) bool {
411		return strings.Contains(s, "/") || strings.Contains(s, "\\") ||
412			strings.HasSuffix(s, ".marmot") || s == "." || s == ".."
```
Calls `runCrossVaultBridgePipeline(localVault, remoteVault, *relations)` (main.go:460).

### warren.go:69-118 (UX)
Full warren subcommand inventory: init, register, list, project(add|import|list|remove|rename|set-readonly), bridge(add|list|remove), doctor, format, mount, unmount, burrow, status, edit, refresh, propose. No `identify` verb exists (R2 greenfield). Usage line:
```go
115	fmt.Fprintln(os.Stderr, "usage: marmot warren <init|project|bridge|doctor|format|register|unregister|list|mount|unmount|burrow|status|edit|refresh|propose> [flags]")
```

### warren.go:120-139, 243-298, 340-392 (UX — flag soup, concretely evidenced)
Every warren-repo verb carries THREE compat spellings: `--warren-dir` (default `.`) plus legacy `--root` override, `--id` vs positional project-id, `--aliases` CSV vs repeated `--alias`. E.g. project add:
```go
249	root := fs.String("warren-dir", ".", "Warren repository root")
250	rootCompat := fs.String("root", "", "Warren repository root")
253	idCompat := fs.String("id", "", "project ID")
254	aliasesCompat := fs.String("aliases", "", "comma-separated aliases")
257	fs.Var(&aliases, "alias", "project alias (repeatable)")
```
Also positional/flag interplay quirk (lines 274-278): with `--id`, a lone positional becomes the PATH. Workspace-side verbs use a different flag vocabulary (`--dir`, `--warren`) than repo-side verbs (`--warren-dir`) — two mental models in one command tree.

### warren.go:486-508 (UX)
`warrenProjectRename` — CLI just calls `warren.RenameProject(root, old, new)` and prints `Renamed project %q -> %q`. No `--move-dir` flag, no message about the directory staying at the old path; the "renames ID but not directory" behavior in the CONTEXT is pinned at the internal layer, and the CLI offers no affordance to fix it.

### warren.go:832-968 (R1 — mount verb; where alias-mount UX lands)
`warrenMount(args, isBurrow)`: parses `--dir/--warren/--materialize/--all/--drop`, loads workspace state + manifest, calls `warren.Mount(workspaceRoot, warrenID, projects, materialize)` (line 914), then `warnModelSkewOnMount` (922) and per-project `warren.Materialize` with rollback (924-964). There is NO self-mount / vault-id awareness at the CLI: any R1 "mounting your own project — treating as alias of the live vault" messaging (or refusal of `--materialize` on a self project) must be added here or surfaced from internal/warren errors. Note editable is NOT a mount flag — editability is a separate `warren edit` verb.

### warren.go:1211-1254 (R1 — editable gating surface)
`warrenEdit` calls `warren.SetEditable(workspaceRoot, warrenID, projectID, !off)` with no vault-id check at CLI level. R1's "refuse editable on self-mounts" will surface here as an error print; the success strings to keep consistent:
```go
1247	fmt.Printf("Project %q in Warren %q is read-only\n", ...)
1251	fmt.Printf("Project %q in Warren %q is editable in this workspace (also mounted — edit implies mount)\n", ...)
```

### warren.go:626-666 (R1 — doctor CLI)
`warrenDoctor --workspace` calls `warren.DoctorWorkspace(marmotDir, workspaceRoot)` (line 652) via `locateWorkspace` (read-only, never fabricates). Exit-code mapping in `printDoctorReport` (670-691): only error-severity issues fail. R1's mount/doctor consistency change is purely internal; CLI passes the report through.

### warren.go:1147-1192 (R2/UX — status display)
`warrenStatus` table has no identity column:
```go
1177	fmt.Fprintln(w, "PROJECT\tSTATE\tEDITABLE\tAVAILABLE\tPATH")
```
R1/R2 need a way to show "this project IS you / aliased to live vault" here and in `warrenList` (818: `WARREN_ID\tPATH\tREACHABLE\tACTIVE\tEDITABLE\tMATERIALIZED`). JSON shapes: status = `warren.Status()` passthrough; list = state + additive `reachable` (793-806) — R2 schema additions must stay additive.

### warren.go:751-774, 1509-1541 (R2 — register verb + workspace resolution)
`warrenRegister` is where R2 vault_id auto-detection at register time would hook (currently just `warren.RegisterWorkspaceWarren(workspaceRoot, id, path)`). `locateWorkspace` vs `ensureWorkspace` split (1514-1541): mutating verbs fabricate a mock-provider `_config.md` with NO `vault_id:` line (1535) — an identity verb using ensureWorkspace would create a workspace that has no vault_id to identify with.

### warren.go:1263-1388 (R1/R2 — refresh; provenance)
`warrenRefresh` touches `_warren.md` so a live daemon's watcher fires `ReloadWarrenState`; `--pull` ff-only + re-materializes stale caches keyed on `prov.SourceCommit != newHead` (1370). Under R1 aliasing, a self project's cache must be excluded from re-materialization (it has no cache) — currently the loop iterates all `entry.ActiveProjects` with caches, which is naturally safe if self-mounts never materialize.

### route.go:66-99 (R1 — manual rt.Set surface)
`marmot route add <vault-id> <path>` does `rt.Set(vaultID, abs)` (route.go:92) against the GLOBAL routing table — a user can manually recreate the exact stale-snapshot shadowing R1 removes from mount. R1 should decide whether `route add` with vault-id == local vault_id warns/refuses too. `route resolve` (129-143) is the debugging surface for "where does @id point".

### pipeline.go:249-270 (R1 — engine wiring, matches CONTEXT)
buildEngine always creates the registry and calls `ReloadWarrenState`:
```go
261	vr := namespace.NewVaultRegistry(vaultCfg.VaultID, dir, nil, routes.EmptyTable())
262	engine.WithVaultRegistry(vr)
263	if reloadErr := engine.ReloadWarrenState(); reloadErr != nil {
264		fmt.Fprintf(os.Stderr, "warning: warren state load failed: %v\n", reloadErr)
```
Note `NewVaultRegistry(vaultCfg.VaultID, dir, ...)` — the local vault_id + live .marmot dir are ALREADY handed to the registry at startup; R1's alias endpoint (workspace's own .marmot) is available here without new plumbing.

### pipeline.go:769-836 (R2 — verify's local-identity precedent)
`verify --bridges` already implements a local-identity notion: `localVaultID = vaultCfg.VaultID` (771), errors `"vault has no vault_id set; cross-vault bridge verification requires vault_id in _config.md"` (778), and picks the remote endpoint by comparing each bridge's SourceVaultID against localVaultID (785, 822-825). R2's identify design should align with this existing `_config.md vault_id` convention rather than invent a second identity field.

### Tests pinning current behavior (will need updating for R1/R2)
- warren_d_test.go:304-341 `TestWarrenDoctorWorkspaceCLI` — pins `vault_id_collision_workspace` as exit-1 for two DIFFERENT warrens sharing a vault_id (legacy state). Survives R1 but add a companion case: local-vault-id self-mount must be healthy post-R1.
- warren_test.go:330-410 `TestBuildEngineQueriesActiveWarrenMount` — pins that a mounted project with `vault_id: project-a` resolves via the WARREN copy path (rt.Set path). If the test workspace's own config gained a matching vault_id it would exercise R1 aliasing; currently the local config has no vault_id (line 337), so it tests the non-self case — keep, add self-mount alias sibling asserting live-vault resolution.
- warren_test.go:458-474 `TestBuildEngineAlwaysCreatesVaultRegistry` — expects `KnownVaultIDs()` empty with no mounts; under R1 a self-alias must also NOT appear in KnownVaultIDs (worth asserting explicitly).
- pipeline_warren_test.go:14+ `TestBuildEngineEnforcesWarrenBridgesForActiveMounts` — pins bridge-activation gating on ActiveProjects and registry contents (vault-b present, dormant vault-c absent). R1's "bridge endpoint = workspace's own .marmot for self" needs a new case here; helper `saveWarrenProject`/`writeTestConfig` (132-153) already parameterize vault_id.
- warren_test.go:497-538 `testWarrenRoot` helper sets `VaultID: projectID` for every fixture project — the fixture factory to extend for self-identity cases.
- surface_coverage_test.go:513-1090 (warren list/refresh/propose/mount/edit/status-JSON/doctor tests) — pin exact usage strings and exit codes; any flag-soup cleanup breaks these en masse (they are the compat contract: e.g. TestWarrenInterspersedFlagsAfterPositionals:938).
- warren_ux_test.go:63-398 — pins current mount refusal texts, burrow --drop, unmount cache-kept hint, unreachable messaging, edit auto-mount message.

### Out-of-date / notable vs CONTEXT
- Nothing in the CONTEXT is contradicted by this folder; however note the CONTEXT's "mount still runs rt.Set" happens in internal/mcp/warren_reload.go, not here — cmd/marmot's only rt.Set is the manual `route add` (route.go:92), an additional R1 surface the plan doesn't mention.
- UX friction not in the CONTEXT list: (1) `ensureWorkspace` silently plants a mock-provider `_config.md` without vault_id on mutating warren verbs (warren.go:1535); (2) repo-side vs workspace-side verbs use disjoint flag vocabularies (`--warren-dir/--root` vs `--dir/--warren`); (3) `warren init` accepts both `--id` and positional with fiddly arity rules (warren.go:132-139); (4) `warren project add --id X <positional>` reinterprets the positional as PATH (warren.go:274-278); (5) cross-vault `marmot bridge <path>` heuristic detection (main.go:410-413) is a parallel bridge system to warren bridges with different relation defaults ("calls,reads,writes,references,cross_project,associated" vs warren's "references").
