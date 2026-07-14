# Dens

> **Status:** P0–P1b shipped (S2-ready). Dens under `$MARMOT_HOME`, reverse
> routes, schema:1 JSON CLI, and dens-aware vault discovery are implemented.
> Link modes (`--edit` / `--link` / live), contribute, and full warren-cache
> migration remain roadmap (P2–P4). In-repo `.marmot/` vaults and Warrens remain
> fully supported — see [warrens.md](./warrens.md).

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
  _bridges/            # consumer-owned bridges (own↔link, link↔link) — P2+
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
# Implemented (P1b / S2)
marmot den create <den-id> [--lifetime task|durable] [--project <abs>]...
    [--no-pointer] [--no-vault] [--dry-run] [--json]
marmot den status  [<den-id>] [--json]
marmot den destroy <den-id> [--force] [--dry-run] [--json]
marmot den list    [--json]
marmot den adopt   [--from <project>] [--id <den-id>] [--dry-run] [--json]
marmot den contribute <den-id> [<link>] [--dry-run] [--json]  # P4 stub; needs edit link

marmot route add|rm|resolve|set-project|pointer ...
marmot serve [--dir <vault>] [--den <den-id>] [--no-daemon]
marmot ui    [--dir <vault>] ...   # same discovery as status/query when --dir omitted

# Roadmap (P2–P4)
marmot den link / unlink / full contribute + promote
```

Human `--edit` / `--link` specs and multi-link create flags are **not** on the S2 wire;
stave passes only `--lifetime`, `--project`, `--no-pointer`, `--json`.

## JSON contract (stave / automation)

Versioned envelope: `{"schema": 1, ...}` — additive-only. Structured errors on **stdout**
with non-zero exit codes. Fixtures: `testdata/contracts/` (create, no-pointer, status,
list, destroy, dry-run, duplicate id, project collision, route set-project, generic error).

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

**`den status --json`**

```json
{
  "schema": 1,
  "den_id": "myproject",
  "lifetime": "task",
  "vault_id": "myproject",
  "projects": ["/Users/you/src/myproject"],
  "links": []
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

## Links (P4 roadmap)

| Mode | Flag | Behavior |
|------|------|----------|
| edit | `--edit <warren>/<project>` | Dedicated worktree; MCP updates-only |
| link | `--link <warren>/<project>` | Pinned read-only from warren-cache |
| live | `--link <den-id>` | Live resolve to another den’s vault |

Query will federate own vault + all links. Not required for stave S2 attach/detach.

## Migration

- In-repo `.marmot/` keeps working forever (discovery step 4).
- `marmot den adopt` moves a vault under `$MARMOT_HOME/dens/` and registers reverse routes.
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
| Propose | `den contribute` then `warren propose` (no auto-push); needs edit link (P4) |
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
