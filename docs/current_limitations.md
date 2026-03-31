# Current Limitations

This document covers known limitations in the ContextMarmot MVP (Phases M1-M7) as of 2026-03-31. Each entry describes what the limitation is, why it exists, its impact on users, and the planned resolution.

---

## Embedding & Search

### Go-side KNN search

**What:** Embedding search performs a full table scan in Go, computing L2 distance between the query vector and every stored embedding, then extracting the top-K results via a max-heap. There is no hardware-accelerated vector index.

**Why:** The sqlite-vec WASM bindings (`asg017/sqlite-vec-go-bindings/ncruces`) have an ABI mismatch with `ncruces/go-sqlite3` v0.17.1. The `vec0` virtual table cannot be loaded, so the system falls back to storing embeddings as raw BLOBs in a standard SQLite table and doing distance computation in Go.

**Impact:** Performant for hundreds to low thousands of nodes per namespace. At 100k+ nodes, the linear scan becomes a bottleneck -- every query reads and deserializes every embedding. Search latency scales linearly with node count.

**Resolution:** Upgradable in place when the `sqlite-vec-go-bindings` package ships a fix for the ncruces ABI. The Store already stores embeddings in the correct serialization format. Migration is a schema addition (create the `vec0` virtual table, populate it from the existing `embeddings` table) and a swap in the `Search` method.

### Embedding providers

**What:** Two `Embedder` implementations are available: `MockEmbedder` (used by default) and `OpenAIEmbedder` (supports `text-embedding-3-small`, `text-embedding-3-large`, and `text-embedding-ada-002`). No Voyage AI, Ollama, or other provider is available yet.

**Why:** OpenAI provides the most widely-used embedding API. The mock embedder remains the default so the tool works without any API key.

**Impact:** With the mock embedder (default), search quality is limited to lexical overlap --- texts must share character trigrams to match. With OpenAI embeddings enabled, semantic paraphrases match properly. Users must set `embedding_provider: openai` in `_config.md` and export `OPENAI_API_KEY` in their environment. If the key is missing, the system logs a warning and falls back to mock automatically. After changing providers, `marmot index --force` is required to regenerate embeddings.

