# Documentation Bundles

`marmot package-docs` packs a `.marmot/` vault into a shareable, read-only archive. Recipients drop the bundle into any project, point their agent at it via MCP, and get agent-native documentation lookup with zero further setup.

## Why bundles

A ContextMarmot vault is markdown nodes plus a SQLite embedding index. Both are derived from your source code or hand-curated content, but together they're useful as agent-facing docs for *another* project — internal SDKs, shared infrastructure, library APIs, ADR archives. A bundle makes that shareable as a single file.

The recipient gets:

- The full graph: nodes, edges, tags, summaries, namespace summaries, bridge manifests
- Pre-built embeddings (no reindex needed)
- Read-only protection enforced at the engine level
- Lexical (keyword) search out of the box — no API key required
- Optional semantic search if they supply a key matching the bundle's embedding provider

## Build a bundle

```bash
# In the vault you want to share
cd path/to/your/project
marmot package-docs --out backend-api-docs.tar.gz
```

Output:

```
Packaged 287 nodes (932 edges, 3 namespaces) to backend-api-docs.tar.gz (1.2MB)
```

Flags:

| Flag | Default | Effect |
|---|---|---|
| `--dir <path>` | discover via `.marmot/` | Source vault directory |
| `--out <path>` | `<vault-id>.tar.gz` | Output archive path |
| `--zip` | `false` | Emit `.zip` instead of `.tar.gz` |
| `--include-heat` | `false` | Include `_heat/*.md` (local usage telemetry) |
| `--no-obsidian` | `false` | Exclude entire `.obsidian/` directory |

## What gets stripped

The packager always removes:

- `.marmot-data/.env` — API keys, never shipped
- `.marmot-data/embeddings.db-wal`, `.db-shm` — transient SQLite files
- `.obsidian/workspace.json`, `workspace-mobile.json` — user-specific layout
- Any inline secret-bearing fields in `_config.md` (defensive; they shouldn't be there)

The packager always rewrites `_config.md` to set `read_only: true`.

By default the packager also drops `_heat/` (local usage data isn't meaningful in another environment). Pass `--include-heat` to keep it.

## What's added

The packager generates a `_package.md` manifest at the archive root:

```yaml
---
package_version: "1"
created: 2026-05-08T17:32:00Z
source_vault_id: backend-api
source_vault_path: /Users/.../backend-api/.marmot
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
```

The manifest is informational (the engine reads `read_only` from `_config.md`, not from `_package.md`), but it gives the recipient and any tooling a clear provenance signal.

## Use a bundle (recipient side)

```bash
cd your-project
tar -xzf backend-api-docs.tar.gz       # extracts to .marmot/
marmot setup --read-only               # writes MCP configs
```

`marmot setup` auto-detects `_package.md` and configures the agent's MCP entry as read-only without prompting for an API key. The generated config passes `--read-only` to `marmot serve`:

```json
{
  "mcpServers": {
    "context-marmot": {
      "command": "/usr/local/bin/marmot",
      "args": ["serve", "--dir", "/path/to/.marmot", "--read-only"]
    }
  }
}
```

When the agent calls a write-side tool, the server returns:

```
vault is read-only; writes are disabled
```

`context_query` and `context_verify` continue to work normally.

## Search behavior

| Recipient state | `context_query` behavior |
|---|---|
| No API key set | Lexical fallback — keyword scoring over node summaries, context, and tags |
| Key matching bundle's provider | Full semantic search (the embedded vectors line up) |
| Key for a different provider | Lexical fallback (vector spaces are incompatible) |

The lexical fallback logs `query: no embedder available; using lexical fallback` to stderr once on first use, then runs silently. It tokenizes the query (lowercase, drop stopwords), scores each node by:

- +3.0 if any token appears in the summary
- +1.0 if any token appears in the context
- +0.5 per matching tag

Top-K seed nodes feed into the same BFS expansion + token-budget compaction as the semantic path, so result formatting is identical.

## Switching to your own embedding provider

If you'd rather query semantically with a different provider than the bundle was built with, re-embed locally:

```bash
# Edit .marmot/_config.md to your preferred embedding_provider/embedding_model
marmot configure
marmot reembed
```

This rebuilds `embeddings.db` from the existing markdown nodes — no source data is lost. The vault stays read-only (the `read_only: true` flag persists).

## Caveats

- **Bundles are snapshots.** Updates require re-packaging and re-distributing. There's no incremental update mechanism in v1.
- **No signing or checksums.** Treat a bundle the same as a git repo from a third party — read the contents before pointing your agent at it.
- **No cross-vault bridges in bundles.** Bundles ship as standalone vaults. If the source vault had cross-vault bridges, those manifests are still inside but won't resolve unless the recipient also has the other vaults.
- **API key still useful.** Lexical fallback is real but coarser than semantic search. For best results on a bundle that will see heavy use, supply a key matching the bundle's provider.

## See also

- [Spec: package-docs](spec-package-docs.md) — full design
- [Bridges](bridges.md) — namespace + cross-vault bridges (related but separate)
- [Embedding Providers](embedding-providers.md) — provider configuration
