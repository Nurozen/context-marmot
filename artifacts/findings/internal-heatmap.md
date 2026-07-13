## internal/heatmap

No SQLite usage anywhere in this package (pure in-memory struct + YAML-frontmatter markdown file). Nothing here changes for the WAL/driver-upgrade quick fix. All findings concern Workstream 2 (single-owner daemon), where heatmap persistence is the known "last-writer-wins on exit" hazard.

### heatmap.go:200-231
Workstream 2: `Load` reads the whole heat map into memory once. Each `marmot serve` process that calls this (via engine construction) gets its own independent in-memory copy with no cross-process refresh — this is the mechanism behind the stale/last-writer-wins behavior. Under the daemon design only the owner should Load.

```go
200	// Load reads a heat map file from _heat/<namespace>.md under the given vault dir.
201	func Load(vaultDir, namespace string) (*HeatMap, error) {
202		path := FilePath(vaultDir, namespace)
203		data, err := os.ReadFile(path)
204		if err != nil {
205			if os.IsNotExist(err) {
206				return New(namespace), nil
207			}
208			return nil, fmt.Errorf("load heatmap: %w", err)
209		}
210		return parse(data, namespace)
211	}
```

### heatmap.go:233-281
Workstream 2: `Save` is atomic per-write (temp file + rename, lines 271-279) so a single writer never corrupts the file, but there is no cross-process coordination — two serve processes each saving their own copy on exit silently clobber each other (last rename wins). The `h.mu` lock at 236-237 is a `sync.Mutex`, in-process only. Under single-owner daemon this becomes safe with no code change here; alternatively a read-merge-write would be needed if multi-process persists.

```go
233	// Save writes the heat map to _heat/<namespace>.md under the given vault dir.
234	// The write is atomic (temp file + rename).
235	func Save(vaultDir string, h *HeatMap) error {
236		h.mu.Lock()
237		defer h.mu.Unlock()
...
271		path := FilePath(vaultDir, h.Namespace)
272		tmpPath := path + ".tmp"
273		if err := os.WriteFile(tmpPath, []byte(buf.String()), 0o644); err != nil {
274			return fmt.Errorf("write tmp heatmap: %w", err)
275		}
276		if err := os.Rename(tmpPath, path); err != nil {
277			_ = os.Remove(tmpPath)
278			return fmt.Errorf("rename heatmap: %w", err)
279		}
```

### heatmap.go:44-56
Workstream 2: all mutation (`RecordCoAccess`, `Decay`) is guarded only by the in-process `sync.Mutex` at line 55 — concurrency safety assumes a single process owning the map, which is exactly the daemon model's invariant. Also note `Decay` (lines 157-165) is invoked by the summary scheduler; duplicate schedulers in multiple processes double-decay their own copies before racing on Save.

```go
45	type HeatMap struct {
...
53		// Internal index for fast lookups (not serialized).
54		index map[string]int // PairKey -> index into Pairs
55		mu    sync.Mutex
56	}
```

### heatmap_test.go:175-220
Test impact: `TestSaveAndLoad` / `TestLoadNonexistent` exercise single-process file round-trips only; no multi-process/concurrent-Save test exists. The daemon workstream should add (or relocate) tests for concurrent Save/last-writer behavior; these existing tests use `t.TempDir()` and are unaffected by either change.

```go
175	func TestSaveAndLoad(t *testing.T) {
176		dir := t.TempDir()
177		h := New("test-ns")
178		h.RecordCoAccess([]string{"auth/login", "auth/validate", "db/users"}, 0.1)
179
180		if err := Save(dir, h); err != nil {
```
