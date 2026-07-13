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
