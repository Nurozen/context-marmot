# internal/config

## internal/config

No SQLite usage, engine construction, process/signal/stdio handling, lock files, sockets, or schedulers live here. The package is pure config surface (parse/save `_config.md`, `.marmot-data/.env` keys, embedder factory). Three peripheral findings relevant to the daemon workstream:

### config.go:66-92
Workstream 2 (daemon): `Save` uses a fixed tmp filename (`_config.md.tmp`) + rename. With multiple `marmot serve` processes on the same vault, concurrent saves race on the same tmp path (interleaved WriteFile/Rename → possible torn/lost writes). Under single-owner daemon this becomes moot for serve, but other CLI commands (e.g. `marmot init`/config edits) could still race the owner. Also relevant as the natural home for any new config keys (e.g. daemon socket path, lock behavior).

```go
82	configPath := filepath.Join(vaultDir, "_config.md")
83	tmpPath := configPath + ".tmp"
84	if err := os.WriteFile(tmpPath, []byte(buf.String()), 0o644); err != nil {
85		return fmt.Errorf("write tmp config: %w", err)
86	}
87	if err := os.Rename(tmpPath, configPath); err != nil {
```

### config.go:159-192
Workstream 2 (daemon): `SaveDotEnv` does a non-atomic read-modify-write of `.marmot-data/.env` (no tmp+rename, no lock) — same multi-process last-writer-wins class of bug as the heatmap save. `.marmot-data` is the same directory that holds `embeddings.db`, so any daemon lock file / socket placed there coexists with this code path.

```go
162	envPath := filepath.Join(vaultDir, ".marmot-data", ".env")
...
191	return os.WriteFile(envPath, []byte(buf.String()), 0o600)
```

### embedder.go:13-39
Both workstreams (context): `NewEmbedderFromVault` is called by every engine construction (each `marmot serve` builds its own embedder → its own embedding store → its own SQLite connection). It writes diagnostics to stderr only (safe for stdio MCP transport). Under the single-owner daemon, proxy processes should skip this entirely; note the callers in cmd/mcp when refactoring engine ownership.

```go
13	func NewEmbedderFromVault(cfg *VaultConfig) (embedding.Embedder, error) {
...
33		fmt.Fprintln(os.Stderr, "embedding: using mock embedder (lexical only)")
```

No SQLite opens, go-sqlite3 imports, or driver API usage in this package — workstream 1 (WAL/busy_timeout/driver upgrade) does not touch it. `config_test.go` exercises only parse/save/env logic and is unaffected by either change.
