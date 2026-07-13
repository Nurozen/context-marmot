# UI Validation 2 — Warren Panel + Surfaces (all three warrens)

Validated 2026-07-11 against **http://localhost:3311** (pid 37520, `v0.1.10-14-gc4711ac-dirty`,
left running). Method: `codex exec` with the computer-use skill (real browser input) in three
phases, cross-checked with independent `curl` calls after every mutating step, and every
FAIL/crash claim re-verified with trusted Playwright input (per the false-positive lesson from
pass 1). Raw transcripts: `$SCRATCH/ui-validate2/logs/codex-alpha.log`, `codex-beta.log`,
`codex-gamma-chat.log` (`$SCRATCH` = `/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad`).

Note: another validation session was driving the same browser concurrently near the end (a C2
bad-warren-id re-test — its 404 console errors and selector changes were observed and excluded
from this report's evidence).

## 1. warren-alpha

| # | Check | Expected | Observed | Result |
|---|---|---|---|---|
| A1 | Rows verbatim | proj-local identity w/ new EDITABLE rendering (not "no"); proj-b mounted; proj-c mounted+editable | Rows: `proj-b \| mounted \| no \| yes \| Unmount`, `proj-c \| mounted \| yes \| yes \| Unmount`, `proj-local \| identity \| local \| yes`. EDITABLE cell for proj-local = **`local`** with title/tooltip: `Edits happen normally in this workspace (live vault). Warren edit/propose applies only to foreign mounted projects.` | **PASS** (new fix confirmed) |
| A2 (C4) | Unmount/remount proj-b; namespace selector updates immediately, no reload | Unmount: row -> `dormant`/`Mount`; selector option updated immediately `Warren warren-alpha (2 active, 1 identity)` -> `(1 active, 1 identity)`; graph label `PROJ-B:DEFAULT` disappeared. curl `/api/warrens`: `active_projects:["proj-c"]`. Remount: selector immediately back to `(2 active, 1 identity)`, `PROJ-B:DEFAULT` reappeared; curl: `active_projects:["proj-b","proj-c"]`. | **PASS** |
| A3 (C3) | Refresh button shows visible success feedback | Toast appeared with exact text: `Warren "warren-alpha" reloaded from disk` | **PASS** |
| A4 | Bridges section lists both bridges | Entries: `proj-local ↔ proj-b (references)` and `proj-b ↔ proj-c (references)` | **PASS** |
| A5 | No console errors / layout breakage | Console: only `Graph loaded: 24 nodes, 29 edges` and `[live-reload] ...` logs; no errors; no cut-off/unclosable panels | **PASS** |

Note on A2: the selector has one option per warren (with active counts), not per-project options;
the immediate update is visible in the warren-alpha option label and the graph namespace labels.

## 2. warren-beta

| # | Check | Expected | Observed | Result |
|---|---|---|---|---|
| B1 | Dormant rows show Mount buttons | `proj-d \| dormant \| no \| yes \| Mount`, `proj-e \| dormant \| no \| yes \| Mount`; no Unmount buttons | **PASS** |
| B2 | Mount read-only proj-d; read-only affordances | Row -> `proj-d \| mounted \| no \| yes \| Unmount` (editable stays `no`); curl `/api/warrens`: warren-beta `active_projects:["proj-d"]` | **PASS** |
| B3 (C5) | Detail panel textareas readOnly + "enable writes" copy | Node `@pd-vault/src/calc_d`: real typing (`XYZ`/`ABC`) left both fields unchanged; DevTools: both textareas `"readOnly":true`. Visible copy: `Read-only Warren Node` / `Enable writes: marmot warren edit proj-d --warren warren-beta` | **PASS** |
| B4 | proj-e row shows materialized/burrow cache state | Dormant proj-e row shows only `dormant` — **no** cache/materialized indicator. Re-verified: source `web/src/warren-panel.ts:244` renders `mounted (burrow)` only for active rows; Playwright confirmed the indicator DOES appear when proj-e is mounted (`proj-e \| mounted (burrow) \| no \| yes \| Unmount`) and disappears again on unmount. API meanwhile reports `materialized:true` for the dormant project. | **FAIL (partial)** — see Issue 2 |
| B5 (C8) | Unmount restores dormant; `/api/warrens` emits `active_projects: []` | After unmount, curl: `"warren-beta":{... "materialized":true,"active_projects":[]}` — key present as an explicit empty array (re-confirmed independently twice) | **PASS** |
| B6 | Console errors | None; only graph/live-reload logs | **PASS** |

## 3. warren-gamma (checkout moved -> unreachable)

| # | Check | Expected | Observed | Result |
|---|---|---|---|---|
| G1 | Unreachable state surfaced in panel/status, not blank/silent | Panel block renders: `warren-gamma ↻ \| proj-g \| mounted \| no \| no \| Unmount`; the AVAILABLE `no` cell carries `.warren-unavailable` (red/danger styling). Playwright confirmed row/cell `title` attributes are empty — no tooltip, and no literal "unreachable" wording anywhere in the panel. | **PASS with note** — not silent, but no explicit "unreachable" badge (Issue 3) |
| G2 (C2) | Graph view shows a VISIBLE error, not console-only | Codex: selecting `Warren warren-gamma (1 active)` produced no banner/toast/inline error; console only `Graph loaded: 0 nodes, 0 edges`. **Independently re-verified with Playwright real select-option input**: zero toasts, zero banners, zero empty-state messages, zero console errors — a silently empty graph. API: `GET /api/warren/warren-gamma/graph` -> **HTTP 200** `{"namespace":"_warren/warren-gamma","nodes":[],"edges":[],"node_count":0,"edge_count":0}` with **no `skipped`/`skipped_reasons`**. | **FAIL** — see Issue 1 |

Root cause (code-confirmed): `internal/warren/warren.go:1858-1870` — when a warren's manifest is
unreadable and the warren is not materialized, `ActiveMounts` logs to **stderr only**
(`mounts skipped`) and `continue`s, so `handleWarrenGraph` never reaches its `markSkipped`
path (`internal/api/handlers.go:963-966`). The response is a clean 200 empty graph, leaving the
frontend nothing to raise a visible error from. The C2 toast does fire for *failed* loads (404
on unknown warren id — seen firing in the concurrent session's console), but the
unreachable-warren case bypasses it entirely.

## 4. Chat

| # | Check | Expected | Observed | Result |
|---|---|---|---|---|
| C-help | `/help` lists commands | Listed: `/help /tag /untag /type /merge /delete /link /unlink /verify` with descriptions | **PASS** |
| C-verify (C6) | `/verify` returns real active/superseded breakdown | UI + curl (`POST /api/chat`): `found 8 issue(s) across 16 node(s)`. No breakdown shown — **because all 16 workspace nodes are active** (verified: `GET /api/graph/default?include_superseded=true` -> `Counter({'active': 16})`). Code `internal/curator/commands.go:637-640` emits `(N active, M superseded)` only when superseded nodes exist; the all-active omission is explicitly asserted in `TestExecuteVerify_CountsSupersededSeparately` (`internal/curator/curator_more_test.go`), which passes (`go test` run live: PASS). Codex initially marked this FAIL; that was a false negative for this all-active dataset. | **PASS** (correct-by-design; breakdown path unit-verified) |

## 5. API cross-checks (every mutating step)

- A2 unmount: `active_projects:["proj-c"]`; remount: `["proj-b","proj-c"]` — matched UI.
- B2 mount proj-d: `active_projects:["proj-d"]`; B5 unmount: `active_projects:[]` — matched UI.
- B4 re-verify mount/unmount proj-e (via `POST /api/warren/warren-beta/{mount,unmount}`):
  `{"action":"mounted","projects":["proj-e"],"status":"reloaded"}` then back to `[]`.
- **C8 confirmed**: `GET /api/warrens` emits `"active_projects": []` (explicit empty array, never
  omitted) for the empty warren-beta in every observation.
- Final state = setup baseline: alpha `["proj-b","proj-c"]`, beta `[]`, gamma `["proj-g"]`.

## Issues found

1. **[MEDIUM] Unreachable warren graph view fails silently (C2 gap).** Selecting warren-gamma's
   graph shows an empty canvas with no toast/banner/empty-state and no console error.
   `ActiveMounts` (`internal/warren/warren.go:1868`) drops unreachable, non-materialized warrens
   with a stderr-only warning before `markSkipped` can annotate the response, so
   `/api/warren/{id}/graph` returns 200 with no `skipped_reasons`. Fix suggestion: have
   `handleWarrenGraph` consult `state.Warrens[id]` reachability (or surface the manifest error)
   and populate `skipped`/`skipped_reasons` or an `error` field so the UI can toast.
   Verified with real Playwright input (not a synthetic-event artifact).
2. **[LOW] Dormant materialized project shows no cache indicator.** proj-e's burrow cache
   (`materialized:true` in `/api/warren/warren-beta/status` even while dormant) is invisible in
   the panel until mounted — `web/src/warren-panel.ts:244` only renders `mounted (burrow)` on
   active rows, and there is no warren-level MATERIALIZED display (CLI `warren list` shows it).
   A user cannot tell from the UI that dormant proj-e has an offline-capable cache.
3. **[LOW] No explicit "unreachable" wording for warren-gamma in the panel.** The state is
   surfaced only as AVAILABLE `no` (red). Row/cell tooltips are empty. The CLI equivalent prints
   `warren "warren-gamma" UNREACHABLE at ... re-run 'marmot warren register ...'`; the panel
   gives no such hint or remediation.

Previous pass's 3 synthetic-event false positives (d3 `null.document` TypeError, drawer-close
failure, panel cut-off) did **not** reproduce with real input in any phase.

## Environment state at end

- UI server **left running**: pid 37520, `marmot ui --dir .marmot --port 3311 --no-open`.
- All mounts restored to setup baseline; the temporary `status: superseded` probe edit to
  `workspace/.marmot/go/render/Render.md` was reverted (file back to `status: active`).
- User's `marmot serve` processes and repo `.marmot` untouched.
