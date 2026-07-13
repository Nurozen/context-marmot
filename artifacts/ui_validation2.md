# Warren UI re-validation (3 warrens, post-fix)

Collated 2026-07-11 from `ui_validation2_setup.md`, `ui_validation2_panel.md`, and
`ui_validation2_robustness.md` (all reproduced verbatim in sections 2–4 below). Environment:
three warrens (warren-alpha rich/healthy, warren-beta all-dormant with a materialized burrow
cache, warren-gamma registered-but-moved/unreachable) served by `marmot ui` on
**http://localhost:3311**.

## 1. Verdict

**ALL FIXES VERIFIED — C1–C8 and the identity-EDITABLE rendering all FIXED-VERIFIED.**

The validators confirmed every fix except one gap: C2 (silent graph-load failure) was fixed for
the *failed*-load case (404 → error toast) but **still broken for its real-world case** — an
unreachable warren (checkout moved on disk, exactly the "warren repo dir vanishing" scenario the
original C2 writeup called out) returned a clean HTTP 200 empty graph with no `skipped_reasons`,
so the UI rendered a silently empty canvas (panel G2 FAIL; robustness finding N1, Medium).
That gap was **fixed during this collation pass** (see section 1.2), the binary rebuilt, the
17-test Playwright e2e suite rerun (all green, including a new unreachable-warren spec), the UI
server restarted with the new build, and the fix re-verified live via curl and Playwright
against http://localhost:3311.

### 1.1 Per-fix status

| Item | Original issue | Status | Evidence |
|---|---|---|---|
| C1 | Major (mobile 390x844): Warrens toggle offscreen, refresh clipped | **FIXED-VERIFIED** | Robustness §1: toggle right=380, refresh right=304, panel opens/closes with real taps, 0 console errors (codex + trusted Playwright) |
| C2 | Medium: failed graph loads silent (console-only) | **FIXED-VERIFIED** (second half fixed during collation) | 404 case: error toast `Failed to load graph … fetchWarrenGraph: 404` (robustness §3). Unreachable-warren case was STILL-BROKEN per panel G2 / robustness N1 — fixed now: `/api/warren/warren-gamma/graph` emits `skipped:["proj-g"]` + `skipped_reasons` (`warren unreachable: manifest unreadable at …`), UI shows `toast-error` `Warren "warren-gamma": 1 project(s) skipped from graph — proj-g: warren unreachable…`, and the proj-g panel row carries the skip tooltip + `warren-row-skipped` class. Verified live via curl + Playwright post-restart; regression-locked by Go test `TestWarrenGraphUnreachableWarrenReportsSkipped` and Playwright spec `unreachable warren graph surfaces skipped projects…` |
| C3 | Minor: per-warren refresh gave no feedback | **FIXED-VERIFIED** | Panel A3: toast `Warren "warren-alpha" reloaded from disk`; robustness §4: button `…`→`✓` + success toast (trusted clicks, twice) |
| C4 | Minor: namespace-selector label stale after unmount | **FIXED-VERIFIED** | Panel A2: unmount/remount of proj-b updated the selector option (`2 active`↔`1 active`) and graph labels immediately, no reload; curl matched at each step |
| C5 | Minor: read-only warren node textareas accepted typing | **FIXED-VERIFIED** | Panel B3: both textareas `readOnly:true` on `@pd-vault/src/calc_d`; real typing left fields unchanged; `Read-only Warren Node` + `Enable writes: marmot warren edit …` copy shown |
| C6 | Minor: chat `/verify` never surfaced active/superseded breakdown | **FIXED-VERIFIED** | Panel §4: `/verify` returns the real backend result (`found 8 issue(s) across 16 node(s)`); breakdown omitted only because all 16 nodes are active (correct-by-design, asserted by passing unit test `TestExecuteVerify_CountsSupersededSeparately`) |
| C7 | Minor: SSE teardown console noise on reload | **FIXED-VERIFIED** | Robustness §2: 2 hard reloads with full console capture — 0 errors, 0 warnings, no `SSE connection lost` line (`pagehide`/`beforeunload` close the EventSource) |
| C8 | Trivial: `/api/warrens` omitted `active_projects` when empty | **FIXED-VERIFIED** | Panel B5 + §5: empty warren-beta always emits `"active_projects": []` (explicit empty array, confirmed repeatedly) |
| editable | Identity row EDITABLE read as a confusing "no" | **FIXED-VERIFIED** | Panel A1: proj-local EDITABLE cell renders `local` with tooltip `Edits happen normally in this workspace (live vault)…`; foreign rows keep yes/no (also locked by the wui e2e spec) |

