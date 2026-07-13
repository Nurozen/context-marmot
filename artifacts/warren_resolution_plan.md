# Warren Resolution Plan

Branch `multiprocess-lock-fix`, commit `1f14f3e`. Source inventory: `artifacts/warren_review.md`
(four tiers + test program); per-folder ground truth with verified line refs:
`artifacts/warren_findings.md`. All line numbers below were re-verified against the working tree
where load-bearing.

**Context.** Warren state lives in three files that all share the name `_warren.md` — warren-repo
manifest (`<warrenRoot>/_warren.md`), per-project identity (`<projectMarmotDir>/_warren.md`), and
workspace mount state (`<workspaceRoot>/.marmot/_warren.md`, see
`internal/warren/warren.go:1093-1097`). It enters the engine once in `buildEngine`
(`cmd/marmot/pipeline.go:236-274`). The Tier 1 defects are pure correctness bugs independent of the
daemon-era freshness redesign: cross-vault *reads* mutate remote vault DBs, live-DB copies tear,
the burrow copier leaks secrets and follows symlinks, editable+materialized mounts lose writes,
every `_warren.md` mutation is an unlocked read-modify-write, a family of swallowed errors hides
all of the above, the frontmatter parser corrupts on `---` in content, and two test files scan the
developer's real vaults.

## Document map

