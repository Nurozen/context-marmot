## internal/routes

No SQLite, engine, MCP, stdio, signal, socket, or scheduler code lives here. The package manages the global vault routing table at `~/.marmot/routes.yml`. Two findings are relevant to Workstream 2 (single-owner daemon); nothing here touches Workstream 1 (SQLite/WAL/driver upgrade).

### routes.go:27-29, 129-178
Workstream 2: cross-process coordination here is *only* atomic tmp+rename — the package mutex is explicitly process-local. Concurrent `marmot serve` processes doing `Update()` (read-modify-write of routes.yml) can lose each other's writes (last-writer-wins), same class of bug as the heatmap save. A single-owner daemon would make this a non-issue for serve; other CLI commands (index, etc.) that register vaults would still race unless routed through the owner or given an OS-level file lock. Also note `~/.marmot/` already exists as the global per-user directory — a natural home for the daemon's lock file and unix socket alongside `routes.yml`.

```go
27	// mu protects Load/Save within a single process. Inter-process safety
28	// is handled by atomic writes (tmp + rename).
29	var mu sync.RWMutex
...
129	// Update performs an atomic read-modify-write cycle on the routing table.
130	// The provided function receives the current table and may modify it.
131	// If fn returns nil, the modified table is saved atomically.
132	func Update(fn func(rt *RoutingTable) error) error {
133		mu.Lock()
134		defer mu.Unlock()
...
143		data, err := os.ReadFile(path)
...
173		if err := os.Rename(tmp, path); err != nil {
```

### routes.go:118-125 and 169-176
Workstream 2 (minor): the temp file name is fixed (`path + ".tmp"`), so two *processes* saving simultaneously write the same tmp file; rename keeps the file valid but content interleaving/lost updates are possible. If the daemon becomes the sole writer for serve, remaining writers (CLI commands) should either go through the daemon or use unique tmp names / flock.

```go
118	tmp := path + ".tmp"
119	if err := os.WriteFile(tmp, data, 0o644); err != nil {
120		return fmt.Errorf("write routing table tmp: %w", err)
121	}
122	if err := os.Rename(tmp, path); err != nil {
```

### routes_test.go, routes_stress_test.go (whole files)
Test impact: tests use `SetOverridePath` to redirect away from `~/.marmot/routes.yml` and stress in-process concurrency (20-50 goroutines). Neither workstream changes this package's API, but daemon e2e tests that spawn real `marmot serve` processes should use the same override mechanism (or an env var) so they don't touch the developer's real `~/.marmot` routing table, lock file, or socket path if those are colocated.
