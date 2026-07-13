# Warren UI validation (codex computer-use)

Consolidated 2026-07-10 from four validation passes (setup, panel mutating flows,
read-only surfaces, robustness) run with `codex exec` computer-use in a real browser
against `marmot ui` at http://localhost:3311 (workspace vault `ws-main`, warren
`demo-warren`). Source artifacts: `ui_validation_setup.md`, `ui_validation_panel.md`,
`ui_validation_surfaces.md`, `ui_validation_robustness.md` (included verbatim below).

## Verdict

**Overall: PASS with issues.** Every scripted expectation for the warren panel, identity
badges, mount/unmount round-trip, doctor section, bridges section, selector identity
counts, read-only detail-panel copy, cross-vault search, and error-JSON surfacing was met;
console was clean under trusted input. 8 real issues survived verification (codex false
positives from synthetic events were explicitly refuted and excluded):

| Severity | Count | Issues |
|---|---|---|
| Major | 1 | C1 (warren panel unreachable at 390px) |
| Medium | 1 | C2 (silent graph-load failure) |
| Minor | 5 | C3–C7 |
| Trivial | 1 | C8 |
| Observations (not defects vs. spec) | 4 | O1–O4 |

Pass/fail by section: Setup — environment built as specified. Panel — 6/6 steps PASS.
Surfaces — 5 PASS, 1 PARTIAL (`/verify` breakdown missing). Robustness — responsive step
FAIL (mobile entry point), error-surfacing step PARTIAL FAIL; rest PASS.

---

# UI Validation Environment Setup

Built 2026-07-10 by the setup subagent. Root:
`/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad/ui-validate`
(referred to as `$ROOT` below). Binary used: `/Users/nurozen/Documents/GitHub/context-marmot/bin/marmot`
(`v0.1.10-10-g41db2da-dirty`).

## Workspace project — `$ROOT/workspace`
- 6 small source files with cross-calls:
  - `go/main.go` -> calls `NewStore`/`Put`/`Get` (`go/store.go`) and `Render` (`go/render.go`)
  - `ts/api.ts` -> imports `fetchUser` (`ts/client.ts`) and `formatUser` (`ts/format.ts`)
- Vault at `$ROOT/workspace/.marmot`, initialised printf-driven with **mock** embeddings and
  **none** classifier (embedding-distance fallback).
- `vault_id: ws-main` set via `marmot configure --vault-id ws-main --dir .marmot`.
- Indexed: `marmot index .` → total=16 added=16 errors=0.

## Warren repo — `$ROOT/warren-repo`
- `marmot warren init --id demo-warren` → manifest `_warren.md` (warren_id demo-warren).
- Projects (imported via `marmot warren project import`):
  - **proj-local** = import of the workspace vault; preserved `vault_id: ws-main`
    (path `projects/proj-local/.marmot`). This matches the workspace vault ID, so it is the
    IDENTITY project.
  - **proj-b** = second seeded project (source `$ROOT/proj-b-src`, 2 Go files `mathutil.go`/
    `calc.go` with a cross-call, mock-embedded, indexed total=4), `vault_id: pb-vault`
    (path `projects/proj-b/.marmot`).
- Bridge: `marmot warren bridge add proj-local proj-b` → manifest bridge
  proj-local -> proj-b (relations: references).
- `marmot warren doctor` (run in warren-repo): `Warren "demo-warren" manifest looks healthy.` (exit 0).

## Workspace registration / mounts
- `marmot warren register --dir .marmot demo-warren $ROOT/warren-repo`
  - Register emitted the R2 identity note: proj-local matches workspace vault ID "ws-main" —
    served as the live vault.
- Mounted ONLY proj-b: `marmot warren mount --warren demo-warren --dir .marmot proj-b`.
- `marmot warren status --warren demo-warren`:

```
PROJECT     STATE     EDITABLE  AVAILABLE  PATH
proj-b      mounted   false     true       $ROOT/warren-repo/projects/proj-b/.marmot
proj-local  identity  false     true       .marmot
```

- `marmot warren list`:

