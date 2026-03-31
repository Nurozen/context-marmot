// Package graph provides an in-memory graph engine for ContextMarmot.
// It loads nodes from a node store, maintains forward and reverse adjacency
// lists, and enforces structural acyclicity (behavioral cycles are allowed).
package graph

import (
	"fmt"
	"sync"

	"github.com/nurozen/context-marmot/internal/node"
)

// Direction indicates edge traversal direction.
type Direction int

const (
	// Outbound follows edges from source to target.
	Outbound Direction = iota
	// Inbound follows edges from target back to source.
	Inbound
)

// Graph is the in-memory graph engine. All methods are safe for concurrent
// read access, but writes must be externally serialised (or use the embedded
// mutex).
type Graph struct {
	mu       sync.RWMutex
	nodes    map[string]*node.Node
	outEdges map[string][]node.Edge // source ID -> outbound edges
	inEdges  map[string][]node.Edge // target ID -> inbound edges (with Target set to source)
}

// NewGraph returns an empty, initialised Graph.
func NewGraph() *Graph {
	return &Graph{
		nodes:    make(map[string]*node.Node),
		outEdges: make(map[string][]node.Edge),
		inEdges:  make(map[string][]node.Edge),
	}
}

// AddNode inserts a node into the graph and registers all of its edges in
// both forward and reverse adjacency lists. Returns an error if a node with
// the same ID already exists.
func (g *Graph) AddNode(n *node.Node) error {
	if n == nil {
		return fmt.Errorf("graph: cannot add nil node")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[n.ID]; exists {
		return fmt.Errorf("graph: node %q already exists", n.ID)
	}

	g.nodes[n.ID] = n

	for _, e := range n.Edges {
		// Ensure edge class is populated.
		if e.Class == "" {
			e.Class = node.ClassifyRelation(string(e.Relation))
		}
		g.outEdges[n.ID] = append(g.outEdges[n.ID], e)

		// Store reverse edge: in inEdges[target], the "Target" field holds
		// the source ID for reverse lookups.
		rev := node.Edge{
			Target:   n.ID,
			Relation: e.Relation,
			Class:    e.Class,
		}
		g.inEdges[e.Target] = append(g.inEdges[e.Target], rev)
	}

	return nil
}

// UpsertNode inserts or replaces a node in the graph. If the node already
// exists, it is removed first (including edge cleanup) and re-added.
func (g *Graph) UpsertNode(n *node.Node) error {
	if n == nil {
		return fmt.Errorf("graph: cannot upsert nil node")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// If the node exists, remove it first (without holding the lock again).
	if _, exists := g.nodes[n.ID]; exists {
		g.removeNodeLocked(n.ID)
	}

	g.nodes[n.ID] = n
	for _, e := range n.Edges {
		if e.Class == "" {
			e.Class = node.ClassifyRelation(string(e.Relation))
		}
		g.outEdges[n.ID] = append(g.outEdges[n.ID], e)
		rev := node.Edge{
			Target:   n.ID,
			Relation: e.Relation,
			Class:    e.Class,
		}
		g.inEdges[e.Target] = append(g.inEdges[e.Target], rev)
	}
	return nil
}

// RemoveNode removes a node and cascades cleanup of all edges to and from it.
func (g *Graph) RemoveNode(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[id]; !exists {
		return fmt.Errorf("graph: node %q not found", id)
	}

	g.removeNodeLocked(id)
	return nil
}

// removeNodeLocked removes a node without acquiring the mutex. Caller must
// hold g.mu in write mode.
func (g *Graph) removeNodeLocked(id string) {
	// Remove outbound edges: for each outbound edge from id -> target,
	// remove the corresponding reverse entry in inEdges[target].
	for _, e := range g.outEdges[id] {
		g.inEdges[e.Target] = removeEdgeFromSlice(g.inEdges[e.Target], id)
		if len(g.inEdges[e.Target]) == 0 {
			delete(g.inEdges, e.Target)
		}
	}
	delete(g.outEdges, id)

	// Remove inbound edges: for each inbound edge source -> id,
	// remove the corresponding forward entry in outEdges[source].
	for _, e := range g.inEdges[id] {
		sourceID := e.Target // in inEdges, Target holds the source
		g.outEdges[sourceID] = removeEdgeByTarget(g.outEdges[sourceID], id)
		if len(g.outEdges[sourceID]) == 0 {
			delete(g.outEdges, sourceID)
		}
	}
	delete(g.inEdges, id)

	delete(g.nodes, id)
}

// AddEdge adds a single edge from sourceID. Structural edges are checked for
// acyclicity before insertion; behavioral edges are allowed freely.
func (g *Graph) AddEdge(sourceID string, e node.Edge) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[sourceID]; !exists {
		return fmt.Errorf("graph: source node %q not found", sourceID)
	}

	// Classify if not already set.
	if e.Class == "" {
		e.Class = node.ClassifyRelation(string(e.Relation))
	}

	// For structural edges, reject if adding would create a cycle.
	if e.Class == node.Structural {
		if hasStructuralCycle(g, sourceID, e.Target) {
			return fmt.Errorf("graph: structural edge %s -> %s would create a cycle", sourceID, e.Target)
		}
	}

	g.outEdges[sourceID] = append(g.outEdges[sourceID], e)

	rev := node.Edge{
		Target:   sourceID,
		Relation: e.Relation,
		Class:    e.Class,
	}
	g.inEdges[e.Target] = append(g.inEdges[e.Target], rev)

	return nil
}

// RemoveEdge removes the first edge from sourceID to targetID.
func (g *Graph) RemoveEdge(sourceID, targetID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	before := len(g.outEdges[sourceID])
	g.outEdges[sourceID] = removeEdgeByTarget(g.outEdges[sourceID], targetID)
	if len(g.outEdges[sourceID]) == before {
		return fmt.Errorf("graph: no edge from %q to %q", sourceID, targetID)
	}
	if len(g.outEdges[sourceID]) == 0 {
		delete(g.outEdges, sourceID)
	}

	g.inEdges[targetID] = removeEdgeFromSlice(g.inEdges[targetID], sourceID)
	if len(g.inEdges[targetID]) == 0 {
		delete(g.inEdges, targetID)
	}

	return nil
}

// GetNode returns the node with the given ID, or (nil, false) if not found.
func (g *Graph) GetNode(id string) (*node.Node, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n, ok := g.nodes[id]
	return n, ok
}

// GetEdges returns edges for the given node in the specified direction.
// Outbound returns edges from the node; Inbound returns edges pointing to it
// (with Target set to the source node).
func (g *Graph) GetEdges(id string, direction Direction) []node.Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()

	switch direction {
	case Outbound:
		edges := g.outEdges[id]
		out := make([]node.Edge, len(edges))
		copy(out, edges)
		return out
	case Inbound:
		edges := g.inEdges[id]
		out := make([]node.Edge, len(edges))
		copy(out, edges)
		return out
	default:
		return nil
	}
}

