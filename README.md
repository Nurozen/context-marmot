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

Nodes are Obsidian-compatible markdown files with YAML frontmatter and `[[wikilinks]]`. Open the vault in Obsidian for instant graph visualization, or query it programmatically.

## Features

- **Graph-based context** --- nodes connected by typed, directed edges (structural and behavioral)
- **Structural acyclicity** --- `contains`, `imports`, `extends`, `implements` edges enforce DAG structure; `calls`, `reads`, `writes`, `references` edges allow cycles (mutual recursion is real)
- **Semantic search** --- embedding index for natural language queries with graph traversal expansion
- **Token-budget compaction** --- results fit your context window, with full/compact/truncated tiers
- **MCP server** --- 3 tools (`context_query`, `context_write`, `context_verify`) over stdio
- **Obsidian-compatible** --- every node file renders natively in Obsidian with working graph view
- **Integrity verification** --- hash-based staleness detection, dangling edge checks, cycle detection
- **Single binary** --- Go, zero CGo, zero runtime dependencies

## Quick Start

### Prerequisites

- Go 1.21+ (`brew install go` or [go.dev/dl](https://go.dev/dl/))
- (Optional) An [OpenAI API key](https://platform.openai.com/api-keys) for semantic search. Without one, the mock embedder provides lexical-overlap search.

### Build

```bash
git clone https://github.com/nurozen/context-marmot.git
cd context-marmot
make build
```

This produces `bin/marmot`.

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

# Open in Obsidian (optional --- open testdata/demo/.marmot as a vault)
```

### Use in your own project

```bash
cd your-project

# Initialize vault, configure embeddings, and set up MCP configs --- all in one step
marmot init
```

`marmot init` runs three stages:
1. Creates the `.marmot/` vault directory
2. **`configure`** --- prompts for embedding provider, model, and API key
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

Once connected, agents get three tools:

| Tool | Description |
|------|-------------|
| `context_query` | Search the graph by natural language. Returns XML-compacted subgraph within token budget. |
| `context_write` | Write or update a node. Enforces structural acyclicity. Updates embedding index. |
| `context_verify` | Check node staleness, dangling edges, and structural integrity. |

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

## CLI Reference

| Command | Description |
|---------|-------------|
| `marmot init [--dir .marmot]` | Create a new vault, run configure, then setup |
| `marmot configure [--dir .marmot]` | Interactive prompt for embedding provider, model, and API key |
| `marmot setup [--dir .marmot] [--claude] [--codex] [--vscode] [--cursor]` | Generate MCP configs for detected (or specified) tools |
| `marmot index [--dir .marmot] [--force]` | Index all node files into the embedding store. `--force` clears and rebuilds all embeddings (use after changing provider/model). |
| `marmot query --query "..." [--dir .marmot] [--depth 2] [--budget 4096]` | Query the knowledge graph |
| `marmot verify [--dir .marmot]` | Run integrity and staleness checks |
| `marmot serve [--dir .marmot]` | Start the MCP server on stdio |

## Node Format

Nodes are markdown files with YAML frontmatter:

```markdown
---
id: auth/login
type: function
namespace: default
status: active
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
  config/                Vault config parser, .env key storage, embedder factory
  node/                  Markdown parser/writer, atomic file I/O
  graph/                 In-memory graph, adjacency lists, cycle detection
  verify/                Hash integrity, staleness, structural acyclicity
  embedding/             SQLite store, KNN search, OpenAI + mock embedders
  traversal/             BFS traversal, token-budget XML compaction
  mcp/                   MCP server (3 tools), engine wiring
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

**MVP complete.** The core hypothesis --- agents perform better with ContextMarmot than with vanilla file reading --- is testable with the current build.

### MVP scope (implemented)

- Node store with Obsidian-compatible markdown
- In-memory graph with structural/behavioral edge classification
- Embedding index with KNN search (Go-side, SQLite-backed)
- Pluggable embedding providers (OpenAI and mock) with config-driven selection
- Token-budget-aware graph traversal and XML compaction
- MCP server with 3 tools over stdio
- CLI with init, configure, setup, index, query, verify, serve
- Interactive configuration (provider, model, API key) with vault `.env` storage
- Auto-setup for Claude Code, Codex, VS Code, and Cursor (MCP config generation)
- Security hardened (path traversal protection, input validation, parameterized SQL)

### Known MVP limitations

- **Go-side KNN** --- sqlite-vec WASM bindings have an ABI mismatch with the current ncruces driver. Search scans all embeddings in Go. Fine for thousands of nodes; would need sqlite-vec for 100k+.
- **Single namespace** --- multi-namespace support and cross-project bridges are post-MVP.
- **Limited provider selection** --- OpenAI and mock are supported. Voyage AI, Ollama, and other providers are not yet available.

### Post-MVP roadmap

See [docs/implementation_plan.md](docs/implementation_plan.md) for the full plan including temporal fields, LLM-based CRUD classification, heat maps, summary engine, static analysis indexing, and the TypeScript web UI.

## License

Apache 2.0 --- see [LICENSE](LICENSE).
