# ContextMarmot Implementation Plan

## Tech Stack

- **Core Engine**: Go
  - SQLite driver: `ncruces/go-sqlite3` (WASM, zero CGo)
  - KNN search: Go-side L2 distance with max-heap (sqlite-vec ABI incompatible — see notes)
  - MCP SDK: `mark3labs/mcp-go` v0.46.0 (well-established, stdio transport)
  - Embedding providers: OpenAI API (net/http, no SDK) + Mock (trigram hashing)
  - Anthropic SDK: `anthropics/anthropic-sdk-go` (for post-MVP LLM features)
- **Embedding Index**: SQLite (Go-side KNN search)
- **LLM Provider**: Anthropic (Haiku-class for CRUD classification + summary generation)
- **Visualization (initial)**: Obsidian (open `.marmot/` as vault — zero integration needed)
- **Visualization**: TypeScript + Vite + D3.js web UI (Phase 19), embedded in Go binary via `go:embed`, served by `marmot ui`
- **Protocol**: MCP (Model Context Protocol)
- **Versioning**: Git (native, no wrapper library)
- **On-disk format**: YAML frontmatter + Markdown + `[[wikilinks]]` (Obsidian-compatible)
- **Agent output format**: XML tags (semantic delineation for LLM context windows)

---

## MVP Definition

The MVP tests the core hypothesis: **agents perform measurably better with
ContextMarmot than with vanilla file reading.**

### MVP Scope (Phases M1-M7)

| In MVP | Out of MVP |
|--------|-----------|
| Node Store (basic, no temporal fields) | Temporal fields (soft-delete, superseded) |
| Graph Manager (basic, structural acyclicity) | CRUD Classifier (LLM-based) |
| Verifier (structural acyclicity + hashing) | Summary Engine |
| Embedding Index (search + upsert) | Heat Map |
| Graph Traversal (basic, no heat map) | REST/WebSocket API |
| Compaction Engine (basic adjacency) | Static Analysis Indexer |
| MCP Server (3 tools, simple upsert writes) | Git auto-commit batching |
| CLI (init, index, query, serve) | Namespace management (single namespace only) |

### MVP Success Criteria

Benchmark: agent with ContextMarmot vs agent without, measured on:
- Tool calls to build equivalent context (target: 3x fewer)
- Tokens consumed (target: 50% reduction)
- Task success rate on code understanding tasks

---

## MVP Status: COMPLETE (2026-03-31)

All M1-M7 phases implemented and tested. 180 tests passing across 8 packages.
Production hardened with path traversal protection, input validation, and race-safe operations.

**Implementation notes:**
- sqlite-vec Go bindings have a WASM ABI mismatch with ncruces v0.17.1 — embedding search uses Go-side KNN (L2 distance with max-heap) instead. Upgradable when bindings are fixed.
- MCP SDK: used `mark3labs/mcp-go` (official Go SDK import paths were unstable at time of implementation).
- Pluggable embedding providers: OpenAI (`text-embedding-3-small`, `text-embedding-3-large`, `text-embedding-ada-002`) and Mock (trigram hashing). Config-driven selection via `_config.md` frontmatter with env var API keys and graceful fallback to mock.
- `internal/config` package provides vault config parsing (`_config.md`), atomic save, `.env` key storage, and embedder factory (`NewEmbedderFromVault`).
- Batch embedding via `EmbedBatch()` for efficient indexing (OpenAI chunks at 100 inputs).
- Variable embedding dimensions supported per-namespace (1536 for small/ada, 3072 for large).
- `marmot index --force` rebuilds all embeddings (required after provider/model change).
- `marmot configure` provides interactive setup for embedding provider, model, and API key. Keys stored in `.marmot/.marmot-data/.env` with env var fallback.
- `marmot setup` auto-detects IDE/agent tools and generates MCP configs: `.mcp.json` (Claude Code, Cursor), `.codex/config.toml` (Codex), `.vscode/mcp.json` (VS Code).
- `marmot init` chains configure → setup for zero-config onboarding.
- MCP tool schemas include full JSON Schema for `edges` (item schema with `target`/`relation` enum) and `source` (property schema) to prevent agent field-name guessing.

## Post-MVP Eval: SWE-QA A/B Benchmark — COMPLETE (2026-04-01)

Real A/B evaluation comparing Claude Sonnet with vanilla file tools vs. the hybrid (ContextMarmot + file tools) pattern on 20 SWE-QA code comprehension questions across 4 repos.

**Results:**
- **Answer quality: identical** (4.62/5 both conditions, judged by Claude on correctness/completeness/specificity)
- **Token reduction: −37%** (151,327 → 95,876 avg tokens per question)
- **Cost reduction: −22%** ($0.1065 → $0.0834 avg cost per question)
- **Turn reduction: −8%** (7.5 → 6.9 avg turns per question)

MCP-only mode (graph without file reads) was evaluated and discontinued — quality was insufficient (1.87/5) for code comprehension tasks requiring precise source reading. The hybrid pattern is the validated configuration.

**Key implementation notes:**
- `cmd/marmot-eval/` — full eval harness: seeder, runner, judge, reporter, JSONL checkpointing
- Seeder stores real file source code (up to 8 KB per node) in `context` field + accurate `source.lines` ranges
- Index pipeline embeds `summary + context` jointly (not summary alone) for richer semantic retrieval
- `HandleContextWrite` in MCP server also embeds `summary + context` on writes
- All three conditions use XML-delimited prompts with explicit `<workflow>` steps
- MCP-only condition deprecated; `--skip-mcp` flag added to `marmot-eval`
- See [docs/benchmark.md](../docs/benchmark.md) for full methodology, per-question breakdown, and related work comparison

---

## Phase M1: Project Bootstrap

- [x] Initialize project module
- [x] Set up directory structure
- [x] Add Makefile with build, test, lint targets
- [x] Add `.gitignore` for build artifacts + `.marmot-data/` + `.obsidian/`
- [x] Initialize git repo

## Phase M2: Node Store

The file I/O layer. Reads/writes/parses Obsidian-compatible markdown node files.
MVP omits temporal fields — all nodes are `status: active`.

