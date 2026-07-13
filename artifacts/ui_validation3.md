# UI Validation 3 — Fixed Build + Real OpenAI Embeddings

Date: 2026-07-13
Target: http://localhost:3311 — `marmot ui` pid 7683, binary `v0.1.10-19-gc1a7c27-dirty`
(branch `multiprocess-lock-fix` @ c1a7c27 + uncommitted fixes), workspace `ui-validate2`,
embeddings `openai/text-embedding-3-small`, classifier/curator `openai/gpt-5.5`.
Environment prep: `artifacts/ui_validation3_setup.md`. Prior findings under test:
`artifacts/ui_validation2_chat.md` Issues 1–3 (Issues-tab mismatch, missing `/verify` in
autocomplete, NL curation over-apply).

Method: chat steps driven by a `codex exec` session (its in-app browser was unavailable this
run — discovery returned `[]` — so per explicit fallback authorization it drove headless
Chromium via scripted Playwright; scripts + full DOM/network evidence in
`scratchpad/ui-validate2/pw-work/`, log `scratchpad/ui-validate2/logs/codex3-chat-retry.log`).
Graph-view steps and every UI claim below were independently re-verified with trusted Playwright
(MCP) input and direct `curl` against the API. Views codex log:
`scratchpad/ui-validate2/logs/codex3-views.log`.

## Per-step results

### 1. Curation guard — PASS (both directions)

**Negative case** — chat: `tag the auth-related nodes with domain security`
- Expected: NOT bulk-tag the graph; narrow/no-op, low-relevance refusal, or a surfaced
  bulk-guard error.
- Observed (codex, verbatim reply): "**No auth-related nodes were tagged** — I didn't find any
  nodes matching the auth-related keywords strongly enough, so no nodes were tagged with
  `domain security`." The generated code (visible in the Code disclosure) now does exactly what
  the fixed phase-1 prompt instructs: `client.search(...)` → filter hits by `score >= top*0.8`
  **and** keyword-in-content → `matched: 0` → **no mutation call at all**. Result JSON listed all
  16 candidates with `"matched": 0`. No Changes banner; API cross-check: 0 nodes tagged. This is
  the same ask that silently tagged all 16 nodes in validation 2 (Issue 3) — fixed.
- The server-side bulk-mutation guard error string did not appear in chat because the model
  self-filtered before writing (the intended first line of defense). The guard backstop itself is
  verified by the new unit suite: `go test ./internal/codemode/ -run Bulk -race` → 6/6 PASS
  (`TestBulkGuard_BlocksWholeGraphMutation`, `_AllowBulkOptInSucceeds`,
  `_AtLimitAllowed_OverLimitBlocked`, `_AccumulatesAcrossCalls`,
  `_RepeatWritesToSameNodeDontAccumulate`, `_SmallGraphFloorOfFive`), run live during this
  validation.
- Latency: 15.6–19.7 s across two runs.

