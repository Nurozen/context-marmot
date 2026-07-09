# ContextMarmot Multi-Process Fix — Synthesis Plan

Sources: `artifacts/findings.md` (per-package audit), code verification against the repo and the Go
module cache (`$(go env GOMODCACHE)/github.com/ncruces/`), and an empirical compile+run check of the
v0.33.2 driver (see 1.4). All paths are repo-relative; line numbers are as of commit `6ec0f61`.

Document map:
[Context](#context--problem-summary) ·
[Workstream 1: WAL + driver upgrade](#workstream-1-wal--busy_timeout--driver-upgrade-to-v033x) ·
[Workstream 2: single-owner daemon](#workstream-2-single-owner-daemon-lock-file-election--stdiounix-socket-proxy) ·
[Testing & Rollout](#testing--rollout) ·
[Risks & Mitigations](#risks--mitigations) ·
[Sequencing](#sequencing)

## Context / Problem Summary

Multiple `marmot serve` processes (one per MCP client, stdio transport) share one vault. Every process
opens `.marmot/.marmot-data/embeddings.db` through `internal/embedding/store.go:42` with a bare
`sqlite3.Open(dbPath)` on `github.com/ncruces/go-sqlite3 v0.17.1` — no WAL, no `busy_timeout`.
Reproduced failure modes:

1. Concurrent write from a second process → instant `database is locked` (SQLITE_BUSY), no retry.
2. A COMMIT that hits another process's SHARED (reader) lock parks that connection on the PENDING
   lock; from then on **all reads in all processes** fail until the parked process dies. Driver
   v0.17.1 additionally has an indefinite `F_OFD_SETLKW` wait in its commit path (per the original
   diagnosis; note 1.3 — that wait persists in v0.33.2 and is eliminated by the WAL pragma, not by
   the driver upgrade).

Secondary multi-process bugs (out of scope for Workstream 1 — "WS1" below — and addressed by
Workstream 2 — "WS2"): stale
in-memory graph per process (loaded once in `mcp.NewEngine`, `internal/mcp/engine.go:149`), one
`summary.Scheduler` per process (duplicate LLM calls, racing `_summary.md` writes), and
last-writer-wins heatmap saves (`cmd/marmot/pipeline.go:343-353`, and on every query at
`internal/mcp/handlers.go:142-146`).

---

## Workstream 1: WAL + busy_timeout + driver upgrade to v0.33.x

Goal: make concurrent multi-process access to `embeddings.db` safe (readers never block writers,
writers retry for 5s instead of failing instantly) and eliminate the indefinite-commit-wait wedge —
which lives in the rollback-journal PENDING/EXCLUSIVE lock-upgrade path that WAL mode stops using
(see the corrected analysis in 1.3: the WAL pragma, not the driver bump, is the load-bearing fix). This is a two-commit change confined to `go.mod`/`go.sum`, `internal/embedding/store.go`, one
`--force` cleanup fix in `cmd/marmot/pipeline.go`, and new tests. **No other production code changes**:
every SQLite open in the codebase funnels through `embedding.NewStore` (verified: the only
`sqlite3.Open` call and the only `ncruces/go-sqlite3` imports in the repo are in
`internal/embedding/store.go:12-13,42`).

### 1.1 Ordered steps

1. **Upgrade the driver** (`go.mod` line 9: `github.com/ncruces/go-sqlite3 v0.17.1`):

   ```sh
   go get github.com/ncruces/go-sqlite3@v0.33.2
   go mod tidy
   ```

2. **Remove the dead `embed` import** in `internal/embedding/store.go:13` — this is the one
   compile-breaking API change (see 1.3):

   ```go
   import (
       ...
       "github.com/ncruces/go-sqlite3"
       // DELETE: _ "github.com/ncruces/go-sqlite3/embed"  — package removed in v0.33.x
   )
   ```

3. **Enable busy_timeout + WAL in `NewStore`** (`internal/embedding/store.go:41-54`). Exact change:

   ```go
   // NewStore opens (or creates) an embedding store at the given path.
   // Use ":memory:" for an in-memory database.
   //
   // File-backed stores are opened in WAL mode with a 5s busy timeout so that
   // multiple marmot processes can share one embeddings.db: readers never block
   // the writer, and a busy writer retries instead of failing with
   // "database is locked". For ":memory:" the WAL pragma is a harmless no-op
   // (journal_mode stays "memory").
   func NewStore(dbPath string) (*Store, error) {
       db, err := sqlite3.Open(dbPath)
       if err != nil {
           return nil, fmt.Errorf("open sqlite: %w", err)
       }

       // Order matters: set the busy timeout first so the journal-mode switch
       // itself retries if another process holds the lock.
       if err := db.BusyTimeout(5 * time.Second); err != nil {
           _ = db.Close()
           return nil, fmt.Errorf("set busy_timeout: %w", err)
       }
       if err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
           _ = db.Close()
           return nil, fmt.Errorf("enable WAL: %w", err)
       }

       s := &Store{db: db}
       if err := s.initSchema(); err != nil {
           _ = db.Close()
           return nil, fmt.Errorf("init schema: %w", err)
       }
       return s, nil
   }
   ```

   Add `"time"` to the import block. Design notes:
   - `Conn.BusyTimeout(time.Duration)` exists identically in both versions (v0.33.2 `conn.go:365`).
     It is equivalent to `PRAGMA busy_timeout = 5000` — the workstream's "busy_timeout(5000)".
   - Pragmas are executed after `Open` rather than via a `file:...?_pragma=journal_mode(WAL)` URI.
     Both versions support `_pragma` URI params (v0.33.2 `conn.go:59-134`), but the URI form would
     require escaping arbitrary `dbPath` values and special-casing `":memory:"`; Exec-after-open
     avoids both and keeps the `":memory:"` test path (`store_test.go:10`,
     `internal/mcp/classify_test.go:23`) working unchanged.
   - `PRAGMA journal_mode = WAL` on `":memory:"` returns "memory" without error (verified
     empirically, see 1.4) — do **not** parse/validate the returned mode as a hard error.
   - `initSchema()` (`store.go:56-74`) needs no change; its first `Exec` now benefits from the busy
     timeout during multi-process startup races. The deliberately-ignored `ALTER TABLE` error at
     `store.go:71` behaves the same on v0.33.2 (verified in 1.4).

4. **Fix `--force` DB removal for WAL sidecars** (`cmd/marmot/pipeline.go:58-62`). After WAL, a bare
   `os.Remove(dbPath)` leaves stale `-wal`/`-shm` files that would be replayed into the fresh DB:

   ```go
   if force {
       // Remove existing embeddings DB (and WAL sidecars) to start fresh.
       _ = os.Remove(dbPath)
       _ = os.Remove(dbPath + "-wal")
       _ = os.Remove(dbPath + "-shm")
   }
   ```

5. Run `go build ./... && go test ./... && make build` (the `web/dist` `go:embed` requirement in
   `web/embed.go` is unrelated to the driver's removed `embed` package but both must keep building),
   then the new tests in 1.6.

### 1.2 Call-site inventory (verified — all covered by the one `NewStore` change)

`sqlite3.Open` call sites: exactly one — `internal/embedding/store.go:42`. All other DB access goes
through `embedding.NewStore`:

| Caller | Path | Role |
|---|---|---|
| `internal/mcp/engine.go:160` | via `NewEngine` | every serve/query/ui/eval engine |
| `internal/namespace/registry.go:223` | `ResolveEmbeddingStore` | remote-vault DBs (cross-vault query/search) |
| `internal/api/handlers.go:450` | warren node update | short-lived open on a mounted vault's DB |
| `cmd/marmot/pipeline.go:64` | `runIndexPipeline` | writer (`marmot index`) |
| `cmd/marmot/pipeline.go:768` | `runStatusPipeline` | read-only `Count` |
| `cmd/marmot/pipeline.go:859` | `watchLoop` | writer (`marmot watch`) |
| `cmd/marmot/pipeline.go:1063` | `runStaticIndexPipeline` | writer |
| Tests | `internal/api/api_test.go:180`, `cmd/marmot/warren_test.go:363`, `internal/embedding/*_test.go`, `internal/mcp/classify_test.go:23` | mixed file/`:memory:` |

All of these get WAL + busy_timeout with no code change. This includes the remote-vault stores that
the findings flag as extra contention multipliers (`registry.go:221-241`, `handlers.go:560-580`).

### 1.3 Driver API migration notes: v0.17.1 → v0.33.2 (verified against module cache)

Verified by diffing `$(go env GOMODCACHE)/github.com/ncruces/go-sqlite3@v0.17.1` vs `...@v0.33.2`
(v0.33.2 is already in the local module cache) and by compiling/running a store.go-equivalent
program against v0.33.2 (1.4).

**Breaking for this repo (one item):**

- `github.com/ncruces/go-sqlite3/embed` **no longer exists**. In v0.17.1 it `//go:embed`-ed
  `sqlite3.wasm` and set `sqlite3.Binary` in `init()` (v0.17.1 `embed/init.go`). In v0.33.x the
  SQLite build ships as **WASM pre-compiled to Go** via the new module dependency
  `github.com/ncruces/go-sqlite3-wasm` (wasm2go; see v0.33.2 `wrap.go:13`), linked automatically —
  no blank import needed. Delete `internal/embedding/store.go:13`. **wazero is gone entirely**
  (no runtime WASM interpretation/JIT; `github.com/tetratelabs/wazero v1.7.3` drops out of `go.mod`
  after `go mod tidy`).

**Non-breaking — every driver API used by `store.go` is signature-identical in v0.33.2** (full usage
inventory from findings, checked against v0.33.2 sources):

| API (store.go usage lines) | v0.17.1 | v0.33.2 | Status |
|---|---|---|---|
| `sqlite3.Open(string) (*Conn, error)` (42) | conn.go:39 | conn.go:46 | unchanged |
| `Conn.Prepare(sql) (stmt, tail, err)` 3-return (110, 149, 208, 253, 267, 290, 333, 414, 474, 496, 518) | conn.go:169 | conn.go:192 | unchanged |
| `Conn.Exec` (57, 71) / `Conn.Close` (534) | ✓ | conn.go:177 / conn.go:159 | unchanged |
| `Stmt.Step` / `Stmt.Err` / `Stmt.Exec` / `Stmt.Close` | ✓ | stmt.go:105/128/134/24 | unchanged |
| `Stmt.BindText` / `Stmt.BindBlob` | ✓ | stmt.go:247/270 | unchanged |
| `Stmt.ColumnText` / `Stmt.ColumnInt` / `Stmt.ColumnRawBlob` | ✓ | stmt.go:525/474/556 | unchanged |
| `Conn.BusyTimeout(time.Duration)` (new use) | conn.go:362 | conn.go:365 | unchanged |

- `ColumnRawBlob` (the riskiest per findings — memory valid only until next `Step`/`Close`) still
  exists with the same contract; `store.go` fully consumes the blob via `deserializeFloat32` before
  stepping, so all `Search`/`SearchActive`/`FindSimilar`/`storedDimensionLocked` uses stay safe.
- Signature changes that do **not** affect this repo (for awareness): `Conn.BusyHandler` gained a
  `context.Context` parameter (v0.33.2 `conn.go:393`) — unused here; `OpenContext` was added.
- Toolchain: v0.33.2 requires `go 1.26.0`; the repo is already on `go 1.26.0` (`go.mod:3`). ✓

**go.mod / go.sum outcome** (what to expect after step 1):

- `require github.com/ncruces/go-sqlite3 v0.33.2` (was v0.17.1).
- New indirect dep: `github.com/ncruces/go-sqlite3-wasm v1.0.4-0.20260329114232-2491c387476c`
  (pseudo-version pinned by the driver's own go.mod — expected, not a mistake).
  `github.com/ncruces/sort`/`github.com/ncruces/wbt` are required by driver ext packages this repo
  does not import, so module pruning keeps them out of go.mod and go.sum (verified with a scratch
  module: after `go mod tidy` the only ncruces entries are go-sqlite3, go-sqlite3-wasm, julianday).
  `golang.org/x/sys` stays at v0.42.0 — already newer than the driver's v0.41.0 requirement, no bump.
- Removed indirect dep: `github.com/tetratelabs/wazero`.
- Commit both `go.mod` and `go.sum`.

**Behavioral notes on the new runtime:** wasm2go means SQLite is now compiled Go code — faster
process startup (no wazero compile step), different (generally lower) memory profile; the wrapper
caps memory at 256MB by default (v0.33.2 `wrap.go:36`, ample for this workload). **Correction to
the original diagnosis:** the indefinite `F_OFD_SETLKW` wait is the PENDING-byte acquisition in the
rollback-journal commit path, and it exists **in both driver versions** — v0.17.1
`vfs/os_unix_lock.go:26-33` passes `timeout=-1` when already RESERVED, and v0.33.2
`vfs/os_ofd.go:29` does exactly the same (both then dispatch to `F_OFD_SETLKW` in
`os_darwin.go`/`os_linux.go` for `timeout < 0`). The upgrade alone does **not** remove that wait;
the WAL pragma does, by taking commits off the PENDING/EXCLUSIVE lock-upgrade path entirely. What
the upgrade adds on Darwin is hardening around the *timed* lock path (`runtime.KeepAlive(lock)` at
v0.33.2 `os_darwin.go:101` — both versions already used `F_OFD_SETLKWTIMEOUT` for the 1ms EXCLUSIVE
wait) plus `F_BARRIERFSYNC`-based sync and EINTR-retry loops. Consequence: do not ship the driver
bump without the pragmas (step 3) — the pragmas carry the fix. WAL requires shared-memory support:
`vfs.SupportsSharedMemory = true` on the platforms we ship (darwin/linux; v0.33.2 `vfs/shm.go:10`).
Do not assume driver error **strings** are stable across 16 minor versions — no repo code matches on
them (verified: no `database is locked` string matching outside test expectations), but re-check
`TestNewStore_OpenError` (see 1.6).

### 1.4 Empirical verification already performed

A scratch module pinned to `github.com/ncruces/go-sqlite3@v0.33.2` reproducing `store.go`'s exact
API usage (Open → BusyTimeout → `PRAGMA journal_mode=WAL` → CREATE TABLE → ignored ALTER →
prepared upsert with BindText/BindBlob/Exec → scan with Step/ColumnText/ColumnRawBlob/ColumnInt/Err)
compiled **without any embed import** and ran successfully:

- `PRAGMA journal_mode` reports `wal`; `embeddings.db-wal` sidecar appears while open.
- On clean `Close()` of the last connection, SQLite auto-checkpoints and **removes** `-wal`/`-shm`.
- `":memory:"` accepts both pragmas without error (journal mode stays `memory`).

### 1.5 WAL side effects and operational implications

- **Sidecar files**: `embeddings.db-wal` and `embeddings.db-shm` appear next to
  `.marmot/.marmot-data/embeddings.db` whenever a connection is open (and persist if a process is
  killed — harmless; SQLite recovers/replays on next open). They never pollute graph/node loading:
  both walkers skip `.marmot-data` (`internal/node/store.go:237-239`,
  `internal/graph/loader.go:26-28`).
- **Checkpointing**: default auto-checkpoint at 1000 WAL pages plus checkpoint-on-last-close
  (verified above) is sufficient; the workload is small (hundreds–low-thousands of rows). No manual
  checkpoint loop needed for WS1. Optionally expose `func (s *Store) Checkpoint() error`
  (`PRAGMA wal_checkpoint(TRUNCATE)`) for the warren-import case below and for WS2's owner handoff.
- **Backups / copies of a live DB**: copying `embeddings.db` alone can miss uncheckpointed pages
  living in `-wal`. Affected today: `internal/warren/warren.go:1317-1323` — `ImportProject` already
  **excludes** `-wal`/`-shm` from copies (good: never copy `-shm`), but importing an actively-written
  vault can snapshot stale data. Mitigation (small follow-up, not a WS1 blocker): before copying,
  open the source DB via `embedding.NewStore` and run `Checkpoint()`, or document that import of a
  live vault is point-in-time. Existing tests `internal/warren/warren_test.go:219-220,748-749`
  (sidecars excluded from import) still pass unchanged.
- **Deletion**: any code deleting the DB must delete all three files — fixed for
  `--force` in step 4; audit for future call sites.
- **Persistence of the mode**: `journal_mode=WAL` is written into the DB header — the flip is
  one-time per file and survives across processes. Old binaries (v0.17.1 driver) can still open a
  WAL-mode DB (v0.17.1's VFS supports shared memory), but they lack the busy timeout, so mixed
  old/new binaries on one vault still risk lock errors — upgrade all marmot binaries on a machine
  together. Rollback: `PRAGMA journal_mode=DELETE` on the file (one-liner), plus `git revert`.
- **Locking semantics after the change**: readers (the long
  `Search`/`SearchActive`/`FindSimilar` scans, `store.go:216-233,344-367,425-450`) no longer block a
  committing writer and vice versa; concurrent writers serialize with up to 5s of retry instead of
  instant SQLITE_BUSY. Note `busy_timeout` does **not** cover the `SQLITE_BUSY_SNAPSHOT`/upgrade
  deadlock case of write transactions started as readers — not applicable here because every write
  in `store.go` is a single autocommit statement (no explicit BEGIN).
- **What WS1 does NOT fix** (retained by design for WS2): stale per-process in-memory graphs,
  duplicate schedulers/LLM calls, heatmap and `_summary.md` last-writer-wins, `routes.yml` /
  `_warren.md` / `.env` read-modify-write races. WS1 turns "vault wedges/fails" into "vault stays
  responsive but processes can still disagree about state".

### 1.6 Workstream 1 test plan

Unit (in `internal/embedding`):
1. `TestNewStore_WALEnabled`: file-backed store → `PRAGMA journal_mode` returns `wal`;
   `-wal` sidecar exists after one Upsert; store still works after Close/reopen.
2. `TestNewStore_MemoryStillWorks`: `NewStore(":memory:")` succeeds and Upsert/Search round-trips
   (guards the pragma-on-memory no-op; existing `store_test.go:10` also covers this implicitly).
3. `TestNewStore_ConcurrentConns`: open **two `Store`s on the same file** (two OS-level file
   handles → exercises the real OFD/shm locking path in-process), run a writer goroutine
   (Upsert loop) against a reader goroutine (`SearchActive` loop) for ~2s; assert zero
   `database is locked` errors. This is the regression test for both reproduced failure modes.
4. Re-verify `TestNewStore_OpenError` (`coverage_test.go:282-289`, path in missing directory) still
   fails at `Open` on v0.33.2; if the error text changed, loosen the assertion (it should only check
   `err != nil`).

Existing suites (no changes expected, but they are the upgrade's regression net):
- `internal/mcp` (`server_test.go:19` real on-disk DB per test; `classify_test.go:23` `:memory:`),
  `internal/api/api_test.go:94,180` (engine + direct store on same file — now WAL),
  `internal/codemode/writes_test.go:415-468`, `cmd/marmot` warren/pipeline tests,
  `internal/namespace/registry_embstore_test.go`. WAL sidecars appearing in `t.TempDir()` are
  harmless.
- `cmd/marmot` `--force` path: add/extend a test that pre-creates `embeddings.db-wal` and asserts
  `index --force` removes it (step 4).

E2E (`e2e/`):
- Extend `TestMCPServer` environments implicitly (all writes now WAL) — no edits needed.
- **New** `TestConcurrentServes` (fills the gap flagged in findings `e2e` section): spawn two
  `marmot serve --dir .marmot` processes on one seeded vault, issue interleaved `context_write` from
  both and `context_query` from each; assert all calls succeed and neither process wedges. Under
  WS1-only this passes at the SQLite layer (graphs may be stale — assert only on tool-call success,
  not cross-process read-your-writes; WS2 tightens it).
- **New** `TestIndexDuringServe` (writer-vs-server): run `marmot index` while a serve process is
  answering `context_query` (the reproduced instant-lock case; today `RunResult.Errors` silently
  absorbs the failures, `internal/indexer/runner.go:358-372` — assert `Errors == 0`).
- Full assertion specs for both new tests, including the red-first baseline verification, are in
  [Testing & Rollout](#the-multi-process-contention-test-canonical-freeze-regression).

Build/tooling: `go build ./...`, `go vet ./...`, `golangci-lint` (CI parity — the lint job runs
golangci-lint v2.1.6, which includes staticcheck), `make build` then
`cmd/marmot-eval`'s `bin/marmot` smoke path (findings `cmd-marmot-eval` `main.go:36`).

### 1.7 Workstream 1 risks & mitigations

| Risk | Mitigation |
|---|---|
| 16-minor-version driver jump changes untested behavior (error codes/strings, txn edge cases) | Full test suite + new concurrency tests; empirical API check already done (1.4); pin exactly v0.33.2 |
| wasm2go runtime differs from wazero (perf/memory) | Startup gets faster (no JIT); 256MB default cap far exceeds workload; e2e suite exercises real binary |
| Stale `-wal` replay after `--force` | Step 4 removes sidecars |
| Live-DB copy in warren import misses WAL pages | Checkpoint-before-copy follow-up (1.5); sidecar exclusion already in place |
| Mixed old/new binaries on one vault | Release note: upgrade binary everywhere; WAL flip is compatible, only retry behavior differs |
| `:memory:` pragma regression | Dedicated unit test (1.6 #2); verified empirically already |
| busy_timeout masks (rather than fixes) architectural multi-writer races | Explicitly scoped: WS1 = availability fix; correctness of shared state = WS2 |

---

## Workstream 2: Single-owner daemon (lock-file election + stdio↔unix-socket proxy)

Goal: exactly one process per vault owns the engine, summary scheduler, watcher, and heatmap; every
other `marmot serve` becomes a thin stdio↔unix-socket relay. This eliminates the bugs WS1 leaves by
design (stale per-process graphs, duplicate schedulers/LLM calls, heatmap/`_summary.md`
last-writer-wins — see 1.5 "What WS1 does NOT fix"). WS1 stays in place underneath: WAL +
busy_timeout is the safety net for the non-serve CLI writers (`index`, `watch`, `ui`) that continue
to open the DB directly (2.7), and for the brief window during owner re-election.

### 2.0 Architecture at a glance

```
MCP client A ──stdio──► marmot serve #1 (wins flock)  = OWNER
                          ├─ buildEngine(dir)            (single engine, graph, heatmap)
                          ├─ summary.Scheduler            (single instance)
                          ├─ fsnotify graph-reload watcher
                          ├─ serves its own stdio session
                          └─ unix-socket accept loop ◄──┐
MCP client B ──stdio──► marmot serve #2 (flock busy) = PROXY ── relays lines over socket
MCP client C ──stdio──► marmot serve #3 (flock busy) = PROXY ──┘
```

New package: `internal/daemon` (files: `lock.go`, `lock_unix.go`, `lock_windows.go`, `socket.go`,
`owner.go`, `proxy.go`, plus `*_test.go`). Entry-point change: `cmd/marmot/pipeline.go`
`runServePipeline` (lines 450–466). No changes to `internal/mcp` transport code are required —
`Server.ListenStdio(ctx, stdin, stdout)` (`internal/mcp/server.go:248-252`) already accepts an
arbitrary reader/writer pair, which is exactly the hook both the owner's per-connection serving and
the relay need (findings `internal-mcp` server.go:246-259).

### 2.1 Lock-file election: `flock` on `.marmot/.marmot-data/daemon.lock`

**Choice: BSD `flock(2)` (LOCK_EX|LOCK_NB), not O_EXCL+PID.** Justification from the findings:

- The e2e harness force-kills serve after 5s (`e2e/e2e_test.go:245-249`) and the Playwright harness
  `kill`s the ui server (`web/e2e/serve.sh:35`). With O_EXCL+PID, every kill leaves a stale lock
  file requiring a `kill(pid, 0)` liveness probe — which is racy under PID reuse and needs
  tie-breaking when two processes both decide the owner is dead. `flock` is released by the kernel
  the instant the holder dies (including SIGKILL), so "lock held" ≡ "owner alive". No liveness
  code, no stale-lock GC, no PID-reuse race.
- The upgraded driver already uses OFD/POSIX locks on `embeddings.db` itself (1.3); using a
  **separate** lock file with `flock` keeps daemon election fully independent of SQLite's locking.
- Vaults live on local filesystems (project checkouts); the classic `flock`-over-NFS caveat is
  noted as a risk (2.10), not a design driver.

Layout (all under `<vault>/.marmot-data/`, which every walker skips — `internal/graph/loader.go:26-28`,
`internal/node/store.go:237-239` — and which is vault-local, keeping the e2e `HOME` override
hermetic per findings `e2e_test.go:101-106`):

- `daemon.lock` — 0600, empty-or-PID content (PID is diagnostic only, never trusted). The fd stays
  open for the owner's lifetime; `flock` travels with the fd.
- `daemon.info.json` — written via tmp+rename **after** the socket is listening:
  `{"pid":1234,"socket":"/abs/path.sock","version":"<binary version>","started_at":"..."}`.
  Proxies and CLI commands read this to find the socket and to answer "is an owner running?".
  Removed on graceful shutdown; if stale (owner SIGKILLed), the connect attempt fails and the
  reader falls back to election (2.4).

API sketch (`internal/daemon/lock.go`):

```go
// TryAcquire returns (lock, nil) if this process is now the owner,
// (nil, ErrHeld) if another live process holds the vault, or an error.
func TryAcquire(dataDir string) (*Lock, error)   // open+flock(LOCK_EX|LOCK_NB) daemon.lock
func (l *Lock) WriteInfo(info Info) error        // tmp+rename daemon.info.json
func (l *Lock) Release() error                   // remove daemon.info.json, close fd (drops flock)
                                                 // (socket file is removed by Owner.Close, 2.3)
func ReadInfo(dataDir string) (Info, error)      // for proxies and other CLI commands
```

`lock_unix.go` uses `golang.org/x/sys/unix.Flock` (x/sys already in go.mod, currently indirect —
it becomes direct). `lock_windows.go`: see 2.10.

### 2.2 Unix socket: location, length limit, permissions

- **Primary path**: `<vault>/.marmot-data/daemon.sock` — hermetic for e2e (vault-derived), skipped
  by all walkers, colocated with the lock.
- **Length fallback**: macOS caps `sun_path` at 104 bytes (findings `e2e_test.go:101-106` flags
  deep temp dirs). If `len(abs socket path) > 96`, fall back to
  `filepath.Join(os.TempDir(), "marmot-"+hex(sha256(absVaultPath))[:16]+".sock")`. Because the
  **actual** path is always published in `daemon.info.json`, consumers never re-derive it — the
  fallback is invisible to them. Hash-keying by vault path keeps hermetic tests from ever
  colliding with a developer's real vaults.
- **Permissions**: `os.Chmod(path, 0600)` immediately after `net.Listen("unix", path)` (listener
  created with default umask, then tightened; the vault is user-private anyway, this is
  defense-in-depth). No abstract-namespace sockets (Linux-only, no permission model).
- **Stale socket file**: before `Listen`, if the file exists, a fresh owner (it holds the flock, so
  no live owner exists) simply `os.Remove`s it. Proxies never remove it.

### 2.3 Owner path: what runs where

`runServePipeline` (`cmd/marmot/pipeline.go:450-466`) is replaced by an election loop:

```go
// cmd/marmot/pipeline.go
func runServePipeline(dir string) error {
    if os.Getenv("MARMOT_NO_DAEMON") == "1" || runtime.GOOS == "windows" {
        return runServeStandalone(dir) // exactly today's body; WS1 WAL keeps it safe-ish
    }
    dataDir := filepath.Join(dir, ".marmot-data")
    for {
        lock, err := daemon.TryAcquire(dataDir)
        switch {
        case err == nil:
            return runServeOwner(dir, lock)
        case errors.Is(err, daemon.ErrHeld):
            info, ierr := daemon.ReadInfo(dataDir)
            if ierr != nil { // owner mid-startup: info not written yet
                time.Sleep(50 * time.Millisecond)
                continue
            }
            err := daemon.RunProxy(os.Stdin, os.Stdout, info.Socket)
            if errors.Is(err, daemon.ErrOwnerGone) {
                continue // owner died with our client still attached: re-elect (2.4)
            }
            return err // client EOF (nil) or fatal relay error
        default:
            return err
        }
    }
}

func runServeOwner(dir string, lock *daemon.Lock) error {
    result, err := buildEngine(dir)          // today's pipeline.go:452, owner-only now
    if err != nil { lock.Release(); return err }

    ctx, cancel := context.WithCancel(context.Background())
    sigCtx, sigStop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
    defer sigStop()

    if result.Scheduler != nil { result.Scheduler.Start(ctx) } // single scheduler
    stopWatch := daemon.StartGraphWatcher(dir, result.Engine)  // graph freshness, 2.5

    own := daemon.NewOwner(dir, lock, func(c net.Conn) error { // accept loop
        srv := mcpserver.NewServer(result.Engine)              // fresh Server per conn,
        return srv.ListenStdio(sigCtx, c, c)                   // shared Engine (see note)
    })
    if err := own.Listen(); err != nil { /* release+return */ }

    // Serve our own MCP client on real stdio.
    ownSrv := mcpserver.NewServer(result.Engine)
    stdioErr := ownSrv.ListenStdio(sigCtx, os.Stdin, os.Stdout) // returns on client EOF

    // Linger: our client is gone, but proxies may still be attached.
    own.WaitIdle(sigCtx)   // returns when active socket sessions == 0 (or signal)

    cancel()
    stopWatch()
    daemon.BoundedStop(result.Scheduler, 3*time.Second) // see shutdown note below
    saveHeatmapAndClose(result)                         // today's Cleanup body, pipeline.go:343-353
    own.Close()                                          // close listener, remove socket
    lock.Release()                                       // remove info.json, drop flock
    return stdioErr
}
```

Notes anchored in the findings:

- **Fresh `mcpserver.NewServer(engine)` per connection, one shared Engine.** `NewServer` only
  builds the mcp-go `MCPServer` and registers tool handlers bound to the Engine
  (`internal/mcp/server.go`) — cheap and stateless. This sidesteps any question of multiplexing
  several `StdioServer` sessions over one `MCPServer`; the Engine itself is the concurrency-safe
  shared object (per-namespace `nsMu` locks, `atomic.Pointer[graph.Graph]`,
  `internal/mcp/engine.go:30-52`), and it finally provides what those in-process locks were always
  assuming: a single process (findings `internal-embedding` store.go:34-37, `internal-api`
  chat_handlers.go:163-188, `internal-codemode` client_writes.go:281-309).
- **Bounded scheduler stop.** `summary.Scheduler.Stop()` can block up to the 2-minute LLM
  regeneration timeout (findings `internal-summary` scheduler.go:62-93,178-205), but the e2e
  harness kills serve 5s after stdin close (`e2e/e2e_test.go:245-249`). `BoundedStop` runs
  `Stop()` in a goroutine and proceeds after 3s — an abandoned regeneration writes `_summary.md`
  atomically (tmp+rename, summary.go:104-152) so the worst case is a wasted LLM call, never a torn
  file. Heatmap save + engine close + lock release must NOT wait on it.
- **Signal handling**: `signal.NotifyContext` (scoped, unregistered on return) so SIGTERM/SIGINT
  produce the same graceful path. Constraint: no test in `cmd/marmot` may send SIGINT/SIGTERM
  (findings `surface_coverage_test.go:244-255`) — tests exercise shutdown via stdin EOF only, and
  the signal registration must be cleaned up (`sigStop`) so it doesn't leak beyond the call.
- **Gating**: the sketch shows the end state (daemon default-on, `MARMOT_NO_DAEMON=1` /
  `--no-daemon` opt-out). In the first WS2 release the gate is inverted — election runs only when
  `MARMOT_DAEMON=1` — over the identical code path; see [Sequencing](#sequencing).
- **`TestServeCommandEOF`** (`cmd/marmot/surface_coverage_test.go:138-152`) stays green by
  construction: empty stdin → owner elected → `ListenStdio` returns immediately on EOF →
  `WaitIdle` returns immediately (zero proxy sessions) → cleanup → exit 0. Add assertions that
  `daemon.lock` is unlocked and `daemon.sock`/`daemon.info.json` are gone afterward.
  `TestServeCommandNoVault` is untouched (the vault-existence check in `runServe`,
  `cmd/marmot/main.go:331-334`, still runs before any election).

### 2.4 Owner lifecycle: client disconnect, linger, re-election (no live handoff)

Three cases:

1. **Owner's client disconnects, no proxies attached** → `WaitIdle` returns at once; full
   shutdown. This is today's behavior, preserved for the e2e 5s-kill budget.
2. **Owner's client disconnects, proxies still attached** → the owner *lingers headless*: it keeps
   the engine, scheduler, watcher, and socket alive until the last proxy session ends, then shuts
   down. Rationale: a live *handoff* (serializing engine state to a successor) buys nothing — the
   engine state is all derivable from disk — and costs a complex protocol. Linger-until-idle is a
   session refcount. Risk: some MCP clients kill the whole child process group on exit; if the
   owner is SIGKILLed despite lingering, case 3 covers it.
3. **Owner dies while proxies are attached** (SIGKILL, crash, case-2 group-kill) → every proxy's
   socket read hits EOF/ECONNRESET → each proxy returns `ErrOwnerGone` → the election loop in
   `runServePipeline` re-runs: one proxy wins the now-free flock and becomes the new owner
   (rebuilding the engine from disk — cheap, and *correct* because all durable state is on disk:
   node files, embeddings.db under WAL, heatmap, `_heat`/`_summary.md`); the rest reconnect to the
   new socket. Session continuity across the failover is the proxy's job (initialize replay, 2.6).

Shutdown/attach race (edge of cases 1–2): a new proxy can dial the socket in the window between
`WaitIdle` returning and `own.Close()`. Order the teardown so `Owner.Close` (stop accepting +
remove socket) runs **before** `lock.Release()` — then a late proxy either fails the dial or gets
an immediate conn drop, returns `ErrOwnerGone`, and re-enters the election against a now-free
flock. `WaitIdle` must also re-check the session count after the listener stops accepting, so a
connection accepted in that window is either served to completion or dropped deterministically —
never silently half-served.

There is deliberately **no ownership transfer protocol**: election is always "flock decides", and
the only state that crosses owners is what's on disk. The known in-memory-only losses on owner
death are bounded and pre-existing: per-session curator undo stacks (findings `internal-curator`
undo.go:32-40) and in-flight code-mode executions (findings `internal-codemode`
executor.go:166-173) — documented, not solved.

### 2.5 What the owner consolidates (the WS2 correctness wins)

| Shared state | Today (per findings) | Under single owner |
|---|---|---|
| Engine + embeddings.db conn | one per serve/query/ui process (`internal/mcp/engine.go:139-173`) | one; serve proxies never call `buildEngine` (they import only `internal/daemon`) |
| In-memory graph | loaded once per process, never refreshed (`engine.go:149`) | one copy + **owner-run fsnotify reload watcher** (see below) |
| summary.Scheduler | one per process → duplicate LLM calls, racing `_summary.md` (`internal-summary` scheduler.go:98-122,178-205) | exactly one, started at `pipeline.go:459-461` owner-only; `NotifyChange` fires in the owner because all writes execute there |
| Heatmap | loaded per process (`pipeline.go:267-271`), saved on every query (`internal/mcp/handlers.go:142-146`) and on every exit (`pipeline.go:343-353`) — continuous last-writer-wins | loaded and saved by the owner only for *serve*; standalone `query`/`ui` still execute queries with their own engines, so their heatmap must be **detached, not just save-suppressed** when an owner is alive (2.7) — the per-query save at `handlers.go:142-146` fires inside `HandleContextQuery` whenever `e.HeatMap != nil` |
| Per-namespace write locks / `NamespaceLock` | in-process only, zero cross-process protection (`engine.go:45`) | sufficient by construction: all mutations (MCP tools, code-mode, curator) funnel through the owner |
| update.Watcher / reindex goroutines | one per watching process (`internal-update` watcher.go:30-74) | owner-only (and `marmot watch` is refused when an owner exists, 2.7) |

**Graph freshness** (`daemon.StartGraphWatcher`): reuse the proven pattern from
`internal/api/watcher.go:19-109` — fsnotify on the vault dir + subdirs, 1s-debounced
(`watcher.go:46`) `graph.LoadGraph(node.NewStore(dir))` + `engine.SetGraph(newGraph)`. Extract that logic into a
shared helper (new `internal/mcp/graphwatch.go` or inside `internal/daemon`) rather than
duplicating it, and have `internal/api.Server.StartWatcher` delegate to it. This is what lets
`marmot index` keep running standalone (2.7): the owner notices the node-file writes and reloads;
embedding rows need no invalidation because every query reads SQLite fresh
(`internal/mcp/handlers.go:60-100`). Carry over the known limitation (subdirectories created after
start are not watched — findings `internal-api` watcher.go:19-95) and fix it while extracting:
re-add watch dirs on `fsnotify.Create` of a directory.

### 2.6 Proxy: stdio ↔ unix-socket JSON-RPC relay

Framing: the mcp-go `StdioServer` speaks **newline-delimited JSON-RPC** — one JSON object per
`\n`-terminated line (confirmed by the e2e harness, which writes `line+"\n"` and
`json.Unmarshal`s per line, `e2e/e2e_test.go:260-287`; a proxy must preserve this exactly per
findings `e2e` `TestMCPServer` note). The same framing flows over the socket unchanged — the owner
serves each conn with `ListenStdio(ctx, conn, conn)`, so **the proxy is a line relay, not a
protocol translator**.

`internal/daemon/proxy.go`:

```go
func RunProxy(stdin io.Reader, stdout io.Writer, socketPath string) error {
    conn, err := net.Dial("unix", socketPath)
    if err != nil { return ErrOwnerGone } // stale info.json / owner mid-death
    sess := &session{} // records the raw `initialize` request line + its id,
                       // and the `notifications/initialized` line, as they pass through
    for {
        err := relay(stdin, stdout, conn, sess)
        switch {
        case err == nil:            // stdin EOF: client is done
            conn.(*net.UnixConn).CloseWrite() // let owner finish in-flight responses
            drain(stdout, conn, 2*time.Second)
            return nil
        case isConnDrop(err):       // owner died mid-session
            conn, err = reconnectWithReelection(socketPath) // caller loops on ErrOwnerGone
            if err != nil { return ErrOwnerGone }
            sess.replayHandshake(conn)  // resend initialize + initialized;
                                        // suppress the duplicate initialize *response*
                                        // (matched by recorded id) from reaching stdout
        default:
            return err
        }
    }
}
```

- `relay` runs two goroutines: stdin→conn (a `bufio.Scanner` with a large buffer — default 64KB
  will truncate big `context_write` payloads; set ~10MB — so the handshake lines can be captured)
  and conn→stdout (line-scanned too, so the post-failover duplicate-initialize-response filter has
  somewhere to live; in steady state it is a pass-through).
- **Handshake replay** is what makes case-3 failover (2.4) invisible to the MCP client: the new
  owner needs an `initialize`/`initialized` exchange before it will serve tool calls, but the real
  client already did its handshake and won't repeat it. The proxy replays the recorded lines
  verbatim and drops the one duplicate response. Same-binary owners return identical capabilities,
  so the client's view stays consistent. In-flight requests at the moment of owner death get no
  response; clients (and the e2e harness, `recv` timeout at `e2e_test.go:269`) treat that as a
  normal request timeout. If replay proves fragile in practice, the degraded fallback is: proxy
  exits nonzero on owner death and the MCP client restarts `marmot serve` — cheap to keep as a
  flag (`MARMOT_PROXY_NO_RESUME=1`).
- Stderr: the proxy prints its own single startup line (`ContextMarmot MCP proxy → <socket>`) to
  stderr; owner-side stderr is not relayed (MCP clients treat stderr as logs; nothing in the
  protocol needs it).
- Testing hook: `RunProxy` takes `io.Reader/io.Writer` (not `os.Stdin/os.Stdout`) precisely so the
  `io.Pipe` transport-test pattern from `internal/mcp/transport_test.go:29-45` (flagged by
  findings as the reuse template) can drive a real client ↔ proxy ↔ owner chain in-process.

### 2.7 Coexistence: non-serve CLI commands and other engine builders

Policy: **only `serve` participates in election.** Everything else keeps working standalone on top
of WS1's WAL, with three targeted adjustments. (Proxying `query`/`ui` through the daemon socket is
a clean phase-2 follow-up — the socket already speaks full MCP — but is not required for
correctness and is out of scope here.)

| Command | Engine site | Behavior with a live owner | Change needed |
|---|---|---|---|
| `index` (`pipeline.go:58-68`, static `1062-1067`) | own store, writer | allowed: WAL makes DB writes safe; node-file writes are picked up by the owner's graph watcher (2.5) | **guard `--force`**: if `daemon.ReadInfo` shows a live owner (verified by a successful socket dial), refuse `index --force` with a clear error — deleting `embeddings.db`+sidecars out from under the owner's open WAL connection is not safe (extends WS1 step 4) |
| `query` (`pipeline.go:146-173`) | full `buildEngine` per invocation | allowed: WAL-safe reads; graph loaded fresh from disk so no staleness | **detach the heatmap**: skipping `heatmap.Save` in `Cleanup` (`pipeline.go:343-353`) is not enough — `HandleContextQuery` also saves per query at `handlers.go:142-146` whenever `e.HeatMap != nil`. When an owner is alive (`daemon.ReadInfo` + successful socket dial), skip `engine.WithHeatMap` in `buildEngine` (`pipeline.go:267-271`): the per-query save is nil-gated at `handlers.go:136` and the `Cleanup` save at `pipeline.go:347` is nil-gated too, so one check suppresses both (cost: no heat recording from standalone queries — acceptable, the owner records its own) |
| `ui` (`pipeline.go:468-529`) | full engine + own watcher + scheduler | allowed to coexist (it has its own fsnotify reload, `internal/api/watcher.go`); its writes go through WAL and the owner's watcher sees its node-file edits | **suppress its scheduler + detach its heatmap** when an owner is alive (same check as `query` — ui also runs `HandleContextQuery`, so the `handlers.go:142-146` per-query save applies); otherwise ui duplicates LLM calls — the exact bug WS2 kills. UI keeps its scheduler when it is the only marmot process |
| `watch` (`pipeline.go:827-889`) | own update.Watcher | **refused** when an owner is alive: it is a strict duplicate of the owner's watcher role (findings `internal-update` watcher.go:30-74) — exit 1 with "vault is served by marmot daemon (pid N); watch is redundant" | add the owner check at `watchLoop` entry |
| `status` (`pipeline.go:766-771`) | read-only `Count` | allowed, no change | — |
| `marmot-eval` seeder (`cmd/marmot-eval/seeder.go:41-67`) | in-process `mcpserver.NewEngine` | runs before any serve is spawned per question; WAL covers accidental overlap. The spawned `serve` in runner.go:38-48 transparently becomes owner (first) — and the daemon's exit-on-idle guarantees vaults are not left owned between questions (findings `cmd-marmot-eval` runner.go) | none |

Cross-vault note: an owner of vault A still opens vault B's `embeddings.db` directly via
`VaultRegistry.ResolveEmbeddingStore` (`internal/namespace/registry.go:221-241`) rather than
dialing vault B's owner. That stays: it is read-only, WAL-safe, and proxy-routing cross-vault reads
is a much larger change. Same for warren-mounted vault writes (`internal/api/handlers.go:448-456`).
The registry's cached remote graphs (`registry.go:75-142`) remain refresh-on-demand — unchanged.

### 2.8 Concrete changes, ordered

1. **New package `internal/daemon`** (~5 files):
   - `lock.go` + `lock_unix.go` (`unix.Flock`) + `lock_windows.go` (stub returning
     `ErrUnsupported`, 2.10): `TryAcquire`/`ReadInfo`/`Lock.WriteInfo`/`Lock.Release`.
   - `socket.go`: path derivation (primary + >96-byte hash fallback), `Listen` (remove stale file,
     listen, chmod 0600), publish path via `Lock.WriteInfo`.
   - `owner.go`: accept loop with per-conn goroutine + session refcount, `WaitIdle(ctx)`,
     `Close()`; `BoundedStop(*summary.Scheduler, timeout)`; `StartGraphWatcher(dir, engine)`
     (extracted from `internal/api/watcher.go:19-109`, shared with the api package).
   - `proxy.go`: `RunProxy` per 2.6 (`ErrOwnerGone`, handshake record/replay, CloseWrite drain).
2. **`cmd/marmot/pipeline.go`**: replace `runServePipeline` body (lines 450–466) with the election
   loop + `runServeOwner` + `runServeStandalone` (2.3). Extract today's body verbatim into
   `runServeStandalone` first (mechanical, keeps `--no-daemon`/Windows path bit-identical).
3. **`cmd/marmot/pipeline.go` `buildEngine`/`Cleanup`** (lines 194–362, 343–353): add an
   owner-alive check (`daemon.ReadInfo` + dial) that skips `engine.WithHeatMap` at 267–271 (see
   2.7 — this nil-gates both the per-query save and the `Cleanup` save); wire it into
   `runQueryPipeline` (146–173) and `runUIPipeline` (468–529, also gate `result.Scheduler.Start`
   at 479–481); owner-alive refusal in `watchLoop` (827–889) and `index --force` (58–62).
4. **`cmd/marmot/main.go`**: `cmdServe` (314–330) gains `--no-daemon` (bool, mirrors
   `MARMOT_NO_DAEMON`) on its `FlagSet` (main.go:315-319). Note `cmdServe` does **not** route args
   through `flags.go`'s `reorderInterspersedFlags` — only `index` (main.go:217) and `bridge`
   (main.go:415) call it, each passing its own flag-name maps — and serve takes no positional
   args, so plain `fs.Bool` is sufficient; no flag-table change anywhere.
5. **`internal/api/watcher.go`**: refactor to call the extracted graph-watch helper (no behavior
   change; keeps ui working standalone).
6. Tests (2.9), then docs: README serve section + release note ("first serve owns the vault;
   `--no-daemon` restores old behavior").

Landing order and release gating are specified in [Sequencing](#sequencing) and
[Rollout](#rollout-stages-and-rollback): WS1 first, then steps 1–2 dark behind `MARMOT_DAEMON=1`
for one release, then flip the default and land steps 3–5 (the 2.7 coexistence adjustments) with
the flip.

### 2.9 Workstream 2 test plan

Unit (`internal/daemon`):
- lock: two `TryAcquire` in one process on one dataDir → second gets `ErrHeld`; release → acquire
  succeeds; **kill-based test**: spawn a helper subprocess (`go test -run TestHelperOwner` exec
  pattern) that acquires and is SIGKILLed → parent acquires immediately (flock auto-release, the
  property that justified the design).
- socket: primary-path happy case; >96-byte vault path → fallback path used and published in
  info.json; stale socket file removed by fresh owner.
- proxy relay: client ↔ `RunProxy` ↔ owner over `io.Pipe`+in-process listener (pattern from
  `internal/mcp/transport_test.go:29-45`): full initialize + tool call round-trip; owner conn
  drop mid-session → reconnect + handshake replay → next tool call succeeds and exactly one
  initialize response reached the client; stdin EOF → clean nil return.
- large-line test: 1MB tool-call line survives the relay (scanner buffer sizing).

`cmd/marmot`:
- `TestServeCommandEOF` (`surface_coverage_test.go:138-152`) unchanged + new assertions: lock free,
  `daemon.sock`/`daemon.info.json` removed after return. No signal-based tests (package constraint,
  `surface_coverage_test.go:246-247`).
- new `TestServeSecondIsProxy`: first serve on pipes (owner), second serve on pipes → assert
  info.json existed, second one's writes are answered (in-process, using `runServePipeline` with
  stdin/stdout injected — requires threading the reader/writer through, or covering this only at
  e2e level if injection is too invasive).
- `watch`-refused and `index --force`-refused cases against a fake live owner (acquire lock + dial-able
  socket in the test).

E2E (`e2e/`, extends the WS1 cases in 1.6):
- `TestConcurrentServes` **tightened** (as promised in 1.6): two serve processes, write via
  process A → query via process B sees the node (read-your-writes across processes — the graph is
  the owner's, so this now must pass, not just "no wedge").
- `TestOwnerFailover`: two serves; SIGKILL the owner (identified via daemon.info.json pid); assert
  the survivor answers the next tool call within a few seconds (re-election + replay) and a third
  serve joins it.
- Shutdown budget: close owner stdin with a proxy attached → owner keeps serving; close proxy
  stdin → owner exits < 5s (the `e2e_test.go:245-249` kill budget), vault left with no lock/socket.
- `TestUIServer` + Playwright `web/e2e/serve.sh` unchanged and must stay green (ui does not
  participate in election; readiness polls at `e2e_test.go:430-443` / `playwright.config.ts:11-16`
  see no new latency because ui never waits on a lock).

### 2.10 Portability

- **Windows**: `flock` doesn't exist and `AF_UNIX` support (Windows 10 1803+) is not exercised by
  this repo's CI. Ship WS2 as unix-only: `lock_windows.go` returns `ErrUnsupported` and
  `runServePipeline` takes the `runServeStandalone` path on `GOOS == "windows"` (2.3) — i.e.
  Windows keeps exactly today's behavior hardened by WS1's WAL+busy_timeout (the driver's WAL
  shared-memory support covers windows per v0.33.2 `vfs/shm.go`). A later Windows daemon can use
  `LockFileEx` + `AF_UNIX` behind the same `internal/daemon` API without touching callers.
- **NFS/network filesystems**: `flock` semantics are fs-dependent; unix sockets can't live on NFS
  at all on some systems. The socket fallback path (os.TempDir, always local) covers the socket;
  for the lock, document "vaults on network filesystems should set MARMOT_NO_DAEMON=1". Not
  auto-detected in v1.
- **macOS sandbox/TMPDIR**: fallback socket path uses `os.TempDir()` (per-user
  `/var/folders/.../T/`, ~50 chars — fits). The scratch/e2e `HOME` overrides don't affect it, and
  hash-keying keeps it collision-free.

### 2.11 Workstream 2 risks & mitigations

| Risk | Mitigation |
|---|---|
| Owner SIGKILLed by its MCP client's process-group kill while proxies attached | Designed for: re-election + handshake replay (2.4/2.6); e2e `TestOwnerFailover` pins it |
| Handshake replay confuses a strict MCP client (duplicate/missing responses at failover) | Replay is verbatim same-binary lines with the duplicate response filtered by id; degraded mode `MARMOT_PROXY_NO_RESUME=1` (proxy exits, client restarts serve); in-flight requests time out client-side |
| Socket path exceeds `sun_path` (deep temp dirs, macOS 104-byte cap) | Hash fallback under `os.TempDir()`, actual path always published in `daemon.info.json` (2.2) |
| Scheduler `Stop()` blocks shutdown past the e2e 5s kill budget | `BoundedStop` 3s cap; abandoned regen writes are atomic (2.3) |
| `mcp-go` session semantics with multiple concurrent servers on one Engine | One fresh `Server` per connection, shared Engine only (2.3); relay unit test drives two concurrent sessions |
| Election/startup race: lock acquired but info.json not yet written | Proxies retry `ReadInfo`+dial with 50ms backoff inside the election loop (2.3); **bound the retry** (~10s) so an owner wedged between flock and listen yields a clear error instead of a silent spin; e2e startMCP has a 30s initialize timeout as headroom |
| `index --force` deletes DB under the owner's open WAL connection | Refused when an owner is dial-able (2.7) |
| Divergent behavior standalone vs daemon (`--no-daemon`, Windows) | Standalone path is the extracted, unmodified old body; both paths covered by the same e2e tool-call suite |
| Regression risk of touching the only serve entry point | Land behind `MARMOT_DAEMON` opt-in for one release (2.8), flip default after eval + e2e soak |

---

## Testing & Rollout

Per-workstream test additions are specified in 1.6 (WS1) and 2.9 (WS2). This section maps them onto
the existing suites and CI, specifies the shared multi-process contention test (the canonical
regression for the reproduced freeze), and defines the release/rollback procedure.

### Test inventory: where each layer lives today

- **Unit**: `go test -race -count=1 -timeout 300s ./...` (CI "Unit Tests" step,
  `.github/workflows/ci.yml`). Relevant existing files: `internal/embedding/store_test.go` +
  `coverage_test.go`, `internal/mcp/server_test.go` (real on-disk embeddings.db per test),
  `internal/mcp/classify_test.go` (`:memory:`), the in-process write-concurrency template
  `internal/mcp/concurrency_test.go:13-40`, transport-over-`io.Pipe` template
  `internal/mcp/transport_test.go:29-45`, and stress tests `internal/routes/routes_stress_test.go`,
  `internal/traversal/bridged_stress_test.go`. New WS1 tests (1.6 #1–4) and the whole
  `internal/daemon` unit suite (2.9) land here and run under the race detector for free.
- **Integration** (build tag `integration`): `internal/integration_stress_test.go` via
  `go test -tags integration -race ./internal/` (CI "Integration Tests" step) — real engine + real
  embeddings.db, but single-process.
- **E2E** (build tag `e2e`): `e2e/e2e_test.go` via `make e2e`
  (= `go test -tags e2e -count=1 -v ./e2e/`) — real binary over stdio JSON-RPC (`TestMCPServer`),
  UI HTTP (`TestUIServer`); plus the Playwright browser suite (`web/e2e/serve.sh`,
  `web/playwright.config.ts`) via `make e2e-ui`. Both run in the CI "E2E + UI validation" job.
- **Eval harness**: `cmd/marmot-eval` (`make eval`, requires `make build` → `bin/marmot`) spawns
  real `marmot serve --dir` processes under the claude CLI — the closest proxy for production
  multi-client usage; used as a manual soak gate below, not in CI.

Known gap being closed (findings, e2e section): **no test today spawns two concurrent
`marmot serve` processes against one vault** — the reproduced freeze scenario.
`internal/mcp/concurrency_test.go` is in-process only (`nsMu` serializes it) and does not
reproduce the failure.

### The multi-process contention test (canonical freeze regression)

Two layers, mirroring the two reproduced failure modes from the
[problem summary](#context--problem-summary):

1. **In-process, multi-connection** (fast, race-detected): `TestNewStore_ConcurrentConns` in
   `internal/embedding` (1.6 #3). Two `Store`s on one file = two OS-level file handles = the real
   OFD/shm locking path, without process-spawn overhead. Runs in the ordinary unit step.
2. **Multi-process, real binary**: the two new e2e tests named in 1.6, implemented in
   `e2e/e2e_test.go` (tag `e2e`) reusing `seedProject`/`startMCP`/`hermeticEnv`
   (`e2e_test.go:56-106,208-257`). Each pins one reproduced failure mode:
   - **`TestIndexDuringServe` — failure mode 1** (instant `database is locked` on concurrent
     write): serve A holds an initialized MCP session issuing `context_write` in a loop;
     concurrently run `marmot index --dir .marmot`. Assert: index exits 0, all of A's writes
     return non-error results, and a post-index `context_query` returns embedded results. Do not
     trust the index exit code alone — the indexer swallows upsert failures into
     `RunResult.Errors` (`internal/indexer/runner.go:358-372`).
   - **`TestConcurrentServes` — failure mode 2** (reader's SHARED lock parks a COMMIT on PENDING;
     all reads wedge): serve A runs a tight `context_query` loop (the long `SearchActive` scans
     holding SHARED locks, `internal/embedding/store.go:216-233,344-367`); serve B runs a
     `context_write` burst (COMMITs). Sustain ~10s. Assert every response arrives within a
     per-call deadline (5s), zero `database is locked` in either process's responses or stderr,
     and both processes exit within the harness's post-EOF budget (`e2e_test.go:239-249`).
   - **Red-first verification**: before merging WS1, run this test once against the v0.17.1
     baseline binary and record in the PR that it reproduces the lock errors/wedge — the proof the
     test guards the right defect.
   - **WS2 reuse**: the same harness carries the tightened assertions of 2.9
     (cross-process read-your-writes, `TestOwnerFailover`, shutdown budget) — assertions are
     additive, no parallel harness.

### CI implications (`.github/workflows/ci.yml`, `Makefile`)

- **No new jobs required**: WS1 unit tests and the `internal/daemon` package are picked up by the
  existing Unit Tests / Vet / golangci-lint steps; the new e2e tests ride `make e2e` in the
  existing "E2E + UI validation" job.
- **Budget**: the contention tests add roughly 15–30s to `make e2e`. Keep sustained-load phases
  short and per-call deadlines generous (CI runners are slow); assert on state, not on durations.
  The 300s unit-step timeout has ample headroom for the ~2s `TestNewStore_ConcurrentConns`.
- **`go mod tidy` gate**: WS1 rewrites `go.mod`/`go.sum` (1.3); the existing "Check go mod tidy"
  step fails unless both are committed together.
- **Platform gap (action item)**: CI runs ubuntu-only. The indefinite-commit-wait wedge is *not*
  darwin-specific (both platforms take the same `F_OFD_SETLKW` PENDING path — see the corrected
  analysis in 1.3), but the darwin VFS code paths (`F_OFD_SETLKWTIMEOUT` timed locks,
  `F_BARRIERFSYNC` sync) are never executed by linux CI, and WS2's `sun_path` fallback (2.2)
  matters most on macOS.
  Add a `macos-latest` leg to the e2e job running `make e2e` only (skip Playwright there — the
  release job already uses macos runners so caching exists), or at minimum require a local darwin
  `make e2e` run before each rollout stage below.
- **Race coverage boundary**: the e2e binary is built without `-race`, so cross-process races are
  caught only by behavior assertions; in-process concurrency (daemon accept loop, session
  refcount, `WaitIdle`) must have real raced unit coverage in `internal/daemon` (2.9), not only
  e2e coverage.
- **Playwright suite stays untouched and green at every stage**: `ui` never participates in
  election (2.7), `serve.sh`'s SIGTERM teardown keeps working because ui signal handling is
  unchanged, and the `/api/version` readiness check (`playwright.config.ts:11-16`) sees no new
  startup latency.

### Rollout stages and rollback

Constraint that shapes everything: `auto-tag.yml` tags and **releases every green push to
`main`** — there is no manual release gate, so feature flags are the only staging mechanism.

- **Stage 0 — WS1** (one self-contained PR → one release): driver upgrade + WAL/busy_timeout +
  `--force` sidecar fix + 1.6 tests together, so no intermediate release ships partial protection.
  Release note: "upgrade all marmot binaries on a machine together; the WAL flip is per-DB-file
  and persistent" (1.5). Rollback: `git revert` (next auto-release) plus the one-liner
  `PRAGMA journal_mode=DELETE` for any vault that must serve old binaries.
- **Stage 1 — WS2 dark launch**: `internal/daemon` + the election loop, gated on
  `MARMOT_DAEMON=1` (default off — inert in the auto-shipped release). Daemon e2e/unit tests set
  the env var explicitly. Soak gates before Stage 2: `make eval` (real serve spawns per question,
  findings `cmd-marmot-eval`), local dogfooding with `MARMOT_DAEMON=1` across at least two MCP
  clients, darwin `make e2e`.
- **Stage 2 — flip the default**: daemon on; opt-out via `--no-daemon`/`MARMOT_NO_DAEMON=1`
  (the 2.3 end state). The coexistence adjustments of 2.7 (`query`/`ui` heatmap detach +
  ui-scheduler suppression, `watch` refusal, `index --force` guard) land in this stage — they only matter once owners exist
  by default. Release note: "first serve owns the vault". Rollback: `MARMOT_NO_DAEMON=1`
  (no binary change) or revert the default-flip commit.
- **Compatibility invariants at every stage** (from findings): `marmot serve --dir <vault>`
  remains the complete MCP invocation (eval `runner.go:38-48`, user MCP configs);
  newline-delimited JSON-RPC framing on stdout is preserved byte-for-byte through owner and proxy
  (2.6); serve exits <5s after stdin EOF (`e2e_test.go:245-249`); `bin/marmot` remains the
  `make build` output (eval `main.go:36`).

---

## Risks & Mitigations

Per-workstream engineering risks are tabled in 1.7 (WS1) and 2.11 (WS2). This table covers the
program-level and cross-workstream risks.

| Risk | Mitigation |
|---|---|
| Auto-release on every merge (`auto-tag.yml`) ships half-landed work | WS1 is one self-contained PR (Stage 0); WS2 lands dark behind `MARMOT_DAEMON=1` (Stage 1) and flips only in Stage 2 |
| Linux-only CI never exercises the darwin-only VFS paths (`F_OFD_SETLKWTIMEOUT` timed locks, `F_BARRIERFSYNC`) or macOS `sun_path` limits (the commit-wait wedge itself is platform-general — 1.3) | macos e2e leg (or mandatory local darwin `make e2e`) before each stage; the 1.4 empirical driver check was darwin-local — keep that discipline |
| The contention test passes for the wrong reason (never reproduced the original bug) | Red-first verification against the v0.17.1 baseline binary, recorded in the WS1 PR |
| WS1 soak hides WS2-class bugs: writes stop failing but processes still disagree (stale graphs, duplicate schedulers/LLM spend, heatmap clobbers) | Explicit scope split (1.5 "What WS1 does NOT fix"); `TestConcurrentServes` asserts only availability under WS1 and is tightened to read-your-writes under WS2 (1.6 → 2.9) |
| Landing WS1+WS2 together confounds driver-upgrade regressions with daemon behavior changes | [Sequencing](#sequencing): separate releases with an eval + e2e soak between them |
| Eval harness breakage (`bin/marmot`, plain `serve --dir` per question) | Invocation contract frozen (rollout invariants); `make eval` is a mandatory Stage 1 → Stage 2 gate |
| Timing-flaky multi-process tests in CI (linger/`WaitIdle`, 5s kill budget, 50ms election backoff) | Assert on end state (lock free, `daemon.sock`/`daemon.info.json` removed, response received) rather than durations; generous per-call deadlines; no SIGINT/SIGTERM in `cmd/marmot` tests (package constraint, `surface_coverage_test.go:246-247`) — signal paths are e2e-only |
| MCP client diversity beyond the e2e harness and claude CLI (framing, handshake strictness at failover) | Proxy is a byte-preserving line relay (2.6) with `MARMOT_PROXY_NO_RESUME=1` degraded mode; Stage 1 dogfood across ≥2 MCP clients before the flip |
| Residual cross-process races neither WS fixes: `routes.yml` (`internal/routes/routes.go:129-178`), `.marmot-data/.env` (`internal/config/config.go:159-192`), `_warren.md` (`internal/warren/warren.go:1065-1077`), live-vault warren import (1.5) | Documented follow-ups (unique tmp names / flock, or route through the owner; checkpoint-before-copy for import). None can wedge a vault — worst case is one lost config write |

---

## Sequencing

**Ship WS1 first, alone.**

1. **Blast radius**: WS1 is one production-file change (`internal/embedding/store.go`) + dependency
   bump + a two-line `--force` fix, verified by the existing suites plus 1.6, and it removes the
   outage-class failure (the vault-wide wedge). WS2 rewrites the serve entry point, adds a package,
   and changes process topology — a different review and soak profile.
2. **WS2 depends on WS1**: the coexistence policy (2.7) is only sound because WAL+busy_timeout
   makes non-serve CLI writers safe alongside an owner, and the re-election window (2.4) briefly
   has two processes touching the DB. WS1 has no dependency on WS2.
3. **Regression attribution**: a report after Stage 0 implicates the driver/pragmas; after
   Stage 2, the daemon. Interleaving the two destroys that signal.

WS2 then ships as a separate, flag-staged deliverable (Stage 1 dark launch → Stage 2 default
flip, see [Rollout](#rollout-stages-and-rollback)). One reconciliation across the document: the
2.3 code sketch shows the **end state** (daemon default-on, `MARMOT_NO_DAEMON=1`/`--no-daemon`
opt-out); Stage 1 inverts the gate (election runs only when `MARMOT_DAEMON=1`) over the identical
code path, so the flip is a one-line default change. The election loop is additive — with the
gate off, `runServePipeline` is byte-for-byte today's behavior.

WS1 can merge as soon as review plus the red-first contention verification complete; nothing in
WS2 needs to be designed further before WS1 lands.