- **[Workstream A: Correctness (Tier 1)](#workstream-a-correctness-tier-1)** — items A1–A8 map
  1:1 to review items 1.1–1.8. Ships as **one PR** (see packaging note at the end of Workstream A).
- **[Workstream B: Daemon-era freshness (Tier 2)](#workstream-b-daemon-era-freshness-tier-2)** — PR 2.
- **[Workstream C: UX repairs (Tier 3)](#workstream-c-ux-repairs-tier-3)** — PR 3.
- **[Workstream D: Capability roadmap (Tier 4)](#workstream-d-capability-roadmap-tier-4)** —
  optional scope; items D1–D6 each carry explicit **defer-if** criteria and ship as individual
  PRs 4a–4c.
- **[Testing & Rollout](#testing--rollout)** — consolidated test matrix, the first-ever warren e2e
  scenarios, CI implications, PR sequencing under the auto-release-on-main constraint.
- **[Risks & Mitigations](#risks--mitigations)** — program-wide risk table.

---

# Workstream A: Correctness (Tier 1)

Recommended commit order inside the single PR (each commit compiles and passes tests on its own):

1. **A8** — test hermeticity (`MARMOT_ROUTES=off`) so every later test run is safe on a dev machine.
2. **A7** — frontmatter parser (foundational: A5/A6 tests round-trip `_warren.md` files).
3. **A5** — flock helper package (infrastructure A2/A4 refusal tests and later workstreams reuse).
4. **A1** — `NewStoreReadOnly` + registry switch.
5. **A2** — `Store.Checkpoint()` + checkpoint-before-copy.
6. **A3** — shared hardened copier for burrow.
7. **A4** — editable+materialized refusal.
8. **A6** — error un-swallowing (last: earlier commits change some of these sites anyway).

---

## A1 — Read-only opens for remote vault DBs (review 1.1)

**Defect.** `embedding.NewStore` (`internal/embedding/store.go:47-70`) unconditionally runs
`PRAGMA journal_mode = WAL` (:59) and `initSchema` (:65 → CREATE TABLE + blind
`ALTER TABLE ... ADD COLUMN status` at :73-88). `VaultRegistry.ResolveEmbeddingStore`
(`internal/namespace/registry.go:186-241`) opens *remote* vault DBs through it (:223), so a
read-only cross-vault search flips the remote DB's journal mode, creates `-wal`/`-shm` sidecars,
and can migrate schema inside the mounted warren git checkout.

**Design: `embedding.NewStoreReadOnly`.** In `internal/embedding/store.go`:

```go
// Store gains one field:
type Store struct {
    db       *sqlite3.Conn
    mu       sync.Mutex
    readOnly bool
}

// NewStoreReadOnly opens an existing embeddings DB without mutating it:
// no WAL pragma, no schema init, no migration. busy_timeout is kept so
// reads retry politely against a concurrent writer.
func NewStoreReadOnly(dbPath string) (*Store, error) {
    db, err := sqlite3.OpenFlags(dbPath, sqlite3.OPEN_READONLY)
    if err != nil {
        return nil, fmt.Errorf("open sqlite read-only: %w", err)
    }
    if err := db.BusyTimeout(5 * time.Second); err != nil {
        _ = db.Close()
        return nil, fmt.Errorf("set busy_timeout: %w", err)
    }
    return &Store{db: db, readOnly: true}, nil
}
```

Design decisions (grounded in findings, `internal-embedding` section):

- **Open flags:** `sqlite3.OpenFlags(path, sqlite3.OPEN_READONLY)` — the idiomatic ncruces
  go-sqlite3 v0.33.2 call; equivalent to `file:...?mode=ro` but no URI-escaping pitfalls. Do NOT
  pass `OPEN_CREATE` — a missing remote DB must be an error, not an empty file in someone's
  checkout.
- **Pragmas:** `busy_timeout` only. `journal_mode = WAL` on a read-only conn fails if the DB is not
  already WAL, and `initSchema`'s DDL fails with `SQLITE_READONLY` — both are skipped, not
  best-effort'd.
- **Note:** read-only open of an already-WAL DB still needs `-shm` create permission; remote vaults
  are local git checkouts so this holds. Record the caveat in the function doc for future network
  mounts (`immutable=1` escape hatch).
- **Write-method guards:** `Upsert` (:143), `UpdateStatus` (:302), `Delete` gain a leading
  `if s.readOnly { return fmt.Errorf("embedding store %s opened read-only", ...) }` so a misuse
  fails with a clear error instead of a raw `SQLITE_READONLY` at Exec time. All read methods
  (`Search` :201, `SearchActive` :326, `FindSimilar` :403, `StaleCheck` :508, `Count`,
  `StoredDimension`) work unchanged — same `*Store` type, `mu` discipline untouched. Add
  `Checkpoint()` guard too (see A2: no-op or error on read-only stores; choose error — callers
  never checkpoint an RO store).

**Call sites to switch — exactly one:**

- `internal/namespace/registry.go:223` (`ResolveEmbeddingStore`) → `embedding.NewStoreReadOnly(dbPath)`.
  Registry never writes remote stores today (verified: no Upsert/write call on `rv.EmbStore`
  anywhere in the package), so nothing regresses. The lazy `loadVaultLocked` call at :232 is
  graph-side and unaffected.

**Call sites that must NOT change** (audit list; add a code comment at each so a future sweep
doesn't "fix" them):

- `internal/api/handlers.go:450` (`handleWarrenNodeUpdate`) — intentional **write** open of an
  editable mount's DB. This is the second remote open the review missed; it stays `NewStore`
  because the editable write path is a legitimate write. (Its swallowed errors are A6's problem.)
- `internal/mcp/engine.go:160` — local vault store, read-write.
- `cmd/marmot/pipeline.go:75, 986, 1083, 1287` — local index/status/watch/static-index, read-write.
- `cmd/marmot/warren_test.go:363` — test fixture seeding, read-write.

**Tests** (in `internal/embedding` and `internal/namespace`):

1. `TestNewStoreReadOnly_DoesNotMutate` — build a DB with `NewStore` in **DELETE** journal mode
   is impossible (NewStore forces WAL), so instead: create the DB file with `NewStore`, `Close`,
   checkpoint away sidecars, record file bytes + assert no `-wal`/`-shm`; open with
   `NewStoreReadOnly`, run `SearchActive`; assert no `-wal`/`-shm` sidecars appear and
   `journal_mode` was not rewritten. Reuse `journalMode(t, s)` helper
   (`internal/embedding/store_wal_test.go:12-24`) and invert `TestNewStore_WALEnabled` (:26-64).
2. `TestNewStoreReadOnly_WritesRejected` — `Upsert`/`UpdateStatus` return the clear read-only error.
3. `TestNewStoreReadOnly_MissingFile` — errors instead of creating a file.
4. Registry regression: extend `internal/namespace/registry_embstore_test.go` using its
   `setupRemoteVault` fixture (:11-23) — seed a real remote `embeddings.db` (template:
   `internal/api/api_test.go:173-189 seedRemoteEmbedding`), resolve via
   `ResolveEmbeddingStore`, search, then assert the remote `.marmot-data/` has no new sidecars and
   the schema was not migrated (`SELECT` the column list, or just mtime/byte-compare the db file).

**Risks.** A remote vault indexed by an *old* marmot (pre-`status` column) will now fail
`SearchActive` (which references `status`) instead of being silently migrated. Mitigation: the
read-only open cannot migrate by definition; surface the error per A6 (it currently would be
swallowed at `internal/mcp/handlers.go:82`), and let Workstream D's doctor flag stale-schema
remotes. This is strictly better than mutating someone else's checkout.

---

## A2 — Checkpoint before copying a live DB (review 1.2)

**Defect.** `ImportProject` copies via `copyMarmotVault` (`internal/warren/warren.go:282`) and
`Materialize` via `copyDir` (:976); both skip `-wal`/`-shm` sidecars
(`importAlwaysExcluded`, :1317-1323 — and burrow copies them, A3) but never checkpoint, so copying
a WAL DB that any process has open snapshots stale or torn data. Docs (`docs/warrens.md:114-120`)
already promise sidecar exclusion — without a checkpoint that promise *loses* un-checkpointed
writes. No `wal_checkpoint` exists anywhere in the repo today.

**Step 1 — `Store.Checkpoint()`** in `internal/embedding/store.go`:

```go
// Checkpoint flushes the WAL into the main database file
// (PRAGMA wal_checkpoint(TRUNCATE)) so a byte-level copy of
// embeddings.db alone is complete and consistent.
func (s *Store) Checkpoint() error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.readOnly {
        return fmt.Errorf("checkpoint on read-only store")
    }
    return s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
}
```

`TRUNCATE` (not `PASSIVE`) so the `-wal` file is zeroed; with the vault's 5s busy_timeout a
concurrent reader makes this block briefly rather than fail. A checkpoint that cannot complete
(persistent reader) returns SQLITE_BUSY after the timeout — handled below.

**Step 2 — warren-side helper + wiring** in `internal/warren/warren.go`. `internal/warren`
currently has no embedding/SQLite import; adding `internal/embedding` is a new but acyclic
dependency (embedding imports nothing from warren) — take it, it is simpler than threading a
`checkpoint func(path string) error` hook through two exported APIs.

```go
// checkpointEmbeddings best-effort flushes the WAL of the source vault's
// embeddings DB so the sidecar-excluding copy below is complete. Failure
// degrades to documented point-in-time semantics (the last checkpoint),
// with a stderr warning.
func checkpointEmbeddings(sourceMarmotDir string) {
    dbPath := filepath.Join(sourceMarmotDir, ".marmot-data", "embeddings.db")
    if _, err := os.Stat(dbPath); err != nil {
        return // no DB, nothing to flush
    }
    st, err := embedding.NewStore(dbPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "warning: cannot open %s to checkpoint before copy: %v; copying last-checkpointed state\n", dbPath, err)
        return
    }
    defer st.Close()
    if err := st.Checkpoint(); err != nil {
        fmt.Fprintf(os.Stderr, "warning: wal_checkpoint failed for %s: %v; copying last-checkpointed state\n", dbPath, err)
    }
}
```

Call sites:

- `ImportProject` (:183-308): call `checkpointEmbeddings(sourceMarmotDir)` immediately before the
  `copyMarmotVault(source, tmp, opts)` call at :282 (source is the project's `.marmot`).
- `Materialize` (:972-980): call `checkpointEmbeddings(source)` before the copy at :976, where
  `source = filepath.Join(warrenRoot, filepath.FromSlash(project.Path))` — note the burrow source's
  embeddings DB lives at `<source>/.marmot-data/embeddings.db` because the warren stores whole
  `.marmot` trees; verify the exact relative layout when implementing (the skip-map keys
  `.marmot-data/...` confirm data dir is directly under the copied root).

Opening with `NewStore` (read-write) is deliberate: a checkpoint is a write-side operation. Since
`NewStore` also WAL-flips + migrates, only call it when the DB file already exists (the `Stat`
guard) — for import that DB is the user's own project vault, already WAL from normal operation;
for burrow it is inside the warren checkout which was itself produced by import (main-db-only), so
checkpointing there is a near-no-op but harmless and correct if a consumer has since written to it.
If we want burrow to never write the checkout at all, alternative: attempt
`NewStoreReadOnly` + skip checkpoint when the `-wal` file is absent; keep the RW checkpoint only
when a `-wal` sidecar exists. **Adopt that refinement:** only run `checkpointEmbeddings` when
`dbPath + "-wal"` exists and is non-empty — no WAL means nothing to flush and no reason to touch
the file.

**Tests** (in `internal/warren`, new file `warren_checkpoint_test.go`):

1. `TestImportProjectCheckpointsWAL` — real SQLite (current tests use fake byte files, per
   findings): build a source vault, `embedding.NewStore` + `Upsert` several rows, **do not close**
   (hold the conn so the WAL stays hot), run `ImportProject`, then open the imported copy's
   `embeddings.db` and assert `Count`/`SearchActive` sees all rows despite `-wal`/`-shm` exclusion.
2. `TestMaterializeCheckpointsWAL` — same shape through `Materialize`.
3. `TestCheckpointHelperNoDB` — source without an embeddings DB: no error, no file created.
4. `embedding.TestCheckpointTruncatesWAL` — Upsert until `-wal` non-empty, `Checkpoint()`, assert
   `-wal` size 0 and rows readable from a fresh main-file-only copy.

**Risks.** Checkpoint blocks up to 5s under a persistent concurrent reader → import/burrow gains
worst-case latency; acceptable for rare operations. A checkpoint racing a writer that keeps writing
after the flush still yields point-in-time semantics — that is the documented contract
(`docs/warrens.md`), now actually true instead of silently worse.

---

## A3 — One hardened copier for burrow and import (review 1.3)

**Defect.** `copyDir` (`warren.go:1289-1315`, sole caller `Materialize` :976) has none of
`copyMarmotVault`'s (:1325-1368) protections: it copies `.marmot-data/.env` (secrets) and WAL
sidecars, has no `IsRegular` check (a FIFO hangs the burrow in `io.Copy`; `d.IsDir()` is false for
a symlink-to-dir so symlinks are followed/mis-copied), passes full `info.Mode()` instead of
`.Perm()` (:1313), and never clears stale files on re-burrow — deleted nodes resurrect.

**Design.** Extract the walk skeleton from `copyMarmotVault` into one shared filtered walker and
express both copiers through it:

```go
// copyFilteredTree walks source and copies regular files to target.
// Symlinks are never followed (skipped with a stderr note), irregular
// files (FIFOs, sockets, devices) are skipped, directories are created
// 0o755, and file permissions are copied via Mode().Perm().
// skipDir/skipFile receive slash-separated paths relative to source.
func copyFilteredTree(source, target string,
    skipDir func(relSlash string) bool,
    skipFile func(relSlash string) bool,
    transformFile func(relSlash, srcPath string, perm os.FileMode) (handled bool, err error), // nil ok
) error
```

Walker body rules (all lifted from `copyMarmotVault`, two additions):

1. `d.Type()&fs.ModeSymlink != 0` → skip, `fmt.Fprintf(os.Stderr, "warning: skipping symlink %s\n", rel)`.
   (**Addition** — `copyMarmotVault`'s `IsRegular` check already excludes symlinks silently; keep
   the skip but make it audible for burrow, matching the import symlink-rejection tests' spirit.)
2. `d.IsDir()` → `os.MkdirAll(dest, 0o755)`, honoring `skipDir` → `filepath.SkipDir`.
3. `!info.Mode().IsRegular()` → skip (covers FIFO/socket/device).
4. Copy with `info.Mode().Perm()` (never full mode bits — no setuid/sticky propagation).
5. `transformFile` hook carries import's `_config.md` sanitization (`sanitizedConfigBytes` :1360).

Then:

- `copyMarmotVault(source, target, opts)` becomes a thin wrapper:
  `skipDir = shouldSkipImportDir(·, opts)`, `skipFile = shouldSkipImportFile(·, opts)`,
  `transformFile` handles `_config.md`. Behavior identical (existing import tests are the guard).
- `Materialize`'s copy becomes `copyFilteredTree(source, target, skipBurrowDir, skipBurrowFile, nil)`
  where `skipBurrowFile(rel) = importAlwaysExcluded[rel]` (`.env`, `-wal`, `-shm`, obsidian
  workspace files — the checkout was sanitized at import time, so no `_config.md` transform is
  needed; re-sanitizing is harmless but skip it to keep burrow byte-faithful) and
  `skipBurrowDir` skips nothing extra. Delete the old `copyDir`.

**Re-burrow staleness + atomicity.** In `Materialize` (:972-980), replace copy-in-place with
copy-aside-then-swap so a failed burrow never leaves a half-written cache and removed files never
survive:

```go
target := materializedProjectPath(...)
tmp := target + ".tmp"
_ = os.RemoveAll(tmp)
if err := copyFilteredTree(source, tmp, ...); err != nil {
    _ = os.RemoveAll(tmp)
    return "", err
}
if err := os.RemoveAll(target); err != nil { ... }
if err := os.Rename(tmp, target); err != nil { ... }
```

(`tmp` is a sibling under the same `.marmot-data/warrens/...` parent, so the rename is same-fs.
The A2 checkpoint call happens before the copy, on `source`.)

**Tests** (extend `internal/warren/warren_extra_test.go`, reuse the symlink test template at
`warren_test.go:318-341` with its `t.Skipf`-when-unsupported pattern):

1. `TestMaterializeSkipsSecretsAndSidecars` — source with `.marmot-data/.env`,
   `embeddings.db-wal`, `embeddings.db-shm`; burrow; `mustNotExist` (helper at `warren_test.go:774`)
   each in the cache.
2. `TestMaterializeSkipsSymlinkAndFIFO` — symlink-to-file, symlink-to-dir, and a
   `syscall.Mkfifo` FIFO (unix-gated) in source; burrow completes (no hang) and copies none of them.
3. `TestMaterializeClearsStaleFiles` — burrow, delete a node file in source, re-burrow, assert the
   node is gone from the cache; also assert an interrupted copy (inject failure via an unreadable
   file) leaves the previous cache intact.
4. `TestMaterializePermPreserved` — 0o600 source file arrives 0o600, and a source file with setuid
   bit arrives without it.
5. Existing import tests must pass unchanged (wrapper equivalence).

**Risks.** `os.RemoveAll(target)` on re-burrow deletes a cache a live engine may have routed to
(routing table points at cache paths). Mitigation: the swap is rename-based so the window is the
rename syscall, and readers hold file handles, not paths, for the DB; the graph loader re-reads
per-request in API paths. Workstream B's refresh semantics formalize this; note it in the PR.

---

## A4 — Refuse editable + materialized (review 1.4)

**Defect.** `SetEditable` (`warren.go:847-872`) auto-mounts and sets the editable flag with no
check of `entry.Materialized`; `Mount(workspaceRoot, warrenID, projects, materialized)`
(:819-844) sets `Materialized` with no check of editable. `preferredProjectPath` (:1011-1019)
returns the burrow-cache path whenever materialized, so `findWarrenMountByVault`
(`internal/api/handlers.go:946-957`) hands the node-update handler (:396-468) a cache path — edits
land in the cache, nothing syncs back, and `warren propose` tells the user to commit a checkout
that never received them. Documented behavior (`docs/warrens.md:277-278`) promises editable writes
go to the project's own vault — this is a contract violation, not just a foot-gun.

**Fix points (minimal, per review):**

1. `SetEditable` (`warren.go:847-872`): when enabling, inside the `updateWorkspaceState` closure,
   if `entry.Materialized` is set **and** a burrow cache exists for this project
   (`dirExists(materializedProjectPath(workspaceMarmotDir(workspaceRoot), warrenID, projectID))` —
   `Materialized` is a warren-level bool, `warren.go:64`, so the per-project ground truth is the
   cache dir, exactly as `ActiveMounts` computes it at :958):
   `return fmt.Errorf("project %q in warren %q is materialized (burrowed); a materialized cache never syncs edits back — drop the burrow first (see 'marmot warren burrow --drop', Workstream C) or re-mount without --materialize", project, warrenID)`.
   Disabling (`--off`) stays allowed regardless. **Sequencing note:** PR A ships before C1's
   `burrow --drop` exists — the PR-A wording of this error must name the manual escape instead
   (delete `.marmot-data/warrens/<warrenID>/projects/<project>/`), and PR 3 swaps the text to the
   verb; track it in C1's doc task.
2. `Mount` (:819-844): when `materialized == true`, refuse any project already in
   `entry.EditableProjects` with the mirror-image error. (CLI caller: `warrenMount`,
   `cmd/marmot/warren.go:685-755`; the error surfaces as-is.)
3. `preferredProjectPath` (:1011-1019): belt-and-braces — if the project is editable, return the
   checkout path even if a stale `_warren.md` (hand-edited, or written by an old binary) carries
   both flags. This automatically fixes `findWarrenMountByVault`'s write-path choice at
   `internal/api/handlers.go:402` and provenance at :596, since both consume
   `ProjectStatus.Path` produced from this function via `ActiveMounts`.

No API/UI change needed: `web/src/detail-panel.ts` already gates on `provenance.editable`; the
server-side path choice is what was wrong.

**Tests** (`internal/warren`):

1. `TestSetEditableRefusesMaterialized` — mount with materialize, `SetEditable` → error mentioning
   "materialized"; state file unchanged (round-trip compare).
2. `TestMountMaterializedRefusesEditable` — inverse ordering.
3. `TestPreferredPathEditableWinsOverStaleFlags` — hand-craft a `WorkspaceWarren` with a project in
   both lists; `ActiveMounts` reports the checkout path and (optionally) a stderr warning about the
   inconsistent state.
4. `internal/api`: extend the `setupAPIWarren` harness (`api_test.go:135-171`) — editable mount,
   node update via `handleNodeUpdate`, assert the write landed under the **checkout** path.

**Risks.** Users with existing both-flags state get behavior change (writes move to checkout).
That is the documented behavior; add a one-line stderr notice when `preferredProjectPath` overrides
a materialized flag for an editable project so the change is visible.

---

## A5 — flock the `_warren.md` / routes.yml read-modify-writes (review 1.5)

**Defect.** Every mutation of all three `_warren.md` roles is an unlocked Load→mutate→Save:
`updateWorkspaceState` (`warren.go:1065-1077`, the single choke point for `RegisterWorkspaceWarren`
:798-816, `Mount` :819-844, `SetEditable` :847-872) and eight manifest Load/Save pairs (`Init`
131/140, `AddProject` 148/173, `ImportProject` 184/299, `RemoveProject` 327/351, `RenameProject`
368/395, `AddBridge` 406/432, `RemoveBridge` 464/488, `Format` 607/611). Concurrent processes drop
each other's writes. `routes.Update` (`internal/routes/routes.go:150-196`) has the same
cross-process RMW hazard plus a fixed `.tmp` name (:136, :187) shared by `Save`/`SaveTo`; the
Load→Save pairs in `cmd/marmot/route.go:89+97` and `:114+125` are equally racy. Individual writes
are already atomic (`writeMarkdownYAML` :1127-1166 uses unique CreateTemp+rename), so this is purely
lost-update, not torn-file.

**Helper API — new package `internal/flock`** (do NOT call `daemon.TryAcquire`: it is
non-blocking, hardcodes `daemon.lock`, and MkdirAll's a dataDir — findings `internal-daemon`
section is explicit that only the `tryFlock` primitive is reusable):

```go
// internal/flock/flock.go
package flock

// WithLock opens (creating if needed, 0o600) lockPath, takes an exclusive
// BLOCKING BSD flock on it, runs fn, and releases the lock by closing the
// fd (kernel-released even on SIGKILL). The lock file is left in place.
func WithLock(lockPath string, fn func() error) error

// internal/flock/flock_unix.go  (//go:build unix)
func lockBlocking(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }

// internal/flock/flock_windows.go
// Degrades to running fn unlocked: individual writes stay atomic via
// rename, so Windows keeps today's (last-writer-wins) semantics instead
// of gaining a hard failure. Documented in the package comment.
func lockBlocking(f *os.File) error { return nil }
```

Blocking (`LOCK_EX` without `LOCK_NB`) is correct for RMW writers — they should wait, not fail
with `ErrHeld`. Critical sections are milliseconds (see below), so no timeout machinery; if a
timeout is ever wanted, add `WithLockTimeout` later. `internal/daemon` migration onto this package
is explicitly out of scope (its non-blocking election semantics differ).

**Lock files and critical sections** (lock file = state file path + `.lock`, always sibling so it
inherits the same fs/permissions):

| State file | Lock path | Critical section |
|---|---|---|
| workspace `<root>/.marmot/_warren.md` | `<root>/.marmot/_warren.md.lock` | entire `updateWorkspaceState` body (:1066-1076) |
| warren manifest `<warrenRoot>/_warren.md` | `<warrenRoot>/_warren.md.lock` | new `updateManifest(root, fn)` helper wrapping each Load→mutate→Save pair |
| project metadata `<marmotDir>/_warren.md` | (none needed) | written only by `SaveProjectMetadata` full-file writes, no RMW today — skip until one appears |
| `~/.marmot/routes.yml` | `<routesPath>.lock` | `routes.Update` :154-195 |

Implementation steps:

1. `updateWorkspaceState`: wrap the whole body in
   `flock.WithLock(statePath + ".lock", func() error { ... })` where `statePath` comes from
   `workspaceStatePath(workspaceRoot)` (:1093-1097). One edit covers Register/Mount/SetEditable.
2. Add `updateManifest(root string, fn func(*Manifest) error) (*Manifest, error)` mirroring
   `updateWorkspaceState` (LoadManifest → fn → SaveManifest under
   `flock.WithLock(manifestPath + ".lock", ...)`) and convert the eight ops. **`ImportProject`
   holds the manifest lock across the copy** (load :184 → copy :282 → rename :294 → save :299):
   imports are rare and correctness requires the manifest snapshot it validated against to still be
   current at save time. Note the latency in the doc comment.
3. `routes.Update` (:150-196): take `flock.WithLock(path + ".lock", ...)` inside the existing
   package `mu`, around the read-modify-rename (:154-195). Lock ordering is fixed
   (process mu → file flock), no inversion possible.
4. Fix the fixed tmp name: `Save`/`SaveTo`/`Update` switch `path + ".tmp"` to
   `os.CreateTemp(filepath.Dir(path), ".routes-*.yml.tmp")` + rename (same pattern as
   `writeMarkdownYAML` and `node.SaveNode` :147-168).
5. Convert `cmd/marmot/route.go` add/remove (`:89+97`, `:114+125`) from Load→Save pairs to
   `routes.Update(fn)` so the CLI paths get the lock for free.
6. Git hygiene: `<warrenRoot>/_warren.md.lock` is an untracked file inside a git checkout. Have
   `warren init` append `_warren.md.lock` to the warren repo's `.gitignore` (create if absent), and
   add a doctor note (Workstream D) for warrens lacking the entry. The workspace-side lock lives
   under `.marmot/`, which projects already ignore or commit deliberately — document it.

**Tests:**

1. `internal/flock`: exec-helper multi-process test mirroring
   `internal/daemon/lock_test.go:106-195` (spawn `os.Args[0]` with
   `-test.run=TestHelperFlockHolder$`, env-gated, stdout sentinel, then assert `WithLock` blocks
   until the holder exits — poll with a deadline) plus a SIGKILL-release test.
2. `internal/warren`: `TestConcurrentMountRMW` — N goroutines calling `Mount` with distinct
   projects on one workspace via `updateWorkspaceState`; assert all N projects present afterwards
   (fails deterministically-ish today, passes with the lock). Single-process goroutines exercise
   the flock because each `WithLock` opens its own fd (BSD flock is per open-file-description).
   Template: `internal/mcp/concurrency_test.go:13-55`. Add a multi-process variant using the
   exec-helper pattern for the real cross-process guarantee.
3. `internal/routes`: extend `routes_stress_test.go` — two exec'd processes doing
   `Update(Set(uniqueKey))` loops; assert union of keys survives. Plus a unit test that
   `Save` no longer leaves/collides on a fixed `.tmp` (concurrent SaveTo to one path).

**Risks.** (a) Blocking lock + a crashed-while-holding process: flock is fd-scoped, kernel releases
on any exit including SIGKILL — no stale-lock cleanup needed (same property the daemon relies on,
`lock_unix.go:12-15`). (b) NFS/network filesystems: BSD flock semantics vary; same exposure the
daemon already accepted — document. (c) Windows: degraded (unlocked) but not worse than today.

---

## A6 — Un-swallow the errors (review 1.6)

Every site, its current behavior, and its new behavior. Convention: **load/read paths warn on
stderr and continue** (a broken warren must not brick local queries); **write paths propagate**;
MCP surfaces failures per mcp-go convention (`mcp.NewToolResultError`), API returns 5xx or a
warning field.

| # | Site | Today | New behavior |
|---|---|---|---|
| 1 | `internal/warren/warren.go:928-933` `ActiveMounts` manifest-load failure | silent `continue` (or materialized fallback) | keep the fallback; on final drop, `fmt.Fprintf(os.Stderr, "warning: warren %q manifest unreadable at %s: %v (mounts skipped)\n", ...)`. Also set a new `ProjectStatus`-level or return-side note so `warren status` (Workstream C) can render it — for Tier 1, stderr only. |
| 2 | `warren.go:895, 945, 986` `LoadProjectMetadata` failures | silent degradation | stderr warning naming the project + path, once per call. |
| 3 | `internal/api/handlers.go:448-456` editable-write embedding upsert (`NewStore`/`Upsert`/`Close` errors discarded) | node write succeeds, embedding silently stale | propagate: on `NewStore`/`Upsert` error, still keep the node write (it already happened) but return the update response with a `"warning": "embedding not updated: <err>"` field and log stderr; on `Embed` error same. Do NOT 500 — the node write is durable and rolling it back is worse. Update `web/src/types.ts` only if the field is surfaced in UI (optional; response is additive JSON, shape-stable). |
| 4 | `internal/api/handlers.go:459-461` `VaultRegistry.Refresh` error dropped | silent | stderr warning (`refresh after editable write failed: %v`); response unaffected. (Workstream B rewrites Refresh itself.) |
| 5 | `cmd/marmot/pipeline.go:391-415` `warrenRuntimeBridges` — workspace-state load error → `return nil, false`; per-warren `LoadManifest` error → `continue` | **silently removes bridge policy enforcement** | stderr warning at both: `"warning: warren bridge manifest unreadable (%s): %v — cross-vault bridge policy NOT enforced for this warren"`. Keep the continue/false control flow (fail-open is today's semantic; fail-closed is a Workstream B/D policy decision). |
| 6 | `internal/mcp/handlers.go:74-98` remote-store open + post-refresh search errors (`continue` at :82, `_ =` at :88) | vanish | stderr warning per vault per query is too chatty: warn **once per vault ID per process** (small `sync.Map` of warned IDs on Engine) with the open/search error; results stay best-effort. |
| 7 | `internal/api/handlers.go:566-572` `searchMountedVaults` ResolveEmbeddingStore/SearchActive `continue`s, and :545 `ActiveMounts` `_ =` | vanish | same once-per-vault stderr warning; keep best-effort continue. |
| 8 | `internal/api/handlers.go:948-950` `findWarrenMountByVault` swallows `ActiveMounts` error → `false` | @-node updates report "not a warren node" | stderr warning; behavior otherwise unchanged (returning an error would change the handler contract — Tier 3 territory). |
| 9 | `internal/api/handlers.go:855-861` `handleWarrenGraph` skips unavailable mounts + per-mount LoadGraph errors | silent partial graph | stderr warning per skipped mount; add `"skipped": [<project ids>]` to the JSON response (additive — `web/src/api.ts` fetchers ignore unknown fields). |
| 10 | `internal/namespace/namespace.go:666` `_ = routes.Update(...)` bridge auto-register | silent | stderr warning (`bridge created but not auto-registered in routes.yml: %v`). |

Non-goals here: `traversal/bridged.go:32-34,60-62` (interface has no error channel — surfacing
belongs in the provider, Workstream B) and `node.Store.ListNodes` malformed-file skips (pre-existing
loader semantic, out of warren scope).

**Tests:** for each stderr site, a unit test that captures stderr (or injects an `io.Writer` — 
prefer adding a package-level `warnw io.Writer = os.Stderr` in warren/api for testability) and
asserts the warning fires on a corrupted fixture: truncated `_warren.md` for #1/#2/#5, missing
remote DB for #6/#7, unreadable mount dir for #9. For #3, an API test with a read-only
`.marmot-data` dir on the mount asserting the `warning` field and node-write success.

---

## A7 — Anchored frontmatter parsing (review 1.7, widened)

**Defect.** `parseMarkdownYAML` (`warren.go:1110-1125`) finds the closing `---` **anywhere**
(`strings.Index(content[3:], "---")`, :1115) and then assumes the delimiter shape with
`content[end+6:]` (:1120) — any `---` inside a YAML value or body truncates the frontmatter and
corrupts every subsequent save (writer at :1127-1166 re-emits whatever was parsed). The findings
widened the blast radius: the same pattern exists in `internal/namespace/namespace.go` —
`parseBridge` :368, `parseNamespace` :166, `extractEdgesFromFrontmatter` :540-548 — and a sibling
defect in `internal/node/parser.go:60` (`strings.Index(rest, "\n---")` accepts any line merely
*starting* with `---`, e.g. `----` or a `---` line inside a block scalar, and `closingIdx+4`
hard-codes the shape).

**Fix — shared splitter, new package `internal/frontmatter`:**

```go
package frontmatter

// Split separates anchored YAML frontmatter from the body. The opening
// delimiter must be exactly "---" on the first line; the closing delimiter
// must be exactly "---" alone on its own line (optionally followed by \r).
// The body is everything after the closing delimiter line, with one leading
// newline stripped (matching what writers emit).
func Split(data []byte) (yamlBlock []byte, body string, err error)
```

Implementation (line-anchored, no regex needed):

```go
content := string(data)
if !(strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") || content == "---") {
    return nil, "", fmt.Errorf("missing YAML frontmatter")
}
rest := content[strings.IndexByte(content, '\n')+1:]
for scan := rest; ; {
    idx := strings.Index(scan, "\n---")  // candidate
    // accept only if the line is exactly "---" (followed by \n, \r\n, or EOF)
    // else advance scan past the candidate and continue
}
```

(Equivalent compact form: split on `regexp.MustCompile(`(?m)^---\s*$`)` finding the *second*
line-anchored match; either is fine — the tests below pin behavior, choose the loop for zero
regex-in-hot-path cost. `\s*` tolerance is deliberate: trailing spaces/`\r` on the delimiter line
must close, matching what editors produce.)

Wiring:

1. `warren.go:parseMarkdownYAML` delegates: `yamlBlock, body, err := frontmatter.Split(data)` then
   `yaml.Unmarshal`. One fix covers its five callers (`LoadManifest` :657, `LoadProjectMetadata`
   :705, `loadWorkspaceStatePath` :758, `sanitizedConfigBytes` :1422, `sourceVaultID` :1514).
   `writeMarkdownYAML` already emits line-anchored `---\n`, so round-trip holds.
2. `internal/namespace`: `parseBridge` (:363-368), `parseNamespace` (:166), and
   `extractEdgesFromFrontmatter` (:540-548) switch to `frontmatter.Split`.
3. `internal/node/parser.go:splitFrontmatter` (:47-69): keep its lenient leading-whitespace
   handling but replace the `strings.Index(rest, "\n---")` + `closingIdx+4` core with the anchored
   scan (or call `frontmatter.Split` on the trimmed content). Public `ParseNode`/`ParseNodeMeta`
   signatures unchanged (widely consumed — findings `internal-node`).

**Behavior notes.** Files that today parse "successfully" but wrongly (early `---` in a value)
will now parse correctly; files that a buggy save already corrupted stay corrupted — no migration
is possible, but the corruption stops compounding. A file whose frontmatter is genuinely
unterminated now errors identically to before.

**Tests:**

1. `internal/frontmatter`: table test — `---` in a YAML double-quoted value, `---` in a block
   scalar, body containing `\n---\n`, delimiter with trailing `\r` and trailing spaces, `----` line
   (must NOT close), `--- foo` line (must NOT close), empty body, no trailing newline after closing
   `---` (EOF case), missing open, unterminated.
2. `internal/warren`: round-trip regression — build a `Manifest` whose project description contains
   a line `---`, `SaveManifest` → `LoadManifest` → deep-equal; same for `WorkspaceState` body
   preservation (the `body` return of `LoadWorkspaceState` must survive a save cycle).
   Corrupt-YAML hardening cases: template `internal/routes/routes_stress_test.go:257-305`.
3. `internal/node`: extend `TestRoundtrip` (`node_test.go:321`) with a Summary/Context containing a
   line-leading `---` (writer `RenderNode` emits `---\n<yaml>---\n`, per findings) — parse must
   return the full YAML and intact body.
4. `internal/namespace`: bridge manifest whose `description`/allowed-relations comment contains
   `---` parses fully (guards the pipeline.go:391-415 policy path from self-inflicted parse errors,
   compounding with A6 #5).

**Risks.** Strictness change: a file whose closing delimiter has leading whitespace (`  ---`) never
closed correctly before either (it matched some other `---`), so no realistic regression. The three
packages previously accepted `---` at arbitrary offsets — any repo content that *depended* on
mid-line closure was already being corrupted on save.

---

## A8 — Test hermeticity: `MARMOT_ROUTES=off` (review 1.8, corrected)

**Defect.** Tests that call `buildEngine` inherit `routes.Load()` (`pipeline.go:236`) reading the
developer's real `~/.marmot/routes.yml` and can scan real user vaults. The review's cite of
`internal/warren/warren_test.go:329` is **wrong** (that package is hermetic — pure `t.TempDir`,
never touches routes); the actual offenders, verified by grep (`MARMOT_ROUTES` appears in zero
cmd/marmot test files):

1. `cmd/marmot/pipeline_warren_test.go:44` (`TestBuildEngineEnforcesWarrenBridgesForActiveMounts`)
2. `cmd/marmot/warren_test.go:380` (`TestBuildEngineQueriesActiveWarrenMount`)
3. `cmd/marmot/surface_coverage_test.go:679` (classifier-provider buildEngine loop — missed by the
   review, found by grep)

**Change list:**

- Add `t.Setenv("MARMOT_ROUTES", "off")` as the first line of each of the three tests. The env
  override exists and is exact (`internal/routes/routes.go:53-70`: `off|none|0` → disabled;
  `SetOverridePath` beats env, unused in these tests).
- Add a guard for the future: a `cmd/marmot` test helper
  `func hermeticEngine(t *testing.T, dir string) *engineResult` that Setenv's `MARMOT_ROUTES=off`
  (and `ANTHROPIC_API_KEY=""`) then calls `buildEngine`; migrate the three call sites to it and use
  it for all new warren tests in this package.
- Scope note for the test program (Workstream Testing): the `e2e/` harness isolates via
  `HOME=<proj>` (`e2e/e2e_test.go:115-117`) because warren e2e tests **need** routes to function —
  `MARMOT_ROUTES=off` is for unit tests only; do not blanket-apply it to e2e.

**Tests:** the change *is* test-only. Verification: run the three tests with a poisoned
`~/.marmot/routes.yml` pointing at a nonexistent vault (manual or a temporary CI check) and confirm
identical results; template assertions exist in `internal/routes/routes_test.go:56-113`
(`TestEnvOverrideDisables`).

---

## Workstream A packaging note (single PR)

Ship A1–A8 as **one PR** titled "warren: Tier 1 correctness", in the eight commits ordered as
listed at the top of this workstream — pure correctness, no CLI surface changes, no daemon
coupling, each commit independently green. New packages introduced: `internal/flock`,
`internal/frontmatter` (both tiny, both reused by Workstreams B–D: flock by Tier 2's cross-workspace
`index --force` guard, frontmatter by any future manifest work). Doc updates bundled in the same
PR: `docs/warrens.md` — burrow copy semantics now match import (:104-137 section gains a burrow
paragraph), editable+materialized refusal documented next to :277-278, checkpoint-before-copy
mentioned as the mechanism behind sidecar exclusion. Rollback story: every item is
behavior-additive (new function, new guard, new warning) except A3's copier swap and A7's parser —
both are pinned by round-trip/equivalence tests against the old outputs.

Cross-cutting note for the PR: coverage discipline — the repo was recently raised to >=80% per
package (commit `0ec6e4e`); there is **no CI coverage gate**, only `make test-cover` locally
(`Makefile:22-24`) — keep parity by landing the new packages' unit tests in the same commits that
introduce them.

---

# Workstream B: Daemon-era freshness (Tier 2)

**Problem.** Warren state enters the engine exactly once, in `buildEngine`
(`cmd/marmot/pipeline.go:236-274`), and the `VaultRegistry` is created only when routes or bridges
exist at startup (:265-267) — a long-lived daemon owner never sees `warren mount/edit/burrow`, a
`git pull` in the warren checkout, or routes changes, and can have a nil registry forever. The
owner's fsnotify watcher deliberately skips `_`-prefixed files (`internal/daemon/owner.go:296-303`),
`POST /api/warren/{id}/refresh` is a printf stub (`internal/api/handlers.go:912-932`), remote
graphs are cached with a never-read `LoadedAt` (`internal/namespace/registry.go:24,137` — verified:
zero readers in the repo), and `VaultRegistry.Refresh` closes the cached `EmbStore` in place
(:153-155) while a concurrent search may hold the pointer.

**Decided freshness model** (everything below implements it):

1. **Query paths (MCP + API cross-vault search)** are registry-cached with *bounded* staleness:
   remote **graphs** expire on a TTL (default **60s**, lazy — reload on next access, not on a
   timer); remote **embedding stores** need no TTL because every `SearchActive` is a live SQLite
   read (WAL readers see committed data; the cached handle never goes stale, only the graph does).
2. **Explicit invalidation beats the TTL:** a single helper, `Engine.ReloadWarrenState`, re-runs
   the mount/bridge/routes wiring and is triggered by (a) the real refresh endpoint, (b) the real
   `marmot warren refresh` command, (c) the owner watcher observing the workspace `_warren.md`.
   The workspace `_warren.md` is the cross-process change signal: every warren CLI mutation
   already rewrites it (atomically, and under A5's flock), so watching that one file is sufficient
   IPC — no daemon-socket control verb is added (the proxy protocol is raw MCP JSON-RPC with no
   room for out-of-band commands, findings `internal-daemon`; a touch-file is simpler and works
   for *every* observer, not just flock owners).
3. **The warren HTTP UI endpoints stay disk-fresh** (`handleWarrens`/`handleWarrenStatus`/
   `handleWarrenGraph`, `internal/api/handlers.go:786,796,826` re-read disk per request). Do not
   registry-back them — the review's "two views in one process" is resolved by making the *stale*
   side fresh, not by making the fresh side stale.
4. **No engine-field swap.** `Engine.VaultRegistry` (`internal/mcp/engine.go:41`) is an exported,
   unsynchronized field read by handlers on every query. Rather than converting it to an atomic
   pointer (touches every consumer in internal/api + internal/mcp), the registry is created
   **unconditionally at startup and mutated in place** via a new `Rebuild` method under the
   registry's own `mu`. The pointer never changes after `WithVaultRegistry`, so unsynchronized
   reads stay correct. (Rejected alternative — `atomic.Pointer[VaultRegistry]` behind accessors —
   is more code, breaks the exported-field API used at `internal/api/handlers.go:459,538,565,589`
   and `internal/mcp/handlers.go:75-88`, and buys nothing the internal mutex doesn't.)

Ships as **PR 2**, after Workstream A (it reuses `internal/flock` from A5 and the A6 warning
conventions). Order: B1 → B2 → B3 → B4; each lands with its tests.

---

## B1 — `VaultRegistry.Rebuild` + swap-then-close `Refresh` + graph TTL

All in `internal/namespace/registry.go`.

**B1.1 `Rebuild`.** Signature decision: the review sketches `Rebuild(mounts, routes)`, but mounts
reach the registry today only as routing-table entries (`rt.Set(mount.VaultID, mount.Path)`,
`pipeline.go:245`) — keep that division of labor and let the caller fold mounts into the table:

```go
// Rebuild atomically replaces the registry's resolution inputs (bridge path
// hints + routing table) and evicts cached vaults whose directory changed or
// disappeared. Vaults whose directory is unchanged keep their cached graph
// and embedding store. Evicted embedding stores are closed AFTER the lock is
// released (swap-then-close) so a search that already resolved a store is
// never handed a just-closed handle by the registry itself.
func (r *VaultRegistry) Rebuild(bridges []*Bridge, rt *routes.RoutingTable) {
    var toClose []*embedding.Store
    r.mu.Lock()
    r.routingTable = rt
    r.pathToID = make(map[string]string)
    for _, b := range bridges { /* same IsCrossVault seeding as NewVaultRegistry :51-61 */ }
    for id, rv := range r.vaults {
        newDir := r.dirForLocked(id) // rt-then-pathToID lookup, extracted from ResolveGraph :95-112
        if newDir != rv.VaultDir {   // moved or unmounted
            if rv.EmbStore != nil {
                toClose = append(toClose, rv.EmbStore)
            }
            delete(r.vaults, id)
        }
    }
    r.mu.Unlock()
    for _, s := range toClose {
        _ = s.Close()
    }
}
```

Residual race, accepted and bounded: a search that called `ResolveEmbeddingStore` *before* the
Rebuild still holds the old `*embedding.Store`; `Store.Close` takes `Store.mu`
(`internal/embedding/store.go:547-551`), so an in-flight `SearchActive` finishes before the close
completes; a search issued *after* the close gets a closed-conn error, which the A6 #6/#7
once-per-vault warning surfaces instead of silently returning empty. Since eviction only happens
when a vault's directory actually changed (rare) and searches are milliseconds, refcounting is not
worth its complexity — record this in the method doc.

**B1.2 Swap-then-close `Refresh`.** Rewrite `Refresh` (:145-159), which today closes the EmbStore
(:154) *before* reloading — the exact close-under-reader the review flags. New order: build the
replacement first, swap the map entry, close the old store after unlock:

```go
func (r *VaultRegistry) Refresh(vaultID string) error {
    r.mu.Lock()
    existing, ok := r.vaults[vaultID]
    if !ok {
        r.mu.Unlock()
        return fmt.Errorf("vault %q not loaded", vaultID)
    }
    dir, old := existing.VaultDir, existing.EmbStore
    delete(r.vaults, vaultID)               // force loadVaultLocked to rebuild
    _, err := r.loadVaultLocked(vaultID, dir) // fresh graph; EmbStore reopens lazily
    r.mu.Unlock()
    if old != nil {
        _ = old.Close()
    }
    return err
}
```

Callers tolerate `not loaded` (constraint from findings): the editable-write refresh at
`internal/api/handlers.go:459-461` and B3's endpoint must treat that error as "nothing cached,
nothing to do", not a failure.

**B1.3 Graph TTL.** Honor `LoadedAt` in the `ResolveGraph` fast path (:78-83): if
`time.Since(rv.LoadedAt) > r.graphTTL`, fall through to the slow path and reload via
`loadVaultLocked` (which stamps a new `LoadedAt`; keep the existing `EmbStore` on the rebuilt
entry — carry it over in `loadVaultLocked` when an evictee exists, so TTL expiry never closes a
store). Default `graphTTL = 60 * time.Second`; `MARMOT_WARREN_TTL` env (Go duration; `0`/`off`
disables expiry) read once in `NewVaultRegistry`, mirroring the `MARMOT_ROUTES` override pattern
(`internal/routes/routes.go:53-70`). 60s is chosen because the TTL is only the backstop for
out-of-band changes (git pull in the checkout, another workspace's re-index) — in-band changes all
go through `ReloadWarrenState` or `Refresh` and take effect immediately; a stale remote answer
bounded at one minute matches the "warrens track git, not real time" contract in
`docs/warrens.md:303-317`. Old graphs are pointer-held by in-flight traversals and simply GC'd —
`graph.Graph` needs no close, and `BridgedGraphResolver.GetNode` already deep-copies edges
(`internal/traversal/bridged.go:43-48`), so a TTL reload cannot corrupt an in-flight traverse.

**Tests** (`internal/namespace`, reuse `setupRemoteVault` fixture `registry_embstore_test.go:11-23`):

1. `TestRebuildKeepsUnchangedVaults` — resolve a vault, Rebuild with same rt → same `*Store`
   pointer returned (cache retained); Rebuild with the vault re-routed to a new dir → old store
   closed, next resolve loads the new dir.
2. `TestRefreshSwapThenClose` — resolve a store, start a goroutine loop of `SearchActive` (template:
   `internal/embedding/store_wal_test.go:99-164 TestNewStore_ConcurrentConns`), call `Refresh`
   repeatedly; assert no panic and every loop iteration either succeeds or returns a closed-store
   error (never a silent empty success with rows present).
3. `TestResolveGraphTTL` — TTL 50ms via env override, resolve, add a node file to the remote vault,
   assert stale within TTL and fresh after; `MARMOT_WARREN_TTL=off` never reloads.
4. `TestRefreshNotLoadedTolerated` — error string pinned so callers can gate on it (or add a
   sentinel `ErrNotLoaded`; prefer the sentinel).

---

## B2 — Always-create the registry + extract `Engine.ReloadWarrenState`

**Placement decision.** The helper cannot live in cmd/marmot (`package main`, not importable —
findings `cmd-marmot` note 3) and must be callable from `internal/api` (refresh endpoint) and
`internal/daemon` (owner watcher). Both already import `internal/mcp`, and the reload must mutate
`Engine.NSManager` under the **unexported** `nsMgrMu` (`internal/mcp/engine.go:46`) — so it lands
as an Engine method: new file `internal/mcp/warren_reload.go`. New imports for internal/mcp:
`internal/warren` (imports only stdlib+node — acyclic, verified) and `internal/routes` (leaf).
`internal/daemon` and `internal/api` need no new imports.

**Exact code moved from `cmd/marmot/pipeline.go`:**

- The warren wiring block :236-263 (`rt, _ := routes.Load()` … mounts→`rt.Set` … `warrenRuntimeBridges`
  merge … stderr mount count) becomes the body of `ReloadWarrenState`.
- `warrenRuntimeBridges` (:390-466) and `runtimeBridgeKey` move verbatim to
  `internal/mcp/warren_reload.go` (unexported). Their A6 #5 stderr warnings come along.
- The registry gate :265-274 is **deleted**; `buildEngine` instead does, unconditionally:

```go
vr := namespace.NewVaultRegistry(vaultID, dir, nil, routes.EmptyTable())
engine.WithVaultRegistry(vr)          // also caches LocalVaultID (engine.go:292-299)
if err := engine.ReloadWarrenState(); err != nil {
    fmt.Fprintf(os.Stderr, "warning: warren state load failed: %v\n", err)
}
```

`routes.EmptyTable()` does **not exist yet** — add it as a one-line constructor in
`internal/routes` (`func EmptyTable() *RoutingTable { return &RoutingTable{Vaults: make(map[string]VaultEntry)} }`),
lifting the literal `buildEngine` already inlines at `pipeline.go:237-239`; both uses below and the
nil-guard in `ReloadWarrenState` go through it.

**Method spec:**

```go
// ReloadWarrenState re-reads routes.yml and the workspace _warren.md, folds
// active warren mounts into a fresh routing table, recomputes warren runtime
// bridges, and rebuilds the vault registry in place. Safe to call on a live
// engine at any time; concurrent searches keep working against the registry
// (see VaultRegistry.Rebuild).
func (e *Engine) ReloadWarrenState() error {
    rt, _ := routes.Load()
    if rt == nil { rt = routes.EmptyTable() }
    mounts, err := warren.ActiveMounts(e.MarmotDir)   // A6 #1 warns inside
    if err == nil {
        for _, m := range mounts {
            if m.VaultID != "" && m.Available {
                if prev, ok := rt.Get(m.VaultID); ok && prev != m.Path {
                    fmt.Fprintf(os.Stderr, "warning: vault ID %q claimed by both %s and %s; using %s\n", m.VaultID, prev, m.Path, m.Path)
                }
                rt.Set(m.VaultID, m.Path)
            }
        }
    }
    bridges, declared := warrenRuntimeBridges(e.MarmotDir, mounts)
    e.setWarrenBridges(bridges, declared)  // under nsMgrMu, see below
    e.VaultRegistry.Rebuild(e.crossVaultBridges(), rt)
    return err
}
```

Two subtleties the extraction must handle:

1. **Bridge idempotence.** `buildEngine` *appends* warren bridges to `nsMgr.CrossVaultBridges`
   (:253) — calling that twice would duplicate. `Engine` gains an unexported
   `warrenBridges []*namespace.Bridge` field; `setWarrenBridges` (under `nsMgrMu`) replaces that
   slice and recomposes `NSManager.CrossVaultBridges = fileBridges ++ warrenBridges`, where
   `fileBridges` is captured once at `WithNamespaceManager` time. It also creates an empty
   `NSManager` when bridges appear on a previously namespace-less engine (move
   `emptyNamespaceManager`, `pipeline.go:382-388`, into the same file).
2. **`engineResult` unchanged** (`pipeline.go:197-203` has no registry field) — callers reach the
   registry through `engine`, as today.
3. **Nil-registry guard.** `ReloadWarrenState` must start with
   `if e.VaultRegistry == nil { return nil }` — production engines always get a registry after
   this change, but the method is exported and zero-value/unit-test Engines (which the
   always-create consequences below promise keep working) would otherwise nil-panic at the
   `Rebuild` call; same guard the B3.3 watcher relies on.

Also update the manual test wiring `wireWarrenVaultRegistry` (`internal/api/api_test.go:191-202`)
to call `ReloadWarrenState` instead of hand-constructing a registry — findings flag it as a second
wiring path that must not survive B2.

**Always-create consequences (verified benign):** every nil-check on `VaultRegistry` stays valid
(the field is now always non-nil after `buildEngine`; zero-value Engines in unit tests keep nil and
keep working); `searchMountedVaults`' nil gate (`internal/api/handlers.go:538-540`) stops silently
disabling cross-vault search for mounts created after startup — that is the fix, and the
`_warren/`-ns-only scoping of that function (findings `internal-api`) is unchanged. Startup cost of
an empty registry is a struct allocation.

**Tests:**

1. `internal/mcp/warren_reload_test.go` — engine on a hermetic vault (template
   `server_test.go:15-35` + `t.Setenv("MARMOT_ROUTES", "off")` per A8, since this test *does*
   exercise `routes.Load`): mount a fixture warren project by writing workspace `_warren.md`
   (fixture helpers: `cmd/marmot` ones are unimportable — port `setupAPIWarren`,
   `internal/api/api_test.go:135-171`, into a shared `internal/warren/warrentest` helper or
   duplicate minimally), call `ReloadWarrenState`, assert `KnownVaultIDs` gains the vault and a
   `context_query` reaches it; unmount (rewrite `_warren.md`), reload, assert it is gone.
2. Bridge idempotence: reload twice, assert `len(NSManager.CrossVaultBridges)` stable.
3. `cmd/marmot`: adapt `TestBuildEngineQueriesActiveWarrenMount` (`warren_test.go:329`) to assert
   the registry is non-nil even with zero mounts/routes.
4. Race: `go test -race` run of reload-in-loop vs `HandleContextQuery`-in-loop (single process;
   template `internal/mcp/concurrency_test.go:13-55`).

---

## B3 — Wire the three triggers

**B3.1 Real `POST /api/warren/{id}/refresh`** (`internal/api/handlers.go:912-932`). Keep the
existing id-validation prologue (:914-926), then replace the printf payload with:

```go
if err := s.engine.ReloadWarrenState(); err != nil {
    writeError(w, http.StatusInternalServerError, "warren refresh: "+err.Error())
    return
}
writeJSON(w, http.StatusOK, map[string]string{
    "warren_id": id,
    "status":    "reloaded",
})
```

The reload is engine-global, not per-warren — acceptable and honest (mounts/bridges/routes are one
composite state; a per-warren partial reload would re-introduce split views). Keep the `warren_id`
echo: `TestWarrenListStatusRefreshSuccess` (`internal/api/api_more_test.go:386-433`) asserts that
key; update its `status` expectation in the same commit. Wire the UI's existing `#refresh-btn`
(`web/src/main.ts:197-199`) to POST this endpoint before `loadGraph()` when the current namespace
is `_warren/<id>` — one fetch call, response shape already typed loosely.

**B3.2 Real `marmot warren refresh`** (`cmd/marmot/warren.go:826-845`). `resolveWarrenID`
(:887-907) loads state and discards it; change it to
`resolveWarrenEntry(workspaceRoot, requested string) (string, warren.WorkspaceWarren, error)` and
mechanically update its three callers (warrenStatus :770, warrenRefresh :838, warrenPropose :859 —
all in-file). New refresh body, after resolve:

1. Reachability check: `entry.Path` missing → the C6 unreachable error, exit 1.
2. **Signal live observers:** add `warren.TouchWorkspaceState(workspaceRoot) error` =
   `updateWorkspaceState(workspaceRoot, func(*WorkspaceState) error { return nil })` — a no-op RMW
   that rewrites the file atomically under A5's flock, bumping its mtime so every owner/API
   watcher fires (B3.3). This is the CLI→daemon IPC; no socket verb.
3. Report: print reloaded mount counts from a fresh `warren.ActiveMounts`, plus — if
   `ownerAlive(dir)` (`cmd/marmot/pipeline.go:664-675`) — "live daemon owner (pid N) will pick up
   the change within ~1s". `git -C <entry.Path> pull` is explicitly **not** run here; that is
   Workstream D1 (`--pull` flag reserved). Update the usage text and `docs/warrens.md` refresh
   section accordingly (C9 kills the "stub presented as feature" complaint).

**B3.3 Owner watcher un-skips the workspace `_warren.md`**
(`internal/daemon/owner.go:236-330`). The workspace mount state lives at
`<workspaceRoot>/.marmot/_warren.md` = `<dir>/_warren.md` — directly inside the watched root, and
currently discarded by the underscore filter at :300-303. Change inside the event loop:

```go
// Warren mount state changed — reload warren wiring, not just the graph.
if event.Name == filepath.Join(dir, "_warren.md") {
    warrenPending = true
    schedule()
    continue
}
// (existing underscore skip stays for everything else)
```

and in the debounce-timer branch (:309-318 — `pending = false` at :315, `reloadGraph` at :316):
when `warrenPending`, call
`eng.ReloadWarrenState()` (stderr log on error, per `reloadGraph`'s pattern :334-343) before
`reloadGraph`, then clear the flag and still invoke `onReload` — the API server consumes this same
watcher via `StartGraphWatcherNotify` (`internal/api/watcher.go:14-16`), so SSE-connected UIs
refresh for free. `routes.yml` lives in `~/.marmot`, outside the watch root — deliberately not
watched; route changes are covered by the TTL and by explicit refresh. Signatures
`StartGraphWatcher`/`StartGraphWatcherNotify` (:228,236) unchanged, so both consumers
(`pipeline.go:592`, `api/watcher.go:15`) compile untouched.

**Tests:**

1. `internal/daemon`: unit — watcher on a temp dir, write `_warren.md`, assert
   `ReloadWarrenState` observable effect (mount count in registry) within the debounce window;
   negative — `_config.md` write does *not* trigger warren reload.
2. `internal/api`: refresh endpoint test — mount after server start, POST refresh, search
   `_warren/<id>` finds the remote node (extends `setupAPIWarren`).
3. e2e (feeds Testing & Rollout): **mount-while-owner-live** — `startMCPDaemon`
   (`e2e/e2e_test.go:250`) on project A; from a second process, register+mount project B's warren;
   run `marmot warren refresh`; assert a `context_query` over the live session now returns
   B-vault results. This test *pins* the freshness model.

---

## B4 — Cross-workspace `index --force` hazard: decision

**Today:** `runIndexPipeline` refuses `--force` under a live daemon owner *of the same vault*
(`pipeline.go:63-72`, already fixed) then deletes `embeddings.db` + sidecars. The remaining hole:
the vault is warren-mounted by *another* workspace whose (daemon-less or daemon-owning) process
holds the DB open via `VaultRegistry.ResolveEmbeddingStore` — deletion under its open WAL
connection leaves that process writing/reading an unlinked file.

**Decision: shared advisory flock** (the review's "better" option), reusing A5's package:

1. `internal/flock` gains `Shared(lockPath string) (release func(), err error)` (blocking
   `LOCK_SH`) and `TryExclusive(lockPath string) (release func(), ok bool, err error)`
   (`LOCK_EX|LOCK_NB`). Windows: `Shared` no-ops, `TryExclusive` always succeeds — degrades to
   today's behavior, same story as A5.
2. `VaultRegistry.ResolveEmbeddingStore` (`internal/namespace/registry.go:186-241`): after
   resolving `vaultDir`, take `flock.Shared(filepath.Join(vaultDir, ".marmot-data", "vault.read.lock"))`
   (Join, not string concat — the same path must hash identically on Windows) and stash
   the release func on the `RemoteVault`; released in `Close`, `Rebuild` eviction, and `Refresh`
   swap (alongside the EmbStore close). One fd per cached remote vault — bounded by mount count.
3. `runIndexPipeline --force` (`pipeline.go:63-72`): after the `ownerAlive` check, take
   `flock.TryExclusive` on the same path; on `!ok`, refuse:
   `"vault is open read-only by another marmot process (warren mount); close it or retry"`.
   Release after the delete+reindex completes.
4. Fallback/mitigation when flock is unavailable (Windows, exotic FS): keep the refusal message in
   `docs/warrens.md` as documentation — the minimal option — and note the platform gap.

**Tests:** `internal/flock` shared/exclusive matrix (exec-helper template
`internal/daemon/lock_test.go:106-195`); integration — process A resolves a remote store via a
registry, process B's `index --force` on that vault exits non-zero with the refusal; after A
closes, B succeeds. e2e reuses the two-process shape of `TestConcurrentServes`
(`e2e/e2e_test.go:495`).

**Risks (workstream-wide).** (a) Reload storms: `_warren.md` writes are debounced 1s by the
existing watcher timer; CLI touch is one write. (b) `ReloadWarrenState` racing `Engine.Close`:
`Close` (`engine.go:334-348`) calls `VaultRegistry.Close`, which serializes with `Rebuild` on the
registry mu; the engine-side `closing` flag should also gate `ReloadWarrenState` to a no-op after
close begins. (c) UI/registry views can still differ within one TTL window — bounded and
documented, vs. unbounded today. (d) B4's shared flock adds an open fd per remote vault per
process; `Close` paths must release or the fd (not the lock — kernel releases on exit) lingers.

---

# Workstream C: UX repairs (Tier 3)

Every Tier 3 review item, as CLI-surface work on top of A (flock, refusals, warnings) and B
(reload). Ships as **PR 3**; all items are independent except C1 (verbs) which C2's rollback and
A4's error text reference — land C1 first. Dispatch point for all new verbs:
`cmdWarren` switch (`cmd/marmot/warren.go:28-66`), usage line (:69-71), and — mandatory, findings
`cmd-marmot` — every verb taking flags after positionals registers them in its
`reorderInterspersedFlags` call (`cmd/marmot/flags.go:12-46`), or interspersed args silently
misparse. Workspace-side verbs use `ensureWorkspace`/`locateWorkspace` (C5), never
`resolveWarrenRoot` (:601-619 — warren-repo-side only).

**Mount-state transitions after C** (workspace `_warren.md`, struct
`WorkspaceWarren{Path, ActiveProjects, EditableProjects, Materialized}` `warren.go:60-65`):

| Verb | ActiveProjects | EditableProjects | Materialized flag | Burrow cache dir |
|---|---|---|---|---|
| `register` | — | — | — | — |
| `mount p…` / `mount --all` | +p | — | — | — |
| `burrow p…` / `--all` | +p | refuse if p editable (A4) | set true | created/refreshed |
| `edit p` | +p (auto-mount, printed) | +p; refuse if materialized cache exists for p (A4) | — | — |
| `edit --off p` | — | −p | — | — |
| `unmount p…` / `--all` (new) | −p | −p | — | untouched (says so) |
| `burrow --drop p…` / `--all` (new) | untouched | — | cleared when last cache gone | deleted |
| `unregister` (new) | must be empty unless `--force` | — | entry deleted | must be dropped unless `--force` |

## C1 — Inverse verbs: `unmount`, `unregister`, `burrow --drop` (review: "the sole escape is hand-editing `_warren.md`")

**Library (`internal/warren/warren.go`; all additive, all through `updateWorkspaceState` :1065 so
they inherit A5's flock; `removeName`/`removeNames` helpers already exist :1639,1679):**

```go
// Unmount deactivates projects (and drops their editable flag). Caches stay.
func Unmount(workspaceRoot, warrenID string, projects []string) (*WorkspaceState, error)
// DropMaterialized deletes projects' burrow caches under
// <marmotDir>/.marmot-data/warrens/<warrenID>/projects/<p>/.marmot
// (materializedProjectPath :1021) and clears entry.Materialized when no
// cache dir remains for any manifest project.
func DropMaterialized(workspaceMarmotDir, workspaceRoot, warrenID string, projects []string) error
// Unregister removes the warren entry. Errors if ActiveProjects is non-empty
// or any burrow cache exists, unless force; with force it also RemoveAlls
// the warren's cache tree and prints nothing (caller prints).
func Unregister(workspaceMarmotDir, workspaceRoot, warrenID string, force bool) error
```

Semantics decisions: `Unmount` does **not** delete caches (cheap to recreate is false — caches can
be large but re-burrow is one command; the real reason: unmount must be non-destructive so
mount→unmount round-trips). Unknown project IDs are per-item errors naming the warren (mirror
`Mount`'s validation :821-833). `Materialized` stays a **warren-level bool** — no state-schema
migration: per-project materialization ground truth is already `dirExists(cached)` (see `materializedStatuses`
:982-1002 — `dirExists(cached)` at :999 — and `preferredProjectPath` :1011-1019), and A4 made the editable check consult the cache
dir. (`web/src/types.ts:70`'s `materialized_projects` field never had a Go producer — leave it;
optional cleanup in the web types.) Live-owner interplay: `DropMaterialized` deletes cache dirs
and then rewrites the workspace `_warren.md` via `updateWorkspaceState`, so a live daemon owner
reloads through B3.3's watcher; within the ≤1s debounce window the owner's routing table can still
point at the deleted cache — queries fail loudly (A6 #6/#7 warnings), the same bounded exposure as
A3's re-burrow swap (R3). Delete caches *before* the state write so the reload observes the final
layout.

**CLI:**

- `marmot warren unmount --warren <id> [--dir .marmot] [--all] <project…>` — `--all` expands from
  `entry.ActiveProjects` (not the manifest — unmounting must work when the checkout is gone, which
  also makes it the escape hatch for unreachable warrens, C6). Prints per-project lines plus
  "burrow caches kept; run `marmot warren burrow --drop` to delete them" when caches exist.
- `marmot warren burrow --drop --warren <id> [--all] <project…>` — routes to `DropMaterialized`;
  `--drop` is a bool flag on the existing burrow verb (`warrenMount` gains an early branch when
  `isBurrow && *drop`). Registered in boolFlags alongside `materialize`.
- `marmot warren unregister --warren <id> [--force]` — refusal text names the blocking projects
  and the exact commands to run first.

Docs: new subsections in `docs/warrens.md:189-236` ("Consume a Warren") for all three; A4's error
strings already reference `burrow --drop` — verify the final flag spelling matches.

**Tests** (`internal/warren` + `cmd/marmot` via `captureRun`, `warren_test.go:459`): round-trip
mount→unmount leaves state deep-equal to pre-mount; unmount of editable project clears both lists;
drop of one of two burrowed projects keeps `Materialized` true, drop of the last clears it;
unregister refusal + `--force` removes cache tree; unmount with warren checkout deleted still works.

## C2 — `burrow` implies materialize

`warrenMount` (`cmd/marmot/warren.go:685-755`): when `isBurrow`, force `*materialize = true` after
parse; keep accepting the flag (compat) but it is inert — print
`"note: burrow always materializes; --materialize is implied"` when explicitly passed. `burrow`
without it was previously *exactly* `mount` (review) — behavior change is the point; migration
path: users who wanted plain mounts use `mount`, existing burrow-without-flag users gain caches on
next run, nothing breaks. Update `docs/warrens.md:229-236` (documented flag contract — findings
`docs`). **Plus the mid-loop failure fix** (findings `cmd-marmot`): today `warren.Mount` (:732)
records state before the `Materialize` loop (:736-752), so a mid-loop failure leaves mounted but
uncached projects. With C1's verbs available, on materialize error: best-effort
`warren.Unmount(workspaceRoot, *warrenID, remainingUnmaterialized)` and exit 1 with a message
naming what stayed mounted. Test: burrow with a source containing an unreadable file → partial
rollback asserted.

## C3 — `mount`/`burrow` with no projects requires `--all`

`warrenMount` :722-727 currently expands zero args to *every* manifest project — contradicting the
"nothing becomes queryable by accident" promise. Add `all := fs.Bool("all", false, ...)`
(registered in boolFlags at :690); the implicit expansion runs only under `--all`; bare zero-arg
invocation errors:
`"warren mount: specify project IDs or --all (N projects registered in warren %q)"`. Update
`docs/warrens.md:207` examples. Test: zero-arg mount exits 1 and leaves `_warren.md` unwritten;
`--all` mounts N.

## C4 — `warren edit` auto-mount messaging

Decision: **keep** the auto-mount (`SetEditable` adds to ActiveProjects, `warren.go:863` — an
edit-implies-visible model is right for agents) and make it audible. `warrenEdit`
(`cmd/marmot/warren.go:796-825`): before calling `SetEditable`, `warren.LoadWorkspaceState` and
check `containsName(entry.ActiveProjects, project)`; after success, when it was not previously
active print
`"Project %q in Warren %q is editable in this workspace (also mounted — edit implies mount)"`.
No library change. Test: capture stdout for both pre-mounted and unmounted cases.

## C5 — Lazy `ensureWorkspace`

`ensureWorkspace` (`cmd/marmot/warren.go:868-885`) MkdirAll's `.marmot/.marmot-data` and writes a
mock-provider `_config.md` — from **every** subcommand, including `list`/`status`. Add:

```go
// locateWorkspace resolves the marmot dir without creating anything.
func locateWorkspace(dirFlag string) (marmotDir, workspaceRoot string, err error) {
    if dirFlag == "" { dirFlag = discoverVault() }        // main.go:46-63
    if fi, statErr := os.Stat(dirFlag); statErr != nil || !fi.IsDir() {
        return "", "", fmt.Errorf("no marmot workspace at %s (run a mutating warren command, or marmot init, to create one)", dirFlag)
    }
    return dirFlag, filepath.Dir(dirFlag), nil
}
```

Switch the read-only verbs to it: `warrenList` (:653), `warrenStatus` (:765), `warrenRefresh`
(:833), `warrenPropose` (:854), plus C1's `unmount`/`unregister`/`--drop` (mutating *state* but
must never fabricate a workspace that isn't there). Keep `ensureWorkspace` in `warrenRegister`
(:632), `warrenMount` (:702), `warrenEdit` (:809). The mock-provider `_config.md` it writes
(:877-882) governs later real indexing — that foot-gun now fires only on genuinely
workspace-creating verbs; follow-up (Workstream D territory) could prompt for a provider, out of
scope here. Test: `warren list` in a bare temp dir errors and creates no `.marmot/`;
`warren register` still creates it.

## C6 — Unreachable-warren surfacing

Findings: `resolveWarrenID` (:887-907) never checks `entry.Path`; `Status` errors opaquely
(:889); moved/deleted checkouts vanish from `ActiveMounts` silently (A6 #1 adds the stderr
warning). Surface in three places:

1. `warren status` (`cmd/marmot/warren.go:757-794`): before the table, when
   `!dirExists(entry.Path)` print
   `"warren %q UNREACHABLE at %s — re-run 'marmot warren register %s <path>' or 'marmot warren unregister --warren %s'"`
   and still render rows from workspace state with AVAILABLE=false (requires `warren.Status` to
   degrade instead of erroring at :889 when the manifest is unreadable *and* the entry is known:
   return statuses built from `entry.ActiveProjects` with `Registered=false, Available=false`).
2. `warren list` (:646-683): add a REACHABLE column (`dirExists(entry.Path)`); JSON output gains
   `"reachable": bool` (additive).
3. Engine startup / reload: covered by A6 #1's `ActiveMounts` warning, which B2 made fire on every
   `ReloadWarrenState` — a daemon owner logs it when the checkout disappears at runtime.
   Doctor-level cross-checks are Workstream D. API side: `handleWarrenGraph`'s `skipped` array
   (A6 #9) is the UI's signal; `web/src/main.ts:184`'s collapse-to-default catch is left as-is
   (soft-failure UI work is optional polish, noted not planned).

Tests: status/list against a registered-then-deleted checkout; `warren.Status` degraded-row unit.

## C7 — Vault-ID collision refusal

Grammar constraint: vault IDs cannot contain `/` (`internal/traversal/bridged.go:82-92`); the
collision space is flat. Today: `Doctor` checks within one warren only (`warren.go:563-575` inside
`Doctor` :495), runtime is last-mount-wins (`pipeline.go:245`), `findWarrenMountByVault`
first-match can pick the wrong project (`internal/api/handlers.go:946-957`).

1. **Refuse at mount time** — inside `Mount`'s `updateWorkspaceState` closure (`warren.go:819-844`):
   build `claimed map[vaultID]{warrenID, projectID}` by walking **all** registered warrens' active
   projects (their vault IDs via `LoadProjectMetadata`/`sourceVaultID` :1508 on each project path)
   plus the local workspace's own vault ID (passed in by the caller or read from
   `<marmotDir>/_config.md`); for each project being mounted, resolve its vault ID the same way
   and refuse:
   `"vault ID %q of project %s/%s collides with %s/%s already mounted in this workspace"`.
   Unresolvable vault IDs (missing metadata) mount as today (they never reach the routing table —
   `pipeline.go:244` gates on `VaultID != ""`). Same check in `SetEditable`'s auto-mount path
   (:847-872) since it too adds to ActiveProjects.
2. **Warn at runtime** — B2's `ReloadWarrenState` already added the `rt.Get`-before-`Set` overwrite
   warning (belt-and-braces for state files written by old binaries or by hand).
3. `findWarrenMountByVault` stays first-match; with (1) refusing new collisions and (2) warning on
   legacy ones, changing its contract is unnecessary.

Tests: two-warren fixture (`testWarrenRoot`, `cmd/marmot/warren_test.go:410`) with a duplicated
vault ID — second mount refused, first intact; `SetEditable` path; legacy both-mounted state still
loads with the warning (no hard failure on existing users).

## C8 — MCP vs API `@`-write asymmetry: decided position

**Decision: align — accept `@vault/...` writes over MCP for active editable mounts.** Rationale:
the HTTP API already allows exactly this (`handleWarrenNodeUpdate`,
`internal/api/handlers.go:396-468`), the UI gates on `provenance.editable`
(`web/src/detail-panel.ts:221-253`), and the primary consumers of warren context are MCP agents —
telling *them* alone to "use the API/UI path" (current error, `internal/mcp/handlers.go:288`) is
the asymmetry. This is a **documented contract change**: `docs/warrens.md:281-283` explicitly
states MCP rejects `@` IDs — rewrite that paragraph in the same PR (findings `docs` flags this as
intentional-doc, not bug-doc).

Implementation (the one C item that is medium-sized; it may trail the rest of PR 3):

1. **Shared write-back helper** so MCP and API cannot diverge — in `internal/warren` (which after
   A2 already imports `internal/embedding`):
   `func WriteEditableNode(mount ProjectStatus, n *node.Node, vec []float32, summaryHash, model string) error`
   — node save via `node.NewStore(mount.Path)` + embedding upsert into
   `<mount.Path>/.marmot-data/embeddings.db` (read-write `NewStore`, the legitimate write open A1
   preserves), propagating errors per A6 #3. Refactor `handleWarrenNodeUpdate` onto it.
2. `HandleContextWrite` (`internal/mcp/handlers.go:287-290`): replace the unconditional rejection —
   parse via the same qualified-ID split (share `splitQualifiedVaultID`; move it from
   `internal/api/handlers.go:934-944` into `internal/warren` or `internal/traversal` so both
   import one copy), `warren.ActiveMounts(e.MarmotDir)`, find the mount by vault ID; if found and
   `mount.Editable`: build the node (existing arg validation, **skip** the namespace auto-prefix
   at :299-301 — the ID is remote-vault-scoped), call `WriteEditableNode`, then
   `e.VaultRegistry.Refresh(vaultID)` (tolerating B1's `ErrNotLoaded`), and return the normal
   success payload with a provenance note. If not found / not editable, keep a rejection whose
   text now says *why*: `"vault %q is not an editable warren mount in this workspace; run 'marmot warren edit --warren <id> <project>'"`.
3. A4 interplay: `mount.Path` is checkout-preferred for editable projects after A4's
   `preferredProjectPath` fix — MCP writes land in the checkout, same as API.

Tests: rewrite `TestContextWriteRejectsMountedWarrenID` (`internal/mcp/server_test.go:217-236`)
into the matrix {editable → write lands under checkout + searchable after refresh; mounted
read-only → rejection text; unmounted vault → rejection}; equivalence test asserting MCP and API
writes of the same payload produce byte-identical node files (guards the shared-helper refactor).
Risk: agents gain write access to shared checkouts — bounded by editable being an explicit
per-project per-workspace opt-in, and superseded by Workstream D's manifest `readonly` policy,
which `WriteEditableNode` should be the single enforcement point for.

## C9 — Stubs presented as features

`refresh` becomes real in B3.2. `propose` (`cmd/marmot/warren.go:847-866` — not to be confused
with `SetEditable` at `internal/warren/warren.go:847-872`) stays advisory until
[D3](#d3--real-warren-propose-review-tier-43):
change its output to be honest —
`"warren propose is not yet implemented; to propose changes: cd %s && git checkout -b <branch> && git add <project files> && git commit && push/PR"`
(with `entry.Path` from the C5/B3.2 `resolveWarrenEntry`), and mark it `(experimental)` in
`warrenUsage` (:69-71) and the README/`docs/warrens.md` command tables. Exit code stays 0 (it
successfully gave guidance). Test: output contains "not yet implemented" and the checkout path.

---

**Workstream C packaging.** One PR ("warren: Tier 3 UX verbs and defaults"), commit order C1 →
C2/C3 (depend on C1's rollback/`--all` plumbing) → C4/C5/C6/C7/C9 (independent) → C8 last
(largest, contract change, can split into its own PR if review load demands). Every new/changed
verb: doc section in `docs/warrens.md`, usage-line update, `reorderInterspersedFlags`
registration, and a `captureRun`-based CLI test. Behavior changes visible to existing users: C2
(burrow materializes), C3 (bare mount errors), C8 (MCP accepts editable @-writes) — all three
called out in the PR description and CHANGELOG-style doc note.

# Workstream D: Capability roadmap (Tier 4)

Optional scope. Every item below carries explicit **defer-if** criteria — Tier 4 is where the
program stops if priorities shift, and nothing in A–C depends on any D item landing. Ordering
honors the review's sequencing: **D1 first** (refresh unlocks staleness UX), **D3+D4 together**
(they define the collaboration story), D2 feeds D1, D5/D6 are small and slot in wherever
convenient. Packaging: **PR 4a = D1+D2**, **PR 4b = D3+D4+D6**, **PR 4c = D5** — each
independently shippable and revertible (auto-release constraint, see
[Testing & Rollout](#testing--rollout)).

Precedent note: production `cmd/marmot` currently execs no git (`exec.Command` appears only in
`cmd/marmot/pipeline.go:766` for browser-open and in the separate `cmd/marmot-eval` binary). D1
and D3 introduce the first production git execs — keep them as small unexported helpers in
`cmd/marmot/warren.go` (`gitOutput(dir string, args ...string) (string, error)` wrapping
`exec.Command("git", append([]string{"-C", dir}, args...)...)`), so `internal/warren` stays
exec-free and unit-testable without a git binary.

---

## D1 — Real `warren refresh --pull` (review Tier 4.1)

**Today (after B3.2):** `marmot warren refresh` reloads state and touches the workspace
`_warren.md` to signal live owners, but never touches git — B3.2 explicitly reserved a `--pull`
flag for this item. Real upstream tracking = pull the checkout, then reload.

**Changes** (all in `cmd/marmot/warren.go`, `warrenRefresh` — B3.2's rewritten body):

1. Flag: `pull := fs.Bool("pull", false, ...)`, registered in the verb's
   `reorderInterspersedFlags` boolFlags (Workstream C dispatch constraint). Without `--pull`,
   behavior is exactly B3.2 (reload + touch), unchanged.
2. Git guard: `gitOutput(entry.Path, "rev-parse", "--is-inside-work-tree")` fails →
   `"warren %q at %s is not a git checkout; --pull requires git (run 'marmot warren refresh' without --pull to reload state only)"`,
   exit 1.
3. **Dirty-checkout handling — refuse, never stash:**
   `gitOutput(entry.Path, "status", "--porcelain")` non-empty → exit 1 with
   `"warren checkout has %d uncommitted change(s) (editable-mount edits?); commit or stash them, or run refresh without --pull"`.
   Editable mounts legitimately dirty the checkout (edits land there per A4), so auto-stash or
   `checkout --force` would destroy user work — the refusal message names the likely cause.
4. Pull: `git -C <path> pull --ff-only`. Non-fast-forward or network failure → print git's stderr
   and exit 1 with `"resolve in the checkout manually, then re-run refresh"`. Never merge, rebase,
   or reset on the user's behalf.
5. **Re-materialize stale burrows:** for each project in `entry.ActiveProjects` with an existing
   burrow cache (`dirExists(materializedProjectPath(...))`, `warren.go:1021`), compare the D2
   provenance `source_commit` against the fresh `rev-parse HEAD`; if different **or provenance is
   absent/unreadable**, re-run `warren.Materialize` (A3's atomic copy-aside-then-swap makes this
   safe under a live engine). If D2 has not landed, degrade to re-materializing all burrowed
   projects unconditionally — correct, just more copying.
6. Finish with B3.2's `warren.TouchWorkspaceState(workspaceRoot)` so daemon owners reload (B3.3),
   and print old→new short commits plus the re-materialized project list.

**Docs:** `docs/warrens.md` refresh section (rewritten by B3.2/C9) gains the `--pull` contract:
ff-only, dirty-refusal, burrow re-materialization. `warrenUsage` line updated.

**Tests:** e2e scenario E6 (see [Testing & Rollout](#testing--rollout)) — local bare origin +
clone; unit-level tests for the dirty-refusal and non-git branches behind an
`exec.LookPath("git")` skip (CI runners all ship git). Provenance-absent fallback pinned by a
test that deletes `provenance.md` before refresh.

**Defer if:** manual `git -C <warren> pull` + `marmot warren refresh` (B3.2) already achieves the
same result in two commands and no user has asked for one; or B3's touch-file reload has been
released for less than one cycle (let the freshness model settle before layering git automation
on it).

---

## D2 — Burrow commit-pinning / provenance (review Tier 4.2)

**Today:** `Materialize` (`warren.go:972-980`) copies bytes and records nothing — `warren status`
cannot say how stale a cache is, and D1 cannot tell which burrows need re-materializing.

**Schema.** New file written by `Materialize` after the A3 swap succeeds, as a sibling of the
cache: `<marmotDir>/.marmot-data/warrens/<warrenID>/projects/<projectID>/provenance.md`
(frontmatter emitted via the existing `writeMarkdownYAML` writer, `warren.go:1127-1166`, parsed
via A7's `frontmatter.Split` — both already round-trip safe):

```yaml
---
source_commit: "<git -C warrenRoot rev-parse HEAD, or empty when not a git repo>"
source_path: "<project.Path from the manifest>"
materialized_at: "2026-07-09T12:00:00Z"   # RFC3339
manifest_version: 1
---
```

Go side: `type BurrowProvenance struct` in `internal/warren` with
`Load/SaveBurrowProvenance(workspaceMarmotDir, warrenID, projectID string)`. Getting the commit
needs git: `Materialize` cannot exec (package stays exec-free, above), so it takes the commit as
a parameter — signature grows to
`Materialize(workspaceMarmotDir, warrenID string, project Project, warrenRoot, sourceCommit string)`;
the sole caller (`cmd/marmot/warren.go:747`) resolves it via `gitOutput(warrenRoot, "rev-parse", "HEAD")`
and passes `""` on error (non-git warren → provenance carries an empty commit and staleness
degrades to "unknown age; re-materialize on refresh", which D1 step 5 already treats as stale).
A crash between swap and provenance write leaves a cache without provenance = treated stale =
re-copied next refresh; no torn state possible.

**Consumers:**

1. `warren status` (`cmd/marmot/warren.go:757-794`): for materialized projects, append a cache
   line — with git available and a non-empty pinned commit,
   `"cache at <short> (%s behind)"` via
   `git rev-list --count <source_commit>..HEAD`; otherwise `"cache from <materialized_at>"`.
   Failures degrade to the date form (A6 convention: warn, don't break status).
2. D1 step 5's staleness gate (the primary consumer).
3. C1's `DropMaterialized` removes the whole `projects/<p>/` dir, so provenance is deleted with
   the cache automatically — verify in C1's drop test once D2 lands.

**Tests** (`internal/warren` + `cmd/marmot`): materialize with a fake commit string → provenance
round-trips; empty-commit path; `warren status` behind-count rendering (git-dependent parts
e2e-only, E6); drop removes provenance.

**Defer if:** D1 is deferred (D2's main consumer disappears — `warren status`'s C6
reachable/available columns plus `materialized_at`-less mtime display are then adequate); or
burrow usage is negligible in practice (no stale-cache reports).

---

## D3 — Real `warren propose` (review Tier 4.3)

**Today (after C9):** `warrenPropose` (`cmd/marmot/warren.go:847-866`) prints honest "not yet
implemented" guidance. The edit feature (A4 fixed, C8 aligned) writes into the warren checkout —
propose is the write-back loop that turns those edits into a reviewable git artifact.

**Flow** (all in `cmd/marmot/warren.go`, using `gitOutput`; `resolveWarrenEntry` from B3.2):

1. `marmot warren propose --warren <id> <project>` (project required when
   `entry.EditableProjects` has more than one; defaults to the single editable project).
   Guards: warren reachable (C6), git work tree (as D1 step 2), **not detached HEAD**
   (`git symbolic-ref --short HEAD` fails → refuse: propose must be able to return to a branch).
2. Scope check: `git status --porcelain -- <project.Path>` empty →
   `"nothing to propose for %q (no changes under %s)"`, exit 0.
3. Branch: `marmot/propose/<projectID>-<yyyymmdd-hhmmss>`; refuse if it already exists
   (`git rev-parse --verify` succeeds) — timestamped names make this near-impossible, the check
   is belt-and-braces.
4. Mechanics, pathspec-limited so unrelated dirty files are never swept in:
   `git checkout -b <branch>` → `git add -- <project.Path>` →
   `git commit -m "marmot propose: <projectID> context updates"` → `git checkout <prevBranch>`.
   The commit moves the project changes onto the branch; other dirty files stay in the working
   tree untouched.
5. Print: branch name, `git -C <path> push -u origin <branch>` and open-a-PR instructions.
   Exit 0. Marmot **never pushes** — network credentials and remote policy stay the user's.
6. Failure handling: any git step failing triggers a best-effort `git checkout <prevBranch>` and
   prints the exact repo state (current branch, staged files) — never delete or reset anything.

**Conflict stance (decided):** propose is local-only branch creation. It never pulls, merges,
rebases, or force-pushes; divergence from upstream is discovered and resolved by humans at
push/PR time through normal git flow. Concurrent proposes are serialized by git's own index lock
plus the branch-exists refusal; no marmot-side lock is needed (propose does not RMW `_warren.md`
or the manifest, so A5's flocks are not involved).

**Docs:** rewrite `docs/warrens.md`'s propose paragraph and drop C9's `(experimental)` marker
from `warrenUsage` and the README command table.

**Tests:** e2e E6 (edit via C8 → propose → branch exists with exactly one commit containing only
the project's files; working tree back on the original branch; a dirty file outside the project
untouched); unit tests for the detached-HEAD/no-changes/branch-exists refusals behind the
`LookPath("git")` skip.

**Defer if:** C9's advisory text is judged sufficient (single-author warrens where the consumer
*is* the author commit directly); or **D4 is deferred** — the review sequences propose and
manifest policy together because a write-back loop without author-side write policy invites
proposals the author never sanctioned. If D4 defers, defer D3.

---

## D4 — Manifest read-only policy (review Tier 4.4)

**Today:** writability is decided entirely by each consumer (`warren edit`); the warren author
has no say. Docs (`docs/warrens.md:266-299`) frame editability as consumer-side only.

**Schema.** `Project` (`internal/warren/warren.go:33-37`) gains one field:

```go
type Project struct {
    ProjectID string   `yaml:"project_id" json:"project_id"`
    Path      string   `yaml:"path" json:"path"`
    Aliases   []string `yaml:"aliases,omitempty" json:"aliases,omitempty"`
    ReadOnly  bool     `yaml:"readonly,omitempty" json:"readonly,omitempty"` // author-side write policy
}
```

Manifest `Version` bumps to **2** when any project sets `readonly` (see D6, which ships in the
same PR): a version-2 manifest edited by a pre-D6 binary would silently strip the field on struct
round-trip (`normalizeManifest`, `warren.go:1172-1183`, drops unknown YAML) — the version ceiling
makes post-D6 binaries refuse instead. Pre-D6 binaries remain a documented hole (see
[Risks](#risks--mitigations) R14).

**Enforcement points — exactly three, all server-side** (the UI already gates on
`provenance.editable`, `web/src/detail-panel.ts:221-253`, and needs no change):

1. `SetEditable` (`warren.go:847-872`): inside the closure, load the warren manifest from
   `entry.Path` and refuse when the target project is `ReadOnly`:
   `"warren author marked project %q read-only; edits must go through the warren repository itself"`.
   Manifest unreachable → **fail-open with a stderr warning** (consistent with A6's read-path
   convention; a broken checkout must not brick the workspace) — the write-time check below is
   the backstop.
2. `warren.WriteEditableNode` (C8's shared MCP/API write helper — deliberately the **single**
   write-path enforcement point, as C8 anticipated): re-read the manifest at write time and
   refuse `ReadOnly` projects even when a stale mount state still lists them as editable. Here
   fail-**closed** (manifest unreadable → refuse the write): a write is an explicit action whose
   refusal is visible, unlike a read.
3. `ActiveMounts`/`Status` status construction (`warren.go:875-970`, incl.
   `materializedStatuses` :982): compute
   `ProjectStatus.Editable = editable && !readonly` so `Provenance.Editable` — and with it the
   UI save button and MCP rejection text — reflect policy without any client change.
   (`web/src/types.ts` shapes are untouched; `readonly` also surfaces additively on the
   `/api/warrens` project JSON for UI badges, optional.)

**Author-side CLI:** `marmot warren project set-readonly <projectID> [--off]` — warren-repo-side
verb (dispatch via `resolveWarrenRoot`, `cmd/marmot/warren.go:601-619`, **not**
`ensureWorkspace`), mutating through A5's `updateManifest` so it is flocked and, with D6, version
checked.

**Docs:** new "Write policy" subsection in `docs/warrens.md` next to the editable-mount section
(:266-299).

**Tests:** unit matrix — `SetEditable` refusal; `WriteEditableNode` refusal with a stale
editable mount state (flag flipped after `warren edit`); `ActiveMounts` reports
`Editable=false`; fail-open/fail-closed branches. Integration: C8's MCP/API equivalence matrix
extended with a readonly row (both surfaces reject identically).

**Defer if:** C8 is deferred (without the shared `WriteEditableNode` helper, enforcement would
have to be duplicated in two write paths and would drift — do not build D4 on the pre-C8 split
paths); or all known warrens are single-author (policy is moot when author == only consumer).
Interim mitigation while deferred: simply don't grant `warren edit` — editability is already an
explicit per-workspace opt-in.

---

## D5 — Doctor additions (review Tier 4.5 + promises made by A1/A5/C7)

All additive `DoctorIssue` codes. After A2, `internal/warren` already imports
`internal/embedding`, so warren-side DB checks need no new dependency; all DB opens use A1's
`NewStoreReadOnly` (doctor must never mutate the vaults it inspects — the exact A1 lesson).

**D5.1 Model skew** (`model_skew`, warning). Remote searches silently return nothing when models
differ: `SearchActive` filters `WHERE model = ?` (`internal/embedding/store.go:349`) and
`checkModel` (:268) reports mismatch counts. Add `Store.Models() ([]string, error)`
(`SELECT DISTINCT model FROM embeddings`, read-only-safe). Two check sites:

- **Warren-side** `warren.Doctor` (`warren.go:495+`, extending the existing per-project
  `embeddings.db` stat at :576): collect each project's model set; when projects disagree, emit
  `model_skew` naming both projects and models ("cross-project semantic search between these will
  return no results").
- **Workspace-side, at mount time** (the review says "doctor *and mount* should warn"):
  `warrenMount` (`cmd/marmot/warren.go:685-755`) compares the workspace's configured model
  (`config.EmbeddingModel`, `internal/config/config.go:23`) against the mounted project's stored
  models and prints a stderr warning on mismatch. Warning only — mounting stays legal (the user
  may be about to re-index).

**D5.2 Stale remote schema** (`schema_stale`, warning). Delivers the follow-up A1's risk note
promised: a project DB lacking the `status` column (pre-migration vault) now *fails*
`SearchActive` instead of being silently migrated — doctor detects it via read-only
`PRAGMA table_info(embeddings)` and says "indexed by an older marmot; re-import the project".

**D5.3 Cross-warren vault-ID collisions** (`vault_id_collision_workspace`, error). C7 refuses
*new* collisions at mount time; legacy state written by older binaries can still carry them.
New **workspace-side** mode — `marmot warren doctor --workspace` (dispatch:
`ensureWorkspace`-family via C5's `locateWorkspace`, not `resolveWarrenRoot`) — walks all
registered warrens' active projects plus the local vault ID (same `sourceVaultID` resolution C7
uses, `warren.go:1508`) and reports duplicates. Reuses C7's `claimed` map builder — implement
that builder once in `internal/warren` and call it from both.

**D5.4 Absolute manifest paths** (`absolute_project_path`, warning; **beyond the review
inventory** — a findings-pass discovery, kept because it is ~10 lines; drop first if trimming
scope). `validateProjectPath`
(`warren.go:1258-1287`) deliberately **accepts** absolute paths at runtime (:1266-1268) while
rejecting relative escapes — but an absolute `path:` committed to a warren repo only resolves on
the author's machine. Doctor warns: "project %q uses an absolute path; the warren will not work
when cloned elsewhere".

**D5.5 Lockfile gitignore hygiene** (`lockfile_not_ignored`, info). The doctor note A5 step 6
promised: warn when the warren repo's `.gitignore` lacks `_warren.md.lock`.

**Tests:** fixture-based doctor units per code — two projects Upserted with different model
strings (direct `Store.Upsert(..., model)` calls; no embedder needed — note the e2e fixture has
only one mock model, so model-skew stays unit-level); a `status`-column-dropped DB built with raw
SQL; a hand-written duplicate-vault-ID two-warren workspace (template `testWarrenRoot`,
`cmd/marmot/warren_test.go:410`); absolute-path manifest; gitignore present/absent.

**Defer if:** each sub-check is independently deferrable, and all five only *report* — the whole
item is the safest D deferral. Specifically: defer D5.3 once C7 has been released for a cycle
(mount-time refusal prevents new occurrences); defer D5.1's mount-time warning if it proves noisy
in multi-model shops; D5.2/D5.4/D5.5 are ~10 lines each and should ride along with whichever PR
touches doctor first rather than being scheduled.

---

## D6 — Manifest version discipline (review Tier 4.6)

**Today:** `normalizeManifest` (`warren.go:1172-1183`) backfills `Version` 0→1 and never checks
an upper bound; unknown YAML fields are silently dropped on any Load→Save round-trip because
parsing goes through fixed structs.

**Changes** (~20 lines; ship inside PR 4b with D4, whose `readonly` field is the first consumer):

1. `const CurrentManifestVersion = 1` in `internal/warren` (becomes 2 in the same commit D4's
   field lands).
2. **Read paths stay permissive:** `LoadManifest` on `Version > CurrentManifestVersion` succeeds
   with a stderr warning (A6 convention) —
   `"warning: warren manifest %s is version %d (this marmot supports <= %d); fields may be ignored, do not edit with this binary"`.
   Queries against a newer warren keep working best-effort.
3. **Write paths refuse:** A5's `updateManifest` helper is the single mutating choke point for
   all eight manifest ops — one guard there:
   `if m.Version > CurrentManifestVersion { return fmt.Errorf("manifest version %d exceeds supported %d; upgrade marmot before editing this warren", ...) }`.
   This is what actually prevents the silent unknown-field stripping: a binary that doesn't know
   a field also doesn't know its version, and now refuses to save.
4. **Unknown-field preservation — decided against (for now):** true preservation requires
   switching Load/Save to a `yaml.Node`-based round-trip, a much larger change with its own
   formatting churn; the version ceiling makes it unnecessary until a concrete version-3 field
   exists. Record the decision as a code comment on `CurrentManifestVersion`.

**Tests:** version-3 fixture — `LoadManifest` warns and succeeds, `AddProject` (via
`updateManifest`) refuses; version-0 backfill to 1 pinned (existing behavior); D4's version-2
manifests accepted once the constant bumps.

**Defer if:** D4 is deferred **and** no other manifest schema change is queued — a ceiling with
nothing above it protects nothing. But given its size, fold it into any PR that touches
`updateManifest` rather than ever scheduling it standalone.

---

# Testing & Rollout

Per-item tests are specified inline in A1–A8, B1–B4, C1–C9, D1–D6 above and are **not repeated**
here; this section maps them onto the repo's four suites, defines the first-ever warren e2e
scenarios (the review confirmed `grep -ri warren e2e/` = 0 hits), and sequences the PRs under the
auto-release constraint.

## Test matrix — suites, harnesses, and what lands where

| Suite | Entry point | Harness helpers to reuse (from findings) | Warren-program content |
|---|---|---|---|
| **Unit** | `go test -race -count=1 -timeout 300s ./...` (`ci.yml:46`) | `journalMode` (`internal/embedding/store_wal_test.go:12-24`); exec-helper flock template (`internal/daemon/lock_test.go:106-195`); concurrent-write template (`internal/mcp/concurrency_test.go:13-55`); `setupRemoteVault` (`internal/namespace/registry_embstore_test.go:11-23`); `writeImportSourceVault` + `mustNotExist` (`internal/warren/warren_test.go:722-774`); symlink-skip template (:318-341); `captureRun`/`testWarrenRoot` (`cmd/marmot/warren_test.go:459,410`); `setupAPIWarren` + `seedRemoteEmbedding` (`internal/api/api_test.go:135-189`); `mockVaultProvider` (`internal/traversal/bridged_test.go:93-98`); corrupt-YAML template (`internal/routes/routes_stress_test.go:257-305`) | All A-item tests (checkpoint w/ real SQLite, copier hardening, frontmatter table, flock single-process); B1 Rebuild/TTL/Refresh-under-search; C verb round-trips; D4/D5/D6 fixtures. New packages `internal/flock`, `internal/frontmatter` tested here. Hermeticity rule: `t.Setenv("MARMOT_ROUTES", "off")` for anything calling `buildEngine` (A8's `hermeticEngine` helper). |
| **Integration** | `go test -tags integration -race -count=1 ./internal/` (`ci.yml:49`) | Same exec-helper pattern, cross-package | A5 multi-process `_warren.md`/routes RMW; B4 registry-shared-lock vs `index --force`; C8 MCP↔API write-equivalence matrix. |
| **e2e (Go)** | `make e2e` = `go test -tags e2e -count=1 -v ./e2e/` (`Makefile:27-35`; CI `ci.yml:78-108`, macOS leg) | `TestMain`/`binPath` + `MARMOT_E2E_BIN` (`e2e/e2e_test.go:34-65`); `seedProject`/`hermeticEnv`/`runCLI` (:69-125); `mcpSession`/`startMCP{,Daemon,Env}`/`callTool` (:230-406); contention templates `TestConcurrentServes`/`TestIndexDuringServe` (:476-679); daemon lifecycle `readDaemonInfo`/`assertVaultReleased`/`waitDaemonOwner` (:681-982); mock-embedder fixture (`e2e/fixture/vault/_config.md`) | Scenarios E1–E6 below, new file `e2e/warren_test.go` (`//go:build e2e`). **Isolation is `HOME=<tmp>` via `hermeticEnv`, never `MARMOT_ROUTES=off`** — warren e2e *needs* routes to function (A8 scope note); warren state lands inside the temp HOME where tests can inspect it. |
| **Playwright (UI)** | `make e2e-ui` → `web/e2e/ui.spec.ts` + `web/e2e/serve.sh` (CI `ci.yml:123`, ubuntu leg) | `serve.sh` HOME-isolation pattern; dead-endpoint/request-tracking pattern (`web/e2e/regressions.spec.ts:161`) | E7 (optional): warren dropdown + refresh-button POST. |

## The first warren e2e scenarios (`e2e/warren_test.go`)

Shared builder `seedWarren(t) (warrenRoot, consumerProj string)`: `seedProject` twice for source
projects; `runCLI` `warren init` in a third temp dir + `warren import` both projects; a third
`seedProject` as the consumer workspace; `git init`+commit the warren root (git needed only by
E6). Each scenario states the PR it lands with — e2e coverage grows *with* the fix it pins, never
after.

- **E1 — register→mount→query through a real serve** *(lands with PR A — the baseline the review
  says doesn't exist)*: register + mount both projects (one via `burrow --materialize` — the
  explicit flag, since C2's implied materialization lands only in PR 3 — so the A3 cache
  assertion below has a cache to inspect); CLI `query` and MCP `context_query`
  return `@vault/`-prefixed remote results. Assertions that pin Tier 1: after the queries, the
  warren checkout has **no new `-wal`/`-shm` sidecars and no schema change** (A1); the imported
  `embeddings.db` is row-complete despite the source WAL being hot at import time (A2); burrow
  cache contains no `.env`/sidecars (A3).
- **E2 — concurrent CLI mutation** *(PR A)*: two processes `warren mount`-ing distinct projects
  into one workspace; both survive in `_warren.md` (A5, cross-process for real).
- **E3 — mount-while-owner-live** *(PR B — pins the freshness model)*: `startMCPDaemon` on the
  consumer; from a second process, register+mount, then `marmot warren refresh`; the live
  session's `context_query` now returns remote-vault results (B2/B3 end to end, via the
  `_warren.md` touch → owner watcher → `ReloadWarrenState` chain; uses `waitDaemonOwner`).
- **E4 — cross-workspace `index --force` refusal** *(PR B)*: process A serves with a warren
  mount resolved; process B's `index --force` on the mounted vault exits non-zero with the B4
  refusal; succeeds after A exits (shape of `TestConcurrentServes`).
- **E5 — editable write-back + burrow lifecycle** *(PR C)*: `warren edit`, MCP `context_write`
  with an `@vault/...` ID (C8) → node file lands under the **checkout**, query-after-refresh
  finds it; then `burrow` (materializes without a flag, C2) → rename the checkout away → query
  still served from cache → `burrow --drop` + `unmount` + `unregister` → workspace state clean
  (C1).
- **E6 — git roadmap loop** *(PR 4a/4b)*: local bare repo as origin, clone as the warren;
  upstream commit lands; `warren refresh --pull` fast-forwards and re-materializes the burrow
  (D1+D2, provenance commit advances); dirty checkout → refusal; `warren propose` after an
  editable write creates `marmot/propose/...` with exactly one pathspec-limited commit and
  restores the original branch (D3).
- **E7 — UI refresh (optional, PR B or C)**: extend `web/e2e` — warren dropdown populated;
  clicking `#refresh-btn` in a `_warren/<id>` namespace fires `POST /api/warren/<id>/refresh`
  (request-tracking pattern) and no console errors. Requires teaching `web/e2e/serve.sh` to build
  a mounted warren fixture; defer-able without losing Go-side coverage of the same endpoint.

## CI implications

- **No workflow edits needed for E1–E6:** new tests ride the existing `e2e` build tag; `make e2e`
  already runs in the CI `e2e` job (`ci.yml:78-108`). E7 rides `make e2e-ui` (`ci.yml:123`).
- **git in tests:** all GitHub-hosted runners ship git; still guard git-exec tests with
  `exec.LookPath("git")` + `t.Skip` for minimal local environments.
- **Runtime budget:** the unit job has a hard `-timeout 300s` (`ci.yml:46`) — anything with
  debounce waits, polling, or process spawning goes behind the `integration`/`e2e` tags, never in
  the default run. The e2e job has no such cap but keep per-scenario deadlines (the
  `mcpSession` per-call deadline pattern) so hangs fail fast.
- **Platform gaps:** CI is ubuntu + macos only — the Windows flock degradation paths (A5, B4)
  are never CI-executed; compile them via `GOOS=windows go build ./...` in the Build step
  (already implied by `go build ./...` only for the host — add nothing; instead document the gap
  in the `internal/flock` package comment, as A5 specifies).
- **Coverage:** no CI gate exists (see the corrected Workstream A note); keep new packages at the
  repo's ≥80% convention via their same-commit unit tests.

## PR sequencing under auto-release-on-main

`auto-tag.yml` tags and goreleaser-releases on **every successful CI run for a push to main**
(patch bump by default; `#minor`/`#major` commit-message tokens; release job builds signed
darwin binaries). `workflow_run` fires on whole-workflow success, so the e2e job is already a
release gate. Consequences and the resulting sequence:

1. **Every merge ships.** No partial features on main: each PR lands whole — code + tests + doc
   updates + usage text — or not at all. D items are individually whole by construction.
2. **Order: PR 1 (A) alone → PR 2 (B) → PR 3 (C) → PR 4a (D1+D2) → PR 4b (D3+D4+D6) → PR 4c
   (D5).** A merges alone and soaks one release before B (B reuses `internal/flock` and the A6
   conventions; a correctness-only release also makes any regression bisectable to one tier).
   C8 may split out of PR 3 if review load demands (its own note) — it then precedes 4b, which
   depends on it.
3. **Version tokens:** PR 1 = patch (pure correctness). PR 2, 3, 4a, 4b = `#minor` in the merge
   commit (new endpoint semantics, new verbs, behavior changes C2/C3/C8, new git verbs). PR 4c =
   patch. Call the three C behavior changes out in the PR description — that text is what lands
   in the goreleaser notes.
4. **Rollback = revert commit = another automatic release.** Keep every PR revertible: no
   cross-PR data migrations. On-disk format additions are all ignorable by older binaries —
   `.lock` files (A5), `provenance.md` (D2) — except D4's `readonly` field, which is exactly why
   D6's version ceiling ships in the same PR (see [Risks](#risks--mitigations) R14).
5. **Branch reality:** this plan's line refs are pinned to `multiprocess-lock-fix` @ `1f14f3e`.
   Land (or rebase onto) main first; re-verify the load-bearing line refs flagged in each item
   during implementation — the plan cites functions by name alongside every line number for
   exactly this drift.

---

# Risks & Mitigations

Program-wide table; per-item details live in the workstream sections cited.

| # | Risk | Source | Impact | Mitigation |
|---|---|---|---|---|
| R1 | Read-only opens fail on pre-`status`-column remote vaults instead of silently migrating them | [A1](#a1--read-only-opens-for-remote-vault-dbs-review-11) | Remote search errors for stale vaults | Error surfaced per A6 (was swallowed); doctor `schema_stale` (D5.2) names the fix; strictly better than mutating someone's checkout |
| R2 | `wal_checkpoint(TRUNCATE)` blocks up to 5s under a persistent reader | [A2](#a2--checkpoint-before-copying-a-live-db-review-12) | Import/burrow latency | Rare, interactive operations; busy_timeout bounds it; failure degrades to documented point-in-time copy with a warning |
| R3 | Re-burrow's `RemoveAll`+rename swaps a cache a live engine routes to | [A3](#a3--one-hardened-copier-for-burrow-and-import-review-13) | Transient miss during the rename syscall | Copy-aside-then-swap keeps the window to one rename; DB readers hold fds not paths; B's reload formalizes; noted in PR |
| R4 | Users with legacy editable+materialized state see writes move to the checkout | [A4](#a4--refuse-editable--materialized-review-14) | Behavior change | It is the documented contract (`docs/warrens.md:277-278`); stderr notice when the override fires |
| R5 | BSD flock semantics on NFS; Windows runs unlocked | [A5](#a5--flock-the-_warrenmd--routesyml-read-modify-writes-review-15), B4 | Lost-update protection absent there | Same exposure `internal/daemon` already accepted; Windows degrades to today's atomic-write semantics, documented in the package comment; never CI-tested (ubuntu+macos only) — flagged in Testing |
| R6 | A6/A7 make previously-silent states loud (warnings, parse errors) | [A6](#a6--un-swallow-the-errors-review-16)/[A7](#a7--anchored-frontmatter-parsing-review-17-widened) | Perceived noise; strictness change | Once-per-vault dedup for query-path warnings; parser change pinned by round-trip tests — files that "worked" via mid-line `---` were already being corrupted on save |
| R7 | TTL window: registry view lags disk up to 60s | [B1](#b1--vaultregistryrebuild--swap-then-close-refresh--graph-ttl) | Stale cross-vault results | Bounded (vs. unbounded today); in-band changes bypass it via `ReloadWarrenState`; `MARMOT_WARREN_TTL` tunable/off |
| R8 | Search racing `Refresh`/`Rebuild` hits a just-closed store | B1 | One failed query | Swap-then-close shrinks the window to already-resolved handles; error surfaced (A6 #6/#7), never a silent empty result; refcounting rejected as not worth it — documented on the method |
| R9 | Reload storms from `_warren.md` watcher; reload racing `Engine.Close` | [B3](#b3--wire-the-three-triggers) | Wasted reloads; shutdown race | Existing 1s debounce; `closing` flag gates `ReloadWarrenState` to a no-op after close begins |
| R10 | One shared read-flock fd per cached remote vault | [B4](#b4--cross-workspace-index---force-hazard-decision) | fd growth | Bounded by mount count; released in `Close`/`Rebuild`/`Refresh` paths (kernel releases on exit regardless) |
| R11 | C2/C3 change documented CLI behavior; scripts relying on bare `mount` or non-materializing `burrow` break | [Workstream C](#workstream-c-ux-repairs-tier-3) | Script breakage | Loud errors (not silent semantic change); `#minor` release + notes; `--all` migration is one flag |
| R12 | C8 grants MCP agents write access to shared checkouts | [C8](#c8--mcp-vs-api--write-asymmetry-decided-position) | Unwanted writes reach a warren | Editable is an explicit per-project per-workspace opt-in; propose (D3) keeps pushes human; D4 gives authors a veto at the single `WriteEditableNode` choke point |
| R13 | Git automation (D1 pull, D3 branch/commit) mangles a user's checkout | [D1](#d1--real-warren-refresh---pull-review-tier-41)/[D3](#d3--real-warren-propose-review-tier-43) | Data loss in user repos | ff-only pulls; dirty-checkout refusal (never stash/reset); pathspec-limited commits; detached-HEAD refusal; marmot never pushes; failure paths restore the prior branch and delete nothing |
| R14 | Pre-D6 binaries silently strip D4's `readonly` field on manifest round-trip | [D4](#d4--manifest-read-only-policy-review-tier-44)/[D6](#d6--manifest-version-discipline-review-tier-46) | Author policy loss | Version bump to 2 + write ceiling in the same PR stops post-D6 binaries; pre-D6 binaries are a documented residual hole (write-time re-check in `WriteEditableNode` still enforces on current binaries) |
| R15 | Auto-release ships every merge immediately | [Testing & Rollout](#pr-sequencing-under-auto-release-on-main) | Regressions reach users fast | Whole-PR discipline; e2e job gates the release workflow; revert = instant next release; A soaks alone before B |
| R16 | Test-suite growth blows the 300s unit timeout or slows CI | Testing & Rollout | CI friction | Debounce/polling/multi-process tests live behind `integration`/`e2e` tags; per-call deadlines in e2e keep hangs fast-failing |