**Positive case** — chat: `tag the store nodes with domain storage`
- Observed: generated code applied the same score+keyword filter and tagged **exactly 6**
  store-related nodes: `go/store/Store`, `go/store`, `go/store/Store.Put`, `go/store/NewStore`,
  `go/main/main`, `go/store/Store.Get` (`tagged 6 node(s) with "domain storage"`, one tag —
  validation 2's two-tag split of the phrase did not recur). "Changes (1)" banner with per-change
  Undo + "Undo all"; sidebar Tags showed a `domain storage` chip (screenshot
  `pw-work/D-positive-before-undo.png`).
- Undo clicked: change entry struck through, sidebar back to "No tags"
  (`pw-work/D-positive-after-undo.png`); API cross-check after the session: **16 nodes, 0
  tagged** — demo left at baseline. Latency 15.4–17.5 s.

### 2. Issues tab shows curator suggestions AND integrity issues — PASS

- `/verify` in chat → `Health check complete: found 8 issue(s) across 16 node(s). See Issues
  tab.` (4.6 s).
- Issues tab (trusted Playwright DOM): badge **10**, tooltip **"2 curator suggestions + 8
  integrity issues"**; stats line `16 nodes · 2 suggestions · 8 integrity`; section headers
  **"Curator suggestions (2)"** and **"Integrity issues (8) across 16 nodes"**; 2 suggestion
  cards + 8 integrity cards.
- Counts match the chat message exactly (8 integrity across 16 nodes) and match
  `GET /api/curator/suggestions` (`suggestions:2`, `integrity_issues:8`,
  `integrity_node_count:16`). The 8 are all `dangling_edge · info` (stdlib/external targets like
  `fmt`, `strings/ToUpper`).
- Distinct styling confirmed via computed styles: integrity cards carry
  `issue-card integrity-card`, **dashed** left border, a `dangling edge · info` kind line, a type
  icon (⛓), node-ref pills, and **no** fix buttons; suggestion cards have a **solid** left
  border and Dismiss/fix actions. Validation-2 Issue 1 fixed.

### 3. Slash autocomplete lists /verify — PASS

Typing `/` in the chat input (trusted Playwright input event) shows all **9** commands:
`/help /tag /untag /type /merge /delete /link /unlink` and **`/verify` — "Run health check"**.
The old `.slice(0, 8)` cap that dropped the 9th entry is gone. Validation-2 Issue 2 fixed.

### 4. Group by project — PASS

- Warren-alpha view (`Warren warren-alpha (2 active, 1 identity)`): the Group-by dropdown now
  contains **"Group by project"**. Selecting it clusters the 24 nodes into exactly three hulls
  labeled **`local (ws-main)`** (16 nodes — the @ws-main identity aliases fold into the local
  group via the new `local_vault_id` from `/api/warrens`), **`pb-vault`** (4) and **`pc-vault`**
  (4). Screenshot: `scratchpad/ui-validate2/alpha-group-by-project.png`.
- Folder grouping no longer shows vault IDs as folders: in the same view the hull labels are
  **`pb-vault › src`**, **`pc-vault › src`**, **`ws-main › default`**, **`ws-main › go`** —
  genuine directories, vault-qualified so same-named folders from different vaults can't collide.
  In the plain local view (`default`) folder hulls are unqualified real directories: `default`,
  `go`.

### 5. All warrens aggregate — PASS

- New selector option **"All warrens (N active)"** (count = sum of active projects across
  warrens; showed `4 active` before the gamma proj-g restore, `5 active` after — see note
  below). Selecting it renders the aggregate: **32 nodes, 35 edges** = local 16 (the
  @ws-main aliases from warren-alpha correctly dedupe onto the local island) + alpha's mounts
  (@pb-vault 4, @pc-vault 4) + beta's active mounts (@pd-vault 4, @pe-vault 4). "Group by
  project" in this view yields five hulls: `local (ws-main)`, `pb-vault`, `pc-vault`,
  `pd-vault`, `pe-vault`. Screenshot:
  `scratchpad/ui-validate2/all-warrens-group-by-project.png`.
- warren-gamma unreachability is surfaced without blocking: loading All warrens fires the error
  toast (captured with a MutationObserver, verbatim) —
  `Warren "warren-gamma": 1 project(s) skipped from graph — proj-g: warren unreachable:
  manifest unreadable at …/warren-gamma: … no such file or directory` — while the graph loads
  and stays fully interactive (`Promise.allSettled` per warren; no error page). The Warrens
  panel proj-g row carries `warren-row-skipped` styling + the full skip-reason tooltip.
- Codex session 2 independently reproduced all of this (headless-Playwright evidence in
  `scratchpad/ui-validate2/pw-work2/`: `step-b-project.png` with exact 16/4/4 hull membership
  counts, `all-warrens-toast.png` capturing the gamma toast verbatim, `step-c-project.png`,
  `primary-report.json`; zero console errors/warnings/page errors). Its one flagged deviation —
  warren-alpha folder hulls labeled `ws-main › go` instead of bare `go` — is correct-by-design:
  in the warren view local nodes are @ws-main identity aliases, so their folders are
  vault-qualified like every other mount; the plain local view shows unqualified `default`/`go`
  (verified, step 4).
- **Demo-state note:** at session start warren-gamma's registration had drifted to
  `active_projects: []` (validation 2 had `["proj-g"]`), which made the unreachable warren
  completely silent (no toast, panel says "No projects in this warren.", workspace doctor
  clean). Restored `proj-g` to `active_projects` in the scratch workspace's
  `workspace/.marmot/_warren.md` to reinstate the documented scenario; left in place so the
  demo keeps demonstrating the C2 surfacing. See Issue 1 below for the underlying gap.

### 6. Real-embedding quality spot check — PASS

- Semantic ranking (direct API, three probes — clearly concept-ranked, not the mock's
  lexical-overlap behavior):
  - `user data fetching` → `default/ts/client/fetchUser` 0.505, `ts/client` 0.497, `ts/api`
    0.488, `ts/api/describeUser` 0.486 …
  - `uppercase text rendering` → `go/render/Render` 0.485 first.
  - `key value storage` → `go/store/Store` 0.481, `Store.Put`/`Store.Get` next.
  Latency: ~0.26–0.68 s per local search; cross-vault (`ns=_warren/warren-alpha`) 0.32 s.
- NL chat: `how is user data fetched in this codebase?` → grounded answer in 18.3–20.9 s citing
  `fetchUser`, `client`, `User`, `describeUser`, `api`, `formatUser` with correct file/line
  provenance (`ts/client.ts` lines 3–6, endpoint `/api/users/{id}`) and the correct flow
  `describeUser → fetchUser → /api/users/{id} → User → formatUser`; it correctly excludes the Go
  Store from the user-fetching path.

### 7. Console hygiene + key material — PASS

- Codex chat session: 0 console messages, 0 uncaught page errors, 0 failed network requests
  across all steps.
- Trusted Playwright session (all view/issue checks): 27 console entries, **0 errors, 0
  warnings** (all `Graph loaded …` / live-reload info logs).
- No `sk-` in any of 4 inspected `/api/chat` response bodies, nor in the served JS bundle,
  `/api/search`, or `/api/graph/_all` responses.

## Issues found

| # | Severity | Issue |
|---|----------|-------|
| 1 | Low | **Unreachable warren with zero registered active projects is completely silent.** The C2 fix keys skip-reasons off `entry.ActiveProjects`; when the registration has `active_projects: []` (as the drifted demo state did), `/api/warren/warren-gamma/graph` returns a clean empty 200, no toast fires, the panel shows only "No projects in this warren.", and `/api/doctor/workspace` reports no issue — the only trace is a server-stderr warning. A warren-level `unreachable` flag in `/api/warrens`/doctor (matching ui_validation2.md remaining-issue 2) would close this. |
| 2 | Info | The bulk-mutation guard error path never surfaces in chat when the model behaves (prompt-side filtering is the first line of defense); the guard itself is covered by 6 passing `-race` unit tests. Consider an e2e that forces an over-limit write through `/api/chat` code-mode to lock the UI surfacing of the guard error string. |
| 3 | Info | Error toasts expire after ~8 s; the all-warrens gamma toast can be missed if the user is not watching (persistent signal exists only as the panel row tooltip). |
| 5 | Info | The "All warrens (5 active)" label counts warren-gamma's registered-but-unreachable proj-g as "active" even though it contributes zero nodes; a "(1 unreachable)" qualifier would be more honest. |
| 4 | Info (positive) | "domain storage"/"domain security" are now applied as a single tag, resolving the two-tag split noted in validation 2 Issue 5. |

## Latency summary

| Operation | Latency |
|---|---|
| `/verify` slash command | 4.6 s |
| NL curation (negative, no-op) | 15.6–19.7 s |
| NL curation (positive, 6 tags) | 15.4–17.5 s |
| NL grounded question | 18.3–20.9 s |
| `/api/search` local / cross-vault | 0.26–0.68 s / 0.32 s |

## Cleanup / state

- All mutations reverted through the chat Undo button; verified via API twice: final state
  **16 nodes, 0 tagged** (baseline).
- Scratch demo change kept deliberately: warren-gamma `active_projects: ["proj-g"]` restored in
  `workspace/.marmot/_warren.md` (returns the demo to the documented validation-2 scenario).
- No repo source files modified by this validation; repo `.marmot` and the user's `marmot serve`
  processes untouched; no git tree modifications.
- UI **LEFT RUNNING** at http://localhost:3311 (pid 7683, real OpenAI embeddings, key in process
  env only).
