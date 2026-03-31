# ContextMarmot Data Structures

## Format Philosophy

- **On disk**: YAML frontmatter + Markdown + `[[wikilinks]]`. Human-readable, Obsidian-compatible, git-friendly.
- **To agents**: XML tags for semantic delineation. Avoids ambiguity when content is injected into an LLM context window.
- **To custom UIs**: JSON via REST API.

The same underlying data is represented differently depending on the consumer.

---

## Node (Markdown File)

Each node is a single markdown file within a namespace folder.
The file path relative to the namespace determines the node ID (e.g., `project-alpha/auth/login.md` -> `auth/login`).

### On-Disk Format

```markdown
---
id: auth/login
type: function
namespace: project-alpha
status: active
valid_from: 2026-03-30T10:00:00Z
valid_until: null
superseded_by: null
source:
  path: src/auth/login.ts
  lines: [15, 42]
  hash: a3f8b2c1d4e5f6
created: 2026-03-30T10:00:00Z
modified: 2026-03-30T14:30:00Z
provenance:
  agent: indexer-v1
  source_type: static_analysis
edges:
  - target: auth/validate_token
    relation: calls
  - target: db/users/find
    relation: reads
  - target: project-beta/api/session
    relation: cross_project
---

Handles user login with OAuth2 flow. Validates credentials,
generates JWT token, and establishes session.

## Relationships

- **calls** [[auth/validate_token]]
- **reads** [[db/users/find]]
- **cross_project** [[project-beta/api/session]]

## Context

```typescript
export async function login(credentials: Credentials): Promise<Session> {
  const user = await findUser(credentials.email);
  const token = await validateToken(credentials);
  return createSession(user, token);
}
```​

```

**Dual representation of edges:**
- `edges` in YAML frontmatter: machine-readable, typed, used by ContextMarmot engine.
- `[[wikilinks]]` in markdown body: used by Obsidian for graph rendering and backlinks.
- The engine is authoritative. Wikilinks are generated from frontmatter on write.

### Node Types

| Type | Description | Typical Source |
|------|-------------|----------------|
| `function` | A function or method | Static analysis |
| `module` | A file or module | Static analysis |
| `class` | A class definition | Static analysis |
| `concept` | An abstract idea or domain concept | Agent-generated |
| `decision` | An architectural decision or rationale | Agent-generated |
| `reference` | A pointer to external resource | Agent or human |
| `composite` | A grouping of related nodes | Compaction engine |
| `summary` | Auto-generated namespace summary | Summary engine |

### Frontmatter Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | yes | Unique within namespace. Derived from file path. |
| `type` | enum | yes | One of the node types above. |
| `namespace` | string | yes | Owning namespace. |
| `status` | enum | yes | `active`, `superseded`, `disputed`. Default: `active`. |
| `valid_from` | datetime | yes | When this node's content became true/valid. |
| `valid_until` | datetime | no | When this node stopped being valid. `null` = still valid. |
| `superseded_by` | string | no | Node ID that replaces this one. `null` = not superseded. |
| `source.path` | string | no | Relative path to source file. |
| `source.lines` | [int, int] | no | Start and end line in source. |
| `source.hash` | string | no | SHA-256 of source content at index time. |
| `created` | datetime | yes | ISO 8601 creation timestamp. |
| `modified` | datetime | yes | ISO 8601 last modification timestamp. |
| `provenance.agent` | string | yes | Agent or tool that created/last modified this node. |
| `provenance.source_type` | enum | yes | `static_analysis`, `agent_generated`, `human`, `inferred`. |
| `edges` | []Edge | no | List of typed, directed edges from this node. |

### Temporal Fields

Nodes carry temporal metadata for tracking knowledge evolution without deleting history.
Inspired by Graphiti's bi-temporal modeling and Mem0's soft-delete pattern.