```
WARREN_ID    PATH               REACHABLE  ACTIVE  EDITABLE  MATERIALIZED  IDENTITY
demo-warren  $ROOT/warren-repo  true       1       0         false         proj-local
```

## UI server (LEAVE RUNNING)
- Started detached from `$ROOT/workspace`:
  `nohup marmot ui --dir .marmot --port 3311 --no-open > $ROOT/logs/ui.log 2>&1 & disown`
- **PID: 98918**, URL: **http://localhost:3311** (binds 127.0.0.1). Log: `$ROOT/logs/ui.log`.
- `GET /api/version` → `{"version":0,"app_version":"v0.1.10-10-g41db2da-dirty"}`
- `GET /api/warrens` →

```json
{"warrens":{"demo-warren":{"path":"$ROOT/warren-repo","active_projects":["proj-b"],"identified_projects":["proj-local"]}}}
```

Expected UI state for validation: warren panel shows demo-warren with proj-b `mounted`
(read-only, available) and proj-local `identity`; one manifest bridge proj-local -> proj-b;
doctor healthy; selector identity count 1.

---

# UI Validation — Warren Panel Mutating Flows

Run: 2026-07-10, driven by `codex exec` (computer-use, real browser) against `marmot ui`
at http://localhost:3311 (workspace vault `ws-main`, warren `demo-warren`).
Evidence: `$SCRATCH/ui-validate/logs/codex_panel_run1.log` (full codex transcript incl.
console capture) and `$SCRATCH/ui-validate/logs/warrens_poll.log` (independent 2s poll of
`/api/warrens` during the whole browser session), where
`$SCRATCH = /private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad`.
Setup baseline: `artifacts/ui_validation_setup.md`. UI server (PID 98918) left running;
final API state restored to baseline (`active_projects:["proj-b"]`).

## Step 1 — Panel rows + selector identity count — PASS

Expected: proj-b `mounted` (read-only, available), proj-local `identity` badge, mount/unmount
affordances, selector identity count.

Observed (verbatim from browser):
- Panel opened via top-bar button `Warrens`.
- Row: `proj-b  mounted  no  yes  Unmount` — STATE `mounted` (green badge), EDITABLE `no`,
  AVAILABLE `yes`, enabled `Unmount` button.
- Row: `proj-local  identity  no  yes` — STATE `identity` (yellow badge), EDITABLE `no`,
  AVAILABLE `yes`, NO buttons. Badge title: `This project IS this workspace (vault_id match) —
  served from the live vault`.
- Selector dropdown option: `Warren demo-warren (1 active, 1 identity)` — identity count present
  in the option label (not as a standalone badge in the collapsed selector, which shows
  `default (16)`).

Matches `/api/warren/demo-warren/status` (proj-b active/editable=false/available=true;
proj-local self_alias=true).

## Step 2 — Unmount then re-mount proj-b — PASS

- Clicked `Unmount` on proj-b: row flipped to `proj-b  dormant  no  yes  Mount`;
  `/api/warrens` dropped `active_projects` (codex in-session curl + independent poller:
  unmounted window 22:33:52–22:34:19).
- Clicked `Mount`: row flipped back to `proj-b  mounted  no  yes  Unmount`;
  `/api/warrens` returned `active_projects:["proj-b"]` (poller: 22:34:21 onward).
- Graph re-rendered on both transitions (console: live-reload + `Graph loaded` entries).
- Caveat feeding Issues 2/3 below: the selector option text did NOT update after unmount
  (still `(1 active, 1 identity)`), and the unmounted API response omits `active_projects`
  rather than returning `[]`.

## Step 3 — Per-warren refresh — PASS (with UX issue)

- Refresh exists per-warren: button `↻` next to `demo-warren`, title `Reload warren state from
  disk` (no row-level refresh).
- Click succeeded (console `Graph loaded: 16 nodes, 15 edges`), no inline error, no crash —
  but zero visible feedback: no spinner, timestamp, toast, or success message (Issue 1).
  The success path is silent; error surfacing could not be exercised (no failure available).

## Step 4 — Doctor badge + section — PASS

- Section `Workspace doctor` rendered (always expanded; no numeric count badge in the header,
  one issue item shown).
