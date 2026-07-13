package embedding

import (
	"bytes"
	"container/heap"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/ncruces/go-sqlite3"
)

// ScoredResult represents a search result with its similarity score.
type ScoredResult struct {
	NodeID string
	Score  float64
}

// Store manages vector embeddings in SQLite with Go-side KNN search.
//
// Embeddings are stored as BLOBs in a standard SQLite table. Search
// is performed by scanning all embeddings and computing L2 distance
// in Go, returning the top-K closest results. This approach is suitable
// for the expected scale (hundreds to low thousands of nodes per
// namespace) and avoids a broken upstream dependency on sqlite-vec
// WASM bindings.
//
// When the sqlite-vec Go bindings are fixed for the current ncruces
// go-sqlite3 ABI, this can be upgraded to use the vec0 virtual table
// for hardware-accelerated search.
type Store struct {
	db       *sqlite3.Conn
	mu       sync.Mutex // sqlite3.Conn is not safe for concurrent use
	readOnly bool       // opened via NewStoreReadOnly; all writes rejected
	closed   bool       // Close ran; all use returns ErrStoreClosed instead of panicking
}

// ErrStoreClosed is returned by every Store method after Close. It exists so
// a search racing a registry Refresh/Rebuild (which swap the cached store
// and close the old handle after their lock is released) fails loudly with a
// gateable error instead of panicking inside the SQLite bindings.
var ErrStoreClosed = errors.New("embedding store is closed")

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

// NewStoreReadOnly opens an existing embeddings DB without mutating it in
// any way: no WAL pragma (which would flip the journal mode of someone
// else's checkout), no schema init, no migration, and a missing file is an
// error rather than an empty database created in a remote vault. Only
// busy_timeout is set so reads retry politely against a concurrent writer.
//
// Caveat: SQLite requires the -shm index to read a WAL-mode database, so a
// read-only open of an already-WAL DB still creates empty, inert -wal/-shm
// sidecars next to it (and needs create permission for them). Remote vaults
// are local git checkouts today, so this holds; the sidecars carry no data
// and the main database file is never modified. If vaults ever live on
// read-only network mounts, an immutable=1 URI open is the escape hatch
// (unsafe against concurrent writers, hence not the default).
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

// errReadOnly is the uniform rejection for write methods on read-only stores,
// surfaced instead of a raw SQLITE_READONLY at Exec time.
func errReadOnly(op string) error {
	return fmt.Errorf("%s: embedding store opened read-only", op)
}

