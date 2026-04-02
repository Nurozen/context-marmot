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
- **Visualization (future)**: TypeScript + Vite web UI (Phase 19), consumes REST/WebSocket API
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

## Phase 12: Heat Map

Co-access frequency tracking. Decay affects traversal priority only.

- [ ] Define `HeatMap` struct (including `decay_floor` field)
- [ ] Implement heat map file parsing (`_heat/<namespace>.md` YAML frontmatter)
- [ ] Implement heat map file writing
- [ ] Implement `RecordCoAccess(node_ids []string) -> error` (pairwise weight update)
- [ ] Implement `GetWeights(node_ids []string) -> map[pair]float64`
- [ ] Implement `Decay() -> error` (apply decay_rate, enforce decay_floor — weights never reach zero)
- [ ] Implement promotion detection (`weight >= threshold` for sustained period)
- [ ] Integrate heat map into Graph Traversal edge priority scoring
- [ ] Log co-access from `context_query` responses
- [ ] Write unit tests for weight accumulation, decay math, and floor enforcement

## Phase 13: Summary Engine + Update Engine

- [ ] **Summary Engine**
  - [ ] Implement `GenerateSummary(namespace) -> SummaryNode` using LLM Provider
  - [ ] Write `_summary.md` with `type: summary` frontmatter and wikilinks
  - [ ] Implement async regeneration scheduler (configurable interval)
  - [ ] Implement change-triggered regeneration (significant node count delta)
  - [ ] Ensure summary generation never blocks read/write operations
  - [ ] Graceful degradation: summaries go stale when LLM unavailable, not broken
- [ ] **Update Engine**
  - [ ] Implement `DetectChanges(namespace) -> []ChangedNode` (hash comparison)
  - [ ] Implement `PropagateStale(changed_ids, graph) -> []AffectedNode` (walk reverse edges)
  - [ ] Implement `Reindex(node_ids) -> error` (re-read source, update node, update embedding)
  - [ ] Implement file watcher mode (fsnotify or equivalent for continuous operation)
  - [ ] Implement batch update mode (on-demand scan)
  - [ ] Trigger summary regeneration on significant changes
  - [ ] Write unit tests for change detection and propagation

## Phase 14: Embedding Model Management

- [x] Store `model` field per embedding in SQLite
- [x] Reject cross-model similarity queries
- [x] Implement `marmot index --force` (regenerate all embeddings — serves as reembed)
- [x] Handle config `embedding_model` changes gracefully (warn + require `--force` reindex)
- [x] Write tests for model mismatch detection and migration

## Phase 15: Full CLI

Complete command-line interface.

- [ ] Implement `marmot index <path>` — index a source directory into a namespace
- [ ] Implement `marmot verify [--namespace]` — run integrity/staleness checks
- [ ] Implement `marmot status` — show namespace stats, node counts, stale counts, superseded counts
- [ ] Implement `marmot watch` — start file watcher + auto-reindex
- [ ] Implement `marmot bridge <ns-a> <ns-b>` — create bridge manifest
- [ ] Implement `marmot summarize [--namespace]` — force summary regeneration
- [ ] Implement `marmot reembed [--namespace]` — regenerate embeddings after model change
- [ ] Write CLI integration tests

## Phase 16: REST / WebSocket API

HTTP + WebSocket interface. Enables future custom UIs without coupling to Obsidian.

- [ ] Define REST API endpoints
  - [ ] `GET /api/graph/:namespace` — full graph as JSON (active nodes by default)
  - [ ] `GET /api/graph/:namespace?include_superseded=true` — include superseded
  - [ ] `GET /api/node/:namespace/:id` — single node detail as JSON
  - [ ] `GET /api/search?q=...&ns=...` — search nodes
  - [ ] `GET /api/heat/:namespace` — heat map data
  - [ ] `GET /api/namespaces` — list all namespaces
  - [ ] `GET /api/bridges` — list all bridges
  - [ ] `GET /api/summary/:namespace` — get namespace summary
