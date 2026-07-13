# UI Validation 3 — Environment Setup: Real OpenAI Embeddings

Date: 2026-07-13
Branch: `multiprocess-lock-fix` @ `c1a7c27`
Binary: `/Users/nurozen/Documents/GitHub/context-marmot/bin/marmot` (`v0.1.10-19-gc1a7c27-dirty`)
Scratch root: `/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad/ui-validate2`

## What changed

Switched the demo environment from mock to real OpenAI embeddings and restarted
the UI on port 3311 with the key in env (key sourced from `~/.zshrc`, never
logged or written to disk).

### Vaults switched to `embedding_provider: openai` (model left empty = provider default)

| Vault | vault_id | Reembed result | Wall time |
|---|---|---|---|
| `workspace/.marmot` | `ws-main` | 16/16 nodes, `openai/text-embedding-3-small` | ~2.8s |
| `warren-alpha/projects/proj-b/.marmot` | `pb-vault` | 4/4 nodes | ~1.1s |
| `warren-alpha/projects/proj-c/.marmot` | `pc-vault` | 4/4 nodes | ~1.2s |
| `warren-alpha/projects/proj-local/.marmot` | `ws-main` (burrow) | 16/16 nodes | ~1.4s |

`proj-local` was not in the original three-vault list but is a registered
warren-alpha project (a burrow of the workspace vault). Leaving it on mock kept
the D5 model-skew warning firing inside alpha, so it was switched and
reembedded too — see doctor output below.

Procedure: UI stopped first (`pkill -f "marmot ui --dir .marmot --port 3311"`,
only PID 72294; the user's two `marmot serve` processes untouched) so the B4
open-reader guard was not an issue; `marmot reembed --dir .marmot` used for all
four vaults (no `index --force` needed).

### Deliberately left on mock

- `warren-beta/projects/proj-d`, `proj-e` (dormant)
- `warren-gamma-moved/projects/proj-g` (warren unreachable at its registered path)

## Doctor (D5 model-skew) verification

Before switching proj-local, `warren doctor` in warren-alpha:

```
warning  model_skew  project "proj-b" embeddings use model(s) text-embedding-3-small
                     but project "proj-local" uses mock-v1; cross-project semantic
                     search between these will return no results
doctor: 0 error(s), 1 warning(s), 0 info
```

After switching proj-local:

```
Warren "warren-alpha" manifest looks healthy.
```

So D5 does NOT fire for alpha (all four alpha projects on
`text-embedding-3-small`). `warren doctor` in warren-beta also reports healthy —
its projects are uniformly mock, so no skew there either; the mock/openai split
across warrens is intentional and does not trip D5 within either warren.

## UI restart verification

Started from `workspace/` with `OPENAI_API_KEY` in env, log at
`.../ui-validate2/logs/ui.log`. Key log lines:

```
embedding: using openai/text-embedding-3-small
warren: 5 active project mounts (1 identity) loaded
classifier: using openai/gpt-5.5
chat: curator LLM provider wired
ContextMarmot UI server starting at http://127.0.0.1:3311
```

Expected (pre-existing, deliberate) warnings: warren-gamma manifest unreadable
at its registered path — mounts skipped, bridge policy not enforced for gamma.

- `GET /api/version` → `{"version":0,"app_version":"v0.1.10-19-gc1a7c27-dirty"}`
- `GET /` → 200

## Search sanity (real embeddings)

Local: `GET /api/search?q=authentication+token+refresh` → 10 results, top hits
`default/ts/client` (0.448), `default/ts/api` (0.446),
`default/ts/client/fetchUser` (0.444) — sensible semantic ranking (auth query →
client/api/fetchUser), scores in the ~0.42–0.45 band typical of
text-embedding-3-small cosine similarity (mock scores were near-degenerate).

Cross-vault: `GET /api/search?q=add+and+multiply+numbers+math+utility&ns=_warren/warren-alpha`
returns results from BOTH mounted vaults with provenance:

- `@pb-vault/src/mathutil_b` (0.472), `@pc-vault/src/mathutil_c` (0.469)
- `@pb-vault/src/mathutil_b/Doubleb` (0.462), `@pc-vault/src/mathutil_c/Doublec` (0.457)
- `@pb-vault/src/calc_b/Quadb` (0.455), `@pc-vault/src/calc_c/Quadc` (0.452)
- plus local alias `@ws-main/default/ts/format` (0.430)

Math-utility nodes from proj-b/proj-c outrank all local workspace nodes for the
math query — cross-vault semantic ranking works with same-model embeddings.

## Cost / latency observations

- Reembed total: 40 nodes across 4 vaults in < 7s wall (batched embedding
  calls; ~1–3s per vault including process startup).
- Cost: negligible — 40 short summaries + query embeds against
  text-embedding-3-small ($0.02 / 1M tokens) is well under $0.01.
- Search latency: plain local search ~0.28–0.32s end-to-end; cross-vault
  (`ns=_warren/warren-alpha`) ~0.75s (one OpenAI query-embed round trip plus
  per-mount store scans). Root page serve < 1ms.

## State at end

UI LEFT RUNNING at http://localhost:3311 (new binary, openai embeddings,
key in process env only). No changes to the repo's own `.marmot` or the user's
`marmot serve` processes. No git tree modifications.