func (s *Store) initSchema() error {
	err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS embeddings (
			node_id TEXT PRIMARY KEY,
			embedding BLOB NOT NULL,
			summary_hash TEXT NOT NULL,
			model TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			status TEXT NOT NULL DEFAULT 'active'
		);
	`)
	if err != nil {
		return fmt.Errorf("create embeddings table: %w", err)
	}
	// Migration for existing databases that may not have the status column.
	_ = s.db.Exec(`ALTER TABLE embeddings ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`)
	// Ignore error — column may already exist.
	return nil
}

// serializeFloat32 converts a float32 slice to little-endian bytes for BLOB storage.
func serializeFloat32(v []float32) ([]byte, error) {
	buf := new(bytes.Buffer)
	buf.Grow(len(v) * 4)
	err := binary.Write(buf, binary.LittleEndian, v)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// deserializeFloat32 converts little-endian bytes back to a float32 slice.
func deserializeFloat32(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("data length %d is not a multiple of 4", len(data))
	}
	result := make([]float32, len(data)/4)
	for i := range result {
		bits := binary.LittleEndian.Uint32(data[i*4 : (i+1)*4])
		result[i] = math.Float32frombits(bits)
	}
	return result, nil
}

// StoredDimension returns the dimension of existing embeddings in the store.
// Returns 0 if the store is empty.
func (s *Store) StoredDimension() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storedDimensionLocked()
}

// storedDimensionLocked returns the dimension of existing embeddings (caller must hold mu).
// As the first db touch on every read/write path it also carries the
// closed-store guard for its callers (Upsert/Search/SearchActive/FindSimilar).
func (s *Store) storedDimensionLocked() (int, error) {
	if s.closed {
		return 0, ErrStoreClosed
	}
	stmt, _, err := s.db.Prepare(`SELECT embedding FROM embeddings LIMIT 1`)
	if err != nil {
		return 0, fmt.Errorf("prepare stored dimension: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	if !stmt.Step() {
		return 0, nil // empty store
	}
	blob := stmt.ColumnRawBlob(0)
	if len(blob)%4 != 0 {
		return 0, fmt.Errorf("stored embedding has invalid size %d", len(blob))
	}
	return len(blob) / 4, nil
}

// Upsert inserts or updates the embedding for a node.
func (s *Store) Upsert(nodeID string, embedding []float32, summaryHash string, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.readOnly {
		return errReadOnly("upsert")
	}

	if len(embedding) == 0 {
		return fmt.Errorf("embedding must not be empty")
	}

	// Validate dimension consistency: all embeddings in the store must share the same dimension.
	storedDim, err := s.storedDimensionLocked()
	if err != nil {
		return fmt.Errorf("check stored dimension: %w", err)
	}
	if storedDim > 0 && len(embedding) != storedDim {
		return fmt.Errorf("embedding dimension mismatch: got %d, want %d (matching existing embeddings)", len(embedding), storedDim)
	}

	blob, err := serializeFloat32(embedding)
	if err != nil {
		return fmt.Errorf("serialize embedding: %w", err)
	}

	stmt, _, err := s.db.Prepare(`
		INSERT INTO embeddings (node_id, embedding, summary_hash, model, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(node_id) DO UPDATE SET
			embedding = excluded.embedding,
			summary_hash = excluded.summary_hash,
			model = excluded.model,
			updated_at = CURRENT_TIMESTAMP
	`)
	if err != nil {
		return fmt.Errorf("prepare upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	if err := stmt.BindText(1, nodeID); err != nil {
		return fmt.Errorf("bind node_id: %w", err)
	}
	if err := stmt.BindBlob(2, blob); err != nil {
		return fmt.Errorf("bind embedding: %w", err)
	}
	if err := stmt.BindText(3, summaryHash); err != nil {
		return fmt.Errorf("bind summary_hash: %w", err)
	}
	if err := stmt.BindText(4, model); err != nil {
		return fmt.Errorf("bind model: %w", err)
	}

	if err := stmt.Exec(); err != nil {
		return fmt.Errorf("exec upsert: %w", err)
	}

	return nil
}

// Search performs a KNN search and returns the top-K most similar nodes.
// It rejects queries if the given model does not match the model used for stored embeddings.
func (s *Store) Search(queryEmbedding []float32, topK int, model string) ([]ScoredResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate query dimension matches stored embeddings.
	storedDim, err := s.storedDimensionLocked()
	if err != nil {
		return nil, fmt.Errorf("check stored dimension: %w", err)
	}
	if storedDim > 0 && len(queryEmbedding) != storedDim {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, want %d", len(queryEmbedding), storedDim)
	}

	// Check that there are embeddings and they use the same model.
	modelOK, err := s.checkModel(model)
	if err != nil {
		return nil, err
	}
	if !modelOK {
		return nil, fmt.Errorf("model mismatch: query model %q does not match stored embeddings", model)
	}

	// Scan all embeddings and compute distances in Go.
	stmt, _, err := s.db.Prepare(`SELECT node_id, embedding FROM embeddings`)
	if err != nil {
		return nil, fmt.Errorf("prepare search scan: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	// Use a min-heap (by distance) to collect all results, then extract top-K.
	var candidates scoredHeap
	for stmt.Step() {
		nodeID := stmt.ColumnText(0)
		blob := stmt.ColumnRawBlob(1)

		stored, err := deserializeFloat32(blob)
		if err != nil {
			continue // skip malformed embeddings
		}

		dist := l2Distance(queryEmbedding, stored)
		// Convert L2 distance to a similarity score: 1 / (1 + distance).
		similarity := 1.0 / (1.0 + dist)

		candidates = append(candidates, ScoredResult{
			NodeID: nodeID,
			Score:  similarity,
		})
	}
	if err := stmt.Err(); err != nil {
		return nil, fmt.Errorf("search scan: %w", err)
	}

	// Sort by score descending (highest similarity first).
	heap.Init(&candidates)

	results := make([]ScoredResult, 0, min(topK, len(candidates)))
	for len(results) < topK && len(candidates) > 0 {
		results = append(results, heap.Pop(&candidates).(ScoredResult))
	}

	return results, nil
}

// checkModel verifies that stored embeddings use the specified model.
// Returns true if the store is empty (no model conflict possible) or all
// stored embeddings use the given model.
func (s *Store) checkModel(model string) (bool, error) {
	stmt, _, err := s.db.Prepare(`SELECT COUNT(*) FROM embeddings`)
	if err != nil {
		return false, fmt.Errorf("prepare count: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	if !stmt.Step() {
		return true, nil
	}
	count := stmt.ColumnInt(0)
	if count == 0 {
		return true, nil
	}

	mismatchStmt, _, err := s.db.Prepare(`SELECT COUNT(*) FROM embeddings WHERE model != ?`)
	if err != nil {
		return false, fmt.Errorf("prepare model check: %w", err)
	}
	defer func() { _ = mismatchStmt.Close() }()

	if err := mismatchStmt.BindText(1, model); err != nil {
		return false, fmt.Errorf("bind model: %w", err)
	}
	if !mismatchStmt.Step() {
		return true, nil
	}

	mismatches := mismatchStmt.ColumnInt(0)
	return mismatches == 0, nil
}

// UpdateStatus updates the status of a node's embedding record.
// This is used when a node is soft-deleted or superseded.
func (s *Store) UpdateStatus(nodeID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.readOnly {
		return errReadOnly("update status")
	}
	if s.closed {
		return ErrStoreClosed
	}

	stmt, _, err := s.db.Prepare(`UPDATE embeddings SET status = ? WHERE node_id = ?`)
	if err != nil {
		return fmt.Errorf("prepare update status: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	if err := stmt.BindText(1, status); err != nil {
		return fmt.Errorf("bind status: %w", err)
	}
	if err := stmt.BindText(2, nodeID); err != nil {
		return fmt.Errorf("bind node_id: %w", err)
	}
	if err := stmt.Exec(); err != nil {
		return fmt.Errorf("exec update status: %w", err)
	}
	return nil
}

// SearchActive searches only active (non-superseded) embeddings.
// This is the default search used by context_query.
func (s *Store) SearchActive(queryEmbedding []float32, topK int, model string) ([]ScoredResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate query dimension matches stored embeddings.
	storedDim, err := s.storedDimensionLocked()
	if err != nil {
		return nil, fmt.Errorf("check stored dimension: %w", err)
	}
	if storedDim > 0 && len(queryEmbedding) != storedDim {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, want %d", len(queryEmbedding), storedDim)
	}

	// Check that there are embeddings and they use the same model.
	modelOK, err := s.checkModel(model)
	if err != nil {
		return nil, err
	}
	if !modelOK {
		return nil, fmt.Errorf("model mismatch: query model %q does not match stored embeddings", model)
	}

	// Scan all embeddings, filtering to active status only.
	stmt, _, err := s.db.Prepare(`SELECT node_id, embedding, status FROM embeddings WHERE model = ?`)
	if err != nil {
		return nil, fmt.Errorf("prepare search scan: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	if err := stmt.BindText(1, model); err != nil {
		return nil, fmt.Errorf("bind model: %w", err)
	}

	var candidates scoredHeap
	for stmt.Step() {
		nodeID := stmt.ColumnText(0)
		blob := stmt.ColumnRawBlob(1)
		status := stmt.ColumnText(2)

		// Skip non-active nodes.
		if status != "" && status != "active" {
			continue
		}

		stored, err := deserializeFloat32(blob)
		if err != nil {
			continue // skip malformed embeddings
		}

		dist := l2Distance(queryEmbedding, stored)
		// Convert L2 distance to a similarity score: 1 / (1 + distance).
		similarity := 1.0 / (1.0 + dist)

		candidates = append(candidates, ScoredResult{
			NodeID: nodeID,
			Score:  similarity,
		})
	}
	if err := stmt.Err(); err != nil {
		return nil, fmt.Errorf("search scan: %w", err)
	}

	// Sort by score descending (highest similarity first).
	heap.Init(&candidates)

	results := make([]ScoredResult, 0, min(topK, len(candidates)))
	for len(results) < topK && len(candidates) > 0 {
		results = append(results, heap.Pop(&candidates).(ScoredResult))
	}

	return results, nil
}

// FindSimilar returns all active nodes whose embedding similarity score is >= threshold.
// Unlike Search (which returns a fixed top-K), FindSimilar returns every match above
// the threshold. This is used by the CRUD classifier to find potential UPDATE/SUPERSEDE candidates.
// Only active nodes are considered (status == "" or "active").
func (s *Store) FindSimilar(queryEmbedding []float32, threshold float64, model string) ([]ScoredResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate query dimension matches stored embeddings.
	storedDim, err := s.storedDimensionLocked()
	if err != nil {
		return nil, fmt.Errorf("check stored dimension: %w", err)
	}
	if storedDim == 0 {
		// Empty store — no candidates possible.
		return nil, nil
	}
	if len(queryEmbedding) != storedDim {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, want %d", len(queryEmbedding), storedDim)
	}

	// Check that stored embeddings use the same model.
	modelOK, err := s.checkModel(model)
	if err != nil {
		return nil, err
	}
	if !modelOK {
		return nil, fmt.Errorf("model mismatch: query model %q does not match stored embeddings", model)
	}

	// Scan all embeddings for the given model, filtering to active status only.
	stmt, _, err := s.db.Prepare(`SELECT node_id, embedding, status FROM embeddings WHERE model = ?`)
	if err != nil {
		return nil, fmt.Errorf("prepare find similar scan: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	if err := stmt.BindText(1, model); err != nil {
		return nil, fmt.Errorf("bind model: %w", err)
	}

	var results []ScoredResult
	for stmt.Step() {
		nodeID := stmt.ColumnText(0)
		blob := stmt.ColumnRawBlob(1)
		status := stmt.ColumnText(2)

		// Skip non-active nodes.
		if status != "" && status != "active" {
			continue
		}

		stored, err := deserializeFloat32(blob)
		if err != nil {
			continue // skip malformed embeddings
		}

		dist := l2Distance(queryEmbedding, stored)
		// Convert L2 distance to a similarity score: 1 / (1 + distance).
		similarity := 1.0 / (1.0 + dist)

		if similarity >= threshold {
			results = append(results, ScoredResult{
				NodeID: nodeID,
				Score:  similarity,
			})
		}
	}
	if err := stmt.Err(); err != nil {
		return nil, fmt.Errorf("find similar scan: %w", err)
	}

	// Sort by score descending (highest similarity first).
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Cap to avoid sending huge candidate lists.
	const maxResults = 10
	if len(results) > maxResults {
		results = results[:maxResults]
	}

	return results, nil
}

// Delete removes a node's embedding from the store.
func (s *Store) Delete(nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.readOnly {
		return errReadOnly("delete")
	}
	if s.closed {
		return ErrStoreClosed
	}

	stmt, _, err := s.db.Prepare(`DELETE FROM embeddings WHERE node_id = ?`)
	if err != nil {
		return fmt.Errorf("prepare delete: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	if err := stmt.BindText(1, nodeID); err != nil {
		return fmt.Errorf("bind node_id: %w", err)
	}
	if err := stmt.Exec(); err != nil {
		return fmt.Errorf("exec delete: %w", err)
	}

	return nil
}

// StaleCheck returns true if the stored summary hash for the node differs from currentHash.
// Returns true (stale) if the node is not found in the store.
func (s *Store) StaleCheck(nodeID string, currentHash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false, ErrStoreClosed
	}

	stmt, _, err := s.db.Prepare(`SELECT summary_hash FROM embeddings WHERE node_id = ?`)
	if err != nil {
		return false, fmt.Errorf("prepare stale check: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	if err := stmt.BindText(1, nodeID); err != nil {
		return false, fmt.Errorf("bind node_id: %w", err)
	}
	if !stmt.Step() {
		return true, nil
	}

	storedHash := stmt.ColumnText(0)
	return storedHash != currentHash, nil
}

// Count returns the number of embeddings in the store.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0
	}

	stmt, _, err := s.db.Prepare(`SELECT COUNT(*) FROM embeddings`)
	if err != nil {
		return 0
	}
	defer func() { _ = stmt.Close() }()

	if !stmt.Step() {
		return 0
	}
	return stmt.ColumnInt(0)
}

// Models returns the distinct embedding model names stored in the database,
// sorted. It is read-only-safe (a plain SELECT), so warren doctor can call it
// on remote vault DBs opened via NewStoreReadOnly to detect cross-project
// model skew: SearchActive filters WHERE model = ?, so projects indexed with
// different models silently return no cross-project results.
func (s *Store) Models() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrStoreClosed
	}
	stmt, _, err := s.db.Prepare(`SELECT DISTINCT model FROM embeddings ORDER BY model`)
	if err != nil {
		return nil, fmt.Errorf("prepare models: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	var models []string
	for stmt.Step() {
		models = append(models, stmt.ColumnText(0))
	}
	if err := stmt.Err(); err != nil {
		return nil, fmt.Errorf("scan models: %w", err)
	}
	return models, nil
}

// HasStatusColumn reports whether the embeddings table carries the status
// column added by the soft-delete migration. Read-only-safe (PRAGMA
// table_info never writes): a pre-migration DB opened read-only cannot be
// migrated in place and fails SearchActive, so warren doctor uses this to
// tell the owner to re-import instead of leaving the failure silent.
func (s *Store) HasStatusColumn() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, ErrStoreClosed
	}
	stmt, _, err := s.db.Prepare(`PRAGMA table_info(embeddings)`)
	if err != nil {
		return false, fmt.Errorf("prepare table_info: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for stmt.Step() {
		if stmt.ColumnText(1) == "status" {
			return true, nil
		}
	}
	if err := stmt.Err(); err != nil {
		return false, fmt.Errorf("scan table_info: %w", err)
	}
	return false, nil
}

// Checkpoint flushes the WAL into the main database file
// (PRAGMA wal_checkpoint(TRUNCATE)) so a byte-level copy of embeddings.db
// alone — with the -wal/-shm sidecars excluded — is complete and consistent.
// A checkpoint is a write-side operation, so it errors on read-only stores
// (callers never checkpoint an RO store). Under a persistent concurrent
// reader the checkpoint retries for the 5s busy timeout, then returns
// SQLITE_BUSY.
func (s *Store) Checkpoint() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return errReadOnly("checkpoint")
	}
	if s.closed {
		return ErrStoreClosed
	}
	return s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
}

// Close closes the underlying SQLite connection. It is idempotent, and any
// method called after Close returns ErrStoreClosed (an in-flight call that
// already holds the mutex finishes against the open connection first).
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// l2Distance computes L2 (Euclidean) distance between two vectors.
func l2Distance(a, b []float32) float64 {
	if len(a) != len(b) {
		return math.Inf(1)
	}
	var sum float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		sum += d * d
	}
	return math.Sqrt(sum)
}

// cosineSimilarity computes cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// SerializeFloat32 converts a float32 slice to little-endian bytes.
func SerializeFloat32(v []float32) ([]byte, error) {
	return serializeFloat32(v)
}

// DeserializeFloat32 converts little-endian bytes back to a float32 slice.
func DeserializeFloat32(data []byte) ([]float32, error) {
	return deserializeFloat32(data)
}

// scoredHeap implements a max-heap by Score for extracting top-K results.
type scoredHeap []ScoredResult

func (h scoredHeap) Len() int           { return len(h) }
func (h scoredHeap) Less(i, j int) bool { return h[i].Score > h[j].Score } // max-heap
func (h scoredHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *scoredHeap) Push(x any) {
	*h = append(*h, x.(ScoredResult))
}

func (h *scoredHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
