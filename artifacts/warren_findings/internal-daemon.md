# internal/daemon findings (branch multiprocess-lock-fix @ 1f14f3e)

## internal/daemon

Package: single-owner election for `marmot serve` (flock on `<dataDir>/daemon.lock`, unix-socket MCP relay, graph watcher). Warren-relevant as (a) the flock utility the review's Tier 1 `_warren.md`/routes.yml RMW fix is told to reuse, (b) the watcher whose skip rules exclude warren state files (Tier 2 freshness — reloadWarrenState will need its own trigger), (c) the exec-helper flock test template Tier 1's concurrency tests should mirror. There are NO refresh stubs, no warren imports, and no `embedding.NewStore`/`copyDir`/manifest calls anywhere in this package.

### lock.go:75-96 — TryAcquire (the flock utility Tier 1 fix 1.5 says to reuse)
Relevance: Tier 1 "flock for `_warren.md`/routes.yml RMW". Note the constraint: `TryAcquire` is **non-blocking and dataDir-scoped** — it hardcodes `lockFileName = "daemon.lock"` (lock.go:43) and creates the whole dataDir. Reusing it for a sibling-lockfile RMW requires either generalizing the filename or (simpler) reusing only the platform primitive `tryFlock` (lock_unix.go:16-22) plus adding a *blocking* variant (`unix.LOCK_EX` without `LOCK_NB`), since an RMW writer should wait, not fail with ErrHeld.

```go
75	func TryAcquire(dataDir string) (*Lock, error) {
76		if err := os.MkdirAll(dataDir, 0o755); err != nil {
...
79		path := filepath.Join(dataDir, lockFileName)   // hardcoded "daemon.lock"
80		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
...
84		if err := tryFlock(f); err != nil {
...
95		return &Lock{dataDir: dataDir, file: f}, nil
96	}
```

### lock_unix.go:16-22 / lock_windows.go — tryFlock platform split
Relevance: Tier 1 flock reuse. The per-OS split already exists (`//go:build unix`); Windows returns `ErrUnsupported` (lock_windows.go, 14 lines). A warren RMW lock must decide its Windows story — daemon mode simply refuses on Windows, but warren edits presumably must still work there, so blind reuse of this package's semantics would break Windows warren writes.

```go
16	func tryFlock(f *os.File) error {
17		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
18		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
19			return ErrHeld
20		}
21		return err
22	}
```

### owner.go:296-303 and 326-330 — watcher skip rules (review cite `owner.go:297-303` is accurate, off by ~1 line)
Relevance: Tier 2 daemon-era freshness. The graph watcher ignores every `_`-prefixed .md file and never watches `_`-/`.`-prefixed dirs — so owner processes will NEVER see `_warren.md`, `routes.yml` (not .md at all, filtered at :297), or anything under `.marmot-data/warrens/`. Any reloadWarrenState wiring must add its own fsnotify watch or a socket refresh verb; it cannot piggyback on this loop without changing these filters.

```go
296		// Only react to .md files.
297		if !strings.HasSuffix(event.Name, ".md") {
298			continue
299		}
300		// Ignore underscore-prefixed files (_config.md, _summary.md, etc.)
301		if strings.HasPrefix(filepath.Base(event.Name), "_") {
302			continue
303		}
...
328	func skipDirName(name string) bool {
329		return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
330	}
```

### owner.go:228-236 — StartGraphWatcher / StartGraphWatcherNotify signatures
Relevance: Tier 2 — where a warren-refresh hook would attach; `internal/api/watcher.go:15` already consumes `StartGraphWatcherNotify(vaultDir, s.engine, s.NotifyChange)`, and `cmd/marmot/pipeline.go:592` consumes `StartGraphWatcher(dir, result.Engine)`. Signature changes here touch both.

```go
228	func StartGraphWatcher(dir string, eng *mcp.Engine) (stop func(), err error) {
229		return StartGraphWatcherNotify(dir, eng, nil)
230	}
236	func StartGraphWatcherNotify(dir string, eng *mcp.Engine, onReload func()) (stop func(), err error) {
```

