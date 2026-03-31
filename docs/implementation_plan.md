# ContextMarmot Implementation Plan

## Tech Stack

- **Core Engine**: Go
  - SQLite driver: `ncruces/go-sqlite3` (WASM, zero CGo)
  - sqlite-vec: `asg017/sqlite-vec-go-bindings/ncruces`
  - MCP SDK: `mark3labs/mcp-go` v0.46.0 (well-established, stdio transport)
  - Anthropic SDK: `anthropics/anthropic-sdk-go` (for post-MVP LLM features)
- **Embedding Index**: SQLite + sqlite-vec
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

All M1-M7 phases implemented and tested. 147 tests passing (139 unit + 8 integration).
Production hardened with path traversal protection, input validation, and race-safe operations.

**Implementation notes:**
- sqlite-vec Go bindings have a WASM ABI mismatch with ncruces v0.17.1 — embedding search uses Go-side KNN (L2 distance with max-heap) instead. Upgradable when bindings are fixed.
- MCP SDK: used `mark3labs/mcp-go` (official Go SDK import paths were unstable at time of implementation).
- MockEmbedder used for MVP (trigram hashing, 1536-dim vectors). Real embedding provider (OpenAI/Anthropic) is a post-MVP integration.

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

- [ ] Define structs: `Node`, `Edge`, `Source`, `Provenance`
  - [ ] `Edge` includes `Class` field (structural / behavioral)
- [ ] Implement markdown parser
  - [ ] Parse YAML frontmatter into `Node` struct (including `edges` array)
  - [ ] Parse markdown body to extract summary text (content before first `##` heading)
  - [ ] Parse `## Context` section for full context content
  - [ ] Extract `[[wikilinks]]` from body (for validation against frontmatter edges)
- [ ] Implement markdown writer (struct -> Obsidian-compatible markdown file)
  - [ ] Render YAML frontmatter from struct fields
  - [ ] Render summary as plain markdown paragraph
  - [ ] Generate `## Relationships` section with `[[wikilinks]]` from frontmatter edges
  - [ ] Render `## Context` section with fenced code blocks
- [ ] Implement atomic file writes (temp file + rename)
- [ ] Implement file operations
  - [ ] `LoadNode(path) -> Node`
  - [ ] `SaveNode(node, path) -> error` (atomic)
  - [ ] `DeleteNode(path) -> error`
  - [ ] `ListNodes(namespace_path) -> []NodeMeta` (lightweight, frontmatter only)
- [ ] Implement ID derivation from file path (`project-alpha/auth/login.md` -> `auth/login`)
- [ ] Write unit tests for parse/write roundtrip
- [ ] Write unit tests for malformed/edge-case files
- [ ] Verify output files render correctly in Obsidian (manual smoke test)

## Phase M3: Graph Manager

In-memory graph engine. Loads from node store, maintains adjacency lists.
Enforces structural acyclicity only — behavioral cycles allowed.

- [ ] Define `Graph` struct (adjacency list, reverse adjacency, node index)
- [ ] Implement `LoadGraph(namespace_path) -> Graph` (bulk load all nodes)
- [ ] Implement `AddNode(node) -> error` (insert into graph)
- [ ] Implement `RemoveNode(id) -> error` (remove + cascade edge cleanup)
- [ ] Implement `AddEdge(source, target, relation) -> error`
  - [ ] Classify edge as structural or behavioral based on relation type
  - [ ] Run acyclicity check only for structural edges
- [ ] Implement `RemoveEdge(source, target) -> error`
- [ ] Implement `GetNode(id) -> Node`
- [ ] Implement `GetEdges(id, direction) -> []Edge` (outbound / inbound)
- [ ] Implement `GetNeighbors(id, depth) -> []Node`
- [ ] Write unit tests for graph operations
- [ ] Write unit tests for structural cycles (rejected) vs behavioral cycles (allowed)
- [ ] Write benchmarks for load time + traversal on synthetic graphs

## Phase M4: Verifier

Structural acyclicity enforcement + hash-based integrity checking.