**Resolution:** Add Voyage AI (`VoyageEmbedder`, Anthropic's recommended partner) and local/Ollama providers as post-MVP integrations. Any implementation of the `Embedder` interface (`Embed`, `EmbedBatch`, `Model`, `Dimension`) can be dropped in.

---

## Graph & Data Model

### Single namespace

**What:** The MVP supports one namespace per vault. There are no cross-project bridges, no namespace manager, and no qualified cross-namespace node references.

**Why:** Intentional MVP scoping. Multi-namespace adds complexity (bridge manifests, cross-namespace edge validation, qualified ID resolution) that is not needed to test the core hypothesis.

**Impact:** Each project requires its own `.marmot/` vault. Agents cannot query across related projects in a single operation. Shared libraries or microservice dependencies must be manually tracked.

**Resolution:** Phase 11 (Namespace Manager + Bridges). Adds `_namespace.md` per namespace, `_bridges/` directory with allowed-relation whitelists, qualified ID resolution, and cross-namespace edge validation.

### No temporal fields

**What:** All nodes have `status: active`. There is no `valid_from`, `valid_until`, or `superseded_by` field. Nodes cannot be soft-deleted or marked as superseded.

**Why:** Intentional MVP scoping. The node struct and markdown parser/writer handle only the active status.

**Impact:** When a function is refactored or renamed, the old node must be manually deleted or overwritten. There is no way to query historical state ("what was the auth flow last month?"), and no supersession chain linking old nodes to their replacements. Deleted knowledge is lost from the graph (though git retains file history).

**Resolution:** Phase 8 (Temporal Fields + Soft-Delete). Adds `status: superseded`, `status: disputed`, `valid_from`, `valid_until`, `superseded_by` fields. Implements `SoftDeleteNode` and `SupersedeNode` operations. Query results exclude superseded nodes by default but support `include_superseded` for temporal queries.

### No CRUD classifier

**What:** Writes via `context_write` are simple upserts. If a node with the given ID exists, it is overwritten. If it does not exist, it is created. There is no semantic deduplication.

**Why:** Intentional MVP scoping. The CRUD classifier requires an LLM provider (for ADD/UPDATE/SUPERSEDE/NOOP classification), which is a post-MVP dependency.

**Impact:** Duplicate nodes can accumulate if agents create nodes with different IDs for the same concept. Renamed functions produce orphaned old nodes. The system cannot distinguish between "this is a new thing" and "this is an update to an existing thing" -- it relies on the caller to provide the correct ID.

**Resolution:** Phase 9 (CRUD Classifier). On write, the engine embeds the incoming node, retrieves semantically similar candidates, and uses an LLM (Haiku-class) to classify the operation as ADD, UPDATE, SUPERSEDE, or NOOP. Falls back to pure embedding distance when the LLM is unavailable.

### No heat map

**What:** There is no co-access frequency tracking. Graph traversal treats all edges equally when selecting which paths to follow under budget constraints.

**Why:** Intentional MVP scoping. The heat map requires temporal infrastructure (decay model, periodic decay runs) and traversal integration that adds complexity beyond MVP scope.

**Impact:** Traversal cannot learn from usage patterns. If an agent frequently queries `auth/login` and `db/users/find` together, the system does not learn to prioritize the `auth/login -> db/users/find` path. Budget-constrained queries may follow less-useful edges instead of frequently co-accessed ones.

**Resolution:** Phase 12 (Heat Map). Adds per-namespace `_heat/<namespace>.md` sidecar files tracking co-access pairs with exponential decay (floor at 0.05 to prevent zero weights). Integrates into traversal edge priority scoring. Co-access is logged from `context_query` responses.

### No summary engine

**What:** There are no auto-generated `_summary.md` files per namespace. Agents must query the graph directly to orient themselves.

**Why:** Intentional MVP scoping. The summary engine requires an LLM provider for synthesis, an async regeneration scheduler, and change-detection triggers.

**Impact:** Agents cannot get a fast high-level overview of a namespace without running a broad query. There is no precomputed "table of contents" for the graph. Orientation in unfamiliar namespaces requires multiple queries.

**Resolution:** Phase 13 (Summary Engine + Update Engine). Auto-generates `_summary.md` per namespace using an LLM. The summary is itself a node with wikilinks to key nodes, discoverable via search. Regeneration runs asynchronously and never blocks read/write operations.

---

## Infrastructure

### No git auto-commit

**What:** Changes to node files (via `context_write` or `marmot index`) are not automatically committed to git. The `.marmot/` directory must be committed manually.

**Why:** Intentional MVP scoping. Auto-commit requires batching logic (to avoid commit-per-write explosion), configurable windows, and auto-generated commit messages.

**Impact:** Users must remember to commit `.marmot/` changes. If the working directory is reset or files are lost before a manual commit, graph state is lost. There is no automatic versioning of knowledge evolution.

**Resolution:** Phase 10 (Concurrency + Git Strategy). Implements batched auto-commit with a configurable window (default 5 seconds). Multiple writes within the window are combined into a single commit. Auto-generated commit messages summarize which nodes were added or updated. Disableable via `--no-autocommit`.

### No file watcher

**What:** There is no automatic re-indexing when source files change on disk. If the source code changes, the graph becomes stale until `marmot index` is run manually.

**Why:** Intentional MVP scoping. File watching requires an fsnotify integration, incremental change detection, and staleness propagation through dependent nodes.

**Impact:** The graph can drift from the actual codebase between manual re-index runs. `context_verify` can detect staleness via hash comparison, but does not fix it. In active development, source hashes in node files may not match current source files.

**Resolution:** Phase 13 (Update Engine). Implements file watcher mode using fsnotify for continuous operation, plus batch update mode for on-demand scans. Change detection propagates staleness flags to dependent nodes via reverse edge traversal. Triggers summary regeneration on significant changes.

### No static analysis indexer

**What:** Nodes must be created manually, via `context_write` from an agent, or by writing markdown files directly. There is no automatic code-to-graph indexing from source ASTs.

**Why:** Intentional MVP scoping. Static analysis requires language-specific AST parsers and a mapping from code constructs to node types and edge relations.

**Impact:** Populating the graph for an existing codebase is a manual process. Agents or users must create nodes one at a time. The demo project uses a shell script (`seed.sh`) to generate sample nodes.

**Resolution:** Phase 17 (Static Analysis Indexer). Implements Go and TypeScript AST parsers that produce nodes with correct types and edge classifications. Includes a generic file-level indexer for unsupported languages. Supports incremental indexing (only re-index changed files) and integrates with the CRUD classifier for deduplication.

### No REST/WebSocket API

**What:** The only programmatic interfaces are MCP (stdio transport) and the CLI. There is no HTTP API.

**Why:** Intentional MVP scoping. MCP is the primary agent interface and sufficient for validating the core hypothesis.

**Impact:** Custom UIs, dashboards, or non-MCP integrations cannot consume graph data programmatically. The only visualization option is opening the `.marmot/` vault in Obsidian.

**Resolution:** Phase 16 (REST / WebSocket API). Adds HTTP endpoints for graph queries, node detail, search, heat map data, namespace listing, and bridge listing. WebSocket endpoint for live graph update push. CORS support for local development.

### No custom web UI

**What:** Graph visualization is Obsidian-only. There is no dedicated ContextMarmot web interface.

**Why:** Intentional deferral. Obsidian provides graph visualization, backlinks, and search for free by opening `.marmot/` as a vault. A custom UI is unnecessary until Obsidian no longer meets visualization needs.

**Impact:** Users must have Obsidian installed for visual graph exploration. There is no agent-specific view, no heat map overlay, and no interactive traversal UI. Obsidian's graph view does not distinguish between structural and behavioral edges or show relevance scores.

**Resolution:** Phase 19 (Custom Visualization Frontend, deferred). TypeScript + Vite application using Cytoscape.js, consuming the REST/WebSocket API from Phase 16. Includes node coloring by type, edge styling by relation, heat map overlay, staleness indicators, and namespace switching.

---

## Concurrency & Scale

### Single-connection SQLite

**What:** The embedding store (`Store`) holds a single `sqlite3.Conn` protected by a Go `sync.Mutex`. All reads and writes serialize at this mutex.

**Why:** The `ncruces/go-sqlite3` WASM-based driver uses a single-threaded WASM instance per connection. Concurrent access to one connection is not safe. Opening multiple connections to the same database file would require WAL mode configuration and additional coordination.

**Impact:** Sufficient for single-agent use and the expected MVP scale. Multi-agent concurrent access serializes at the mutex -- agents take turns. For read-heavy workloads this adds latency proportional to the number of concurrent agents. Write contention is low because writes are infrequent relative to reads.

**Resolution:** Phase 10 (Concurrency + Git Strategy) addresses the broader concurrency model. For the embedding store specifically, the mutex approach remains adequate unless benchmarks at scale show it as a bottleneck. If needed, a connection pool with WAL mode can be introduced without changing the `Store` interface.

### In-memory graph

**What:** The full graph (all nodes, edges, and adjacency lists) is loaded into memory at startup. There is no lazy loading or partitioning.

**Why:** Simplicity and performance for the expected scale. In-memory graph operations are O(1) lookups and O(edges) traversals with no I/O.

**Impact:** Fine for hundreds to thousands of nodes. Memory usage grows linearly with node count and edge density. For very large codebases (100k+ nodes), memory consumption may become significant. Startup time also increases linearly with the number of node files to parse.

**Resolution:** No specific phase targets this. If scale demands it, the graph can be partitioned by namespace (load only queried namespaces) or switched to a lazy-loading model that pages nodes in from disk. The in-memory approach is expected to remain viable well beyond current use cases.

### No namespace-level write mutex

**What:** Concurrent writes to the same vault are serialized only at the embedding store mutex. There is no dedicated write lock at the node store or graph manager level.

**Why:** Intentional MVP scoping. With a single namespace and single-agent use, the embedding store mutex provides sufficient serialization.

**Impact:** If two agents write to the same vault concurrently, node file writes are atomic (temp file + rename), so no file corruption occurs. However, the in-memory graph could see inconsistent state during multi-step operations (e.g., a supersede that involves deleting one node and creating another). The CRUD classifier (when added) would also be vulnerable to a TOCTOU race without a higher-level lock.

**Resolution:** Phase 10 (Concurrency + Git Strategy). Adds namespace-level write mutexes. Writes to different namespaces proceed in parallel. Writes within the same namespace serialize at the namespace lock, preventing CRUD classification races and multi-step operation inconsistencies.

---

## Token Estimation

### chars/4 heuristic

**What:** Token counts are estimated as `len(content) / 4`. This is the only `TokenEstimator` implementation.

**Why:** The heuristic is within 10-20% for English text and code, which is sufficient for budget allocation (deciding how many nodes fit in a response). It requires no external dependencies and no tokenizer model files.

**Impact:** Token budgets are approximate. A budget of 4000 tokens may produce output that is actually 3400-4600 tokens when measured by a real tokenizer. For most use cases this is acceptable -- the budget is a soft ceiling, not a billing boundary. Edge cases include non-Latin scripts (where chars/4 overestimates) and highly repetitive code (where it underestimates).

**Resolution:** The `TokenEstimator` interface is defined in the traversal package. A proper tokenizer (e.g., tiktoken for OpenAI models, or a BPE tokenizer for Claude) can be plugged in as an alternative implementation. No specific phase is assigned; this is a drop-in upgrade when precision becomes necessary.
