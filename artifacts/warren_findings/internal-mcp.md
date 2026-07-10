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
