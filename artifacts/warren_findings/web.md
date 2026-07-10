# web

## web

Frontend-only folder (TypeScript UI + Playwright e2e harness). No Go warren logic lives here, but it is a *consumer* of the warren HTTP API and the e2e harness is a reusable hermeticity template. No line drift found vs warren_review.md (the review does not cite web/ lines directly).

### web/src/api.ts:65-75
Consumer of the warren HTTP endpoints. Any Tier 2 refresh endpoint (`reloadWarrenState`) or Tier 3 verb changes must keep `/api/warrens` and `/api/warren/{id}/graph` response shapes stable, or update these fetchers. A new `/api/warren/refresh` endpoint could be wired to the existing `#refresh-btn` here.

```ts
65  export async function fetchWarrens(): Promise<WarrensResponse> {
66    const res = await fetch('/api/warrens');
67    if (!res.ok) throw new Error(`fetchWarrens: ${res.status}`);
68    return res.json();
69  }
70
71  export async function fetchWarrenGraph(warrenId: string): Promise<GraphResponse> {
72    const res = await fetch(`/api/warren/${encodeURIComponent(warrenId)}/graph`);
73    if (!res.ok) throw new Error(`fetchWarrenGraph: ${res.status}`);
74    return res.json();
75  }
```

### web/src/types.ts:42-75
API-stability constraint: these interfaces mirror the Go JSON shapes. `Provenance.editable`, `source` (`local | warren_mount | warren_materialized`), `warren_id`, `vault_id`, `qualified_id` and `WorkspaceWarren.{path,active_projects,editable_projects,materialized_projects}` are load-bearing for the UI. Tier 1 "editable+materialized write loss" and Tier 4 "manifest read-only policy" fixes must preserve or version these fields.

```ts
42  export interface Provenance {
43    source?: 'local' | 'warren_mount' | 'warren_materialized' | string;
44    warren_id?: string;
45    project_id?: string;
46    vault_id?: string;
47    marmot_dir?: string;
48    qualified_id?: string;
49    editable?: boolean;
50  }
...
66  export interface WorkspaceWarren {
67    path: string;
68    active_projects?: string[];
69    editable_projects?: string[];
70    materialized_projects?: string[];
71  }
72
73  export interface WarrensResponse {
74    warrens: Record<string, WorkspaceWarren>;
75  }
```

### web/src/detail-panel.ts:221-253
The UI already enforces read-only on non-editable warren nodes (relevant to Tier 3 "MCP vs API @-write asymmetry": the web UI gates writes on `provenance.editable`, so the API side is the place that must match). Any node lacking `provenance` is treated as writable; a node with `provenance` and falsy `editable` is blocked client-side only — server must enforce too.

```ts
221    const isReadOnlyWarrenNode = Boolean(node.provenance && !node.provenance.editable);
...
238    if (isReadOnlyWarrenNode) {
239      saveBtn.textContent = 'Read-only Warren Node';
240    }
...
242    const enableSave = () => {
243      if (isReadOnlyWarrenNode) return;
...
252      if (isReadOnlyWarrenNode) return;
```

### web/src/main.ts:167-199,246-291
Warren namespace selector uses `_warren/<id>` pseudo-namespace values; `loadGraph()` routes them to `fetchWarrenGraph`. `#refresh-btn` (line 197) just re-runs `loadGraph()` — it does NOT hit any server-side warren refresh; a Tier 2 real refresh endpoint would be a natural addition here. If `fetchWarrens()` throws, the whole namespace population falls into the catch (line 184) and degrades to a single 'default' option — Tier 3 "unreachable-warren surfacing" would want a softer failure than that.

```ts
167    const warrenData = await fetchWarrens();
...
179      opt.value = `_warren/${warrenId}`;
...
197    document.getElementById('refresh-btn')?.addEventListener('click', () => {
198      void loadGraph();
199    });
...
248    } else if (currentNamespace.startsWith('_warren/')) {
249      currentData = await fetchWarrenGraph(currentNamespace.slice('_warren/'.length));
```

### web/e2e/serve.sh:1-33
Reusable hermeticity template for the warren e2e test program (Tier 1 test-hermeticity + test-plan "zero warren e2e today"). Key trick: it isolates via `export HOME="$WORK"` rather than `MARMOT_ROUTES=off` — a warren e2e can copy this pattern and additionally set `MARMOT_ROUTES`. Requires prebuilt `bin/marmot`; fixture vault at repo `e2e/fixture/vault` copied to `$WORK/.marmot`, then `marmot index --dir` and `marmot ui --dir .marmot --port N --no-open`. Playwright drives it via `webServer` in playwright.config.ts (health URL `/api/version`).

```bash
19  # Isolate spawned marmot processes from the developer's real ~/.marmot state
20  # (e.g. routes.yml vault registrations) so the fixture server is hermetic.
21  export HOME="$WORK"
22
23  cp -R "$ROOT/e2e/fixture/vault" "$WORK/.marmot"
...
27  "$BIN" index --dir "$WORK/.marmot"
...
31  "$BIN" ui --dir .marmot --port "$PORT" --no-open &
```

### web/e2e/*.spec.ts (regressions.spec.ts, ui.spec.ts)
No warren coverage exists in the browser e2e suite (confirms review's "zero warren e2e today" for the UI layer too). regressions.spec.ts:161 shows the reusable pattern for asserting no dead API endpoints (`page.on('request', ...)` + console-error tracking) — directly reusable to validate a new `/api/warren/refresh` endpoint and warren dropdown behavior.

### Not present here
None of the plan's Go targets (embedding.NewStore, copyDir/copyMarmotVault, updateWorkspaceState, ActiveMounts, buildEngine, daemon watcher, ensureWorkspace, findWarrenMountByVault, refresh stubs, flock helpers, frontmatter parser) have call sites or definitions under web/; `web/embed.go` only exposes `//go:embed all:dist` as `web.Assets`.