- Issue verbatim: severity `info`, code `self_identity` (chip colored rgb(212,168,83)),
  message `project demo-warren/proj-local is identified with this workspace (vault ID
  "ws-main"); it serves from the live vault`.
- Matches `/api/doctor/workspace` exactly: one info-level self_identity issue, no errors or
  warnings. Meets expectation.

## Step 5 — Bridges section — PASS

- Section `Cross-vault bridges` rendered line verbatim: `proj-local ↔ proj-b (references)`.
- Matches `/api/bridges`: source proj-local, target proj-b, allowed_relations ["references"],
  is_cross_vault true.

## Step 6 — Negative case: identity project has no mount/edit affordance — PASS

- proj-local row has NO controls at all: no Mount, no Unmount, no "make editable" — consistent
  with R2 (identity is automatic). Explanatory copy is present as the identity badge title:
  `This project IS this workspace (vault_id match) — served from the live vault`.
- Detail-panel read-only copy verified: selecting warren namespace
  (`Warren demo-warren (1 active, 1 identity)`) showed `PROJ-B:DEFAULT` / `PROJ-LOCAL:DEFAULT`
  groups; clicking proj-local node `client` showed `@ws-main/default/ts/client`,
  `Project proj-local`, `Vault ws-main`, `Editable no`, `Read-only Warren Node`,
  `This is your live vault — edit the unqualified node.` — the expected local_alias copy.

## Console

No browser console errors during the entire session (LOG entries only: graph loads,
live-reload notices).

## Issues found

1. **Minor (UX)** — Per-warren refresh `↻` gives no visible feedback on success (no spinner,
   toast, timestamp, or inline message). It does work (state reloads) and no error is
   swallowed, but a user cannot tell the click did anything.
2. **Minor (staleness)** — After unmounting proj-b, the namespace-selector option still read
   `Warren demo-warren (1 active, 1 identity)` while `/api/warrens` showed no active projects;
   the panel row updated but the selector label did not refresh until later. Confirmed by
   codex's in-session curl vs. rendered option text.
3. **Trivial (API shape)** — With all non-identity projects unmounted, `/api/warrens` omits
   the `active_projects` key entirely (Go `omitempty`) instead of returning `[]`; clients
   must null-check.
4. **Observation (not a defect vs. spec)** — No standalone identity-count badge in the
   collapsed selector area and no numeric issue-count badge on the doctor header; counts live
   in the dropdown option text and the doctor list itself.

Overall: all 6 steps PASS against expectations; mount/unmount round-trip independently
verified via API polling; identity project correctly offers no affordances; read-only copy
matches spec.

---

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

---

# UI Validation — Robustness (Responsive / Console / Tooltips / Reload / Errors)

Run: 2026-07-10, driven by `codex exec` (computer-use, isolated instrumented Chrome) against
`marmot ui` at http://localhost:3311 (workspace vault `ws-main`, warren `demo-warren`,
proj-b mounted read-only, proj-local identity). Read-only session: no mount/unmount clicked,
no mutating POSTs. Evidence: `$SCRATCH/ui-validate/logs/codex_robustness_run1.log` (full codex
transcript) plus an independent Playwright verification pass by the validator (trusted CDP
input events, used to confirm/refute codex findings), where
`$SCRATCH = /private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad`.
UI server left running; API state at exit matches baseline (`active_projects:["proj-b"]`).

Note on evidence quality: codex drove some interactions with synthetic `dispatchEvent`
MouseEvents (no `view: window`), which produced two false positives. Every codex finding below
was re-tested with trusted input events; verdicts reflect the re-test.

## Step 1 — Responsive 390x844 — FAIL (one real defect; two codex claims refuted)

CONFIRMED (codex + independent Playwright re-measure, exact same numbers):
- Viewport 390: `#refresh-btn` rect x=369 right=399 (9px clipped) and `#warren-toggle`
  (`Warrens` button) rect x=409 right=475 — ENTIRELY offscreen. Body `overflow-x: hidden`
  (bodyScrollWidth=390), so both are unreachable by a real user: **the warren panel cannot be
  opened at all on a 390px phone.** Root cause: `web/src/style.css` — `#toolbar` wraps
  (`@media (max-width: 900px)` sets `flex-wrap: wrap`) but `.toolbar-left` (line 136,
  `flex: 0 0 auto`, non-wrapping row) contains the wide `#namespace-select`
  (option text `Warren demo-warren (1 active, 1 identity)`) plus ↻ and Warrens; the group
  exceeds 390px and the tail is clipped. This is the same class of mobile regression the old
  sidebar had.
