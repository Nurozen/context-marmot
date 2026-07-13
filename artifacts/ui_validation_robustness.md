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
