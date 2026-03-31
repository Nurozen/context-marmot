package graph

import (
	"github.com/nurozen/context-marmot/internal/node"
)

// hasStructuralCycle checks whether adding a structural edge from sourceID to
// targetID would create a cycle among structural edges. It performs DFS from
// targetID, following only structural outbound edges. If sourceID is reachable,
// a cycle would be formed.
//
// This function assumes the caller holds at least a read lock on g.
func hasStructuralCycle(g *Graph, sourceID, targetID string) bool {
	// Self-loop is always a cycle.
	if sourceID == targetID {
		return true
	}

	visited := make(map[string]bool)
	return dfsStructural(g, targetID, sourceID, visited)
}

// dfsStructural recursively searches from current following only structural
// outbound edges. Returns true if goal is reachable.
func dfsStructural(g *Graph, current, goal string, visited map[string]bool) bool {
	if current == goal {
		return true
	}
	if visited[current] {
		return false
	}
	visited[current] = true

	for _, e := range g.outEdges[current] {
		if e.Class == node.Structural {
			if dfsStructural(g, e.Target, goal, visited) {
				return true
			}
		}
	}
	return false
}
