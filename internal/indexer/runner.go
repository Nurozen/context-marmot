package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/node"
)

// NodeStore is the subset of node.Store needed by the Runner to persist nodes.
type NodeStore interface {
	SaveNode(n *node.Node) error
	LoadNode(path string) (*node.Node, error)
	NodePath(id string) string
	ListNodes() ([]node.NodeMeta, error)
}

// EmbeddingStore is the subset of embedding.Store needed by the Runner.
type EmbeddingStore interface {
	Upsert(nodeID string, emb []float32, summaryHash string, model string) error
	StaleCheck(nodeID string, currentHash string) (bool, error)
	FindSimilar(queryEmbedding []float32, threshold float64, model string) ([]embedding.ScoredResult, error)
}

// Embedder generates vector embeddings from text.
type Embedder interface {
	Embed(text string) ([]float32, error)
	EmbedBatch(texts []string) ([][]float32, error)
	Model() string
}

// Classifier performs CRUD classification on incoming nodes. May be nil.
type Classifier interface {
	Classify(ctx context.Context, incoming *node.Node, g GraphReader) (llm.ClassifyResult, error)
}

// GraphReader looks up existing nodes by ID (used by the Classifier).
type GraphReader interface {
	GetNode(id string) (*node.Node, bool)
}

// RunnerConfig configures a directory indexing run.
type RunnerConfig struct {
	// SrcDir is the source directory to index.
	SrcDir string
	// VaultDir is the .marmot vault directory.
	VaultDir string
	// Namespace is the target namespace for generated nodes.
	Namespace string
	// Incremental enables incremental indexing: only re-index files whose
	// source hash has changed since the last run.
	Incremental bool
	// ExtraIgnore is an optional list of additional ignore patterns.
	ExtraIgnore []string
}

// RunResult summarises the outcome of an indexing run.
type RunResult struct {
	Added      int
	Updated    int
	Superseded int
	Skipped    int
	Errors     int
	Total      int
}

// Runner orchestrates indexing an entire directory tree, converting source
// entities into knowledge-graph nodes with embeddings.
type Runner struct {
	config     RunnerConfig
	registry   *Registry
	nodeStore  NodeStore
	embStore   EmbeddingStore
	embedder   Embedder
	classifier Classifier   // may be nil
	graph      GraphReader  // may be nil; used by classifier
}

// NewRunner creates a Runner with the given configuration and dependencies.
// classifier and graph may be nil.
func NewRunner(
	config RunnerConfig,
	registry *Registry,
	nodeStore NodeStore,
	embStore EmbeddingStore,
	embedder Embedder,
	classifier Classifier,
	graph GraphReader,
) *Runner {
	return &Runner{
		config:     config,
		registry:   registry,
		nodeStore:  nodeStore,
		embStore:   embStore,
		embedder:   embedder,
		classifier: classifier,
		graph:      graph,
	}
}

