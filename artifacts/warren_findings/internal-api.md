# internal/api findings (commit 1f14f3e, branch multiprocess-lock-fix)

## internal/api

### api.go:56-77 (route registration) — Tier 2.2a, Tier 3
All warren HTTP surface is registered here; the refresh endpoint already exists and only needs a real implementation behind it. Server holds `engine *mcpserver.Engine` (api.go:19) — a `reloadWarrenState(engine)` helper is directly callable from handlers.

```go
66	s.mux.HandleFunc("GET /api/warrens", s.handleWarrens)
67	s.mux.HandleFunc("GET /api/warren/{id}", s.handleWarrenStatus)
68	s.mux.HandleFunc("GET /api/warren/{id}/graph", s.handleWarrenGraph)
69	s.mux.HandleFunc("GET /api/warren/{id}/status", s.handleWarrenStatus)
70	s.mux.HandleFunc("POST /api/warren/{id}/refresh", s.handleWarrenRefresh)
```

### handlers.go:912-932 (handleWarrenRefresh) — Tier 2.2a (refresh stub)
Confirmed printf stub. It validates the warren exists via `warren.LoadWorkspaceStateFromMarmot(s.engine.MarmotDir)` then returns a static JSON body; never touches `VaultRegistry`. Review's cite "handlers.go:912-931" is correct (function ends at 932).

```go
912	// handleWarrenRefresh is a no-op refresh hook for git-backed Warren state.
913	func (s *Server) handleWarrenRefresh(w http.ResponseWriter, r *http.Request) {
...
928	writeJSON(w, http.StatusOK, map[string]string{
929		"warren_id": id,
930		"status":    "git-backed Warren state is read from disk",
931	})
```
Existing test `TestWarrenListStatusRefreshSuccess` (api_more_test.go:386-433) asserts the stub's 200 + `warren_id` echo — a real refresh must keep those assertions or update them (it checks `refreshResp["warren_id"]`, so changing the response shape breaks the test).

### handlers.go:396-468 (handleWarrenNodeUpdate) — Tier 1.1, 1.4, 1.6
Signature: `func (s *Server) handleWarrenNodeUpdate(w http.ResponseWriter, id string, req NodeUpdateRequest)`. Dispatched from `handleNodeUpdate` at handlers.go:319-322 (`strings.HasPrefix(id, "@")`). Writes node + embeddings to `mount.Path` (burrow cache when materialized → Tier 1.4 write loss). This is the API side of the MCP/API @-write asymmetry (Tier 3): HTTP allows editable @-writes, MCP rejects them.

Tier 1.6 swallowed errors (review cited :449-454; actual :448-456, one-line drift, harmless):
```go
448	vec, err := s.engine.Embedder.Embed(embedText)
449	if err == nil {
450		embStore, storeErr := embedding.NewStore(filepath.Join(mount.Path, ".marmot-data", "embeddings.db"))
451		if storeErr == nil {
452			h := sha256.Sum256([]byte(embedText))
453			_ = embStore.Upsert(diskNode.ID, vec, hex.EncodeToString(h[:]), s.engine.Embedder.Model())
454			_ = embStore.Close()
455		}
456	}
457	}
458	}
459	if s.engine.VaultRegistry != nil {
460		_ = s.engine.VaultRegistry.Refresh(vaultID)
461	}
```
Note for Tier 1.1: this `embedding.NewStore` at :450 is a legitimate WRITE open (editable mount) — do NOT convert it to `NewStoreReadOnly`; only registry reads change. Also `_ = s.engine.VaultRegistry.Refresh(vaultID)` (:460) is a close-in-place refresh call — Tier 2.3 swap-then-close semantics must keep this call safe.

### handlers.go:946-957 (findWarrenMountByVault) — Tier 1.4, Tier 3 vault-ID collision
Signature: `func (s *Server) findWarrenMountByVault(vaultID string) (warren.ProjectStatus, bool)`. First-match over `warren.ActiveMounts(s.engine.MarmotDir)` (error swallowed → false at :948-950). Call sites in this package: handlers.go:402 (node update, chooses write path — Tier 1.4 fix point "prefer checkout for editable") and handlers.go:596 (provenance in resolveSearchNode).

```go
946	func (s *Server) findWarrenMountByVault(vaultID string) (warren.ProjectStatus, bool) {
947		mounts, err := warren.ActiveMounts(s.engine.MarmotDir)
948		if err != nil {
949			return warren.ProjectStatus{}, false
950		}
951		for _, mount := range mounts {
952			if mount.VaultID == vaultID {
953				return mount, true
954			}
```

### handlers.go:537-581 (searchMountedVaults) — Tier 1.1, 2.1, 2.3
Calls `s.engine.VaultRegistry.ResolveEmbeddingStore(vaultID)` (:565) — the read-only-open (Tier 1.1) call site in this package. Nil-registry gate at :538-540 means API cross-vault search silently returns nothing when registry wasn't created at startup (Tier 2.1 always-create fixes this here for free). Errors from ResolveEmbeddingStore and SearchActive are `continue`-swallowed (:566-568, :570-572 — Tier 1.6). Semantic note the review glosses over: this function ONLY searches remote vaults when `ns` starts with `_warren/` (:541-544 early-return otherwise) — plain `/api/search` never hits remote stores; scope any freshness/e2e test accordingly.

