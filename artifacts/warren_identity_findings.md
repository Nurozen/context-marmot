# Findings: warren identity + UX pass

## Contents

- [cmd-marmot](#cmd-marmot)
- [docs](#docs)
- [e2e](#e2e)
- [internal-api](#internal-api)
- [internal-daemon](#internal-daemon)
- [internal-mcp](#internal-mcp)
- [internal-namespace](#internal-namespace)
- [internal-routes](#internal-routes)
- [internal-warren](#internal-warren)
- [web](#web)


## cmd-marmot

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


## docs

# docs

No code lives here; findings are documentation claims that pin current behavior (and must change with R1/R2) plus UX-surface evidence. 16 docs files; only warrens.md, architecture.md, bridges.md, current_limitations.md are relevant. The rest (benchmark, crud-classifier, data-structures, development, embedding-providers, implementation_plan, language_comparison, spec-*, typescript-sdk) do not touch warren identity or the warren UX surface.

## warrens.md

### docs/warrens.md:262-267 — R1 (MUST REWRITE: documents the exact behavior R1 removes)
The self-mount-with-warning behavior is documented as "the documented way to activate Warren bridges". R1's alias semantics obsolete this paragraph entirely; the C7 unconditional refusal changes the first sentence too ("from another Warren" scoping goes away).
```
262: Mounting refuses a project whose `vault_id` is already claimed by a project
263: mounted from another Warren (vault IDs are one flat routing namespace per
264: workspace; a duplicate would silently answer queries from the wrong
265: project). Mounting the Warren copy of the *local* project — same vault ID as
266: the workspace vault, the documented way to activate Warren bridges — is
267: allowed with a warning about the routing shadowing it causes.
```

### docs/warrens.md:224-228 — R1 (doctor/mount inconsistency documented as intended)
Doctor's `vault_id_collision_workspace` error is framed as catching *legacy* state ("Mount refuses new collisions") — but per the plan context, mount currently permits the local-vault collision that doctor flags as a hard error. This paragraph papers over the R1 inconsistency and needs updating alongside the fix (alias mounts must not trip this check post-R1).
```
224: `marmot warren doctor --workspace` runs in a consuming workspace instead of a
225: warren repo: it reports vault-ID collisions across the local vault and all
226: registered warrens' active projects (`vault_id_collision_workspace`, an
227: error). Mount refuses new collisions; this catches legacy state written by
228: older binaries.
```

### docs/warrens.md:291-302, 384-405 — R1 (editable gating docs; no self-mount exclusion documented)
"edit implies mount" (291) and editable/materialized mutual exclusion (400-405) are documented, but nothing says editable is refused on self-mounts — R1 adds that refusal; both sections need a sentence. Snippet at 291:
```
291: Enable writes for one project (edit implies mount — an unmounted project is
292: auto-mounted, and the command says so):
```

### docs/warrens.md:376-379 — R1 (bridge endpoint semantics that alias changes)
Bridge endpoints resolve project_id -> vault_id; "Both bridge endpoints must be active mounted projects" (line 378). Under R1 an aliased local endpoint is NOT a mounted project — this constraint statement must gain the alias case; under R2, an "identified" project satisfies it without any mount.
```
376: Both bridge endpoints must be active mounted projects. Dormant projects stay out
377: of the queryable graph even if a bridge references them, and relations not listed
378: in the Warren bridge are rejected on write.
```

### docs/warrens.md:53-67 — R1/R2 (vault_id identity contract; import preserves it)
```
58: warren_id: product-platform
59: vault_id: project-a-vault
...
65: `vault_id` is the ID used in qualified node references such as
```
Also 157-158: "Import rewrites the copied project `.marmot/_warren.md` to the target Warren/project identity" — vault_id preserved on import is the root cause of the R1 collision. R2's identify-verb docs slot naturally after the register section (~line 235).

### docs/warrens.md:104-194 — UX (author-side CLI surface; evidences flag soup + rename gap)
Verbatim: `--warren-dir` (106), `warren init --id` (112), `project import <id> <path> --vault-id --alias` (118-120), `project add <id> --vault-id --path --alias` (173-176, note `--path` here vs positional path on import), `--generate-id` on both (166, 184), `project rename project-a payments-api` (192). Rename doc confirms it's ID-only — nothing says the `projects/<project-id>/` directory is renamed, matching the known RenameProject gap. Consumer verbs use `--warren <id>` while author verbs use `--warren-dir <path>` — two different warren-selection idioms in one command family.

### docs/warrens.md:235-337 — UX (consumer verb surface; no quickstart)
register/list/mount(--all)/unmount/status/edit(--off)/burrow(--drop)/unregister(--force) all documented with rationale prose, but the doc is reference-shaped: there is no zero-to-first-bridge walkthrough anywhere in docs/ (confirms the onboarding gap). The one "how do I activate a bridge involving my own project" answer is the 265-267 self-mount paragraph — i.e., the current onboarding path IS the thing R1/R2 replace.

### docs/warrens.md:338-353 — R1/UX (propose)
Propose is documented as resolving "the sole editable project by default". Interaction with an R2 identified-local project (which is edited in place, never via editable mount) is undefined — doc will need an explicit statement.

### docs/warrens.md:457-462, 483-491 — UX (web UI warren capability, verbatim)
Confirms UI is read-only for warren management: graph selector entries plus an automatic refresh call only.
```
459: - `GET /api/warren/product-platform/graph` returns active mounted Warren nodes.
...
462: The web UI exposes active Warrens in the graph selector as `Warren <id>`.
...
485: - `POST /api/warren/{id}/refresh` reloads the serving engine's warren state
486:   directly. The web UI's refresh button calls this automatically when a
487:   Warren view is selected.
```

### docs/warrens.md:465-495 — R1 (freshness/reload; alias must be honored by reload path)
Documents that every register/mount/edit rewrite of `.marmot/_warren.md` is picked up by live daemon owners within ~1s via ReloadWarrenState. R1's skip-rt.Set-for-alias logic must hold on this hot-reload path too, not just startup; doc section unchanged in shape but the reload semantics description is where alias behavior should be mentioned.

## architecture.md

### docs/architecture.md:365-408 — R1/R2 (Warren Mounts section)
"Only active mounts are added to the engine" (380) and "active and available Warren project bridge endpoints are converted from project IDs to vault IDs and fed into the same cross-vault validation and vault registry path" (392-394) both change under R1 (alias endpoint = local .marmot path, no registry entry for the warren copy) and R2 (identified project participates with no mount at all). API scope list at 403-408 matches warrens.md.

## bridges.md

### docs/bridges.md:85-118 — R1 (warren project bridges)
```
116: Only active mounted Warren projects participate. A bridge to a dormant Warren
117: project resolves no nodes ...
118: accepted for writes until that project is mounted.
```
Same "must be mounted" claim; needs the alias/identity carve-out. Also 57: cross-vault bridges register in `~/.marmot/routes.yml` — the routing table R1's alias skips writing for the local vault_id.

## current_limitations.md

### docs/current_limitations.md — UX (stale relative to warren work)
Contains no warren section at all despite listing resolved/known limitations for older subsystems — the file predates the warren workstreams and neither documents warren limitations (self-mount shadowing, rename-ID-only, no UI management) nor got updated by workstreams A-D. Candidate for the UX pass.

## Out-of-date-context check
Nothing in the provided CONTEXT is contradicted by docs; docs/warrens.md:262-267 confirms the warn-not-refuse local self-mount exactly as the plan describes. One nuance: warrens.md:227 asserts "Mount refuses new collisions" for the cross-warren case, so C7 refusal already exists for non-local conflicts per docs — R1's "make refusal unconditional" is specifically about removing the local-vault exemption, matching the plan.


## e2e

# e2e scanner findings

## e2e

Scope note: the e2e folder contains no product code — it pins behavior. Relevance is (a) which
tests pin behavior R1/R2 will change (mostly: none directly — the self-mount/collision path has
ZERO e2e coverage today, a gap R1 must fill), (b) verbatim evidence of the actual warren CLI
surface and output strings for the UX pass, and (c) confirmation the web UI has no warren surface.

### e2e/warren_test.go:24-42 (constants) — R1/R2: no self-mount coverage exists
All warren e2e projects are imported with `--vault-id` values (`pa-vault`, `pb-vault`) that never
collide with the consumer workspace's local vault. The fixture vault `_config.md`
(e2e/fixture/vault/_config.md) has **no vault_id at all**:

```
---
version: "1"
namespace: default
embedding_provider: mock
token_budget: 8192
---
```

So no e2e test exercises refuseVaultIDCollision, the local-vault WARN path, self-mount aliasing,
or doctor's `vault_id_collision_workspace`. R1 needs new e2e scenarios (self-mount alias resolves
to live vault; `warren edit` refused on self-mount; true collision refused unconditionally;
doctor/mount agree). No existing warren e2e test pins the behavior R1 removes, so none should
break — but they should be re-run since they all traverse mount/ReloadWarrenState.

```go
24	const (
25		warrenID = "wa"
27		projA      = "pa"
28		projAVault = "pa-vault"
...
37		projB      = "pb"
38		projBVault = "pb-vault"
```

### e2e/warren_test.go:49-95 (seedWarren) — R2/UX: import surface
Verbatim import surface: `warren project import <projectID> <vaultPath> --vault-id <id>` and
`warren init --id <id>` run from the warren root. R2's "identified-local project" interaction with
import is currently untested. Note import here always *overrides* vault_id via `--vault-id`; the
context's claim "import preserves vault_id" is the default path not exercised by e2e.

```go
64		if out, err := runCLI(warrenRoot, "warren", "init", "--id", warrenID); err != nil {
80		if out, err := runCLI(warrenRoot, "warren", "project", "import", projA,
81			filepath.Join(srcA, ".marmot"), "--vault-id", projAVault); err != nil {
```

### e2e/warren_test.go:129-131 (burrowCachePath) — R1: bridge/materialized path convention
Pins the workspace-local materialized path (`internal/warren.materializedProjectPath`):

```go
130		return filepath.Join(consumer, ".marmot", ".marmot-data", "warrens", warrenID, "projects", projectID, ".marmot")
131	}
```

R1's alias must ensure self-mounts never materialize/serve from this path.

### e2e/warren_test.go:149-231 (TestWarrenRegisterMountQueryServe) — UX: full verb surface + `--dir` flags
Verbatim CLI surface used everywhere (note the flag pattern: `--dir .marmot` on every verb,
`--warren <id>` named flag, project ID positional):

```go
152		runCLI(consumer, "warren", "register", "--dir", ".marmot", warrenID, warrenRoot)
155		runCLI(consumer, "warren", "mount", "--dir", ".marmot", "--warren", warrenID, projA)
174		runCLI(consumer, "warren", "burrow", "--dir", ".marmot", "--warren", warrenID, "--materialize", projB)
197		runCLI(consumer, "query", "--dir", ".marmot", "--query", hotwalQuery)
```

UX friction evidence: every single warren invocation needs `--dir .marmot` even when run from the
project root — no default vault discovery is relied on in any e2e call. Context's "flag soup"
mentions `--root vs --warren-dir`; the e2e suite uses neither — only `--dir`, `--warren`, `--id`,
`--vault-id`, `--query`. (Verify against cmd/ source; e2e suggests --root/--warren-dir may be out
of date or unused on these paths.)

### e2e/warren_test.go:264-288 (status --json) — UX: output ergonomics friction, pinned schema
`warren status --json` output can be preceded by stderr warnings (test must scrape for the JSON
array — concrete evidence that machine-readable output is polluted):

```go
268		// The JSON array may be preceded by stderr warnings in CombinedOutput.
269		start, end := strings.Index(out, "["), strings.LastIndex(out, "]")
273		var statuses []struct {
274			ProjectID string `json:"project_id"`
275			Active    bool   `json:"active"`
276		}
```

R2 display work: status JSON schema currently has `project_id`/`active` (at minimum) — an
identified-local project needs representation here; this test will need updating for R2.

### e2e/warren_test.go:296-334 (index --force refusal) — R1-adjacent: pinned error text
Pins the B4 read-flock refusal message; the self-mount alias must NOT take this shared read lock
on the local vault against itself:

```go
318		if !strings.Contains(out, "open read-only by another marmot process") {
```

### e2e/warren_test.go:342-433 (editable write-back + lifecycle) — R1: editable gating tests
`warren edit --dir .marmot --warren <id> <proj>` (edit implies mount, output must contain
"editable"); MCP `context_write` to `@pa-vault/auth/login` lands in the warren CHECKOUT. R1's
"refuse editable on self-mounts" needs a sibling test; this test pins the non-self editable path
and should survive unchanged. Also pins teardown verbs verbatim: `burrow --drop --all`,
`unmount --all`, `unregister`, and `warren list` empty-state string `"No Warrens registered."`.

```go
348		out, err := runCLI(consumer, "warren", "edit", "--dir", ".marmot", "--warren", warrenID, projA)
352		if !strings.Contains(out, "editable") {
416		runCLI(consumer, "warren", "burrow", "--dir", ".marmot", "--warren", warrenID, "--drop", "--all")
430		if !strings.Contains(out, "No Warrens registered.") {
```

### e2e/warren_test.go:444-608 (TestWarrenGitRoadmapLoop) — R2/UX: refresh --pull, propose, provenance
Pinned output strings (error-message-quality inventory for the UX pass):
`"checkout pulled"`, `"Re-materialized burrow cache(s): <proj>"`, `"uncommitted change"`
(dirty refusal), `"never pushes"` (propose), branch pattern
`marmot/propose/<proj>-[0-9]{8}-[0-9]{6}`. Provenance file lives at
`.../warrens/<wid>/projects/<pid>/provenance.md` (sibling of the `.marmot` cache) and contains the
clone HEAD commit. R2 must define propose/refresh semantics for an identified-local project (today
propose is pathspec-limited to `projects/<pid>/` inside the warren clone — meaningless for a live
local vault).

```go
521		if !strings.Contains(out, "checkout pulled") {
524		if !strings.Contains(out, "Re-materialized burrow cache(s): "+projB) {
561		if !strings.Contains(out, "uncommitted change") {
579		if !strings.Contains(out, "never pushes") {
582		branch := regexp.MustCompile(`marmot/propose/` + projA + `-[0-9]{8}-[0-9]{6}`).FindString(out)
```

### e2e/warren_refresh_test.go:16-38 (seedWarrenFixture) — R2: workspace/warren state schema, hand-written
Pins the on-disk schemas R2's identity record must extend/migrate. Warren manifest and per-project
`_warren.md` frontmatter, verbatim:

```go
24		filepath.Join(warrenRoot, "_warren.md"): "---\nwarren_id: wp\nversion: 1\nprojects:\n    - project_id: proj-a\n      path: projects/proj-a/.marmot\n---\n",
25		filepath.Join(projVault, "_config.md"):  "---\nversion: \"1\"\nvault_id: proj-a-vault\nnamespace: default\nembedding_provider: mock\n---\n",
26		filepath.Join(projVault, "_warren.md"):  "---\nproject_id: proj-a\nwarren_id: wp\nvault_id: proj-a-vault\n---\n",
```

(The CONSUMER workspace `_warren.md` — where mounts/aliases live and where R2's identity record
would go — is only ever created via the CLI in e2e, never asserted structurally; R2 tests should
add schema assertions.)

### e2e/warren_refresh_test.go:45-92 (TestWarrenMountWhileOwnerLive) — R1: ReloadWarrenState liveness pin
The only e2e directly exercising ReloadWarrenState freshness: mount+`warren refresh` (output must
contain `"refreshed"`) from a second process while a MARMOT_DAEMON=1 owner serves; owner's watcher
fires on the workspace `_warren.md` touch (1s debounce, 20s poll). R1's "skip rt.Set for
self-mounts" changes this code path; this test uses a non-colliding vault_id so it should pass
unchanged, but it is the template for an R1 self-mount-alias liveness test.

```go
71		if !strings.Contains(out, "refreshed") {
77		// The owner's watcher fires on the _warren.md touch (1s debounce);
```

### e2e/e2e_test.go:112-125 (hermeticEnv/runCLI) — harness facts
Isolation is `HOME=<projdir>` only (routes.yml etc. land under temp HOME); `MARMOT_E2E_BIN` env
selects a prebuilt binary (line 35-39), otherwise `go build ./cmd/marmot`. New R1/R2 e2e tests
should follow this pattern. Build tag: `//go:build e2e` on all three files.

### e2e/e2e_test.go:997-1109 (TestUIServer) — UX: web UI has zero warren surface (CONFIRMS context)
The entire UI e2e coverage is `ui --dir .marmot --port N --no-open` plus exactly four endpoints:
`GET /`, `/api/graph/default`, `/api/namespaces`, `/api/search?q=`, `/api/node/default/auth/login`.
No warren endpoint exists or is tested — confirming the context claim that mount/unmount/edit/
refresh are CLI-only. Any UX-pass web work needs new endpoints + e2e subtests here.

```go
1002	cmd := exec.Command(binPath, "ui", "--dir", ".marmot", "--port", fmt.Sprint(port), "--no-open")
1076	code, body := get("/api/graph/default")
```

### Context-staleness flags
- "import preserves vault_id": true only for the default path; every e2e import overrides with
  `--vault-id`, so the preserving path (the one that creates R1's collision) is untested in e2e.
- Flag-soup examples `--root` / `--warren-dir` / `--aliases` never appear in e2e; the exercised
  surface is uniformly `--dir`/`--warren`/`--id`/`--vault-id`. Cross-check cmd/ before planning.
- Tests needing updates: R1 → none break, several new scenarios needed (self-mount alias, editable
  refusal, unconditional collision refusal, doctor consistency); R2 → warren_test.go:273-276
  status JSON struct and likely list/status assertions; migration test from R1 alias state.


## internal-api

# internal/api

## internal/api

Package is the HTTP API server. No mount/unmount/register/refresh-pull management endpoints exist here (read + one refresh verb only) — confirming the UX-PASS claim "web UI has zero warren management" holds at the API layer too. Warren-relevant code paths below.

### api.go:72-93 (UX)
Full route surface. Warren-related endpoints are read-only plus one refresh:
```go
80	s.mux.HandleFunc("GET /api/bridges", s.handleBridges)
82	s.mux.HandleFunc("GET /api/warrens", s.handleWarrens)
83	s.mux.HandleFunc("GET /api/warren/{id}", s.handleWarrenStatus)
84	s.mux.HandleFunc("GET /api/warren/{id}/graph", s.handleWarrenGraph)
85	s.mux.HandleFunc("GET /api/warren/{id}/status", s.handleWarrenStatus)
86	s.mux.HandleFunc("POST /api/warren/{id}/refresh", s.handleWarrenRefresh)
```
No POST/DELETE for mount, unmount, register, unregister, editable toggle, propose, or bridge activation. `/api/warren/{id}` and `/api/warren/{id}/status` are duplicate routes to the same handler (minor UX/API-surface redundancy). Also relevant: `PUT /api/node/{id...}` (line 76) is the editable-write path for `@vault/...` ids.

### handlers.go:399-483 handleWarrenNodeUpdate (R1/R2)
The API editable-write path resolves purely by vault_id via ActiveMounts — an R1 self-alias (vault_id == LocalVaultID) would match the warren mount here and, if editable were allowed, write to the warren copy (split-brain). R1's "refuse editable on self-mounts" must gate this; R2's identified-local endpoint needs a decision for `@<LocalVaultID>/...` PUTs (redirect to local node path vs. refuse).
```go
405	mount, ok := s.findWarrenMountByVault(vaultID)
407		writeError(w, http.StatusNotFound, "Warren mount not found for vault: "+vaultID)
410	if !mount.Editable {
411		writeError(w, http.StatusForbidden, "Warren project is read-only in this workspace: "+mount.ProjectID)
420	mu := s.engine.NamespaceLock("@" + vaultID)
424	store := node.NewStore(mount.Path)
467	writeWarning, err := warren.WriteEditableNode(mount, diskNode, vec, summaryHash, model)
479		if err := s.engine.VaultRegistry.Refresh(vaultID); ...
```
Note lock key `"@"+vaultID`: for a self-alias this collides with local vault_id semantics — fine only if self-writes are refused.

### handlers.go:980-994 findWarrenMountByVault (R1/R2)
Vault-id → mount resolution used by both the write path (405) and provenance (627). Returns the first mount whose `mount.VaultID == vaultID` with no LocalVaultID special-casing. R1 aliasing must either exclude self-mounts here or callers must check `vaultID == s.engine.LocalVaultID` first.
```go
988	for _, mount := range mounts {
989		if mount.VaultID == vaultID {
990			return mount, true
```

### handlers.go:561-612 searchMountedVaults (R1/R2)
Already contains the one existing LocalVaultID guard in this package — mounted-vault search silently skips the self vault:
```go
588	for vaultID := range mountByVault {
589		if vaultID == "" || vaultID == s.engine.LocalVaultID {
590			continue
```
Consequence today: with a self-mount, `_warren/<id>` scoped search (line 565 `warrenFilter`) excludes the local project's nodes entirely (they're skipped as self, and local results are filtered out at handlers.go:537 `if strings.HasPrefix(ns, "_warren/") && !strings.HasPrefix(sr.NodeID, "@")`). R1/R2 should make warren-scoped search include the LIVE local vault for an aliased/identified project instead of dropping it — concrete evidence of a hole to spec.

### handlers.go:614-640 resolveSearchNode (R1/R2)
`@vaultID/nodeID` resolution goes through `s.engine.VaultRegistry.Resolve(vaultID, nodeID)` (line 623) — depends entirely on the rt.Set routing done in ReloadWarrenState. Under R1 (skip rt.Set for self), `@<LocalVaultID>/x` would stop resolving here unless VaultRegistry.Resolve itself aliases to the live graph; provenance then reports `Source: "warren_mount", MarmotDir: mount.Path` (629-638) pointing at the stale copy — provenance semantics for self-alias need a spec decision (e.g. `Source: "local_alias"`).

### handlers.go:846-948 handleWarrenGraph (R1)
Warren graph view reads node stores directly from `mount.Path` (line 894 `node.NewStore(mount.Path)`) — for a self-mount this renders the STALE snapshot in the UI, not the live vault. R1 must make this handler substitute the workspace's own store for alias mounts (parallel to warrenRuntimeBridges using the workspace .marmot path). Node IDs are qualified `"@" + mount.VaultID + "/" + n.ID` (906) — self-alias IDs would equal LocalVaultID-qualified IDs.

### handlers.go:950-978 handleWarrenRefresh (R1)
Only mutating warren endpoint: validates warren is registered then calls `s.engine.ReloadWarrenState()` (line 970), engine-global by design (comment 950-954). Any R1 change to ReloadWarrenState's rt.Set skipping flows through here automatically; no per-endpoint change needed, but no `--pull` equivalent exists over HTTP (UX gap: API refresh cannot pull remote checkpoints).

### handlers.go:135-260 handleGraphAll bridge classification (UX, minor)
Bridge edges in the all-graph view are inferred by namespace heuristics (strategies at 213-229), not from warrenRuntimeBridges — the API has no endpoint exposing active manifest bridges/endpoints; `GET /api/bridges` (handlers.go ~740-786) returns engine bridge configs with `IsCrossVault` only. UX PASS: no endpoint surfaces per-bridge endpoint paths, staleness, or mount provenance for bridge management UI.

### types.go:83-115, 141 (UX/R2)
`WarrensResponse`/`WarrenStatusResponse` expose `warren.WorkspaceWarren` and `[]warren.ProjectStatus` verbatim — any R1 alias flag or R2 identity field added to workspace state/ProjectStatus is automatically serialized here (good), but the UI would need to render it. `BridgeInfo` (113) has no endpoint-path or local/remote distinction fields.

### Tests pinning current behavior (will need updating for R1/R2)
None currently exercise a self-mount / vault_id==LocalVaultID case — the alias behavior is untested in this package; new tests needed rather than edits, except where resolution semantics shift:
- api_test.go:135 `setupAPIWarren` (shared fixture — natural place to add a self-vault variant)
- api_test.go:292 `TestWarrenGraphAndEditableWritePolicy`
- api_test.go:358 `TestWarrenGraphQualifiesEmbeddedNodeEdges`
- api_test.go:411 `TestWarrenAPISearchAndUnknownWarrenPolicy` (pins the LocalVaultID-skip in searchMountedVaults indirectly)
- api_more_test.go:389 `TestWarrenListStatusRefreshSuccess`, 445 `TestWarrenRefreshPicksUpNewMount` (pins ReloadWarrenState via API; R1 rt.Set skip changes what "picked up" means for self-mounts)
- warren_editable_test.go:18 `TestWarrenEditableWriteLandsInCheckoutDespiteStaleMaterializedFlag`
- warren_write_equivalence_test.go:22/90/155 (editable write policy + read-only refusal text + lock serialization — R1 "refuse editable on self-mounts" adds a third refusal case to cover)
- warren_warnings_test.go:43/88/115/149 (warn-once and skip semantics; self-alias skip should not warn)

### UX friction evidenced in this folder
- Zero warren-management surface over HTTP: mount/unmount/register/editable/propose/refresh --pull are CLI-only; only read views + engine-global reload exist (api.go:82-86).
- Duplicate `GET /api/warren/{id}` vs `/{id}/status` routes.
- Read-only refusal message names only ProjectID, not how to fix: `"Warren project is read-only in this workspace: "+mount.ProjectID` (handlers.go:411) — no hint about `warren editable` or manifest read-only policy.
- Mount-unavailable/skip conditions are stderr warnings (handlers.go:890, 897, 985), invisible to web UI clients except `Skipped` project IDs in GraphResponse (types.go:54-55) with no reason strings.

### Out-of-date context check
Nothing in the provided CONTEXT is contradicted by this folder; note only that the API-side write path already fully shares warren.WriteEditableNode and per-mount locking (C8 landed as described).


## internal-daemon

# internal/daemon findings

## internal/daemon

Mostly infrastructure (daemon lock, socket, MCP proxy). Warren relevance is limited to one code path: the owner's fsnotify graph watcher, which is the daemon-side trigger for `Engine.ReloadWarrenState` — the exact function R1 will change (skipping `rt.Set` for self-mount aliases). No CLI subcommands, flags, or web UI live here.

### owner.go:257-261 — warren state file watch (R1/R2)

The watcher treats the workspace `_warren.md` (inside the watched `.marmot` dir) as the cross-process signal that warren wiring must reload:

```go
257	// The workspace warren mount state lives directly inside the watched
258	// root; every warren CLI mutation rewrites it atomically (and `marmot
259	// warren refresh` touches it), so this one file is the cross-process
260	// signal that warren wiring — not just the graph — must reload.
261	warrenStatePath := filepath.Join(dir, "_warren.md")
```

R2 note: if first-class local identity is recorded in workspace state via a different file than `_warren.md`, this watch will not fire; if it stays inside `_warren.md`, no daemon change is needed. R1's alias behavior lives entirely in `Engine.ReloadWarrenState` (internal/mcp/warren_reload.go) — the daemon just calls it.

### owner.go:307-313, 330-337 — reload trigger and call site (R1)

```go
309				if event.Name == warrenStatePath {
310					warrenPending = true
311					schedule()
312					continue
313				}
...
330				if warrenPending {
331					warrenPending = false
332					if err := eng.ReloadWarrenState(); err != nil {
333						fmt.Fprintf(os.Stderr, "daemon: warren state reload failed: %v\n", err)
334					} else {
335						fmt.Fprintln(os.Stderr, "daemon: warren state reloaded")
336					}
337				}
```

Debounce is 1s (owner.go:265). Failures only log to stderr — a self-mount that ReloadWarrenState starts refusing under R1 would be silently swallowed here (UX: no surfaced diagnostic beyond daemon stderr; doctor is the only user-visible path).

### warren_watch_test.go:21-110 — test pinning current behavior (R1: will need updating/extending)

`TestGraphWatcherReloadsWarrenState` builds a workspace with `vault_id: local-vault` and mounts a warren project with a DIFFERENT vault_id (`proj-a-vault`); asserts `eng.VaultRegistry.KnownVaultIDs()` gains `proj-a-vault` and evicts a seeded stale route. It does NOT cover the self-mount case (warren copy with vault_id == LocalVaultID), so it will not break under R1 — but R1 should add a sibling test here: mount a warren project whose vault_id equals `local-vault` and assert the registry does NOT gain a route pointing at the warren copy (alias resolves to live vault path). Key existing assertions:

```go
93	if _, err := warren.Mount(workspace, "wp", []string{"proj-a"}, false); err != nil {
...
102		if k["proj-a-vault"] && !k["stale-vault"] {
103			return // reloaded: mount visible, stale route swapped out
```

### UX PASS — no user-facing surface

No subcommands, flags, help text, or API endpoints in this folder. User-visible strings are daemon stderr logs only (`daemon: warren state reloaded`, `daemon: warren state reload failed: %v`, `daemon: graph reloaded (%d nodes)`) plus proxy/lock errors (lock.go:20-39 `ErrHeld`, `ErrOwnerGone`, `ErrNoResume`, `ErrUnsupported`). None are warren-UX candidates beyond the silent-reload-failure note above.

### Context-staleness check

Nothing in the PROJECT CONTEXT is out of date w.r.t. this folder; the B3.3 watcher behavior described in the warren resolution landed as stated.


## internal-mcp

# internal/mcp scanner findings

## internal/mcp

### warren_reload.go:22-59 — ReloadWarrenState (R1 primary change site)
R1: This is the exact site where a self-mount's warren-copy path overwrites the live local vault's route. The loop does NOT special-case `mount.VaultID == e.LocalVaultID`; the collision only produces a stderr warning at line 46-48 (only if the ID was *already* in routes.yml — the local vault's own path is typically NOT in the loaded routes table, so a self-mount often sets the route silently with no warning at all). R1 must add a skip here.

```go
42	for _, mount := range mounts {
43		if mount.VaultID == "" || !mount.Available {
44			continue
45		}
46		if prev, ok := rt.Get(mount.VaultID); ok && prev != mount.Path {
47			fmt.Fprintf(os.Stderr, "warning: vault ID %q claimed by both %s and %s; using %s\n", mount.VaultID, prev, mount.Path, mount.Path)
48		}
49		rt.Set(mount.VaultID, mount.Path)
50	}
```

CONTEXT correction: the plan cites "rt.Set(vaultID, warrenCopyPath) in warren_reload.go:49" — confirmed accurate at commit-time. Note the engine has `e.LocalVaultID` available right here (see engine.go:44), so the R1 skip is a one-line guard: `if mount.VaultID == e.LocalVaultID { continue }` plus refusing/skipping in bridge derivation below. Caveat: ReloadWarrenState does not consult LocalVaultID today at all; also LocalVaultID is only populated via `WithVaultRegistry` (engine.go:329-337) — a registry-less engine has `LocalVaultID == ""`, but ReloadWarrenState already no-ops when `e.VaultRegistry == nil` (line 23), so the invariant "LocalVaultID set whenever reload runs" holds *if* every WithVaultRegistry caller has a loadable config. If config load fails, LocalVaultID stays "" silently and an R1 guard keyed on it would not fire — plan should harden that.

### warren_reload.go:101-178 — warrenRuntimeBridges (R1 bridge-endpoint change site)
R1: Bridge endpoints take `source.Path` / `target.Path` straight from the mount's ProjectStatus — i.e., the warren snapshot path, never the workspace's own `.marmot`. R1 needs: when `source.VaultID == LocalVaultID` (or target), substitute `e.MarmotDir` as `SourceVaultPath`/`TargetVaultPath` and treat the endpoint as active even without a mount. Note the function is currently a free function taking `(marmotDir string, mounts []warren.ProjectStatus)` — it has no LocalVaultID; the signature must grow (or become a method).

```go
152	runtimeBridge = &namespace.Bridge{
153		Source:          bridge.Source,
154		Target:          bridge.Target,
155		SourceVaultID:   source.VaultID,
156		TargetVaultID:   target.VaultID,
157		SourceVaultPath: source.Path,
158		TargetVaultPath: target.Path,
159	}
```

Also relevant for R2: a bridge only activates if BOTH endpoints are in `activeProjects` (lines 144-147), i.e. both are mounted. R2's "identified local project" needs this loop to accept a local-identity endpoint with no mount — the natural design is to synthesize a pseudo-ProjectStatus `{VaultID: LocalVaultID, Path: marmotDir, Active/Available: true}` keyed by the identified project ID per warren.

### warren_reload.go:60-91 — setWarrenBridges / crossVaultBridges
R1/R2: bridge recomposition (fileBridges ++ warrenBridges) and registry seeding; no vault-ID awareness here — unaffected mechanically, but the registry Rebuild at line 57 receives the routing table with the self-route already poisoned, which is why @local-id resolves to the stale snapshot. Fixing the rt.Set skip fixes registry resolution too; no change needed in these helpers.

### engine.go:44, 329-337 — LocalVaultID caching
R1/R2: `LocalVaultID string // cached from config` is set only in WithVaultRegistry:

```go
328	func (e *Engine) WithVaultRegistry(vr *namespace.VaultRegistry) {
329		e.VaultRegistry = vr
330		// Cache local vault ID to avoid repeated disk reads in handlers.
331		if e.MarmotDir != "" {
332			if cfg, err := config.Load(e.MarmotDir); err == nil {
333				e.LocalVaultID = cfg.VaultID
334			}
335		}
336	}
```

Silent swallow of config.Load error → empty LocalVaultID → any R1 alias guard silently disabled. R2's "identify" verb likely also wants a place in Engine state; today the only local-identity notion in this package is this string.

### engine.go:285-300 — validateCrossVaultEdges
R1: edge validation calls `e.NSManager.ValidateCrossVaultEdge(e.LocalVaultID, qid.VaultID, relation)`. Under R1 aliasing, an @LocalVaultID edge target becomes an edge to *self*; `qid.VaultID != ""` still trips and validation asks for a bridge LocalVaultID↔LocalVaultID. Plan must decide: normalize `@LocalVaultID/x` → local `x` at parse time, or allow the self-pair. Whole function no-ops if `e.LocalVaultID == ""` (line 288) — another spot where a missing config silently changes semantics.

### handlers.go:75-107 (approx; see 79-80) — cross-vault fanout in HandleContextQuery
R1: the remote-search loop already skips the local vault ID:

```go
79	for _, vid := range e.VaultRegistry.KnownVaultIDs() {
80		if vid == "" || vid == e.LocalVaultID {
81			continue
82		}
```

So query fanout is already alias-safe for embedding search — but graph traversal is not: results found remotely are prefixed `@vid/`, and today a self-mount makes `@LocalVaultID/...` resolvable via the registry to the STALE path (BridgedGraphResolver, engine.go:339-347 uses `Local: e.GetGraph(), Vaults: e.VaultRegistry`). After R1's rt.Set skip, `@LocalVaultID/x` becomes unresolvable via registry — traversal/parse code must map it to the local graph.

### handlers.go:580-693 — handleWarrenContextWrite (R1 editable gating)
R1: @-writes are gated ONLY on finding an ActiveMounts entry with matching VaultID and `mount.Editable`:

```go
597	for _, m := range mounts {
598		if m.VaultID == vaultID {
...
604	if !found || !mount.Editable {
605		return mcp.NewToolResultError(fmt.Sprintf("vault %q is not an editable warren mount in this workspace; run 'marmot warren edit --warren <id> <project>'", vaultID)), nil
```

There is no `vaultID == e.LocalVaultID` check: today an *editable self-mount* accepts `@local-vault-id/x` writes and writes to the warren snapshot via `warren.WriteEditableNode(mount, ...)` — the split-brain write path R1 must close. R1's "refuse editable on self-mounts" should ALSO add a guard here (defense in depth for pre-existing state where an editable self-mount is already recorded), either redirecting to the local store or erroring with "write locally without the @ prefix". Then line 679-682 refreshes `e.VaultRegistry.Refresh(vaultID)` — for the self case that refreshes a registry entry that (post-R1) won't exist.

### server.go:88-94 — context_write tool description (UX / R1 doc surface)
UX: MCP tool help text the agent sees; must be updated if R1/R2 changes @local semantics:

```go
93	When referencing nodes in other vaults, use @vault-id/node-id format. Cross-vault edges require a cross-vault bridge with the relation type whitelisted.
94	Writing with an @vault-id/node-id ID updates that existing node in an active *editable* warren mount (summary/context/tags only; the write lands in the mounted project's own checkout). Read-only or unmounted vaults reject @-writes.`),
```

UX friction evidenced in this package's error strings: handlers.go:605 tells users to run `marmot warren edit --warren <id> <project>` (flag+positional mix — matches the plan's "flag soup" candidate); handlers.go:640 wording "refusing to blank a mounted node" is good; handlers.go:637 not-found message correctly points at the project's own workspace.

### Tests pinning current behavior (will need updating for R1/R2)
- warren_reload_test.go:25-44 `warrenEngine` — local vault_id is hardcoded `local-vault`; all fixtures use distinct remote vault IDs (`proj-a-vault` etc.), so NO existing test covers the self-mount/alias case. R1 needs new tests: mount project with vault_id `local-vault`, assert route NOT set, bridge endpoint == workspace .marmot, editable self-mount refused.
- warren_reload_test.go:124 TestReloadWarrenStateMountUnmount, :177 TestReloadWarrenStateBridgeIdempotent, :230 NilRegistry, :244 Serialized, :273 ConcurrentQuery, :306 RuntimeBridgeKeyOrdering, :312 EmptyNamespaceManager, :323 WarnsOnUnreadableManifest — bridge tests break if warrenRuntimeBridges signature changes (it's called directly? verify — currently only via ReloadWarrenState in prod, but the unreadable-manifest test calls it directly at :323).
- warren_write_test.go:16 TestContextWriteEditableWarrenMount, :82 TestContextWriteWarrenMatrix — pin the exact error strings ("marmot warren edit", "not an editable warren mount", "not found"); the matrix test needs a fourth case (self-mount refusal) under R1.
- coverage_test.go:75 TestWithVaultRegistryCachesLocalVaultID, :457 TestValidateCrossVaultEdges — direct pins on LocalVaultID caching and edge validation; must extend for alias normalization.
- crossvault_write_test.go:19-395 (9 tests) — cross-vault edge behavior with `@vault/...` targets; if R1 normalizes `@LocalVaultID/x`, add a case asserting self-qualified edges are treated as local.

### Callers of ReloadWarrenState (context for R1 blast radius, outside this folder)
cmd/marmot/pipeline.go:263 (startup), internal/daemon/owner.go:332 (_warren.md watcher), internal/api/handlers.go:970 (HTTP refresh endpoint). All funnel through the single warren_reload.go path — R1's skip lands in one place and covers all three.

### UX PASS relevance of this folder
This folder has no CLI subcommands or web UI; its user-facing surface is MCP tool schemas/descriptions (server.go) and tool error strings (handlers.go). Notable: context_write arg-alias validation (handlers.go:236-289) is a good existing error-quality pattern to replicate in the warren CLI verbs. The warren HTTP refresh endpoint lives in internal/api, not here.


## internal-namespace

# internal/namespace

## internal/namespace

### registry.go:54-79 — VaultRegistry stores localVaultID/localDir but NEVER uses them (R1 core fact, R2)

`localVaultID` and `localDir` are struct fields set by the constructor and referenced nowhere else in the package (grep confirms only lines 56, 68, 70). All vault-ID resolution ignores them — so an `@<local-vault-id>/node` reference or a bridge endpoint whose vault_id equals the local one is resolved like any remote vault: routing table first, bridge paths second. This is exactly why `rt.Set(localVaultID, warrenCopyPath)` (done upstream in internal/mcp/warren_reload.go) routes self-references to the stale warren snapshot. R1's alias fix has two candidate seams: skip `rt.Set` upstream, and/or short-circuit here.

```go
54	type VaultRegistry struct {
55		mu           sync.RWMutex
56		localVaultID string
57		localDir     string
58		vaults       map[string]*RemoteVault // vault_id -> loaded vault
59		pathToID     map[string]string       // vault_path -> vault_id (from bridges)
60		routingTable *routes.RoutingTable    // global routing table; may be nil
61		graphTTL     time.Duration           // 0 = cached graphs never expire
62	}
```

```go
68	func NewVaultRegistry(localVaultID, localDir string, bridges []*Bridge, rt *routes.RoutingTable) *VaultRegistry {
```

R1 option in this package: in `dirForLocked` (or at the top of `ResolveGraph`/`ResolveEmbeddingStore`), return `r.localDir` when `vaultID == r.localVaultID` — but note that loads a *second* copy of the local graph read-only from disk rather than aliasing to the Engine's live in-memory graph. A true alias (live graph, live embedding store) must be wired at the Engine/mcp layer, not here; the registry can only offer "read the local dir fresh". Plan should decide which semantics R1 wants.

### registry.go:116-130 — dirForLocked: resolution priority, the site self-mount routing flows through (R1)

```go
118	func (r *VaultRegistry) dirForLocked(vaultID string) string {
119		if r.routingTable != nil {
120			if p, ok := r.routingTable.Get(vaultID); ok {
121				return p
122			}
123		}
124		for path, id := range r.pathToID {
125			if id == vaultID {
126				return path
127			}
128		}
129		return ""
130	}
```

Routing table wins over bridge-manifest paths, so once `rt.Set(localID, warrenCopy)` has run, even a bridge manifest carrying the workspace's own path (`SourceVaultPath`) is shadowed by the stale route. Also note `pathToID` iteration is map-ordered — if two paths claim the same vault_id (local + warren copy), which path wins is nondeterministic. R1's unconditional collision refusal removes that ambiguity upstream.

### registry.go:137-163 — Rebuild evicts by dir change; alias transitions will hit this (R1/R2)

`Rebuild(bridges, rt)` (called from VaultRegistry.Rebuild freshness path after ReloadWarrenState) evicts a cached vault when `dirForLocked(id) != rv.VaultDir`. Under R1, unmounting a self-alias (or R2's identify migration) changes the local vault_id's resolution from warren-copy path to "" or localDir — this eviction logic already handles the cache side correctly, provided the routing inputs stop carrying the warren-copy route. No change likely needed here, but the R1 tests should assert eviction of a previously-cached self-copy after alias adoption.

### registry.go:188-212, 295-373 — ResolveGraph / ResolveEmbeddingStore treat every vault as remote read-only (R1)

`ResolveEmbeddingStore` opens `<dir>/.marmot-data/embeddings.db` with `embedding.NewStoreReadOnly` and takes a shared `vault.read.lock` via `flock.TryShared` (lines 328-340). If R1 aliases self-mounts to the live local `.marmot` dir *through the registry*, the registry would open a second read-only connection to the local embeddings DB and take a shared read lock against the workspace's own `index --force` — harmless but redundant. If R1 instead resolves self-references at the Engine layer (bypassing the registry entirely for `vaultID == LocalVaultID`), none of this fires. Argues for the Engine-layer alias.

### registry.go:28, 85-98 — graph TTL default 60s + MARMOT_WARREN_TTL env (R1 context)

Cached remote graphs go stale for up to 60s (`defaultGraphTTL`). For a self-mount today this means `@local-id` reads can lag the live vault by *both* the checkpoint-copy staleness and up to 60s of cache TTL. Under an R1 alias, local reads should have zero staleness — another reason to bypass the registry for the local ID.

### namespace.go:312-336 — ParseQualifiedID: "@vault-id/node" has no local-ID special case (R1/R2)

```go
316		if strings.HasPrefix(target, "@") {
317			rest := target[1:]
318			parts := strings.SplitN(rest, "/", 2)
319			if len(parts) < 2 || parts[0] == "" {
320				// Invalid cross-vault reference (e.g., "@", "@/node") — treat as local.
321				return QualifiedID{Namespace: currentNamespace, NodeID: target}
322			}
323			return QualifiedID{VaultID: parts[0], NodeID: parts[1]}
324		}
```

`QualifiedID.VaultID` doc (line 60) says `"" = local vault`, but an explicit `@<own-vault-id>/node` still produces a non-empty VaultID and is resolved as cross-vault. R1/R2 must decide whether the alias normalization happens here (Manager would need to know the local vault ID — it currently doesn't; `Manager` has only VaultDir/Namespaces/Bridges) or at the consumer. The Manager not knowing the local vault_id is a gap R2's "identified local project" design should note.

### namespace.go:37-56 — Bridge struct + IsCrossVault (R1: bridge endpoint path selection)

Bridges carry `SourceVaultPath/TargetVaultPath` and `SourceVaultID/TargetVaultID`. `IsCrossVault` is true when either path is set or both IDs are set. warrenRuntimeBridges (in internal/mcp) synthesizes these; R1's "use the workspace's own .marmot path as that bridge endpoint" means setting Source/TargetVaultPath to the local vault dir for the alias side — `seedBridgePathsLocked` (registry.go:102-114) would then map that path to the local vault_id, and `dirForLocked`'s bridge fallback would resolve to the live dir. But the routing-table-first priority (see above) still overrides it unless the warren-copy route is also suppressed.

### namespace.go:589-663 — CreateCrossVaultBridge: no self-bridge guard, auto rt.Set of both endpoints (R1, UX)

Refuses only missing vault_ids; it does NOT refuse `localCfg.VaultID == remoteCfg.VaultID` — bridging a vault to a copy of itself would write a manifest named `@X--@X.md` and `rt.Set(X, absLocal); rt.Set(X, absRemote)` where the second Set silently clobbers the first (lines 654-658). This is a second, unguarded path to the exact split-brain R1 fixes at mount time; R1's unconditional collision refusal should also land here. UX: the failure-mode warning text is good ("run 'marmot route add' manually", line 659) but the self-bridge case produces no warning at all.

```go
600		if localCfg.VaultID == "" {
601			return nil, fmt.Errorf("local vault at %s has no vault_id set; run 'marmot configure' first", localVaultDir)
602		}
```

```go
654		if err := routes.Update(func(rt *routes.RoutingTable) error {
655			rt.Set(localCfg.VaultID, absLocal)
656			rt.Set(remoteCfg.VaultID, absRemote)
657			return nil
658		}); err != nil {
```

### Tests pinning current behavior (need updating if registry gains local-ID logic)

All registry tests construct `NewVaultRegistry("local"|"vault-a", dir, ...)` with a local ID that never collides with the resolved vault IDs, so nothing currently pins self-resolution behavior — R1 needs NEW tests here rather than edits, unless the alias lands in this package, in which case these become the template:

- registry_test.go:10-246 (TestNewVaultRegistry*, Resolve*, Refresh*) — /Users/nurozen/Documents/GitHub/context-marmot/internal/namespace/registry_test.go
- registry_rebuild_test.go:28-316 (TestRebuildKeepsUnchangedVaults, TestRefreshSwapThenClose, TTL tests, read-lock tests) — note line 32 etc. pass localVaultID="local" with routes containing other IDs only.
- registry_routes_test.go:34-232 (route-vs-bridge priority tests: TestRoutesRoutePriorityOverBridge, TestRoutesFallbackToBridge, TestRoutesRoutePriorityVerifyDir) — these pin the routing-table-wins priority that makes the stale self-route shadow bridge paths; any priority change for the local ID must extend these.
- registry_embstore_test.go:52-191 — pins read-only open + no-mutation of remote (TestResolveEmbeddingStoreDoesNotMutateRemote); relevant if alias routes local through the registry.
- namespace_test.go:274-303 (TestParseQualifiedID), 548-573 (TestParseQualifiedID_CrossVault) — pin that any `@id/node` parses as cross-vault with no local special case; update if normalization lands in ParseQualifiedID.
- namespace_test.go:438-511 (TestCreateCrossVaultBridge*) — pin absence of a self-bridge guard; add a refusal case.

### UX PASS

No user-facing surface in this package (no subcommands/flags/help text/endpoints). Error strings that surface to users and are decent: "unknown vault %q: not in routing table or bridge manifests" (registry.go:208), the "run 'marmot configure' first" hints (namespace.go:601,604), and the routes warning (namespace.go:659). One friction item: `ResolveEmbeddingStore`'s "unknown vault %q" (registry.go:314) lacks the "not in routing table or bridge manifests" hint its ResolveGraph twin has — inconsistent error quality for the same condition.

### Context-staleness check

Nothing in the CONTEXT is contradicted by this package. Confirms: import-preserving vault_id + rt.Set(warrenCopy) will indeed shadow bridge paths because dirForLocked is routes-first; localVaultID is plumbed into the registry but unused — the alias hook point exists and is dead code today.


## internal-routes

# internal/routes findings

## internal/routes

Package is the generic vault_id -> path routing table (`~/.marmot/routes.yml`, overridable via `MARMOT_ROUTES` or `SetOverridePath`). It contains NO warren-, LocalVaultID-, bridge-, or collision-specific logic — no hits for `LocalVaultID`, `ReloadWarrenState`, `warrenRuntimeBridges`, `refuseVaultIDCollision`, `vaultIDClaims`, or `DoctorWorkspace`. It is purely the mechanism R1's `rt.Set(vaultID, warrenCopyPath)` call (in internal/mcp/warren_reload.go) writes through. R1's "skip rt.Set for self-mount alias" change lives in the CALLER; this package needs no code change unless we want an alias concept in the table itself.

### routes.go:236-243 (R1 — the Set primitive that self-mount currently abuses)

`Set` is last-write-wins with no collision or identity awareness — a warren mount silently overwrites the live local vault's route for the same vault_id:

```go
236	// Set registers or updates a vault path.
237	func (rt *RoutingTable) Set(vaultID, path string) {
238		rt.mu.Lock()
239		defer rt.mu.Unlock()
240		if rt.Vaults == nil {
241			rt.Vaults = make(map[string]VaultEntry)
242		}
243		rt.Vaults[vaultID] = VaultEntry{Path: path}
244	}
```

R1 design note: because Set is unconditional-overwrite, whether the stale-snapshot route "wins" depends only on caller ordering. If R2 wants a first-class local-identity marker, `VaultEntry` (routes.go:22-24, single `Path string` yaml field) would need a new field (e.g. `Local bool` or `Kind`), which is a schema change to routes.yml — old entries unmarshal fine (additive), so migration is trivial.

### routes.go:219-233 (R1 — Get, bridge/@ref resolution endpoint)

`Get(vaultID)` returns the single stored path; there is no fallthrough or precedence, so once ReloadWarrenState sets the warren-copy path, every `@local-id` resolution in-process gets the stale snapshot:

```go
219	// Get returns the filesystem path for a vault ID, or ("", false) if not found.
220	func (rt *RoutingTable) Get(vaultID string) (string, bool) {
```

### routes.go:185-216 (context — Update is the flock-protected persistent RMW)

`Update()` (mu + `flock.WithLock(path+".lock")`) is the only cross-process-safe write path to routes.yml. Note asymmetry: in-memory `RoutingTable.Set` used by ReloadWarrenState mutates a loaded table without persisting; if R1 changes which entries get Set, no on-disk migration is needed for the in-memory case, but any routes.yml entries persisted by register commands would need the alias-skip too.

### routes.go:54-71 (UX — MARMOT_ROUTES env surface)

User-facing env knob, verbatim doc: `MARMOT_ROUTES=off|none|0 disables the global routing table entirely`; any other non-empty value is the routes file path. Error text when disabled (routes.go:129, 191): `"empty routing table path (routing disabled via MARMOT_ROUTES?)"` — reasonable, but this env var appears in no CLI help; onboarding docs should mention it.

### Tests (routes_test.go, routes_lock_test.go, routes_stress_test.go)

All tests are generic table mechanics (round-trip, env override, Remove/List, atomic write, cross-process Update, corrupt YAML, large tables, concurrency). NONE pin self-mount/alias/warren behavior — no test updates needed here for R1/R2 unless VaultEntry gains fields (then routes_test.go:20 TestRoundTrip should cover the new field).

### Out-of-date context check

Nothing in the task CONTEXT is contradicted by this folder; the cited `rt.Set` behavior in warren_reload.go is consistent with this package's last-write-wins semantics.


## internal-warren

# internal/warren scanner findings

## internal/warren

Package scope note: this package has NO knowledge of `LocalVaultID`, `ReloadWarrenState`, `warrenRuntimeBridges`, or the routing table — those live in internal/mcp (warren_reload.go). Here, "the local vault" is only ever detected via `sourceVaultID(workspaceMarmotDir)` reading `_config.md`'s `vault_id`. R1's "alias" concept must therefore be expressed here either as (a) a mount-time flag/refusal change in `Mount`/`SetEditable`/`refuseVaultIDCollision`, plus (b) a way for consumers (`ActiveMounts`) to signal "this status IS the local vault" — e.g. a new field on `ProjectStatus` — or handled entirely in internal/mcp by comparing `status.VaultID == LocalVaultID`.

### warren.go:1412-1428 — refuseVaultIDCollision (R1 core site; context cited 1418-1429, now 1412-1428)
The local-vault case warns-and-allows; the cross-warren case refuses. R1 wants the mount to succeed as an alias (no rt.Set downstream) and possibly a changed/removed warning; the "unconditional C7 refusal for true conflicts" is already the behavior for warren-vs-warren — only the local-vault branch changes semantics.
```go
1418	func refuseVaultIDCollision(claimed map[string]vaultClaim, vaultID, warrenID, projectID string) error {
1419		claim, taken := claimed[vaultID]
1420		if !taken || (claim.WarrenID == warrenID && claim.ProjectID == projectID) {
1421			return nil
1422		}
1423		if claim.WarrenID == "" {
1424			fmt.Fprintf(warnWriter, "warning: vault ID %q of project %s/%s matches the local workspace vault; cross-vault queries for %q will resolve to the mounted copy\n", vaultID, warrenID, projectID, vaultID)
1425			return nil
1426		}
1427		return fmt.Errorf("vault ID %q of project %s/%s collides with %s/%s already mounted in this workspace", vaultID, warrenID, projectID, claim.WarrenID, claim.ProjectID)
1428	}
```
Doc comment (1412-1417) explicitly says mounting the warren copy of *this* project "is the documented way to activate warren bridges for local writes, so it must stay allowed" — that rationale becomes obsolete under R1/R2 and should be rewritten.

### warren.go:1350-1399 — vaultIDClaims / claimedVaultIDs (R1)
`vaultIDClaims` seeds the local vault claim first (line 1352-1354: `claims[local] = append(..., vaultClaim{ProjectID: "the local workspace vault"})`, WarrenID==""), then warrens sorted by ID; `claimedVaultIDs` (1393-1399) reduces to `owners[0]`, so the local vault always has precedence when present. Both mount-time refusal and DoctorWorkspace share this builder — an R1 alias exemption must be encoded here or in `refuseVaultIDCollision`, and DoctorWorkspace's collision report must learn to treat a self-alias mount as non-colliding (or the alias must not appear as a claim).

### warren.go:809-846 — DoctorWorkspace (R1: hard-error vs mount-warn inconsistency)
Any vault ID with >=2 claimants — including exactly the local-vault + self-mount pair that Mount permits with only a warning — is severity "error", code `vault_id_collision_workspace`:
```go
839		report.Issues = append(report.Issues, DoctorIssue{
840			Severity: "error",
841			Code:     "vault_id_collision_workspace",
842			Message:  fmt.Sprintf("vault ID %q is claimed by %s; queries resolve to one of them arbitrarily — unmount or re-import with distinct vault IDs", vaultID, strings.Join(names, " and ")),
843		})
```
This is the doctor/mount inconsistency R1 must resolve. Under R1, a self-alias mount should either be excluded from claims or reported as info/ok; a true cross-warren duplicate stays error.

### warren.go:1059-1106 — Mount (R1)
Mount is where the alias decision is made per project: `vaultID := mountVaultID(...)` (1093) then `refuseVaultIDCollision` (1094). No concept of "alias" exists — a self-mount lands in `entry.ActiveProjects` identically to any other mount, and downstream `ActiveMounts` will emit it with `Path` = warren copy path, which internal/mcp then rt.Set's. Also note the materialized+editable refusal text at 1088 (verbatim error strings useful for UX pass).

### warren.go:1108-1170 — SetEditable (R1: editable gating for self-mounts)
Editable gating today: author-side `project.ReadOnly` (1137-1139), materialized burrow cache (1146-1151), and the auto-mount collision check (1154-1160). There is NO refusal of editable on a self-mount (vault_id == local vault) — R1's "refuse editable on self-mounts" is a new check to add here (and in `WriteEditableNode` as a backstop, warren.go:1469-1509, which currently only checks `mount.Editable` and manifest ReadOnly).

### warren.go:1404-1410 — mountVaultID + preferredProjectPath (R1: endpoint path selection)
```go
1404	func mountVaultID(workspaceMarmotDir, warrenID string, entry WorkspaceWarren, project Project) string {
1405		path := preferredProjectPath(workspaceMarmotDir, warrenID, entry, project)
1406		if meta, _, err := LoadProjectMetadata(path); err == nil && meta != nil && meta.VaultID != "" {
1407			return meta.VaultID
1408		}
1409		return project.ProjectID
1410	}
```
`preferredProjectPath` (1717-1737): editable → checkout; materialized+cache-exists → burrow cache; else checkout. R1's "use the workspace's own .marmot path as the bridge endpoint" is NOT expressible here — this function only knows warren paths; the substitution has to happen in internal/mcp's warrenRuntimeBridges, keyed on VaultID == LocalVaultID.

### warren.go:1579-1635 — ActiveMounts (R1: what feeds ReloadWarrenState)
Builds `ProjectStatus{VaultID, Path: marmotPath, Editable, ...}` per active project — this is exactly what internal/mcp's ReloadWarrenState iterates to call rt.Set(vaultID, path). VaultID resolution: metadata vault_id, fallback projectID (1608-1611). R1's skip-rt.Set-for-self needs either a marker here (e.g. `ProjectStatus.SelfAlias bool` computed against the local `_config.md`) or the comparison done by the caller. `ActiveMounts` takes only `marmotDir`, so it already has what it needs to call `sourceVaultID(marmotDir)` itself.

### warren.go:326-335, 361-368 — ImportProject vault_id preservation (R1 premise confirmed)
Import preserves the source's vault_id (`sourceVaultID(source)`, falling back to project ID), writing it into the copy's `.marmot/_warren.md` `ProjectMetadata.VaultID`. This is why self-mounts collide by construction. `ImportOptions.VaultID` (146-150) already allows an explicit override — relevant to R2 migration ("re-import with distinct vault IDs" is doctor's current advice).

### warren.go:78-89 — WorkspaceState schema (R2)
```go
79	type WorkspaceState struct {
80		Warrens map[string]WorkspaceWarren `yaml:"warrens,omitempty"`
81	}
84	type WorkspaceWarren struct {
85		Path             string   `yaml:"path" json:"path"`
86		ActiveProjects   []string `yaml:"active_projects,omitempty" ...`
87		EditableProjects []string `yaml:"editable_projects,omitempty" ...`
88		Materialized     bool     `yaml:"materialized,omitempty" ...`
89	}
```
No version field and struct-based YAML parsing: unknown fields written by a newer binary are silently dropped on the next Load→Save by an older binary (unlike the manifest, which has `Version` + `checkManifestWritable` ceiling at warren.go:37, 1805-1810). R2's "identified projects" field (e.g. `IdentifiedProjects []string` or a per-warren `LocalProject string`) needs to consider this: there is no workspace-state version guard today. Also relevant: `normalizeWorkspaceState`/`validateWorkspaceState` (1963-2027) must learn any new field.

### warren.go:91-107 — ProjectStatus (R1/R2 display + API surface)
Fields: WarrenID, WarrenPath, ProjectID, Path, VaultID, Registered, Active, Editable, Materialized, Available. No local/alias marker. This struct is serialized to JSON by the HTTP API and consumed by MCP and CLI status output, so adding e.g. `Local bool`/`SelfAlias bool` propagates everywhere.

### warren.go:428-472 — RenameProject (UX: renames ID but not directory — confirmed)
Rewrites manifest project ID, bridges, and metadata via `ensureProjectMetadata`, but `renamed.Path` keeps the old `projects/<oldID>/.marmot` path — no `os.Rename` of the directory anywhere. Also note `ensureProjectMetadata` (2628-2641) sets `meta.ProjectID = newID` but leaves `meta.VaultID` at its old value (defaulted from old project ID at import), so a rename silently diverges project_id from vault_id.

### warren.go:2335-2348 — sourceVaultID (R1/R2: the only local-identity probe)
```go
2335	func sourceVaultID(marmotDir string) string {
2336		data, err := os.ReadFile(filepath.Join(marmotDir, "_config.md"))
...
2344		if value, ok := out["vault_id"].(string); ok {
2345			return strings.TrimSpace(value)
```
Unexported. R1/R2 likely need this exported (or a new exported `LocalVaultID(marmotDir)`) so mcp/CLI can compare mount VaultIDs against the live vault without duplicating the parse.

### warren.go:572-728 + 809-846 — Doctor / DoctorWorkspace codes (UX inventory)
Manifest doctor codes: lockfile_not_ignored (info), materialized_cache_in_warren (warning), absolute_project_path (warning), project_missing (error), project_not_directory (error), metadata_unreadable (warning), project_id_mismatch (error), warren_id_mismatch (error), duplicate_vault_id (error), embeddings_missing (warning), schema_stale (warning), model_skew (warning), bridge_source_missing/bridge_target_missing (error). Workspace doctor: only vault_id_collision_workspace (error). Note `Doctor`'s `duplicate_vault_id` (667-677) is the intra-warren analogue; R2's identified-local project does not affect it.

### warren.go — verbatim error strings for UX pass
- Mount unknown warren: `warren %q is not registered in this workspace` (1065, repeated in SetEditable/Unmount/DropMaterialized/Unregister/Status).
- Mount editable+materialize: line 1088 — long remediation string embedding two full CLI invocations (`'marmot warren edit %s --warren %s --off'`).
- SetEditable readonly: `warren author marked project %q read-only; edits must go through the warren repository itself` (1138).
- SetEditable burrowed: line 1149 — embeds `'marmot warren burrow --drop --warren %s %s'`.
- Unregister refusals (1317-1322): embed `'marmot warren unmount --warren %s --all'` / `'marmot warren burrow --drop --warren %s --all'`, "or pass --force".
- Collision refusal (1427): names both claimants but gives no remediation, unlike doctor's message (842) which suggests "unmount or re-import with distinct vault IDs".
Friction evidence: error strings hardcode CLI flag spellings; any UX-pass flag renaming must sweep these.

### Tests pinning current behavior (will need updating for R1)
- warren_inverse_test.go:433-458 `TestMountLocalVaultCollisionWarnsOnly` — pins warn-not-refuse for local-vault collision and the exact warning text "matches the local workspace vault". Under R1 this becomes the alias path; test must change to assert alias semantics (mount succeeds, marked as alias / no warren-copy claim).
- warren_inverse_test.go:371-431 `TestMountRefusesVaultIDCollision` — pins cross-warren refusal text "collides with warren-a/project-a" and SetEditable auto-mount refusal; stays valid but may need alias-case additions.
- warren_d_test.go:449-506 `TestDoctorWorkspaceVaultIDCollision` — pins that ANY two claimants (currently two warrens) is a hard error; needs a new case asserting a self-alias mount is NOT an error once R1 lands.
- warren_d_test.go:133 `TestSetEditableRefusesReadOnly`, warren_inverse_test.go:460 `TestWriteEditableNode` — editable-gating tests; R1 adds a new refusal (editable on self-mount) needing a new test.
- warren_extra_test.go:397 `TestRenameProjectErrors` + warren_test.go:94 `TestAuthoringInitAddRenameRemoveProjectPreservesBody` — pin RenameProject's current no-directory-rename behavior (warren_test.go:139 renames api→api-service and the fixture path stays put).

### provenance.go:1-67 — BurrowProvenance (context check: matches CONTEXT, no drift)
SourceCommit/SourcePath/MaterializedAt/ManifestVersion; missing/unreadable = stale. Relevant to R2 only insofar as an identified-local project should presumably never be burrowed/refreshed — refresh/burrow interactions with local identity are undefined here today.

### Out-of-date notes on the CONTEXT
- refuseVaultIDCollision is at warren.go:1412-1428 (comment starts 1411), not 1418-1429; DoctorWorkspace at 809-846 (cited 814-845, close enough).
- CONTEXT says "an editable self-copy could split-brain writes" — confirmed possible: SetEditable has no self-mount check; WriteEditableNode (1469) would write into the warren checkout copy while local writes go to the live vault.
- Everything else in CONTEXT about this package checks out; ReloadWarrenState/warrenRuntimeBridges/LocalVaultID are not in this folder (internal/mcp).


## web

# web

## web

Scope: `/web` is the browser UI (vanilla TS + d3, built by vite, embedded via `embed.go`). It contains NO warren *management* — the CONTEXT claim "web UI has zero warren management (mount/unmount/edit/refresh are CLI-only; UI only lists/views warren graphs)" is **mostly accurate but slightly out of date**: the UI additionally (a) triggers a server-side warren state reload via `POST /api/warren/{id}/refresh` on the Refresh button, and (b) shows per-node warren provenance and enforces read-only editing for non-editable warren nodes. Still no mount/unmount/register/burrow/propose/doctor surface.

### src/api.ts:58-75 (UX — full warren API surface the UI consumes)
The UI touches exactly four warren-adjacent endpoints: `/api/bridges`, `/api/warrens`, `/api/warren/{id}/graph` (GET), and `/api/warren/{id}/refresh` (POST, in main.ts). No endpoints exist client-side for mount/unmount/register/unregister/burrow/propose/doctor/import — a UX-pass gap if web management is desired.
```ts
58  export async function fetchBridges(): Promise<BridgeInfo[]> {
59    const res = await fetch('/api/bridges');
...
65  export async function fetchWarrens(): Promise<WarrensResponse> {
66    const res = await fetch('/api/warrens');
...
71  export async function fetchWarrenGraph(warrenId: string): Promise<GraphResponse> {
72    const res = await fetch(`/api/warren/${encodeURIComponent(warrenId)}/graph`);
```
Note: `fetchBridges()` exists but I found no caller in `src/` — dead or latent API surface (UX candidate: a bridges list view).

### src/main.ts:167-183 (UX — warren display in namespace selector)
Warrens are appended to the namespace `<select>` behind a disabled "Warrens" divider option, labeled `Warren <id> (<n> active)`. Under R1/R2, an aliased/identified local project inside a warren view has no distinct display treatment here — the count comes from `active_projects` only.
```ts
177    for (const [warrenId, warren] of warrenEntries) {
178      const opt = document.createElement('option');
179      opt.value = `_warren/${warrenId}`;
180      const count = warren.active_projects?.length ?? 0;
181      opt.textContent = `Warren ${warrenId} (${count} active)`;
```

### src/main.ts:199-210 (R1 — UI path that exercises ReloadWarrenState)
The Refresh button, when a `_warren/` namespace is selected, fires `POST /api/warren/{id}/refresh` before re-fetching the graph. This is a client of `Engine.ReloadWarrenState`, so R1's "skip rt.Set for vault_id == LocalVaultID" change is directly observable here: after R1, refreshing a warren view whose bridge involves the local project should resolve to the LIVE local vault rather than the warren snapshot. Any web e2e for R1 should go through this button.
```ts
201      if (currentNamespace.startsWith('_warren/')) {
202        const warrenId = currentNamespace.slice('_warren/'.length);
203        try {
204          await fetch(`/api/warren/${encodeURIComponent(warrenId)}/refresh`, { method: 'POST' });
```

### src/types.ts:42-49 (R1/R2 — provenance schema the UI expects)
`Provenance.source` is typed `'local' | 'warren_mount' | 'warren_materialized' | string`. R1's self-mount aliasing and R2's identified-local endpoint should decide whether aliased nodes report `source: 'local'` (UI already handles it gracefully — open string union) or a new value; either way the UI renders whatever comes through, but the `editable` flag drives real behavior (see detail-panel below).
```ts
42  export interface Provenance {
43    source?: 'local' | 'warren_mount' | 'warren_materialized' | string;
44    warren_id?: string;
45    project_id?: string;
46    vault_id?: string;
47    marmot_dir?: string;
48    qualified_id?: string;
49    editable?: boolean;
```

### src/types.ts:66-82 (R2 — WorkspaceWarren / BridgeInfo shapes)
`WorkspaceWarren` carries `active_projects` / `editable_projects` / `materialized_projects`. R2's identified-local project has no representation in this response shape; if `/api/warrens` grows an `identified_project` (or R1 grows an alias marker), this interface and the selector label (main.ts:180-181) need updating. `BridgeInfo.is_cross_vault` exists but is unused (no fetchBridges caller).
```ts
66  export interface WorkspaceWarren {
67    path: string;
68    active_projects?: string[];
69    editable_projects?: string[];
70    materialized_projects?: string[];
71  }
...
77  export interface BridgeInfo {
78    source: string;
79    target: string;
80    allowed_relations: string[];
81    is_cross_vault: boolean;
82  }
```

### src/detail-panel.ts:109-124 and 221-253 (R1 — editable gating in the UI)
Provenance is rendered verbatim per node, and any node with `provenance` present and `editable !== true` is hard read-only: save button disabled/relabeled and input handlers early-return. R1's "refuse editable on self-mounts" interacts here: today an editable self-mount copy would render as editable in the UI and writes would go to the warren snapshot (the split-brain risk). After R1, aliased local nodes presumably arrive as plain local nodes (no provenance) and stay editable against the live vault — worth pinning in a web e2e.
```ts
221    const isReadOnlyWarrenNode = Boolean(node.provenance && !node.provenance.editable);
...
238    if (isReadOnlyWarrenNode) {
239      saveBtn.textContent = 'Read-only Warren Node';
240    }
...
252      if (isReadOnlyWarrenNode) return;
```

### src/graph-view.ts:230-240, 553-570 (UX — bridge rendering only)
Bridge edges (`class === 'bridge'`) are drawn as separate Bezier arcs (`.bridge-arc`) with participating nodes marked `.bridge-node`; index.html:47 has an edge-class radio filter including "Bridge". Purely presentational; no vault-ID logic. Relevant to R1 only in that bridge endpoints resolved to a stale warren copy vs the live vault will change which node IDs appear in these arcs.

### e2e/ (tests that pin behavior)
`e2e/ui.spec.ts` and `e2e/regressions.spec.ts` contain ZERO warren coverage — no test touches `/api/warrens`, `_warren/` namespaces, provenance display, read-only gating, or the warren refresh POST. `e2e/serve.sh` runs a hermetic single-vault fixture (`cp -R "$ROOT/e2e/fixture/vault" "$WORK/.marmot"`; scrubs global config) with no warren fixture at all. No web tests need *updating* for R1/R2, but there is nothing pinning current self-mount UI behavior either; the "first warren e2e suite" mentioned in CONTEXT is not in `/web/e2e` (it lives elsewhere in the repo). R1/R2 plan should add a warren fixture + specs here for: warren option in selector, refresh POST, provenance panel, read-only save gating, and alias-vs-snapshot node content.

### UX friction evidence (web)
- No warren management verbs in UI (mount/unmount/edit/refresh-pull/propose/doctor) — only view + state-reload.
- `fetchBridges` / `BridgeInfo` implemented but never used: bridges are invisible except as arcs inside a warren graph.
- Warren picker is crammed into the namespace `<select>` with a disabled-option divider (main.ts:171-176) — no dedicated warren panel, no per-project (active/editable/materialized) breakdown despite the API returning it.
- Read-only warren node message is a bare relabeled button ("Read-only Warren Node") with no explanation of why or how to make it editable (`marmot warren edit ...`).
- Warren refresh failure is silently swallowed (main.ts:205-207 "Best-effort") — no error surfaced to the user.

