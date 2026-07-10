# web

## web

Scope: `/web` is the browser UI (vanilla TS + d3, built by vite, embedded via `embed.go`). It contains NO warren *management* — the CONTEXT claim "web UI has zero warren management (mount/unmount/edit/refresh are CLI-only; UI only lists/views warren graphs)" is **mostly accurate but slightly out of date**: the UI additionally (a) triggers a server-side warren state reload via `POST /api/warren/{id}/refresh` on the Refresh button, and (b) shows per-node warren provenance and enforces read-only editing for non-editable warren nodes. Still no mount/unmount/register/burrow/propose/doctor surface.

### src/api.ts:58-75 (UX — full warren API surface the UI consumes)
The UI touches exactly four warren-adjacent endpoints: `/api/bridges`, `/api/warrens`, `/api/warren/{id}/graph` (GET), and `/api/warren/{id}/refresh` (POST, in main.ts). No endpoints exist client-side for mount/unmount/register/unregister/burrow/propose/doctor/import — a UX-pass gap if web management is desired.
```ts
58  export async function fetchBridges(): Promise<BridgeInfo[]> {
59    const res = await fetch('/api/bridges');
...
65  export async function fetchWarrens(): Promise<WarrensResponse> {
66    const res = await fetch('/api/warrens');
...
71  export async function fetchWarrenGraph(warrenId: string): Promise<GraphResponse> {
72    const res = await fetch(`/api/warren/${encodeURIComponent(warrenId)}/graph`);
```
Note: `fetchBridges()` exists but I found no caller in `src/` — dead or latent API surface (UX candidate: a bridges list view).

### src/main.ts:167-183 (UX — warren display in namespace selector)
Warrens are appended to the namespace `<select>` behind a disabled "Warrens" divider option, labeled `Warren <id> (<n> active)`. Under R1/R2, an aliased/identified local project inside a warren view has no distinct display treatment here — the count comes from `active_projects` only.
```ts
177    for (const [warrenId, warren] of warrenEntries) {
178      const opt = document.createElement('option');
179      opt.value = `_warren/${warrenId}`;
180      const count = warren.active_projects?.length ?? 0;
181      opt.textContent = `Warren ${warrenId} (${count} active)`;
```

### src/main.ts:199-210 (R1 — UI path that exercises ReloadWarrenState)
The Refresh button, when a `_warren/` namespace is selected, fires `POST /api/warren/{id}/refresh` before re-fetching the graph. This is a client of `Engine.ReloadWarrenState`, so R1's "skip rt.Set for vault_id == LocalVaultID" change is directly observable here: after R1, refreshing a warren view whose bridge involves the local project should resolve to the LIVE local vault rather than the warren snapshot. Any web e2e for R1 should go through this button.
```ts
201      if (currentNamespace.startsWith('_warren/')) {
202        const warrenId = currentNamespace.slice('_warren/'.length);
203        try {
204          await fetch(`/api/warren/${encodeURIComponent(warrenId)}/refresh`, { method: 'POST' });
```

### src/types.ts:42-49 (R1/R2 — provenance schema the UI expects)
`Provenance.source` is typed `'local' | 'warren_mount' | 'warren_materialized' | string`. R1's self-mount aliasing and R2's identified-local endpoint should decide whether aliased nodes report `source: 'local'` (UI already handles it gracefully — open string union) or a new value; either way the UI renders whatever comes through, but the `editable` flag drives real behavior (see detail-panel below).
```ts
42  export interface Provenance {
43    source?: 'local' | 'warren_mount' | 'warren_materialized' | string;
44    warren_id?: string;
45    project_id?: string;
46    vault_id?: string;
47    marmot_dir?: string;
48    qualified_id?: string;
49    editable?: boolean;
```

### src/types.ts:66-82 (R2 — WorkspaceWarren / BridgeInfo shapes)
`WorkspaceWarren` carries `active_projects` / `editable_projects` / `materialized_projects`. R2's identified-local project has no representation in this response shape; if `/api/warrens` grows an `identified_project` (or R1 grows an alias marker), this interface and the selector label (main.ts:180-181) need updating. `BridgeInfo.is_cross_vault` exists but is unused (no fetchBridges caller).
```ts
66  export interface WorkspaceWarren {
67    path: string;
68    active_projects?: string[];
69    editable_projects?: string[];
70    materialized_projects?: string[];
71  }
...
77  export interface BridgeInfo {
78    source: string;
79    target: string;
80    allowed_relations: string[];
81    is_cross_vault: boolean;
82  }
```

### src/detail-panel.ts:109-124 and 221-253 (R1 — editable gating in the UI)
Provenance is rendered verbatim per node, and any node with `provenance` present and `editable !== true` is hard read-only: save button disabled/relabeled and input handlers early-return. R1's "refuse editable on self-mounts" interacts here: today an editable self-mount copy would render as editable in the UI and writes would go to the warren snapshot (the split-brain risk). After R1, aliased local nodes presumably arrive as plain local nodes (no provenance) and stay editable against the live vault — worth pinning in a web e2e.
```ts
221    const isReadOnlyWarrenNode = Boolean(node.provenance && !node.provenance.editable);
...
238    if (isReadOnlyWarrenNode) {
239      saveBtn.textContent = 'Read-only Warren Node';
240    }
...
252      if (isReadOnlyWarrenNode) return;
```

### src/graph-view.ts:230-240, 553-570 (UX — bridge rendering only)
Bridge edges (`class === 'bridge'`) are drawn as separate Bezier arcs (`.bridge-arc`) with participating nodes marked `.bridge-node`; index.html:47 has an edge-class radio filter including "Bridge". Purely presentational; no vault-ID logic. Relevant to R1 only in that bridge endpoints resolved to a stale warren copy vs the live vault will change which node IDs appear in these arcs.

### e2e/ (tests that pin behavior)
`e2e/ui.spec.ts` and `e2e/regressions.spec.ts` contain ZERO warren coverage — no test touches `/api/warrens`, `_warren/` namespaces, provenance display, read-only gating, or the warren refresh POST. `e2e/serve.sh` runs a hermetic single-vault fixture (`cp -R "$ROOT/e2e/fixture/vault" "$WORK/.marmot"`; scrubs global config) with no warren fixture at all. No web tests need *updating* for R1/R2, but there is nothing pinning current self-mount UI behavior either; the "first warren e2e suite" mentioned in CONTEXT is not in `/web/e2e` (it lives elsewhere in the repo). R1/R2 plan should add a warren fixture + specs here for: warren option in selector, refresh POST, provenance panel, read-only save gating, and alias-vs-snapshot node content.

### UX friction evidence (web)
- No warren management verbs in UI (mount/unmount/edit/refresh-pull/propose/doctor) — only view + state-reload.
- `fetchBridges` / `BridgeInfo` implemented but never used: bridges are invisible except as arcs inside a warren graph.
- Warren picker is crammed into the namespace `<select>` with a disabled-option divider (main.ts:171-176) — no dedicated warren panel, no per-project (active/editable/materialized) breakdown despite the API returning it.
- Read-only warren node message is a bare relabeled button ("Read-only Warren Node") with no explanation of why or how to make it editable (`marmot warren edit ...`).
- Warren refresh failure is silently swallowed (main.ts:205-207 "Best-effort") — no error surfaced to the user.
