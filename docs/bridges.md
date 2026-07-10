# Bridges

For many-project workflows, also see [Warrens](warrens.md). A bridge connects
specific namespaces or vaults. A Warren registers a curated set of project vaults
that can be mounted on demand.

Bridges allow edges and queries to cross namespace or vault boundaries. Three
ownership patterns are supported.

## Namespace bridges (same vault)

Connect two namespaces within the same `.marmot/` vault. Nodes can declare edges to nodes in the other namespace using `namespace/node-id` format.

```bash
# Create a bridge between frontend and backend namespaces
marmot bridge frontend backend --relations calls,reads,references
```

This creates `.marmot/_bridges/frontend--backend.md`:

```yaml
---
source: frontend
target: backend
created: "2026-04-08T10:00:00Z"
allowed_relations:
    - calls
    - reads
    - references
---
```

Once a bridge exists, nodes can declare cross-namespace edges:

```yaml
# In frontend/dashboard.md
edges:
    - target: backend/auth
      relation: calls
```

The bridge manifest authorizes specific relation types at write time --- `context_write` rejects cross-namespace edges that aren't in the `allowed_relations` list.

**Querying across namespaces:** `context_query` searches the global embedding index (all namespaces share one store) and traverses edges across namespace boundaries automatically. No special query syntax needed.

**Graph UI:** Select "All namespaces" in the namespace dropdown to see cross-namespace edges rendered as dashed amber Bezier arcs ("marmot tunnels") between namespace islands.

## Cross-vault bridges (separate projects)

Connect two separate `.marmot/` vaults. Each vault can be a different project with its own embedding store.

```bash
# From your local vault, bridge to a remote vault
marmot bridge /path/to/remote/.marmot --relations cross_project,references
```

This creates matching bridge manifests in **both** vaults and registers them in `~/.marmot/routes.yml` for cross-vault node resolution.

Cross-vault bridge files use `@vault-id` prefixes:

```yaml
---
source: local-vault-id
target: remote-vault-id
source_vault_path: /path/to/local/.marmot
target_vault_path: /path/to/remote/.marmot
source_vault_id: local-vault-id
target_vault_id: remote-vault-id
allowed_relations:
    - cross_project
    - references
---
```

Nodes declare cross-vault edges with the `@vault-id/node-id` format:

```yaml
edges:
    - target: "@remote-vault-id/shared/api-client"
      relation: cross_project
```

**Querying across vaults:** `context_query` automatically searches the remote vault's embedding store (top 3 results per bridged vault) and includes those as additional entry points for graph traversal. Remote nodes appear in results with `@vault-id/` prefixed IDs.

## Warren project bridges (shared Warren repo)

Connect two project vaults that belong to the same Warren. The bridge is owned by
the Warren repo's top-level `_warren.md`, not by `_bridges/` files in each
project vault.

```bash
marmot warren bridge add project-a project-b --relations calls,reads,references
```

The manifest records project IDs:

```yaml
bridges:
  - source: project-a
    target: project-b
    relations:
      - calls
      - reads
      - references
```

At runtime, Marmot maps those project IDs to each project's `vault_id`. Nodes use
the same qualified target syntax as cross-vault bridges:

```yaml
edges:
    - target: "@project-b-vault/shared/api"
      relation: calls
```

Only active mounted Warren projects — or projects *identified* with this
workspace — participate. A bridge to a dormant foreign Warren project is
retained as policy in the manifest, but it is not queryable or accepted for
writes until that project is mounted. An identified project (a Warren
project whose `vault_id` equals this workspace's own) satisfies the endpoint
requirement automatically, with no mount, and its side of the bridge
resolves to the live workspace vault rather than the Warren copy — mounting
the other endpoint is all it takes to turn such a bridge on.

**When to use which cross-project mechanism:** `marmot bridge <path>` (the
cross-vault bridge above) is the right tool for a *pair* of independent
vaults you both control locally — it writes matching `_bridges/` manifests
into each vault and registers both in the global routing table, with a
different default relation set. A Warren is the right tool when a *curated
set* of projects should be shareable and mounted on demand: bridge policy
lives in one reviewed manifest, endpoints activate per workspace, and
nothing touches the other project's vault. The two systems are separate
surfaces today (a warren bridge never creates `_bridges/` files and vice
versa); both refuse to bridge a vault to a copy of itself. Reconciling them
into one surface is future design work.

## Namespace setup

To use namespace bridges, each namespace needs a `_namespace.md` file:

```bash
marmot namespace create frontend --dir .marmot --root-path ../frontend
marmot namespace create backend --dir .marmot --root-path ../backend
```

Marmot also auto-creates a non-default namespace manifest when `context_write`
or `marmot index` first writes a node into that namespace. The explicit command
is still useful when you want to review or set namespace metadata before adding
nodes.

Verify namespaces are detected:

```bash
marmot namespace list --dir .marmot
marmot namespace doctor --dir .marmot
```
