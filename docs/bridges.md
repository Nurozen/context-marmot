# Bridges

Bridges allow edges and queries to cross namespace or vault boundaries. Two types are supported.

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

## Namespace setup

To use namespace bridges, each namespace needs a `_namespace.md` file:

```bash
# Create namespace directories with manifests
mkdir -p .marmot/frontend .marmot/backend

cat > .marmot/frontend/_namespace.md << 'EOF'
---
name: frontend
description: Frontend UI components
---
EOF

cat > .marmot/backend/_namespace.md << 'EOF'
---
name: backend
description: Backend API services
---
EOF
```

Verify namespaces are detected:

```bash
marmot status --dir .marmot
# Should show: Namespaces: frontend, backend
```