- [x] Define structs: `Node`, `Edge`, `Source`, `Provenance`
  - [x] `Edge` includes `Class` field (structural / behavioral)
- [x] Implement markdown parser
  - [x] Parse YAML frontmatter into `Node` struct (including `edges` array)
  - [x] Parse markdown body to extract summary text (content before first `##` heading)
  - [x] Parse `## Context` section for full context content
  - [x] Extract `[[wikilinks]]` from body (for validation against frontmatter edges)
- [x] Implement markdown writer (struct -> Obsidian-compatible markdown file)
  - [x] Render YAML frontmatter from struct fields
  - [x] Render summary as plain markdown paragraph
  - [x] Generate `## Relationships` section with `[[wikilinks]]` from frontmatter edges
  - [x] Render `## Context` section with fenced code blocks
- [x] Implement atomic file writes (temp file + rename)
- [x] Implement file operations
  - [x] `LoadNode(path) -> Node`
  - [x] `SaveNode(node, path) -> error` (atomic)
  - [x] `DeleteNode(path) -> error`
  - [x] `ListNodes(namespace_path) -> []NodeMeta` (lightweight, frontmatter only)
- [x] Implement ID derivation from file path (`project-alpha/auth/login.md` -> `auth/login`)
- [x] Write unit tests for parse/write roundtrip
- [x] Write unit tests for malformed/edge-case files
- [x] Verify output files render correctly in Obsidian (manual smoke test)

## Phase M3: Graph Manager

In-memory graph engine. Loads from node store, maintains adjacency lists.
Enforces structural acyclicity only — behavioral cycles allowed.

- [x] Define `Graph` struct (adjacency list, reverse adjacency, node index)
- [x] Implement `LoadGraph(namespace_path) -> Graph` (bulk load all nodes)
- [x] Implement `AddNode(node) -> error` (insert into graph)
- [x] Implement `RemoveNode(id) -> error` (remove + cascade edge cleanup)
- [x] Implement `AddEdge(source, target, relation) -> error`
  - [x] Classify edge as structural or behavioral based on relation type
  - [x] Run acyclicity check only for structural edges
- [x] Implement `RemoveEdge(source, target) -> error`
- [x] Implement `GetNode(id) -> Node`
- [x] Implement `GetEdges(id, direction) -> []Edge` (outbound / inbound)
- [x] Implement `GetNeighbors(id, depth) -> []Node`
- [x] Write unit tests for graph operations
- [x] Write unit tests for structural cycles (rejected) vs behavioral cycles (allowed)
- [x] Write benchmarks for load time + traversal on synthetic graphs

## Phase M4: Verifier

Structural acyclicity enforcement + hash-based integrity checking.

- [x] Implement topological sort for structural cycle detection
- [x] Implement `CheckStructuralAcyclicity(graph, proposed_edge) -> (bool, []string)` (returns cycle path if found; skips behavioral edges)
- [x] Implement `ComputeNodeHash(node) -> string` (SHA-256 of content)
- [x] Implement `ComputeSourceHash(source_path, lines) -> string`
- [x] Implement `VerifyStaleness(node) -> StaleStatus` (compare stored vs current source hash)
- [x] Implement `VerifyIntegrity(graph) -> []IntegrityIssue` (dangling edges, hash mismatches, structural cycles)
- [x] Integrate structural acyclicity check into `Graph.AddEdge()` (reject structural cycles only)
- [x] Write unit tests for cycle detection (various graph topologies, mixed edge classes)
- [x] Write unit tests for staleness detection

## Phase M5: Embedding Index

SQLite + sqlite-vec for semantic search. Decay-agnostic.

- [x] Set up SQLite store (sqlite-vec bindings incompatible — Go-side KNN used instead)
- [x] Define `EmbeddingStore` interface
- [x] Implement SQLite schema initialization (create tables on first run, including `model` column)
- [x] Implement `Upsert(node_id, embedding, summary_hash, model) -> error`
- [x] Implement `Search(query_embedding, top_k) -> []ScoredNodeID` (rejects cross-model queries)
- [x] Implement `Delete(node_id) -> error`
- [x] Implement `StaleCheck(node_id, current_hash) -> bool`
- [x] Define `Embedder` interface (abstract over embedding provider)
- [x] Implement OpenAI embedder (`text-embedding-3-small`, `text-embedding-3-large`, `text-embedding-ada-002`)
- [x] Implement Mock embedder (trigram hashing, 1536-dim, zero dependencies)
- [x] Implement `EmbedBatch()` for efficient bulk indexing (OpenAI chunks at 100)
- [x] Implement embedding caching (skip re-embed if summary unchanged)
- [x] Write integration tests with in-memory SQLite
- [x] Write benchmarks for search at various collection sizes

## Phase M6: Graph Traversal + Compaction

BFS/DFS expansion + token-budget-aware compaction into XML output.

- [x] Define `TraversalConfig` (depth, budget, mode)
- [x] Implement `Traverse(entry_nodes, config) -> Subgraph`
- [x] Implement token budget tracking (chars/4 heuristic, documented approximation)
- [x] Implement frontier expansion with priority queue
- [x] Implement depth limiting
- [x] Define `CompactedResult` struct
- [x] Implement adjacency compaction (shallow mode)
  - [x] Full `<node>` for entry/high-relevance nodes (includes context)
  - [x] `<node_compact>` for adjacent nodes (summary + source ref only)
  - [x] `<truncated>` section listing over-budget node IDs
- [x] Implement XML serialization with proper entity escaping
- [x] Implement relevance scoring for node ranking within result
- [x] Write unit tests for traversal with various budgets and depths
- [x] Write unit tests for compaction output format
- [x] Verify XML output is well-formed and parseable

## Phase M7: MCP Server + CLI

Minimal MCP server and CLI to enable agent testing.

- [x] Set up MCP server framework
- [x] Implement `context_query` tool handler
  - [x] Parse parameters (query, depth, budget, namespace, mode)
  - [x] Route to semantic search -> traversal -> compaction pipeline
  - [x] Return XML compacted result