- Search IS usable at 390: input rect x=10 w=370, typing `client` renders result rows
  (`default/ts/format`, `default/ts/client/User`, `default/ts/client/fetchUser`,
  `default/ts/client`) on-screen.
- Detail panel opens as a full-width drawer (rect x=0 w=390 h=677, `default/ts/client`,
  `MODULE ACTIVE`).
- Warren panel content is fine at 390 once open (opened at 1440 then resized down): rect
  x=0 w=351, close button in viewport (x=310..334), rows `proj-b mounted no yes Unmount` /
  `proj-local identity no yes`, table fits (w=343), `overflow-y: auto`, no `undefined` text.
  Only the entry point (toggle button) is broken.
- Resize back to 1440x900 recovers cleanly (all controls in viewport, no overflow).

REFUTED codex claims (automation artifacts, not real bugs):
- "detail close failed and layout shifted left x=-84.5": with a trusted click on
  `#detail-close` the drawer closes correctly (class `hidden`,
  transform `matrix(1,0,0,1,390,0)`, x=390 offscreen; body/doc scrollLeft 0). The -84.5px
  shift only occurred in codex's session after it force-manipulated the DOM.
- "Warrens panel opens cut off at x=-84.5": same artifact; independently the open panel sits
  at x=0.

## Step 2 — Console hygiene — PASS (after artifact filtering)

Codex's normal desktop browse (fresh load, panel open, nodes Store/fetchUser/api clicked,
search `ts` and `zzzqqq`, namespace switched default → _all-ish options → warren, group-by
namespace, superseded toggle) captured:
- LOG lines proving capture worked: `Graph loaded: 16 nodes, 15 edges`,
  `Graph loaded: 0 nodes, 0 edges`, `Graph loaded: 20 nodes, 26 edges`.
- warning `[live-reload] SSE connection lost, will auto-reconnect` +
  `GET /api/events net::ERR_ABORTED` — occurs only on navigation/reload (EventSource aborted
  by page teardown); benign/cosmetic, auto-reconnects.
- error `TypeError: Cannot read properties of null (reading 'document')` (repeated, from
  bundled d3 `Xt(e){var t=e.document...}` = `dragDisable(event.view)`): **REFUTED as a real
  bug.** Trigger is synthetic MouseEvents lacking `view: window`. Independent Playwright pass
  clicking the same nodes with trusted CDP input: **0 errors, 0 warnings**. Real user pointer
  events always carry `view`.
- No 4xx/5xx network responses during normal browsing; no phantom-endpoint requests (the old
  JSON-404 catch-all regression did not reappear).

## Step 3 — Skip-reason tooltips (absence check) — PASS

No skipped vaults exist (`/api/warren/demo-warren/graph` has no `skipped`/`skipped_reasons`
keys). With `Warren demo-warren (1 active, 1 identity)` selected and the panel open:
- proj-b row: title `""`, class `""`, no `warren-row-skipped` class.
- proj-local row: title `""`, class `""`, no `warren-row-skipped` class.
- Whole-panel DOM text search: `undefinedInPanelText=false`, `nullInPanelText=false`,
  `skippedClassCount=0`, `exactUndefinedMatches=[]`. Independently re-confirmed
  (`panelTextHasUndefined: false`). Clean absence — no `undefined` artifacts, no phantom
  tooltips.

## Step 4 — Hard reload mid-panel-open — PASS (with note)

Before: namespace `_warren/demo-warren`, panel open. After Cmd+Shift+R: page renders fully
(`Graph loaded: 16 nodes, 15 edges` x2), panel closed (`panelClass="hidden"`), namespace reset
to `default (16)`, no console errors (only the expected SSE-lost warning + `/api/events`
ERR_ABORTED from teardown). Reopening the panel renders both rows correctly. Sane recovery;
note: namespace/panel state is not persisted across reload (resets to default) — acceptable,
but worth knowing.

