# UI Validation — Read-Only Surfaces (Graph / Detail / Selector / Search / Chat)

Run: 2026-07-10, driven by `codex exec` (computer-use, real browser) against `marmot ui`
at http://localhost:3311 (workspace vault `ws-main`, warren `demo-warren`). READ-ONLY pass:
no mount/unmount, no node edits; only `/help` and `/verify` sent in chat.
Evidence: `$SCRATCH/ui-validate/logs/codex_surfaces_stdout.log` (full codex transcript incl.
console capture), where
`$SCRATCH = /private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad`.
Setup baseline: `artifacts/ui_validation_setup.md`. UI server left running; API state untouched
(`active_projects:["proj-b"]` before and after).

## Step 1 — Graph view + warren switch — PASS

Expected: default graph renders; switching to demo-warren shows @pb-vault nodes (mounted)
and identity-project nodes served from the live vault.

Observed (verbatim from browser):
- Default view: selector `default (16)`, 16 nodes rendered (`client`, `formatUser`,
  `Store.Get`, `NewStore`, `Render`, ... — all unqualified), grouping `Group by type`,
  legend types `function/module/file/method/interface/type`.
- Switched selector to `Warren demo-warren (1 active, 1 identity)`. Console:
  `Graph loaded: 20 nodes, 26 edges`. Grouping changed to namespace groups
  `proj-b:default` and `proj-local:default`.
- @pb-vault nodes render: `@pb-vault/calc/Total`, `@pb-vault/mathutil/Add` (confirmed via
  detail/search). @ws-main nodes render: `@ws-main/go/store/NewStore`,
  `@ws-main/default/ts/client/User`.

API cross-check: `/api/graph/_all` = 16 nodes (all ns `default`);
`/api/warren/demo-warren/graph` = 20 nodes, exactly 4 with provenance `warren_mount`
(vault `pb-vault`) and 16 with provenance `local_alias` (vault `ws-main`) — i.e. the
identity project's nodes are read-throughs of the live vault, not copies. Matches UI.

## Step 2 — Detail panel edit vs read-only copy — PASS (with UX issue)

a. Local node (default view): clicked `NewStore` → detail title `go/store/NewStore`,
   editable `Summary` and `Context` sections, button `Save Changes` (disabled until input).
   Normal edit affordance present. PASS.
b. @pb-vault node (warren view): clicked `Total` → title `@pb-vault/calc/Total`; provenance
   block `Source warren_mount / Warren demo-warren / Project proj-b / Vault pb-vault /
   Editable no`; button replaced by disabled `Read-only Warren Node`; hint verbatim:
   `Enable writes: marmot warren edit proj-b --warren demo-warren`. Matches spec. PASS.
c. local_alias node (warren view): clicked `NewStore` → title `@ws-main/go/store/NewStore`;
   provenance `Source local_alias / Project proj-local / Vault ws-main / Editable no`;
   hint verbatim: `This is your live vault — edit the unqualified node.` Matches spec. PASS.
- Issue 1 (both b and c): the `Summary`/`Context` textareas remain enabled on read-only
  warren nodes — you can type into them; only the save path is blocked. Confirmed in source:
  `web/src/detail-panel.ts` sets `saveBtn` to read-only mode (lines 219–264) but never sets
  `readOnly`/`disabled` on `summaryArea`/`contextArea`. Silent dead-end for typed edits.

## Step 3 — Selector identity counts (R2.6) — PASS

Selector entry verbatim: `Warren demo-warren (1 active, 1 identity)` — identity count
renders. Matches `/api/warren/demo-warren/status` (proj-b active, proj-local self_alias).

## Step 4 — Cross-vault search — PASS

Query `sums integers` in warren view. Results (verbatim, top of list):
- `@pb-vault/mathutil/Add  0.47  Function Add — Add sums two integers; used by calc`
- `@ws-main/default/ts/client/User  0.44  Interface User in module client.ts`
- `@ws-main/go/store  0.44  Go file store.go in package main`
- `@pb-vault/calc/Total  0.44  Function Total — Total sums a slice via Add`
- `@pb-vault/mathutil  0.44  Go file mathutil.go in package pb`
- ... (12 results total; both `@pb-vault` and `@ws-main` prefixes present)

Clicked `@pb-vault/mathutil/Add`: graph panned/zoomed to the node and the detail panel
opened with title `@pb-vault/mathutil/Add` — click focuses the right node. PASS.

API cross-check: `GET /api/search?q=sums%20integers&ns=_warren/demo-warren` returns the same
ranked list with `@`-qualified ids and per-result provenance (`warren_mount` for pb-vault,
`local_alias` for ws-main). Plain `GET /api/search?q=...` (no `ns`) returns only unqualified
local ids — cross-vault results are correctly scoped to the warren namespace.

## Step 5 — Curator chat /help and /verify — PARTIAL

- `/help` PASS. Returned verbatim:
  `Available commands: /help, /tag <name>, /untag <name>, /type <type>, /merge <A> <B>,
  /delete, /link <A> <rel> <B>, /unlink <A> <rel> <B>, /verify -- Run health check`.
- `/verify` ran without error: chat replied `Health check complete. See Issues tab.` and
  switched to the Issues tab, which showed `16 nodes · 2 issues`, `88% curated`, and two
  `Small disconnected subgraph with 2 node(s)` items (go/main*, go/render*).
- Issue 2: `/verify` does NOT report the expected active/superseded breakdown. Root cause in
  source: `web/src/curator.ts:554-558` short-circuits the `verify` case — it only dispatches
  `curator-refresh-issues` and prints the static string; it never invokes the backend curator
  verify command, whose result message is exactly the expected breakdown
  (`"%d node(s) (%d active, %d superseded)"`, `internal/curator/commands.go:637-651`).
  The breakdown exists server-side but is unreachable from the chat UI.
- Note: the Issues tab count (`16 nodes`) reflects the local vault even while the graph is in
  warren view — the curator panel is not warren-scoped (consistent with mounted vaults being
  read-only, so arguably by design; recorded as an observation, not a defect).

## Step 6 — Console — PASS

0 errors, 0 warnings across the whole session. Only LOG entries
(`Graph loaded: 16 nodes, 15 edges` / `Graph loaded: 20 nodes, 26 edges`). No failed
network requests (performance-entry check returned `[]`).

## Issues found

1. **Minor (UX, detail panel)** — Read-only warren nodes (`@pb-vault/...` and `@ws-main/...`
   aliases) still render enabled Summary/Context textareas; typing is accepted but can never
   be saved (save path blocked, no feedback). `web/src/detail-panel.ts` never sets
   `readOnly` on the textareas when `isReadOnlyWarrenNode` is true.
2. **Minor (feature gap, chat)** — `/verify` in curator chat never surfaces the
   active/superseded breakdown: `web/src/curator.ts:557` prints a hardcoded
   `Health check complete. See Issues tab.` instead of calling the backend verify command
   that computes `N node(s) (X active, Y superseded)` (`internal/curator/commands.go:639`).
3. **Observation (not a defect vs. spec)** — Curator Issues tab stays scoped to the local
   vault (16 nodes) while the graph is in warren view; mounted read-only nodes are not
   health-checked from the UI.

Overall: graph view, warren switch, detail-panel copy (both read-only variants), selector
identity counts, cross-vault search + focus, and /help all PASS with verbatim matches to
spec and API; /verify runs but lacks the active/superseded breakdown (Issue 2).