- **`status: active`** — Current, valid node. Default for all new nodes.
- **`status: superseded`** — Replaced by another node. `superseded_by` points to the replacement. The file remains on disk (Obsidian still shows it, git tracks the history). Superseded nodes are excluded from query results by default but can be included with a `include_superseded` flag.
- **`status: disputed`** — Conflicting information detected. Requires human or agent review.
- **`valid_from` / `valid_until`** — When the fact or code described by this node was true. Enables temporal queries like "what was the auth flow in January?" by filtering on validity windows. For code nodes, `valid_from` is set on creation; `valid_until` is set when the source changes and a new node is created.

**Soft-delete, not hard-delete.** When a node is superseded:
1. Old node gets `status: superseded`, `valid_until: <now>`, `superseded_by: <new_id>`.
2. New node gets `status: active`, `valid_from: <now>`.
3. Old node's wikilinks gain a `(superseded)` suffix in the markdown body for Obsidian visibility.
4. Git tracks the full history of both files.

For in-place evolution (same node, content changes), just update the node normally — git diff is the history. Soft-delete is for when a node is *replaced* by a structurally different one.

### Superseded Node Example

```markdown
---
id: auth/login_v1
type: function
namespace: project-alpha
status: superseded
valid_from: 2026-01-15T10:00:00Z
valid_until: 2026-03-30T14:30:00Z
superseded_by: auth/login
source:
  path: src/auth/login.ts
  lines: [15, 80]
  hash: old_hash_here
created: 2026-01-15T10:00:00Z
modified: 2026-03-30T14:30:00Z
provenance:
  agent: indexer-v1
  source_type: static_analysis
edges:
  - target: auth/validate_token_v1
    relation: calls
---

*(Superseded by [[auth/login]])*

Legacy login handler using basic auth. Replaced by OAuth2 implementation.

## Relationships

- **calls** [[auth/validate_token_v1]] *(superseded)*

## Context

```typescript
// Legacy implementation — see auth/login for current version
```​

```

---

## CRUD Classification on Write

When `context_write` is called, the engine does not blindly upsert. It classifies the
operation to keep the graph clean and prevent unbounded growth. Inspired by Mem0's
LLM-as-classifier pattern.

### Classification Flow

1. **Embed** the incoming node's summary.
2. **Retrieve** top-k semantically similar existing nodes from the embedding index.
3. **If candidates found**: Send the incoming node + candidates to the **LLM Provider**
   for semantic classification. The LLM determines the operation using structured output
   (function calling), considering name changes, structural differences, and semantic
   equivalence — not just embedding distance.
4. **If no candidates**: Classify as ADD (no similar node exists).

| Operation | Condition | Action |
|-----------|-----------|--------|
| **ADD** | No semantically similar node exists, or LLM determines this is genuinely new. | Create new node file. |
| **UPDATE** | LLM determines incoming node is an evolution of an existing node (same identity, changed content). | Update existing node file in-place. Git tracks the diff. |
| **SUPERSEDE** | LLM determines incoming node structurally replaces an existing node (different signature, different file, architectural change). | Soft-delete old node, create new node with `superseded_by` link. |
| **NOOP** | LLM determines content is semantically equivalent to an existing node. | Skip write, return existing node ID. |

### Why LLM, Not Just Embedding Distance

A single embedding similarity threshold cannot reliably distinguish these cases:
- **Renamed function** (same behavior, different name): embedding says NOOP, should be SUPERSEDE
- **Same name, different behavior**: embedding says UPDATE, might need SUPERSEDE
- **Similar descriptions, different functions**: embedding says UPDATE, should be ADD

The embedding step is **candidate retrieval** (fast, cheap). The LLM step is
**semantic classification** (slower, but accurate). Uses a Haiku-class model with
structured output — fast and cheap enough for the write path.

**Fallback**: If the LLM provider is unavailable, the classifier falls back to
pure embedding distance with the `crud_similarity_threshold`. Less accurate but
functional — the system degrades gracefully.

### Deduplication

The primary goal is preventing duplicate nodes. Before creating a new node, the engine
checks if a semantically equivalent one already exists. If so, it routes to UPDATE
or NOOP instead of ADD.

