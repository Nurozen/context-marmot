# Dens

> **Status:** P0–P4 shipped. Dens under `$MARMOT_HOME`, reverse routes,
> schema:1 JSON CLI, dens-aware vault discovery, all three link modes
> (`--edit` / `--link` pinned / live), `den unlink`, `den create --ref`,
> den-scoped bridges, promote-on-destroy, and link freshness are implemented.
> In-repo `.marmot/` vaults and Warrens remain fully supported — see
> [warrens.md](./warrens.md).

A **den** is a per-project context workspace stored under `$MARMOT_HOME/dens/<den-id>/`
(default home `~/.marmot`). It is a **vault plus a link table**, not a vault alone. Vaults
can live outside code repos so agents stop self-reading/editing an in-tree `.marmot/`.

## Layout

```text
$MARMOT_HOME/dens/<den-id>/
  _den.md              # den manifest (projects, links, lifetime)
  vault/               # OPTIONAL identity vault (at most one)
    _config.md         #   vault_id = den identity
    <namespaces>/...
    .marmot-data/      #   vault-scoped: embeddings.db, .env, vault.read.lock
  _bridges/            # consumer-owned bridges (own↔link, link↔link)
  .marmot-data/        # den-scoped: daemon lock/socket, link/cache state
                       #   (sole data dir for links-only dens)
```

Ownership rule: **vault data lives with the vault; composition data lives with the den.**
A den vault is byte-identical in layout to an in-repo `.marmot/` vault.

`$MARMOT_HOME` defaults to `~/.marmot`. Override with the `MARMOT_HOME` environment
variable (stave space-local MCP embeds this so clients resolve the same dens root).

## Vault discovery / resolution order

**All vault-consuming commands** resolve a vault/den in this order:

1. `--dir` flag (raw vault path — never repurposed to den-id-only)
2. Reverse route: cwd (normalized) → `$MARMOT_HOME/routes.yml` `projects:` table
3. `.marmot-vault` pointer file in the project (walk up parents; one line: den-or-vault id)
4. Legacy walk-up for in-repo `.marmot/` (permanent compat)

This applies to `serve`, `ui`, `status`, `query`, `index`, `verify`, `watch`,
`bridge`, `namespace`, `summarize`, `reembed`, `configure`, and `setup` — not only
`serve`. Without dens-aware discovery, a project registered only via reverse route
would silently open an ancestor repo `.marmot` instead of the den vault.

**Global MCP setup:** `marmot setup --global` registers a bare `marmot serve`
(no `--dir`) in the user-scope config of each detected harness (Claude Code
`~/.claude.json`, Codex `~/.codex/config.toml`, Cursor `~/.cursor/mcp.json`,
VS Code user `settings.json` under the `mcp.servers` key). It depends on this
resolution chain: with no embedded path, serve finds the right vault through
the reverse route or `.marmot-vault` pointer of whatever project the client
launches it in. `--dry-run` prints the target files and payloads without
writing. Existing config files are merged non-destructively; unparseable JSON
is refused rather than clobbered.

**Space-local MCP (stave):** `marmot serve --den <den-id>` resolves
`$MARMOT_HOME/dens/<id>/vault` (or the den root for links-only dens). Mutually
exclusive with `--dir`.

**Empty local vault is allowed for design:** links-only dens have no `vault/`;
serve uses the den root. Full “warrens only” empty-identity UI remains a follow-up.

### Who writes `.marmot-vault`

| Command | Writes pointer? |
|---------|-----------------|
| `marmot den create` | Yes, into each `--project` path, unless `--no-pointer` |
| `marmot route pointer` | Yes (repair / explicit write or remove) |
| `marmot init` | **No** — creates in-repo `.marmot/` where walk-up already works |

**Stave spaces:** `stave memory attach` always invokes
`marmot den create … --no-pointer --json` and writes **space-local MCP config**
instead. Never place `.marmot-vault` in a stave space root.

### Project ownership (no silent split-brain)

A project path may be claimed by **at most one** den-or-vault id.

- `den create` **refuses** if `routes.yml` already maps the path to another id, or if
  `.marmot-vault` points at another id (including dry-run).
