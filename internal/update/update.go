// Package update detects source file changes, propagates staleness through
// the graph, and re-indexes affected nodes. It bridges the verify package
// (hash comparison) with the embedding index (re-embedding) and graph
// (reverse edge walking).
package update

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/verify"
)

// ChangedNode describes a node whose source has changed.
type ChangedNode struct {
	NodeID      string
	StoredHash  string
	CurrentHash string
	SourcePath  string
}

// AffectedNode is a node affected by a change (directly or via reverse edges).
type AffectedNode struct {
	NodeID string
	Reason string // "source_changed" or "dependency_changed:<source_node_id>"
	Depth  int    // Distance from the changed node (0 = direct change)
}

// ReindexResult summarizes a reindex operation.
type ReindexResult struct {
	Updated []string // Node IDs that were successfully re-indexed
	Failed  []string // Node IDs that failed
	Errors  []error  // Corresponding errors for failed nodes
}

// BatchResult aggregates the output of a full detect-propagate-reindex cycle.
type BatchResult struct {
	Changed   []ChangedNode
	Affected  []AffectedNode
	Reindexed *ReindexResult
}

// NodeStore abstracts the node file operations needed by the update engine.
type NodeStore interface {
	LoadNode(path string) (*node.Node, error)
	SaveNode(n *node.Node) error
	NodePath(id string) string
	ListActiveNodes() ([]node.NodeMeta, error)
}

// GraphReader abstracts read-only graph operations.
type GraphReader interface {
	GetNode(id string) (*node.Node, bool)
	GetEdges(id string, direction graph.Direction) []node.Edge
}

// EmbeddingStore abstracts embedding persistence.
type EmbeddingStore interface {
	Upsert(nodeID string, embedding []float32, summaryHash string, model string) error
}

// Embedder abstracts embedding generation.
type Embedder interface {
	Embed(text string) ([]float32, error)
	Model() string
}

// Engine orchestrates change detection and reindexing.
type Engine struct {
	nodeStore NodeStore
	graph     GraphReader
	embStore  EmbeddingStore
	embedder  Embedder
	onChange  func(changedCount int)
}

// NewEngine creates a new update Engine.
func NewEngine(store NodeStore, g GraphReader, embStore EmbeddingStore, embedder Embedder) *Engine {
	return &Engine{
		nodeStore: store,
		graph:     g,
		embStore:  embStore,
		embedder:  embedder,
	}
}

// WithOnChange sets a callback that is invoked when nodes are successfully
// reindexed. The callback receives the count of updated nodes.
func (e *Engine) WithOnChange(fn func(int)) {
	e.onChange = fn
}

// DetectChanges lists all active nodes and identifies those whose source file
// hash has diverged from the stored hash.
func (e *Engine) DetectChanges(ctx context.Context) ([]ChangedNode, error) {
	metas, err := e.nodeStore.ListActiveNodes()
	if err != nil {
		return nil, fmt.Errorf("list active nodes: %w", err)
	}

	var changed []ChangedNode
	for _, m := range metas {
		select {
		case <-ctx.Done():
			return changed, ctx.Err()
		default:
		}

		path := e.nodeStore.NodePath(m.ID)
		n, err := e.nodeStore.LoadNode(path)
		if err != nil {
			continue // skip unloadable nodes
		}

		if n.Source.Path == "" || n.Source.Hash == "" {
			continue // no source reference to verify
		}

		currentHash, err := verify.ComputeSourceHash(n.Source.Path, n.Source.Lines)
		if err != nil {
			// Source file missing or unreadable.
			changed = append(changed, ChangedNode{
				NodeID:      n.ID,
				StoredHash:  n.Source.Hash,
				CurrentHash: "",
				SourcePath:  n.Source.Path,
			})
			continue
		}

		if currentHash != n.Source.Hash {
			changed = append(changed, ChangedNode{
				NodeID:      n.ID,
				StoredHash:  n.Source.Hash,
				CurrentHash: currentHash,
				SourcePath:  n.Source.Path,
			})
		}
	}

	return changed, nil
}

