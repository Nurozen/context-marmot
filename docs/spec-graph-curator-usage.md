# Graph Curator: Usage Patterns and Edge Cases

## 1. User Personas and Journeys

**First-time indexer.** Runs `marmot index` on a new repo, opens the web UI, sees 200 nodes auto-generated. Journey: scans the D3 graph for obvious problems (orphan nodes with no edges, mis-typed `function` nodes that should be `module`), uses natural language ("this node should be a class, not a function"), accepts 3-4 quick-fix suggestions, closes the tab. Session: 5-10 minutes.

**Periodic maintainer.** Opens the graph weekly after code changes triggered `marmot watch`. Filters by `is_stale` to find hash-mismatched nodes. Journey: clicks "verify all" to run integrity checks, sees 8 stale nodes and 2 dangling edges, uses batch operations ("update all stale hashes"), manually reviews the 2 dangling edges (one is a deleted file, one is a renamed function), merges or deletes as needed. Session: 10-15 minutes.

**Team lead building onboarding docs.** Browses the graph grouped by `namespace` or `folder`, looking for gaps in coverage. Journey: filters to `concept` and `decision` nodes, notices three subsystems have no concept nodes, uses the curator to draft them ("add a concept node for the authentication subsystem, it contains auth/login and auth/session"). Exports or screenshots the graph for the wiki. Session: 20-30 minutes.

**Developer debugging bad AI context.** An AI agent retrieved wrong context because a `calls` edge pointed to the wrong target. Journey: searches for the specific node ID, inspects its edges in the detail panel, uses the curator to fix the edge ("change the calls edge from auth/validate to auth/verify"), runs `context_verify` to confirm no new dangling edges. Session: 2-5 minutes.

## 2. Likely Usage Patterns

Estimated operation frequency (descending): (1) reviewing/accepting suggestions, (2) editing edges (add/remove/retarget), (3) changing node types or tags, (4) merging duplicate nodes, (5) creating new nodes from scratch.

Structured commands vs. natural language: expect 70% natural language for discovery and 30% structured commands for precise mutations (e.g., `/merge node-a into node-b`). Power users shift toward structured commands over time.

Typical curation ratio: 5-15% of auto-indexed nodes need human correction. Most fixes are edge corrections and type changes, not wholesale node rewrites.

## 3. Edge Cases and Failure Modes

| Case | Risk | Mitigation |
|---|---|---|
| Empty graph (0 nodes) | Curator has nothing to suggest | Show onboarding prompt: "Run `marmot index` first" |
| Massive graph (5000+ nodes) | D3 force sim bogs down; suggestion list overwhelms | Paginate suggestions; scope curator to filtered/visible subgraph |
| Node ID conflict on merge | Two nodes have incoming edges; merged ID must be chosen | Curator proposes retargeting all edges to the surviving ID, shows preview |
| Circular structural edge | User adds `contains` edge A->B when B->A already exists | Reject immediately with explanation; `CheckStructuralAcyclicity` already enforces this |
| Deleting a node with dependents | Incoming edges from other nodes become dangling | Show dependent count, require confirmation, offer to cascade-delete edges |
| Concurrent modification (two tabs, or `marmot watch` + curator) | SSE `graph-changed` event arrives mid-edit; pending suggestion references stale state | Invalidate pending suggestions on SSE refresh; show "graph updated" toast |
| LLM hallucinates a non-existent node ID | Proposed edge target doesn't exist in the graph | Validate all IDs against the live node set before presenting suggestion |
| LLM suggests invalid `EdgeRelation` | Relation not in the `contains/imports/calls/...` enum | Validate against `EdgeRelation` constants; reject with allowed-values hint |
| Undo past stack depth | User clicks undo with no history | Disable undo button; show "nothing to undo" |
| Network disconnect during mutation | Write request fails silently | Optimistic UI with rollback on error; retry with exponential backoff |
| Selected node deleted by external process | Detail panel shows stale data; highlight targets a removed node | On SSE refresh, check if selected node still exists; auto-close panel if gone |
| Long chat history | Context window fills up; LLM loses early conversation state | Summarize and compact history after ~20 exchanges; keep a structured mutation log separate from chat |
| Live-reload invalidates pending suggestions | Suggestion references a node that was just renamed/deleted | Tag each suggestion with a graph-version counter; discard stale suggestions on reload |

## 4. Low-Friction Design Principles

- **Proactive, dismissible suggestions.** After `context_verify` runs, surface issues as cards below the chat: "3 dangling edges found -- Fix all". One click to accept, one to dismiss.
- **Batch operations.** "12 orphan nodes detected" with a single "Connect to nearest parent" action. Each fix is previewed in the graph (highlight the proposed edge) before committing.
- **Smart defaults.** When a stale node is detected, pre-fill the updated hash and show the diff. User just confirms.
- **Progressive disclosure.** New users see only natural language input and suggestion cards. The `/command` syntax is documented in a collapsible help panel, not shown by default.
- **No gamification.** A progress bar implies curation is mandatory. Instead, show a neutral health summary: "247 nodes, 3 issues" -- factual, not judgmental.

## 5. Anti-Patterns to Avoid

- **Not a general chatbot.** Reject off-topic queries ("what's the weather?") with a redirect: "I can help with graph curation. Try describing a node or edge change."
- **Not a duplicate detail panel.** The existing `DetailPanel` handles viewing/editing single-node fields. The curator handles multi-node operations, relationship reasoning, and bulk fixes.
- **Not a mandatory gate.** The graph is fully usable for `context_query` without any curation. The curator is an optimization, not a prerequisite.
- **Not an auto-pilot.** Never mutate the graph without user confirmation. Suggestions are proposals, not actions.
