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
