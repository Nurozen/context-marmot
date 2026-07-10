package traversal

import (
	"strings"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// VaultGraphProvider resolves graphs for remote vaults by vault ID.
// Implemented by namespace.VaultRegistry.
type VaultGraphProvider interface {
	ResolveGraph(vaultID string) (*graph.Graph, error)
}

// BridgedGraphResolver wraps a local graph and optional VaultGraphProvider
// to enable traversal across vault boundaries. Node IDs prefixed with
// "@vault-id/" are resolved against the remote vault's graph — except the
// local vault's own ID, which resolves against the live in-memory graph
// (never the registry: a registry hit would load a second read-only copy
// from disk with TTL staleness and take a shared read lock on our own
// vault).
type BridgedGraphResolver struct {
	Local        *graph.Graph
	Vaults       VaultGraphProvider // nil = single-vault mode
	LocalVaultID string             // non-empty: "@LocalVaultID/x" resolves against Local (live), not Vaults
}

// GetNode resolves a node, delegating to remote vault graphs for @-prefixed IDs.
// For remote nodes, the returned node is a shallow copy with its ID rewritten to
// the @vault-id/node-id form so it matches the traversal key used in Depths/EntryNodes.
func (r *BridgedGraphResolver) GetNode(id string) (*node.Node, bool) {
	vaultID, localID := parseVaultPrefix(id)
	if vaultID != "" && r.LocalVaultID != "" && vaultID == r.LocalVaultID {
		// "@LocalVaultID/x" is a local node wearing a costume: serve it from
		// the live graph with the same @-qualified ID-rewrite discipline as
		// the remote path, so traversal keys stay consistent.
		n, ok := r.Local.GetNode(localID)
		if !ok {
			return nil, false
		}
		cp := *n
		cp.ID = id
		if len(n.Edges) > 0 {
			cp.Edges = append([]node.Edge(nil), n.Edges...)
		}
		return &cp, true
	}
	if vaultID == "" || r.Vaults == nil {
		return r.Local.GetNode(id)
	}
	g, err := r.Vaults.ResolveGraph(vaultID)
	if err != nil {
		return nil, false
	}
	n, ok := g.GetNode(localID)
	if !ok {
		return nil, false
	}
	// Return a copy with the @-prefixed ID so the node's ID is consistent
	// with the key used throughout traversal and compaction. Deep-copy the
	// Edges slice so mutations can't corrupt the remote vault's cached graph.
	cp := *n
	cp.ID = id
	if len(n.Edges) > 0 {
		cp.Edges = make([]node.Edge, len(n.Edges))
		copy(cp.Edges, n.Edges)
	}
	return &cp, true
}

// GetEdges resolves edges, delegating to remote vault graphs for @-prefixed IDs.
// Edge targets from remote vaults are rewritten with @vaultID/ prefix so that
// the BFS frontier can resolve them back through the BridgedGraphResolver.
func (r *BridgedGraphResolver) GetEdges(id string, direction graph.Direction) []node.Edge {
	vaultID, localID := parseVaultPrefix(id)
	if vaultID != "" && r.LocalVaultID != "" && vaultID == r.LocalVaultID {
		return rewriteEdgeTargets(r.Local.GetEdges(localID, direction), vaultID)
	}
	if vaultID == "" || r.Vaults == nil {
		return r.Local.GetEdges(id, direction)
	}
	g, err := r.Vaults.ResolveGraph(vaultID)
	if err != nil {
		return nil
	}
	edges := g.GetEdges(localID, direction)
	// Rewrite edge targets to be @-prefixed so the traversal BFS can resolve them
	// back through the BridgedGraphResolver for cross-vault node lookups.
	// For inbound edges, Target holds the source node (within the remote vault);
	// rewrite similarly.
	return rewriteEdgeTargets(edges, vaultID)
}

// rewriteEdgeTargets prefixes unqualified edge targets with @vaultID/ so the
// BFS frontier resolves them back through the BridgedGraphResolver. Targets
// that already carry an @-prefix are left alone (they may reference other
// vaults).
func rewriteEdgeTargets(edges []node.Edge, vaultID string) []node.Edge {
	rewritten := make([]node.Edge, len(edges))
	for i, e := range edges {
		rewritten[i] = e
		if !strings.HasPrefix(e.Target, "@") {
			rewritten[i].Target = "@" + vaultID + "/" + e.Target
		}
	}
	return rewritten
}

// parseVaultPrefix splits "@vault-id/node-id" into (vaultID, nodeID).
// Returns ("", id) if id has no vault prefix.
func parseVaultPrefix(id string) (string, string) {
	if !strings.HasPrefix(id, "@") {
		return "", id
	}
	rest := id[1:]
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return "", id
	}
	return rest[:idx], rest[idx+1:]
}
