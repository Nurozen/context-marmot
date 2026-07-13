# UI Validation 2 — Graph Curator Chat (NL + slash commands)

Date: 2026-07-13 (updated same day after OpenAI quota was restored and the UI restarted with the new key — pid 72294)
Target: http://localhost:3311 (marmot ui, workspace `ui-validate2`, 16 nodes, mock embeddings, classifier openai/gpt-5.5)
Method: two codex exec computer-use sessions (logs: `scratchpad/ui-validate2/logs/codex-chat1.log`, `codex-chat2.log`), all behavior/mutation claims re-verified with direct Playwright input and direct `curl` against the API. Backend log: `scratchpad/ui-validate2/logs/ui.log`.

## History note

The first run was hard-blocked: the original `OPENAI_API_KEY` account had exhausted its quota (persistent 429 `insufficient_quota`; key valid, env reached the process, model `gpt-5.5` exists — full diagnosis retained in Issue 4 below). The user funded a new key, the UI was restarted with it, and steps 1, 2, 3, and 7 were re-run successfully. Results below are the final, unblocked results; UI behaviors validated during the outage (error surfacing, slash commands, hygiene) were re-confirmed where relevant.

## Per-step results

### 1. NL query: "what does the auth module do in this project?" — PASS
- Expected: real LLM answer grounded in the graph; progress indicator; latency noted.
- Observed: progress indicator shown (thinking dots, then `working...` with elapsed seconds and a Cancel button). Reply in ~17.9 s (server log: phase 1 2.5 s + phase 2 15.4 s). Answer verbatim (excerpt):
  > "I don't see an actual auth module in the search results. The closest matching cluster is a small TypeScript user API flow: it fetches a user, formats that user, and exposes a higher-level describeUser function. api — module ... Summary: Module api.ts ... Provides describeUser(id) ..."
- Grounded: correctly states no auth module exists and cites real nodes (`api`, `client`, `format`, `describeUser`, `fetchUser`, `formatUser`). A collapsible "Code" disclosure shows the sandbox code + result (code-mode transparency).

### 2. NL curation: "tag the auth-related nodes with domain security" — PASS with a significant quality finding (Issue 3)
- Observed: the curator **executed** (did not merely propose). Generated code: `client.search("auth authentication login session token password user")` → `client.tag(ids, ["domain","security"])`. Because the workspace uses mock embeddings, search returned **all 16 nodes**, and all 16 were tagged — despite no auth nodes existing (the model itself had just said so in step 1).
- Mutation UX is good: a "Changes (1)" banner with per-mutation `Undo` and `Undo all` appeared; `tagged 16 node(s) with "domain, security"`; sidebar Tags updated live. API cross-check confirmed the real mutation (`GET /api/graph/_all`: 16/16 nodes tagged `['domain','security']`).
- A control test "tag the store-related nodes with domain storage" behaved well: the generated code **did** apply an explicit relevance filter (`id/summary/context includes "store"`), tagged exactly 6 store-related nodes, and clicking the chat `Undo` reverted it (sidebar tag disappeared; verified).
- Cleanup: the auth-mutation was reverted via `/untag domain` + `/untag security` over all 16 nodes through POST /api/chat (responses: `removed tag "domain" from 16 node(s)`, `removed tag "security" from 16 node(s)`); final API state verified **0 tagged nodes** — demo state left exactly as found.
- Note: "domain security" was interpreted as two tags `domain` + `security`, not one `domain:security` tag — arguably reasonable given the phrasing, noted for awareness.

### 3. Node-ref pills in chat replies — PASS
- "show me the Store.Put node" (re-run with trusted Playwright input): reply is grounded (type `method`, namespace, status, summary, source path, lines 8–8, edges) and renders `Store.Put` / `Store` as clickable `.node-ref` chips (`data-node-id="go/store/Store.Put"`, pointer cursor, styled distinctly from plain text/code spans).
- Clicking the pill (Playwright click) opened the node detail panel for `go/store/Store.Put` — namespace, summary, context (`func (s *Store) Put(k, v string) { s.m[k] = v }`), source+hash, edges (0 out / 2 in), temporal. PASS.
- Pills in the Issues tab also work the same way (validated in the earlier session).

