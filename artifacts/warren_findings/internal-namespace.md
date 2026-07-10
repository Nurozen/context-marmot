# internal/namespace

## internal/namespace

Review line citations for this folder verified accurate at commit 1f14f3e (`registry.go:76-142,186-241` matches). No line drift found. One semantic addition the review understates: `Refresh` (registry.go:145-159) closes the EmbStore in-place *before* reload — the Tier 2.3 "swap-then-close" hazard site is here, not only in cache-TTL logic.

### registry.go:186-241 — ResolveEmbeddingStore (Tier 1.1: read-only remote opens; Tier 2.3)
The exact site that must switch to a future `embedding.NewStoreReadOnly`. Note it also lazily calls `loadVaultLocked` if the graph isn't cached (line 232), so read-only opens must not break that path.

```go
186	func (r *VaultRegistry) ResolveEmbeddingStore(vaultID string) (*embedding.Store, error) {
...
222		dbPath := filepath.Join(vaultDir, ".marmot-data", "embeddings.db")
223		store, err := embedding.NewStore(dbPath)   // <- writes WAL pragma + schema into remote vault
224		if err != nil {
225			return nil, fmt.Errorf("open embedding store for vault %q: %w", vaultID, err)
226		}
...
239		rv.EmbStore = store
240		return store, nil
241	}
```

### registry.go:144-159 — Refresh (Tier 2.3: close-in-place under concurrent search)
Closes the cached EmbStore while a concurrent `context_query` may hold the pointer returned by ResolveEmbeddingStore (returned outside the lock). Fix must swap the `*RemoteVault` entry then close old store after, or refcount.

```go
145	func (r *VaultRegistry) Refresh(vaultID string) error {
146		r.mu.Lock()
147		defer r.mu.Unlock()
148		existing, ok := r.vaults[vaultID]
149		if !ok {
150			return fmt.Errorf("vault %q not loaded", vaultID)
151		}
153		if existing.EmbStore != nil {
154			_ = existing.EmbStore.Close()   // <- close-in-place; concurrent search can use closed DB
155		}
157		_, err := r.loadVaultLocked(vaultID, existing.VaultDir)
158		return err
159	}
```
Also: `Refresh` errors if the vault was never loaded — a "real refresh endpoint" calling it for every known vault must tolerate `not loaded`.

### registry.go:42-63 — NewVaultRegistry signature (Tier 2.1: always-create + Rebuild)
```go
42	func NewVaultRegistry(localVaultID, localDir string, bridges []*Bridge, rt *routes.RoutingTable) *VaultRegistry {
```
State to rebuild for the proposed `Rebuild(mounts, routes)`: `pathToID` (seeded only from `IsCrossVault()` bridges, lines 51-61), `routingTable`, plus flushing `vaults` cache (close EmbStores as in `Close()`, registry.go:244-252). `localVaultID`/`localDir` are immutable and unused after construction except as fields — Rebuild need not touch them.

Sole production constructor call site: `cmd/marmot/pipeline.go:272` inside the gate `hasCrossVaultBridges || hasRoutes` (pipeline.go:265-267) — confirms the review's "nil forever" claim. Other production consumers (must survive an always-non-nil registry): `internal/mcp/engine.go:41,251,291-293,304-307,344-345`; `internal/mcp/handlers.go:75-88` (KnownVaultIDs + ResolveEmbeddingStore, errors swallowed = Tier 1.6); `internal/api/handlers.go:459-460` (Refresh, error dropped), `538,565,589-592`; `internal/traversal/bridged.go:11-13` defines the interface `ResolveGraph(vaultID string) (*graph.Graph, error)` — Rebuild must not change that method signature.

### registry.go:119-142 — loadVaultLocked (Tier 2.3 TTL; Tier 1.6)
`LoadedAt` is set (line 137) but never read anywhere in the repo — confirms review's "never-checked LoadedAt". TTL/mtime check belongs in the ResolveGraph fast path (lines 78-83).

### namespace.go:362-381 — parseBridge (Tier 1.7 frontmatter parser)
Same defective `strings.Index(content[3:], "---")` pattern the review cites in warren.go:1110-1125 also exists here and in `extractEdgesFromFrontmatter` (namespace.go:540-548). The review only cites warren.go; the fix should cover these two sites too (any `---` in a YAML value or body truncates the frontmatter).

```go
363	func parseBridge(data []byte) (*Bridge, error) {
364		content := string(data)
365		if !strings.HasPrefix(content, "---") {
366			return nil, fmt.Errorf("missing YAML frontmatter")
367		}
368		end := strings.Index(content[3:], "---")   // <- matches "---" anywhere, not line-anchored
```
`parseNamespace` (namespace.go:166) uses the same pattern. Bridge parse errors are the pipeline.go:391-415 swallow site (Tier 1.6) — callers: `LoadBridge` (namespace.go:354) from `loadBridges` (namespace.go:124-138).

### namespace.go:601-703 — CreateCrossVaultBridge / writeCrossVaultBridgeTemp (Tier 1.5 flock scope check)
`CreateCrossVaultBridge(localVaultDir, remoteVaultDir string, allowedRelations []string) (*Bridge, error)` writes manifests into *both* vaults' `_bridges/` dirs via temp+rename (writeCrossVaultBridgeTemp, line 678). Write path is atomic-rename, not RMW, so it does NOT need the Tier 1.5 flock; only `_warren.md`/routes.yml do. No change needed here.

### inventory.go:71 — Doctor (Tier 4: doctor model-skew / collision checks)
```go
71	func Doctor(vaultDir string) ([]DoctorIssue, error) {
```
`DoctorIssue` struct at inventory.go top; natural home or template for the planned cross-warren vault-ID-collision and model-skew checks (those likely live in warren/doctor, but this is the existing per-vault doctor pattern to mirror).

### registry_embstore_test.go:11-23 — setupRemoteVault helper (test program: hermetic vault setup)
Reusable minimal-vault fixture: `t.TempDir()` + `_config.md` frontmatter with `vault_id` + one node file. Good template for warren e2e vault fabrication. Note: none of this package's tests set `MARMOT_ROUTES` — safe today because `NewVaultRegistry` takes `rt` explicitly and tests pass nil/explicit tables (no implicit `~/.marmot/routes.yml` read in this package; `routes.Load` is called only by callers). No hermeticity bug in this folder.

### Constraint summary
- `VaultRegistry` methods `ResolveGraph`/`Resolve`/`ResolveEmbeddingStore`/`Refresh`/`KnownVaultIDs`/`Close` are the public surface consumed by mcp, api, traversal; `ResolveGraph` signature frozen by `traversal.VaultResolver` interface (bridged.go:11-13).
- `embedding.NewStore` (internal/embedding/store.go:47) does BusyTimeout → WAL pragma → initSchema; a read-only variant must keep busy_timeout and skip the other two.
- Registry never writes remote vaults today (review's "nothing regresses" claim confirmed: no Upsert/write calls on `rv.EmbStore` in this package).