- [ ] Implement WebSocket endpoint for live graph updates
- [ ] Implement CORS for local dev
- [ ] Serve static frontend assets in production mode (when frontend exists)
- [ ] Write API integration tests

## Phase 17: Static Analysis Indexer

Automated code-to-graph indexer (first use case enabler).

- [ ] Define `Indexer` interface
- [ ] Implement Go indexer (parse Go AST -> nodes + edges with correct edge classes)
- [ ] Implement TypeScript indexer (parse TS AST -> nodes + edges)
- [ ] Implement generic file indexer (module-level nodes for unsupported languages)
- [ ] Implement incremental indexing (only re-index changed files)
  - [ ] On re-index, use CRUD classifier to determine ADD/UPDATE/SUPERSEDE per node
- [ ] Implement ignore patterns (respect `.gitignore` + namespace config globs)
- [ ] Integrate with `marmot index` CLI command
- [ ] Write tests against sample codebases

## Phase 18: Integration Testing & Hardening

- [ ] End-to-end test: index a real project -> query via MCP -> verify results
- [ ] End-to-end test: mutual recursion in code -> verify behavioral cycles preserved
- [ ] End-to-end test: multi-namespace with bridges -> cross-project queries
- [ ] End-to-end test: open indexed vault in Obsidian, verify graph renders correctly
- [ ] End-to-end test: CRUD lifecycle — ADD -> UPDATE -> SUPERSEDE -> verify temporal chain
- [ ] End-to-end test: summary generation after indexing, verify wikilinks resolve
- [ ] End-to-end test: concurrent agents writing to same namespace
- [ ] Load test: synthetic graph with 10k+ nodes -> query performance
- [ ] Fuzz test: malformed node files, invalid edges, corrupt embeddings
- [ ] CI/CD pipeline setup (GitHub Actions)

---

## Phase 19 (Deferred): Custom Visualization Frontend

Interactive graph UI. Deferred until Obsidian no longer meets visualization needs.
When built, consumes the REST/WebSocket API from Phase 16.

- [ ] Initialize TypeScript + Vite project in `web/`
- [ ] Set up Cytoscape.js (or selected graph library) with base configuration
- [ ] Implement graph data fetching from REST API
- [ ] Implement node rendering (color by type, size by edge count, namespace grouping)
- [ ] Implement edge rendering (style by relation, opacity by heat weight)
- [ ] Implement DAG layout (dagre/hierarchical) + force-directed alternative
- [ ] Implement interactions (click, hover, expand, search, filter)
- [ ] Implement heat map overlay toggle
- [ ] Implement staleness indicators
- [ ] Implement namespace switcher / multi-namespace view
- [ ] Write component tests

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
    └─> Phase 16 (REST/WS API)

  Phase M3 + M4
    └─> Phase 17 (Static Analysis Indexer)

  Phase 9 + 10 + 11 + 12 + 13 + 15 + 17
    └─> Phase 18 (Integration Testing)

  Phase 16 (when needed)
    └─> Phase 19 (Custom Viz Frontend) [DEFERRED]

  Phase 18 (when stable)
    └─> F1-F5 (Future Enhancements) [DEFERRED]
```

## Parallel Work Streams (Post-MVP)

Once MVP is validated, three streams can proceed in parallel:

| Stream A (Lifecycle) | Stream B (Intelligence) | Stream C (Scale) |
|---------------------|------------------------|-------------------|
| Phase 8: Temporal Fields | Phase 9: CRUD Classifier | Phase 11: Namespaces |
| Phase 10: Concurrency + Git | Phase 12: Heat Map | Phase 14: Embedding Mgmt |
| | Phase 13: Summary + Update | Phase 16: REST/WS API |

Streams converge at Phase 15 (Full CLI) and Phase 18 (Integration Testing).

Visualization is handled by Obsidian from Phase M2 onward — every node file
written is immediately visible in the vault.
