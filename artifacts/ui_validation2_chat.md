# UI Validation 2 — Graph Curator Chat (NL + slash commands)

Date: 2026-07-13
Target: http://localhost:3311 (marmot ui pid 20783, workspace `ui-validate2`, 16 nodes, mock embeddings)
Method: codex exec computer-use session (log: `scratchpad/ui-validate2/logs/codex-chat1.log`), all crash/behavior claims re-verified with direct Playwright input and direct `curl` against the API. Backend log: `scratchpad/ui-validate2/logs/ui.log`.

## Headline: NL chat is hard-blocked by OpenAI quota exhaustion

Every natural-language message fails with:

```
LLM error (phase 1): openai: chat API error (429): You exceeded your current quota,
please check your plan and billing details. ...
```

Diagnosis performed (per instructions):
- `OPENAI_API_KEY` **does** reach the process (verified via `ps eww -p 20783`: `OPENAI_API_KEY=sk-proj-...`).
- Provider **is** wired: ui.log shows `classifier: using openai/gpt-5.5` and `chat: curator LLM provider wired`; per-turn log lines show `turn start, model=gpt-5.5`.
- Key is **valid** (not a 401): `GET https://api.openai.com/v1/models` with the same key succeeds (125 models) and `gpt-5.5` is in the list.
- The 429 is **`insufficient_quota`** (account/billing), not a transient rate limit: persistent across retries 20+ seconds apart and across 8+ requests over the session. Phase-1 fails in ~0.7–1.9 s.

Conclusion: not a code or config bug — the OpenAI account backing the key has no remaining quota/billing. Requires human action (fund/switch the OpenAI account, or reconfigure to Anthropic). Steps 1, 2, 3 (LLM half), and 7 are BLOCKED, not failed; UI-side behavior around the failure was still validated.

## Per-step results

### 1. NL query: "what does the auth module do in this project?"
- Expected: real LLM answer grounded in the graph, with progress indicator; latency noted.
- Observed (codex + Playwright re-verified): progress indicator (three "thinking" dots) shown, input/send disabled while pending; after ~0.7–1.9 s a **visible error bubble** rendered in the chat:
  `Chat request failed (500): LLM error (phase 1): openai: chat API error (429): You exceeded your current quota...`
  Input re-enabled and usable afterwards.
- Note: the workspace has no auth module (nodes are `go/store`, `ts/client`, etc.), so even a working LLM should have answered "no auth module found".
- Verdict: **BLOCKED (quota)** for the LLM answer; **PASS** for UI behavior (progress indicator works, error is visible not silent, no wedge).

### 2. NL curation: "tag the auth-related nodes with domain security"
- Expected: curator executes/proposes/explains; graph change verified via API.
- Observed: same visible 429 error. API cross-check `GET /api/graph/_all`: 16 nodes, **0 nodes tagged** before and after — no phantom mutation occurred. Sidebar still shows "No tags".
- Verdict: **BLOCKED (quota)**; **PASS** on the safety property (failed request mutated nothing).

### 3. Node-ref pills: "show me the login handler node"
- Expected: node references in the LLM reply render as clickable pills; clicking focuses the node.
- Observed: NL reply is the 429 error, so no chat-reply pills could be produced. However the pill component itself was exercised via the Issues tab: node pills (`go/main`, `go/render`, ...) render with pointer cursor, and clicking `go/main` (trusted Playwright click) opened the node detail panel (namespace, summary, source path+hash, edges 2 out/0 in, temporal info).
- Verdict: **BLOCKED (quota)** for chat-reply pills; pill rendering + click-to-focus **PASS** where testable.

### 4. Slash commands alongside NL
- Expected: /help and /verify work with real backend counts.
- Observed:
  - Typing `/` opens an autocomplete menu: `/help /tag /untag /type /merge /delete /link /unlink` — **`/verify` is missing from the menu** though `/help` documents it and it executes fine (Issue 2).
  - `/help`: full command list returned (9 commands incl. /verify).
  - `/verify` (UI, codex, and curl all agree): `Health check complete: found 8 issue(s) across 16 node(s). See Issues tab.` Node count 16 matches `/api/graph/_all` exactly.
  - Discrepancy: the Issues tab shows **2 issues** ("16 nodes · 2 issues", 88% curated — two "Small disconnected subgraph" suggestions, matching `GET /api/curator/suggestions`), while /verify reports **8**. Root cause (source-verified): `executeVerify` (internal/curator/commands.go:651) counts `verify.VerifyIntegrityScoped` integrity/staleness issues, a different set from the curator suggestions the Issues tab renders — yet the message says "See Issues tab", where those 8 issues are not visible anywhere (Issue 1).
- Verdict: **PASS** (commands work, counts real), with the two issues noted.

### 5. Error handling
- Empty message + Enter: no-op — no request, no message, no error; input stays usable. PASS.
- ~5000-char lorem message: request sent, UI did not freeze; visible 429 error returned. PASS.
- Nonsense query ("xyzzy plugh frobnicate the quux"): visible 429 error (would have gone to the LLM). PASS (as testable).
- After all cases the input still accepted typing; no crash/wedge (re-verified with Playwright: multiple sends in one session, panel and graph still responsive). **PASS**.

### 6. Console hygiene + key leakage
- Console over the whole session: **zero warnings**, and the only errors are the expected `Failed to load resource: ... 500 ... /api/chat` entries corresponding 1:1 to the failed NL sends (codex saw 7× 500 for its 7 POSTs + one 404 from its own GET probe of /api/chat, which is POST-only; my Playwright session saw exactly 1 error for 1 NL send). No unexpected console errors. PASS.
- API key: not present in either inspected `/api/chat` response body, in any same-origin network response codex scanned, or in the served JS/CSS bundles (`/assets/index-C_DvMRV3.js`, `index-BHfPWKUa.css` grepped for `sk-proj|sk-ant|OPENAI_API_KEY`: 0 hits). The 429 error text also does not echo the key. **PASS**.

### 7. Cross-check OPENAI usage is real (two questions → different grounded answers)
- Observed: cannot compare answer variance — every call fails at phase 1. What IS confirmed real: requests genuinely go to OpenAI (server-side per-turn logs `phase 1 done in 1.137s (err=...429...)`, error text is OpenAI's own quota message, and latency/behavior consistent with a live upstream call). No canned-response fakery detected — there are simply no successful responses to compare.
- Verdict: **BLOCKED (quota)**.

## Issues found

| # | Severity | Issue |
|---|----------|-------|
| 1 | Medium | `/verify` reports "found 8 issue(s)... See Issues tab" but the Issues tab shows a different, smaller set (2 curator suggestions). The 8 integrity issues are not viewable anywhere in the UI; the pointer to the Issues tab is misleading. (internal/curator/commands.go:651 vs /api/curator/suggestions) |
| 2 | Low | Slash-command autocomplete menu omits `/verify` even though `/help` lists it and it executes successfully. |
| 3 | Blocker (environment, not code) | OpenAI account behind `OPENAI_API_KEY` has exhausted its quota (persistent 429 `insufficient_quota`); all NL chat features untestable until the account is funded or the provider switched. Key is valid, env reaches the process, model `gpt-5.5` exists. |
| 4 | Info | NL error surfacing itself is good: visible error bubble, exact backend message shown, input recovers, no silent failure, no key leakage in the error. |

## Human action required
- [ ] Fund or replace the OpenAI account for `OPENAI_API_KEY` (or run `marmot configure` and switch the classifier provider to Anthropic), then re-run chat NL validation steps 1–3 and 7.