```go
545	mounts, _ := warren.ActiveMounts(s.engine.MarmotDir)   // error swallowed
565	remoteStore, err := s.engine.VaultRegistry.ResolveEmbeddingStore(vaultID)
566	if err != nil { continue }
569	remoteResults, err := remoteStore.SearchActive(vec, limit, s.engine.Embedder.Model())
```
Also :569 passes the LOCAL embedder's model to remote SearchActive — the Tier 4.5 model-skew silent-empty behavior lives here for the API path.

### handlers.go:583-609 (resolveSearchNode) — Tier 2 freshness
`s.engine.VaultRegistry.Resolve(vaultID, nodeID)` (:592) serves nodes from the registry's forever-cached remote graph, while handleWarrenGraph (below) reads disk fresh — the exact "two views in one process" split, both inside this file.

### handlers.go:785-910 (handleWarrens/handleWarrenStatus/handleWarrenGraph) — Tier 2 (disk-fresh side), Tier 3 unreachable surfacing
All three re-read disk per request: `warren.LoadWorkspaceStateFromMarmot(s.engine.MarmotDir)` (:787, :832, :919), `warren.LoadWorkspaceState(workspaceRoot)` with `workspaceRoot := filepath.Dir(s.engine.MarmotDir)` (:802-803), `warren.Status(workspaceRoot, id)` (:813), `warren.ActiveMounts(s.engine.MarmotDir)` (:841). handleWarrenGraph opens each mount fresh via `node.NewStore(mount.Path)` + `graph.LoadGraph` (:858-859) and skips unavailable mounts silently (:855 `!mount.Available` → continue; per-mount LoadGraph errors `continue` at :860-861 — another Tier 1.6/Tier 3 silent-unreachable spot). API-side constraint: these handlers are why Tier 2 must not regress "UI is disk-fresh"; making them registry-backed would inherit staleness.

### handlers.go:934-944 (splitQualifiedVaultID) — reference
`func splitQualifiedVaultID(id string) (vaultID, nodeID string, ok bool)` — package-private; MCP has its own copy; Tier 3 MCP/API alignment could share it.

### watcher.go:14-16 — Tier 2.2c constraint
`func (s *Server) StartWatcher(vaultDir string) (stop func(), err error)` delegates to `daemon.StartGraphWatcherNotify(vaultDir, s.engine, s.NotifyChange)` — the API server uses the SAME daemon graph watcher whose skip-`_`-files rule Tier 2 changes; un-skipping `_warren.md` in internal/daemon automatically flows to the API serve path, and `s.NotifyChange` gives free SSE UI refresh on warren state change.

### ActiveMounts consumers in this package (for the Tier 2 reload plan)
handlers.go:545 (searchMountedVaults), :841 (handleWarrenGraph), :947 (findWarrenMountByVault), plus test helper api_test.go:193. None cache — all disk-per-call.

### Test templates worth reusing — test program
- `setupAPIWarren` (api_test.go:135-171): full hermetic warren fixture — temp warrenRoot, `_config.md` with vault_id, `SaveProjectMetadata`, `SaveManifest`, `RegisterWorkspaceWarren`, `Mount`. Best existing template for warren e2e/unit fixtures anywhere in the repo.
- `wireWarrenVaultRegistry` (api_test.go:191-202): manual registry wiring via `namespace.NewVaultRegistry("", engine.MarmotDir, nil, rt)` + `engine.WithVaultRegistry` — Tier 2.1 (always-create + Rebuild) should obsolete this helper; update it rather than leaving a second wiring path.
- `seedRemoteEmbedding` (api_test.go:173-189): real-SQLite remote embeddings.db seeding (useful for the Tier 1.1 read-only regression test: seed, open via registry, assert no `-wal` sidecar/journal_mode change).
- `setupTestEngine` (api_test.go:65-104) + `seedEmbeddings` + `doRequest` (api_test.go:206): hermetic engine/HTTP harness. NOTE: these tests never call buildEngine, so the Tier 1.8 MARMOT_ROUTES hermeticity bug does NOT apply here (no `Setenv("MARMOT_ROUTES", ...)` anywhere in internal/api tests, and none needed).

### Review corrections
- 1.6 cite "handlers.go:449-454" → actual swallow block is 448-456; trivial drift.
- 1.4 cite "handlers.go:412-457" → function body writing is 412-458 (registry refresh at 459-461); accurate enough.
- Not stated in review: `searchMountedVaults` only runs for `ns` prefixed `_warren/` — plain API search never touches remote stores; Tier 1.1/2.3 API-path tests must use a `_warren/<id>` ns filter.
- Not stated: `handleWarrenNodeUpdate`'s `embedding.NewStore` (:450) is an intentional write open; the read-only-store fix must not touch it.
- No `stat/refresh` line drift elsewhere; handlers.go:912-931 cite verified correct.