// PropagateStale performs a BFS from the changed nodes through reverse (inbound)
// edges, collecting all nodes transitively affected up to maxDepth.
func (e *Engine) PropagateStale(changedIDs []string, maxDepth int) []AffectedNode {
	if maxDepth < 0 {
		maxDepth = 0
	}

	visited := make(map[string]bool, len(changedIDs))
	var result []AffectedNode

	// Seed the BFS with the directly changed nodes at depth 0.
	type bfsEntry struct {
		id       string
		sourceID string // the original changed node that caused propagation
		depth    int
	}

	queue := make([]bfsEntry, 0, len(changedIDs))
	for _, id := range changedIDs {
		if visited[id] {
			continue
		}
		visited[id] = true
		queue = append(queue, bfsEntry{id: id, sourceID: id, depth: 0})
		result = append(result, AffectedNode{
			NodeID: id,
			Reason: "source_changed",
			Depth:  0,
		})
	}

	// BFS through inbound edges.
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.depth >= maxDepth {
			continue
		}

		inEdges := e.graph.GetEdges(cur.id, graph.Inbound)
		for _, edge := range inEdges {
			depID := edge.Target // in inEdges, Target holds the source node
			if visited[depID] {
				continue
			}
			visited[depID] = true
			newDepth := cur.depth + 1
			queue = append(queue, bfsEntry{id: depID, sourceID: cur.sourceID, depth: newDepth})
			result = append(result, AffectedNode{
				NodeID: depID,
				Reason: fmt.Sprintf("dependency_changed:%s", cur.sourceID),
				Depth:  newDepth,
			})
		}
	}

	return result
}

// Reindex re-reads source files, recomputes hashes, regenerates embeddings,
// and persists the updated nodes for each given node ID.
func (e *Engine) Reindex(ctx context.Context, nodeIDs []string) *ReindexResult {
	result := &ReindexResult{}

	for _, id := range nodeIDs {
		select {
		case <-ctx.Done():
			result.Failed = append(result.Failed, id)
			result.Errors = append(result.Errors, ctx.Err())
			return result
		default:
		}

		if err := e.reindexNode(id); err != nil {
			result.Failed = append(result.Failed, id)
			result.Errors = append(result.Errors, err)
		} else {
			result.Updated = append(result.Updated, id)
		}
	}

	if e.onChange != nil && len(result.Updated) > 0 {
		e.onChange(len(result.Updated))
	}

	return result
}

// reindexNode handles reindexing a single node.
func (e *Engine) reindexNode(id string) error {
	path := e.nodeStore.NodePath(id)
	n, err := e.nodeStore.LoadNode(path)
	if err != nil {
		return fmt.Errorf("load node %s: %w", id, err)
	}

	// Update source hash if there is a source path.
	if n.Source.Path != "" {
		newHash, err := verify.ComputeSourceHash(n.Source.Path, n.Source.Lines)
		if err != nil {
			// If file is missing, clear the hash.
			n.Source.Hash = ""
		} else {
			n.Source.Hash = newHash
		}
	}

	// Build embed text: summary + truncated context (matching pipeline.go logic).
	text := n.Summary
	if n.Context != "" {
		ctx := n.Context
		if len(ctx) > 6000 {
			ctx = ctx[:6000]
		}
		text = n.Summary + "\n\n" + ctx
	}
	if text == "" {
		text = n.ID
	}

	// Generate embedding.
	vec, err := e.embedder.Embed(text)
	if err != nil {
		return fmt.Errorf("embed node %s: %w", id, err)
	}

	// Compute summary hash for the embedding store.
	summaryHash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))

	// Upsert embedding.
	if err := e.embStore.Upsert(id, vec, summaryHash, e.embedder.Model()); err != nil {
		return fmt.Errorf("upsert embedding for %s: %w", id, err)
	}

	// Persist updated node.
	if err := e.nodeStore.SaveNode(n); err != nil {
		return fmt.Errorf("save node %s: %w", id, err)
	}

	return nil
}

// RunBatchUpdate performs a full detect-propagate-reindex cycle.
func (e *Engine) RunBatchUpdate(ctx context.Context, maxPropagationDepth int) (*BatchResult, error) {
	changed, err := e.DetectChanges(ctx)
	if err != nil {
		return nil, fmt.Errorf("detect changes: %w", err)
	}

	changedIDs := make([]string, len(changed))
	for i, c := range changed {
		changedIDs[i] = c.NodeID
	}

	affected := e.PropagateStale(changedIDs, maxPropagationDepth)

	// Collect unique node IDs from affected set for reindexing.
	reindexIDs := make([]string, len(affected))
	for i, a := range affected {
		reindexIDs[i] = a.NodeID
	}

	reindexed := e.Reindex(ctx, reindexIDs)

	return &BatchResult{
		Changed:   changed,
		Affected:  affected,
		Reindexed: reindexed,
	}, nil
}