- [x] Implement `context_write` tool handler (simple upsert, no CRUD classification in MVP)
  - [x] Parse parameters (id, type, namespace, summary, context, edges, source)
  - [x] Validate structural acyclicity
  - [x] Write Obsidian-compatible node via node store
  - [x] Update embedding index
  - [x] Return confirmation with hash
- [x] Implement `context_verify` tool handler
  - [x] Parse parameters (node_ids, namespace, check)
  - [x] Route to verifier (staleness / integrity / all)
  - [x] Return verification report
- [x] Implement rich tool descriptions to guide agent behavior
  - [x] `context_query`: explains semantic search pipeline, depth/budget tuning, natural language tips
  - [x] `context_write`: when to create nodes, summary writing guidance (good/bad examples), node type meanings, edge selection (structural vs behavioral), ID structure conventions
  - [x] `context_verify`: when to run each check type, what each detects
- [x] Implement tool discovery (`tools/list`)
- [x] Implement vault auto-discovery (walk up directory tree to find `.marmot/`, like git finds `.git/`)
- [x] Set up CLI framework
- [x] Implement `marmot init` — initialize `.marmot/` vault structure
- [x] Implement `marmot index` — index node files into embedding store (`--force` to rebuild)
- [x] Implement `marmot query <query>` — run a context query and print XML result
- [x] Implement `marmot verify` — run integrity/staleness checks
- [x] Implement `marmot serve` — start MCP server
- [x] Implement `marmot configure` — interactive prompt for embedding provider, model, and API key
  - [x] Provider selection (openai / mock) with numbered menu
  - [x] Model preset selection (text-embedding-3-small / text-embedding-3-large)
  - [x] API key input with masked echo (golang.org/x/term) and vault `.env` storage
  - [x] Config save with `LoadRaw()`/`Save()` preserving markdown body
  - [x] Auto-chained from `marmot init`
- [x] Implement `marmot setup` — generate MCP configs for IDE/agent tools
  - [x] Auto-detect installed tools (Claude Code, Codex, VS Code, Cursor)
  - [x] Generate `.mcp.json` (Claude Code / Cursor format)
  - [x] Generate `.codex/config.toml` (Codex TOML format, append-safe)
  - [x] Generate `.vscode/mcp.json` (VS Code format with `servers` key)
  - [x] Per-tool flags (`--claude`, `--codex`, `--vscode`, `--cursor`)
  - [x] Auto-chained from `marmot init` after configure
- [x] Add proper JSON Schema for `context_write` edges and source parameters
  - [x] `edges` array item schema with `target` and `relation` (enum of 10 valid values)
  - [x] `source` object schema with `path`, `lines`, `hash` properties
  - [x] `node_ids` array item schema (string items)
- [x] Write integration tests for full query/write/verify flows
- [x] Write tests for configure (6 tests) and setup (10 tests) commands
- [x] **Benchmark: SWE-QA A/B evaluation — ContextMarmot hybrid vs vanilla file reading**
  - 20 curated questions across 4 repos (Flask, Requests, Django, pytest) from SWE-QA-Benchmark
  - Real `claude -p` invocations per (question, condition) pair; Claude judge scores each answer
  - **Quality: 4.62/5 both conditions** — identical answer quality
  - **Token reduction: −37%** (151,327 → 95,876 avg tokens/question)
  - **Cost reduction: −22%** ($0.1065 → $0.0834 avg cost/question)
  - Hybrid condition: graph-first navigation with XML `<workflow>` prompt + targeted file reads
  - Node seeder stores real source code (8 KB) + line ranges; index embeds summary+context jointly
  - MCP-only condition evaluated and deprecated (1.87/5 quality — insufficient for code comprehension)
  - JSONL checkpointing for resumable runs; `--skip-mcp` flag added
  - See [docs/benchmark.md](../docs/benchmark.md) for full per-question breakdown and methodology

---

## Post-MVP Phases

After MVP validates the core hypothesis, layer in full features.

## Phase 8: Temporal Fields + Soft-Delete — COMPLETE (2026-04-01)

- [x] Add temporal fields to Node struct: `Status`, `ValidFrom`, `ValidUntil`, `SupersededBy`
- [x] Update markdown parser/writer for temporal frontmatter
- [x] Implement `SoftDeleteNode(path, superseded_by) -> error`
- [x] Implement `ListActiveNodes(namespace_path) -> []NodeMeta` (excludes superseded)
- [x] Update Graph Manager: `SupersedeNode(old_id, new_node) -> error`
- [x] Update Graph Manager: separate active-node index and full-node index
- [x] Update Embedding Index: `UpdateStatus(node_id, status)`, `include_superseded` param on search
- [x] Update Compaction Engine: superseded node rendering with status attribute
- [x] Update MCP `context_query`: `include_superseded` parameter
- [x] Update Verifier: superseded-chain integrity checks
- [x] Write unit tests for soft-delete lifecycle
- [x] Write unit tests for temporal queries

## Phase 9: CRUD Classifier (LLM-Based) — COMPLETE (2026-04-01)

- [x] Define `LLMProvider` interface (classify, summarize)
- [x] Implement Anthropic provider (Haiku-class model, structured output)
- [x] Implement OpenAI provider (gpt-5.1-codex-mini, reuses existing OPENAI_API_KEY)
- [x] Implement `FindSimilar(embedding, threshold) -> []ScoredNodeID` in Embedding Index
- [x] Implement CRUD classification flow:
  - [x] Embed incoming node summary
  - [x] Retrieve top-k similar candidates
  - [x] If candidates: send to LLM for ADD/UPDATE/SUPERSEDE/NOOP classification
  - [x] If no candidates: classify as ADD
- [x] Implement fallback: pure embedding distance when LLM unavailable
- [x] Integrate into `context_write` handler (replace simple upsert)
- [x] Write integration tests for all 4 classification paths
- [x] Write tests for LLM-unavailable fallback

## Phase 10: Concurrency — COMPLETE (2026-04-01)

