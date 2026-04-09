# Graph Curator -- UX Specification

**Status:** Draft  
**Date:** 2026-04-08  
**Scope:** Chat-driven curation panel for the ContextMarmot web UI

---

## 1. Layout

The curator lives in a **bottom drawer** spanning the full width beneath `#main-area`. The existing three-column layout (filter | graph | detail) remains untouched above it.

```
+---------------------------------------------------------------+
|  toolbar                                                       |
+--------+----------------------------------+-------------------+
| filter |          graph-svg               |  detail-panel     |
| panel  |                                  |  (slides in)      |
| 224px  |   force-directed canvas          |  370px            |
|        |                                  |                   |
|        |              [minimap]            |                   |
+--------+----------------------------------+-------------------+
|  [^] curator-drawer (collapsed: 42px tab)                     |
|  ┌────────────────────────────────────────────────────────┐   |
|  │ msg  msg  msg  ...               [73% curated ██████░] │   |
|  │ ▸ /tag auth_                          [Tab] autocomplete│   |
|  └────────────────────────────────────────────────────────┘   |
+---------------------------------------------------------------+
```

**Collapsed state (default):** A 42px bar at the bottom. Shows the input field, a curation badge ("3 issues"), and a grab handle. The graph canvas gets `calc(100vh - var(--toolbar-h) - 42px)` height.

**Expanded state:** Drawer grows to 320px max (40vh cap). The graph canvas shrinks with `transition: height 280ms var(--ease-out)`. A 6px drag handle along the top edge allows resize between 160px and 40vh. Messages scroll above the input.

**Transitions:** Expand/collapse uses `height` + `opacity` on the message area, same `--transition-med` (280ms) used by the detail panel. The input bar never leaves the screen.

**Mobile (<768px):** Drawer becomes a full-screen overlay triggered by a floating action button (bottom-right, 48px circle, amber `#d4a853`). Filter panel collapses behind a hamburger. Detail panel becomes a bottom sheet.

