# Spec: `marmot package-docs`

Bundle a `.marmot/` vault into a sharable, agent-native documentation archive.
Recipients drop the bundle into their project, point their agent at it via
MCP, and get read-only graph access immediately — no API key required for
basic browsing/traversal. Semantic search degrades gracefully to keyword
match when no API key is available.

## CLI

```
marmot package-docs [--dir .marmot] [--out vault.tar.gz] [--zip] [--include-heat] [--no-obsidian]
```

Flags:

- `--dir`        path to the source vault (default: discover via `.marmot/`)
- `--out`        output archive path (default: `<vault-id-or-dirname>.tar.gz`,
                 or `.zip` when `--zip` is set)
- `--zip`        emit a `.zip` archive instead of `.tar.gz`
- `--include-heat`  include `_heat/*.md` (excluded by default; usage telemetry
                    is local and rarely meaningful in another environment)
- `--no-obsidian`   exclude the entire `.obsidian/` directory (default: keep
                    the directory but strip user-specific workspace files)

Exit codes:
- 0 success
- 1 invalid args / vault not found / packaging error
- 2 vault unsuitable (no `_config.md`, empty embeddings, etc.)

## Bundle layout

The archive contains a `.marmot/` directory at the root with the following:

```
.marmot/
├── _package.md            ← NEW. Manifest with provenance + how-to-use.
├── _config.md             ← Sanitized: read_only=true, no inline keys.
├── _summary.md            ← Kept (shareable LLM-generated overview).
├── _bridges/              ← Kept (cross-namespace edge declarations).
│   └── *.md
├── _heat/                 ← Excluded by default (usage data, local).
├── <namespace>/_namespace.md   ← Kept.
├── **/*.md                ← All node files kept verbatim.
├── .obsidian/             ← Kept (no workspace files).
│   ├── app.json
│   ├── appearance.json
│   ├── core-plugins.json
│   ├── graph.json
│   └── community-plugins.json
│   ── workspace.json      ← STRIPPED (user-specific).
│   └── workspace-mobile.json ← STRIPPED.
└── .marmot-data/
    ├── embeddings.db      ← Kept (the whole point of the bundle).
    ├── .env               ← STRIPPED (secrets).
    ├── embeddings.db-wal  ← STRIPPED (transient SQLite files).
    └── embeddings.db-shm  ← STRIPPED.
```

## `_package.md` manifest format

```yaml
---
package_version: "1"
created: 2026-04-09T17:32:00Z
source_vault_id: backend-api      # may be empty if source vault had no vault_id
source_vault_path: /Users/.../backend-api/.marmot   # informational only
embedding_provider: openai
embedding_model: text-embedding-3-small
node_count: 287
edge_count: 932
namespaces:
  - auth
  - api
  - db
read_only: true
generator: "marmot package-docs vX.Y.Z"
---

# ContextMarmot Documentation Bundle

This is a packaged ContextMarmot vault. It contains pre-built embeddings and
read-only graph data for agentic documentation lookup.

## Drop-in usage

1. Extract the archive at the root of your project (creates `.marmot/`).
2. Run `marmot setup --read-only` to register the bundle with your agent.
3. Your agent can now call `context_query`, `context_verify`, and read tools.

## API key (optional)

Without a key, `context_query` falls back to lexical search across node
summaries and content. To enable semantic search, set `OPENAI_API_KEY` (or
`ANTHROPIC_API_KEY` if the bundle was built with Anthropic embeddings) and
restart your agent.

The bundle was built with `<provider>/<model>` — match that provider for
best results, or run `marmot reembed` to switch to your own provider.

## Read-only mode

In read-only mode the following MCP tools are disabled:
- `context_write`
- `context_tag` (write side)
- `context_delete`

This protects the bundle from accidental mutation and makes the contract
obvious to the agent.
```

## `_config.md` sanitization

Before packaging, the source `_config.md` is rewritten to:

- Strip any inline secret-bearing fields (defensive — they shouldn't be there
  to begin with, but check).
- Add `read_only: true` (parsed by the engine).
- Keep `embedding_provider` and `embedding_model` so query embedding can
  match if the recipient supplies a key.
- Keep `namespace` and `vault_id` (used for bridge resolution).

## Engine read-only mode

`internal/mcp/engine.go` gains:

```go
type Engine struct {
    // ...
    ReadOnly bool
}
```

Effects when `ReadOnly == true`:
- `context_write` returns `error: "vault is read-only"` (MCP error, not panic).
- `context_delete` returns same error.
- `context_tag` returns same error.
- `context_query` and `context_verify` work normally.
- File watcher / update engine remain inert (no source code paths to watch).

The flag is set from one of two sources, in order:
1. `_config.md` frontmatter `read_only: true` (highest priority — bundle-level).
2. CLI flag `marmot serve --read-only` (per-invocation override).

## Lexical fallback in `context_query`

When the engine has no embedder *with API key access*, `context_query`:

1. Logs `query: no embedder available; using lexical fallback` to stderr.
2. Tokenizes the query into lowercase words (split on whitespace + punctuation).
3. Scores each active node by:
   - +3.0 if any token appears in `summary`
   - +1.0 if any token appears in `context`
   - +0.5 if any token matches a tag
4. Returns top-K above a small threshold, then runs the normal BFS expansion
   and XML compaction.

This is a deliberate degradation — keyword match isn't as good as semantic,
but it's good enough to be useful and requires no setup.

## `marmot setup --read-only`

When run with `--read-only`, or when `setup` detects a `_package.md` with
`read_only: true`:

- The generated MCP config passes `--read-only` to `marmot serve`:
  ```json
  "args": ["serve", "--dir", "/path/to/.marmot", "--read-only"]
  ```
- Setup skips the `marmot configure` API-key prompt.
- A `Read-only documentation vault detected` notice is printed.

## Out of scope (v1)

- Signing / checksums of the bundle.
- Delta packaging (incremental updates).
- Bundle merging.
- Re-embedding during package step.
- Cross-vault bridges in bundles (bundles ship as standalone vaults; cross-
  vault routes need manual setup at the recipient's end).

## Test plan

- Round-trip: package a vault, extract to a temp dir, query it.
- Verify secret-stripping: `.env` and `workspace.json` MUST NOT be in the archive.
- Verify read-only enforcement: `context_write` returns error.
- Verify lexical fallback: query without API key returns sensible results.
- Verify graceful upgrade: query with API key uses semantic search.
- Verify zip + tar.gz both work.