### 4. Slash commands alongside NL — PASS (unchanged from first run)
- `/` opens autocomplete (`/help /tag /untag /type /merge /delete /link /unlink` — still missing `/verify`, Issue 2). `/help` lists all 9 commands. `/verify` returns real backend counts: `found 8 issue(s) across 16 node(s)`, node count matches the API exactly. Slash `/untag` with selected_nodes also verified working during cleanup. Count-vs-Issues-tab discrepancy remains (Issue 1).

### 5. Error handling — PASS (unchanged from first run)
- Empty message: no-op. ~5000-char message: sent, no freeze. Nonsense query: handled. Backend errors (when they occurred) surfaced as visible chat error bubbles with the exact upstream message; input always recovered; no wedge or crash across both sessions.

### 6. Console hygiene + key leakage — PASS
- With the working key: **zero console errors and zero warnings** across the whole codex session and the Playwright session.
- New key does not leak: no `sk-` string in any of 6 inspected `POST /api/chat` response bodies (codex) nor in my direct curl response; served JS/CSS bundles previously grepped clean.

### 7. Varied grounded responses (OpenAI usage is real) — PASS
- Different questions produce different, graph-grounded answers:
  - "how many nodes and namespaces...?" → "16 nodes in 1 namespace (default)" (correct).
  - "what does the auth module do?" → no-auth-module answer citing the ts user-API cluster.
  - "which node has the most connections?" → "main, with 6 total connections: 5 outbound and 1 inbound", plus a correct runner-up list (store 4, client 4, api 3, Store 3).
- Server log confirms real per-turn OpenAI calls (`turn start, model=gpt-5.5`, distinct phase timings/byte counts per turn). No canned responses.

## Latency / quality

- End-to-end per message: **5.6–17.9 s** (typical ~7–10 s). Phase 1 (code generation) 2.3–5.0 s; phase 2 (answer synthesis) 2.3–15.4 s; sandbox execution 2–15 ms. Progress indicator with elapsed time + Cancel covers the wait well.
- Quality: answers are consistently grounded (real node IDs, correct counts/edges, honest "no auth module" negative), with the executed code inspectable via a collapsible disclosure. Main quality risk is Issue 3 (over-broad mutation).

## Issues found

| # | Severity | Issue | Status |
|---|----------|-------|--------|
| 1 | Medium | `/verify` reports "found 8 issue(s)... See Issues tab" but the Issues tab shows a different, smaller set (2 curator suggestions). The 8 integrity issues are not viewable anywhere in the UI. (internal/curator/commands.go:651 vs /api/curator/suggestions) | Open |
| 2 | Low | Slash-command autocomplete menu omits `/verify` even though `/help` lists it and it executes successfully. | Open |
| 3 | Medium | **NL curation over-applies on unmatched queries**: "tag the auth-related nodes with domain security" silently tagged ALL 16 nodes because `client.search(...)` under mock embeddings returns everything and the generated code applied tags with no relevance filter or empty-result guard — even though the same model had just concluded no auth module exists. Mitigated by the Changes/Undo banner, and partly an artifact of mock embeddings, but the phase-1 prompt should instruct the model to filter search hits (as it spontaneously did for the "store" ask) or confirm before bulk-tagging >N nodes. | Open (new) |
| 4 | Resolved | OpenAI account quota exhaustion (persistent 429 `insufficient_quota`) blocked all NL testing in the first run. Diagnosis: env var reached the process, provider wired, key valid (models list OK, gpt-5.5 present) — pure billing. Resolved 2026-07-13: user funded a new key, UI restarted, direct completion verified. | Resolved |
| 5 | Info | NL error surfacing is good: visible error bubble with exact backend message, input recovers, no silent failure, no key leakage. "domain security" is interpreted as two tags (`domain`, `security`) rather than one. | — |

## Cleanup / state

All mutations caused by this validation were reverted and verified: storage tags undone via the chat Undo button; the 16-node domain/security tagging reverted via `/untag` slash commands through the API; final state `16 nodes, 0 tagged` (matches pre-validation baseline). UI left running (pid 72294). Repo `.marmot` and `marmot serve` processes untouched.