- `route set-project` / `den.RelocateProject` refuse a target path owned by a
  different id.
- Last-write-wins reassignment without reconciliation is intentionally rejected so
  reverse-route consumers and pointer consumers cannot disagree.

### Reverse routes and archive

`routes.yml` `projects:` maps abs project path → den-or-vault id. Stave registers the
space root on attach. When a space is **archived**, the path moves under `.archive/` —
stave calls `marmot route set-project --from <old> --to <new> --json`, which also
rewrites `_den.md` `projects:` so destroy/status stay consistent.

`den destroy` sweeps **every** reverse-route entry that points at the den id (union of
manifest projects and live routes), so a relocate cannot leave orphans.

## Command tree (implemented + roadmap)

```text
marmot den create <den-id> [--lifetime task|durable] [--project <abs>]...
    [--ref name=<n>,url=<u>,path=<p>,ref=<r>]...   # §15.5: resolved into links
    [--no-pointer] [--no-vault]
    [--embedding-provider openai|mock] [--embedding-model <name>]
    [--dry-run] [--json]
marmot den status  [<den-id>] [--json]             # real link freshness (§9)
marmot den destroy <den-id> [--promote <target-den-id>] [--force] [--dry-run] [--json]
marmot den list    [--json]
marmot den adopt   [--from <project>] [--id <den-id>] [--no-pointer] [--no-rewrite] [--dry-run] [--json]
marmot den link <den-id> --edit <warren-id>/<project-id> [--dry-run] [--json]
    # cache-backed warren (warren add): dedicated edit worktree, see below
    # legacy registered checkout: editable mount in the registered checkout
marmot den link <den-id> --link <warren-id>/<project-id> [--dry-run] [--json]  # pinned
marmot den link <den-id> --link <den-id>                 [--dry-run] [--json]  # live
marmot den unlink <den-id> <target> [--force] [--dry-run] [--json]
marmot den contribute <den-id> [<link>] [--dry-run] [--json]  # needs an edit-mode link;
    # legacy: commits on marmot/edit/<den>/<project> in the registered checkout
    # cache-backed: commits in the edit worktree on marmot/edit/<den>/<warren>
marmot den bridge add  <den-id> <from> <to> [--relation r]... [--json]
marmot den bridge list <den-id> [--json]
marmot den bridge rm   <den-id> <from> <to> [--json]

marmot route add|rm|resolve|set-project|pointer ...
marmot resolve --url <u> [--path <p>] [--name <n>] [--ref <git-ref>] [--json]
marmot serve [--dir <vault>] [--den <den-id>] [--no-daemon]
marmot ui    [--dir <vault>] ...   # same discovery as status/query when --dir omitted
```

Human `--edit` / `--link` specs and multi-link create flags are **not** on the S2 wire;
stave passes only `--lifetime`, `--project`, `--no-pointer`, `--json`.

### Identity-vault embedding provider

There is no global marmot config file — embedding settings live per-vault in
`_config.md`. `den create` therefore defaults the identity vault to the `mock`
embedder but accepts `--embedding-provider openai --embedding-model <name>` so
a den vault can be created with a REAL provider without hand-editing
`_config.md`. The API key resolves at serve time from the environment
(`OPENAI_API_KEY`), falling back to the vault's `.marmot-data/.env`
(`marmot configure` can store it there). An unknown provider is refused at
create time. `den adopt` needs no flags: the moved vault keeps whatever
provider its `_config.md` already declares.

## JSON contract (stave / automation)

Versioned envelope: `{"schema": 1, ...}` — additive-only. Structured errors on **stdout**
with non-zero exit codes. Fixtures: `testdata/contracts/` (create, no-pointer, status,
list, destroy, dry-run, duplicate id, project collision, route set-project, generic error,
link, unlink, bridge list, contribute, adopt, resolve, warren propose).

**`den adopt --json`** adds (additively to the create shape) `vault_moved`,
`configs_rewritten` — see `testdata/contracts/den_adopt.v1.json`.

**`den create --json`** (default pointer write)