## Step 5 — Error surfacing, nonexistent warren — PARTIAL FAIL

a) `GET /api/warren/nope-does-not-exist/status` → HTTP 404, body verbatim
   `{"error":"Warren not registered: nope-does-not-exist"}` (same for `/graph`). Human-readable
   JSON, not blank. PASS. (`/api/phantom/endpoint` → 404
   `{"error":"unknown API endpoint: GET /api/phantom/endpoint"}` — catch-all healthy.)
b) `GET /definitely-not-a-real-page` → HTTP 200 `text/html`, serves the SPA (app renders
   normally, no console errors). Acceptable SPA fallback, not blank. PASS.
c) In-app: forcing the selector to `_warren/nope-does-not-exist` (the UI has no URL routing, so
   this is the only in-app path) → **no blank screen, but no user-visible error either**: the
   stale previous graph stays rendered, selector shows the bogus option, `#warren-panel-error`
   stays empty, warren panel shows no inline error. Console gets
   `Failed to load graph: Error: fetchWarrenGraph: 404`. Source: `web/src/main.ts` `loadGraph()`
   catch block (line ~319) only does `console.error`. Selecting `default` again recovers fully
   (`default (16)`, graph reloads). FAIL on surfacing: a failed graph load is silent to the
   user (relevant beyond this synthetic case — e.g., a warren whose repo dir vanishes between
   panel refreshes would fail the same silent way).

## Issues found

1. **Major (mobile)** — At 390x844 the ↻ refresh button is clipped and the `Warrens` button is
   entirely offscreen and unreachable (body overflow-x hidden): the new warren panel cannot be
   opened on a phone. Fix locus: `web/src/style.css` `.toolbar-left` doesn't wrap and
   `#namespace-select` is unconstrained; panel content itself is fine at 390 once open.
2. **Medium (error surfacing)** — `loadGraph()` failures (e.g., warren graph 404) are
   console-only; the UI silently keeps the stale graph with no toast/inline/panel error
   (`#warren-panel-error` unused for this path). `web/src/main.ts` catch at ~line 319.
3. **Minor (cosmetic/console)** — Every navigation/reload logs
   `[live-reload] SSE connection lost, will auto-reconnect` warning +
   `/api/events net::ERR_ABORTED`; harmless but pollutes the console.
4. **Observation** — No namespace/panel state persistence across reload (resets to `default`,
   panel closed); sane but arguably lossy.
5. **Observation** — Disabled `Warrens` group-header option in the selector can be force-selected
   programmatically and yields `Graph loaded: 0 nodes, 0 edges`; not reachable by real users.

False positives explicitly ruled out (do NOT act on these from the raw codex log):
d3 `Cannot read properties of null (reading 'document')` on node clicks, mobile detail-drawer
close failure, and the x=-84.5 panel cut-off — all reproduced only with synthetic events /
forced DOM manipulation; trusted-input re-tests were clean.

---

## Consolidated issues

Deduped across the three test passes, ranked by severity. All were confirmed with trusted
input events; codex-only artifacts (d3 `null.document` TypeError, mobile drawer-close
failure, x=-84.5 panel cut-off) are excluded as refuted false positives.

### C1 — Major (mobile): warren panel cannot be opened at 390x844
The `Warrens` toggle (`#warren-toggle`, rect x=409..475) is entirely offscreen and the
refresh button (`#refresh-btn`, right=399) is 9px clipped at viewport width 390; body has
`overflow-x: hidden` so neither is reachable. Panel content itself is fine at 390 once open.
Root cause: `web/src/style.css` — `.toolbar-left` (`flex: 0 0 auto`, non-wrapping) holds the
wide `#namespace-select` plus ↻ and Warrens and exceeds 390px.
**Repro:** open http://localhost:3311, resize viewport to 390x844, try to click `Warrens`.
Source: robustness Step 1.

