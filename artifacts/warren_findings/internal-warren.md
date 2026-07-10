# internal/warren

Folder = 1 source file (`warren.go`, 1829 lines) + 2 test files (`warren_test.go` 794, `warren_extra_test.go` 774). No DB/embedding code here — all `embedding.NewStore` opens (Tier 1.1) happen in `internal/namespace` / consumers, NOT this package. This package never opens SQLite; copies are byte-level.

## internal/warren/warren.go

### warren.go:1289-1315 — copyDir (Tier 1.2, 1.3)
Unfiltered copier used only by `Materialize` (burrow). Confirms review 1.3: no `IsRegular` check, copies `.env`/WAL sidecars, `d.IsDir()` false for a symlink-to-dir → `copyRegularFile` on it (io.Copy of dir open fails / symlink-to-file is followed), passes full `info.Mode()` (not `.Perm()`) to `os.OpenFile`, never clears stale target files on re-burrow.
```go
1289	func copyDir(source, target string) error {
...
1306			if d.IsDir() {
1307				return os.MkdirAll(dest, 0o755)
1308			}
1309			info, err := d.Info()
...
1313			return copyRegularFile(path, dest, info.Mode())
```

### warren.go:1317-1391 — importAlwaysExcluded + copyMarmotVault (Tier 1.2, 1.3 template)
The hardened copier to share: skip map at 1317-1323 (`.marmot-data/.env`, `-wal`, `-shm`, obsidian workspace files), `!info.Mode().IsRegular()` skip at 1352, `_config.md` sanitization at 1359, `.Perm()` at 1364/1366. No WAL checkpoint anywhere — review 1.2 confirmed for both import and burrow. Signature: `func copyMarmotVault(source, target string, opts ImportOptions) error`.

### warren.go:183-308 — ImportProject (Tier 1.2, 1.5)
`func ImportProject(root, sourceMarmotDir string, project Project, opts ImportOptions) (*Manifest, error)`. Copy happens via `copyMarmotVault(source, tmp, opts)` at :282 into a temp dir then `os.Rename` (:294) — checkpoint hook belongs before :282. Manifest RMW is Load(:184)→Save(:299) unlocked. Note review cited "1317-1368" for the import copy; the actual copy loop is 1325-1368 (line drift only, semantics correct).

### warren.go:972-980 — Materialize (Tier 1.2, 1.3, Tier 4.2 provenance)
```go
973	func Materialize(workspaceMarmotDir, warrenID string, project Project, warrenRoot string) (string, error) {
974		source := filepath.Join(warrenRoot, filepath.FromSlash(project.Path))
975		target := materializedProjectPath(workspaceMarmotDir, warrenID, project.ProjectID)
976		if err := copyDir(source, target); err != nil {
```
Natural place to write commit/mtime provenance (Tier 4.2) and clear target first. Sole caller: `cmd/marmot/warren.go:747`.

### warren.go:1065-1077 — updateWorkspaceState (Tier 1.5 flock point)
Confirmed unlocked Load→fn→Save; single choke point — flocking here covers `RegisterWorkspaceWarren` (:798-816), `Mount` (:819-844), `SetEditable` (:847-872).
```go
1065	func updateWorkspaceState(workspaceRoot string, fn func(*WorkspaceState) error) (*WorkspaceState, error) {
1066		state, body, err := LoadWorkspaceState(workspaceRoot)
...
1073		if err := SaveWorkspaceState(workspaceRoot, state, body); err != nil {
```
Manifest-side Load→Save pairs needing the same flock (all in this file): `Init` 131/140, `AddProject` 148/173, `ImportProject` 184/299, `RemoveProject` 327/351, `RenameProject` 368/395, `AddBridge` 406/432, `RemoveBridge` 464/488, `Format` 607/611. Lock key differs: manifest ops lock the warren root, workspace ops lock the workspace `.marmot`.

### warren.go:819-872 — Mount / SetEditable (Tier 1.4, 3)
`func Mount(workspaceRoot, warrenID string, projects []string, materialized bool)` — only ever adds (`addName` :836) and only sets `Materialized = true` (:838-840); confirms "no unmount / un-burrow" (Tier 3). `SetEditable` auto-mounts at :863 (`entry.ActiveProjects = addName(...)`) — review's :863 cite exact. Nothing forbids editable+materialized (Tier 1.4); the enforcement point for "refuse editable on materialized" is here (entry.Materialized is in scope).

### warren.go:920-970 — ActiveMounts (Tier 1.6, 2)
`func ActiveMounts(marmotDir string) ([]ProjectStatus, error)`. Swallow is at :928-933 (review said 929-934; off by one, same code): manifest load error → materialized fallback or silent `continue`, no warning. Also silent `LoadProjectMetadata` drops at :895, :945, :986 — all three cites exact. Consumers (must not break signature): `cmd/marmot/pipeline.go:242` (buildEngine wiring — the Tier 2 reloadWarrenState extraction source), `internal/api/handlers.go:545,841,947`. `findWarrenMountByVault` lives at `internal/api/handlers.go:946-`; its first-match-wins over this sorted slice + `preferredProjectPath` (:1011-1019, returns burrow cache when materialized) is the Tier 1.4 write-loss mechanism.

