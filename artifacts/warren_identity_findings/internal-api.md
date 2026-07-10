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
