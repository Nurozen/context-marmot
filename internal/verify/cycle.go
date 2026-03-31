package verify

import (
	"github.com/nurozen/context-marmot/internal/node"
)

// CheckStructuralAcyclicity determines whether the structural edges among the
// given nodes form a DAG. It uses Kahn's algorithm (topological sort).
//
// Returns (true, nil) if the structural subgraph is acyclic.
// Returns (false, cycleNodeIDs) if a cycle exists, where cycleNodeIDs lists
// the node IDs that participate in the cycle.
//
// Behavioral edges are completely ignored.
func CheckStructuralAcyclicity(nodes []*node.Node) (bool, []string) {
	if len(nodes) == 0 {
		return true, nil
	}

	// Build adjacency list and in-degree map for structural edges only.
	// Only consider nodes that actually participate in structural edges.
	adj := make(map[string][]string)    // source -> []target
	inDegree := make(map[string]int)    // node -> structural in-degree

	// Initialize all node IDs in the maps so isolated nodes are included.
	for _, n := range nodes {
		if _, ok := inDegree[n.ID]; !ok {
			inDegree[n.ID] = 0
		}
	}

	for _, n := range nodes {
		for _, e := range n.Edges {
			if e.Class != node.Structural {
				continue
			}
			// Only track edges whose target is in the node set.
			if _, exists := inDegree[e.Target]; !exists {
				continue
			}
			adj[n.ID] = append(adj[n.ID], e.Target)
			inDegree[e.Target]++
		}
	}

	// Kahn's algorithm: start with all nodes that have zero structural in-degree.
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	processed := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		processed++

		for _, target := range adj[current] {
			inDegree[target]--
			if inDegree[target] == 0 {
				queue = append(queue, target)
			}
		}
	}

	totalNodes := len(inDegree)
	if processed == totalNodes {
		return true, nil
	}

	// Collect nodes still in the cycle (in-degree > 0 after Kahn's).
	var cycleNodes []string
	for id, deg := range inDegree {
		if deg > 0 {
			cycleNodes = append(cycleNodes, id)
		}
	}

	return false, cycleNodes
}