### warren.go:1110-1125 — parseMarkdownYAML (Tier 1.7)
Confirmed exactly as reviewed: `strings.Index(content[3:], "---")` matches `---` anywhere (even mid-line/mid-value), and `content[end+6:]` assumes the delimiter is exactly `---` with nothing else on the line. Callers: LoadManifest :657, LoadProjectMetadata :705, loadWorkspaceStatePath :758, sanitizedConfigBytes :1422, sourceVaultID :1514 — one fix covers all five. Writer counterpart `writeMarkdownYAML` (:1127-1166) emits `---\n` at line start, so anchored parsing (`\n---\n` / `^---$`) round-trips.

### warren.go:1127-1166 — writeMarkdownYAML (Tier 1.5 context)
Already atomic per-write (unique `os.CreateTemp` + rename), so the race is purely RMW-level, not torn writes. Note `routes.Update`'s fixed `.tmp` name problem does NOT exist here.

### warren.go:563-575 — Doctor duplicate_vault_id (Tier 3 collision)
Confirmed: collision detection is per-manifest only (`vaultIDs` map local to one warren, :502). Cross-warren detection needs workspace-level logic (ActiveMounts consumers or mount time), not this function. `func Doctor(root string) (DoctorReport, error)` — Tier 4.5 model-skew check would also live here (it already stats `embeddings.db` at :576 but never opens it; adding a model check means this package grows an embedding dep or the check moves to the caller).

### warren.go:875-918 — Status (Tier 3 unreachable-warren surfacing)
On manifest load failure with `entry.Materialized` false it returns the raw error (:889) — "warren X unreachable at <path>" messaging hooks here and in ActiveMounts.

### warren.go:1172-1183 — normalizeManifest version handling (Tier 4.6)
`if m.Version == 0 { m.Version = 1 }` — no upper-bound check anywhere; confirmed struct round-trip drops unknown YAML fields (plain `yaml.Unmarshal` into fixed structs).

## internal/warren/warren_test.go

### warren_test.go:722-772 — writeImportSourceVault helper (test program)
Reusable hermetic vault fixture: builds a `.marmot` with `_config.md` (incl. secret keys to assert sanitization), fake byte-file `embeddings.db` + `-wal`/`-shm`, `.env`, `_heat/`, `.obsidian/`. Confirms review: import tests use fake byte files, so a real-SQLite checkpoint test is genuinely missing. `mustNotExist` at :774.

### warren_test.go:318-341 — symlink-rejection test template
Pattern (symlink + `t.Skipf` when unsupported + assert manifest unmutated) is the template for the copyDir symlink/FIFO cases.

### Review error — Tier 1.8 cite "warren_test.go:329"
`internal/warren/warren_test.go:329` is inside `TestImportProjectRejectsSymlinkedDestinationParent` and never calls `buildEngine` or touches routes; this package's tests are already hermetic (pure t.TempDir, no env). The real offenders are `cmd/marmot/warren_test.go:380` and `cmd/marmot/pipeline_warren_test.go:44` (both call `buildEngine` with no `MARMOT_ROUTES` isolation — grep confirms zero `MARMOT_ROUTES`/`Setenv` in either file). The 1.8 fix belongs in cmd/marmot, not here.

## Constraints shaping fixes

- Package has no embedding/SQLite import today; adding `Store.Checkpoint()` calls for 1.2 either imports `internal/embedding` here (new dep, likely fine) or takes a `checkpoint func(path string) error` hook from callers.
- Exported API used by cmd/marmot + internal/api: `ActiveMounts`, `Status`, `Mount`, `SetEditable`, `Materialize`, `ImportProject`, `LoadWorkspaceState(FromMarmot)`, `RegisterWorkspaceWarren`, `Load/SaveManifest`, `Doctor` — signatures above; Tier 3 verbs (Unmount/Unregister/Dematerialize) are additive, no breakage.
- Flock reuse target: `internal/daemon/lock_unix.go:16 tryFlock(f *os.File)` (unix.Flock LOCK_EX|LOCK_NB) — currently unexported and non-blocking-only; RMW locking wants a blocking or retry variant, so either export a blocking helper from daemon or add a small lockfile util here. Exec-helper kill test template: `internal/daemon/lock_test.go:106-178`.
- `ProjectStatus.Path` semantics (checkout vs burrow cache, decided in `preferredProjectPath` :1011-1019) are load-bearing for `findWarrenMountByVault` and buildEngine routing — the 1.4 fix ("prefer checkout for editable") changes this one function.
