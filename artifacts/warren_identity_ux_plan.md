# Warren Identity & UX Plan

Branch `multiprocess-lock-fix`, commit `41db2da`. Source inventory:
`artifacts/warren_identity_findings.md` (per-folder ground truth with verified line refs); style
and rigor follow `artifacts/warren_resolution_plan.md`. All line numbers below were re-verified
against the working tree where load-bearing.

**Context.** The full warren resolution (workstreams A–D) landed: read-only remote stores,
checkpointed copies, `internal/flock` locking, `Engine.ReloadWarrenState` + `VaultRegistry.Rebuild`
freshness, inverse verbs, `refresh --pull`, provenance, propose, manifest read-only policy, doctor
additions, and the first warren e2e suite. One deliberate deviation survived it: activating a
manifest bridge that involves the *local* project requires mounting the warren's copy of that
project, whose `vault_id` equals the local vault's (import preserves `vault_id`,
`internal/warren/warren.go:326-368`). `refuseVaultIDCollision`
(`internal/warren/warren.go:1412-1428`) warns instead of refusing for this local case, but
`ReloadWarrenState` still runs `rt.Set(vaultID, warrenCopyPath)`
(`internal/mcp/warren_reload.go:49`) — so bridge traversal and `@local-id` references resolve to
the STALE warren snapshot instead of the live vault, an editable self-copy can split-brain writes,
and `DoctorWorkspace` reports the exact state mount permits as a hard error
(`vault_id_collision_workspace`, `warren.go:814-846`). This plan fixes that (R1), designs the
mount-free successor (R2), and scopes the next warren UX pass.

## Document map