// GetNeighbors performs BFS from the given node up to the specified depth and
// returns all reachable nodes (excluding the start node). Follows outbound
// edges only.
func (g *Graph) GetNeighbors(id string, depth int) []*node.Node {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if depth <= 0 {
		return nil
	}
	if _, exists := g.nodes[id]; !exists {
		return nil
	}

	visited := map[string]bool{id: true}
	queue := []string{id}
	var result []*node.Node

	for d := 0; d < depth && len(queue) > 0; d++ {
		var nextQueue []string
		for _, cur := range queue {
			for _, e := range g.outEdges[cur] {
				if !visited[e.Target] {
					visited[e.Target] = true
					nextQueue = append(nextQueue, e.Target)
					if n, ok := g.nodes[e.Target]; ok {
						result = append(result, n)
					}
				}
			}
		}
		queue = nextQueue
	}

	return result
}

// AllNodes returns a snapshot of all nodes in the graph.
func (g *Graph) AllNodes() []*node.Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	nodes := make([]*node.Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		nodes = append(nodes, n)
	}
	return nodes
}

// NodeCount returns the number of nodes in the graph.
func (g *Graph) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

// EdgeCount returns the total number of directed edges in the graph.
func (g *Graph) EdgeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	count := 0
	for _, edges := range g.outEdges {
		count += len(edges)
	}
	return count
}

// WouldCreateCycle checks whether adding a structural edge from sourceID to
// targetID would create a cycle, without actually modifying the graph. If
// sourceID is not yet in the graph, it is treated as an isolated new node
// for the purpose of the check.
func (g *Graph) WouldCreateCycle(sourceID, targetID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return hasStructuralCycle(g, sourceID, targetID)
}

// removeEdgeByTarget removes the first edge whose Target matches targetID.
func removeEdgeByTarget(edges []node.Edge, targetID string) []node.Edge {
	for i, e := range edges {
		if e.Target == targetID {
			return append(edges[:i], edges[i+1:]...)
		}
	}
	return edges
}

// removeEdgeFromSlice removes the first edge whose Target matches sourceID
// (used for reverse-adjacency cleanup where Target stores the source).
func removeEdgeFromSlice(edges []node.Edge, sourceID string) []node.Edge {
	for i, e := range edges {
		if e.Target == sourceID {
			return append(edges[:i], edges[i+1:]...)
		}
	}
	return edges
}