This replaces naive "information content gating" — nodes are always updated to reflect
current reality (code can shrink, simplify, or grow). Git handles the history of what
changed and when.

---

## Async Summary Nodes

Each namespace has an auto-generated `_summary.md` that provides a fast global context
injection point. Agents can read the summary first, then drill into the graph only if
they need detail.

### Format

```markdown
---
id: _summary
type: summary
namespace: project-alpha
status: active
valid_from: 2026-03-30T14:00:00Z
created: 2026-03-30T10:00:00Z
modified: 2026-03-30T14:00:00Z
provenance:
  agent: summary-engine
  source_type: agent_generated
node_count: 42
last_regenerated: 2026-03-30T14:00:00Z
---

project-alpha is an authentication and user management service.

## Key Components

- **Auth flow**: [[auth/login]] handles OAuth2 login, delegates to
  [[auth/validate_token]] for JWT validation, reads from [[db/users/find]].
- **Session management**: [[api/session]] manages active sessions with
  cross-project integration to [[project-beta/api/session]].
- **Database layer**: [[db/users/find]], [[db/users/create]] provide
  PostgreSQL-backed user CRUD.

## Recent Changes

- auth/login refactored from basic auth to OAuth2 (2026-03-30)
- db/users/find optimized with index on email column (2026-03-28)
```

### Regeneration

- Summaries are regenerated **asynchronously** — not in the critical path of any write.
- Triggered periodically (configurable interval) or when node count changes significantly.
- Uses an LLM to synthesize the summary from all active nodes in the namespace.
- The summary itself is a node — it appears in Obsidian, has wikilinks to key nodes,
  and is discoverable via search.
- Agents can use `context_query` with `_summary` as a node ID for fast orientation.

---

## Edge

Edges are declared in YAML frontmatter. They are always **directed** (source -> target) where the source is the containing node.

### Edge Classes

Edges are classified as **structural** or **behavioral**. This distinction determines
whether cycles are allowed. Real code has mutual recursion (`A calls B, B calls A`),
circular imports, and bidirectional dependencies — a strict DAG cannot represent these.

| Class | Relations | Cycles? | Purpose |
|-------|-----------|---------|---------|
| **Structural** | `contains`, `imports`, `extends`, `implements` | **No** — acyclicity enforced | Hierarchy, module structure, type system |
| **Behavioral** | `calls`, `reads`, `writes`, `references`, `cross_project`, `associated` | **Yes** — cycles allowed | Runtime behavior, data flow, associations |

### Frontmatter Format

```yaml
edges:
  - target: auth/validate_token
    relation: calls          # behavioral — cycles OK
  - target: db/users/find
    relation: reads          # behavioral — cycles OK
  - target: auth/module
    relation: contains       # structural — acyclicity enforced
```

Cross-namespace edges reference the namespace folder path:
```yaml
edges:
  - target: project-beta/api/session
    relation: cross_project  # behavioral — cycles OK
```

### Wikilink Format (Markdown Body)

Generated from frontmatter. Provides Obsidian graph connectivity.

```markdown
## Relationships

- **calls** [[auth/validate_token]]
- **reads** [[db/users/find]]
- **cross_project** [[project-beta/api/session]]
```

### Relation Types

| Relation | Class | Description | Direction Semantics |
|----------|-------|-------------|---------------------|
| `contains` | Structural | Source contains target (module -> function) | Hierarchy |
| `imports` | Structural | Source imports/depends on target | Dependency |
| `extends` | Structural | Source extends target | Type hierarchy |
| `implements` | Structural | Source implements target interface | Type hierarchy |
| `calls` | Behavioral | Source invokes target | Control flow |
| `reads` | Behavioral | Source reads from target | Data flow |
| `writes` | Behavioral | Source writes to target | Data flow |
| `references` | Behavioral | Source mentions or links to target | Loose association |
| `cross_project` | Behavioral | Source relates to target in another namespace | Cross-namespace |
| `associated` | Behavioral | Promoted from heat map co-access | Inferred |