- **[Workstream R1: self-mount aliasing](#workstream-r1-self-mount-aliasing)** — fully specified;
  items R1.1–R1.9 plus consolidated test list and single-PR packaging note.
- **[Workstream R2: first-class local identity](#workstream-r2-first-class-local-identity)** —
  designed (R2.0–R2.10): derived identity (vault_id auto-detection, no new workspace state),
  bridge activation without self-mounts, verb/interaction semantics, R1-state migration, **ship
  recommendation** with explicit defer-if criteria.
- **[Workstream U: next warren UX pass](#workstream-u-next-warren-ux-pass)** — seven items
  (U1–U7) ranked by user impact from the verified CLI/API/web surface inventory, each with
  verbatim before/after and an R1/R2 dependency marker.
- **[Testing & Rollout](#testing--rollout)** — consolidated test matrix across R1/R2/U, e2e
  additions (Go + net-new web e2e warren fixture), nine-PR sequencing under the
  auto-release-on-main constraint, rollback story.
- **[Risks & Mitigations](#risks--mitigations)** — cross-cutting and U-workstream risks
  (R1's live in its packaging note; R2's are its defer-if criteria), plus the terminology
  glossary: *self-alias* (R1, mounted), *identified project* (R2, no mount), *identity* (the
  derived relationship, never stored), *legacy self-mount* (pre-R1 state).

---

# Workstream R1: self-mount aliasing

## R1.0 — Decided semantics: the alias contract

A workspace mount whose resolved `vault_id` equals the live local vault's `vault_id` is a
**self-alias**: a declaration that "this warren project *is* this workspace," kept only so the
warren's manifest bridges involving the local project can activate. Six invariants define it:

1. **No route claim.** A self-alias never calls `rt.Set` and never appears in the vault
   registry's resolution inputs; the live local vault is the sole answerer for its `vault_id`.
2. **Live bridge endpoint.** `warrenRuntimeBridges` uses the workspace's own `.marmot` dir as the
   alias side's `SourceVaultPath`/`TargetVaultPath`; `SourceVaultID`/`TargetVaultID` keep the
   (local) vault ID so cross-vault edge validation still matches the bridge.
3. **Never editable, never materialized.** `warren edit`, `--materialize` mounts, `Materialize`,
   and both `@`-write paths (MCP + HTTP API) refuse self-aliases; `--off`/`--drop` stay allowed
   as the migration escape hatch.
4. **`@<LocalVaultID>/node` resolves to the live vault** (in-memory graph, zero staleness), never
   through the registry (no second read-only copy, no TTL lag, no shared read lock on our own
   vault that would trip the B4 `index --force` refusal against ourselves).
5. **Unconditional collision refusal.** `refuseVaultIDCollision` loses its warn-and-allow branch;
   every true conflict (two claimants that are not the local vault + its aliases) refuses.
6. **Doctor agrees with mount.** A self-alias is not a duplicate claim; doctor reports it as info,
   errors only on legacy *editable* self-mounts and true collisions, warns on legacy self burrow
   caches.

Blast-radius note: workspaces without a `vault_id` in `_config.md` (the common default; the e2e
fixture vault has none) never engage any of this — every guard requires a non-empty local ID on
both sides, so pre-R1 behavior is bit-identical for them.

## R1.1 — The alias predicate

**Local vault ID sources (two, deliberately):**

- **Warren layer:** `sourceVaultID(marmotDir)` (`internal/warren/warren.go:2335-2348`) reads
  `_config.md`'s `vault_id` (trimmed; `""` on missing file, unparseable frontmatter, or absent
  key). Export it as `warren.LocalVaultID(marmotDir string) string` (thin exported wrapper; keep
  the unexported name as the implementation) so cmd/ and tests share the parse instead of
  duplicating it.
- **Engine layer:** `Engine.LocalVaultID` (`internal/mcp/engine.go:44`), cached once in
  `WithVaultRegistry` (`engine.go:328-337`). Today a failed `config.Load` is silently swallowed,
  leaving `LocalVaultID == ""` and silently disabling every alias guard. Harden: log
  `fmt.Fprintf(os.Stderr, "warning: local vault config unreadable (%v); vault_id unknown — self-mount aliasing and cross-vault edge validation disabled\n", err)`
  in the error branch. (`validateCrossVaultEdges` already no-ops on empty ID, `engine.go:288` —
  same degradation, now visible.)

**The predicate**, everywhere: `selfAlias := local != "" && vaultID == local`. The empty-string
edge case is load-bearing in both directions: (a) an empty local ID must alias nothing;
(b) `mount.VaultID` is never `""` after `ActiveMounts`' project-ID fallback
(`warren.go:1608-1611`), and the reload loop independently skips `""` (`warren_reload.go:43`).

**Carry it on `ProjectStatus`.** Add one additive field so every consumer (reload, API JSON, CLI
status, web UI) gets the derivation once instead of re-probing `_config.md`:

```go
// internal/warren/warren.go, ProjectStatus (currently WarrenID..Available):
SelfAlias bool `json:"self_alias,omitempty"` // vault_id matches the live local vault; served as an alias, never routed/editable/materialized
```

Compute it in all three status builders, which each already have the workspace `.marmot` dir:
`ActiveMounts` (`warren.go:1612-1625`), `Status` (`warren.go:1512-1575`, both the healthy rows and
skip the degraded/unreadable rows — their `vaultID` is `""`), and `materializedStatuses`
(`warren.go:1687`). In `ActiveMounts` also force the editable flag off for aliases (see R1.2):

```go
local := sourceVaultID(marmotDir) // hoist above the warren loop, once
...
selfAlias := local != "" && vaultID == local
mounts = append(mounts, ProjectStatus{
    ...
    Editable:  containsName(entry.EditableProjects, projectID) && !project.ReadOnly && !selfAlias,
    SelfAlias: selfAlias,
    ...
})
```

Skew note (document in the field comment, accept for R1): `SelfAlias` is derived fresh from
`_config.md` per call; `Engine.LocalVaultID` is cached at `WithVaultRegistry` time. Editing
`vault_id` under a live daemon desynchronizes them until restart — same restart requirement every
other cached config field already has. Do NOT make `Engine.LocalVaultID` mutable in R1 (it is read
lock-free from handler goroutines).

**Tests (R1.1):**
- `internal/warren`: `TestLocalVaultID` — present / absent / unreadable `_config.md` → id / `""` /
  `""`. `TestActiveMountsMarksSelfAlias` — fixture via `registerAndMount`
  (`internal/warren/warren_inverse_test.go:18`) plus a workspace
  `_config.md` carrying one project's vault ID — the exact pattern
  `TestMountLocalVaultCollisionWarnsOnly` (:433-455) already uses. (The `testWarrenRoot` helper
  that sets `VaultID: projectID` is cmd/marmot's fixture, `cmd/marmot/warren_test.go:476-538` —
  used by the R1.3 CLI tests, not here.) Assert that mount's `SelfAlias=true`,
  `Editable=false` even when listed in `EditableProjects`, and all others `SelfAlias=false`.
- `internal/mcp/coverage_test.go:75` `TestWithVaultRegistryCachesLocalVaultID` — extend with an
  unreadable-config case asserting the new stderr warning fires and `LocalVaultID` stays `""`.

## R1.2 — Mount / SetEditable / Materialize: alias-aware gating, unconditional refusal

**`Mount` (`internal/warren/warren.go:1060-1106`).** Hoist `local := sourceVaultID(wsMarmotDir)`
before the project loop. After `vaultID := mountVaultID(...)` (:1093), branch before the collision
check:

```go
if local != "" && vaultID == local {
    // Self-alias: this project IS the workspace vault. It activates the
    // warren's bridges against the live vault; it claims no route and can
    // never be editable or materialized (a cache/copy would be a stale or
    // split-brained shadow).
    if materialized {
        return fmt.Errorf("project %q in warren %q has this workspace's own vault ID %q; a self-alias serves from the live vault and cannot be materialized — mount without --materialize", projectID, warrenID, vaultID)
    }
    if containsName(entry.EditableProjects, projectID) {
        return fmt.Errorf("project %q in warren %q is marked editable but has this workspace's own vault ID %q; edit it directly in this workspace — run 'marmot warren edit %s --warren %s --off' first", projectID, warrenID, vaultID, projectID, warrenID)
    }
    fmt.Fprintf(warnWriter, "note: project %s/%s shares this workspace's vault ID %q; mounting as an alias of the live local vault (bridges activate; queries and writes stay local)\n", warrenID, projectID, vaultID)
    entry.ActiveProjects = addName(entry.ActiveProjects, projectID)
    continue // no vault-ID claim: aliases don't own a route
}
if err := refuseVaultIDCollision(claimed, vaultID, warrenID, projectID); err != nil {
    return err
}
```

Two projects with the local vault ID mounted in one call (or from two warrens) both alias — that
is coherent, not a conflict: the ID *is* the local vault, and each mount is just a bridge
activation handle.

**`refuseVaultIDCollision` (`warren.go:1412-1428`) becomes unconditional.** Delete the
`claim.WarrenID == ""` warn branch (:1423-1425) and rewrite the doc comment (:1412-1417 — its
"documented way to activate warren bridges … must stay allowed" rationale is what R1 obsoletes).
The local-claim case is unreachable from `Mount`/`SetEditable` after the short-circuits above, but
keep it *refusing* (not panicking, not warning) as defense for future callers, with sane
formatting since `claim.WarrenID == ""` would otherwise print `/the local workspace vault`:

```go
func refuseVaultIDCollision(claimed map[string]vaultClaim, vaultID, warrenID, projectID string) error {
    claim, taken := claimed[vaultID]
    if !taken || (claim.WarrenID == warrenID && claim.ProjectID == projectID) {
        return nil
    }
    owner := claim.WarrenID + "/" + claim.ProjectID
    if claim.WarrenID == "" {
        owner = claim.ProjectID // "the local workspace vault"
    }
    return fmt.Errorf("vault ID %q of project %s/%s collides with %s already mounted in this workspace", vaultID, warrenID, projectID, owner)
}
```

`vaultIDClaims` (`warren.go:1350-1388`) keeps seeding the local claim first (:1352-1354) —
doctor still needs it (R1.6), and the seeded claim now backs a refusal rather than a warning if a
future caller forgets the alias branch.

**`SetEditable` (`warren.go:1109-1170`).** After the manifest lookup (and independent of the
author-side `ReadOnly` check at :1137-1139), compute the alias predicate once via
`mountVaultID(wsMarmotDir, warrenID, entry, project)` vs `sourceVaultID(wsMarmotDir)`:

```go
if selfAlias && editable {
    return fmt.Errorf("project %q in warren %q has this workspace's own vault ID %q; it is served as an alias of the live vault — edit nodes directly in this workspace (no @ prefix) instead of enabling warren edit", projectID, warrenID, vaultID)
}
```

`--off` stays allowed unconditionally (the legacy-state escape hatch, R1.7). In the auto-mount
block (:1154-1160), mirror `Mount`: when `selfAlias`, skip `refuseVaultIDCollision` (an `--off` on
an unmounted self project just becomes an alias mount).

**`Materialize` (`warren.go:1650`) backstop.** `Mount(materialized=true)` already refuses above,
but the CLI calls `warren.Materialize` per project after `Mount` (`cmd/marmot/warren.go:924-964`)
and `refresh --pull` re-materializes caches (`cmd/marmot/warren.go:1263-1388`). Add a guard at the
top of `Materialize`: load the project metadata `vault_id` (same
`LoadProjectMetadata`/project-ID-fallback resolution as `mountVaultID`), and if it equals
`sourceVaultID(workspaceMarmotDir)` return
`fmt.Errorf("refusing to materialize project %q: vault ID %q is this workspace's own vault; a burrow cache would be a stale shadow of the live vault", project.ProjectID, vaultID)`.
The `refresh --pull` re-materialize loop (`cmd/marmot/warren.go:1365-1383`) needs a matching
**loop-side skip**, not just the refusal: the loop iterates `entry.ActiveProjects` with caches and
`return`s 1 on any `Materialize` error, so a *legacy* self cache (pre-R1 state: self-mount +
burrow cache) plus a moved HEAD would make the whole `refresh --pull` hard-fail through the new
refusal. Skip cached self projects with
`fmt.Fprintf(os.Stderr, "warren refresh: warning: burrow cache for %q shadows this workspace's own vault; skipping re-materialize — drop it with 'marmot warren burrow --drop --warren %s %s'\n", ...)`
(predicate: checkout metadata vault_id == `warren.LocalVaultID(marmotDir)`, same probe as
`mountVaultID`). Fresh self caches never exist post-R1 (Mount and Materialize both refuse); the
skip exists purely for legacy state, and doctor (R1.6) owns the durable diagnostic.

**`WriteEditableNode` (`warren.go:1469-1509`)** needs no signature change: it takes only
`ProjectStatus`, and R1.1 forces `Editable=false` on alias statuses, so its existing
`mount.Editable` check refuses. The caller-side guards in R1.4/R1.5 exist to give a *better
message* than "not editable" and to defend against hand-built `ProjectStatus` values.

**Tests (R1.2):**
- Rewrite `internal/warren/warren_inverse_test.go:430-458` `TestMountLocalVaultCollisionWarnsOnly`
  — **this is the one deliberate-deviation test the fix agents documented**; it currently pins the
  warn text `"matches the local workspace vault"` (:452). Becomes `TestMountSelfAliasesLiveVault`:
  mount succeeds, warnWriter contains `"mounting as an alias of the live local vault"`,
  `ActiveMounts` reports `SelfAlias=true`, and a subsequent status carries no editable flag.
- New `TestMountSelfAliasRefusesMaterialize` (Mount with `materialized=true`),
  `TestMountSelfAliasRefusesWhenEditable` (legacy `EditableProjects` entry),
  `TestSetEditableRefusesSelfAlias` (+ `--off` allowed), `TestMaterializeRefusesSelfAlias`.
- New `TestRefuseVaultIDCollisionUnconditional` — direct unit: local claim
  (`WarrenID==""`) now refuses with `"collides with the local workspace vault"`.
- Keep `warren_inverse_test.go:371-431` `TestMountRefusesVaultIDCollision` unchanged (cross-warren
  refusal text is untouched).

## R1.3 — Engine reload: skip `rt.Set`; live-path bridge endpoints

**`ReloadWarrenState` (`internal/mcp/warren_reload.go:42-50`).** One guard in the mount loop:

```go
for _, mount := range mounts {
    if mount.VaultID == "" || !mount.Available {
        continue
    }
    if mount.SelfAlias {
        // Self-alias: the live local vault answers for this vault ID. A
        // route to the warren copy would shadow it with a stale snapshot
        // (the pre-R1 bug); routes.yml may legitimately map this ID to our
        // own live path (that is how OTHER workspaces resolve us) — leave
        // whatever is there alone.
        continue
    }
    ...rt.Set(mount.VaultID, mount.Path)...
}
```

Use `mount.SelfAlias` (fresh, derived by `ActiveMounts` from `_config.md`) rather than
`e.LocalVaultID` so a daemon whose cached ID went stale still skips correctly. Do NOT
`rt.Remove(LocalVaultID)`: a persisted `routes.yml` entry mapping the local ID to this
workspace's own `.marmot` is *correct* (peers resolve us through it); only the in-memory
warren-copy override was poisonous, and it was never persisted (`rt` is rebuilt from
`routes.Load()` on every reload, :31-34). All three trigger paths — startup
(`cmd/marmot/pipeline.go:263`), the daemon `_warren.md` watcher
(`internal/daemon/owner.go:330-337`), and the HTTP refresh endpoint
(`internal/api/handlers.go:950-978`) — funnel through this one loop, so the skip lands once.
`VaultRegistry.Rebuild` (:57 → `internal/namespace/registry.go:147-164`) then evicts any cached
poisoned self-vault automatically, because `dirForLocked(localID)` no longer resolves to the
warren copy — assert this in tests.

**`warrenRuntimeBridges` (`warren_reload.go:105-178`).** No signature change needed —
`ProjectStatus.SelfAlias` carries the predicate and `marmotDir` is already a parameter. In the
bridge synthesis (:143-170), substitute the live path per endpoint and drop degenerate bridges:

```go
source, sourceOK := activeProjects[bridge.Source]
target, targetOK := activeProjects[bridge.Target]
if !sourceOK || !targetOK || source.VaultID == "" || target.VaultID == "" {
    continue
}
if source.VaultID == target.VaultID {
    // Both endpoints resolve to one vault (e.g. both alias the local
    // vault): a self-bridge is meaningless — skip, don't synthesize.
    continue
}
sourcePath, targetPath := source.Path, target.Path
if source.SelfAlias {
    sourcePath = absMarmotDir // filepath.Abs(marmotDir), computed once at function top
}
if target.SelfAlias {
    targetPath = absMarmotDir
}
runtimeBridge = &namespace.Bridge{
    Source: bridge.Source, Target: bridge.Target,
    SourceVaultID: source.VaultID, TargetVaultID: target.VaultID,
    SourceVaultPath: sourcePath, TargetVaultPath: targetPath,
}
```

`SourceVaultID`/`TargetVaultID` are deliberately unchanged (the alias side carries the local vault
ID): `NSManager.ValidateCrossVaultEdge(e.LocalVaultID, remoteID, relation)`
(`internal/mcp/engine.go:294`) matches bridges by ID, so a local↔remote edge validates against
this synthesized bridge exactly as before. In R1 the self project must still be *mounted* for the
bridge to activate (both endpoints must be in `activeProjects`, :144-147) — removing the mount
ritual entirely is R2.

**Tests (R1.3):** in `internal/mcp/warren_reload_test.go` (fixture `warrenEngine` :25-44 hardcodes
local `vault_id: local-vault`; add a project whose metadata vault_id is `local-vault`):
- `TestReloadWarrenStateSelfAliasSkipsRoute` — after reload, `rt.Get("local-vault")` unset (or
  preserved at its pre-seeded routes.yml value if one is planted) and
  `VaultRegistry.KnownVaultIDs()` excludes `local-vault`; a route pre-seeded to the warren copy is
  evicted after reload (Rebuild eviction).
- `TestWarrenRuntimeBridgesSelfAliasEndpoint` — bridge `local-proj ↔ proj-a`: synthesized bridge
  has `SourceVaultPath == <abs workspace .marmot>` and `SourceVaultID == "local-vault"`.
- `TestWarrenRuntimeBridgesSkipsSelfToSelf` — manifest bridge between two projects that both carry
  the local vault ID synthesizes nothing.
- `internal/daemon/warren_watch_test.go:21-110` sibling: mount a project with `vault_id:
  local-vault` while the owner is live; assert the registry does NOT gain a `local-vault` route
  (inverse of the existing `proj-a-vault` assertion at :102-103).
- `cmd/marmot/warren_test.go:458-474` `TestBuildEngineAlwaysCreatesVaultRegistry` — add explicit
  assertion that a self-alias mount leaves `KnownVaultIDs()` empty.
- `cmd/marmot/warren_test.go:330-410` `TestBuildEngineQueriesActiveWarrenMount` — keep as the
  non-self case; add sibling `TestBuildEngineSelfMountResolvesLiveVault`: workspace `_config.md`
  gains the mounted copy's vault_id (:337 currently has none), local node content diverges from
  the warren copy, query/traversal returns the LIVE content.
- `cmd/marmot/pipeline_warren_test.go:14+` — new case in the bridge-gating test: bridge between
  self-alias and vault-b activates with the workspace's `.marmot` as the alias endpoint (helpers
  `saveWarrenProject`/`writeTestConfig` :132-153 already parameterize vault_id).

## R1.4 — Resolution and MCP write path: `@<LocalVaultID>/…` means "me, live"

After R1.3 the registry can no longer resolve the local ID via a warren-copy route; without more,
`@<LocalVaultID>/x` would go dark. Resolve it at the *engine* layer against the live in-memory
graph — never through the registry (invariant 4; the registry path would load a second read-only
copy from disk with up-to-60s TTL staleness, `registry.go:28`, and `ResolveEmbeddingStore` takes a
shared `vault.read.lock`, `registry.go:328-340`, which would make our own `index --force` refuse
against ourselves — the exact refusal e2e pins at `e2e/warren_test.go:318`).

**(a) `BridgedGraphResolver` (`internal/traversal/bridged.go:19-78`).** Add a field and a local
short-circuit that mirrors the remote path's ID-rewrite discipline (traversal keys stay
`@`-qualified):

```go
type BridgedGraphResolver struct {
    Local        *graph.Graph
    Vaults       VaultGraphProvider // nil = single-vault mode
    LocalVaultID string             // non-empty: "@LocalVaultID/x" resolves against Local (live), not Vaults
}

// In GetNode, after parseVaultPrefix:
if vaultID != "" && vaultID == r.LocalVaultID {
    n, ok := r.Local.GetNode(localID)
    if !ok { return nil, false }
    cp := *n
    cp.ID = id
    if len(n.Edges) > 0 { cp.Edges = append([]node.Edge(nil), n.Edges...) }
    return &cp, true
}
// In GetEdges, symmetrically: r.Local.GetEdges(localID, direction) with the
// same @vaultID/ target rewrite as the remote branch (:69-77).
```

Wire it in `Engine.graphResolver` (`internal/mcp/engine.go:340-347`): `LocalVaultID:
e.LocalVaultID`.

**(b) Registry seeding (`internal/namespace/registry.go:102-115`).** The R1.3 bridge substitution
would otherwise seed `pathToID[<workspace .marmot>] = LocalVaultID`, re-opening the
registry-resolves-self door via the bridge fallback in `dirForLocked` (:118-130). Skip local IDs
in `seedBridgePathsLocked` — the first real use of the constructor's hitherto-dead `localVaultID`
field (:54-70, referenced nowhere today):

```go
if b.SourceVaultID != "" && b.SourceVaultID != r.localVaultID && b.SourceVaultPath != "" {
    r.pathToID[b.SourceVaultPath] = b.SourceVaultID
}
// same for Target
```

Post-R1, `VaultRegistry.ResolveGraph(localID)` legitimately errors `unknown vault` unless
routes.yml carries a (correct, peer-facing) local entry — every in-process consumer short-circuits
first: query fanout already skips the local ID (`internal/mcp/handlers.go:79-81`), API search-skip
likewise (`internal/api/handlers.go:588-590`), traversal via (a), API node resolution via R1.5.

**(c) Edge validation (`internal/mcp/engine.go:285-300`).** A `@<LocalVaultID>/x` edge target is a
*local* edge wearing a costume; do not demand a LocalVaultID↔LocalVaultID bridge:

```go
if qid.VaultID != "" && qid.VaultID != e.LocalVaultID {
    if err := e.NSManager.ValidateCrossVaultEdge(...); err != nil { return err }
}
```

R1 does NOT rewrite the stored target to the unqualified form (read side handles both via (a));
canonicalizing `@self` references at write time is noted as an R2/UX-pass candidate.

**(d) MCP `@`-write guard (`internal/mcp/handlers.go`, `handleWarrenContextWrite`, mount scan at
:597-605).** Before scanning mounts:

```go
if vaultID != "" && vaultID == e.LocalVaultID {
    return mcp.NewToolResultError(fmt.Sprintf("vault %q is this workspace's own vault; write the node locally as %q (no @ prefix) — self-alias warren mounts are read-through views of the live vault", vaultID, localID)), nil
}
```

This is defense in depth over the `Editable=false` forcing from R1.1 (better message; covers
legacy state where an editable self-mount is already recorded) and closes the split-brain write
path (`warren.WriteEditableNode` into the checkout copy) the context calls out. Also update the
`context_write` tool description (`internal/mcp/server.go:88-94`) to say `@`-writes target
*foreign* editable mounts and self-qualified writes are refused.

**Tests (R1.4):**
- `internal/traversal`: new `TestBridgedResolverLocalAlias` — GetNode/GetEdges on
  `@local/x` with `LocalVaultID: "local"` return live-graph content with `@`-qualified IDs/edge
  targets; empty `LocalVaultID` falls through to `Vaults` (pre-R1 behavior).
- `internal/namespace/registry_test.go`: new seed-skip test — a bridge whose source ID equals the
  registry's `localVaultID` seeds only the target into `pathToID`; `KnownVaultIDs` excludes it.
  Existing route-priority tests (`registry_routes_test.go:34-232`) unchanged (non-local IDs).
- `internal/mcp/coverage_test.go:457` `TestValidateCrossVaultEdges` — add: `@local/x` target
  passes with no bridge; `@other/x` still requires one.
- `internal/mcp/crossvault_write_test.go` — add a case asserting a self-qualified edge target is
  accepted and traversable as local.
- `internal/mcp/warren_write_test.go:82` `TestContextWriteWarrenMatrix` — add the fourth case:
  `@local-vault/x` write refused with the "write the node locally" message, even when workspace
  state marks the self project editable (legacy state).

## R1.5 — HTTP API alignment

**(a) `handleWarrenNodeUpdate` (`internal/api/handlers.go:399-483`).** Guard immediately after
`SplitQualifiedVaultID` (:400-404), before `findWarrenMountByVault` (:405) can match a self-alias:

```go
if vaultID != "" && vaultID == s.engine.LocalVaultID {
    writeError(w, http.StatusForbidden, fmt.Sprintf("vault %q is this workspace's own vault; update the node via PUT /api/node/%s (no @ prefix)", vaultID, localID))
    return
}
```

(403 matches the existing read-only refusal at :410-412.) `findWarrenMountByVault` (:980-994)
itself stays dumb — callers gate; both callers (write :405, provenance :627) are covered by this
item and (b).

**(b) `resolveSearchNode` (:614-640).** Short-circuit the local ID to the live graph and report
alias provenance instead of `warren_mount`-with-stale-path:

```go
if vaultID != "" && vaultID == s.engine.LocalVaultID {
    n, ok := s.engine.GetGraph().GetNode(nodeID)
    if !ok { return nil, nil, false }
    return n, &warren.Provenance{
        Source: "local_alias", VaultID: vaultID,
        MarmotDir: s.engine.MarmotDir, QualifiedID: id,
        Editable: false, // edit via the unqualified local node, not @-writes
    }, true
}
```

`web/src/types.ts:42-49` types `Provenance.source` as an open string union, so `local_alias`
renders without web changes; `editable: false` keeps the detail panel's save gating
(`web/src/detail-panel.ts:221-253`) read-only for the `@`-qualified view — correct, since the API
write path now refuses it. Add `local_alias` to the union for documentation value.

**(c) `handleWarrenGraph` (:856-948).** For `mount.SelfAlias`, substitute the live store —
`node.NewStore(s.engine.MarmotDir)` instead of `node.NewStore(mount.Path)` (:894) — so the warren
view renders live nodes, not the snapshot; keep the `@vaultID/`-qualified IDs (:906) and emit
`Source: "local_alias"`, `MarmotDir: s.engine.MarmotDir`, `Editable: false` in the per-node
provenance (:907-916).

**(d) `searchMountedVaults` (:561-612).** Today the local-ID skip (:588-590) silently drops the
self project from `_warren/<id>`-scoped search entirely (local results are also filtered out of
warren scope at :537) — the alias project would be invisible in its own warren's search. Replace
the skip with a live-store search when the mount is a self-alias:

```go
for vaultID, mount := range mountByVault {
    if vaultID == "" {
        continue
    }
    if vaultID == s.engine.LocalVaultID {
        if !mount.SelfAlias {
            continue // stale engine cache or hand-edited state: stay skipped
        }
        localResults, err := s.engine.EmbeddingStore.SearchActive(vec, limit, s.engine.Embedder.Model())
        if err != nil {
            s.warnVaultOnce(vaultID, "local vault search failed for warren scope: %v", err)
            continue
        }
        for _, r := range localResults {
            results = append(results, embedding.ScoredResult{NodeID: "@" + vaultID + "/" + r.NodeID, Score: r.Score})
        }
        continue
    }
    ...existing remote path...
}
```

Results then flow through (b) for `local_alias` provenance. *Defer-if:* if PR size pressure
demands, (d) may split into a fast-follow commit — but not out of R1: without it aliasing makes
warren-scoped search strictly worse for the local project than the pre-R1 stale snapshot was.

**Tests (R1.5)** (fixture: extend `setupAPIWarren`, `internal/api/api_test.go:135`, with a
self-vault variant — workspace config vault_id equals one mounted project's):
- `TestWarrenNodeUpdateRefusesSelfAlias` — PUT `@local-id/x` → 403 with the "own vault" message,
  live node untouched, warren copy untouched.
- `TestResolveSearchNodeSelfAlias` — `@local-id/x` returns live content with `source:
  "local_alias"`, `editable: false`, `marmot_dir` = workspace `.marmot`.
- `TestWarrenGraphSelfAliasServesLiveNodes` — diverge live vs snapshot content; graph shows live.
- `TestWarrenScopedSearchIncludesSelfAlias` — `_warren/<id>` search returns `@local-id/`-prefixed
  live results.
- Existing `api_test.go:292/358/411`, `api_more_test.go:389/445`, `warren_editable_test.go:18`,
  `warren_write_equivalence_test.go:22/90/155`, `warren_warnings_test.go:43/88/115/149` all use
  non-colliding vault IDs — re-run unchanged; `warren_write_equivalence_test.go` gains the
  self-alias refusal as its third refusal case.

## R1.6 — DoctorWorkspace alignment

**`DoctorWorkspace` (`internal/warren/warren.go:814-846`).** Compute
`local := sourceVaultID(workspaceMarmotDir)` and split the report:

- **`vaultID == local` with ≥2 claimants** → NOT `vault_id_collision_workspace`. The non-local
  claimants are self-aliases. Emit per alias claimant:
  - `self_alias_mount` (severity **info**): `"project %s/%s aliases the local workspace vault (vault ID %q); it serves from the live vault"`.
  - `self_alias_editable` (severity **error**) when the claimant is in that warren entry's
    `EditableProjects` (doctor already holds `state`, :815): `"project %s/%s aliases the local vault but is marked editable — @-writes would split-brain; run 'marmot warren edit %s --warren %s --off'"`.
  - `self_alias_materialized` (severity **warning**) when
    `dirExists(materializedProjectPath(workspaceMarmotDir, warrenID, projectID))`: stale cache
    shadow; remediation `'marmot warren burrow --drop --warren %s %s'`.
- **Any other `vaultID` with ≥2 claimants** → unchanged `vault_id_collision_workspace` error
  (:839-843).
- **Optional (small, recommended):** `local_route_mismatch` (severity **warning**) when
  `routes.Load()` succeeds and maps `local` to a path other than this workspace's `.marmot` —
  covers `marmot route add <local-id> <elsewhere>` (`cmd/marmot/route.go:92`), the one remaining
  manual way to recreate the shadowing. Warning, not error: two checkouts of one repo legitimately
  share a `vault_id`.

Exit-code alignment falls out for free: `printDoctorReport` (`cmd/marmot/warren.go:670-691`) fails
only on error severity, so post-R1 **doctor errors exactly where mount refuses** (editable
self-mounts, true collisions) and is healthy exactly where mount permits (plain aliases) — the
consistency restoration the context demands. Update the `DoctorWorkspace` doc comment
(:809-813): "Mount refuses new collisions" becomes accurate without the local-case asterisk.

**Tests (R1.6):**
- `internal/warren/warren_d_test.go:449-506` `TestDoctorWorkspaceVaultIDCollision` — keep
  (two-warren collision stays error); add `TestDoctorWorkspaceSelfAliasHealthy` (plain alias → no
  error-severity issues, one `self_alias_mount` info), `TestDoctorWorkspaceSelfAliasEditableError`
  (hand-write legacy editable state → `self_alias_editable` error),
  `TestDoctorWorkspaceSelfAliasMaterializedWarns`, and (if taken) `TestDoctorLocalRouteMismatch`.
- `cmd/marmot/warren_d_test.go:304-341` `TestWarrenDoctorWorkspaceCLI` — survives as the
  legacy-two-warren case; add the companion the findings call for: a local-vault-id self-mount
  exits 0 post-R1.

## R1.7 — Adjacent collision surfaces + migration of legacy self-mount state

**`CreateCrossVaultBridge` (`internal/namespace/namespace.go:589-663`).** The second unguarded
path to the same split-brain: it never compares the two vault IDs, so bridging a vault to a copy
of itself writes `@X--@X.md` and double-`rt.Set`s X (:654-658, second Set silently clobbers).
Add, after the empty-ID checks (:600-604):

```go
if localCfg.VaultID == remoteCfg.VaultID {
    return nil, fmt.Errorf("both vaults have vault_id %q — refusing to bridge a vault to itself (a self-route would shadow the live vault); if these are truly different projects, give one a distinct vault_id in its .marmot/_config.md", localCfg.VaultID)
}
```

**`marmot route add` (`cmd/marmot/route.go:66-99`).** Global command with no workspace context;
refusing is wrong (multi-checkout setups are legitimate). R1 scope: no code change; the
`local_route_mismatch` doctor warning (R1.6) plus a doc sentence cover it.

**Migration — workspaces already carrying a warned self-mount.** No state file changes, no
migration code; R1's aliasing is *derived* (vault_id comparison), so `WorkspaceState`
(`warren.go:78-89`) is untouched and old/new binaries read each other's state freely
(old binaries simply keep the old warn-and-shadow behavior — acceptable: single-user workspaces,
and the auto-release train retires old binaries fast). Per legacy shape:

1. **Plain active self-mount:** flips to alias behavior on the next `ReloadWarrenState`
   (daemon watcher fires within ~1s of any `_warren.md` touch, or at next startup). The poisoned
   route was in-memory only — reload rebuilds `rt` from `routes.yml` every time
   (`warren_reload.go:31-34`) — so nothing to scrub.
2. **Editable self-mount:** `ActiveMounts` now reports `Editable=false` (R1.1), both write paths
   refuse with remediation (R1.4d, R1.5a), doctor errors `self_alias_editable`, and
   `warren edit <p> --warren <w> --off` (still allowed) cleans the state.
3. **Materialized self cache:** inert — bridges substitute the live path and reload never routes
   it; doctor warns `self_alias_materialized`; `burrow --drop` cleans. `refresh --pull`'s
   re-materialize loop skips it with a drop hint (R1.2's loop-side skip) instead of hard-failing
   through the `Materialize` refusal.

## R1.8 — Docs

- `docs/warrens.md:262-267` — full rewrite: the "allowed with a warning" self-mount paragraph
  becomes the alias contract (invariants 1-3, in prose); first sentence loses its "from another
  Warren" scoping (refusal is unconditional).
- `docs/warrens.md:224-228` — doctor paragraph: replace "catches legacy state" framing with the
  new code table (`self_alias_mount`/`self_alias_editable`/`self_alias_materialized`/
  `local_route_mismatch` + unchanged `vault_id_collision_workspace`).
- `docs/warrens.md:291-302, 384-405` — editable sections gain one sentence each: editable is
  refused on self-aliases; edit locally instead.
- `docs/warrens.md:376-379` and `docs/bridges.md:116-118` — "both bridge endpoints must be active
  mounted projects" gains the alias case: a self-alias mount satisfies the endpoint requirement
  and resolves to the live workspace vault.
- `docs/warrens.md:465-495` — freshness section: one sentence that reload treats self-aliases as
  live (no route, zero staleness).
- `docs/architecture.md:365-408` — Warren Mounts section: alias endpoints resolve to the local
  `.marmot`; self-aliases are excluded from the vault registry.
- Code-comment sweep: `refuseVaultIDCollision` doc (:1412-1417), `DoctorWorkspace` doc (:809-813),
  MCP `context_write` description (`internal/mcp/server.go:88-94`).

## R1.9 — e2e scenario (new; nothing existing breaks)

No warren e2e test exercises a colliding vault_id today (fixture `_config.md` has no `vault_id`;
every import overrides via `--vault-id` — `e2e/warren_test.go:24-42, 80-81`), so R1 breaks zero
e2e tests and adds one:

`TestWarrenSelfMountAlias` (`e2e/warren_test.go`, template: `TestWarrenRegisterMountQueryServe`
:149-231):
1. Consumer workspace `_config.md` written WITH `vault_id` (new inline config, not the shared
   fixture).
2. `warren project import self <consumer>/.marmot` **without** `--vault-id` — first e2e coverage
   of the vault_id-preserving import path that creates the collision by construction.
3. Import a second project (`pb`, distinct vault id), add a manifest bridge `self ↔ pb`.
4. `warren mount --all`: output contains `"mounting as an alias of the live local vault"`.
5. Mutate a live local node so it diverges from the warren snapshot; `marmot query` traversing the
   bridge returns the LIVE content; `@<local-id>/<node>` resolves live.
6. `warren edit --warren <id> self` exits non-zero with the alias message;
   `warren burrow --materialize` of `self` refused.
7. `warren doctor --workspace` exits 0; JSON contains `self_alias_mount` info.
8. Liveness variant (template `e2e/warren_refresh_test.go:45-92`): with a `MARMOT_DAEMON=1` owner
   serving, mount the self project from a second process; owner reload does not shadow the live
   vault (query for fresh local content still succeeds through the daemon).

## R1.10 — Consolidated test-update list

**Existing tests that pin the removed behavior (must change):**

| Test | Change |
|---|---|
| `internal/warren/warren_inverse_test.go:430-458` `TestMountLocalVaultCollisionWarnsOnly` | The documented deliberate-deviation test. Rewrite → `TestMountSelfAliasesLiveVault` (R1.2). |

That is the only test in the repo asserting the warn text (`grep 'matches the local workspace
vault'` → warren.go + this test). Everything else is additive:

**New/extended, by package:** `internal/warren` (R1.1, R1.2, R1.6), `internal/mcp` (R1.1, R1.3,
R1.4), `internal/traversal` (R1.4a), `internal/namespace` (R1.4b, R1.7 self-bridge refusal —
extend `namespace_test.go:438-511`), `internal/api` (R1.5), `internal/daemon` (R1.3),
`cmd/marmot` (R1.3, R1.6), `e2e` (R1.9). Tests that must be **re-run but not changed** (they
traverse mount/reload with non-colliding IDs): `internal/mcp/warren_reload_test.go:124-323` suite,
`cmd/marmot/surface_coverage_test.go:513-1090`, `cmd/marmot/warren_ux_test.go:63-398`, all
existing warren e2e.

## R1 packaging note (single PR)

One PR — "warren: self-mount aliasing (R1)" — the pieces are not independently shippable: skipping
`rt.Set` (R1.3) without the resolver short-circuit (R1.4) makes `@local-id` go dark; the alias
mount path (R1.2) without the doctor change (R1.6) inverts the current inconsistency instead of
fixing it. Commit order, each compiling and green on its own:

1. **warren layer** — `LocalVaultID` export, `ProjectStatus.SelfAlias`, `ActiveMounts`/`Status`
   builders, `Mount`/`SetEditable`/`Materialize` gating, unconditional `refuseVaultIDCollision`,
   rewritten `TestMountLocalVaultCollisionWarnsOnly`. (Behavior at this point: alias mounts exist
   in state but the engine still routes them — acceptable mid-PR since commit 2 follows.)
2. **engine + traversal + namespace** — reload skip, bridge path substitution + self↔self skip,
   `BridgedGraphResolver.LocalVaultID`, registry seed skip, `validateCrossVaultEdges`, MCP
   `@`-write guard, `WithVaultRegistry` warning, `CreateCrossVaultBridge` self-bridge refusal.
3. **HTTP API** — R1.5 a–d.
4. **doctor** — R1.6 codes + CLI companion tests.
5. **docs + e2e** — R1.8, R1.9.

**Risks & mitigations (R1):**
- *Empty `LocalVaultID` silently disables aliasing* (config unreadable, or the mock-provider
  `_config.md` that `ensureWorkspace` fabricates with no `vault_id`, `cmd/marmot/warren.go:1535`)
  → warning added in `WithVaultRegistry` (R1.1); behavior degrades to pre-R1 semantics minus the
  warn branch, and the unconditional refusal can't fire spuriously because the local claim doesn't
  exist without a local ID.
- *Engine `LocalVaultID` cache vs fresh `SelfAlias` skew under live `vault_id` edits* → reload
  keys on fresh `SelfAlias`; resolver/API guards key on the cache; worst case a restart realigns —
  documented in the field comment (R1.1).
- *Users depending on snapshot semantics of a self-mount* (querying the frozen copy via
  `@local-id`) → judged nonexistent-by-construction (the snapshot answering for the live ID is the
  bug); release notes call it out.
- *Two legitimate checkouts of one repo sharing a `vault_id`* → only surfaces as the
  `local_route_mismatch` **warning**, never a refusal; mount-time refusal compares against
  in-workspace claims only.

---

# Workstream R2: first-class local identity

**Status: design, with a SHIP recommendation** (own PR, immediately after R1 merges — see R2.9 for
the recommendation and the explicit defer-if criteria). R1 ships first and R2 builds on R1's exact
shapes (`ProjectStatus.SelfAlias`, `warren.LocalVaultID`, the reload/resolver/write-guard
machinery); nothing below makes sense against a pre-R1 tree.

## R2.0 — The identity contract

R1 removes the *harm* of the self-mount ritual (stale routes, split-brain writes, doctor
disagreement) but keeps the ritual itself: a bridge involving the local project only activates if
the user mounts the warren's copy of themselves (`warrenRuntimeBridges` requires both endpoints in
`activeProjects`, `internal/mcp/warren_reload.go:143-148`; R1.3 explicitly defers removing that to
R2). R2's contract, extending R1.0's six invariants:

7. **Identity is derived, always on, and stateless.** A warren project is *identified* with this
   workspace iff its checkout metadata `vault_id` equals the live vault's `_config.md` `vault_id`
   (`warren.LocalVaultID`, R1.1) — the same predicate as R1's `SelfAlias`, evaluated over **all
   manifest projects of every registered warren**, not just active mounts. No mount, no verb, no
   workspace-state field.
8. **Bridges involving an identified project activate without any self-mount.** The identified
   endpoint resolves to the live workspace `.marmot`; the *other* endpoint still requires a real
   mount — mounting the foreign side remains the single deliberate act that turns a manifest
   bridge on.
9. **All R1 alias invariants apply to identified projects verbatim** (no route claim, never
   editable/materialized, live resolution, doctor agreement). An identified project is an R1
   self-alias minus the mount entry.

Blast radius: identical to R1 — a workspace without `vault_id` (the common default) derives no
identity anywhere; behavior is bit-identical to R1 for it.

## R2.1 — Mechanism decision: vault_id auto-detection, not an `identify` verb

Two candidate mechanisms were on the table; **auto-detection wins**. The weigh-up:

**(A) Auto-detect by vault_id (chosen).** Identity is computed wherever `SelfAlias` already is,
generalized from active mounts to all registered warrens' manifest projects.
- *For:* R1 already ships the predicate, the carrier field, and every downstream consumer
  (reload skip, bridge path substitution, resolver short-circuit, write guards, doctor codes) —
  R2 becomes a one-package generalization (R2.3) instead of a new subsystem. It matches the one
  existing local-identity convention in the codebase: `verify --bridges` keys on `_config.md`
  `vault_id` (`cmd/marmot/pipeline.go:769-836`); an `identify` verb would create a second,
  contradictable source of truth (an identified project whose vault_id *doesn't* match would need
  its own conflict semantics). And it actually removes the ritual — an explicit verb renames
  `mount self` to `identify self`, it doesn't delete the step.
- *Against (and answers):* (1) *Implicitness* — registering a warren silently makes bridges
  involving you activatable. Answer: activation still requires mounting the foreign endpoint
  (invariant 8), which is deliberate; and bridge *policy enforcement* already engages at register
  regardless of mounts (`warrenRuntimeBridges` `declared`, `warren_reload.go:127-137`), so the
  implicit part is only the good half. Register additionally prints a note (R2.4). (2) *Wrong
  match* — a genuinely-different project coincidentally sharing your vault_id would be silently
  treated as you. Answer: that state is exactly what R1's unconditional collision refusal and
  doctor exist for; the escape hatch is re-importing the copy with a distinct `--vault-id`
  (`ImportOptions.VaultID`, `internal/warren/warren.go:146-150`), which doctor's collision message
  already recommends. (3) *No opt-out.* Answer: opt-out = distinct `--vault-id` on import (author
  side) or unregister (consumer side); a per-workspace ignore flag is a small later addition *if*
  a real need appears — see defer criterion 2.

**(B) Explicit `warren identify <project>` verb, recorded in workspace state (rejected).**
- *For:* explicit intent; visible in `warren list`; per-workspace opt-in.
- *Against, decisive:* it requires the one schema move this codebase is worst-positioned to make.
  `WorkspaceState` has **no version field** and struct-based YAML parsing
  (`internal/warren/warren.go:79-89`, `loadWorkspaceStatePath` :990-1008,
  `normalizeWorkspaceState`/`validateWorkspaceState` :1963-1972/:2012-2027): an
  `identified_projects:` key written by a new binary is **silently dropped** on the next
  Load→Save by any older binary — identity would evaporate after any old-binary warren verb, the
  worst possible failure mode for an identity record. Shipping (B) safely requires
  workspace-state versioning first (a `version:` field + a `checkManifestWritable`-style ceiling,
  mirroring the manifest's :37 + :1805-1810) — real work with its own compat tail, bought for a
  verb that keeps the ritual. Not worth it while (A) costs nothing.

Also rejected: detect-at-register-time-and-record (auto-detect once, persist the result) —
inherits (B)'s schema problem *and* goes stale when `vault_id` or the warren manifest changes.
Derived-per-read is self-healing.

## R2.2 — Workspace-state schema: none (that is the design)

R2 adds **no fields** to `WorkspaceState`/`WorkspaceWarren` and writes nothing new to
`_warren.md`. Frontmatter round-trip implications: zero — old and new binaries exchange workspace
state freely, in both directions, with no version guard needed. `ProjectStatus.SelfAlias` (R1.1,
JSON `self_alias`) stays the sole carrier, unrenamed (it is API surface as of the R1 release).
The one interop asymmetry is behavioral, not schema: a workspace cleaned of its redundant R1
self-mount (R2.8) loses bridge activation *when driven by an R1 binary* (R1 still requires the
mount). Accepted under the fast release train; called out in release notes and in doctor's
cleanup message being a suggestion, not an auto-fix.

## R2.3 — Core: identity synthesis in `ActiveMounts`; `warrenRuntimeBridges` unchanged from R1

The entire engine-facing change is in **one function**. `ActiveMounts`
(`internal/warren/warren.go:1580-1635`) currently iterates `entry.ActiveProjects` per registered
warren; R2 makes it iterate **all manifest projects**, emitting (a) the existing mount statuses
for active non-self projects and (b) a synthesized identity status for every identified project —
mounted or not:

```go
// ActiveMounts returns active warren project vaults plus identified-local
// projects (manifest projects whose vault_id matches the live workspace
// vault) for a local .marmot dir. Identified projects are served as the
// live vault: Path is the workspace's own .marmot, never routed, never
// editable/materialized (SelfAlias).
func ActiveMounts(marmotDir string) ([]ProjectStatus, error) {
    ...
    local := sourceVaultID(marmotDir) // R1.1 hoist, now load-bearing for dormant probes too
    for warrenID, entry := range state.Warrens {
        ... manifest load, materialized fallback unchanged ...
        activeSet := toNameSet(entry.ActiveProjects)
        for _, project := range manifest.Projects {
            isActive := activeSet[project.ProjectID]
            if !isActive && local == "" {
                continue // dormant and identity impossible: no metadata probe
            }
            marmotPath := preferredProjectPath(marmotDir, warrenID, entry, project)
            meta := loadProjectMetadata(marmotPath, isActive) // warn only for active projects; dormant probes stay silent
            vaultID := projectID-fallback as today (:1608-1611)
            selfAlias := local != "" && vaultID == local
            switch {
            case selfAlias:
                mounts = append(mounts, ProjectStatus{
                    WarrenID: warrenID, WarrenPath: entry.Path,
                    ProjectID: project.ProjectID,
                    Path: marmotDir, VaultID: local,
                    Registered: true, Active: true, Available: true,
                    Editable: false, Materialized: false, SelfAlias: true,
                })
            case isActive:
                ... existing mount status (:1612-1625), unchanged ...
            }
        }
    }
    ... sort unchanged ...
}
```

Load-bearing details:
- **Dedupe is structural**: one loop, one entry per (warren, project) — an R1-era self entry in
  `ActiveProjects` produces the identity-shaped status (Path = live `.marmot`), not a
  warren-copy-path one. This *supersedes* R1.1's per-mount `SelfAlias` computation inside
  `ActiveMounts` (generalized, not duplicated).
- **Zero cost without a vault_id**: the `local == ""` early-continue means workspaces without
  identity never probe dormant projects' metadata — the reload hot path (1s debounce,
  `internal/daemon/owner.go:265`) is unchanged for them. With identity, cost is one
  `LoadProjectMetadata` file read per dormant manifest project per reload; see defer criterion 3.
- **Dormant probes must not warn**: `loadProjectMetadataWarn` (:1607) warns on unreadable
  metadata; probing 50 dormant projects must not emit 50 warnings — use the silent loader for
  dormant probes, keep the warn for active ones.
- The `materialized`-fallback branch (manifest unreachable, :1589-1595) synthesizes nothing: no
  manifest, no identity — bridges involving self degrade exactly like every other bridge of an
  unreachable warren. But `materializedStatuses` (:1687) **keeps R1.1's `SelfAlias` computation**:
  a *legacy* self burrow cache surfacing through this branch without the flag would be routed by
  the reload loop (`rt.Set(local, cachePath)`), re-poisoning `@local-id` — the pre-R1 bug back
  through the one door R2's restructure doesn't touch. Pin it: unreachable manifest + legacy self
  cache → status still `SelfAlias=true`, registry gains no local route.

**`warrenRuntimeBridges` change relative to R1's version: none.** This is the payoff of R1.3's
shape. The synthesized statuses carry `Active: true, Available: true, VaultID != ""`, so they pass
the `activeProjects` filter (`warren_reload.go:115-118`) and satisfy the both-endpoints-active
check (:143-148) with no mount; R1's `SelfAlias → absMarmotDir` path substitution and self↔self
skip apply verbatim (the substitution also normalizes the synthesized relative `marmotDir` to
absolute — keep it). Likewise unchanged: `ReloadWarrenState`'s mount loop skips the synthesized
entries via `mount.SelfAlias` (R1.3), so no route is ever set; `handleWarrenGraph` renders them
from the live store (R1.5c); `searchMountedVaults` includes them (R1.5d); every write guard
already refuses them. One cosmetic touch: the reload count line
(`"warren: %d active project mounts loaded"`, `warren_reload.go:51-53`) should say
`"%d active project mounts (%d identity)"` so logs distinguish them.

## R2.4 — Verb semantics

- **`Mount` (`internal/warren/warren.go:1060-1106`).** R1.2's self-alias branch stops writing
  state: keep the `--materialize` refusal and the legacy-`EditableProjects` refusal verbatim, but
  replace the `entry.ActiveProjects = addName(...)` + "mounting as an alias" note with a pure
  skip:
  `fmt.Fprintf(warnWriter, "note: project %s/%s IS this workspace (vault ID %q); identity is automatic — bridges involving it activate without a mount\n", ...)`.
  `mount --all` (`cmd/marmot/warren.go:893-905` expands to every manifest project) therefore
  skips the self project with the note instead of recording it — `--all` over a warren containing
  yourself stays usable.
- **`Unmount` (:1189-1208) unchanged.** It is the cleanup path for R1-era self entries
  (`removeNames` works on any recorded name). Post-cleanup, unmounting an identified project that
  was never mounted correctly errors `"not mounted"` (:1199-1201); append one clause to that
  message: `"(identity is derived from vault_id, not a mount — to sever it, re-import the warren copy with a distinct --vault-id)"`.
- **`SetEditable` (:1109-1170).** R1.2's refusal stays verbatim. Delta: R1.2 made `--off` on an
  unmounted self project auto-mount it as an alias; under R2 the self case must **skip both** the
  auto-mount collision block (:1154-1160) **and** the unconditional
  `entry.ActiveProjects = addName(...)` that follows it (:1161) — the addName is where the mount
  record is actually written, and leaving it would re-record R1-era self state on every `--off`
  (there is nothing to mount; `--off` just clears any legacy `EditableProjects` entry).
- **`warren register` (`cmd/marmot/warren.go:751-775`).** After `RegisterWorkspaceWarren`
  succeeds, detect and announce identity — the auto-detect-at-register hook from the brief,
  implemented with zero new API: call `warren.ActiveMounts(marmotDir)`, filter
  `WarrenID == id && SelfAlias`, and print per hit:
  `"note: project %q in warren %q matches this workspace's vault ID %q — served as your live vault; manifest bridges involving it activate once their other endpoint is mounted"`.
- **`burrow`/`burrow --drop`:** unchanged (Materialize refuses self per R1.2; `--drop` cleans
  legacy caches).

## R2.5 — Interactions: import, propose, refresh --pull

- **Import / re-import of an identified project (author side).** Import refuses an existing
  project ID (`importProjectLocked`, `warren.go:293`), so "re-import" is `project remove` +
  `project import` today. Identity is keyed on vault_id, so a vault_id-preserving re-import
  re-establishes identity automatically the moment the fresh copy lands; re-importing with a
  distinct `--vault-id` is the documented **opt-out** (the copy becomes a foreign project,
  mountable/burrowable like any other). No code change; one docs paragraph (R2.10).
- **Propose (`cmd/marmot/warren.go:1397-1507`).** Propose commits pathspec-limited checkout
  changes (`projects/<pid>/`, :1464-1491) — meaningless for an identified project, whose edits
  live in the workspace vault and never touch the checkout. Default selection can never pick one
  (sole-*editable*-project rule, :1434-1446, and identified projects are never editable); only an
  explicit `warren propose <self-project>` reaches it. Refuse there, after manifest resolution
  (:1452-1463) — keep `marmotDir` from `locateWorkspace` (currently discarded, :1409) and compare
  the project's checkout metadata vault_id against `warren.LocalVaultID(marmotDir)`:
  `"warren propose: project %q is this workspace (vault ID %q); its live context never lands in the warren checkout — refresh the warren's copy in the warren repo (project remove + project import) and commit there"`.
  A real "publish my live context to the warren" leg (sync live vault → checkout via the import
  copier, then the existing branch+commit machinery) is genuinely useful but is **new machinery —
  backlog for the UX pass**, not R2.
- **`refresh --pull` (`cmd/marmot/warren.go:1322-1388`).** Nothing new needed beyond R1.2's
  loop-side skip (as corrected above): identified projects are not in `ActiveProjects`
  post-migration, so the re-materialize loop (:1365) never visits them; R1-era self entries with
  legacy caches hit the skip-with-drop-hint. The pull/ff-only leg is per-checkout, identity-blind
  — correct as is.

## R2.6 — Status / list / API / web display

- **`warren status`**: `Status` (`internal/warren/warren.go:1512-1577`) already computes
  `SelfAlias` per R1.1; R2 additionally sets `Path` to the workspace `.marmot` on identity rows
  (:1563 today reports the checkout path — a lie for an identified project). CLI table
  (`cmd/marmot/warren.go:1177-1185`): third STATE value —
  `if status.SelfAlias { state = "identity" } else if status.Active { state = "mounted" }` —
  with EDITABLE always `false` and PATH the live vault. `status --json` is `[]ProjectStatus`
  passthrough; `self_alias` (R1) plus the corrected `path` are additive — the e2e JSON scrape
  (`e2e/warren_test.go:273-276`) keys on `project_id`/`active` and survives.
- **`warren list`**: identity is derived, so the state-passthrough JSON can't show it. Extend the
  additive `listEntry` pattern (`cmd/marmot/warren.go:793-806`, which already grafts `reachable`)
  with `identified_projects []string` (computed via `ActiveMounts` filter), and add an
  `IDENTITY` column to the human table (project IDs, `-` when none).
- **HTTP API**: `WarrenStatusResponse`/`WarrensResponse` (`internal/api/types.go:83-115`)
  serialize `ProjectStatus`/`WorkspaceWarren` verbatim — status gets `self_alias` for free; for
  `/api/warrens` add the same computed `identified_projects` per warren (additive field on the
  response type, not on `warren.WorkspaceWarren`).
- **Web**: `types.ts:66-71` gains optional `identified_projects?: string[]`; rendering it in the
  selector label (`main.ts:177-183`) and detail views is deferred to the UX pass — the graph view
  already shows identified projects live via R1.5c since they appear in `ActiveMounts`.

## R2.7 — Doctor

`DoctorWorkspace` (post-R1.6 shape) deltas:

- **New `self_identity` (info)**, one per identified project regardless of mounts:
  `"project %s/%s is identified with this workspace (vault ID %q); it serves from the live vault"` —
  makes identity discoverable from doctor even on a workspace that never mounted anything.
- **`self_alias_mount` (info) repurposed as the redundancy signal**: emitted only when an
  identified project *also* sits in `ActiveProjects` (R1-era state); message becomes
  `"project %s/%s has a redundant self-mount recorded — identity is automatic; clean with 'marmot warren unmount --warren %s %s'"`.
  Cleanup is suggested, never automatic (see the interop asymmetry, R2.2).
- **`self_alias_editable` (error) and `self_alias_materialized` (warning) unchanged** — legacy
  shapes, same remediations. `vault_id_collision_workspace` and `local_route_mismatch` unchanged.

Doctor must derive identity the same way `ActiveMounts` does (share the probe — either call
`ActiveMounts` or extract the per-warren identity scan into an unexported helper both use), so
doctor and the engine can never disagree about who is identified.

## R2.8 — Migration from R1 aliasing; deleted vs generalized

**No migration code, no state rewrite.** Every R1-era shape is recognized and functional:

| R1-era state | R2 behavior | Cleanup |
|---|---|---|
| Self project in `ActiveProjects` (R1 alias mount) | `ActiveMounts` emits the identity entry for it (structural dedupe, R2.3); behavior identical to a never-mounted identified project | doctor `self_alias_mount` info → `warren unmount` (optional; harmless if kept) |
| Self in `EditableProjects` | already forced off / refused by R1; unchanged | doctor `self_alias_editable` error → `warren edit --off` (SetEditable's self no-op keeps `--off` working, R2.4) |
| Legacy self burrow cache | inert (R1); `refresh --pull` skips it (R1.2 corrected) | doctor `self_alias_materialized` warning → `burrow --drop` |

Nothing is stranded: an R1 workspace upgraded to R2 keeps its bridges active through the (now
redundant) mount entry with zero behavior change, and works identically after cleanup. The only
regression vector is *downgrade after cleanup* (R2.2) — release-notes material, not code.

**R1 code deleted vs generalized by R2:**

- *Deleted:* `Mount`'s alias-branch state mutation + "mounting as an alias" note (→ skip + new
  note, R2.4); `SetEditable`'s alias auto-mount path (→ self no-op); `ActiveMounts`'/`Status`'s
  per-active-mount `SelfAlias` computation (→ subsumed by the manifest-wide scan / Path
  substitution — but `materializedStatuses`' copy of it stays, see the R2.3 fallback note).
- *Kept verbatim (the R1 investment):* `refuseVaultIDCollision` unconditional; `Materialize` +
  `WriteEditableNode` refusals; `ReloadWarrenState` route skip; `warrenRuntimeBridges` path
  substitution + self↔self skip; `BridgedGraphResolver.LocalVaultID`; registry seed skip;
  `validateCrossVaultEdges` exemption; MCP/API `@`-write guards;
  `resolveSearchNode`/`handleWarrenGraph`/`searchMountedVaults` alias handling;
  `CreateCrossVaultBridge` self-refusal; doctor error/warning codes; `local_route_mismatch`.

## R2.9 — Ship/defer recommendation

**Recommendation: SHIP**, as one PR ("warren: first-class local identity (R2)") immediately after
R1 merges — gated on R1 *merged*, not merely green, since R2 edits R1's exact code. Rationale: the
marginal diff is one function generalized (`ActiveMounts`), a handful of verb messages, display
columns, two doctor codes, docs, and tests — no schema change, no new subsystem, no
`warrenRuntimeBridges`/engine change at all. Deferring R2 means the R1 release documents
alias-*mounting yourself* as the recommended bridge-activation flow, which R2 then re-documents
away — two doc churns and a user-visible ritual change for no benefit. The design risk R2 adds
over R1 is essentially zero because every resolution/write/doctor path is R1 code exercised
identically.

**Defer-if criteria (any one suffices; re-evaluate after R1 bake):**

1. **R1's shapes move in review/bake** — if `ProjectStatus.SelfAlias` (the carrier) or
   `warren.LocalVaultID` (the probe) get reshaped or cut from R1, R2's foundation is gone; rebase
   R2 after the dust settles rather than co-evolving two PRs.
2. **Product wants opt-in identity** — if R1 feedback shows users are surprised that
   register + foreign-mount activates local-endpoint bridges (i.e., invariant 7's "always on" is
   wrong), R2 re-scopes to the explicit-verb design (R2.1-B), which **must** be preceded by
   workspace-state versioning (`version:` field + write ceiling mirroring the manifest's,
   `warren.go:37, 1805-1810`) — that ordering is non-negotiable given the silent-field-drop
   round-trip (R2.2).
3. **Reload-path probing regresses** — the identity scan adds one metadata read per dormant
   manifest project per reload *when the workspace has a vault_id*. If profiling on a realistic
   large warren (≳100 dormant projects) shows the 1s-debounced reload or `buildEngine` startup
   visibly regressing, land a metadata mtime-cache first. (Expected: negligible — these are
   sub-KB frontmatter files — but measure before shipping, in the R2 PR itself.)
4. **Mixed-binary workspaces turn out common** — if the R1 release reveals workspaces routinely
   driven by multiple marmot versions (shared checkouts, CI + local), the downgrade asymmetry
   (R2.2) bites; hold R2's doctor cleanup suggestion (keep `self_alias_mount` as plain info, no
   unmount hint) until the fleet is past R1, or defer R2 one release.

## R2.10 — Tests, docs, e2e

**`internal/warren`:**
- `TestActiveMountsSynthesizesIdentity` — registered warren, identified project, **zero** mounts →
  exactly one `SelfAlias` status: `Path == marmotDir`, `Active && Available`, `!Editable`,
  `!Materialized`.
- `TestActiveMountsDedupesR1SelfMount` — self project *in* `ActiveProjects` (hand-written R1-era
  state) → exactly one entry, identity-shaped (live path, not warren-copy path).
- `TestActiveMountsNoLocalIDSkipsDormantProbes` — no workspace vault_id + a dormant project with
  *unreadable* metadata → no warning emitted, no identity entry (proves the early-continue).
- `TestMountSelfIsNoOp` — mount of self project: exit success, note printed, `ActiveProjects`
  unchanged; `TestMountAllSkipsSelf`.
- `TestUnmountCleansR1SelfMount`; `TestSetEditableOffSelfClearsWithoutMount`.
- `TestStatusIdentityRow` — `Status` reports `SelfAlias` with `Path` = workspace `.marmot`.
- Rewrites of R1 tests: `TestMountSelfAliasesLiveVault` (R1.2) reshapes to the no-op semantics;
  R1's `TestActiveMountsMarksSelfAlias` (R1.1) merges into `TestActiveMountsDedupesR1SelfMount`.

**`internal/mcp`:** `TestReloadWarrenStateIdentityBridgeWithoutMount` — warren with bridge
`self-proj ↔ proj-a`, only `proj-a` mounted, self **never** mounted: synthesized bridge exists
with the workspace `.marmot` endpoint, `KnownVaultIDs()` excludes `local-vault`. (This is R1.3's
`TestWarrenRuntimeBridgesSelfAliasEndpoint` minus the self-mount step — keep both; the delta *is*
R2.)

**`cmd/marmot`:** `TestWarrenRegisterAnnouncesIdentity`; `TestWarrenStatusShowsIdentityState`
(table contains `identity`); `TestWarrenListIdentifiedColumn` (+ JSON `identified_projects`);
`TestWarrenProposeRefusesIdentified`; `TestWarrenRefreshPullSkipsLegacySelfCache` (R1-era state:
self in `ActiveProjects` + cache + moved HEAD → exit 0, skip warning, other projects
re-materialized); `pipeline_warren_test.go` sibling: bridge activates with no self-mount.

**`internal/api`:** `TestWarrensResponseIdentifiedProjects`; extend R1.5's self-alias API tests to
run **without** the self-mount step (identity-only fixture) — same expected results.

**e2e:** extend R1.9's `TestWarrenSelfMountAlias` into `TestWarrenLocalIdentity`: drop step 4's
self-mount (mount only `pb`); assert the bridge traverses live content with self never mounted;
`warren status` shows `identity`; explicit `warren mount ... self` prints the no-op note and
records nothing; doctor exits 0 with `self_identity` info. Migration leg: hand-write R1-era
`_warren.md` (self in `active_projects`) → behavior identical, doctor shows the redundancy info,
`unmount` cleans, queries still live. Daemon leg unchanged from R1.9 step 8.

**Docs:** `docs/warrens.md` — R1.8's rewritten self-mount paragraph (:262-267) becomes the
identity section ("a project whose vault_id matches your workspace *is* your workspace; register
the warren, mount the other endpoint, the bridge is live"); the endpoint-requirement sentence
(:376-379, and `docs/bridges.md:116-118`) becomes "both endpoints must be active mounted projects
*or identified with this workspace*"; propose section (:338-353) gains the identified-project
refusal + re-import flow; the import section documents the `--vault-id` opt-out; the onboarding
gap shrinks to: `configure` (vault_id — requires U1 piece 0; today no verb writes it) → author
imports → `register` → `mount <other>` — feed that to the UX pass quickstart. `docs/architecture.md:365-408` — identified projects join the
bridge endpoint description. Sweep the R1.8 code comments that say "mount yourself to activate
bridges".

# Workstream U: next warren UX pass

Grounded in the scanners' inventory of the *actual* current surface (all line refs re-verified
against the working tree): 15 warren subcommands (`cmd/marmot/warren.go:115`), six warren HTTP
endpoints (`internal/api/api.go:80-86`), four warren-adjacent fetches in the web client
(`web/src/api.ts:58-75` + the refresh POST in `main.ts:199-210`). Items are **ranked by user
impact**; each carries an **R1/R2 dependency marker** (`independent` = can land before or in
parallel with R1/R2; `after R1`/`after R2` = touches their code or documents their surface).
Sequencing across items is in [Testing & Rollout](#testing--rollout).

## U1 — Onboarding quickstart (rank 1; docs land **after R2 decision**, nudges independent)

**Problem.** There is no zero-to-first-bridge walkthrough anywhere: `docs/warrens.md:235-337` is
reference-shaped; the README's warren block (`README.md:162-198`) is a terse command dump with no
`vault_id` step — and `vault_id` is the one prerequisite for identity/bridges involving your own
project (R1.0 blast-radius note: no `vault_id`, no aliasing). Worse, the only documented answer to
"how do I activate a bridge involving my own project" is the self-mount paragraph
(`docs/warrens.md:262-267`) — i.e. the current onboarding path *is* the thing R1/R2 replace.
Compounding it, mutating warren verbs fabricate a mock-provider `_config.md` with **no
`vault_id`** via `ensureWorkspace` (`cmd/marmot/warren.go:1535`), so a user who starts from
`warren register` has silently opted out of identity forever. Deeper still: **no CLI verb writes
`vault_id` at all** — `marmot configure` prompts only provider/model/classifier
(`cmd/marmot/configure.go:68-141`; `config.VaultID`, `internal/config/config.go:20`, has no
writer anywhere in cmd/), so today the sole way to gain an identity is hand-editing
`_config.md` frontmatter, and `CreateCrossVaultBridge`'s existing "run 'marmot configure' first"
hint (`internal/namespace/namespace.go:601,604`) is already misleading.

**Proposal (four small pieces):**

0. **Teach `marmot configure` to set `vault_id`.** One additional prompt in the existing
   interactive flow (default: current value if set, else a slug of the workspace directory name)
   plus a non-interactive `--vault-id` flag, persisted through the existing `config.Save`
   (`internal/config/config.go:68`). Prerequisite for pieces 1-2 — without it the quickstart's
   first step and the nudge's remediation command do not exist — and it makes the stale
   namespace.go:601 hint true for free.
1. **"Quickstart: zero to first bridge"** section at the top of `docs/warrens.md`, following the
   flow R2.10 hands off: `marmot configure` (sets `vault_id`, piece 0) → author `warren init` /
   `project import` / `bridge add` / `doctor` → consumer `warren register` → `warren mount <other
   endpoint>` → `marmot query` traverses the bridge. One page, copy-pasteable, ends with "what
   you should see" (`warren status` table incl. the `identity` state, doctor exit 0). README's
   block gets a pointer + the missing `marmot configure` line.
2. **No-vault_id nudge** at the two entry points: `warrenRegister` (`cmd/marmot/warren.go:766-772`)
   and `ensureWorkspace`'s fabrication path (:1535) print
   `note: this workspace has no vault_id in _config.md; warren bridges involving this project cannot identify it — set one with 'marmot configure'`
   (register-side complement to R2.4's identity announcement, which only fires when a match
   exists). Ships with or after piece 0, so the remediation it names actually works.
3. **`warren init --guided`: DEFERRED to backlog.** The CLI's one interactive surface today is
   `marmot configure`'s prompt menus (`cmd/marmot/configure.go:68-141`); a guided warren init
   would spread that machinery across many verbs while the docs walkthrough covers the same
   ground for near-zero cost. Revisit only if quickstart-doc feedback shows users still stall.

**Dependency:** piece 1 documents whichever contract ships — write it *after* the R2 ship/defer
call so it says "identity is automatic" (R2) or "mount yourself as an alias" (R1-only) exactly
once. Pieces 0/2-3: independent (piece 2 sequenced after piece 0).

**Tests:** `cmd/marmot` — `TestConfigureSetsVaultID` (prompt + `--vault-id` flag round-trip
through `config.Load`); `TestWarrenRegisterNudgesMissingVaultID` (register in a vault_id-less
workspace → stderr contains the nudge; with vault_id → absent). Docs are exercised by following
them in the R2 e2e (`TestWarrenLocalIdentity` steps mirror the quickstart on purpose).

## U2 — Flag consolidation with a deprecation path (rank 2; **independent**, land after R1 to avoid churn)

**Problem, concretely.** Every repo-side verb registers three compat spellings
(`cmd/marmot/warren.go:249-257` and ten siblings):

```go
249	root := fs.String("warren-dir", ".", "Warren repository root")
250	rootCompat := fs.String("root", "", "Warren repository root")
253	idCompat := fs.String("id", "", "project ID")
254	aliasesCompat := fs.String("aliases", "", "comma-separated aliases")
257	fs.Var(&aliases, "alias", "project alias (repeatable)")
```

None of the compat spellings appear in any usage line, doc, or e2e invocation (the e2e suite uses
only `--dir/--warren/--id/--vault-id`, `e2e/warren_test.go:152-197`) — they are silent, untaught,
and yet pinned as the compat contract by `surface_coverage_test.go:513-1090`. Plus one genuine
trap: `warren project add --id X <positional>` silently reinterprets the positional as the PATH
(`warren.go:274-278`). The repo-side/workspace-side vocabulary split (`--warren-dir <path>` vs
`--dir <.marmot> --warren <id>`) is **kept** — the two flag sets select different things (a warren
repo root vs a workspace vault) and merging them would be the real confusion.

**Proposal — deprecate loudly, remove never (in this pass):**

1. One helper in `cmd/marmot/flags.go`:
   ```go
   // warnDeprecatedFlag prints a one-line stderr notice when a legacy
   // spelling was actually used. Legacy flags keep working; nothing in this
   // pass schedules their removal (auto-release means every merge ships —
   // a silent break is never acceptable).
   func warnDeprecatedFlag(used bool, old, canonical string) {
       if used {
           fmt.Fprintf(os.Stderr, "warning: --%s is deprecated; use --%s\n", old, canonical)
       }
   }
   ```
   Apply after each `fs.Parse`: `--root`→`--warren-dir` (12 sites: init, project add/import/list/remove/rename/set-readonly, bridge add/list/remove, doctor, format), `--aliases`→`--alias`
   (project add :254, import :351), `--id`→positional project-id (add :253, import :350; **not**
   `warren init --id`, which is the canonical spelling there, usage :137).
2. **Refuse the ambiguity trap** instead of reinterpreting. Before (behavior at
   `warren.go:274-278`): `warren project add --id pay ./svc` treats `./svc` as `--path`. After:
   ```
   warren project add: both --id "pay" and a positional argument "./svc" given; write 'marmot warren project add pay --path ./svc'
   ```
3. Help-text sweep: usage lines already show only canonical spellings — assert that stays true
   (grep `--root|--aliases` in usage strings → zero hits today; add a `surface_coverage_test`
   guard so it stays zero).

**Tests:** extend `surface_coverage_test.go` — existing compat tests (e.g.
`TestWarrenInterspersedFlagsAfterPositionals:938`) stay green *and* gain
stderr-deprecation-warning assertions; new `TestWarrenProjectAddRefusesIDPlusPositional`;
canonical-spelling invocations assert **no** warning.

## U3 — status / list / doctor output ergonomics (rank 3; identity columns **after R2**, rest independent)

Current surfaces, verbatim: `warren status` table header
`PROJECT\tSTATE\tEDITABLE\tAVAILABLE\tPATH` (`cmd/marmot/warren.go:1177`), STATE ∈
{`dormant`,`mounted`}; `warren list` header
`WARREN_ID\tPATH\tREACHABLE\tACTIVE\tEDITABLE\tMATERIALIZED` (:818);
`status --json` is a raw `warren.Status()` passthrough whose consumers scrape
around stderr noise (`e2e/warren_test.go:268-269` — "The JSON array may be preceded by stderr
warnings in CombinedOutput").

1. **Identity display** — already specced as R2.6 (STATE gains `identity`; `warren list` gains an
   `IDENTITY` column + additive `identified_projects` JSON). U3 owns no duplicate spec; it owns
   verifying the *rendering* (column widths, `-` placeholder, `--json` additivity) lands with
   R2.6 and reads coherently next to the burrow-cache trailer lines (:1186-1190).
2. **Machine-output purity guarantee (independent).** All degradation warnings already go to
   stderr (`warnWriter = os.Stderr`, `internal/warren/warren.go:41`); stdout in `--json` mode is
   pure JSON today. Make that a *contract*: one sentence in `docs/warrens.md` ("in `--json` mode
   stdout is exactly one JSON document; diagnostics go to stderr") + an e2e assertion that runs
   `status --json` with separated streams and `json.Unmarshal`s stdout whole — retiring the
   index/scrape hack as the template for future tests.
3. **Doctor output (independent).** `printDoctorReport` (`cmd/marmot/warren.go:670-691`) prints
   `severity code message` lines; post-R1 there are 4 new workspace codes (R1.6) and post-R2 one
   more (R2.7). Add a summary tail line — `doctor: N error(s), M warning(s), K info` — and in
   `--json` mode emit the same report object doctor already builds (exists today; keep stable).
   No re-ranking of severities here; R1.6/R2.7 own semantics.

**Tests:** e2e stream-separation assertion (piece 2); `TestWarrenDoctorSummaryLine`;
R2.10's `TestWarrenStatusShowsIdentityState`/`TestWarrenListIdentifiedColumn` cover piece 1.

## U4 — Error-message quality sweep, incl. the new A–D verbs (rank 4; **after R1+R2** so their strings are swept too)

The A–D verbs' pinned strings are mostly good (`"checkout pulled"`, `"never pushes"`,
`"uncommitted change"`, unmount's cache-kept hint with a full drop command,
unregister's refusals embedding exact commands — `e2e/warren_test.go:338-354`,
`internal/warren` findings). The sweep fixes the outliers, before → after verbatim:

1. **Collision refusal lacks remediation** (`internal/warren/warren.go:1427`, post-R1 shape in
   R1.2), while doctor's twin has one (:842). Before:
   `vault ID %q of project %s/%s collides with %s already mounted in this workspace`
   After (append): `... in this workspace — unmount it or re-import one with a distinct --vault-id`.
2. **API read-only refusal names no fix** (`internal/api/handlers.go:411`). Before:
   `Warren project is read-only in this workspace: <project>` After:
   `Warren project is read-only in this workspace: <project> — enable writes with 'marmot warren edit <project> --warren <id>' (unless the warren author marked it read-only)`.
3. **Registry error parity** (`internal/namespace/registry.go:314` vs :208). Before:
   `unknown vault %q` (ResolveEmbeddingStore). After: match its ResolveGraph twin —
   `unknown vault %q: not in routing table or bridge manifests`.
4. **Skipped projects are reasonless over HTTP**: graph responses carry `Skipped []string`
   (`internal/api/types.go:54-55`) while the reasons go to stderr (`handlers.go:890, 897`). Add
   additive `skipped_reasons map[string]string` populated where the stderr warnings fire; web UI
   consumption is U5.
5. **Sweep the ~10 new R1/R2 messages** (R1.2/R1.4/R1.5/R2.4 quote them verbatim in this plan)
   after bake: consistent verb prefixes (`warren <verb>: ...`), every refusal names its
   remediation command, remediation commands use canonical flag spellings (U2 alignment; today's
   strings already do — `warren.go` findings list six embedding exact CLI invocations).
6. **`MARMOT_ROUTES` documented** (`internal/routes/routes.go:54-71` env knob appears in no help
   or doc): one paragraph in `docs/warrens.md`'s routing section.

**Tests:** message pins live in `warren_ux_test.go:63-398`, `surface_coverage_test.go`,
`internal/api` warren tests — update pins in the same commit as each string change (grep-driven:
every changed literal gets its pinning test updated or added). New: `TestResolveEmbeddingStoreUnknownVaultHint`.

## U5 — Web UI warren management + API endpoints (rank 5; endpoints independent, display bits **after R2**)

**What exists** (verbatim `internal/api/api.go:80-86`): `GET /api/bridges`, `GET /api/warrens`,
`GET /api/warren/{id}` and `GET /api/warren/{id}/status` (duplicate routes, same handler),
`GET /api/warren/{id}/graph`, `POST /api/warren/{id}/refresh`. The web client uses warrens +
graph + the refresh POST (daemon-aware refresh button already exists, `web/src/main.ts:199-210`);
`fetchBridges` (`api.ts:58`) has **no caller**. No management verb is reachable from the browser.

**(a) API additions — mount/unmount + workspace doctor, nothing else.**

```go
// internal/api/api.go
s.mux.HandleFunc("POST /api/warren/{id}/mount", s.handleWarrenMount)     // body: {"projects": ["a"], "all": false}
s.mux.HandleFunc("POST /api/warren/{id}/unmount", s.handleWarrenUnmount) // same body
s.mux.HandleFunc("GET /api/doctor/workspace", s.handleDoctorWorkspace)   // DoctorWorkspace report, verbatim JSON
```

Handlers are thin: validate the warren is registered (mirror `handleWarrenRefresh`'s check,
`handlers.go:956-969`), call `warren.Mount`/`warren.Unmount` (never `--materialize` over HTTP),
then `s.engine.ReloadWarrenState()` — refusals (self-alias materialize/editable from R1.2,
collisions, unknown projects) pass through as 400 with the warren error text, giving the UI the
same message quality as the CLI for free. **Explicitly not over HTTP:** register/unregister
(filesystem paths from a browser), burrow `--materialize`/`--drop` (heavy IO + cache lifecycle),
edit toggle (write-policy change; phase 2 at the earliest), propose and `refresh --pull` (git
operations — the refresh endpoint stays a state reload, and its "no pull over HTTP" gap is
documented, `handlers.go:950-978`).

**Prerequisite hardening:** `marmot ui` binds all interfaces —
`addr := fmt.Sprintf(":%d", port)` (`cmd/marmot/pipeline.go:632`). Mutating endpoints already
exist (`PUT /api/node`, `POST /api/chat`), but before *adding* workspace-state mutation, default
the bind to `127.0.0.1:%d` with a `--host` flag to opt out. One-line change + flag; e2e's
`get("/api/...")` calls localhost already (`e2e/e2e_test.go:1002+`).

**(b) UI: a warren panel, not more select-cramming.** Today warrens live behind a disabled
divider option in the namespace `<select>` labeled `Warren <id> (<n> active)`
(`main.ts:171-183`). Add a collapsible warren panel (project rows from `GET
/api/warren/{id}/status`, which carries `ProjectStatus` verbatim — incl. R1's `self_alias` and,
post-R2, identity rows): per row STATE/EDITABLE/AVAILABLE + `identity` badge, mount/unmount
buttons wired to (a) followed by the existing refresh POST + graph re-fetch; a doctor badge from
`GET /api/doctor/workspace` (error/warning counts); surface refresh/mount failures in the panel
instead of the current silent swallow (`main.ts:205-207` "Best-effort"). Detail-panel read-only
message (`detail-panel.ts:238-240`, button relabeled `Read-only Warren Node`) gains one line of
copy: `Enable writes: marmot warren edit <project> --warren <id>` — or, for `local_alias`
provenance (R1.5b), `This is your live vault — edit the unqualified node`.

**(c) Cleanups:** wire `fetchBridges` into the panel (bridge list per warren with
`is_cross_vault`) or delete it — decide by whether (b) ships a bridges row; keep both
`GET /api/warren/{id}` routes (removal breaks clients) but document `/{id}/status` as canonical
and fold the other into it with a comment. Render `skipped_reasons` (U4.4) as row tooltips.

**Tests:** `internal/api` — `TestWarrenMountEndpoint` (mount + reload observable via
`/api/warren/{id}/status`), `TestWarrenMountEndpointRefusalPassthrough` (400 + warren error text;
use the self-alias materialize refusal once R1 lands), `TestDoctorWorkspaceEndpoint`,
`TestUIBindsLoopbackByDefault`. e2e — `TestUIServer` gains a warren fixture + subtests for the
three endpoints. Web e2e — see Testing & Rollout (warren fixture is net-new for `web/e2e`).

## U6 — `project rename`: move the directory, keep the identity (rank 6; **independent**)

**Problem.** `RenameProject` (`internal/warren/warren.go:428-472`) rewrites the manifest project
ID and bridge endpoints, and `ensureProjectMetadata` (:2628-2641) sets `meta.ProjectID = newID` —
but the checkout stays at `projects/<oldID>/.marmot` (no `os.Rename` anywhere; `renamed.Path`
keeps the old path) and `meta.VaultID` keeps its old value. The CLI prints
`Renamed project %q -> %q` (`cmd/marmot/warren.go:506`) with no hint of either fact. Post-R1/R2
the vault_id half flips from bug to load-bearing: **vault_id is the identity key** (R2.0
invariant 7), so rename must *never* silently rewrite it — the gap is that nothing *says* so.

**Proposal.**

1. **Move the directory by default** when the project path is the conventional
   `projects/<oldID>/.marmot`: `os.Rename(projects/<oldID>, projects/<newID>)` + update
   `renamed.Path` in the same `updateManifest` transaction ordering as today (manifest write
   last, so a failed move leaves the manifest consistent). Refuse when `projects/<newID>` exists.
   When the path is unconventional (absolute, or not under `projects/<oldID>/`), skip the move
   and say so. `--keep-path` opts out explicitly.
2. **Say what happened to both the directory and the vault_id.** Before:
   ```
   Renamed project "api" -> "api-service"
   ```
   After:
   ```
   Renamed project "api" -> "api-service" (moved projects/api -> projects/api-service)
   note: vault_id "api-vault" unchanged — vault identity is stable across renames; re-import with --vault-id to change it
   ```
   (Second line only when `meta.VaultID != newID`, i.e. whenever the old default
   vault_id==project_id convention now visibly diverges.)
3. Git note in `docs/warrens.md:192` area: the move is a plain rename; `git add -A` in the warren
   repo records it (propose's pathspec is `projects/<pid>/`, `cmd/marmot/warren.go:1464-1491` —
   authors commit renames directly, so no propose interaction).

**Tests:** update the two pins of the old behavior —
`warren_test.go:94` `TestAuthoringInitAddRenameRemoveProjectPreservesBody` (:139 renames
api→api-service and asserts the fixture path stays put — now asserts the move) and
`warren_extra_test.go:397` `TestRenameProjectErrors`. New: `TestRenameMovesConventionalDir`,
`TestRenameKeepPath`, `TestRenameRefusesExistingTargetDir`, `TestRenameUnconventionalPathSkipsMove`,
CLI `TestWarrenProjectRenameOutput` (both output lines).

## U7 — Docs & paper-cut hygiene (rank 7; **independent**)

- `docs/current_limitations.md` gains a warren section (file predates warrens entirely): rename
  semantics (pre-U6), no HTTP management (pre-U5), no `refresh --pull` over HTTP, the
  `marmot bridge` path-heuristic system (below), mixed-binary workspace-state caveat (R2.2).
- **`marmot bridge <path>` vs warren bridges**: `cmdBridge`'s `looksLikeVaultPath` heuristic
  (`cmd/marmot/main.go:410-413`) is a parallel, older cross-vault bridge surface with different
  relation defaults. Reconciliation is **backlog** (real design work); this pass adds one
  paragraph to `docs/bridges.md` stating when to use which, and R1.7's self-bridge refusal
  already covers its worst self-injury.
- Carry-over backlog register (so nothing silently drops): `warren init --guided` (U1.3),
  HTTP edit/burrow/propose (U5a), bridges UI row (U5c), propose "publish live context to warren"
  leg (R2.5), `@self` write-time canonicalization (R1.4c), workspace-state versioning
  (prerequisite only if R2.1-B ever revives — R2.9 criterion 2).

**Tests:** none beyond doc-link checks; the backlog register is this document.

---

# Testing & Rollout

## Test matrix (consolidated: R1.10 + R2.10 + U items)

| Package / suite | R1 | R2 | U |
|---|---|---|---|
| `internal/warren` | R1.1 predicate/status, R1.2 mount/edit/materialize gating + unconditional refusal (rewrites `TestMountLocalVaultCollisionWarnsOnly` — the only behavior-pinning test that must change), R1.6 doctor codes | identity synthesis, R1-state dedupe, verb no-ops (R2.10 list) | U6 rename move/keep-path/refusals |
| `internal/mcp` | reload route-skip, bridge substitution, resolver alias, `@`-write guard (R1.3/R1.4) | bridge-without-mount (`TestReloadWarrenStateIdentityBridgeWithoutMount`) | — |
| `internal/traversal`, `internal/namespace` | resolver local short-circuit; seed-skip; self-bridge refusal (R1.4/R1.7) | — | U4.3 error-parity pin |
| `internal/api` | R1.5 a-d (self-alias refusal/provenance/graph/search) | identity-only reruns of R1.5 fixtures; `identified_projects` | U5 mount/unmount/doctor endpoints, loopback bind; U4.2/U4.4 message + `skipped_reasons` pins |
| `internal/daemon` | watcher sibling: self-mount adds no route (R1.3) | — | — |
| `cmd/marmot` | buildEngine live-resolution siblings, doctor CLI companion (R1.3/R1.6) | register announcement, status/list identity display, propose refusal, refresh legacy-cache skip | U1 nudge, U2 deprecation warnings + ambiguity refusal, U3 doctor summary, U4 string pins, U6 rename output |
| `e2e` (Go, `//go:build e2e`) | `TestWarrenSelfMountAlias` (R1.9, 8 steps incl. daemon leg) | extends into `TestWarrenLocalIdentity` (R2.10: no self-mount, migration leg, no-op mount note) | U3.2 stream-separation assertion; U5 `TestUIServer` warren subtests |
| `web/e2e` (Playwright) | — (nothing pins current warren UI behavior either) | — | **net-new warren fixture** (below) + specs |

Re-run-unchanged set (regression sentinels, no edits): `internal/mcp/warren_reload_test.go:124-323`,
`cmd/marmot/surface_coverage_test.go:513-1090` (until U2/U4 update them deliberately),
`cmd/marmot/warren_ux_test.go:63-398`, all existing warren e2e, `internal/api` warren tests with
non-colliding IDs (R1.5 list).

## e2e additions

1. **Go e2e:** R1.9's `TestWarrenSelfMountAlias` lands with R1; R2 converts it to
   `TestWarrenLocalIdentity` (delta documented in R2.10 — keep the unit-level bridge tests as the
   pair proving the mount-vs-no-mount delta). U5 extends `TestUIServer` (`e2e/e2e_test.go:997-1109`,
   currently four endpoints, zero warren) with a warren fixture and subtests: mount via POST →
   status shows active → graph serves nodes → unmount → doctor endpoint shape.
2. **Web e2e (net-new coverage):** `web/e2e` has zero warren coverage and `serve.sh` runs a
   single-vault fixture (`cp -R e2e/fixture/vault …`). Add a warren fixture leg to `serve.sh`
   (author a two-project warren + register + mount one project via the CLI it already builds)
   and specs for: warren entry in the selector, refresh button POST (daemon-aware path,
   `main.ts:199-210`), provenance panel rendering (`warren_mount` and, post-R1, `local_alias`),
   read-only save gating (`detail-panel.ts:221-253`), live-vs-snapshot node content through a
   self-alias/identity bridge, and (post-U5b) the panel's mount/unmount buttons. CI: the
   Playwright leg already runs on ubuntu only (`.github/workflows/ci.yml:78-123`, `make e2e-ui`)
   — the fixture must stay hermetic (HOME-scoped, no network) like the Go suite.

## PR sequencing under the auto-release constraint

**The constraint, verified:** `auto-tag.yml` triggers on CI `workflow_run` success for every push
to `main` and runs goreleaser — **every merged PR is a public release within minutes**. Two
consequences bind the sequencing: (i) every PR must be user-complete (no half-features, no
dead flags, release-notes-worthy on its own); (ii) deprecations start warning in the very release
that introduces them, and nothing in this plan removes a spelling or route.

| # | PR | Contents | Gate |
|---|---|---|---|
| 1 | `warren: self-mount aliasing (R1)` | R1.1–R1.9 as one PR, five commits (packaging note) | none |
| 2 | `warren: rename moves the project directory (U6)` | U6 | independent; may run in parallel with #1 review (different functions, low conflict) |
| 3 | `warren: flag deprecation warnings (U2)` | U2 | after #1 *merges* (both edit `cmd/marmot/warren.go` broadly; rebase cost, not correctness) |
| 4 | `warren: first-class local identity (R2)` | R2.0–R2.10 | after #1 merged **and** R2.9 defer-if criteria pass |
| 5 | `warren: docs & limitations hygiene (U7)` | U7 | independent; anytime after #1 (limitations text references R1 semantics) |
| 6 | `warren: output ergonomics + error sweep (U3 + U4)` | U3.2-3, U4.1-6, R2.6 display polish | after #4 (sweeps R1+R2 strings; identity columns need R2) |
| 7 | `api/ui: warren management endpoints (U5a)` | mount/unmount/doctor endpoints + loopback bind | after #1 (reuses R1 refusals); before #8 |
| 8 | `web: warren panel (U5b/c) + web e2e warren fixture` | panel, badges, cleanups, Playwright specs | after #7; identity badge needs #4 |
| 9 | `docs: warren quickstart (U1)` | U1.0 configure vault_id + U1.1 (+ U1.2 nudge if not folded into #4) | last — documents the final surface (U1.0 may land any time earlier; it is independent) |

If R2 defers (R2.9 criteria): #4 drops out, #6 sweeps R1 strings only and skips identity columns,
#8 ships without the identity badge, #9's quickstart documents the R1 alias-mount flow — every
other PR is unchanged (that is what "independent" above means).

**Rollback story:** auto-released versions are immutable; a bad release is superseded by
reverting the PR on main (next auto-tag). R1/R2 write no new state (R1.7, R2.2), so a downgrade
after any of these releases loses no data; the single asymmetry (R2 cleanup → R1 binary loses
bridge activation) is documented in R2.2 and gated by doctor's suggest-don't-autofix stance.

---

# Risks & Mitigations

R1-specific risks live in the [R1 packaging note](#r1-packaging-note-single-pr); R2's are its
[defer-if criteria](#r29--shipdefer-recommendation). Cross-cutting and U-workstream risks:

- **Auto-release exposes every intermediate state.** *Mitigation:* the PR table above is the
  contract — no PR merges without its tests, docs, and release-notes line; R1 is deliberately one
  PR because its pieces are mutually dependent (packaging note).
- **U2 deprecation warnings break scripted users' stderr parsing.** *Mitigation:* warnings are
  one line, stderr-only, fire only when a legacy spelling is actually used; no spelling is
  removed; `surface_coverage_test` keeps pinning that legacy spellings still *work*.
- **U4/U2 string churn invalidates test pins en masse** (`surface_coverage_test.go`,
  `warren_ux_test.go` pin exact texts). *Mitigation:* every string change and its pin update land
  in the same commit, grep-driven (`grep -rn '<old literal>'` before each edit); PR #6 exists so
  the churn happens once, after R1/R2 strings settle.
- **U5a puts workspace-state mutation on an HTTP listener bound to all interfaces.**
  *Mitigation:* loopback-by-default bind ships in the same PR as the endpoints (prerequisite,
  U5a); endpoints are POST-only, validate registration, and reuse the warren layer's flock'd
  state writes — no new write primitive.
- **U6's directory move can strand a warren repo half-renamed on crash.** *Mitigation:* move
  first, manifest write last (single `updateManifest` commit point); refuse existing targets;
  `--keep-path` escape hatch; `warren doctor`'s existing `project_missing`/`project_id_mismatch`
  codes detect any torn state.
- **Web e2e warren fixture flakes CI** (git + daemon + Playwright in one leg). *Mitigation:*
  fixture is CLI-seeded and hermetic like the Go suite (HOME-scoped); warren specs are a separate
  Playwright project so a flake quarantines without blocking the vault specs.
- **R2 defers and R2-dependent U items were built against it.** *Mitigation:* every U item
  carries its dependency marker; the sequencing table's defer branch reassigns #6/#8/#9 content
  explicitly — nothing is orphaned.
- **Terminology drift across three workstreams.** *Mitigation:* fixed glossary — **self-alias**
  (R1: a *mounted* project serving as the live vault), **identified project** (R2: same predicate,
  no mount), **identity** (the derived relationship; never a stored field), **legacy self-mount**
  (pre-R1 state). This document uses only these four; docs edits (R1.8, R2.10, U1, U7) inherit
  them.