- [x] Implement namespace-level write mutex
- [x] Implement atomic file writes (temp file + rename) — verified from M2
- [x] Write concurrent write tests (multiple goroutines/agents writing to same namespace)
- [x] Write concurrent read-during-write tests

> **Note:** Git auto-commit was removed from this phase. The supersede chain (Phase 8) already provides semantic history — intentional replacements with timestamps and `superseded_by` links. Git's mechanical byte-level history adds little value on top of this and would intrude on the host project's commit history. Users manage `.marmot/` in git the same way they manage any other project directory.

## Phase 11: Namespace Manager + Bridges — COMPLETE (2026-04-01)

Project isolation + cross-namespace references.

- [x] Define `Namespace` struct — `internal/namespace/namespace.go`
- [x] Implement `LoadNamespace(path) -> Namespace`
- [x] Implement `CreateNamespace(name, root_path) -> Namespace`
- [x] Implement namespace config parsing (`_namespace.md` YAML frontmatter)
- [x] Implement qualified ID resolution (namespace + node_id from path) — `Manager.ParseQualifiedID`
- [x] Define `Bridge` struct (allowed_relations whitelist only — no manual edge list)
- [x] Implement bridge manifest parsing (`_bridges/*.md` YAML frontmatter)
- [x] Implement `ValidateCrossNamespaceEdge(edge, bridges) -> error` (check relation in whitelist)
- [x] Auto-discover cross-namespace edges by scanning node files — `Manager.DiscoverCrossNamespaceEdges`
- [x] Implement `ListNamespaces(marmot_root) -> []Namespace`
- [x] Define `Config` struct (global config) — `internal/config/config.go`
- [x] Implement global config parsing (`_config.md` YAML frontmatter) — `internal/config/config.go`
- [x] Write unit tests for namespace resolution and bridge validation — 15 tests in `namespace_test.go`
- [x] Integrate namespace manager into MCP engine — cross-namespace edge validation on `context_write`
- [x] Wire namespace manager into serve pipeline

## Phase 12: Heat Map — COMPLETE (2026-04-01)

Co-access frequency tracking. Decay affects traversal priority only.

- [x] Define `HeatMap` struct (including `decay_floor` field) — `internal/heatmap/heatmap.go`
- [x] Implement heat map file parsing (`_heat/<namespace>.md` YAML frontmatter)
- [x] Implement heat map file writing
- [x] Implement `RecordCoAccess(node_ids []string) -> error` (pairwise weight update)
- [x] Implement `GetWeights(node_ids []string) -> map[pair]float64`
- [x] Implement `Decay() -> error` (apply decay_rate, enforce decay_floor — weights never reach zero)
- [x] Implement promotion detection (`weight >= threshold` for sustained period)
- [x] Integrate heat map into Graph Traversal edge priority scoring
- [x] Log co-access from `context_query` responses
- [x] Write unit tests for weight accumulation, decay math, and floor enforcement — 15 tests in `heatmap_test.go`
- [x] Wire heat map into serve pipeline (load on startup, save on shutdown)

## Phase 13: Summary Engine + Update Engine — COMPLETE (2026-04-01)

- [x] **Summary Engine** — `internal/summary/`
  - [x] Implement `GenerateSummary(namespace) -> SummaryNode` using LLM Provider
  - [x] Write `_summary.md` with `type: summary` frontmatter and wikilinks
  - [x] Implement async regeneration scheduler (configurable interval)
  - [x] Implement change-triggered regeneration (significant node count delta)
  - [x] Ensure summary generation never blocks read/write operations
  - [x] Graceful degradation: summaries go stale when LLM unavailable, not broken
- [x] **Update Engine** — `internal/update/`
  - [x] Implement `DetectChanges(namespace) -> []ChangedNode` (hash comparison)
  - [x] Implement `PropagateStale(changed_ids, graph) -> []AffectedNode` (walk reverse edges)
  - [x] Implement `Reindex(node_ids) -> error` (re-read source, update node, update embedding)
  - [x] Implement file watcher mode (fsnotify for continuous operation)
  - [x] Implement batch update mode (on-demand scan via `RunBatchUpdate`)
  - [x] Trigger summary regeneration on significant changes (via OnChange callback)
  - [x] Write unit tests for change detection and propagation — 15 tests in `update_test.go`
- [x] **Integration**
  - [x] Add `Summarizer` interface to LLM package (OpenAI, Anthropic, Mock implementations)
  - [x] Wire Summary Engine + Scheduler + Update Engine into MCP engine and serve pipeline
  - [x] Notify summary scheduler from `context_write` and `context_delete` handlers

## Phase 14: Embedding Model Management

- [x] Store `model` field per embedding in SQLite
- [x] Reject cross-model similarity queries
- [x] Implement `marmot index --force` (regenerate all embeddings — serves as reembed)
- [x] Handle config `embedding_model` changes gracefully (warn + require `--force` reindex)
- [x] Write tests for model mismatch detection and migration

## Phase 15: Full CLI — COMPLETE (2026-04-02)

Complete command-line interface.

- [x] Implement `marmot index <path>` — accepts path argument, defers to static analysis indexer (Phase 17) with clear message
- [x] Implement `marmot verify [--namespace] [--staleness]` — namespace filter and source staleness checks
- [x] Implement `marmot status` — vault stats: node counts by status, edges, embeddings, stale nodes, namespaces, bridges, heat map pairs, summary status
- [x] Implement `marmot watch` — starts fsnotify file watcher + auto-reindex via Update Engine; graceful shutdown on SIGINT/SIGTERM
- [x] Implement `marmot bridge <ns-a> <ns-b> [--relations ...]` — creates bridge manifest with configurable allowed relations
- [x] Implement `marmot summarize [--namespace]` — force summary regeneration using configured LLM provider
- [x] Implement `marmot reembed [--namespace]` — regenerate all embeddings (delegates to index --force)
- [x] Write CLI integration tests — 14 tests in `cli_phase15_test.go`
- [x] Added `embedding.Store.Count()` method for status reporting
- [x] Add `token_budget` to vault config (`_config.md` frontmatter) — configurable default for queries
- [x] CLI `--budget` flag overrides config; MCP `budget` param overrides config; config overrides built-in default (4096)
- [x] Add `marmot version` command with build-time ldflags (version, commit, date)
- [x] Add GoReleaser cross-compilation + GitHub Actions auto-tag & release pipeline

