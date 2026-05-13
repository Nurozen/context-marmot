# Warrens

A Warren is a git-backed collection of project Marmot vaults. It is useful when
several repositories belong to the same product, platform, or organization and
agents need cross-project context without cloning every codebase into one
repository.

Warrens are mounted explicitly. Registered projects stay dormant until activated
with `marmot warren mount`, so large company graphs do not become queryable by
accident.

## Repository layout

A Warren has a top-level `_warren.md` manifest and project vaults below
`projects/<project-id>/.marmot/`.

```text
product-warren/
  _warren.md
  projects/
    project-a/
      .marmot/
        _config.md
        _warren.md
        ...
    project-b/
      .marmot/
        _config.md
        _warren.md
        ...
```

Example top-level manifest:

```yaml
---
warren_id: product-platform
version: 1
projects:
  - project_id: project-a
    path: projects/project-a/.marmot
  - project_id: project-b
    path: projects/project-b/.marmot
bridges:
  - source: project-a
    target: project-b
    relations: [calls, reads, references, cross_project]
---

# Product Platform Warren
```

Each project has its own `.marmot/_warren.md` identity file:

```yaml
---
project_id: project-a
warren_id: product-platform
vault_id: project-a-vault
aliases:
  - payments-api
---
```

`vault_id` is the ID used in qualified node references such as
`@project-a-vault/service/api`.

## Workspace layout

Warren state is local to the workspace and stored in the workspace `.marmot`
directory:

```text
virtual-mono/
  .marmot/
    _config.md
    _warren.md
    .marmot-data/
      warrens/
        product-platform/
          projects/
            project-b/
              .marmot/
                ...
  project-a/
  project-b/
```

The workspace `_warren.md` records registered Warren paths, active projects,
editable projects, and whether materialized caches are enabled. This is local
workspace configuration; keep the Warren repo itself in git.

## Build and maintain a Warren

Run authoring commands inside a Warren repository, or pass `--warren-dir` to
point at one.

Create the top-level manifest:

```bash
marmot warren init --id product-platform
```

Add projects explicitly:

```bash
marmot warren project add project-a \
  --path projects/project-a/.marmot \
  --vault-id project-a-vault \
  --alias payments-api
```

`project_id` is durable command and UI identity. It is explicit by default so it
can outlive folder renames. If you want Marmot to choose a conservative ID from
existing project metadata or the path folder name, use:

```bash
marmot warren project add --generate-id --path projects/payments/.marmot
```

Maintain project entries:

```bash
marmot warren project list
marmot warren project list --json
marmot warren project rename project-a payments-api
marmot warren project remove payments-api
```

Add Warren-owned bridge policy between projects:

```bash
marmot warren bridge add project-a project-b --relations calls,reads,references
marmot warren bridge list
marmot warren bridge remove project-a project-b
```

Validate and normalize the Warren:

```bash
marmot warren doctor
marmot warren doctor --json
marmot warren format
```

`doctor` checks the top-level manifest, project paths, project identity files,
ID consistency, duplicate vault IDs, bridge endpoints, bridge relations,
accidental materialized cache folders, and missing embedding databases. Missing
embeddings are warnings because a graph can be valid before it is indexed.

These commands edit Warren files atomically but never commit, push, pull, or
open PRs. Use normal git workflow to review and publish Warren changes.

## Consume a Warren

Register a Warren in the current workspace:

```bash
marmot warren register product-platform /path/to/product-warren
```

List registered Warrens:

```bash
marmot warren list
marmot warren list --json
```

Activate selected projects:

```bash
marmot warren mount --warren product-platform project-a project-b
```

Show project state:

```bash
marmot warren status --warren product-platform
marmot warren status --warren product-platform --json
```

Enable writes for one project:

```bash
marmot warren edit --warren product-platform project-a
```

Disable writes again:

```bash
marmot warren edit --off --warren product-platform project-a
```

Materialize selected project graphs into the local `.marmot-data/` cache:

```bash
marmot warren burrow --materialize --warren product-platform project-b
```

`burrow --materialize` is useful when you want offline graph access or want a
stable local snapshot while the Warren git checkout changes elsewhere.

## Bridge policy

Warren bridges are owned by the top-level Warren manifest:

```yaml
bridges:
  - source: project-a
    target: project-b
    relations: [calls, reads, references, cross_project]
```

At runtime, Marmot converts active Warren bridge endpoints from `project_id` to
their project `vault_id`s and uses the existing cross-vault validation path.
Edges between mounted Warren projects use qualified node IDs:

```yaml
edges:
  - target: "@project-b-vault/service/api"
    relation: calls
```

Both bridge endpoints must be active mounted projects. Dormant projects stay out
of the queryable graph even if a bridge references them, and relations not listed
in the Warren bridge are rejected on write.

## Read and write policy

Mounted Warren projects are read-only by default. They can be queried and viewed,
but Marmot will reject writes to mounted nodes unless that project has been made
editable in the local workspace:

```bash
marmot warren edit --warren product-platform project-a
```

Editability is per project, not per Warren. This supports virtual monorepo
workflows where an agent can reference many services but should only update graph
knowledge for repositories the user is actively editing.

When a Warren node is editable, API/UI updates write back to that project's own
`.marmot/` vault and embedding database. Read-only Warren nodes show provenance
in the detail panel and the save button is disabled.

MCP `context_write` does not accept `@vault-id/...` node IDs directly. Use the
Warren-aware API/UI path for editable mounted nodes, or write local nodes as
usual.

## Query behavior

Active Warren projects are included in MCP and CLI graph queries. Results from
mounted projects use qualified node IDs:

```xml
<node id="@project-a-vault/service/api" ...>
```

Plain local graph views stay local:

- `GET /api/graph/default` returns only local `default` nodes.
- `GET /api/search?q=...&ns=default` returns only local `default` results.
- `GET /api/warren/product-platform/graph` returns active mounted Warren nodes.
- `GET /api/search?q=...&ns=_warren/product-platform` returns Warren-scoped results.

The web UI exposes active Warrens in the graph selector as `Warren <id>`.

## Embeddings and materialization

Each mounted project uses its own embedding database from that project's
`.marmot/.marmot-data/embeddings.db`. The local workspace does not merge all
Warren embeddings into one global database.

When a project is materialized, Marmot reads that project's graph from:

```text
.marmot/.marmot-data/warrens/<warren-id>/projects/<project-id>/.marmot/
```

If no materialized cache exists, Marmot reads the project directly from the
registered Warren checkout.

## Provenance

Warren API and UI responses include provenance for mounted nodes:

```json
{
  "source": "warren_mount",
  "warren_id": "product-platform",
  "project_id": "project-a",
  "vault_id": "project-a-vault",
  "qualified_id": "@project-a-vault/service/api",
  "editable": true
}
```

This lets users and agents distinguish local nodes from mounted Warren nodes and
see whether a selected node can be edited from the current workspace.

## Warrens vs bridges

Use a namespace bridge when two namespaces in the same vault need an explicit
relationship. Use a cross-vault bridge when two independent vaults own their own
bridge files.

Use a Warren when you want a curated set of many project graphs that can be
mounted on demand. Warren project bridges are managed in the Warren repo, not in
each project vault's `_bridges/` folder.
