<p align="center">
  <img src="assets/hero.png" alt="ContextMarmot — a marmot overlooking a glowing underground knowledge graph" width="100%">
</p>

<h1 align="center">
  <img src="assets/logo.png" alt="ContextMarmot logo" width="48" height="48" valign="middle">
  ContextMarmot
</h1>

<p align="center">A graph-based memory engine for agentic systems.</p> Agents query and write to a persistent knowledge graph via [MCP](https://modelcontextprotocol.io/), getting structured context instead of reading raw files.

```
Agent  -->  MCP Server  -->  Embed + Search + Traverse + Compact  -->  XML context
```

Nodes are Obsidian-compatible markdown files with YAML frontmatter and `[[wikilinks]]`. Open the vault in Obsidian or use the built-in web UI for graph visualization.

## Features

- **Graph-based context** --- nodes connected by typed, directed edges (structural and behavioral)
- **Structural acyclicity** --- `contains`, `imports`, `extends`, `implements` edges enforce DAG structure; `calls`, `reads`, `writes`, `references` edges allow cycles (mutual recursion is real)
- **Semantic search** --- embedding index for natural language queries with graph traversal expansion
- **Token-budget compaction** --- results fit your context window, with full/compact/truncated tiers
- **MCP server** --- 5 tools (`context_query`, `context_write`, `context_tag`, `context_verify`, `context_delete`) over stdio
- **Domain tags** --- many-to-many semantic categorization for nodes; bulk-tag via search query; graph clustering and filtering by tag
- **Obsidian-compatible** --- every node file renders natively in Obsidian with working graph view
- **Integrity verification** --- hash-based staleness detection, dangling edge checks, cycle detection
- **Temporal node lifecycle** --- soft-delete and supersede nodes; active-only queries by default with `include_superseded` opt-in
- **CRUD classifier** --- `context_write` classifies incoming nodes as ADD / UPDATE / SUPERSEDE / NOOP using embedding similarity + optional LLM; falls back to embedding-distance thresholds when no LLM is configured
- **Multi-namespace** --- project isolation with namespace directories, bridge manifests for cross-namespace edges, qualified ID resolution
- **Heat map** --- co-access frequency tracking with exponential decay and floor; hot edges get traversal priority within budget constraints
- **Summary engine** --- auto-generates `_summary.md` per namespace using LLM; async scheduler regenerates on significant node changes; graceful degradation when LLM unavailable
- **Update engine** --- detects source file changes via hash comparison, propagates staleness through reverse edges, reindexes affected nodes; file watcher mode for continuous operation
- **Concurrent-safe** --- namespace-level write locks let multiple agents safely share a vault; reads are lock-free
- **Static analysis indexer** --- `marmot index <path>` parses Go (full AST), TypeScript (regex-based), and 30+ other languages into graph nodes with typed edges; incremental mode skips unchanged files; respects `.gitignore`
- **Single binary** --- Go, zero CGo, zero runtime dependencies

## Quick Start

### Prerequisites

- Go 1.21+ (`brew install go` or [go.dev/dl](https://go.dev/dl/))
- Node.js 20.19+ (or 22.12+) only if building/running the web UI (`marmot ui`)
- (Optional) An [OpenAI API key](https://platform.openai.com/api-keys) for semantic search. Without one, the mock embedder provides lexical-overlap search.

### Build

```bash
git clone https://github.com/nurozen/context-marmot.git
cd context-marmot
make build
```

This produces `bin/marmot`.

If you also want the embedded web UI:

```bash
make build-full
```

### Try the demo

```bash
# Seed a demo vault with 8 nodes (auth + database graph)
cd testdata/demo
bash seed.sh

# Index nodes into the embedding store
../../bin/marmot index --dir .marmot

# Query the graph
../../bin/marmot query --dir .marmot --query "user authentication login"

# Verify integrity
../../bin/marmot verify --dir .marmot

# Launch graph UI
../../bin/marmot ui --dir .marmot --no-open

# Open in Obsidian (optional --- open testdata/demo/.marmot as a vault)
```

Then open [http://localhost:3274](http://localhost:3274).

### Use in your own project

```bash
cd your-project

# Initialize vault, configure embeddings, and set up MCP configs --- all in one step
marmot init
```

`marmot init` runs three stages:
1. Creates the `.marmot/` vault directory
2. **`configure`** --- prompts for embedding provider, model, API key, and CRUD classifier (provider + model)
3. **`setup`** --- detects your tools (Claude Code, Codex, VS Code, Cursor) and writes MCP configs

After init, write nodes and index:

```bash
marmot index
```

Agents automatically connect to the MCP server --- no manual server start needed.

### Manual setup (if needed)

Run `marmot configure` or `marmot setup` individually at any time:

```bash
# Re-configure embedding provider/model/key
marmot configure

# Regenerate MCP configs (e.g., after installing a new tool)
marmot setup

# Target a specific tool
marmot setup --claude
marmot setup --codex
marmot setup --vscode
marmot setup --cursor
```

Once connected, agents get five tools:

| Tool | Description |
|------|-------------|
| `context_query` | Search the graph by natural language. Returns XML-compacted subgraph within token budget. |
| `context_write` | Write or update a node. Enforces structural acyclicity. Updates embedding index. Accepts optional `tags` for domain categorization. |
| `context_tag` | Bulk-tag nodes by semantic search query. Finds nodes matching a query and applies the given tags. |
| `context_verify` | Check node staleness, dangling edges, and structural integrity. |
| `context_delete` | Soft-delete (supersede) a node. Marks it `status: superseded` with a `valid_until` timestamp. Excluded from future queries by default. |

### Example: context_query

```json
{"query": "user authentication", "depth": 2, "budget": 4096}
```

Returns:

```xml
<context_result tokens="948" nodes="7">
  <node id="auth/login" type="function" depth="0">
    <summary>Authenticates a user with email and password...</summary>
    <edges>
      <edge target="auth/validate_token" relation="calls"/>
      <edge target="db/users" relation="reads"/>
    </edges>
    <context language="typescript">...</context>
  </node>
  <node_compact id="db/users" type="function" depth="1">
    <summary>Queries the users table...</summary>
  </node_compact>
  ...
</context_result>
```

### Example: context_write

```json
{
  "id": "auth/refresh",
  "type": "function",
  "tags": ["auth", "security"],
  "summary": "Refreshes an expired JWT token",
  "context": "func refreshToken(old string) (string, error) { ... }",
  "edges": [{"target": "auth/validate_token", "relation": "calls"}]
}
```

### Example: context_verify

```json
{"check": "integrity"}
```

## Embedding Providers

ContextMarmot supports pluggable embedding providers for semantic search. Out of the box, two providers are available:

| Provider | Models | Description |
|----------|--------|-------------|
| `mock` (default) | `mock-v1` | Character trigram hashing. No API key needed. Lexical overlap only --- texts must share substrings to match. |
| `openai` | `text-embedding-3-small` (default), `text-embedding-3-large`, `text-embedding-ada-002` | Real semantic embeddings via the OpenAI API. Paraphrases and synonyms match. |

### Configuring OpenAI embeddings

The easiest way is the interactive configure command (also runs automatically during `marmot init`):

```bash
marmot configure
```

This prompts for provider, model, and API key. The key is stored in `.marmot/.marmot-data/.env` (gitignored).

**Manual configuration** --- edit `.marmot/_config.md` frontmatter directly:

```yaml
---
version: "1"
namespace: default
embedding_provider: openai
embedding_model: text-embedding-3-small
---
```

Then set your API key via environment variable:

```bash
export OPENAI_API_KEY=sk-...
```

After changing the embedding provider or model, rebuild embeddings:

```bash
marmot index --force
```

### Fallback behavior

If `embedding_provider` is set to `openai` (or another non-mock provider) but the required API key is not found in the environment, ContextMarmot logs a warning to stderr and falls back to the mock embedder automatically. This ensures the tool never fails to start due to a missing key.

## CRUD Classifier

When an agent calls `context_write`, ContextMarmot automatically classifies the write before persisting:

| Action | Meaning |
|--------|---------|
| **ADD** | New concept — no similar node exists |
| **UPDATE** | Enriches or corrects an existing node (same concept, better content) |
| **SUPERSEDE** | Replaces an existing node — old node is soft-deleted, chain preserved |
| **NOOP** | Content is essentially identical to an existing node — skipped |

Classification uses embedding similarity to find candidates, then optionally an LLM to decide. Without an LLM, pure embedding distance thresholds are used (NOOP ≥ 0.95, UPDATE ≥ 0.80, SUPERSEDE ≥ 0.65, else ADD).

### Configuring the classifier

Run `marmot configure` (also runs during `marmot init`) — it prompts for classifier provider after the embedding section:

```
CRUD Classifier:
  > 1) openai   (gpt-5.1-codex-mini)
    2) anthropic (claude-haiku-4-5-20251001)
    3) none     (embedding-distance fallback)
```

If you select **openai** and your embedding provider is also OpenAI, the same `OPENAI_API_KEY` is reused — no second key needed. Selecting **anthropic** prompts for `ANTHROPIC_API_KEY` separately. Selecting **none** uses the embedding-distance fallback with no API calls.

**Manual configuration** — edit `.marmot/_config.md` frontmatter directly:

```yaml
classifier_provider: openai
classifier_model: gpt-5.1-codex-mini
```

### Fallback behavior

If the configured classifier provider's API key is not found at serve time, ContextMarmot logs a warning to stderr and falls back to embedding-distance classification automatically.

## CLI Reference

| Command | Description |
|---------|-------------|
| `marmot init [--dir .marmot]` | Create a new vault, run configure, then setup |
| `marmot configure [--dir .marmot]` | Interactive prompt for embedding provider, model, API key, and CRUD classifier |
| `marmot setup [--dir .marmot] [--claude] [--codex] [--vscode] [--cursor]` | Generate MCP configs for detected (or specified) tools |
| `marmot index [--dir .marmot] [--force] [<path>] [--incremental]` | Index node files into the embedding store. With `<path>`: run static analysis indexer on source code directory. `--incremental` skips unchanged files. `--force` clears and rebuilds all embeddings. |
| `marmot query --query "..." [--dir .marmot] [--depth 2] [--budget 4096]` | Query the knowledge graph |
| `marmot verify [--dir .marmot]` | Run integrity and staleness checks |
| `marmot serve [--dir .marmot]` | Start the MCP server on stdio |
| `marmot status [--dir .marmot]` | Show vault stats: node counts, edges, embeddings, stale nodes, namespaces, heat map |
| `marmot watch [--dir .marmot]` | Start file watcher for auto-reindex on source changes |
| `marmot bridge <ns-a> <ns-b> [--relations ...] [--dir .marmot]` | Create bridge manifest between two namespaces |
| `marmot summarize [--namespace ...] [--dir .marmot]` | Force summary regeneration for a namespace |
| `marmot reembed [--dir .marmot]` | Regenerate all embeddings (use after changing provider/model) |
| `marmot ui [--dir .marmot] [--port 3274] [--no-open]` | Start HTTP server for the embedded graph visualization UI |

## Node Format

Nodes are markdown files with YAML frontmatter:

```markdown
---
id: auth/login
type: function
namespace: default
status: active
tags:
  - auth
  - security
source:
  path: src/auth/login.ts
  lines: [1, 35]
  hash: a3f8b2c1
edges:
  - target: auth/validate_token
    relation: calls
  - target: db/users
    relation: reads
---

Authenticates a user with email and password.

## Relationships

- **calls** [[auth/validate_token]]
- **reads** [[db/users]]

## Context

// typescript
export async function login(email: string, password: string) { ... }
```

### Edge types

**Structural** (acyclic --- enforced):
`contains`, `imports`, `extends`, `implements`

**Behavioral** (cycles allowed):
`calls`, `reads`, `writes`, `references`, `cross_project`, `associated`

## Architecture

```
cmd/marmot/              CLI (init, configure, setup, index, query, serve, verify)
internal/
  config/                Vault config parser, .env key storage, embedder + classifier factory
  node/                  Markdown parser/writer, atomic file I/O, temporal fields
  graph/                 In-memory graph, adjacency lists, cycle detection, active-node index
  verify/                Hash integrity, staleness, structural + temporal chain checks
  embedding/             SQLite store, KNN search, OpenAI + mock embedders
  traversal/             BFS traversal, token-budget XML compaction, superseded-node filtering
  llm/                   LLM provider interface, OpenAI + Anthropic + mock implementations
  classifier/            CRUD classifier: embedding search + LLM path + distance fallback
  namespace/             Namespace manager, bridge manifests, qualified ID resolution
  summary/               Namespace summary generation, async scheduler, _summary.md I/O
  update/                Source change detection, staleness propagation, reindex, file watcher
  indexer/               Static analysis indexer: Go AST, TypeScript regex, generic file, ignore patterns, runner
  mcp/                   MCP server (5 tools), engine wiring, cross-namespace edge validation
```

See [docs/architecture.md](docs/architecture.md) for the full system design, and [docs/data-structures.md](docs/data-structures.md) for format specifications.

## Development

```bash
make build         # Build binary to bin/marmot
make test          # Run all tests with race detector
make test-v        # Verbose test output
make test-cover    # Generate coverage report
make vet           # Run go vet
make fmt           # Format code
make tidy          # Tidy go.mod
```

Run integration tests:

```bash
go test -race -tags integration -count=1 ./internal/
```

## Current Status

**MVP complete.** Evaluated on [SWE-QA](https://huggingface.co/datasets/swe-qa/SWE-QA-Benchmark) — 20 code comprehension questions across django, flask, pytest, and requests, judged by Claude on correctness, completeness, and specificity (1–5):

| Metric | Vanilla (file tools) | Hybrid (ContextMarmot) | Improvement |
|--------|---------------------|----------------------|-------------|
| Answer quality (1–5) | 4.62 | 4.62 | **identical** |
| Tokens per question | 151,327 | 95,876 | **−37%** |
| Cost per question | $0.1065 | $0.0834 | **−22%** |
| Avg turns | 7.5 | 6.9 | −8% |

Same quality. Lower cost. The graph acts as a navigation map — agents query it first, then read only the files and line ranges it identifies, skipping broad exploration. On hard multi-file questions the savings are largest (up to 50% fewer turns).

Evaluated with Claude Sonnet and OpenAI `text-embedding-3-small`. See [docs/benchmark.md](docs/benchmark.md) for full per-question breakdown and methodology.

### MVP scope (implemented)

- Node store with Obsidian-compatible markdown
- In-memory graph with structural/behavioral edge classification
- Embedding index with KNN search (Go-side, SQLite-backed)
- Pluggable embedding providers (OpenAI and mock) with config-driven selection
- Token-budget-aware graph traversal and XML compaction
- MCP server with 5 tools over stdio
- CLI with init, configure, setup, index, query, verify, serve
- Interactive configuration (provider, model, API key) with vault `.env` storage
- Auto-setup for Claude Code, Codex, VS Code, and Cursor (MCP config generation)
- Security hardened (path traversal protection, input validation, parameterized SQL)
- Temporal node lifecycle (Phase 8): soft-delete, supersede, `valid_from`/`valid_until`/`superseded_by` fields, active-only queries
- CRUD classifier (Phase 9): ADD/UPDATE/SUPERSEDE/NOOP classification on every `context_write`, LLM + embedding-distance fallback, OpenAI and Anthropic providers
- `context_delete` MCP tool for explicit soft-delete with optional `superseded_by` reference
- Namespace-level write mutex (Phase 10): concurrent writes to different namespaces proceed in parallel; same-namespace writes serialize to prevent CRUD races and TOCTOU bugs
- Namespace manager + bridges (Phase 11): `_namespace.md` per-namespace config, `_bridges/*.md` relation whitelists, qualified ID resolution, cross-namespace edge validation on write, auto-discovery of cross-namespace edges
- Heat map (Phase 12): co-access frequency tracking in `_heat/<namespace>.md`, exponential decay with floor, heat-boosted BFS traversal priority, automatic co-access logging from `context_query`
- Summary engine (Phase 13): `_summary.md` per namespace via LLM synthesis, async regeneration scheduler with delta threshold + periodic interval, graceful degradation
- Update engine (Phase 13): source hash change detection, reverse-edge staleness propagation, automatic reindexing, fsnotify file watcher, batch update mode
- Full CLI (Phase 15): `status`, `watch`, `bridge`, `summarize`, `reembed` commands; enhanced `verify` with namespace filter and staleness checks
- Slim HTTP API (Phase 16): `/api/graph`, `/api/namespaces`, `/api/node`, `/api/search`, `/api/heat`, `/api/bridges`, `/api/summary`, and inline node updates via `PUT /api/node/{id...}`
- Static analysis indexer (Phase 17): Go AST indexer (packages, functions, methods, types, interfaces), TypeScript regex-based indexer (classes, interfaces, functions, type aliases), generic file indexer (30+ extensions), `.gitignore` support, incremental indexing with CRUD classification, `marmot index <path>` CLI integration
- Graph visualization frontend (Phase 19): embedded D3 web UI served by `marmot ui` with filters, search, heat overlay, minimap, legend, keyboard shortcuts, and inline summary/context editing
- Node tags & domain clustering (Phase 20): many-to-many domain tags on nodes, `context_tag` MCP tool for bulk-tagging via semantic search, graph grouping and filtering by tag, tag badges in detail panel

### Known MVP limitations

- **Go-side KNN** --- sqlite-vec WASM bindings have an ABI mismatch with the current ncruces driver. Search scans all embeddings in Go. Fine for thousands of nodes; would need sqlite-vec for 100k+.
- **Limited provider selection** --- OpenAI and mock are supported. Voyage AI, Ollama, and other providers are not yet available.

### Post-MVP roadmap

See [docs/implementation_plan.md](docs/implementation_plan.md) for the full plan including the TypeScript web UI. Temporal fields (Phase 8), CRUD classification (Phase 9), namespace-level concurrency (Phase 10), namespace manager + bridges (Phase 11), heat map (Phase 12), summary + update engines (Phase 13), full CLI (Phase 15), static analysis indexer (Phase 17), integration testing + CI/CD (Phase 18), graph visualization (Phase 19), and node tags + domain clustering (Phase 20) are already implemented. See [docs/benchmark.md](docs/benchmark.md) for the SWE-QA evaluation methodology and results.

## License

Apache 2.0 --- see [LICENSE](LICENSE).