### Edge Rules

1. **Structural acyclicity**: Adding a structural edge that creates a cycle is rejected. Behavioral edges skip cycle checks.
2. **Target must exist**: Edges to nonexistent nodes are flagged as dangling (warning, not error — supports incremental builds).
3. **Cross-namespace edges**: Must reference a namespace that has a bridge manifest with the relation type in its `allowed_relations` whitelist. The bridge manifest is auto-derived — no manual edge list required.

---

## Heat Map (Sidecar File)

One per namespace. Stored in `_heat/`. Tracks co-access frequency for traversal prioritization.

### Scope and Boundaries

The heat map **only affects traversal priority** — which edges get followed when budget
is constrained. It never affects node existence or discoverability.

| Layer | Decays? | Effect of decay |
|-------|---------|-----------------|
| **Node files** | Never | Always exist, always readable |
| **Embedding index** | Never | Always discoverable by semantic search |
| **Heat map (co-access)** | Yes | Reduces traversal priority when budget-constrained |

This means old, untouched code modules remain fully discoverable via direct query
or semantic search. They just won't appear as bonus context alongside actively-used
nodes. First access reactivates them immediately.

### Format

```markdown
---
namespace: project-alpha
last_decay: 2026-03-30T00:00:00Z
decay_rate: 0.95
decay_floor: 0.05
promotion_threshold: 0.80
pairs:
  - a: auth/login
    b: auth/validate_token
    weight: 0.82
    hits: 47
    last: 2026-03-30T14:00:00Z
  - a: auth/login
    b: db/users/find
    weight: 0.71
    hits: 38
    last: 2026-03-30T13:45:00Z
  - a: auth/login
    b: api/session
    weight: 0.34
    hits: 12
    last: 2026-03-29T09:00:00Z
---

Heat map for project-alpha. Auto-generated by ContextMarmot.
Do not edit manually unless you know what you're doing.
```

### Weight Update Formula

```
On co-access:
  weight = min(1.0, weight + (1.0 - weight) * learning_rate)
  hits += 1
  last = now()

On decay (scheduled):
  weight = max(decay_floor, weight * decay_rate)
```

The `decay_floor` (default: 0.05) ensures weights never reach zero. Even ice-cold
nodes maintain a nonzero traversal priority, so they aren't completely invisible
in broad exploratory queries.

### Promotion

When `weight >= promotion_threshold` for a sustained period, the pair is a candidate
for promotion to an explicit `associated` edge in both nodes. This requires verification
(an agent or human confirms the relationship is meaningful).

---

## Bridge Manifest

Declares which relation types are allowed between two namespaces. Stored in `_bridges/`.
The actual edge list is **auto-derived** by scanning node files for cross-namespace
edges — no manual edge bookkeeping in the bridge.

### Format

```markdown
---
source: project-alpha
target: project-beta
created: 2026-03-30T10:00:00Z
allowed_relations:
  - cross_project
  - calls
  - reads
---

Bridge between [[project-alpha/_namespace]] and [[project-beta/_namespace]].

Allowed cross-namespace relation types: `cross_project`, `calls`, `reads`.

Actual edges are declared in individual node files and auto-discovered by the engine.
```

### How Cross-Namespace Edges Work

1. A node in `project-alpha` declares an edge to `project-beta/api/session` in its frontmatter.
2. On load, the engine checks if a bridge manifest exists for `project-alpha -> project-beta`.
3. The engine checks if the edge's relation type is in `allowed_relations`.
4. If allowed, the edge is indexed. If not, it's flagged as a validation warning.

The bridge manifest is a **whitelist of relation types**, not an inventory of edges.
This eliminates dual bookkeeping and drift — edges live in node files only.

---

## Namespace Config

Per-namespace metadata. Stored as `_namespace.md` in each namespace folder.

### Format