## Phase 16: HTTP API (Slim) — COMPLETE (2026-04-05)

Minimal HTTP interface serving the graph visualization frontend. Scoped to what the D3 frontend (Phase 19) actually needs — full REST/WebSocket deferred until a consumer demands it.

- [x] Implement `GET /api/graph/{namespace}` — full graph as JSON (nodes + edges + metadata, active by default)
  - [x] `?include_superseded=true` query param to include superseded nodes
- [x] Implement `GET /api/namespaces` — list all namespaces with node counts
- [x] Implement `PUT /api/node/{id...}` — update node summary/context (inline editing from UI)
  - [x] Validate input (node ID format, field lengths)
  - [x] Re-embed updated node after save
- [x] Implement CORS middleware for local dev (Vite dev server proxy)
- [x] Serve embedded static frontend assets via `go:embed` (`web/embed.go`)
- [x] Add `marmot ui` CLI subcommand — starts HTTP server, opens browser (`cmd/marmot/pipeline.go` runUIPipeline)
  - [x] `--port` flag (default 3274)
  - [x] `--no-open` flag to suppress browser launch
- [x] Write API integration tests (20 tests in `internal/api/api_test.go`)
- [x] Implement `GET /api/node/{namespace}/{id...}` — single node detail
- [x] Implement `GET /api/search?q=...` — server-side semantic search
- [x] Implement `GET /api/heat/{namespace}` — heat map data
- [x] Implement `GET /api/bridges` — bridge listing
- [x] Implement `GET /api/summary/{namespace}` — namespace summary
- [x] Uses Go 1.26 ServeMux pattern routing (no external router)
- [x] Shares Engine instance with MCP server via extracted `buildEngine()` helper
- [x] Exported `ResolveNodeID` in `internal/mcp/engine.go` for API reuse

### Deferred
- WebSocket for live graph updates (manual refresh sufficient initially)

## Phase 17: Static Analysis Indexer — COMPLETE (2026-04-02)

Automated code-to-graph indexer (first use case enabler).

- [x] Define `Indexer` interface — `internal/indexer/indexer.go`: `SourceEntity`, `SourceRef`, `EntityEdge`, `IndexResult`, `Indexer` interface
- [x] Implement Go indexer (parse Go AST -> nodes + edges with correct edge classes) — `internal/indexer/golang.go`: full `go/ast` + `go/parser` analysis; extracts packages, functions, methods, types, interfaces; edges: `contains`, `imports`, `calls`, `extends` (embedded structs), `implements` (same-package best-effort); import resolution via `go.mod` module path
- [x] Implement TypeScript indexer (parse TS AST -> nodes + edges) — `internal/indexer/typescript.go`: regex-based parsing with string/comment masking; extracts modules, classes, interfaces, functions, arrow functions, type aliases, methods; edges: `contains`, `imports`, `extends`, `implements`; JSDoc comment extraction; brace-depth line tracking
- [x] Implement generic file indexer (module-level nodes for unsupported languages) — `internal/indexer/generic.go`: 30+ extensions; one module/file node per file; best-effort import detection (Python, Java, Ruby, Rust, C/C++); symbol counting for summaries; binary file detection
- [x] Implement incremental indexing (only re-index changed files) — `Runner` compares source hashes; skips unchanged entities
  - [x] On re-index, use CRUD classifier to determine ADD/UPDATE/SUPERSEDE per node — `classifierAdapter` wraps `classifier.Classifier` for Runner's interface
- [x] Implement ignore patterns (respect `.gitignore` + namespace config globs) — `internal/indexer/ignore.go`: standard gitignore syntax (comments, negation, `**` recursive, trailing `/`), always-ignore defaults (`.git/`, `node_modules/`, `vendor/`, etc.), extra patterns from config
- [x] Integrate with `marmot index` CLI command — `marmot index <path>` runs static analysis; `marmot index <path> --incremental` for incremental mode; `marmot index` (no path) still embeds existing nodes
- [x] Write tests against sample codebases — 40 tests in `indexer_test.go`: Go indexer (11), TypeScript indexer (9), generic indexer (7), registry (3), ignore matcher (6), runner (6)
- [x] `Registry` with `NewDefaultRegistry()` pre-populating Go, TypeScript, and Generic indexers
- [x] `Runner` orchestrator with `NodeStore`, `EmbeddingStore`, `Embedder`, `Classifier`, `GraphReader` interfaces; batch embedding; `RunResult` reporting

## Phase 18: Integration Testing & Hardening — COMPLETE (2026-04-02)

