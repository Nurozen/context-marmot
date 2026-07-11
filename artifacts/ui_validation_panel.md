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
