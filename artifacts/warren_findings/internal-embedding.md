# internal/embedding scanner findings

## internal/embedding

Files: store.go (611), store_wal_test.go (164), mock.go (111), embedder.go (13), provider.go (25), openai.go (227) + tests. This package is the direct target of two Tier-1 fixes (read-only remote opens; checkpoint-before-copy) and touches Tier-4 model-skew.

### store.go:39-70 — NewStore (Tier 1: read-only remote opens)
Review's cite `store.go:47-89` is accurate (NewStore body 47-70, initSchema 72-90). NewStore unconditionally sets busy_timeout + `PRAGMA journal_mode = WAL` + runs DDL, so opening a remote vault's embeddings.db flips it to WAL and creates -wal/-shm sidecars.

```go
47	func NewStore(dbPath string) (*Store, error) {
48		db, err := sqlite3.Open(dbPath)
...
55		if err := db.BusyTimeout(5 * time.Second); err != nil {
59		if err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
64		s := &Store{db: db}
65		if err := s.initSchema(); err != nil {
```

Constraints for `NewStoreReadOnly`:
- ncruces go-sqlite3 v0.33.2 provides `sqlite3.OpenFlags(filename, sqlite3.OPEN_READONLY)` (conn.go:51) — preferable to `file:...?mode=ro` URI, both work.
- A read-only conn CANNOT run initSchema (CREATE TABLE / ALTER at store.go:73-87 would fail with SQLITE_READONLY) and cannot exec `journal_mode = WAL` if the DB is not already WAL. NewStoreReadOnly must skip both; busy_timeout is fine.
- Read-only open of a WAL DB still needs -shm creation permission unless immutable=1; since remote vaults are local git checkouts this is fine, but note it for network mounts.
- `Store.db` is unexported and `Store.mu` guards every method — the read-only variant returns the same `*Store` type; all read methods (Search, SearchActive, FindSimilar, StaleCheck, Count, StoredDimension) work unchanged. Upsert/UpdateStatus/Delete would return SQLITE_READONLY errors at Exec time; acceptable, or add a `readOnly bool` guard for clearer errors.

### store.go:72-90 — initSchema (Tier 1 constraint)
Runs `CREATE TABLE IF NOT EXISTS` plus a blind `ALTER TABLE ... ADD COLUMN status` whose error is deliberately ignored (line 87-88). Any read-only path must bypass this entirely.

### Checkpoint-before-copy (Tier 1, import/burrow) — NO helper exists yet
There is no `Checkpoint`/`wal_checkpoint` anywhere in the repo. Because `db` is unexported, warren's copy path cannot checkpoint externally; the fix must add a Store method, e.g.:

```go
func (s *Store) Checkpoint() error {
	s.mu.Lock(); defer s.mu.Unlock()
	return s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
}
```

Call sites that copy embeddings.db and currently skip the sidecars instead of checkpointing: internal/warren/warren.go:1319-1320 (skip map for `-wal`/`-shm`), copyMarmotVault warren.go:1325, copyDir warren.go:1289, Materialize copy warren.go:976. Skipping the WAL without checkpointing loses all un-checkpointed writes — exactly the review's Tier-1 point; the fix is open via NewStore + Checkpoint() + Close() before copy.

### store.go:34-37 — Store struct (Tier 2: concurrent search safety)
`sync.Mutex mu` serializes all ops on one conn; safe for concurrent search within a process. Cross-process safety comes from WAL + 5s busy_timeout. A registry Rebuild that closes/reopens stores must not race in-flight SearchActive — Close() takes mu (line 547-551), so a search in progress finishes first, but a search started after Close gets a closed-conn error; refresh code should swap the pointer, not Close under readers.

### store.go:326-397 SearchActive, 268-298 checkModel, 349 WHERE model = ? (Tier 4 model-skew)
Correction to review line ~148: model skew does NOT "degrade silently" at the store level. checkModel (called by Search/SearchActive/FindSimilar) returns a hard error `model mismatch: query model %q does not match stored embeddings` when ANY stored row has a different model. The silent degradation happens at the callers that discard the error: internal/mcp/handlers.go:88 (`remoteResults, _ = remoteStore.SearchActive(...)`) and internal/api/handlers.go:569 area. Doctor's model-skew check can use checkModel semantics or `SELECT DISTINCT model`.

### NewStore call sites (complete list, non-test) — everything a NewStoreReadOnly split touches
- internal/namespace/registry.go:223 — `ResolveEmbeddingStore` (registry.go:184) opens REMOTE vault DBs; THE site to switch to read-only. Caches on `RemoteVault.EmbStore` (registry.go:23) forever — Tier-2 TTL issue lives here too.
- internal/api/handlers.go:450 — opens a mounted warren project's embeddings.db directly (`mount.Path/.marmot-data/embeddings.db`); ALSO a remote/read path the review's read-only fix must cover (review focuses on registry; don't miss this one).
- internal/mcp/engine.go:160 — buildEngine local vault store (must stay read-write).
- cmd/marmot/pipeline.go:75, 986, 1083, 1287 — local index/query pipeline (read-write; 986 and 1287 are query-side and could be RO candidates but are local).

### Method signatures the plan may reference
```go
store.go:143 func (s *Store) Upsert(nodeID string, embedding []float32, summaryHash string, model string) error
store.go:201 func (s *Store) Search(queryEmbedding []float32, topK int, model string) ([]ScoredResult, error)
store.go:326 func (s *Store) SearchActive(queryEmbedding []float32, topK int, model string) ([]ScoredResult, error)
store.go:403 func (s *Store) FindSimilar(queryEmbedding []float32, threshold float64, model string) ([]ScoredResult, error)
store.go:508 func (s *Store) StaleCheck(nodeID string, currentHash string) (bool, error)
store.go:302 func (s *Store) UpdateStatus(nodeID, status string) error
store.go:547 func (s *Store) Close() error
```

### store_wal_test.go:94-164 — reusable test template (Tier 1/2 tests)
`TestNewStore_ConcurrentConns` is the canonical two-connections-one-file concurrency harness (writer Upsert loop + reader SearchActive loop, 2s deadline, error recorder). Directly reusable for a "read-only open doesn't create -wal/-shm" test and for refresh-under-concurrent-search tests. `journalMode(t, s)` helper (lines 12-24) reads PRAGMA journal_mode via the unexported conn — useful for asserting a RO open does not flip journal mode. `TestNewStore_WALEnabled` (26-64) asserts -wal sidecar appears after Upsert — invert this for the RO test.

### mock.go:12-40 — MockEmbedder (hermetic test setup)
`NewMockEmbedder(model)` produces deterministic 1536-dim trigram-hash vectors (similar texts → similar vectors), no network. Standard hermetic vault fixture across the repo (see cmd/marmot/warren_test.go:363 which seeds a remote vault's embeddings.db with NewStore + MockEmbedder — template for warren e2e). provider.go:21 wires `mock`/`mock:<name>` provider names into NewEmbedder.

### Review-error flags
1. review line ~148: model skew is a hard error from checkModel, not silent empty results; silence is caller-side error-swallowing (mcp/handlers.go:88, api/handlers.go:450ff).
2. Review's read-only fix mentions only ResolveEmbeddingStore; internal/api/handlers.go:450 is a second remote-open of embeddings.db via plain NewStore that equally flips remote DBs to WAL.
3. `file:...?mode=ro` works, but `sqlite3.OpenFlags(path, sqlite3.OPEN_READONLY)` is the idiomatic ncruces call; either way initSchema and the WAL pragma must be skipped or the open fails.
