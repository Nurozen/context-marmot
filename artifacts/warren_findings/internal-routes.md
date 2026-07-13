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