### 1.2 C2 completion fix applied during collation

Change set (branch `multiprocess-lock-fix`, working tree):

- `internal/api/handlers.go` — `handleWarrenGraph` now checks the warren's manifest when the
  warren is not materialized; if unreadable, every `ActiveProjects` entry is `markSkipped` with
  `warren unreachable: manifest unreadable at <path>: <err>`, so the 200 response carries
  `skipped`/`skipped_reasons` instead of pretending the warren is empty.
- `web/src/main.ts` — warren graph loads with a non-empty `skipped` list now raise an error
  toast naming the warren, the skipped projects, and the reasons (the existing
  `setSkippedReasons` panel-tooltip plumbing now finally receives data for this case).
- `internal/api/warren_warnings_test.go` — new `TestWarrenGraphUnreachableWarrenReportsSkipped`
  (moves a mounted warren's checkout away, asserts skipped + reason). `go test ./internal/api ./internal/warren` PASS.
- `web/e2e/serve.sh` — fixture grows an unreachable-warren leg (`wgone`/`ghost`, checkout moved
  after mount). `web/e2e/warren.spec.ts` — new spec asserts the error toast + panel skip tooltip
  and clean recovery; one existing C3 spec selector tightened to `.warren-block[data-warren="wui"]`
  (the new fixture warren sorts first). Playwright: **warren 10/10 PASS, vault 7/7 PASS**.
- Rebuilt `web/dist` (vite) + `make build`; UI server restarted with the new binary
  (**pid 54823**, same recipe/port/log). Live re-verification: curl shows `skipped_reasons` on
  `/api/warren/warren-gamma/graph`; Playwright against :3311 shows the error toast on selecting
  warren-gamma, the proj-g skip tooltip, and warren-alpha still loading 24 nodes with zero
  error toasts.

### 1.3 Consolidated remaining issues (non-C, deduped across panel + robustness)

1. **[LOW] Dormant materialized project shows no cache indicator** (panel B4/Issue 2). proj-e's
   burrow cache (`materialized:true` while dormant) is invisible until mounted; no warren-level
   MATERIALIZED display in the panel (CLI `warren list` shows it).
2. **[LOW] No explicit "unreachable" badge on the warren panel row before a graph load** (panel
   Issue 3 / robustness N1-remainder). Partially mitigated by the C2 completion fix: loading the
   warren's graph now toasts `warren unreachable…` and stamps the row tooltip; but the panel on
   its own still surfaces only AVAILABLE `no` (red) with no remediation hint (CLI prints
   `UNREACHABLE … re-run 'marmot warren register …'`).
3. **[MINOR] Per-warren refresh on an unreachable warren shows a success toast** (robustness N2):
   `POST /api/warren/warren-gamma/refresh` answers 200 `{"status":"reloaded"}` while stderr logs
   manifest-unreadable — misleading success on the exact failure the button diagnoses.
4. **[MINOR] After the C2 404-toast expires, the stale previous graph and bad selector value
   persist with no residual indicator** (robustness §3 residual).
5. **[TRIVIAL] Initial page load fetches the graph twice** (robustness N3): `init` calls
   `loadGraph()` and the EventSource `open` handler immediately reloads.
6. **Known O3 (not a regression):** namespace + panel-open state not persisted across reload;
   recovery itself is clean.

### 1.4 End state (verified live after restart)

- UI: **http://localhost:3311**, pid **54823**, `v0.1.10-14-gc4711ac-dirty` + working-tree C2 fix,
  log `$SCRATCH/ui-validate2/logs/ui.log`. `GET /api/version` 200.
- warren-alpha: `active_projects:["proj-b","proj-c"]`, `editable_projects:["proj-c"]`,
  `identified_projects:["proj-local"]` — fully populated, proj-c editable.
- warren-beta: `active_projects:[]`, `materialized:true`; burrow cache intact at
  `workspace/.marmot/.marmot-data/warrens/warren-beta/projects/proj-e/.marmot`.
- warren-gamma: registered path absent on disk (checkout at `warren-gamma-moved`) — unreachable,
  `active_projects:["proj-g"]`, graph view now loudly skipped.
- User's `marmot serve` processes (25237, 36147) and the repo `.marmot` untouched throughout.

---

## 2. Setup report (verbatim: `ui_validation2_setup.md`)

Note: the setup report's UI pid 37520 was superseded by the post-fix restart — current pid 54823.

# UI Validation Environment 2 — Setup

Built 2026-07-11 by the setup subagent. Root (`$ROOT` below):
`/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad/ui-validate2`
Binary: `/Users/nurozen/Documents/GitHub/context-marmot/bin/marmot`
(`v0.1.10-14-gc4711ac-dirty`, commit c4711ac). Full CLI transcripts in `$ROOT/logs/`
(`setup.log`, `warren-alpha.log`, `warren-beta.log`, `warren-gamma.log`, `doctor.log`, `api.log`).
The previous validation UI (old pid 98918 serving scratchpad/ui-validate) was killed via
`pkill -f "marmot ui --dir .marmot --port 3311"`; the user's two `marmot serve` processes
(pids 25237, 36147) were verified untouched before and after.

## Workspace — `$ROOT/workspace` (vault_id `ws-main`)
- 6 source files with cross-calls: `go/main.go` -> `go/store.go` (`NewStore`/`Put`/`Get`) and
  `go/render.go` (`Render`); `ts/api.ts` -> `ts/client.ts` (`fetchUser`) and `ts/format.ts`
  (`formatUser`).
- Vault `$ROOT/workspace/.marmot`, `embedding_provider: mock` (classifier: embedding-distance
  fallback), `vault_id` set via `marmot configure --vault-id ws-main --dir .marmot`.
- Indexed: total=16 added=16 errors=0.

## Seed projects (each mock-embedded, 2 Go files with a cross-call, indexed total=4)
| source dir | vault_id | used by |
|---|---|---|
| `$ROOT/proj-b-src` | pb-vault | warren-alpha proj-b |
| `$ROOT/proj-c-src` | pc-vault | warren-alpha proj-c |
| `$ROOT/proj-d-src` | pd-vault | warren-beta proj-d (read-only) |
| `$ROOT/proj-e-src` | pe-vault | warren-beta proj-e (materialized) |
| `$ROOT/proj-g-src` | pg-vault | warren-gamma proj-g |

## warren-alpha — `$ROOT/warren-alpha` (rich, healthy)
- `warren init --id warren-alpha`; imports: proj-local (from workspace vault, keeps ws-main),
  proj-b (pb-vault), proj-c (pc-vault); bridges proj-local->proj-b and proj-b->proj-c
  (relations: references).
- Registered in the workspace; register emitted the R2 identity note:
  `project "proj-local" ... matches this workspace's vault ID "ws-main" — served as your live vault`.
- Mounted proj-b and proj-c; `warren edit proj-c` -> editable.
- `warren status`: proj-b mounted/editable=false/available=true; proj-c mounted/**editable=true**/
  available=true; proj-local **identity** (path = workspace `.marmot`).
- **Expected panel**: identity chip on proj-local, proj-b mounted read-only, proj-c mounted +
  editable, two manifest bridges, doctor healthy.

## warren-beta — `$ROOT/warren-beta` (all dormant, policy + cache states)
- Two projects: proj-d (pd-vault), proj-e (pe-vault).
- `warren project set-readonly proj-d` -> D4 manifest policy (`readonly: true`, manifest v2):
  `Project "proj-d" is read-only for consumers`.
- `warren burrow --materialize proj-e` (CLI noted burrow implies materialize) -> mounted with
  materialized cache at `workspace/.marmot/.marmot-data/warrens/warren-beta/projects/proj-e/.marmot`
  (`burrow cache for "proj-e": cache from 2026-07-11T07:33:02Z`); then unmounted — cache kept.
- Read-only refusal exercised live: mounted proj-d, `warren edit proj-d` failed exit=1 with
  `warren edit: warren author marked project "proj-d" read-only; edits must go through the warren
  repository itself`; proj-d then unmounted to restore the all-dormant state.
- Final `warren status`: proj-d dormant, proj-e dormant, burrow cache for proj-e present.
- **Expected panel**: mount buttons only (nothing mounted); proj-d shows read-only policy and
  refuses edit if mounted+edited; proj-e shows materialized/burrow-cache state; warren row
  MATERIALIZED=true.

## warren-gamma — registered at `$ROOT/warren-gamma`, checkout MOVED (unreachable)
- `warren init --id warren-gamma`; imported proj-g (pg-vault); registered; mounted proj-g; then
  `mv warren-gamma warren-gamma-moved`.
- `warren status --warren warren-gamma` now prints:
  `warren "warren-gamma" UNREACHABLE at $ROOT/warren-gamma — re-run 'marmot warren register ...'
  or 'marmot warren unregister --warren warren-gamma'` plus the manifest-unreadable warning
  (status degraded to workspace state); proj-g mounted but **available=false**.
- **Expected panel**: warren-gamma surfaced as unreachable (C6-adjacent), proj-g mounted but
  unavailable.

## `marmot warren list` (workspace view)
```
WARREN_ID     REACHABLE  ACTIVE  EDITABLE  MATERIALIZED  IDENTITY
warren-alpha  true       2       1         false         proj-local
warren-beta   true       0       0         true          -
warren-gamma  false      1       0         false         -
```

## Doctors (see `$ROOT/logs/doctor.log`)
- `warren doctor --warren-dir $ROOT/warren-alpha` -> `Warren "warren-alpha" manifest looks healthy.` exit=0
- `warren doctor --warren-dir $ROOT/warren-beta` -> `Warren "warren-beta" manifest looks healthy.` exit=0
- `warren doctor --workspace --dir $ROOT/workspace/.marmot` -> exit=0,
  `doctor: 0 error(s), 0 warning(s), 1 info` (info self_identity for warren-alpha/proj-local);
  stderr carried the warren-gamma manifest-unreadable warning.

## UI server (LEAVE RUNNING)
- Started detached from `$ROOT/workspace`:
  `nohup marmot ui --dir .marmot --port 3311 --no-open > $ROOT/logs/ui.log 2>&1 & disown`
- **PID: 37520**, URL: **http://localhost:3311**. Log: `$ROOT/logs/ui.log`.
- `GET /api/version` -> `{"version":0,"app_version":"v0.1.10-14-gc4711ac-dirty"}` (answered after 1s poll).
- `GET /api/warrens` (full JSON in `$ROOT/logs/api.log`):
  - warren-alpha: `editable_projects: ["proj-c"]`, `active_projects: ["proj-b","proj-c"]`,
    `identified_projects: ["proj-local"]`
  - warren-beta: `materialized: true`, `active_projects: []`
  - warren-gamma: `active_projects: ["proj-g"]`, path points at the moved-away
    `$ROOT/warren-gamma` (unreachable on disk)
- `GET /api/doctor/workspace` -> one `info`/`self_identity` issue for proj-local, no errors/warnings.

---

## 3. Panel + surfaces report (verbatim: `ui_validation2_panel.md`)

Note: B4's FAIL and G2's C2 FAIL are addressed post-report — B4 remains open as remaining issue 1;
G2/C2 is fixed and re-verified (section 1.2).

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

---

## 4. Robustness report (verbatim: `ui_validation2_robustness.md`)

Note: N1 is fixed post-report (section 1.2); N2/N3 remain open (remaining issues 3 and 5).

# UI Validation 2 — Robustness / fix re-check (codex computer-use + Playwright verification)

Run 2026-07-11 by the robustness subagent against `marmot ui` (pid 37520,
`v0.1.10-14-gc4711ac-dirty`) at http://localhost:3311, environment per
`artifacts/ui_validation2_setup.md` (`$ROOT` =
`/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad/ui-validate2`).
Three `codex exec --dangerously-bypass-approvals-and-sandbox` computer-use passes
(transcripts: `$ROOT/logs/codex_run1_mobile.log`, `codex_run2_console.log`,
`codex_run3_errors.log`; prompts in `$ROOT/prompts/`), with every failure-shaped or
surprising claim re-verified by trusted Playwright input (real clicks/selects, console
capture). No mounts/unmounts performed; UI server left running.

## Verdict

**PASS with 3 new findings.** All three re-checked fixes hold under trusted input:

| Fix | Result |
|---|---|
| C1 (mobile 390x844: Warrens button offscreen, refresh clipped) | **FIXED** |
| C2 (silent graph-load failure) | **FIXED** (error toast) |
| C7 (SSE 'connection lost' + ERR_ABORTED console noise on reload) | **FIXED** (console clean) |
| C3 (per-warren refresh no feedback) — incidental re-check | **FIXED** (button `…`→`✓` + success toast) |

New findings (all verified, none refuted):

| ID | Severity | Summary |
|---|---|---|
| N1 | Medium | Unreachable warren (warren-gamma) renders a silently empty graph: no toast, no empty-state, no `skipped_reasons` → no skip tooltip; panel never says "unreachable" |
| N2 | Minor | Per-warren refresh on an unreachable warren shows success toast `Warren "warren-gamma" reloaded from disk` (server returns `{"status":"reloaded"}` while stderr logs manifest-unreadable) |
| N3 | Trivial | Initial page load fetches the graph twice (`loadGraph()` once directly, once from the EventSource `open` handler) — two `Graph loaded:` lines per load |

Known observations that still reproduce (not regressions): O3 (namespace + panel-open
state not persisted across reload; recovery itself is clean).

---

## 1. Mobile 390x844 — C1 re-test: PASS (codex + trusted Playwright)

Codex (real clicks, viewport verified `clientWidth=390`):
- `#warren-toggle` rect x=314.39, right=380 — fully inside [0,390]. `#refresh-btn` rect
  x=274.39, right=304.39 — not clipped. `body.scrollWidth=390` (no horizontal overflow).
- Real click on `#warren-toggle` opened `#warren-panel` (rect x=0, right=351, fits
  viewport). Headers visible: Warrens, warren-alpha↻, warren-beta↻, warren-gamma↻,
  Workspace doctor, Cross-vault bridges.
- All three project tables horizontally inside [0,390] (widest right=358.76 inside the
  351px panel scroll area, `clippedElements=[]`): proj-b mounted/no/yes, proj-c
  mounted/yes/yes, proj-local identity, proj-d + proj-e dormant with Mount buttons,
  proj-g mounted/no/no. Per-warren ↻ buttons all hit-testable.
- Real click on `#warren-panel-close` closed the panel (x=-351, opacity 0).
- Console during the whole mobile pass: 0 errors, 0 warnings.

Playwright trusted re-measure confirmed the same rects and a real-click panel open
(x=0..351). Note: codex reported the toggle text as `Warrens 0` — the `0` is the
`#warren-badge` doctor-issue span with `hidden` set (verified `badge.hidden=true`); it is
not visually rendered. Not a defect.

## 2. Console hygiene — full browse + C7 re-test: PASS

Codex sweep (desktop, CDP console+network capture): initial load, 2 hard reloads, panel
open, namespace switches (default → warren-alpha 24n/29e → warren-beta 4n/3e →
warren-gamma 0n/0e), 5 node clicks incl. `@pb-vault/...` and `@pc-vault/...` detail
panels, search `Store`/`fetchUser` + opening results, curator chat `/help`, per-warren
refresh on warren-alpha. **Zero console errors and zero console warnings** across the
session, except network-layer `GET /api/events net::ERR_ABORTED` entries captured via CDP
`Network.loadingFailed` on each reload.

Trusted Playwright verification of C7: loaded the page and hard-reloaded twice with full
console capture — **12 total messages, 0 errors, 0 warnings**; no
`[live-reload] SSE connection lost` line and no ERR_ABORTED in the console stream. The
CDP entry codex saw is the EventSource stream being torn down at navigation (visible only
in the Network domain, not the console) — inherent to SSE, not the C7 console noise.
**C7 fixed.** (Fix visible in `web/src/main.ts` ~262-283: `pagehide`/`beforeunload` close
the EventSource and suppress the warning.)

N3 (trivial): every page load logs `Graph loaded: 16 nodes, 15 edges` twice — `init`
calls `loadGraph()` and the EventSource `open` listener (`web/src/main.ts`) immediately
reloads again. Cosmetic double-fetch.

"All namespaces" option was not present in the selector in this environment (selector
options verified via Playwright: `default (16)`, group header `Warrens`, three
`_warren/...` entries) — noted for completeness, matches single-namespace vault.

## 3. Error surfacing — C2 re-test: PASS, with residuals

Bad warren id (the only UI-reachable repro; synthetic option injection per the original
C2 repro, run identically by codex and Playwright): selecting
`_warren/nope-does-not-exist` now shows a visible error toast
`Failed to load graph for "_warren/nope-does-not-exist": fetchWarrenGraph: 404`
(`toast toast-error`, role=alert, ~8s TTL; codex timed 0.5s→8.5s). Console carries the
matching `Failed to load graph: Error: fetchWarrenGraph: 404` + 404 resource error, and
nothing else. Switching back to warren-alpha recovers (`Graph loaded: 24 nodes, 29
edges`). The previous graph does remain rendered behind the toast — no longer *silent*
(C2's complaint), but after the toast expires the stale graph + bad selector value
persist with no residual indicator. Residual, minor.

N1 (medium) — warren-gamma, the real-world C2 analogue (warren dir moved on disk):
switching to `Warren warren-gamma (1 active)` renders **0 nodes, 0 edges with no toast,
no empty-state message, and no explanation**. Verified by both codex and trusted
Playwright: panel row `proj-g | mounted | no | no | Unmount` (available cell has class
`warren-unavailable` but text is just "no"); `row.title` = `""` — **no skip-reason
tooltip**; the word "unreachable" appears nowhere in the panel, while the CLI prints
`warren "warren-gamma" UNREACHABLE at ...`. Root cause confirmed server-side:
`GET /api/warren/warren-gamma/graph` returns
`{"namespace":"_warren/warren-gamma","node_count":0,"edge_count":0}` with **no
`skipped_reasons` key**, because `warren.ActiveMounts` (`internal/warren/warren.go:1868`)
drops all mounts of a non-materialized warren whose manifest is unreadable before the
graph handler's `markSkipped` (`internal/api/handlers.go:964`) can run — the
skip-tooltip plumbing (`web/src/warren-panel.ts:199,224-227`) never receives data for
exactly the warren that needs it. Meanwhile `/api/warren/warren-gamma/status` still
reports proj-g `active:true, available:false`, so the panel shows a live-looking row
with a clickable Unmount button.

## 4. Reload recovery + skip tooltips + unreachable-warren refresh

- **Reload with panel open** (codex + Playwright): page recovers cleanly — graph
  renders, 0 console errors/warnings, no SSE noise. Panel comes back **closed** and
  namespace resets to `default (16)` (known O3, not a regression). Codex's claim that a
  "WARRENS ✕ shell remained" was **refuted** by trusted measurement: after reload
  `#warren-panel` sits fully offscreen (x=-380, opacity 0, nothing visible); codex had
  read DOM text of the hidden element.
- **Skip-reason tooltips**: the only vault state that could produce `skipped_reasons`
  (warren-gamma/proj-g, unavailable) produces none (N1 above), so no tooltip renders
  anywhere; all six project rows have `title=""` (verified via Playwright). The tooltip
  code path is present but currently unreachable end-to-end. warren-alpha/beta rows
  correctly have no tooltip (nothing skipped).
- **Per-warren refresh feedback (C3) — fixed, but N2**: real click on warren-alpha's ↻
  gives button `…`→`✓`; real click on warren-gamma's ↻ (codex, re-confirmed by trusted
  Playwright click with a MutationObserver) shows
  `Warren "warren-gamma" reloaded from disk` (`toast toast-success`, ~5s) even though the
  warren is unreachable — `POST /api/warren/warren-gamma/refresh` answers HTTP 200
  `{"status":"reloaded","warren_id":"warren-gamma"}` in ~12ms while the server stderr
  logs `manifest unreadable ... (mounts skipped)`. Misleading success on the exact
  failure the button exists to diagnose.

## False positives refuted this pass

1. "WARRENS ✕ panel shell remains after reload" — refuted (offscreen hidden element).
2. "Warrens 0 badge" — refuted as visual defect (`hidden` span, not rendered).
3. codex run-2 "per-warren refresh shows no toast" — refuted (toast confirmed twice with
   trusted clicks; codex checked too late in run 2 and saw it in run 3).

## Environment left as required

- UI server pid 37520 still serving http://localhost:3311 (HTTP 200 verified after all
  passes). No mounts/unmounts/edits performed; user's `marmot serve` processes untouched;
  `.marmot` of the repo untouched. Playwright browser closed.