// Run walks the source directory, indexes all supported files, and persists
// the resulting nodes and embeddings. It respects ignore patterns and, when
// Incremental is true, skips files whose source hash has not changed.
func (r *Runner) Run(ctx context.Context) (*RunResult, error) {
	result := &RunResult{}

	// Verify source directory exists before walking.
	if _, err := os.Stat(r.config.SrcDir); err != nil {
		return result, fmt.Errorf("source directory: %w", err)
	}

	ignore := NewIgnoreMatcher(r.config.SrcDir, r.config.ExtraIgnore)

	// Build a set of existing node IDs for incremental mode.
	existingNodes := make(map[string]bool)
	if r.config.Incremental {
		metas, err := r.nodeStore.ListNodes()
		if err == nil {
			for _, m := range metas {
				existingNodes[m.ID] = true
			}
		}
	}

	// Collect entities from all files.
	type pendingNode struct {
		entity   SourceEntity
		prebuilt *node.Node // cached entityToNode result (non-nil when classifier ran)
		action   llm.Action
		target   string // for UPDATE/SUPERSEDE: the existing node ID
	}
	var pending []pendingNode

	walkErr := filepath.Walk(r.config.SrcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		// Compute relative path.
		relPath, relErr := filepath.Rel(r.config.SrcDir, path)
		if relErr != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		// Check ignore patterns.
		if info.IsDir() {
			if relPath == "." {
				return nil
			}
			if ignore.ShouldIgnore(relPath, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if ignore.ShouldIgnore(relPath, false) {
			return nil
		}

		// Check for context cancellation.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Find the right indexer.
		ext := filepath.Ext(path)
		idx, ok := r.registry.IndexerFor(ext)
		if !ok {
			return nil // no indexer for this extension
		}

		// Index the file.
		indexResult, err := idx.IndexFile(path, relPath, r.config.Namespace)
		if err != nil {
			result.Errors++
			return nil // skip files that fail to parse
		}
		if indexResult == nil || len(indexResult.Entities) == 0 {
			return nil
		}

		// Process each entity.
		for _, entity := range indexResult.Entities {
			result.Total++

			// Incremental check: compare source hash.
			if r.config.Incremental && existingNodes[entity.ID] {
				nodePath := r.nodeStore.NodePath(entity.ID)
				existing, loadErr := r.nodeStore.LoadNode(nodePath)
				if loadErr == nil && existing != nil && existing.Source.Hash == entity.Source.Hash {
					result.Skipped++
					continue
				}
			}

			// Determine action.
			action := llm.ActionADD
			var targetNodeID string

			if r.config.Incremental && existingNodes[entity.ID] {
				// Entity already exists with a different hash.
				action = llm.ActionUPDATE
				targetNodeID = entity.ID
			}

			// Use classifier if available for more nuanced decisions.
			var prebuilt *node.Node
			if r.classifier != nil && r.graph != nil {
				prebuilt = entityToNode(entity, r.config.Namespace)
				classResult, classErr := r.classifier.Classify(ctx, prebuilt, r.graph)
				if classErr == nil {
					action = classResult.Action
					targetNodeID = classResult.TargetNodeID
				}
			}

			pending = append(pending, pendingNode{
				entity:   entity,
				prebuilt: prebuilt,
				action:   action,
				target:   targetNodeID,
			})
		}

		return nil
	})

	if walkErr != nil {
		return result, fmt.Errorf("walk source directory: %w", walkErr)
	}

	// Persist nodes and collect texts for batch embedding.
	var embedQueue []embedItem

	now := time.Now().UTC().Format(time.RFC3339)

	for _, p := range pending {
		n := p.prebuilt
		if n == nil {
			n = entityToNode(p.entity, r.config.Namespace)
		}
		n.ValidFrom = now

		switch p.action {
		case llm.ActionNOOP:
			result.Skipped++
			continue

		case llm.ActionADD:
			n.Status = node.StatusActive
			if err := r.nodeStore.SaveNode(n); err != nil {
				result.Errors++
				continue
			}
			result.Added++

		case llm.ActionUPDATE:
			n.Status = node.StatusActive
			if err := r.nodeStore.SaveNode(n); err != nil {
				result.Errors++
				continue
			}
			result.Updated++

		case llm.ActionSUPERSEDE:
			// Mark old node as superseded if it exists and is different.
			if p.target != "" && p.target != n.ID {
				oldPath := r.nodeStore.NodePath(p.target)
				oldNode, loadErr := r.nodeStore.LoadNode(oldPath)
				if loadErr == nil && oldNode != nil {
					oldNode.Status = node.StatusSuperseded
					oldNode.ValidUntil = now
					oldNode.SupersededBy = n.ID
					_ = r.nodeStore.SaveNode(oldNode)
				}
			}
			n.Status = node.StatusActive
			if err := r.nodeStore.SaveNode(n); err != nil {
				result.Errors++
				continue
			}
			result.Superseded++

		default:
			// Unknown action — treat as ADD.
			n.Status = node.StatusActive
			if err := r.nodeStore.SaveNode(n); err != nil {
				result.Errors++
				continue
			}
			result.Added++
		}

		// Queue for embedding.
		embedText := n.Summary
		if n.Context != "" {
			ctx64 := truncateUTF8(n.Context, 6000)
			embedText += "\n\n" + ctx64
		}
		if embedText != "" {
			embedQueue = append(embedQueue, embedItem{nodeID: n.ID, text: embedText})
		}
	}

	// Batch embed all new/changed nodes.
	if r.embedder != nil && len(embedQueue) > 0 {
		result.Errors += r.batchEmbedItems(embedQueue)
	}

	return result, nil
}

// batchEmbedItems embeds a list of (nodeID, text) pairs in batches and upserts
// them into the embedding store. Returns the count of failed upserts.
func (r *Runner) batchEmbedItems(items []embedItem) int {
	const batchSize = 32
	var errCount int

	for i := 0; i < len(items); i += batchSize {
		end := i + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]

		texts := make([]string, len(batch))
		for j, item := range batch {
			texts[j] = item.text
		}

		embeddings, err := r.embedder.EmbedBatch(texts)
		if err != nil {
			// Fall back to individual embedding.
			for _, item := range batch {
				vec, embErr := r.embedder.Embed(item.text)
				if embErr != nil {
					errCount++
					continue
				}
				summaryHash := hashString(item.text)
				if upsertErr := r.embStore.Upsert(item.nodeID, vec, summaryHash, r.embedder.Model()); upsertErr != nil {
					errCount++
				}
			}
			continue
		}

		for j, vec := range embeddings {
			if j >= len(batch) {
				break
			}
			summaryHash := hashString(batch[j].text)
			if upsertErr := r.embStore.Upsert(batch[j].nodeID, vec, summaryHash, r.embedder.Model()); upsertErr != nil {
				errCount++
			}
		}
	}

	return errCount
}

// embedItem holds a node ID and the text to embed.
type embedItem struct {
	nodeID string
	text   string
}

// entityToNode converts a SourceEntity into a node.Node.
func entityToNode(entity SourceEntity, namespace string) *node.Node {
	edges := make([]node.Edge, len(entity.Edges))
	for i, e := range entity.Edges {
		rel := node.EdgeRelation(e.Relation)
		edges[i] = node.Edge{
			Target:   e.Target,
			Relation: rel,
			Class:    node.ClassifyRelation(e.Relation),
		}
	}

	return &node.Node{
		ID:        entity.ID,
		Type:      entity.Type,
		Namespace: namespace,
		Status:    node.StatusActive,
		Source: node.Source{
			Path:  entity.Source.Path,
			Lines: entity.Source.Lines,
			Hash:  entity.Source.Hash,
		},
		Edges:   edges,
		Summary: entity.Summary,
		Context: entity.Context,
	}
}

// hashString returns a simple hash string for embedding staleness tracking.
func hashString(s string) string {
	// Use a simple FNV-inspired hash for speed; not cryptographic.
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}

// truncateUTF8 truncates s to at most maxBytes bytes without splitting a UTF-8 character.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk backwards from maxBytes to find a valid rune boundary.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// String returns a human-readable summary of the run result.
func (r *RunResult) String() string {
	parts := []string{
		fmt.Sprintf("total=%d", r.Total),
		fmt.Sprintf("added=%d", r.Added),
		fmt.Sprintf("updated=%d", r.Updated),
		fmt.Sprintf("superseded=%d", r.Superseded),
		fmt.Sprintf("skipped=%d", r.Skipped),
		fmt.Sprintf("errors=%d", r.Errors),
	}
	return strings.Join(parts, " ")
}