```markdown
---
name: project-alpha
root_path: /Users/dev/projects/alpha
created: 2026-03-30T10:00:00Z
settings:
  auto_index: true
  source_globs:
    - "src/**/*.ts"
    - "src/**/*.tsx"
  ignore_globs:
    - "node_modules/**"
    - "dist/**"
    - "*.test.ts"
  embedding_model: text-embedding-3-small
  summary_regeneration_interval: 1h
---

Namespace configuration for project-alpha.
```

---

## Global Config

Top-level configuration. Stored as `_config.md` in vault root.

### Format

```markdown
---
version: 0.1.0
created: 2026-03-30T10:00:00Z
settings:
  heat_decay_interval: 24h
  heat_learning_rate: 0.1
  heat_decay_floor: 0.05
  default_query_depth: 3
  default_query_budget: 4000
  compaction_mode: adjacency
  crud_similarity_threshold: 0.85
  git_commit_batch_window: 5s
  llm_provider: anthropic
  llm_model: claude-haiku-4-5
  llm_api_key_env: ANTHROPIC_API_KEY
namespaces:
  - name: project-alpha
    path: project-alpha
  - name: project-beta
    path: project-beta
---

ContextMarmot global configuration.
```

---

## Embedding Index (SQLite)

One per namespace. Stored in `.marmot-data/<namespace>/embeddings.db` (gitignored).

Embedding search is **decay-agnostic** — it always returns results regardless of how
recently a node was accessed. This ensures old codebases remain fully discoverable.

### Schema

```sql
CREATE TABLE nodes (
    id TEXT PRIMARY KEY,          -- node ID (e.g., "auth/login")
    embedding BLOB NOT NULL,      -- vector embedding of node summary
    summary_hash TEXT NOT NULL,   -- hash of summary text (reindex on change)
    status TEXT NOT NULL,         -- mirrors node status (active/superseded/disputed)
    model TEXT NOT NULL,          -- embedding model that generated this vector
    updated_at TEXT NOT NULL      -- ISO 8601 timestamp
);

-- Vector similarity index (via sqlite-vec extension)
CREATE VIRTUAL TABLE node_embeddings USING vec0(
    id TEXT PRIMARY KEY,
    embedding FLOAT[1536]         -- dimension matches embedding model
);
```

**Model tracking**: The `model` column records which embedding model generated each
vector. `search()` and `find_similar()` reject queries if the query model doesn't
match stored embeddings. After changing `embedding_model` in namespace config, run
`marmot reembed --namespace <ns>` to regenerate all embeddings.

### Operations

| Operation | Description |
|-----------|-------------|
| `upsert(id, embedding, summary_hash)` | Insert or update node embedding |
| `search(query_embedding, top_k, include_superseded)` | Return top_k nearest node IDs. Excludes superseded by default. |
| `delete(id)` | Remove node from index |
| `stale_check(id, current_hash)` | Compare stored hash to detect if re-embedding needed |
| `find_similar(embedding, threshold)` | Return nodes above similarity threshold (used by CRUD classifier) |

---

## Compacted Output (Query Response to Agents)

The structured response returned to agents from `context_query`. **This is where XML
is used** — it provides unambiguous semantic delineation when injected into an LLM
context window, preventing the model from confusing node boundaries, metadata, and content.

### Format

