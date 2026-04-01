// Package traversal provides BFS-based graph traversal with token-budget-aware
// compaction into XML output for ContextMarmot agent consumption.
package traversal

import (
	"container/heap"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// TraversalConfig controls the traversal behaviour.
type TraversalConfig struct {
	EntryIDs          []string // Starting node IDs for BFS expansion.
	MaxDepth          int      // Maximum BFS depth from any entry node.
	TokenBudget       int      // Approximate token ceiling (chars/4 heuristic).
	Mode              string   // Compaction mode; default "adjacency".
	IncludeSuperseded bool     // if false (default), superseded/archived nodes are skipped
}

// Subgraph is the result of a traversal: the collected nodes together with
// metadata about which nodes were entry points and the depth at which each
// node was discovered.
type Subgraph struct {
	Nodes      []*node.Node
	EntryNodes map[string]bool
	Depths     map[string]int // depth from nearest entry node
}

// Traverse performs BFS from each entry node in config.EntryIDs up to
// config.MaxDepth, collecting reachable nodes. Nodes are prioritised by
// depth (shallow first) and ordered via a min-heap so that the token budget
// is spent on the most relevant context.
func Traverse(g *graph.Graph, config TraversalConfig) *Subgraph {
	if config.Mode == "" {
		config.Mode = "adjacency"
	}

	sub := &Subgraph{
		EntryNodes: make(map[string]bool),
		Depths:     make(map[string]int),
	}

	// Seed the BFS frontier with entry nodes.
	pq := &nodeQueue{}
	heap.Init(pq)

	for _, id := range config.EntryIDs {
		if _, ok := g.GetNode(id); !ok {
			continue
		}
		sub.EntryNodes[id] = true
		sub.Depths[id] = 0
		heap.Push(pq, &queueItem{id: id, depth: 0})
	}

	// BFS expansion with depth limiting.
	for pq.Len() > 0 {
		item := heap.Pop(pq).(*queueItem)
		id := item.id
		depth := item.depth

		// If the node was already added at a shallower depth via a different
		// path, skip. Depths is written once per node (first arrival wins).
		if recorded, exists := sub.Depths[id]; exists && recorded < depth {
			continue
		}

		n, ok := g.GetNode(id)
		if !ok {
			continue
		}

		// Skip superseded/archived nodes unless explicitly requested.
		if !config.IncludeSuperseded && !n.IsActive() {
			continue
		}

		// Add the node to the result set if not already present.
		if !containsNode(sub.Nodes, id) {
			sub.Nodes = append(sub.Nodes, n)
		}

		// Expand neighbours if within depth limit.
		if depth < config.MaxDepth {
			edges := g.GetEdges(id, graph.Outbound)
			for _, e := range edges {
				if _, seen := sub.Depths[e.Target]; !seen {
					sub.Depths[e.Target] = depth + 1
					heap.Push(pq, &queueItem{id: e.Target, depth: depth + 1})
				}
			}
		}
	}

	// Sort result nodes by depth (shallow first), entry nodes first.
	sortNodes(sub)

	return sub
}

// containsNode checks whether the node slice already has a node with the
// given id.
func containsNode(nodes []*node.Node, id string) bool {
	for _, n := range nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// sortNodes orders the Subgraph.Nodes slice so that entry nodes come first,
// then remaining nodes in ascending depth order.
func sortNodes(sub *Subgraph) {
	type scored struct {
		n     *node.Node
		depth int
		entry bool
	}

	items := make([]scored, len(sub.Nodes))
	for i, n := range sub.Nodes {
		d := sub.Depths[n.ID]
		items[i] = scored{n: n, depth: d, entry: sub.EntryNodes[n.ID]}
	}

	// Stable-ish insertion sort (small N in practice).
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && less(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}

	for i, s := range items {
		sub.Nodes[i] = s.n
	}
}

func less(a, b struct {
	n     *node.Node
	depth int
	entry bool
}) bool {
	if a.entry != b.entry {
		return a.entry // entry nodes come first
	}
	return a.depth < b.depth
}

// ---------------------------------------------------------------------------
// Priority queue (container/heap interface) used for BFS frontier.
// ---------------------------------------------------------------------------

type queueItem struct {
	id    string
	depth int
	index int // managed by heap
}

type nodeQueue []*queueItem

func (q nodeQueue) Len() int            { return len(q) }
func (q nodeQueue) Less(i, j int) bool  { return q[i].depth < q[j].depth }
func (q nodeQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i]; q[i].index = i; q[j].index = j }
func (q *nodeQueue) Push(x interface{}) { item := x.(*queueItem); item.index = len(*q); *q = append(*q, item) }
func (q *nodeQueue) Pop() interface{}   { old := *q; n := len(old); item := old[n-1]; old[n-1] = nil; item.index = -1; *q = old[:n-1]; return item }
