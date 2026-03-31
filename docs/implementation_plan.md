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

All M1-M7 phases implemented and tested. 156 tests passing (148 unit + 8 integration) across 8 packages.
Production hardened with path traversal protection, input validation, and race-safe operations.

**Implementation notes:**
- sqlite-vec Go bindings have a WASM ABI mismatch with ncruces v0.17.1 — embedding search uses Go-side KNN (L2 distance with max-heap) instead. Upgradable when bindings are fixed.
- MCP SDK: used `mark3labs/mcp-go` (official Go SDK import paths were unstable at time of implementation).
- Pluggable embedding providers: OpenAI (`text-embedding-3-small`, `text-embedding-3-large`, `text-embedding-ada-002`) and Mock (trigram hashing). Config-driven selection via `_config.md` frontmatter with env var API keys and graceful fallback to mock.
- `internal/config` package provides vault config parsing (`_config.md`) and embedder factory (`NewEmbedderFromVault`).
- Batch embedding via `EmbedBatch()` for efficient indexing (OpenAI chunks at 100 inputs).
- Variable embedding dimensions supported per-namespace (1536 for small/ada, 3072 for large).
- `marmot index --force` rebuilds all embeddings (required after provider/model change).

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
- [x] Implement tool discovery (`tools/list`)
- [x] Set up CLI framework
- [x] Implement `marmot init` — initialize `.marmot/` vault structure
- [x] Implement `marmot index` — index node files into embedding store (`--force` to rebuild)
- [x] Implement `marmot query <query>` — run a context query and print XML result
- [x] Implement `marmot verify` — run integrity/staleness checks
- [x] Implement `marmot serve` — start MCP server
- [x] Write integration tests for full query/write/verify flows
- [ ] **Benchmark: test with Claude Code as MCP client, measure vs vanilla file reading**

---

## Post-MVP Phases

After MVP validates the core hypothesis, layer in full features.

## Phase 8: Temporal Fields + Soft-Delete

- [ ] Add temporal fields to Node struct: `Status`, `ValidFrom`, `ValidUntil`, `SupersededBy`
- [ ] Update markdown parser/writer for temporal frontmatter
- [ ] Implement `SoftDeleteNode(path, superseded_by) -> error`
- [ ] Implement `ListActiveNodes(namespace_path) -> []NodeMeta` (excludes superseded)
- [ ] Update Graph Manager: `SupersedeNode(old_id, new_node) -> error`
- [ ] Update Graph Manager: separate active-node index and full-node index
- [ ] Update Embedding Index: `UpdateStatus(node_id, status)`, `include_superseded` param on search
- [ ] Update Compaction Engine: superseded node rendering with status attribute
- [ ] Update MCP `context_query`: `include_superseded` parameter
- [ ] Update Verifier: superseded-chain integrity checks
- [ ] Write unit tests for soft-delete lifecycle
- [ ] Write unit tests for temporal queries

## Phase 9: CRUD Classifier (LLM-Based)

- [ ] Define `LLMProvider` interface (classify, summarize)
- [ ] Implement Anthropic provider (Haiku-class model, structured output)
- [ ] Implement `FindSimilar(embedding, threshold) -> []ScoredNodeID` in Embedding Index
- [ ] Implement CRUD classification flow:
  - [ ] Embed incoming node summary
  - [ ] Retrieve top-k similar candidates
  - [ ] If candidates: send to LLM for ADD/UPDATE/SUPERSEDE/NOOP classification
  - [ ] If no candidates: classify as ADD
- [ ] Implement fallback: pure embedding distance when LLM unavailable
- [ ] Integrate into `context_write` handler (replace simple upsert)
- [ ] Write integration tests for all 4 classification paths
- [ ] Write tests for LLM-unavailable fallback

## Phase 10: Concurrency + Git Strategy

- [ ] Implement namespace-level write mutex
- [ ] Implement atomic file writes (temp file + rename) — verify from M2
- [ ] Implement git auto-commit batching (configurable window, default 5s)
- [ ] Implement auto-generated commit messages
- [ ] Add `--no-autocommit` flag to `marmot serve`
- [ ] Write concurrent write tests (multiple goroutines/agents writing to same namespace)
- [ ] Write concurrent read-during-write tests

## Phase 11: Namespace Manager + Bridges

Project isolation + cross-namespace references.

- [ ] Define `Namespace` struct
- [ ] Implement `LoadNamespace(path) -> Namespace`
- [ ] Implement `CreateNamespace(name, root_path) -> Namespace`
- [ ] Implement namespace config parsing (`_namespace.md` YAML frontmatter)
- [ ] Implement qualified ID resolution (namespace + node_id from path)
- [ ] Define `Bridge` struct (allowed_relations whitelist only — no manual edge list)
- [ ] Implement bridge manifest parsing (`_bridges/*.md` YAML frontmatter)
- [ ] Implement `ValidateCrossNamespaceEdge(edge, bridges) -> error` (check relation in whitelist)
- [ ] Auto-discover cross-namespace edges by scanning node files
- [ ] Implement `ListNamespaces(marmot_root) -> []Namespace`
- [x] Define `Config` struct (global config) — `internal/config/config.go`
- [x] Implement global config parsing (`_config.md` YAML frontmatter) — `internal/config/config.go`
- [ ] Write unit tests for namespace resolution and bridge validation

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
