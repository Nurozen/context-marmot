# internal/node

## internal/node

Package is pure filesystem markdown-node storage: no SQLite, no engine/process/signal/socket/scheduler code. Only two tangential findings relevant to the daemon workstream's multi-process story.

### store.go:126-174
Relevance: workstream 2 (single-owner daemon). Node persistence is already atomic per-file (temp + rename), so concurrent `SaveNode` from multiple `marmot serve` processes cannot corrupt a file — but it IS last-writer-wins per node, and there is no cross-process invalidation: another process's in-memory graph (loaded once in mcp.NewEngine) never sees these writes. This is part of the stale-graph problem the daemon fixes; no changes needed here, but it means node files are safe to leave as-is once a single owner performs writes.

```go
129	func (s *Store) SaveNode(node *Node) error {
...
146	// Atomic write: create temp file in the same directory, write, then rename.
147	tmp, err := os.CreateTemp(dir, ".node-*.md.tmp")
...
168	if err := os.Rename(tmpPath, target); err != nil {
169		return fmt.Errorf("save node: rename: %w", err)
170	}
```

### store.go:228-244
Relevance: both workstreams, informational. `ListNodes` (the input to graph loading) walks the vault and explicitly skips hidden dirs like `.marmot-data` (where `embeddings.db` lives) and `_`-prefixed system dirs/files (`_heat`, `_summary.md`). So SQLite WAL sidecar files (`embeddings.db-wal`, `embeddings.db-shm`) created by the quick fix, and any lock file / unix socket placed under `.marmot-data` for the daemon, will NOT pollute node listing or graph loads — safe location for both.

```go
231	err := filepath.Walk(s.basePath, func(path string, info os.FileInfo, walkErr error) error {
...
237	// Skip hidden dirs (.obsidian, .marmot-data) and system dirs (_bridges, _heat).
238	if path != s.basePath && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
239		return filepath.SkipDir
240	}
...
244	if strings.HasPrefix(info.Name(), "_") {
```

No SQLite usage, engine construction, background goroutines, lock files, sockets, MCP/CLI wiring, or affected tests in this package (parser.go, writer.go, types.go, node_test.go, temporal_test.go are pure in-memory/tempdir markdown parsing and rendering).