**Colors:** All drawer surfaces use `--bg-secondary` (#151719). User messages get `--bg-elevated` (#22262a) bubbles. System messages use a left border of `--secondary` (#6b9e87). Command output uses `--font-mono` on `--bg-tertiary`. The curation badge pulses with `--accent-glow`.

---

## 2. Interaction Patterns

**Open/close:** Click the drawer tab, press `Cmd+K`, or type `/` when no input is focused (current `/` behavior for search moves to `Cmd+F`; the `/` key becomes the curator shortcut). `Escape` collapses the drawer and returns focus to the graph.

**Chat mentions a node:** When the system references a node ID (rendered as a `<span class="node-ref">` pill styled like the existing tag badges), clicking it: (a) pans the graph to center that node via `graphView.highlightNode()`, (b) applies a 2-second amber pulse ring animation (3 concentric expanding circles at 15% opacity), (c) opens the detail panel for that node.

**User clicks a node:** If the drawer is expanded and the input is focused, clicking a node inserts its short ID at the cursor as a `@mention` token (pill-styled, removable with backspace). The node's full context is silently appended to the LLM prompt. If the input is not focused, normal detail-panel behavior fires.

**Graph mutations from chat:** New nodes fade-in over 400ms with a scale(0) to scale(1) spring animation (`--ease-spring`). Deleted nodes shrink and fade out over 300ms. Re-tagged nodes flash their new tag badge. New edges draw from source to target as a growing SVG path stroke over 500ms via `stroke-dashoffset` animation. All mutations are batched: the simulation restarts once per command, not per-node.

**Multi-select + chat:** Hold `Shift` and click nodes to build a selection set (highlighted with a dashed amber ring). The input placeholder updates to "3 nodes selected -- ask or command..." Typing `/tag payments` applies to the entire selection. The selection clears after command execution.

---

## 3. Input Modes

A single input field handles both modes. Typing `/` as the first character enters **command mode** (input background shifts to `--bg-tertiary`, left border becomes `--accent`). Anything else is **natural language mode**.

### 3a. Structured Commands

Commands parse client-side and execute directly against the REST API with zero LLM involvement.

| Command | Args | Action |
|---------|------|--------|
| `/tag <name>` | tag string | Adds tag to selected/mentioned nodes |
| `/untag <name>` | tag string | Removes tag |
| `/type <type>` | node type | Changes node type |
| `/merge <A> <B>` | two node IDs | Merges B into A, re-links edges |
| `/delete` | none | Deletes selected node(s) with confirmation |
| `/link <A> <rel> <B>` | source, relation, target | Creates an edge |
| `/unlink <A> <rel> <B>` | source, relation, target | Removes an edge |
| `/verify` | none | Runs health check, highlights issues on graph |

**Autocomplete:** After `/`, a dropdown appears (max 8 items) filtered by keystrokes. Node ID arguments fuzzy-match against the loaded graph data. `Tab` accepts the top suggestion. `Enter` executes. The dropdown uses `--bg-elevated` with `--border-medium`, same radius as search input.

**Feedback:** Commands echo as a system message with the result: "Tagged 2 nodes with `payments`" or "Error: node `auth/foo` not found". Successful mutations trigger graph re-render via the existing `loadGraph()` path or optimistic local update.

### 3b. Natural Language

All other input routes to an LLM endpoint (`POST /api/chat`). The request payload includes: the user's message, the current namespace, selected node IDs and their full metadata, and the last 10 messages for context. Responses stream via SSE.

LLM responses can embed structured actions in fenced blocks (` ```action ... ``` `) which the client parses and executes with the same mutation pipeline as slash commands. The user sees both the explanation and the result.

---

## 4. Guided Curation Flow

On every `loadGraph()`, the client calls `GET /api/verify/{namespace}`. Results populate a **curation queue** displayed as a badge on the drawer tab: a sage circle with the issue count.

When the drawer is expanded, an "Issues" tab (alongside "Chat") shows the queue as a card stack:

```
 ┌──────────────────────────────────────────────────────┐
 │  ▲ Orphan node                                       │
 │  auth/stale-handler has 0 edges.                     │
 │  [Delete]  [Keep]  [Connect to...]                   │
 └──────────────────────────────────────────────────────┘
```

**Issue types detected:**
- **Orphans:** 0 edges. Actions: delete, keep (dismiss), connect (opens a node picker).
- **Missing type:** type is "unknown". Action: type picker dropdown inline.
- **Possible duplicates:** nodes with >0.85 name similarity. Actions: merge, dismiss.
- **Stale sources:** `is_stale === true`. Actions: re-index, archive, dismiss.
- **Untagged clusters:** 5+ connected nodes sharing no tag. Action: suggest a tag.

**Progress bar:** At the top of the Issues tab: `"Your graph is 73% curated"` with a segmented bar (sage fill on `--bg-tertiary` track). Percentage = `(total_nodes - open_issues) / total_nodes`. Reaching 100% shows a one-time "All clear" animation (amber confetti particles, 1.5s).

**Quick-fix buttons** use the existing badge/chip styling. [Delete] is `--color-danger`, [Keep] is `--bg-elevated` with `--text-secondary`, [Connect to...] and positive actions are `--secondary`.

---

## 5. Node-Context Awareness

**Placeholder text shifts dynamically:**
- No selection: `"Ask about your graph, or type / for commands..."`
- Single node selected: `"Ask about auth/login..."`
- Multi-selection: `"Ask about 3 selected nodes..."`
- Hovering a cluster hull: `"Ask about the payments cluster..."`

**Inline hints** appear as faint text below the input (not inside it), triggered by node selection:
- "0 incoming edges" / "Last indexed 3 days ago" / "Tagged: auth, legacy"
- These hints use `--text-muted` at 11px, `--font-mono`.

**LLM context injection:** When the user submits a natural language message, the selected node's full `APINode` object (summary, context, edges, source, tags) is prepended to the system prompt. For multi-select, all selected nodes are included, capped at 10 to stay within token limits.

---

## 6. Edge Cases

**No LLM API key:** Natural language input shows a single-line notice: "LLM not configured -- slash commands are available. Set MARMOT_LLM_KEY to enable AI." The input still works for structured commands. The notice is dismissable.

**Empty graph:** The drawer replaces the chat with an onboarding card: "No nodes yet. Run `marmot index` to build your knowledge graph." with a copy-to-clipboard button for the command.

**Long LLM response:** Streaming tokens render in real-time. A pulsing amber dot indicates generation. A "Stop" button (square icon, `--color-danger`) appears next to the input. Clicking it sends an abort signal and appends "[interrupted]" to the message.

**Undo/redo:** Every mutation (from commands or LLM actions) pushes onto an undo stack (max 50 entries). `Cmd+Z` undoes the last mutation, `Cmd+Shift+Z` redoes. The undo executes the inverse API call (delete undoes create, untag undoes tag, etc.). A brief toast notification confirms: "Undone: tag payments".

**Concurrent editing:** The existing SSE `graph-changed` event triggers a reload. If a reload conflicts with a pending mutation, the pending mutation is retried once against the fresh data. Chat history is local and unaffected.

**Large graphs (1000+ nodes):** Highlight and pan operations target the D3 simulation's node array by ID (O(n) scan, ~1ms at 1000 nodes). Pulse animations use a single dedicated SVG `<circle>` element repositioned per highlight rather than creating/destroying DOM nodes. The verify endpoint paginates issues (50 per page).

---

## 7. Keyboard Shortcuts

| Shortcut | Action |
|----------|--------|
| `Cmd+K` | Toggle drawer + focus input |
| `/` | Focus input in command mode (when not in a text field) |
| `Escape` | Collapse drawer, clear selection |
| `Tab` | Accept autocomplete suggestion |
| `Up` / `Down` | Navigate command history (in input) or autocomplete list |
| `Cmd+Z` | Undo last mutation |
| `Cmd+Shift+Z` | Redo |
| `Shift+Click` (node) | Add/remove node from multi-selection |
| `Enter` | Submit message or execute command |
| `Shift+Enter` | Newline in natural language mode |

All shortcuts are registered through the existing `initKeyboard()` system, extended with a `onCurator` callback. The `/` key rebinding is gated on whether the curator module is loaded.