### C2 — Medium (error surfacing): failed graph loads are silent
`loadGraph()` failures (e.g., warren graph 404) only `console.error`; the UI keeps the stale
previous graph with no toast/inline error, and `#warren-panel-error` is unused for this path.
Relevant beyond the synthetic case — a warren repo dir vanishing between refreshes fails the
same silent way. Fix locus: `web/src/main.ts` catch at ~line 319.
**Repro:** in devtools, set the namespace selector value to `_warren/nope-does-not-exist` and
dispatch `change`; graph stays stale, no visible error, console shows
`Failed to load graph: Error: fetchWarrenGraph: 404`. Source: robustness Step 5c.

### C3 — Minor (UX): per-warren refresh gives no success feedback
Clicking the per-warren `↻` (title `Reload warren state from disk`) works but shows no
spinner, toast, timestamp, or message — the user cannot tell the click did anything.
**Repro:** open the Warrens panel, click `↻` next to `demo-warren`; only the console
(`Graph loaded: ...`) shows activity. Source: panel Step 3 / panel Issue 1.

### C4 — Minor (staleness): namespace-selector label not refreshed after unmount
After unmounting proj-b the panel row flips to `dormant` but the selector option still reads
`Warren demo-warren (1 active, 1 identity)` while `/api/warrens` shows no active projects.
**Repro:** open panel, click `Unmount` on proj-b, inspect the selector option text vs.
`curl http://localhost:3311/api/warrens`. (Re-mount afterwards.) Source: panel Step 2 /
panel Issue 2.

### C5 — Minor (UX, detail panel): read-only warren nodes leave textareas editable
On `@pb-vault/...` (warren_mount) and `@ws-main/...` (local_alias) nodes the Summary/Context
textareas accept typing but the save path is blocked (`Read-only Warren Node`) — a silent
dead-end. `web/src/detail-panel.ts` (lines ~219–264) sets the save button to read-only mode
but never sets `readOnly`/`disabled` on `summaryArea`/`contextArea`.
**Repro:** switch selector to `Warren demo-warren (...)`, click `@pb-vault/calc/Total`, type
into Summary — input accepted, unsaveable, no feedback. Source: surfaces Step 2 / Issue 1.

### C6 — Minor (feature gap, chat): `/verify` never surfaces the active/superseded breakdown
`web/src/curator.ts:554-558` short-circuits the `verify` case with a hardcoded
`Health check complete. See Issues tab.` and never invokes the backend curator verify
command, whose result is exactly the expected `N node(s) (X active, Y superseded)` message
(`internal/curator/commands.go:637-651`).
**Repro:** send `/verify` in curator chat; only the static string and Issues-tab switch
appear. Source: surfaces Step 5 / Issue 2.

### C7 — Minor (cosmetic, console): SSE teardown noise on every navigation/reload
Each navigation/reload logs `[live-reload] SSE connection lost, will auto-reconnect` plus
`GET /api/events net::ERR_ABORTED` (EventSource aborted by page teardown). Harmless,
auto-reconnects, but pollutes the console.
**Repro:** hard-reload the page with devtools open. Source: robustness Steps 2/4 / Issue 3.

### C8 — Trivial (API shape): `/api/warrens` omits `active_projects` when empty
With all non-identity projects unmounted the key is dropped entirely (Go `omitempty`)
instead of returning `[]`, forcing clients to null-check.
**Repro:** unmount proj-b, `curl http://localhost:3311/api/warrens`. Source: panel Issue 3.

### Observations (recorded, not defects vs. spec)
- **O1** — No standalone identity-count badge in the collapsed selector and no numeric
  issue-count badge on the doctor header; counts live in the dropdown option text and the
  doctor list. (panel Issue 4)
- **O2** — Curator Issues tab stays scoped to the local vault (16 nodes) while the graph is
  in warren view; mounted read-only nodes are not health-checked from the UI. (surfaces
  Issue 3)
- **O3** — Namespace/panel state is not persisted across hard reload (resets to `default`,
  panel closed); sane recovery but lossy. (robustness Issue 4)
- **O4** — The disabled `Warrens` group-header option can be force-selected programmatically
  and yields `Graph loaded: 0 nodes, 0 edges`; not reachable by real users. (robustness
  Issue 5)