### owner.go:334-343 — reloadGraph replaces only the node graph
Relevance: Tier 2. Reload path is `graph.LoadGraph(node.NewStore(dir))` → `eng.SetGraph(newGraph)` — it does NOT rebuild VaultRegistry, warren mounts, or routes. This is the concrete mechanism behind the review's "buildEngine load-once warren wiring is a real problem under the daemon": a long-lived owner refreshes nodes forever but its warren state is frozen at startup.

```go
334	func reloadGraph(dir string, eng *mcp.Engine) bool {
335		newGraph, err := graph.LoadGraph(node.NewStore(dir))
...
340		eng.SetGraph(newGraph)
```

### proxy.go:187-206 — no refresh verb on the socket protocol
Relevance: Tier 2 "real refresh endpoint/command". The socket carries raw newline-delimited MCP JSON-RPC only (`RunProxy(stdin io.Reader, stdout io.Writer, socketPath string) error` at :187; `RunProxySession` at :206 with handshake-replay resumption, `MARMOT_PROXY_NO_RESUME` at :214). There is no side-channel/control verb; a `marmot warren refresh` CLI hitting the owner would need either a new MCP tool routed through the normal engine or a second listener — the protocol itself has no room for out-of-band commands without framing changes.

### lock_test.go:106-195 — exec-helper flock test template (reuse for Tier 1 concurrency tests)
Relevance: the review's test program explicitly says "flock test mirrors internal/daemon's". Template: `TestFlockReleasedOnKill` spawns `os.Args[0]` with `-test.run=TestHelperLockHolder$`, gates the helper body on `MARMOT_DAEMON_LOCK_HELPER=1` env (t.Skip otherwise), passes the dir via env, synchronizes on a stdout sentinel (`HELPER_ACQUIRED`), then SIGKILLs and polls `TryAcquire` with a 5s deadline.

```go
109		cmd := exec.Command(os.Args[0], "-test.run=TestHelperLockHolder$", "-test.v")
110		cmd.Env = append(os.Environ(),
111			"MARMOT_DAEMON_LOCK_HELPER=1",
112			"MARMOT_DAEMON_TEST_DATADIR="+dataDir,
113		)
...
180	func TestHelperLockHolder(t *testing.T) {
181		if os.Getenv("MARMOT_DAEMON_LOCK_HELPER") != "1" {
182			t.Skip("helper process only")
```

### socket.go:12-30 — SocketPath and the 96-byte macOS cap
Relevance: constraint for any new per-warren daemon socket/lock: `maxSocketPathLen = 96` with a hashed fallback path; deep `.marmot-data/warrens/<id>/projects/<p>/.marmot/.marmot-data` dirs would routinely overflow, so warren-side sockets must go through `SocketPath` (or reuse the published `Info.Socket`, never re-derive — per lock.go:51-54 doc).

### Call sites outside this package (functions the plan may touch)
- `daemon.TryAcquire`: cmd/marmot/pipeline.go:498; cmd/marmot/surface_coverage_test.go:183,197,280
- `daemon.ReadInfo`: pipeline.go:505,665; surface_coverage_test.go:250
- `daemon.RunProxySession`: pipeline.go:511
- `daemon.StartGraphWatcher`: pipeline.go:592; `StartGraphWatcherNotify`: internal/api/watcher.go:15
- `daemon.NewOwner`: pipeline.go:600 (`runServeOwner(dir string, lock *daemon.Lock, stdin io.Reader) error` at pipeline.go:575)
- `daemon.BoundedStop`: pipeline.go:608,639
- `daemon.SocketPath`: surface_coverage_test.go:179,203,286

### Review-accuracy notes
- The review's `internal/daemon/owner.go:297-303` cite for underscore-file skipping is essentially correct (the .md filter is :297, underscore skip :301) — no meaningful drift.
- The review implies the daemon's flock utilities are drop-in for `_warren.md` RMW; they are not drop-in: TryAcquire is non-blocking (ErrHeld, not wait), hardcodes the `daemon.lock` filename, and is a no-op-refusal on Windows. The plan should extract/extend `tryFlock` with a blocking mode and parameterized path rather than call TryAcquire.
- No refresh stub exists in this package; any "wired to the owner watcher" language in Tier 2 must mean *new* code here, not modification of an existing hook.
