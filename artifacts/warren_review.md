# Warren System Review & Improvement Proposal

Based on two deep code reviews (core architecture: `internal/warren` + `cmd/marmot/warren.go`;
integration surfaces: engine wiring, vault registry, routes, API/UI, daemon interplay), on branch
`multiprocess-lock-fix`. All line references verified by the reviewers at commit `1f14f3e`.

## How warren state flows today (context for the proposals)

Warren state lives in three files that all share the name `_warren.md` (warren-repo manifest,
per-project identity, workspace mount state) and enters the engine exactly once, in `buildEngine`
(`cmd/marmot/pipeline.go:236-274`): mounts become in-memory routing-table entries, manifest bridges
become runtime cross-vault bridges, and a `VaultRegistry` is created **only if** bridges or routes
exist. Remote vault graphs and `embeddings.db` connections are lazily opened and **cached forever**
(`internal/namespace/registry.go:76-142,186-241`). API warren endpoints re-read disk per request,
so a long-lived process serves two different views of the same warren.

---

## Tier 1 — Correctness fixes (small, high value, do first)

### 1.1 Stop cross-vault *reads* from mutating remote vaults (worst finding)
`ResolveEmbeddingStore` opens remote DBs through `embedding.NewStore`, which now unconditionally
runs `PRAGMA journal_mode=WAL`, `CREATE TABLE IF NOT EXISTS`, and an `ALTER TABLE` migration
(`internal/embedding/store.go:47-89`). A "read-only" cross-vault search therefore flips the remote
DB's journal mode, creates `-wal`/`-shm` sidecars, and can migrate schema **inside the mounted
warren git checkout** — dirtying it with exactly the files import excludes.
**Fix:** add `embedding.NewStoreReadOnly(path)` (SQLite `file:...?mode=ro`, no pragmas beyond
busy_timeout, no schema init) and use it for all registry/remote opens. Registry writes don't exist
today, so nothing regresses.

### 1.2 Checkpoint before copying a live DB (import + burrow)
`ImportProject`/`Materialize` copy `embeddings.db` with plain `io.Copy` and no
`wal_checkpoint` (`internal/warren/warren.go:1317-1368`, `1289-1315`) — importing or burrowing a
vault that any process has open in WAL mode snapshots stale or torn data. This was a known
follow-up from the WAL plan (synthesis_plan.md 1.5).
**Fix:** before copying, open the source via `embedding.NewStore` and run
`PRAGMA wal_checkpoint(TRUNCATE)` (add `Store.Checkpoint()`); fall back to documented
point-in-time semantics if the open fails.

### 1.3 Harden `copyDir` (burrow) to match `copyMarmotVault` (import)
`copyDir` copies `.marmot-data/.env` (secrets), WAL sidecars, follows symlinks, and calls
`copyRegularFile` on any non-dir entry without an `IsRegular` check — a FIFO hangs the burrow
(`warren.go:1289-1315`, contrast `1352`). Re-burrowing onto an existing cache also never deletes
removed files, resurrecting deleted nodes.
**Fix:** share one filtered copier (skip `.env`, sidecars, irregular files, don't follow symlinks,
use `.Perm()`); clear the target before re-materializing.

### 1.4 Editable + materialized mounts silently lose edits
`findWarrenMountByVault` returns the burrow cache path when materialized; the node-update handler
writes there (`internal/api/handlers.go:412-457`), nothing syncs back, and `warren propose` advises
committing a checkout that never received the edits. Nothing forbids the combination
(`warren.go:847-872`).
**Fix (minimal):** refuse `warren edit` on a materialized project (and vice versa) with a clear
error; make `findWarrenMountByVault` prefer the checkout path for editable projects.