```xml
<context_result query="how does login work" depth="3" nodes_accessed="7" budget_used="3200" budget_total="4000">

  <node id="auth/login" type="function" namespace="project-alpha" status="active" relevance="0.95">
    <summary>Handles user login with OAuth2 flow. Validates credentials, generates JWT token, and establishes session.</summary>
    <source path="src/auth/login.ts" lines="15-42" hash="a3f8b2c1..." />
    <context>
export async function login(credentials: Credentials): Promise&lt;Session&gt; {
  const user = await findUser(credentials.email);
  const token = await validateToken(credentials);
  return createSession(user, token);
}
    </context>
    <edges>
      <edge target="auth/validate_token" relation="calls" />
      <edge target="db/users/find" relation="reads" />
    </edges>
  </node>

  <node_compact id="auth/validate_token" type="function" namespace="project-alpha" status="active" relevance="0.78">
    <summary>Validates JWT token signature and expiry. Returns decoded claims or throws AuthError.</summary>
    <source path="src/auth/token.ts" lines="8-25" />
  </node_compact>

  <node_compact id="db/users/find" type="function" namespace="project-alpha" status="active" relevance="0.65">
    <summary>Queries user by email from PostgreSQL users table. Returns User or null.</summary>
    <source path="src/db/users.ts" lines="30-45" />
  </node_compact>

  <truncated count="4">
    <ref id="auth/oauth_config" namespace="project-alpha" />
    <ref id="db/connection_pool" namespace="project-alpha" />
    <ref id="api/session" namespace="project-beta" />
    <ref id="auth/jwt_sign" namespace="project-alpha" />
  </truncated>

</context_result>
```

### Compaction Rules

1. **Entry nodes** (highest relevance) get full `<node>` with complete context.
2. **Adjacent nodes** within budget get `<node_compact>` with summary + source ref only.
3. **Over-budget nodes** are listed in `<truncated>` so the agent knows they exist and can query deeper.
4. **Hard references** (`<source path="..." />`) always point to the original file so agents can `read_file()` if needed.
5. **XML entity escaping** in `<context>` blocks — source code containing `<`, `>`, `&` is escaped.
6. **Superseded nodes** are excluded by default. When `include_superseded` is set, they appear with `status="superseded"` and a `<superseded_by>` element.

### Why XML for Agent Output

Markdown is ambiguous in LLM context — the model can't reliably distinguish where one
node's context ends and another begins, or where metadata ends and content starts.
XML tags provide:
- **Unambiguous boundaries**: `<node>` ... `</node>` is a clear container.
- **Attribute metadata**: `id`, `type`, `relevance` are inline without consuming content tokens.
- **Nesting semantics**: `<edges>` inside `<node>` is unambiguous parent-child.
- **LLM familiarity**: Models are trained heavily on XML/HTML and parse it reliably.

---

## MCP Tool Interface

### context_query

```json
{
  "name": "context_query",
  "description": "Search the project knowledge graph for relevant context. Returns a structured subgraph compacted to fit the token budget. Use this before reading raw files — it returns pre-contextualized results with relationships.",
  "parameters": {
    "query": {
      "type": "string",
      "description": "Natural language query or node ID"
    },
    "depth": {
      "type": "integer",
      "default": 3,
      "description": "Max traversal depth from entry nodes"
    },
    "budget": {
      "type": "integer",
      "default": 4000,
      "description": "Max token budget for response"
    },
    "namespace": {
      "type": "string",
      "description": "Namespace to search. Omit for all namespaces."
    },
    "mode": {
      "type": "string",
      "enum": ["shallow", "deep"],
      "default": "shallow",
      "description": "shallow = adjacency compaction, deep = multi-agent traversal"
    },
    "include_superseded": {
      "type": "boolean",
      "default": false,
      "description": "Include superseded nodes in results for temporal queries"
    }
  }
}
```

### context_write

```json
{
  "name": "context_write",
  "description": "Write or update a node in the knowledge graph. Uses LLM to classify as ADD, UPDATE, SUPERSEDE, or NOOP. Enforces structural acyclicity.",
  "parameters": {
    "id": {
      "type": "string",
      "description": "Node ID (e.g., 'auth/login')"
    },
    "type": {
      "type": "string",
      "description": "Node type (function, module, class, concept, decision, reference, composite)"
    },
    "namespace": {
      "type": "string",
      "description": "Target namespace"
    },
    "summary": {
      "type": "string",
      "description": "Short description of the node"
    },
    "context": {
      "type": "string",
      "description": "Full context content (code, documentation, etc.)"
    },
    "edges": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "target": { "type": "string" },
          "relation": { "type": "string" }
        }
      },
      "description": "Typed directed edges from this node"
    },
    "source": {
      "type": "object",
      "properties": {
        "path": { "type": "string" },
        "lines": { "type": "array", "items": { "type": "integer" } },
        "hash": { "type": "string" }
      },
      "description": "Source file reference"
    }
  }
}
```

