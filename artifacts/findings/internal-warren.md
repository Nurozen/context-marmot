# internal/warren

## internal/warren

### warren.go:1317-1323
Workstream 1 (WAL quick fix): `ImportProject` already excludes SQLite WAL sidecar files when copying a vault into a Warren, so enabling `journal_mode(WAL)` will not break import. HOWEVER, copying a live `embeddings.db` without its `-wal` file while WAL mode is active can produce a stale/torn snapshot (uncheckpointed pages live only in the `-wal`). After the quick fix, import of an actively-used vault should checkpoint (or open the DB) before copying. Also note the copy reads the db file with plain `io.Copy` (no sqlite lock coordination) via `copyRegularFile` (lines 1393-1414).

```go
1317	var importAlwaysExcluded = map[string]bool{
1318		".marmot-data/.env":               true,
1319		".marmot-data/embeddings.db-wal":  true,
1320		".marmot-data/embeddings.db-shm":  true,
1321		".obsidian/workspace.json":        true,
1322		".obsidian/workspace-mobile.json": true,
1323	}
```

### warren.go:576-584
Workstream 1/2: `Doctor` stats `<project>/.marmot-data/embeddings.db` directly to warn when a project has no embedding database. Purely a filesystem stat — no sqlite open — so unaffected by driver upgrade, but this is the same DB path the shared-store/daemon work centers on; keep the path in sync if it moves.

```go
576			if _, err := os.Stat(filepath.Join(projectPath, ".marmot-data", "embeddings.db")); err != nil {
577				report.Issues = append(report.Issues, DoctorIssue{
578					Severity:  "warning",
579					Code:      "embeddings_missing",
```

### warren.go:1065-1077 and 1127-1166
Workstream 2 (daemon): workspace state `.marmot/_warren.md` is mutated via read-modify-write (`updateWorkspaceState`) with atomic temp-file rename (`writeMarkdownYAML`) but NO inter-process lock — two concurrent processes (e.g. daemon owner plus a CLI `warren mount`) can lose updates (last-writer-wins), same class of bug as the heatmap save. If the daemon owns the vault, warren workspace-state writes from other CLI invocations should be routed through it or file-locked.

```go
1065	func updateWorkspaceState(workspaceRoot string, fn func(*WorkspaceState) error) (*WorkspaceState, error) {
1066		state, body, err := LoadWorkspaceState(workspaceRoot)
...
1073		if err := SaveWorkspaceState(workspaceRoot, state, body); err != nil {
```

### warren.go:920-970
Workstream 2: `ActiveMounts(marmotDir)` resolves the set of mounted Warren project vaults for a `.marmot` dir — this is read at engine construction time (multi-vault engine). A single-owner daemon must decide whether mount changes made after startup are picked up (currently each process reads this once; contributes to the stale-in-memory-state problem alongside the graph).

```go
920	// ActiveMounts returns active Warren project vaults for a local .marmot dir.
921	func ActiveMounts(marmotDir string) ([]ProjectStatus, error) {
```

### warren_test.go:216-220, 746-749
Test impact: import tests assert `embeddings.db` is copied and `-wal`/`-shm` are excluded. These pass regardless of the WAL quick fix (they use fake file contents, not real sqlite), but must be kept in mind if import gains a checkpoint step or the exclusion list changes.

```go
216	mustExist(t, filepath.Join(dest, ".marmot-data", "embeddings.db"))
219	mustNotExist(t, filepath.Join(dest, ".marmot-data", "embeddings.db-wal"))
220	mustNotExist(t, filepath.Join(dest, ".marmot-data", "embeddings.db-shm"))
```

No SQLite opens, unix sockets, lock files, signal handling, schedulers, or MCP transport code in this package — it is pure YAML/manifest/file-copy logic.