```json
{
  "schema": 1,
  "den_id": "myproject",
  "den_path": "/Users/you/.marmot/dens/myproject",
  "vault_id": "myproject",
  "routes": { "project_path": "/Users/you/src/myproject" },
  "pointer_written": true,
  "links": [],
  "warnings": []
}
```

**Stave attach** always uses `--no-pointer` → same shape with `"pointer_written": false`
(fixture: `testdata/contracts/den_create_no_pointer.v1.json`).

**`den status --json`** — links carry real freshness (see
[Link freshness](#link-freshness-9); fixture `den_status.v1.json`):

```json
{
  "schema": 1,
  "den_id": "myproject",
  "lifetime": "task",
  "vault_id": "myproject",
  "projects": ["/Users/you/src/myproject"],
  "links": [
    { "ref": "platform/docs", "mode": "link",
      "pinned_commit": "abc…", "ahead": 0, "behind": 3,
      "pending_edits": 0, "state": "stale", "source_commit": "77aa…" }
  ]
}
```

**`den list --json`**

```json
{
  "schema": 1,
  "dens": ["alpha", "beta"]
}
```

**`den destroy --json`**

```json
{
  "schema": 1,
  "den_id": "task-123",
  "destroyed": true,
  "kept": false,
  "unpushed_edits": 0,
  "promoted": null,
  "contributed": null
}
```

`unpushed_edits` is a REAL count for cache-backed edit links: per edit
worktree, commits on `marmot/edit/<den>/<warren>` not reachable from its
upstream (`origin/<edit-branch>` once pushed, else `origin/<default-branch>`,
else the cache pin), plus 1 if the worktree carries uncommitted engine writes.
A non-zero count **refuses** destroy with error code `unpushed_edits` unless
`--force`. Destroy removes the den's edit worktrees but **never deletes edit
branches** — the knowledge always survives in the shared bare cache. Legacy
registered-checkout edit links have no reliable upstream to count against and
always report 0 (documented limitation).

**`den contribute --json`** (committed run)

```json
{
  "schema": 1,
  "den_id": "demo",
  "link": { "target": "product-platform/project-a", "mode": "edit",
            "warren": "product-platform", "project": "project-a" },
  "branch": "marmot/edit/demo/project-a",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "committed": true,
  "checkout": "/Users/you/warrens/product-platform",
  "push_command": "git -C /Users/you/warrens/product-platform push -u origin marmot/edit/demo/project-a",
  "contributed": { "added": 2, "updated": 1, "superseded": 0, "noop": 3 },
  "warnings": []
}
```

Contribute is the **terminal packaging verb** of the den flow: a committed run
carries the shell-quoted `push_command` and the `checkout` path itself. Running
`warren propose` afterwards sees a clean tree and reports `nothing_to_propose`
— `warren propose` remains for the legacy uncommitted-edit flows (live MCP/API
edits in a warren checkout), not as a follow-up to contribute. marmot never
pushes for you.

Contribute rules:

- **Clean-scope preflight** — pre-existing uncommitted changes under the
  project path (`.marmot-data` sidecars excluded) refuse with
  `checkout_dirty` before any write or branch op, so user work is never swept
  into a contribute commit. Commit/stash first, or package the edits with
  `marmot warren propose`.
- **Failure recovery** — on any git failure contribute removes/restores
  exactly the files its engine wrote, returns to the previous branch, and
  deletes the edit branch if that run created it; a retry is a real
  contribute, never a NOOP against leftovers.
- **No embeddings.db writes** — contribute stages node markdown only; the
  target's `embeddings.db` is never touched (an edit-branch-only commit must
  not mutate the main branch's derived state). Consumers reindex/reembed
  (`marmot index` / `marmot reembed`) after merging a contribute PR.
- **Dry-run divergence** — when the edit branch already exists, `--dry-run`
  plans against the *currently checked-out* tree while the real run checks
  the branch out first; the dry-run appends a note op disclosing that results
  may differ. Dry-run applies the `checkout_dirty` rule identically.
- **Branch-name safety** — den and project ids are validated as git-ref
  components (`[A-Za-z0-9._-]`, no leading `.`/`-`, no `..`, no trailing `.`
  or `.lock`); hostile ids refuse with `invalid_branch_component`.

## Edit links into cache-backed warrens (edit worktrees)

For a warren in the shared cache (`marmot warren add`, see
[warrens.md](./warrens.md)), `den link --edit` does not need — and never
touches — a user-managed checkout. It creates (idempotently, under the
per-warren cache lock) a **dedicated edit worktree** of the warren repo:

```text
$MARMOT_HOME/warren-cache/edits/<warren-id>/<den-id>/   # the worktree
branch: marmot/edit/<den-id>/<warren-id>                # permanently checked out
```

The worktree starts from the shared checkout's pin (falling back to
`origin/<default-branch>`); an existing branch is re-attached instead of
recreated. The den vault's warren state points at the worktree, so every
read/write mount of that warren in this den routes through it. `den link
--json` reports both additively: top-level `worktree` and `branch`
(`testdata/contracts/den_link.v1.json`); legacy registered-checkout links omit
them and behave exactly as before.

**Granularity (deliberate deviation from the plan's per-project branch
sketch, §18.4):** one worktree/branch per **(warren, den)** — not per project
— because a worktree checks out the whole warren repo. Every edit link one
den holds into one warren shares the branch, which is also the right review
granularity: one PR per den per warren.

Consequences of the dedicated worktree (the §17 quarantine property):

- **Agent writes never touch a user checkout** or the shared read checkout.
- **Auto-commit per MCP write (OQ7)** — every `warren.WriteEditableNode`
  write into an edit worktree is committed immediately, pathspec-limited to
  the node file, message `marmot edit: <vault-id>/<node-id> (write)`. A
  commit failure degrades to a warning on the write result (the node file is
  saved; the next contribute sweeps it). Legacy checkout mounts are never
  auto-committed.
- **Contribute without the checkout dance** — the worktree is permanently on
  its edit branch, so contribute skips branch checkout/restore entirely, the
  dry-run plan is always accurate (it reads the branch tip), and failure
  recovery restores/removes exactly the engine-written files. Pre-existing
  dirt under the project scope still refuses (`checkout_dirty`) — in a
  worktree it can only mean a previous failed run (hint:
  `git -C <worktree> status`). `push_command`/`checkout` in the contribute
  envelope point at the worktree.
- **Destroy is knowledge-safe** — `den destroy` refuses on real unpushed
  edits (see `unpushed_edits` above), removes the worktree, and never deletes
  the edit branch from the bare cache.

**Structured error** (e.g. project already owned)

```json
{
  "schema": 1,
  "error": {
    "code": "den_create_failed",
    "message": "project \"/path\" is already registered to \"other-den\"; …",
    "hint": "marmot den create …"
  }
}
```

## Links

| Mode | Flag | Behavior |
|------|------|----------|
| edit | `--edit <warren>/<project>` | Editable mount + edit branch. Cache-backed warrens: dedicated edit worktree (above). Legacy checkouts: editable mount in the registered checkout |
| link | `--link <warren>/<project>` | **Cache-tracking read-only reference (link-time baseline recorded for skew).** Pure manifest entry (`pinned_ref` = the warren's cache pin at link time, or the registered checkout's HEAD for legacy warrens); no mount-state change. Federation serves it from the warren's shared cache checkout **as-synced** — the recorded ref is the baseline `den status` reports skew against, not a serving guarantee (true pin-enforced serving is deferred; see §18.4) |
| live | `--link <den-id>` | **Live den-to-den link.** Pure manifest entry; resolves at serve time to the target den's identity vault (or a routed vault id). A `lifetime: task` den is **refused** as a live target (`task_den_refused`) — it can vanish at any moment |

`context_query` federates the den's own vault plus every resolved link
(`engine.LoadDenLinks`). `den link --link` envelopes carry the additive
`pinned_commit` field; dedupe is idempotent (`"already linked"` warning).

### `den unlink <den-id> <target>`

Removes every manifest link matching `<target>` (any mode; `<warren>/<project>`
or the live target). Edit links whose edit branch carries **unpushed commits**
refuse with `unpushed_edits` unless `--force` (same posture as destroy — the
branch always survives in the shared cache). On success the workspace-state
mount for the project is removed; the edit **worktree and branch stay in
place** — `den destroy` owns worktree cleanup. Envelope:
`{"schema":1, "den_id", "removed":[<link bodies>], "warnings":[]}`
(`testdata/contracts/den_unlink.v1.json`).

### `den create --ref` (§15.5 machine grammar)

Each repeatable `--ref name=<n>,url=<u>,path=<p>,ref=<r>` is resolved through
`warren.ResolveReference` (the same resolver behind `marmot resolve`):

| `resolved_via` | Link recorded |
|----------------|---------------|
| `warren-url` | pinned link (`mode=link`) on the matched warren project, pinned to the warren's cache pin |
| `checkout-vault` | live link (`mode=live`) on the checkout vault's `vault_id`; the vault dir is route-registered (if unrouted) so federation can resolve it |
| `none` | skipped, with warning `ref <name>: no warren source_url or checkout vault_id match; skipped` |

The create envelope's `links[]` carries `{ref, mode, resolved_via}` per spec
(`testdata/contracts/den_create_with_links.v1.json`); `mode` is `null` for
skipped refs. A malformed spec refuses `invalid_args` before any persistence.

## Link freshness (§9)

`den status` computes REAL per-link freshness; `state` draws from
`{ok, unpushed, stale, unreachable}`:

- **edit** (cache-backed): `ahead`/`behind` of the edit branch vs its upstream
  (`origin/<edit-branch>` once pushed, else `origin/<default-branch>`, else the
  cache pin); `pending_edits = ahead` (+1 if the worktree carries uncommitted
  engine writes); `state: unpushed` when pending. Legacy registered-checkout
  edit links have no reliable upstream and stay at zeros/`ok`.
- **link** (pinned): `pinned_commit` from the recorded pin (else the current
  cache pin); `behind` = commits between the pin and `origin/<default-branch>`
  in the bare cache; `state: stale` when behind. When the warren project
  carries manifest-v3 `source_commit` provenance, status adds the additive
  `source_commit` field and renders the skew line
  `vault snapshot from source commit <sc>` — the knowledge snapshot's
  source-repo commit, so consumers can judge knowledge-vs-code skew.
- **live**: `state: unreachable` when neither a route nor a den identity vault
  answers for the target.

Every git/registry failure degrades to a stderr warning, never a hard failure.
The MCP `initialize` instructions annotate den link lines with the cheap
exec-free subset (`[pinned@<commit>, stale]`, `[unreachable]`) via
`den.LinkFreshnessNote`; full numbers stay in `den status`.

## Den-scoped bridges (§7)

`$MARMOT_HOME/dens/<id>/_bridges/` holds **consumer-owned** cross-vault edge
policies in the exact same manifest shape as vault-internal cross-vault
bridges (`_bridges/@<from>--@<to>.md`, frontmatter `source`/`target`/
`source_vault_id`/`target_vault_id`/`allowed_relations`). They declare which
edge relations may cross between two vaults this den links — without touching
either vault's own files (the vaults may be read-only warren checkouts).

- `den bridge add <den> <from> <to> [--relation r]...` — idempotent; re-adding
  merges relation sets. Default relations mirror `marmot bridge`
  (`calls,reads,writes,references,cross_project,associated`).
- `den bridge list <den> [--json]` — envelope
  `{"schema":1, "den_id", "bridges":[{from,to,relations}]}`
  (`testdata/contracts/den_bridge_list.v1.json`).
- `den bridge rm <den> <from> <to>` — matches either direction.

When serving a den vault, `engine.LoadDenBridges` loads them ADDITIONALLY into
the same cross-vault bridge structures as vault-file and warren-runtime
bridges, so cross-vault edge validation and bridged traversal treat them
identically; they survive warren state reloads.

**Deferred (§18.4):** warren-manifest-suggested bridge auto-activation on
`den link --link` (a warren's `bridges:` entries between two *pinned-linked*
projects do not auto-activate; today warren bridges activate only for active
mounts via `warrenRuntimeBridges`). Declare the edge policy explicitly with
`den bridge add` until a later round wires pinned-link activation.

## Promote-on-destroy (§15.3 sibling)

`den destroy --promote <target-den-id>` folds the dying den vault's ACTIVE
nodes into the TARGET den's identity vault **before** destruction, using the
same deterministic+classifier machinery as `den contribute` (`den.Promote`
shares the classification core). Differences from contribute:

- The target is a **live local vault**, so node writes go through its node
  store directly AND its `embeddings.db` IS updated with the **target's**
  embedder (contribute stages markdown only and leaves embeddings to consume).
- Refusals fire before anything is destroyed: `promote_target_not_found`,
  `promote_target_no_vault`, self-promotion (`invalid_args`), and any engine
  failure (`promote_failed`) leave the source den fully intact.
- A links-only source den (no identity vault) degrades to a zero-count promote
  with a warning.
- `--dry-run` composes: `promote: <op>` lines followed by the destroy ops.

The destroy envelope's `promoted` field carries the flow counts
(`{added, updated, superseded, noop}` — same shape as `contributed`).

## Migration

- In-repo `.marmot/` keeps working forever (discovery step 4).
- `marmot den adopt` migrates a project in one shot (the in-repo and den vault
  layouts are byte-identical, OQ1):
  - **moves** `.marmot/` (embeddings.db and the rest of `.marmot-data`
    included) into `$MARMOT_HOME/dens/<id>/vault/` — same-filesystem rename
    when possible, else copy+verify+remove; nothing is deleted before the copy
    verifies. The den id defaults to the source vault's `vault_id`, else the
    project basename (`--id` overrides).
  - registers the reverse route and writes the one-line `.marmot-vault`
    pointer (OQ3; `--no-pointer` opts out).
  - **rewrites** project-local MCP configs that embed `serve --dir <old-vault>`
    (`.mcp.json`, `.codex/config.toml`, `.vscode/mcp.json`, `.cursor/mcp.json`)
    to `serve --den <id>` (OQ13), preserving unrelated keys; unparseable files
    are warn+skip, never clobbered. `--no-rewrite` opts out.
  - `--dry-run` prints every planned op (move, pointer, each rewrite) and
    touches nothing. Structured refusals: `not_a_vault` (no
    `.marmot/_config.md`), `den_vault_exists`, `move_failed` (source left
    intact, den rolled back, hint carries recovery guidance).
- Legacy `warren register` / `mount` / `edit` remain supported.

## Stave integration (consumer)

Stave’s `stave memory` group is the first external consumer. Critical contracts:

| Topic | Contract |
|-------|----------|
| Phase gate | Stave S2 needs marmot **P1a+P1b** (routes + den verbs + `schema: 1`) — **met** |
| Attach flags | `den create <space-id> --lifetime task --project <spacePath> --no-pointer --json` |
| MCP | Space-local: `serve --den <id>` + `MARMOT_HOME` env in `.mcp.json` / Cursor / VS Code / Codex |
| Detach | Stave strips generated `context-marmot` MCP entries; `--keep` retains den, `--destroy` destroys it |
| Archive | `route set-project --from <space> --to <.archive/…>` keeps reverse routes fresh |
| JSON | Golden fixtures in `testdata/contracts/` — SoT for `schema: 1` |
| Propose | `den contribute` packages AND exposes `push_command`/`checkout` (no auto-push); needs edit link. `warren propose` is for legacy uncommitted-edit flows only |
| Portals | Memory is local-summon-only in v1; dens never under the space tree |

## UI notes

- Graph UI live-reload (SSE `graph-changed`) refreshes the graph **and** namespace
  selector node-count badges so counts do not go stale after vault writes.
- Prefer `marmot ui` from a project cwd with `MARMOT_HOME` set, or pass
  `--dir $MARMOT_HOME/dens/<id>/vault`.

## Related

- [architecture.md](./architecture.md) — system map
- [warrens.md](./warrens.md) — warren model (coexists with dens)
- [bridges.md](./bridges.md) — bridge manifests
- Stave memory: sibling repo `docs/memory.md` (when present) / `stave memory --help`
