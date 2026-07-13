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
