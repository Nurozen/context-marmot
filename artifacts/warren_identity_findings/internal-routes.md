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
