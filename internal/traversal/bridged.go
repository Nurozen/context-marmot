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
// "@vault-id/" are resolved against the remote vault's graph.
type BridgedGraphResolver struct {
	Local  *graph.Graph
	Vaults VaultGraphProvider // nil = single-vault mode
}

// GetNode resolves a node, delegating to remote vault graphs for @-prefixed IDs.
func (r *BridgedGraphResolver) GetNode(id string) (*node.Node, bool) {
	vaultID, localID := parseVaultPrefix(id)
	if vaultID == "" || r.Vaults == nil {
		return r.Local.GetNode(id)
	}
	g, err := r.Vaults.ResolveGraph(vaultID)
	if err != nil {
		return nil, false
	}
	return g.GetNode(localID)
}

// GetEdges resolves edges, delegating to remote vault graphs for @-prefixed IDs.
// Edge targets from remote vaults are rewritten with @vaultID/ prefix so that
// the BFS frontier can resolve them back through the BridgedGraphResolver.
func (r *BridgedGraphResolver) GetEdges(id string, direction graph.Direction) []node.Edge {
	vaultID, localID := parseVaultPrefix(id)
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
	rewritten := make([]node.Edge, len(edges))
	for i, e := range edges {
		rewritten[i] = e
		// Only rewrite if not already @-prefixed (remote edges might reference other vaults)
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
