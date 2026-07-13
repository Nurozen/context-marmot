# internal/daemon findings

## internal/daemon

Mostly infrastructure (daemon lock, socket, MCP proxy). Warren relevance is limited to one code path: the owner's fsnotify graph watcher, which is the daemon-side trigger for `Engine.ReloadWarrenState` — the exact function R1 will change (skipping `rt.Set` for self-mount aliases). No CLI subcommands, flags, or web UI live here.

### owner.go:257-261 — warren state file watch (R1/R2)

The watcher treats the workspace `_warren.md` (inside the watched `.marmot` dir) as the cross-process signal that warren wiring must reload:

```go
257	// The workspace warren mount state lives directly inside the watched
258	// root; every warren CLI mutation rewrites it atomically (and `marmot
259	// warren refresh` touches it), so this one file is the cross-process
260	// signal that warren wiring — not just the graph — must reload.
261	warrenStatePath := filepath.Join(dir, "_warren.md")
```

R2 note: if first-class local identity is recorded in workspace state via a different file than `_warren.md`, this watch will not fire; if it stays inside `_warren.md`, no daemon change is needed. R1's alias behavior lives entirely in `Engine.ReloadWarrenState` (internal/mcp/warren_reload.go) — the daemon just calls it.

### owner.go:307-313, 330-337 — reload trigger and call site (R1)

```go
309				if event.Name == warrenStatePath {
310					warrenPending = true
311					schedule()
312					continue
313				}
...
330				if warrenPending {
331					warrenPending = false
332					if err := eng.ReloadWarrenState(); err != nil {
333						fmt.Fprintf(os.Stderr, "daemon: warren state reload failed: %v\n", err)
334					} else {
335						fmt.Fprintln(os.Stderr, "daemon: warren state reloaded")
336					}
337				}
```

Debounce is 1s (owner.go:265). Failures only log to stderr — a self-mount that ReloadWarrenState starts refusing under R1 would be silently swallowed here (UX: no surfaced diagnostic beyond daemon stderr; doctor is the only user-visible path).

### warren_watch_test.go:21-110 — test pinning current behavior (R1: will need updating/extending)

`TestGraphWatcherReloadsWarrenState` builds a workspace with `vault_id: local-vault` and mounts a warren project with a DIFFERENT vault_id (`proj-a-vault`); asserts `eng.VaultRegistry.KnownVaultIDs()` gains `proj-a-vault` and evicts a seeded stale route. It does NOT cover the self-mount case (warren copy with vault_id == LocalVaultID), so it will not break under R1 — but R1 should add a sibling test here: mount a warren project whose vault_id equals `local-vault` and assert the registry does NOT gain a route pointing at the warren copy (alias resolves to live vault path). Key existing assertions:

```go
93	if _, err := warren.Mount(workspace, "wp", []string{"proj-a"}, false); err != nil {
...
102		if k["proj-a-vault"] && !k["stale-vault"] {
103			return // reloaded: mount visible, stale route swapped out
```

### UX PASS — no user-facing surface

No subcommands, flags, help text, or API endpoints in this folder. User-visible strings are daemon stderr logs only (`daemon: warren state reloaded`, `daemon: warren state reload failed: %v`, `daemon: graph reloaded (%d nodes)`) plus proxy/lock errors (lock.go:20-39 `ErrHeld`, `ErrOwnerGone`, `ErrNoResume`, `ErrUnsupported`). None are warren-UX candidates beyond the silent-reload-failure note above.

### Context-staleness check

Nothing in the PROJECT CONTEXT is out of date w.r.t. this folder; the B3.3 watcher behavior described in the warren resolution landed as stated.