- [ ] Implement topological sort for structural cycle detection
- [ ] Implement `CheckStructuralAcyclicity(graph, proposed_edge) -> (bool, []string)` (returns cycle path if found; skips behavioral edges)
- [ ] Implement `ComputeNodeHash(node) -> string` (SHA-256 of content)
- [ ] Implement `ComputeSourceHash(source_path, lines) -> string`
- [ ] Implement `VerifyStaleness(node) -> StaleStatus` (compare stored vs current source hash)
- [ ] Implement `VerifyIntegrity(graph) -> []IntegrityIssue` (dangling edges, hash mismatches, structural cycles)
- [ ] Integrate structural acyclicity check into `Graph.AddEdge()` (reject structural cycles only)
- [ ] Write unit tests for cycle detection (various graph topologies, mixed edge classes)
- [ ] Write unit tests for staleness detection

## Phase M5: Embedding Index

SQLite + sqlite-vec for semantic search. Decay-agnostic.

- [ ] Set up native bindings for sqlite-vec
- [ ] Define `EmbeddingStore` interface
- [ ] Implement SQLite schema initialization (create tables on first run, including `model` column)
- [ ] Implement `Upsert(node_id, embedding, summary_hash, model) -> error`
- [ ] Implement `Search(query_embedding, top_k) -> []ScoredNodeID` (rejects cross-model queries)
- [ ] Implement `Delete(node_id) -> error`
- [ ] Implement `StaleCheck(node_id, current_hash) -> bool`
- [ ] Define `Embedder` interface (abstract over embedding provider)
- [ ] Implement OpenAI `text-embedding-3-small` embedder (or Anthropic equivalent)
- [ ] Implement embedding caching (skip re-embed if summary unchanged)
- [ ] Write integration tests with in-memory SQLite
- [ ] Write benchmarks for search at various collection sizes

## Phase M6: Graph Traversal + Compaction

BFS/DFS expansion + token-budget-aware compaction into XML output.

- [ ] Define `TraversalConfig` (depth, budget, mode)
- [ ] Implement `Traverse(entry_nodes, config) -> Subgraph`
- [ ] Implement token budget tracking (chars/4 heuristic, documented approximation)
- [ ] Implement frontier expansion with priority queue
- [ ] Implement depth limiting
- [ ] Define `CompactedResult` struct
- [ ] Implement adjacency compaction (shallow mode)
  - [ ] Full `<node>` for entry/high-relevance nodes (includes context)
  - [ ] `<node_compact>` for adjacent nodes (summary + source ref only)
  - [ ] `<truncated>` section listing over-budget node IDs
- [ ] Implement XML serialization with proper entity escaping
- [ ] Implement relevance scoring for node ranking within result
- [ ] Write unit tests for traversal with various budgets and depths
- [ ] Write unit tests for compaction output format
- [ ] Verify XML output is well-formed and parseable

## Phase M7: MCP Server + CLI

Minimal MCP server and CLI to enable agent testing.

- [ ] Set up MCP server framework
- [ ] Implement `context_query` tool handler
  - [ ] Parse parameters (query, depth, budget, namespace, mode)
  - [ ] Route to semantic search -> traversal -> compaction pipeline
  - [ ] Return XML compacted result
- [ ] Implement `context_write` tool handler (simple upsert, no CRUD classification in MVP)
  - [ ] Parse parameters (id, type, namespace, summary, context, edges, source)
  - [ ] Validate structural acyclicity
  - [ ] Write Obsidian-compatible node via node store
  - [ ] Update embedding index
  - [ ] Return confirmation with hash
- [ ] Implement `context_verify` tool handler
  - [ ] Parse parameters (node_ids, namespace, check)
  - [ ] Route to verifier (staleness / integrity / all)
  - [ ] Return verification report
- [ ] Implement tool discovery (`tools/list`)
- [ ] Set up CLI framework
- [ ] Implement `marmot init` — initialize `.marmot/` vault structure
- [ ] Implement `marmot query <query>` — run a context query and print XML result
- [ ] Implement `marmot serve` — start MCP server
- [ ] Write integration tests for full query/write/verify flows
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
- [ ] Define `Config` struct (global config)
- [ ] Implement global config parsing (`_config.md` YAML frontmatter)
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

- [ ] Store `model` field per embedding in SQLite
- [ ] Reject cross-model similarity queries
- [ ] Implement `marmot reembed --namespace <ns>` command (regenerate all embeddings)
- [ ] Handle namespace config `embedding_model` changes gracefully (warn + require reembed)
- [ ] Write tests for model mismatch detection and migration

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
