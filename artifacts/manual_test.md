# Manual test findings

## CLI & daemon surface sweep (agent 1)

Environment: branch `multiprocess-lock-fix`, binary `bin/marmot` (`marmot v0.1.10-dirty`, commit 6ec0f61). Scratch project: `/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad/manualtest/proj` (5 source files: 4 Go incl. one added mid-test, 2 TS, go.mod). Vault: `<proj>/.marmot`, mock embeddings, classifier `none`.

### 1. init / configure / setup — PASS with notes

- `printf '\n\n' | marmot init` — created vault, defaulted to mock embeddings + `none` classifier, wrote `.marmot/_config.md`, and also auto-wrote `.codex/config.toml`. PASS.
- `marmot configure` standalone (default answers) — re-saves mock/none config cleanly. PASS.
- `marmot setup -claude -cursor -vscode -codex` — wrote `.mcp.json`, `.cursor/mcp.json`, `.vscode/mcp.json`, `.codex/config.toml`. PASS.
- NOTE: every generated config uses a **relative** vault path `args: ["serve","--dir",".marmot"]`. Works only if the MCP client launches the server with cwd = project root. Fragile for clients with different cwd semantics.
- NOTE: no generated config sets `MARMOT_DAEMON=1` (expected while dark-launched, but means MCP clients won't exercise the daemon by default).
- For the follow-up Codex tester, `.codex/config.toml` was rewritten to use the absolute vault path plus `[mcp_servers.context-marmot.env] MARMOT_DAEMON = "1"`.

### 2. index / status / verify / query / reembed — PASS with defects

- `marmot index` (no args) on a fresh vault: "No nodes found. Nothing to index." — vault-pipeline mode; source indexing requires a positional path. NOTE: easy to misuse; nothing hints at `marmot index .`.
- `marmot index .` — "Static analysis complete: total=24 added=20 ... errors=4" in ~20ms. PASS, but `errors=4` is **silent** — no file/entity names, nothing on stderr (see Issues 2, 3).
- Re-running `marmot index .` on an **unchanged** tree is not idempotent: `added=16 superseded=4 errors=4` every run; `default/web/src/cart.md` and `format.md` (TS file nodes) end up `status: superseded`, `superseded_by:` their **own child functions** (`.../cart/renderCartSummary`, `.../format/formatPrice`) — see Issue 1.
- `marmot status` — correct counts, but reports `Stale: 3` immediately after a fresh index (see Issue 4).
- `marmot verify` — right after a fresh index reports 12 issues: 3 false hash mismatches (both TS file nodes + the go.mod "go" node), 7 `dangling_edge` errors for stdlib/external targets (`fmt`, `errors`, `fmt/Println`, `errors/New`, cross-package refs), and 2 `valid_until == valid_from` warnings on superseded nodes. See Issues 4, 5, 8.
- `marmot query -query "how is the cart total computed"` — returned `main/ComputeCartTotal` + neighbors with code context, well-formed `<context_result>`. `-depth`/`-budget` respected. PASS.
- `marmot reembed` — "Indexed 20/20 nodes into embedding store", exit 0, ~12ms. PASS.
- NOTE: query/serve stderr prints "vault registry: 4 remote vaults registered" — the fresh scratch vault picked up the user's global `~/.marmot/routes.yml`. By design for warren routing, but surprising for an isolated vault (Issue 9).

### 3. Standalone stdio MCP session (no daemon env) — PASS

Drove `marmot serve` with raw newline-delimited JSON-RPC (initialize → notifications/initialized → tools/list → tools/call). 6 tools listed (`context_delete/namespace/query/tag/verify/write`).
- `context_write` without `id` → clean error "id parameter is required". Schema requires `["id","type"]`.
- Write `qa/baseline-discount-note` then `context_query` in the same session → node returned first in results, and persisted as `.marmot/qa/baseline-discount-note.md`. PASS.
- NOTE: unknown arguments are silently accepted — a write with `content:` (correct prop is `context:`) succeeded and produced a node with an empty Context section (Issue 7).
- No daemon artifacts created without `MARMOT_DAEMON=1`. PASS.

### 4. Daemon mode, same vault (MARMOT_DAEMON=1) — PASS

- serve #1 (stdin held via fifo): stderr "ContextMarmot MCP server ready on stdio (vault owner)"; `.marmot/.marmot-data/` gained `daemon.lock`, `daemon.info.json` (`{"pid":83772,"socket":"$TMPDIR/marmot-ec6884....sock","version":"v0.1.10-dirty",...}`), unix socket in TMPDIR, and `embeddings.db-wal`/`-shm`. PASS.
- serve #2, same vault: stderr exactly "ContextMarmot MCP proxy → /var/folders/.../marmot-ec6884abe95ba0ed.sock"; full MCP session through the proxy (initialize, context_write `qa/proxy-note`, context_query) worked. PASS.
- Cross-process read-your-writes: proxy-written node visible via owner session query, owner-written node visible via proxy session query (both directions). PASS.
- NOTE: a `context_query` issued immediately (same pipe flush) after `context_write` in the proxy session did **not** return the just-written node; it was visible ~2s later. Small async-embedding visibility window (Issue 6).

### 5. Failover (SIGKILL owner with live proxy session) — PASS

- `kill -9 <owner 83772>` while proxy session open. Within ~3s `daemon.info.json` showed `pid` = proxy's PID (83881), proxy stderr re-printed the engine banner ending "...ready on stdio (vault owner)".
- A `tools/call` on the **same, already-open** proxy session after the kill succeeded with correct results. PASS.
- A fresh serve #3 then proxied to the re-elected owner and answered queries. PASS.
- After all serves exited (stdin EOF): `daemon.info.json` removed, socket removed, WAL checkpointed (`-wal`/`-shm` gone). Only the inert 6-byte `daemon.lock` file remains — normal for flock; not recreated by non-daemon runs. PASS.

### 6. Guards with live owner — PASS

- `marmot watch` → exit 1, "watch: vault is served by marmot daemon (pid 83772); watch is redundant". PASS.
- `marmot index --force` → exit 1, "index: vault is served by marmot daemon (pid 83772); index --force would delete the embeddings DB under its open connection — stop the daemon first". Clear message. PASS.
- Plain `marmot index` (vault pipeline) allowed, exit 0. PASS.
- Freshness: wrote `internal/store/shipping.go`, ran `marmot index .` externally; ~3s later a query through the **owner's live session** returned `internal/store/shipping/ShippingRate`. Graph watcher picks up external index. PASS.
- `marmot query` and `marmot ui` with live owner print "heatmap: detached — vault owner (pid N) records heat". PASS.

### 7. Overrides — PASS

- `MARMOT_DAEMON=1 marmot serve --no-daemon` → serves standalone, no daemon artifacts.
- `MARMOT_DAEMON=1 MARMOT_NO_DAEMON=1 marmot serve` → same, env override wins.
- Default (no env): no daemon artifacts, byte-for-byte pre-daemon behavior observed. PASS.

### 8. `marmot ui --no-open` with live owner — PASS with notes

- `marmot ui -no-open -port 3299`: started, logged heatmap detach, "live-reload: watching vault for changes". Note the flag is `-no-open` (single dash also accepted with `--`).
- `GET /api/version` → 200 `{"version":0}` — see Issue 10.
- `GET /api/graph/default` → 200, 23 nodes/14 edges. `GET /api/namespaces`, `GET /api/search?q=discount` → correct JSON (search ranked the QA insight node first). `GET /` → 200 SPA.
- NOTE: `GET /api/graph/nodes` (unknown namespace) returns 200 with an empty graph rather than 404; unknown `/api/*` paths fall through to the SPA HTML.
- Scheduler: with mock config there is no summarizer, so the suppression branch ("summary: scheduler suppressed — vault owner (pid N) runs it", pipeline.go:694) is not observable; observed line was "summary: no summarizer available" — i.e., no duplicate-LLM behavior possible in this config. NOTE only.
- SIGTERM → "Shutting down UI server...", process exited cleanly. PASS.

### 9. WAL sanity — PASS

- `embeddings.db-wal` + `-shm` present while a serve is open (WAL grew to ~4MB under load).
- 30 rapid `context_write` calls through the live owner concurrently with 3× `marmot index .` + 3× `marmot index`: 30/30 writes returned created/updated, zero "database is locked" in any stdout/stderr. WAL checkpointed back into `embeddings.db` on shutdown. PASS.

### 10. Incidental observations

- The generic indexer is a **catch-all for every extension**: `marmot index .` indexed `go.mod` (node id "go"), `.codex/config.toml`, `.mcp.json`, `.cursor/mcp.json`, `.vscode/mcp.json`, and even a stray `serve_baseline.err` log file this test briefly left in the tree (node was removed). See Issue 3.
- Startup stderr is chatty on every CLI query/serve (6-7 banner lines). Fine for serve, noisy for `query`.
- No orphan processes: all test-spawned serves/ui exited; only the two pre-existing user `marmot serve` processes (25237, 36147) remain, untouched.

### Issues found

1. **major — re-indexing an unchanged tree corrupts TS file nodes via fallback classifier.** Repro: mock embeddings + classifier `none`; `marmot index .` twice in a project containing `web/src/cart.ts` / `format.ts`. Second run reports `superseded=4` and leaves `default/web/src/cart` and `default/web/src/format` with `status: superseded`, `superseded_by:` their own child function nodes (`.../cart/renderCartSummary`, `.../format/formatPrice`). Every subsequent index repeats the churn (`added=16 superseded=4` forever). The embedding-distance fallback classifier should never let a child supersede its parent file node, and an identical-hash entity should be a NOOP.
2. **major — index save errors are silent and unexplained.** Repro: `marmot init` (writes `.codex/config.toml`) then `marmot index .` → `errors=1` (or 4 after `marmot setup`) with zero diagnostics. Isolated cause: entities from dot-directory config files (`.codex/config.toml`, `.mcp.json`, `.cursor/mcp.json`, `.vscode/mcp.json`) are indexed by the generic catch-all indexer and then fail `SaveNode` (internal/indexer/runner.go increments `result.Errors` and continues without logging). Users can't tell what failed or why.
3. **minor — generic indexer indexes every file extension.** `Registry.IndexerFor` (internal/indexer/registry.go:38) falls back to the generic indexer for any unrecognized extension, so lock files, logs (`*.err`), editor/MCP configs, and `go.mod` become graph nodes. Combined with Issue 2 this produces both noise nodes and silent errors. Suggest an allowlist or at least skipping dotfiles/dot-directories.
4. **major — verify/status report false staleness immediately after a fresh index.** Repro: `marmot index .` then `marmot verify` → `[warning] ... source hash mismatch` for `default/web/src/cart`, `default/web/src/format`, and `go` (go.mod) even though files are unchanged; `marmot status` shows `Stale: 3`. Go nodes are fine (golang.go stores Lines [0,0] to match `ComputeSourceHash`'s whole-file path); the TS/generic indexers' stored hash disagrees with verify's recomputation. False positives train users to ignore verify.
5. **minor — verify flags edges to external/stdlib symbols as errors.** Fresh index → 7 `[error] dangling_edge` for `fmt`, `errors`, `fmt/Println`, `errors/New`, and cross-package call targets like `internal/store/NewOrderStore` that the indexer itself emitted but never created nodes for. Either create stub nodes, mark edges external, or downgrade to info.
6. **minor — write→query visibility window.** A `context_query` sent immediately after `context_write` in the same MCP session (daemon proxy) missed the just-written node; visible ~2s later. If write-ack is meant to imply queryability, embed synchronously before responding, or document eventual visibility.
7. **minor — context_write silently ignores unknown arguments.** Passing `content:` instead of the schema's `context:` succeeds (`"status":"created"`) and yields a node with an empty Context body. Rejecting or warning on unknown args would catch client typos.
8. **minor — superseded nodes get `valid_until` == `valid_from`.** verify warns `valid_until "T" is not after valid_from "T"` for nodes superseded in the same index run that (re)stamped ValidFrom (runner.go sets both from the same `now`). Cosmetic but pollutes verify output.
9. **minor — fresh scratch vault inherits global routing.** Any vault, including a brand-new one under /tmp, loads `~/.marmot/routes.yml` and registers all remote vaults ("vault registry: 4 remote vaults registered"). By design for warren, but there is no per-vault opt-out and query stderr suggests cross-vault reach the user never configured for this vault.
10. **minor — `/api/version` returns `{"version":0}`** while `marmot version` reports v0.1.10-dirty and daemon.info.json carries the real version string. UI clients can't display a meaningful version.
11. **minor — generated MCP configs use a relative vault path** (`--dir .marmot`) and setup offers no flag for absolute paths or daemon env. Breaks any MCP client that doesn't launch servers with cwd = project root.

No critical issues found. The daemon election/proxy/failover/guard/override/WAL surfaces all behaved as specified.

## Codex MCP + UI computer-use pass (agent 2)

Environment: Codex CLI v0.143.0 (`/opt/homebrew/bin/codex`, ChatGPT auth), headless via `codex exec`. Scratch project: `/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad/manualtest/proj` (vault `<proj>/.marmot`, ~24 active nodes). Evidence logs in `<scratchpad>/codexqa/` (step*.stdout/stderr.log, *_last.txt, owner.*.log).

### Step 1 — MCP wiring (which config mechanism works)
- Command: `codex exec --skip-git-repo-check -s read-only "list MCP tools"` from the project dir (project has `.codex/config.toml` with the context-marmot server).
- Expected: context-marmot tools visible.
- Observed: only built-in tools listed; `tool_search` lookups for `context_query`/`context_write` found nothing. A follow-up run with the same project config but default (read-only) sandbox also could not find the tools.
- However: with `--dangerously-bypass-approvals-and-sandbox` the SAME project-level `.codex/config.toml` was honored — codex called `context_write` successfully with NO `-c` overrides (step 4 run). And with `-c mcp_servers.context-marmot.*` overrides + read-only sandbox, tools were visible but every call failed with `"user cancelled MCP tool call"`; adding the bypass flag made them succeed.
- Conclusion: BOTH mechanisms (project `.codex/config.toml` and `-c` CLI overrides) deliver the server, but codex 0.143 only exposes/executes the project-config MCP server when run with `--dangerously-bypass-approvals-and-sandbox` (project-trust gating), and MCP tool calls in `exec` mode are auto-cancelled unless approvals are bypassed. **PASS (with the trust/approval caveats above).**

### Step 2 — Cross-client daemon proxying (my serve owns, codex proxies)
- Setup: owner serve held open via fifo (`MARMOT_DAEMON=1 marmot serve --dir <vault> < fifo`), `daemon.info.json` pid = 86980 (mine).
- Command: `codex exec --dangerously-bypass-approvals-and-sandbox -c mcp_servers.context-marmot...` with prompt to call `context_query('authentication flow')`, `context_write('codex-e2e-test')`, `context_query('codex-e2e-test')`.
- Expected: all calls succeed through the proxy; owner pid unchanged; owner session can read codex's write.
- Observed: all three calls succeeded (query returned 4 nodes incl. `main/ComputeCartTotal`; write returned `{"node_id":"codex-e2e-test","status":"created"}`; re-query returned the new node first). `daemon.info.json` still pid 86980 after the run. I then issued a raw JSON-RPC `tools/call context_query('codex-e2e-test')` into MY owner's stdin via the fifo — the owner returned the codex-written node (cross-client read-your-writes). Owner stderr logged `daemon: graph reloaded (56 nodes)`. **PASS.**

### Step 3 — Codex-as-owner election + cleanup
- Closed my owner (fifo EOF): serve exited, `daemon.info.json` + socket removed, `daemon.lock` remained but flock-FREE (verified by acquiring it). **PASS.**
- Ran `codex exec` alone (project bypass mode): mid-run `daemon.info.json` showed pid 89439, whose parent was the codex process (89416) — codex's spawned serve won the election. Both `context_query` calls returned correct results (incl. the persisted `codex-e2e-test` node from the previous session).
- After codex exited: no `daemon.info.json`, socket gone, lock file present but flock-free, no orphan `marmot serve` processes. **PASS.**

### Step 4 — UI tested via codex computer use
- Setup: `marmot ui --dir <vault> --port 3299 --no-open` started by me (HTTP 200, `live-reload: watching vault for changes`; stderr notes chat has no LLM provider — slash commands only).
- Command: `codex exec --dangerously-bypass-approvals-and-sandbox` prompting codex to browser-test http://localhost:3299. Codex's computer-use/browser capability worked; it reported concretely:
  1. Graph rendered: namespace selector `default (24)`, console `Graph loaded: 24 nodes, 14 edges`, labels incl. `main/ComputeCartTotal`, `codex-e2e-test`, `ShippingRate`. **PASS**
  2. Node click: right-side details panel for `main/ComputeCartTotal` with type/status/namespace, summary + context textareas, source path + `Lines 15-21`, hash, `Edges (2)`, disabled Save button. Noted mismatch: hover card said `function · 4 edges` vs panel `Edges (2)`. **PASS with note**
  3. Search `shipping`: ranked dropdown, 19 rows, top hits `internal/store/shipping/ShippingRate` (0.49) etc. **PASS**
  4. Chat/curator: placeholder invites `/` commands, but `/help` → `Unknown command: /help`. `/verify` worked: switched to Issues tab, `56 nodes · 17 issues · 70% curated`, listed disconnected nodes (`qa/proxy-note`, `codex-e2e-test`, ...). **PASS with issue (no /help)**
  5. Console: no JS errors; only repeated `Graph loaded: 24 nodes, 14 edges` info logs. **PASS**
  6. Resize to 390x844 + reload: layout broke — search input collapsed to ~48px, Heat/Superseded controls offscreen, header overflow, sidebar/canvas overlap, details panel + close button offscreen. **FAIL (responsive layout)**
- Codex also wrote `qa/manual-graph-explorer-browser-pass` via MCP during this run (proving project-config MCP + UI coexistence).

### Step 5 — Live-reload of session-written nodes
- Expected: nodes written during the session appear in the UI.
- Observed: `codex-e2e-test` (written in step 2) rendered in the graph and in `/verify` output; the `qa/manual-graph-explorer-browser-pass` node written DURING the UI session triggered `daemon: graph reloaded (57 nodes)` in UI stderr and appears in `GET /api/graph/_all` (25 active nodes incl. both new nodes). **PASS.**

### Step 6 — Anomalies / artifacts
- No codex-side MCP timeouts once approvals were bypassed; no stderr noise surfaced by codex; no daemon artifacts left (only flock-free `daemon.lock`); no orphan processes; the two pre-existing `marmot serve` sessions (pids 25237, 36147) were untouched throughout.
- NOTE: `GET /api/graph` and `/api/nodes` (no namespace) return the SPA index.html via catch-all instead of 404/JSON — harmless but confusing for API consumers (real routes are `/api/graph/{namespace}`, `/api/graph/_all`).
- NOTE: curator `/verify` reports `56 nodes` while the graph/API shows 25 active — presumably superseded/archived nodes from agent 1's sweep are counted; worth confirming that is intended.

### Issues found
1. **medium — mobile/narrow responsive layout is broken.** Repro: open http://localhost:3299, resize to 390x844, reload. Search input collapses to ~48px, Heat/Superseded controls and node-details close button go offscreen, filter sidebar overlaps the canvas, graph overflows both edges. (Observed by codex computer-use browser.)
2. **minor — `/help` is not a recognized curator command** even though the input placeholder says "type / for commands". Repro: type `/help` in the chat box → `Unknown command: /help`. Should list available commands.
3. **minor — hover card vs details panel edge-count mismatch.** `main/ComputeCartTotal` hover shows `4 edges`, details panel shows `Edges (2)` (likely in+out vs out-only). Repro: hover then click the node.
4. **minor — codex exec UX interaction (client-side but affects adoption):** with the default/read-only sandbox, MCP tool calls fail as `"user cancelled MCP tool call"` and project `.codex/config.toml` MCP servers may be hidden entirely; headless codex users must pass `--dangerously-bypass-approvals-and-sandbox` (or configure trust) for context-marmot to work. Worth documenting in setup docs.
5. **note — bare `/api/graph`, `/api/nodes` return index.html (SPA fallback) rather than a JSON error.**

Daemon election, proxying, cross-client read-your-writes, codex-as-owner failover, and cleanup all behaved as specified with a real second MCP client.

## Fix verification (iteration 2)

Environment: branch `multiprocess-lock-fix` working tree (uncommitted fixes), rebuilt `bin/marmot` (`v0.1.10-4-gbf3540d-dirty`, commit bf3540d) via `make build` after `cd web && npm run build`. Fresh scratch project `/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad/retest1/proj` (go.mod, main.go, internal/store/store.go, web/src/cart.ts, web/src/format.ts; mock embeddings, classifier `none`). Every issue re-reproduced manually; logs in `<scratchpad>/retest1/logs/`.

### Agent-1 issues

1. **FIXED** — re-index of an unchanged tree is a pure no-op: run1 `total=12 added=12`, run2/run3 `added=0 updated=0 superseded=0 skipped=12 errors=0`; zero `status: superseded` nodes; TS file nodes (`default/web/src/cart`, `.../format`) stay active. Fix: runner is hash-deterministic for existing entity IDs (classifier never consulted), classifier fallback skips hierarchically related candidates, and runner degrades any SUPERSEDE against a live entity of the same run to ADD. Regression tests: `TestRunner_ReindexUnchangedTreeIsNoop`, `TestRunner_ChangedFileIsPlainUpdate`, `TestClassify_Fallback_SkipsParentChild`, `TestClassify_Fallback_PrefersUnrelatedCandidate`.
2. **FIXED** — index errors now print diagnostics: forced a `SaveNode` failure (pre-created dir at `.marmot/notes.md`) → stderr `index error: save node notes: save node: rename ...: file exists` alongside `errors=1`. The original silent cause is also gone: `marmot init` + `marmot setup` tree indexes with `errors=0` because dot-directory configs are skipped. `RunResult.ErrorDetails` covers save/embed/parse failures (unit-tested).
3. **FIXED (scope) / DEFERRED-BY-DESIGN (catch-all)** — hidden (dot-prefixed) files/dirs are skipped (`.codex/`, `.mcp.json`, `.cursor/`, `.vscode/` produce no nodes) and a non-semantic extension denylist (`.log .err .lock .tmp .bak .swp .swo .orig .rej`, gitignore-negation overridable) drops logs/locks — verified `stray_baseline.err`/`debug.log` produced no nodes. The generic catch-all for other unrecognized extensions is deliberately kept (registry.go registers generic as fallback; generic.go handles Unknown language + binary detection): `go.mod` still becomes node `go`. Rationale verified in code.
4. **FIXED** — fresh index → `marmot verify` reports zero hash mismatches and `marmot status` shows `Stale: 0` (was 3). `ComputeSourceHash` line-range path now hashes exact bytes incl. terminators so indexer-stored and verify-recomputed hashes agree (regression test `internal/verify/fresh_index_staleness_test.go`).
5. **FIXED (verify-side)** — dangling edges to external/stdlib symbols (`fmt`, `errors`, `fmt/Println`, `errors/New`, `internal/store/NewOrderStore`, …) are now `[info] ... likely external/stdlib` instead of `[error]`; only reference-type relations (imports/calls/extends/implements/references) are downgraded — a dangling `contains` stays an error. Indexer-side stub/external nodes deliberately not implemented (deferral rationale verified: Info classification chosen instead).
6. **FIXED** — daemon proxy session: `context_query` pipelined in the same pipe flush immediately after `context_write` returned the just-written node (`qa/proxy-rw-note` present in the id:3 response). Fix: stdio worker pool of 1 per session serializes pipelined tool calls; separate sessions still run concurrently (verified in `internal/mcp/server.go`).
7. **FIXED** — `context_write` with `content:` → tool error `unknown argument(s) "content" (did you mean "context"?) — valid arguments are: id, type, namespace, summary, context, tags, edges, source`; a write with neither summary nor context is refused ("refusing to create an empty node"); the corrected write succeeded and persisted. Regression tests in `internal/mcp/write_validation_test.go`.
8. **FIXED (verify-side) / runner stamping DEFERRED-BY-DESIGN** — hand-crafted node with `valid_until == valid_from` produces no warning; an inverted window (`valid_until < valid_from`) still warns (`is before valid_from`). Runner stamping both from the same batch `now` for create-and-supersede-in-one-run is intentional (zero-length validity window) — rationale verified.
9. **DEFERRED-BY-DESIGN, with opt-out** — loading `~/.marmot/routes.yml` on every serve/query is deliberate for warren/cross-vault routing; kept. Verified improvements: stderr now reads `vault registry: 4 remote vaults registered (global routing table; MARMOT_ROUTES=off disables)`; `MARMOT_ROUTES=off marmot query ...` prints no registry line and queries fine (also `none`/`0`/alternate path supported, README documents it). Regression tests in `internal/routes/routes_test.go`.
10. **FIXED** — `GET /api/version` → `{"version":0,"app_version":"v0.1.10-4-gbf3540d-dirty"}` (ldflags version threaded via `Server.WithAppVersion`).
11. **FIXED** — `marmot setup -claude -cursor -vscode -codex` writes the **absolute** vault path in all four configs (`.mcp.json`, `.cursor/mcp.json`, `.vscode/mcp.json`, `.codex/config.toml`); `relOrAbs` replaced by `absVaultPath`.

### Agent-2 issues

1. **FIXED** — 390x844 (Playwright, real browser): no horizontal overflow (`scrollWidth == 390`), search input full-width (370px, was ~48px), Heat/Superseded toggles on-screen (x=234/291), filter panel is a collapsed overlay drawer with a visible toggle button, minimap hidden, node click opens the detail panel fully on-screen with the close button reachable (x=345). Regression test `web/e2e/regressions.spec.ts` (mobile viewport test) passes.
2. **FIXED** — `/help` lists all 9 slash commands; unknown commands now answer `Unknown command: /bogus. Type /help to list available commands.` (verified in browser + e2e test).
3. **FIXED** — hover card `function · 1 out / 2 in` matches detail panel `Edges (1 out / 2 in)` for `main/ComputeCartTotal`, plus an "N incoming edges from other nodes (not listed)" note (verified in browser + e2e test).
4. **DEFERRED-BY-DESIGN (documented)** — codex `exec` approval/trust gating is Codex client behavior, not marmot's; README now carries a "Codex CLI users" callout explaining the `--dangerously-bypass-approvals-and-sandbox` / project-trust requirement.
5. **FIXED** — bare `GET /api/graph` and `/api/nodes` → `404 application/json {"error":"unknown API endpoint: GET /api/graph"}`; SPA fallback intact for non-API routes; `GET /api/graph/{unknown-namespace}` still returns 200 with an empty graph — DEFERRED-BY-DESIGN (namespaces are lazily created; rationale verified). Regression test `TestUnknownAPIPathsReturnJSON404`.
- Note (curator `/verify` node counts): **FIXED (display)** — curator verify now reports `found 7 issue(s) across 15 node(s) (14 active, 1 superseded)`; scanning superseded nodes is intentional (AllNodes — supersession chains/validity windows are graph integrity; rationale verified in `internal/curator/commands.go` + `internal/mcp/handlers.go`). `/api/curator/suggestions` `node_count` now counts active nodes only (14, excluded a superseded fixture).

### New finding during retest (fixed in this iteration)

- `web/src/curator.ts` `loadSuggestions()` fetched `/api/verify/{ns}` — an endpoint that never existed server-side. Pre-fix it silently received the SPA HTML; after the JSON-404 fix (agent-2 issue 5) every `/verify` slash command logged a visible `404 (Not Found)` console error (dead code path; Issues tab itself was unaffected — it uses `/api/curator/suggestions` via issues.ts). Fixed: `/verify` now dispatches `curator-refresh-issues` (handled in main.ts by `IssuesPanel.load`), and the dead `loadSuggestions`/`renderSuggestion`/`makeIssueBtn` duplicate renderer was removed. Regression test added: `web/e2e/regressions.spec.ts` "`/verify` refreshes the Issues tab without dead API calls" (asserts no `/api/verify/*` requests and no console errors). Re-verified in a live browser: `/verify` → Issues tab renders `12 nodes · 4 issues · 67% curated`, console clean.

### Deferred-rationale audit

All ten deferral claims from the fix agents were checked against code/docs and hold: generic catch-all registration (registry.go/generic.go), write-path validation (actually implemented by the MCP agent, verified), verify-side Info classification instead of indexer stub nodes, intentional zero-length validity windows, one-time staleness churn on pre-fix vaults (inherent to the hash-algorithm correction; next index heals via the plain-UPDATE path), verify covering superseded nodes (AllNodes in CLI/MCP/curator confirmed), lazy-namespace 200-empty graphs, per-session worker pool (cross-session concurrency preserved), and the global-routing default with `MARMOT_ROUTES` opt-out.

### Daemon/WAL spot-check (no regression)

- Owner election: `MARMOT_DAEMON=1 marmot serve` → `daemon.info.json` (pid 4734) + unix socket + `embeddings.db-wal/-shm`, stderr "vault owner". PASS.
- Proxy: second serve printed `MCP proxy →` and completed a full MCP session through the socket; owner pid unchanged. PASS.
- Cross-process read-your-writes: proxy-written `qa/proxy-rw-note` returned by a query in the owner's own stdio session (and the pipelined proxy-side query, per issue 6). PASS.
- Shutdown: stdin EOF → owner exited, `daemon.info.json` + socket removed, WAL checkpointed (`-wal`/`-shm` gone), only the inert `daemon.lock` remains. No orphan processes; the user's pre-existing serves (25237, 36147) untouched. PASS.

### Gates

`go build ./...`, `go vet ./...`, `go test -race -count=1 ./...` (22 packages ok, 0 FAIL), `go test -tags e2e ./e2e/` ok, `golangci-lint run ./internal/... ./cmd/...` → 0 issues, `cd web && npm run build` (tsc + vite) clean, `npx playwright test` → 7/7 passed.

### Remaining issues

none