- [x] End-to-end test: index a real project -> query via MCP -> verify results — `TestE2E_IndexProjectThenQuery` + `TestIndexProjectThenQueryViaMCP`: creates mini Go project, runs static analysis indexer, queries via MCP engine, verifies functions/types found
- [x] End-to-end test: mutual recursion in code -> verify behavioral cycles preserved — `TestE2E_MutualRecursionBehavioralCycles` + `TestMutualRecursionBehavioralCycles`: IsEven/IsOdd mutual recursion, verifies bidirectional `calls` edges classified as behavioral, no structural cycle issues
- [x] End-to-end test: multi-namespace with bridges -> cross-project queries — `TestE2E_MultiNamespaceBridges` + `TestMultiNamespaceWithBridges`: frontend/backend namespaces, bridge with allowed relations, cross-namespace edge validation, disallowed relation rejection
- [x] End-to-end test: open indexed vault in Obsidian, verify graph renders correctly — `TestE2E_ObsidianCompatibleOutput` + `TestObsidianCompatibility`: verifies YAML frontmatter, `## Relationships` with `[[wikilinks]]`, `## Context` sections, valid UTF-8, `_config.md`, `.obsidian/graph.json`
- [x] End-to-end test: CRUD lifecycle — ADD -> UPDATE -> SUPERSEDE -> verify temporal chain — `TestE2E_CRUDLifecycleTemporalChain` + `TestCRUDLifecycleADDUpdateSupersede`: MockProvider-controlled classification, verifies status transitions, superseded_by/valid_until fields, include_superseded query filtering, disk persistence
- [x] End-to-end test: summary generation after indexing, verify wikilinks resolve — `TestE2E_SummaryGenerationWikilinks` + `TestSummaryGenerationAfterIndexing`: mock LLM with wikilinks, WriteSummary/ReadSummary roundtrip, verifies wikilinks resolve to actual graph nodes, frontmatter (node_count, generated_at)
- [x] End-to-end test: concurrent agents writing to same namespace — `TestE2E_ConcurrentWritesSameNamespace` + `TestConcurrentAgentsWriting`: 20 goroutines writing unique nodes, verifies all nodes exist, correct edge count, no data corruption, integrity check passes
- [x] Load test: synthetic graph with 10k+ nodes -> query performance — `TestE2E_LoadTest10kNodes`: 10,000 nodes with 3-5 random edges, graph load + search + traversal + verify performance timings; completes in ~13s with race detector
- [x] Fuzz test: malformed node files, invalid edges, corrupt embeddings — `TestE2E_FuzzMalformedNodes` (8 malformed file types), `TestE2E_FuzzInvalidEdges` (unknown relations, empty targets, oversized IDs, null bytes), `TestE2E_FuzzCorruptEmbeddings` (corrupt .db file recovery)
- [x] CI/CD pipeline setup (GitHub Actions) — `.github/workflows/ci.yml`: matrix build (Go 1.23/1.24/1.26), unit tests + integration tests with race detector, golangci-lint; `.golangci.yml` config

---

## Phase 18.5: Cross-Vault Bridges — COMPLETE (2026-04-03)

Core cross-vault bridge infrastructure.

- [x] Add `vault_id` field to `VaultConfig` for stable cross-vault identity
- [x] Extend `Bridge` struct with `SourceVaultPath`, `TargetVaultPath`, `SourceVaultID`, `TargetVaultID`
- [x] Add `IsCrossVault()` method, `QualifiedID.VaultID` for `@vault-id/node-id` format
- [x] Implement `VaultRegistry` with lazy-loading of remote vault graphs (`internal/namespace/registry.go`)
- [x] Implement `CreateCrossVaultBridge()` writing manifests to both vaults
- [x] Extend CLI `marmot bridge` to detect path arguments and dispatch to cross-vault bridge creation
- [x] Define `GraphResolver` interface in traversal package (`GetNode`, `GetEdges`)
- [x] Implement `BridgedGraphResolver` wrapping local graph + `VaultGraphProvider` for cross-vault traversal
- [x] Refactor `Traverse()` and `Compact()` to accept `GraphResolver` interface (backward compatible — `*graph.Graph` satisfies it)
- [x] Wire `VaultRegistry` into MCP Engine with `graphResolver()` helper
- [x] Update `HandleContextQuery` to use `BridgedGraphResolver` for cross-vault traversal
- [x] Add cross-vault edge validation in `HandleContextWrite` for `@vault-id/...` edge targets
- [x] Add `--bridges` flag to `marmot verify` for cross-vault bridge connectivity checks (path existence, vault_id match)
- [x] 28 unit tests: `registry_test.go` (10), `bridged_test.go` (10), `namespace_test.go` cross-vault additions (8)

### Bug fixes (2026-04-04)

