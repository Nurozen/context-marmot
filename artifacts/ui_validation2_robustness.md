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
