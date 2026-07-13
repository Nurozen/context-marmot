# internal/embedding

## internal/embedding

### store.go:39-54
Workstream 1 (quick fix) — this is THE SQLite open site. Bare `sqlite3.Open(dbPath)`: no WAL, no busy_timeout, no open flags. The quick fix (journal_mode(WAL) + busy_timeout(5000)) goes here, either via PRAGMA Exec right after open or via `sqlite3.OpenFlags`/URI params. Also the only place `sqlite3.Open` is called in this package; call sites elsewhere (internal/namespace/registry.go:223, internal/mcp/engine.go:160, internal/api/handlers.go:450, cmd/marmot/pipeline.go:64/768/859/1063) all funnel through `NewStore`, so one change covers all processes.

```go
41	func NewStore(dbPath string) (*Store, error) {
42		db, err := sqlite3.Open(dbPath)
43		if err != nil {
44			return nil, fmt.Errorf("open sqlite: %w", err)
45		}
46
47		s := &Store{db: db}
48		if err := s.initSchema(); err != nil {
49			_ = db.Close()
50			return nil, fmt.Errorf("init schema: %w", err)
51		}
52
53		return s, nil
54	}
```

### store.go:3-14
Workstream 1 — imports to audit for the v0.17.1 → v0.33.x upgrade: `github.com/ncruces/go-sqlite3` plus the blank `embed` import (bundled WASM sqlite; new versions ship a newer wazero + sqlite build, so go.mod/go.sum both change). go.mod currently pins v0.17.1 (go.mod:9) with `ncruces/julianday v1.0.0` indirect.

```go
12		"github.com/ncruces/go-sqlite3"
13		_ "github.com/ncruces/go-sqlite3/embed"
14	)
```

### store.go:34-37
Workstream 1 — `*sqlite3.Conn` stored directly; a `sync.Mutex` guards ALL access because Conn is not goroutine-safe. This mutex only serializes within one process — it does nothing across the multiple `marmot serve` processes, which is why cross-process locking fails. Also relevant to workstream 2: with a single-owner daemon this in-process mutex becomes the sole serialization point (sufficient), so the daemon design removes the multi-process SQLite contention entirely.

```go
34	type Store struct {
35		db *sqlite3.Conn
36		mu sync.Mutex // sqlite3.Conn is not safe for concurrent use
37	}
```

### store.go:56-74
Workstream 1 — schema init runs `Exec` (CREATE TABLE + ALTER TABLE migration) immediately on every open. Under multi-process startup these are the first writes that hit "database is locked" with no busy_timeout; also the natural place to add the WAL/busy_timeout PRAGMAs (before or inside initSchema). Note the deliberately-ignored ALTER TABLE error at line 71 — after the driver upgrade verify the error kind is still safely ignorable.

```go
56	func (s *Store) initSchema() error {
57		err := s.db.Exec(`
...
71		_ = s.db.Exec(`ALTER TABLE embeddings ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`)
```

### store.go:110-123 (pattern repeated at 149-178, 208-236, 253-281, 290-305, 333-341, 414-422, 474-485, 496-510, 518-527)
Workstream 1 — the full inventory of driver API surface used, to check against v0.33.x: `Conn.Prepare` (3-return form `stmt, _, err`), `Stmt.Step`, `Stmt.Err`, `Stmt.Exec`, `Stmt.Close`, `Stmt.BindText`, `Stmt.BindBlob`, `Stmt.ColumnText`, `Stmt.ColumnInt`, `Stmt.ColumnRawBlob`, `Conn.Exec`, `Conn.Close`. `ColumnRawBlob` is the riskiest: it returns memory only valid until the next Step/Close; here the blob is fully consumed by `deserializeFloat32` before stepping (safe), but confirm the method still exists/behaves the same in v0.33.x.

```go
110		stmt, _, err := s.db.Prepare(`SELECT embedding FROM embeddings LIMIT 1`)
...
116		if !stmt.Step() {
117			return 0, nil // empty store
118		}
119		blob := stmt.ColumnRawBlob(0)
```

### store.go:216-233 and 344-367, 425-450
Workstream 1 — long-running read scans: `Search`/`SearchActive`/`FindSimilar` hold a read statement open while scanning the entire embeddings table in Go. These are exactly the readers whose SHARED lock a concurrent COMMIT from another process trips over (PENDING-lock parking / F_OFD_SETLKW hang in v0.17.1). WAL mode makes these readers non-blocking; the driver upgrade fixes the indefinite commit wait.

```go
216		for stmt.Step() {
217			nodeID := stmt.ColumnText(0)
218			blob := stmt.ColumnRawBlob(1)
```

### store.go:530-535
Workstream 2 — `Close()` is the only lifecycle hook; the daemon owner must ensure exactly one process opens/closes this. No file locking, PID, or socket logic exists in this package (correctly — election belongs in the serve layer).

```go
531	func (s *Store) Close() error {
532		s.mu.Lock()
533		defer s.mu.Unlock()
534		return s.db.Close()
535	}
```

### store_test.go:8-16 and coverage_test.go:282-307
Test impact for both workstreams — tests open stores via `NewStore(":memory:")` (store_test.go:10) and a file path (coverage_test.go:293). After the WAL change, verify `:memory:` still works (PRAGMA journal_mode=WAL on :memory: is a no-op returning "memory" — the code must not treat that as an error). `TestNewStore_OpenError` (coverage_test.go:282-289) asserts open fails for a path in a missing directory — behavior should persist across the driver upgrade. No test currently exercises multi-connection/multi-process concurrency; both workstreams need new tests here.

```go
282	func TestNewStore_OpenError(t *testing.T) {
283		// A path inside a non-existent directory cannot be opened.
284		badPath := filepath.Join(t.TempDir(), "no-such-dir", "store.db")
285		_, err := NewStore(badPath)
```

### embedder.go / provider.go / openai.go / mock.go / openai_test.go / temporal_test.go
No relevant findings — pure embedding-provider abstractions (HTTP OpenAI client, mock, interface) with no SQLite, process, lifecycle, socket, or scheduler code.