- [x] Fix `BridgedGraphResolver.GetNode` — remote nodes were returned with bare local IDs causing traversal mismatch; now rewrites ID to `@vault-id/node-id` form
- [x] Deep-copy `Edges` slice in `BridgedGraphResolver.GetNode` to prevent cache corruption from shared backing array
- [x] Fix namespace auto-prefix incorrectly prefixing cross-namespace edge targets (e.g., `backend/auth/login` → `frontend/backend/auth/login`); now checks if first path segment is a known namespace
- [x] Skip `@`-prefixed targets in `VerifyIntegrity` dangling edge checks (cross-vault references can't be validated locally)
- [x] Filter `@`-prefixed IDs from `RecordCoAccess` in heat map to avoid orphaned cross-vault pairs
- [x] Wire `VaultRegistry` when cross-vault bridges or routes exist, even without explicit namespace directories
- [x] 21 stress/integration tests: `bridged_stress_test.go` (13), `bridged_integration_test.go` (8)
- [x] 9 cross-vault write-path tests: `crossvault_write_test.go`

## Phase 18.6: Vault Routing Table + Relative Paths — COMPLETE (2026-04-05)

AS-style routing between independent vaults, relative source paths, and concurrency hardening.

### Global Routing Table (`~/.marmot/routes.yml`)

- [x] New `internal/routes/` package — `RoutingTable` struct mapping vault_id → filesystem path
- [x] `Load()`/`Save()` with atomic writes (tmp + rename), `LoadFrom()`/`SaveTo()` for explicit paths
- [x] `SetOverridePath()` for test isolation — prevents test pollution of `~/.marmot/routes.yml`
- [x] `Get`/`Set`/`Remove`/`List` methods with `sync.RWMutex` for goroutine safety
- [x] `Update(fn func(*RoutingTable) error)` — atomic read-modify-write under single lock, eliminates race window in concurrent bridge creation
- [x] `defaultPathLocked()` helper — `DefaultPath()` reads `overridePath` under package-level `RLock`
- [x] `VaultRegistry` resolution priority: routing table first, bridge manifest paths fallback
- [x] `KnownVaultIDs()` returns union of bridge and route entries
- [x] Auto-registration: `CreateCrossVaultBridge` registers both vaults in routing table via `routes.Update()`
- [x] 9 unit tests (`routes_test.go`), 8 stress tests (`routes_stress_test.go`), 7 route priority tests (`registry_routes_test.go`)

### `marmot route` CLI Command

- [x] `marmot route` — list all registered vaults with paths
- [x] `marmot route add <id> <path>` — register or update a vault
- [x] `marmot route rm <id>` — remove a vault registration
- [x] `marmot route resolve <id>` — show resolved filesystem path

### Relative Source Paths

- [x] `source.path` written as relative (to project root) in `HandleContextWrite`; absolute paths converted on write
- [x] `ResolveSourcePath(sourcePath, projectRoot)` resolves relative paths back to absolute on read
- [x] Path traversal protection: `..` components that escape project root return raw relative path (fails safely on open)
- [x] `VerifyStaleness` and `VerifyIntegrity` accept `projectRoot` parameter for resolution
- [x] Backward compatible with existing absolute paths
- [x] 6 relative path tests (`relative_path_test.go`), 3 MCP source path tests (`relative_source_test.go`)

---

## Phase 19: Graph Visualization Frontend — COMPLETE (2026-04-05)

Standalone interactive graph UI replacing Obsidian's limited graph view. Built with D3.js for maximum rendering control over node coloring, sizing, clustering, and edge styling. Embedded in Go binary via `go:embed`, served by `marmot ui` (Phase 16).

**Stack:** TypeScript + Vite + D3.js (`d3-force`, `d3-zoom`)
**Serving:** Embedded in Go binary via `go:embed all:dist` (`web/embed.go`), accessed via `marmot ui`
**Data:** Single fetch from `GET /api/graph/{namespace}`, client-side refresh button
**Design:** Alpine Cartographic aesthetic with Sora + IBM Plex Mono typography

### Scaffold & Data Layer
- [x] Initialize Vite + TypeScript project in `web/`
- [x] Define TypeScript types mirroring Go `Node`, `Edge`, `EdgeRelation` types (`web/src/types.ts`)
- [x] Implement graph data fetcher with namespace selection (`web/src/api.ts`)
- [x] Wire `go:embed` in serve command (`web/embed.go`), add `marmot ui` subcommand (`cmd/marmot/pipeline.go`)

### Core Visualization (D3)
- [x] Force-directed layout with `d3-force` (charge, link, collision, center) (`web/src/graph-view.ts`)
- [x] Node coloring by type (function, module, class, concept, decision, reference, composite)
- [x] Node sizing by edge count (in-degree + out-degree)
- [x] Edge rendering: solid for structural, dashed for behavioral
- [x] Edge coloring by relation type
- [x] Namespace clustering via force grouping (centripetal force per namespace)
- [x] Labels with truncation and zoom-adaptive visibility

### Interaction
- [x] Zoom/pan via `d3-zoom`
- [x] Click node to show detail panel (summary, context, source, edges) (`web/src/detail-panel.ts`)
- [x] Hover to highlight connected subgraph (dim unrelated nodes)
- [x] Drag nodes to reposition
- [ ] Double-click to expand/collapse containment groups — deferred
- [x] Search with highlight + fly-to (`web/src/search.ts`)

### Filtering & Views
- [x] Filter by node type (toggle chips) (`web/src/filters.ts`)
- [x] Filter by edge class (structural only, behavioral only, all)
- [ ] Hide/show superseded nodes — deferred
- [ ] Staleness indicator (orange ring on stale nodes) — deferred
- [x] Heat overlay mode (node glow intensity by heat weight) (`web/src/heat-overlay.ts`)

### Inline Editing (v1)
- [x] Click node detail panel → edit summary/context fields
- [x] Save via `PUT /api/node/{id...}`
- [x] Optimistic UI update, rollback on error

### Polish
- [x] Minimap for orientation on large graphs (`web/src/minimap.ts`)
- [x] Legend panel (color = type, size = connectivity, edge style = class) (`web/src/legend.ts`)
- [x] Keyboard shortcuts (`/` search, `Esc` close panel, arrow navigation) (`web/src/keyboard.ts`)
- [x] Responsive layout (detail panel collapses on narrow viewport)
- [x] Makefile targets: `build-ui`, `build-full`, `dev-ui`
- [x] 12 TypeScript modules: main, graph-view, detail-panel, filters, search, keyboard, legend, minimap, heat-overlay, types, api, style.css

## Phase 20: Node Tags & Domain Clustering — COMPLETE (2026-04-07)

Tags provide many-to-many domain-level categorization for nodes, enabling semantic grouping beyond code-structural types (e.g., "combat", "ai", "rendering" for a game codebase).

- [x] Add `tags []string` field to Node, NodeMeta, and frontmatterFields structs
- [x] YAML serialization with `omitempty` — backward-compatible with existing nodes
- [x] Add `tags` parameter to `context_write` MCP tool schema
- [x] Parse and persist tags in HandleContextWrite (create and update paths)
- [x] Include tags in embedding text for better semantic search
- [x] New `context_tag` MCP tool — bulk-tag nodes via semantic search query
- [x] REST API: tags in APINode, NodeUpdateRequest, nodeToAPI, handleNodeUpdate
- [x] API test for tag creation and update
- [x] Frontend: tags field in TypeScript types and SimNode
- [x] Graph grouping: "Group by tag" with weighted centroid pull (multi-tag nodes bridge between islands)
- [x] Detail panel: tag badges with sage-green pill styling
- [x] Filter sidebar: clickable tag filter chips
- [x] Updated transport test for 5 MCP tools

## Phase 21: Multi-Namespace Graph & Bridge Visualization — COMPLETE (2026-04-07)

Multi-namespace view shows all namespaces as distant islands on the same canvas, with cross-namespace bridge edges rendered as curved Bezier arcs ("marmot tunnels").

- [x] New `GET /api/graph/_all` endpoint — returns nodes from all namespaces plus bridge edges
- [x] `GraphResponse.Namespaces` field for multi-namespace metadata
- [x] Bridge edges discovered via `DiscoverCrossNamespaceEdges()`, class: "bridge"
- [x] "All namespaces" option in namespace selector (when 2+ namespaces exist)
- [x] Bridge arcs: quadratic Bezier curves with 40% perpendicular offset, amber dashed stroke
- [x] Bridge node indicator: sage-green ring on nodes participating in bridge edges
- [x] Namespace watermark labels: 28px uppercase text at island centroids, 8% opacity
- [x] Auto-switches to namespace grouping in all-namespaces view
- [x] Edge class filter supports 'bridge' value
- [x] Bridge arc hover highlighting with neighbor dimming
- [x] `fetchGraphAll()` and `fetchBridges()` API functions in frontend

## Phase 22: Folder Grouping with Contour Hulls — COMPLETE (2026-04-07)

"Group by folder" mode clusters nodes into topographic contour islands based on their directory prefix, rendered as organic Catmull-Rom hulls with an alpine cartographic color palette.

- [x] Add 'folder' mode to groupBy system
- [x] Extract folder prefix from node IDs (e.g. `packages/node` → `packages`)
- [x] Implement folder-based force island layout (forceX/forceY at 0.18 strength)
- [x] Render topographic contour hulls (convex hull with Catmull-Rom curves)
- [x] Support 1-node circles, 2-node capsules, 3+ node organic hulls
- [x] Alpine cartographic color palette (8 warm territory fills)
- [x] Folder name labels at hull centroids (IBM Plex Mono, uppercase, faded)
- [x] Hull updates on every simulation tick for fluid animation
- [x] Hull respects type/tag visibility filters
- [x] Fixed forceCenter competition with all groupBy modes
- [x] Fixed tag grouping dilution (primary tag strategy)
- [x] Fixed heat map persistence (`heatmap.Save()` after `RecordCoAccess`)
- [x] Add 'Group by folder' option to toolbar dropdown
- [x] CSS styles for `folder-hull` and `folder-label` elements

## Future Enhancements (Research-Informed, Deferred)

These enhancements are architecturally compatible but deferred until the core is stable.

### F1: Dual Retrieval Strategy
**Source**: Mem0g (2025), AriGraph (2024)

- [ ] Add entity extraction from queries (named entities, function names, identifiers)
- [ ] Implement entity-centric retrieval: exact ID/alias match -> edge traversal
- [ ] Merge entity-centric and semantic search results with weighted score fusion
- [ ] Add to `context_query` as a retrieval strategy option

### F2: Hierarchical Tier Promotion
**Source**: H-MEM (2025), Hybrid Multi-Layered Memory (2025)

- [ ] Add `tier` field to node frontmatter (`episodic`, `semantic`, `strategic`)
- [ ] Implement significance scoring
- [ ] Implement tier promotion: create higher-tier node with `contains` edges
- [ ] Update compaction engine to prefer higher-tier nodes when budget is tight

### F3: Ebbinghaus Decay with Reactivation
**Source**: Hou et al. (2024)

- [ ] Add per-node `access_weight` and `last_accessed` to heat map
- [ ] Implement reactivation boost on query hit
- [ ] Implement per-node decay with floor
- [ ] Integrate node access weights into traversal priority scoring

### F4: RL-Optimized Edge Weights
**Source**: "From Experience to Strategy: Trainable Graph Memory" (2025)

- [ ] Define agent feedback interface (task success/failure signal)
- [ ] Implement edge weight reinforcement from task outcomes
- [ ] Requires: agent feedback loop infrastructure

### F5: Retroactive Node Self-Organization
**Source**: A-MEM (2025, NeurIPS)

- [ ] On ADD, identify existing nodes whose context should be updated
- [ ] Implement retroactive edge creation and description refinement
- [ ] Requires: careful scoping to avoid cascading rewrites

---

## Dependency Graph

```
MVP:
  Phase M1 (Bootstrap)
    └─> Phase M2 (Node Store)
          ├─> Phase M3 (Graph Manager)
          │     └─> Phase M4 (Verifier)
          └─> Phase M5 (Embedding Index)
    Phase M3 + M4 + M5
      └─> Phase M6 (Traversal + Compaction)
            └─> Phase M7 (MCP Server + CLI)

    *** MVP BENCHMARK HERE ***

Post-MVP:
  Phase M7
    ├─> Phase 8 (Temporal Fields)
    ├─> Phase 9 (CRUD Classifier) — requires LLM Provider
    ├─> Phase 10 (Concurrency + Git)
    └─> Phase 11 (Namespaces + Bridges)

  Phase 8 + 9
    └─> Phase 12 (Heat Map)
    └─> Phase 13 (Summary + Update Engine)
    └─> Phase 14 (Embedding Model Mgmt)

  Phase 11 + 12 + 13
    └─> Phase 15 (Full CLI)
    └─> Phase 16 (Slim HTTP API)

  Phase M3 + M4
    └─> Phase 17 (Static Analysis Indexer)

  Phase 9 + 10 + 11 + 12 + 13 + 15 + 17
    └─> Phase 18 (Integration Testing)

  Phase 11 + 18
    └─> Phase 18.5 (Cross-Vault Bridges)
          └─> Phase 18.6 (Routing Table + Relative Paths)

  Phase 16 (Slim HTTP API)
    └─> Phase 19 (Graph Viz Frontend)
          └─> Phase 20 (Tags & Clustering)
          └─> Phase 21 (Multi-Namespace Bridges)
          └─> Phase 22 (Folder Grouping + Contour Hulls)

  Phase 18.6 (when stable)
    └─> F1-F5 (Future Enhancements) [DEFERRED]
```

## Parallel Work Streams (Post-MVP)

Once MVP is validated, three streams can proceed in parallel:

| Stream A (Lifecycle) | Stream B (Intelligence) | Stream C (Scale) |
|---------------------|------------------------|-------------------|
| Phase 8: Temporal Fields | Phase 9: CRUD Classifier | Phase 11: Namespaces |
| Phase 10: Concurrency + Git | Phase 12: Heat Map | Phase 14: Embedding Mgmt |
| | Phase 13: Summary + Update | Phase 16: Slim HTTP API |

Streams converge at Phase 15 (Full CLI) and Phase 18 (Integration Testing).

Visualization is handled by Obsidian from Phase M2 onward — every node file
written is immediately visible in the vault. Custom D3 visualization (Phase 19)
replaces Obsidian for advanced features: node coloring/sizing, clustering,
edge styling, inline editing, and heat overlays.