### context_verify

```json
{
  "name": "context_verify",
  "description": "Check node staleness and graph integrity. Compares stored source hashes to current files.",
  "parameters": {
    "node_ids": {
      "type": "array",
      "items": { "type": "string" },
      "description": "Node IDs to verify. Omit for full namespace verification."
    },
    "namespace": {
      "type": "string",
      "description": "Target namespace"
    },
    "check": {
      "type": "string",
      "enum": ["staleness", "integrity", "all"],
      "default": "all",
      "description": "staleness = source hash comparison, integrity = structural acyclicity + dangling edges, all = both"
    }
  }
}
```

---

## Future Enhancements (Research-Informed)

The following enhancements are informed by recent research in agentic memory systems.
They are architecturally compatible with the current design but deferred until the
core system is stable.

### 5. Dual Retrieval Strategy

**Source**: Mem0g (2025), AriGraph (2024)

Add entity-centric retrieval alongside semantic search. Extract named entities from
the query, find matching nodes by exact ID or alias, traverse edges. Merge with
semantic search results and deduplicate.

- Semantic: "how does authentication work?" -> embedding similarity
- Entity: query mentions `login`, `JWT` -> direct node lookup + edge traversal
- Combined ranking by weighted score fusion

**Where it fits**: Phase 8 (Graph Traversal) and Phase 6 (Embedding Index).

### 6. Hierarchical Tier Promotion

**Source**: H-MEM (2025), Hybrid Multi-Layered Memory (2025)

Add a `tier` field to nodes:

| Tier | Contains | Promotion trigger |
|------|----------|-------------------|
| `episodic` | Raw observations, specific facts | Created on first write |
| `semantic` | Consolidated knowledge, patterns | Multiple episodic nodes converge |
| `strategic` | High-level principles, decisions | Repeated semantic patterns |

Promotion creates a higher-tier node with `contains` edges to the lower-tier nodes
it consolidates. The compaction engine prefers higher-tier nodes when budget is tight.

**Where it fits**: New phase after Phase 9 (Compaction Engine).

### 7. Ebbinghaus Decay with Reactivation

**Source**: Hou et al. (2024), "Human-like Memory Recall and Consolidation"

Extend the heat map to individual node access weights (not just co-access pairs).
Nodes that get queried receive a reactivation boost; unaccessed nodes decay.

```
On query hit:
  node.access_weight = min(1.0, node.access_weight + reactivation_boost)
  node.last_accessed = now()

On decay:
  node.access_weight = max(decay_floor, node.access_weight * decay_rate)
```

**Critical constraint**: Decay only affects traversal priority, never existence or
embedding discoverability. Old code modules remain fully findable by direct query.
First access reactivates immediately.

**Where it fits**: Phase 7 (Heat Map) extension.

### 8. RL-Optimized Edge Weights

**Source**: "From Experience to Strategy: Trainable Graph Memory" (2025)

Learn edge utility from task outcomes. When an agent uses context from a traversal
and the task succeeds, reinforce the edges that were followed. When the task fails,
reduce their weight. Requires a reward/feedback signal from the agent.

**Where it fits**: Future phase. Requires agent feedback loop infrastructure.

### 9. Retroactive Node Self-Organization

**Source**: A-MEM (2025, NeurIPS)

Agent autonomously re-links and updates existing nodes when new information refines
their context. When a new node is added, the system identifies existing nodes whose
descriptions or relationships should be updated and rewrites them.

**Where it fits**: Extension of CRUD classification (Phase 10). The system already
checks for similar nodes on write — self-organization extends this to *also* update
the existing nodes, not just the incoming one.
