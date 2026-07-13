# Findings

## docs

This folder is documentation only (no code, no tests). It contributes user-facing contracts the fix plan must either preserve or consciously update, plus pointers to the e2e harness the test program should extend. Six sections below; anything not listed (benchmark.md, bridges.md, crud-classifier.md, current_limitations.md, data-structures.md, embedding-providers.md, implementation_plan.md, language_comparison.md, spec-*.md, typescript-sdk.md) has zero warren/daemon/flock content and is irrelevant.

### docs/warrens.md:104-137 (import copy semantics — Tier 1.2/1.3 constraint)
Docs already promise the hardened-copy behavior for **import** (regular-files-only, skip symlinks/device/socket, secret stripping, WAL/SHM exclusion). Note: docs say `embeddings.db-wal`/`-shm` are *excluded* — so a checkpoint-before-copy fix (Tier 1.2) is required for import to not silently drop un-checkpointed WAL data; excluding WAL without checkpointing loses data. The plan's `copyDir` hardening for burrow (Tier 1.3) should be documented here too once fixed; today the docs describe import only, silently implying burrow differs.

```text
114  Import copies regular files only, skips symlinks and device/socket files, strips
115  obvious inline secret fields or API-key-looking values from `_config.md`, and
116  always excludes transient or sensitive files:
117
118  - `.marmot-data/.env`
119  - `.marmot-data/embeddings.db-wal`
120  - `.marmot-data/embeddings.db-shm`
```

### docs/warrens.md:229-236 (burrow flag — Tier 3 constraint)
Docs document `burrow --materialize` as an explicit flag; the Tier 3 fix "burrow implies materialize" changes a documented CLI contract — this file must be updated in the same change.

```text
232  marmot warren burrow --materialize --warren product-platform project-b
```

### docs/warrens.md:189-236 (no inverse verbs documented — Tier 3 confirmation)
The "Consume a Warren" section documents register/list/mount/status/edit (`edit --off` exists as the only inverse) and burrow — no unmount/unregister/un-burrow anywhere, confirming review Tier 3 "missing inverse verbs". New verbs need doc sections here. Also note mount syntax takes explicit project args (`warren mount --warren <id> p1 p2`), relevant to the "mount-all needs --all" fix.

```text
207  marmot warren mount --warren product-platform project-a project-b
226  marmot warren edit --off --warren product-platform project-a
```

### docs/warrens.md:266-299 (write policy + MCP asymmetry — Tier 1.4 / Tier 3 documented behavior)
The editable+materialized write-loss bug (Tier 1.4) directly contradicts documented behavior: docs promise editable writes go "back to that project's own `.marmot/` vault and embedding database." The MCP vs API @-write asymmetry (Tier 3) is *intentionally documented*, so removing the asymmetry is a doc-visible contract change, not just a bug fix.

```text
277  When a Warren node is editable, API/UI updates write back to that project's own
278  `.marmot/` vault and embedding database. Read-only Warren nodes show provenance
...
281  MCP `context_write` does not accept `@vault-id/...` node IDs directly. Use the
282  Warren-aware API/UI path for editable mounted nodes, or write local nodes as
283  usual.
```

### docs/warrens.md:303-317 + docs/architecture.md:376-382 (freshness model — Tier 2/4 constraint)
Docs state materialized-cache-else-live-checkout resolution and per-project embedding DBs (no merge). Any remote-graph cache TTL / refresh design (Tier 2) and burrow commit-pinning (Tier 4.2) must keep this documented resolution order. architecture.md:380-381 ("Only active mounts are added to the engine... dormant") is the semantic the daemon Rebuild/reloadWarrenState work must preserve.

```text
warrens.md 315  If no materialized cache exists, Marmot reads the project directly from the
warrens.md 316  registered Warren checkout.
architecture.md 380  Only active mounts are added to the engine. Registered but unmounted projects are
architecture.md 381  dormant and do not participate in query or UI results.
```

### docs/development.md:21-42 (e2e harness pointers — test program)
Documents the existing e2e harness the warren test program should reuse: `e2e/` Go package builds the binary itself against `e2e/fixture/` (CLI, MCP stdio JSON-RPC, embedded UI over HTTP); Playwright suite is `web/e2e/ui.spec.ts` + `web/e2e/serve.sh` on a temp fixture copy. Make targets: `make e2e`, `make e2e-ui`, `make e2e-all`; integration tests via `go test -race -tags integration -count=1 ./internal/`. No warren coverage exists in these docs — consistent with review's "zero warren e2e today". Note: development.md says "all six tools" for MCP; the deferred-tool list shows six `mcp__context-marmot__*` tools, so this is accurate.

```text
23  The `e2e/` package exercises the built binary against a static fixture vault
24  (`e2e/fixture/`): CLI flows (index, status, query, verify, sdk, staleness
25  detection), the MCP server over stdio JSON-RPC (all six tools), and the
26  embedded web UI over HTTP.
```

### Review-accuracy check
warren_review.md cites no docs/ paths, so no line drift to flag. No contradictions found between the review's issue descriptions and the docs, except the nuance above: the review frames Tier 3 MCP @-write asymmetry as a UX gap, but warrens.md:281-283 documents it as intended behavior — the plan should treat it as a deliberate contract to change, and update docs accordingly. Docs mention no daemon, no MARMOT_ROUTES, no flock anywhere — daemon-era behavior (Tier 2) is entirely undocumented and will need new doc sections.