### 1.5 Lock the `_warren.md` read-modify-writes (and routes.yml)
Every mutation of all three `_warren.md` roles is an unlocked Load→mutate→Save
(`warren.go:1065-1077` and every manifest op); concurrent `mount`/`edit`/imports silently drop each
other's writes; `routes.Update` shares the hazard with a fixed `.tmp` name
(`internal/routes/routes.go:150-196`).
**Fix:** reuse the new `internal/daemon` flock utilities — take a short-lived flock on a sibling
`.lock` file around each read-modify-write. ~20 lines each, removes a whole class of lost updates.

### 1.6 Surface the swallowed errors
`ActiveMounts` silently drops a warren whose manifest fails to load (`warren.go:929-934`); project
metadata corruption degrades silently (`:895,945,986`); editable-write embedding upserts discard
errors (`handlers.go:449-454`); bridge-manifest parse errors silently *remove bridge policy
enforcement* (`pipeline.go:391-415`); remote-store open failures and post-`Refresh` search errors
vanish (`internal/mcp/handlers.go:81-88`).
**Fix:** stderr warnings at load points, error propagation on the write path, and a
`warren status`/doctor line for unreachable warrens.

### 1.7 Fix the frontmatter parser
`parseMarkdownYAML` finds the closing `---` anywhere in the file, not at line start
(`warren.go:1110-1125`) — any value or body containing `---` corrupts every subsequent save.
**Fix:** split on `\n---\n` / regex anchored to line start; add the regression test.

### 1.8 Test hermeticity bug (immediate)
`pipeline_warren_test.go` and `warren_test.go:329` run `buildEngine` without isolating
`MARMOT_ROUTES` — the tests read the developer's real `~/.marmot/routes.yml` and can scan real
user vaults. The `MARMOT_ROUTES` override added this week makes the fix one line per test
(`t.Setenv("MARMOT_ROUTES", "off")`).

---

## Tier 2 — Daemon-era freshness (the architectural gap)

The new single-owner daemon makes `buildEngine`'s load-once warren wiring a real problem: a
long-lived owner never sees `warren mount/edit/burrow`, a `git pull` in the warren checkout, or
routes changes. Today: the registry can even be **nil forever** (created only if mounts/routes
existed at startup, `pipeline.go:265-267`), the owner's fsnotify watcher deliberately skips
`_`-prefixed files (`internal/daemon/owner.go:297-303`), remote graphs are cached with a
never-checked `LoadedAt`, and `POST /api/warren/{id}/refresh` is a printf stub that doesn't call
`VaultRegistry.Refresh` (`handlers.go:912-931`). Result: the warren UI view (disk-fresh) and MCP
query results (startup-frozen) disagree within one process.

**Proposal (in order):**
1. Always create the `VaultRegistry` (drop the non-empty gate); teach it `Rebuild(mounts, routes)`.
2. Extract a `reloadWarrenState(engine)` helper that re-runs the `ActiveMounts` → rt/bridges →
   registry wiring from `buildEngine`; call it from (a) a real `POST /api/warren/{id}/refresh`,
   (b) a new `marmot warren refresh` implementation (optionally after `git -C <warren> pull`), and
   (c) the owner's watcher on workspace `_warren.md` changes (stop skipping that one file).
3. Bound remote-graph cache staleness: honor `LoadedAt` with a TTL or mtime check on the remote
   vault dir; `Refresh` must be safe under concurrent searches (swap-then-close, not close-in-place
   — today a concurrent `context_query` can search a just-closed store and silently return empty).
4. Cross-workspace guard: `marmot index --force` checks only the local vault's owner
   (`pipeline.go:63-73`); when the target vault is warren-mounted elsewhere the deletion happens
   under another workspace's open connection. Minimal fix: document; better: a shared advisory
   flock on the vault's `.marmot-data` taken by any force-deleter and any registry open.

## Tier 3 — UX repairs (mostly CLI verbs and defaults)

- **Missing inverse verbs:** no `unmount`, no `unregister`, no un-burrow — `Mount` only ever adds,
  `Materialized` is only ever set true; the sole escape is hand-editing `_warren.md`. Add
  `warren unmount/unregister` and `warren burrow --drop`.
- **`burrow` without `--materialize` is exactly `mount`** — make the verb imply materialization.
- **`warren mount --warren X` with no projects mounts everything** (`cmd/marmot/warren.go:722-727`),
  contradicting the "nothing becomes queryable by accident" promise — require `--all`.
- **`warren edit` silently activates the project in queries** (`SetEditable` auto-mounts,
  `warren.go:863`) — print what happened, or require the project to already be mounted.
- **Read-only commands mutate the workspace:** even `warren list` creates `.marmot/.marmot-data/`
  and writes a mock-provider `_config.md` (`cmd/marmot/warren.go:868-885`) that later governs real
  indexing — make `ensureWorkspace` lazy (only on actual mutation).
- **Moved/deleted warren checkouts vanish silently** — `warren status` and engine startup should
  say "warren X unreachable at <path>; re-run `marmot warren register`".
- **Stubs presented as features:** `refresh`/`propose` print advice (`warren.go:826-866`) but are
  listed as first-class commands in the README — implement (Tier 2 / Tier 4) or mark experimental.
- **Vault-ID collisions:** doctor checks within one warren only (`warren.go:563-575`); runtime
  last-mount-wins overwrites routes silently (`pipeline.go:245`) and `findWarrenMountByVault`
  first-match can consult the wrong project's editable flag. Detect and refuse at mount time.
- **MCP/API asymmetry:** MCP `context_write` rejects all `@vault/...` IDs even for editable mounts
  (`internal/mcp/handlers.go:288-290`) while the HTTP API allows them — align (accept editable
  writes over MCP, or document why not).

## Tier 4 — Capability roadmap (bigger, sequenced)

1. **Real `warren refresh`** = `git -C <warren> pull` (with dirty-check) + `reloadWarrenState` +
   re-materialize stale burrows. This makes warrens actually track their upstream.
2. **Burrow provenance & staleness:** record source commit/mtime at materialize time in the cache;
   `warren status` shows "cache 12 commits behind"; re-materialize invalidates cleanly.
3. **Real `warren propose`:** for editable mounts, a `git checkout -b`, commit of the touched
   project files, and printed push/PR instructions — turning today's aspirational stub into the
   write-back loop the edit feature implies.
4. **Manifest policy:** per-project `readonly: true` / protected flag in the warren manifest so the
   warren author (not each consumer) controls writability; enforce in `SetEditable` and the API
   write path.
5. **Embedding-model compatibility in doctor:** remote searches silently return nothing when a
   mounted project was indexed with a different embedding model (`WHERE model = ?`) — doctor and
   mount should compare models and warn.
6. **Version discipline:** manifest `Version` is backfilled 0→1 and unbounded — refuse to mutate a
   manifest with `Version > supported`, preserving unknown fields is otherwise impossible with the
   current struct round-trip.

## Test additions (accompany whichever tiers land)

- e2e: **zero warren coverage today** (`grep -ri warren e2e/` = 0 hits). Add: register→mount→query
  through a real serve; mount-while-owner-live (pins whatever Tier 2 freshness semantics you pick);
  editable write-back path.
- Unit: concurrent `_warren.md` RMW (flock test mirrors `internal/daemon`'s); real-SQLite import
  checkpoint test (current tests use fake byte files); `copyDir` symlink/FIFO/redelete cases;
  frontmatter `---`-in-body round-trip; cross-warren vault-ID collision; `Refresh`-under-search.
- Hermeticity: `MARMOT_ROUTES=off` in every warren/pipeline test (Tier 1.8).

## Suggested sequencing

1. Tier 1 (1.1–1.8) as one PR — pure correctness, each fix is small and independently testable.
2. Tier 2 as a second PR — it builds on the daemon and decides the freshness model everything else
   (refresh, UI) hangs off.
3. Tier 3 verbs/defaults as a third PR — mechanical once Tier 2 exists.
4. Tier 4 items individually, `refresh` first (it unlocks staleness UX), `propose` + manifest
   policy together (they define the collaboration story).
