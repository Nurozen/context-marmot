package classifier

import (
	"context"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/node"
)

// Similarity thresholds for the embedding-distance fallback (used when LLM is nil).
const (
	ThresholdNOOP      = 0.95 // nearly identical
	ThresholdUPDATE    = 0.80 // clearly the same concept, richer content
	ThresholdSUPERSEDE = 0.65 // significantly overlapping but evolved
	// Below ThresholdSUPERSEDE → ADD
)

// SimilaritySearchThreshold is the minimum score for FindSimilar to return candidates.
// Set just below ThresholdSUPERSEDE to catch all cases.
const SimilaritySearchThreshold = 0.60

// EmbeddingStore is the subset of embedding.Store used by the classifier.
type EmbeddingStore interface {
	FindSimilar(queryEmbedding []float32, threshold float64, model string) ([]embedding.ScoredResult, error)
}

// Embedder is the subset of embedding.Embedder used by the classifier.
type Embedder interface {
	Embed(text string) ([]float32, error)
	Model() string
}

// GraphReader allows the classifier to look up existing nodes by ID.
type GraphReader interface {
	GetNode(id string) (*node.Node, bool)
}

// Classifier performs CRUD classification on incoming nodes.
type Classifier struct {
	Store    EmbeddingStore
	Embedder Embedder
	LLM      llm.Provider // optional; nil = embedding-distance fallback
}

// Classify determines the CRUD action for an incoming node by comparing it
// against similar existing nodes in the graph via embedding similarity.
func (c *Classifier) Classify(ctx context.Context, incoming *node.Node, g GraphReader) (llm.ClassifyResult, error) {
	// Step 1: Build embed text from summary and optional context.
	embedText := incoming.Summary
	if incoming.Context != "" {
		ctx64 := incoming.Context
		if len(ctx64) > 6000 {
			ctx64 = ctx64[:6000]
		}
		embedText += "\n\n" + ctx64
	}

	// Step 2: If embedText is empty, return ADD immediately.
	if embedText == "" {
		return llm.ClassifyResult{Action: llm.ActionADD, Reasoning: "no content to compare"}, nil
	}

	// Step 3: Embed the text.
	vec, err := c.Embedder.Embed(embedText)
	if err != nil {
		return llm.ClassifyResult{Action: llm.ActionADD}, nil
	}

	// Step 4: Find similar nodes above the search threshold.
	candidates, err := c.Store.FindSimilar(vec, SimilaritySearchThreshold, c.Embedder.Model())
	if err != nil || len(candidates) == 0 {
		return llm.ClassifyResult{Action: llm.ActionADD, Reasoning: "no similar nodes found"}, nil
	}

	// Step 5: Filter out the incoming node itself.
	filtered := candidates[:0]
	for _, sc := range candidates {
		if sc.NodeID != incoming.ID {
			filtered = append(filtered, sc)
		}
	}

	// Step 6: If no candidates remain, return ADD.
	if len(filtered) == 0 {
		return llm.ClassifyResult{Action: llm.ActionADD, Reasoning: "no distinct similar nodes found"}, nil
	}

	// Step 7 & 8: Resolve candidates to nodes (up to 5).
	llmCandidates := make([]llm.CandidateNode, 0, 5)
	for _, sc := range filtered {
		if len(llmCandidates) >= 5 {
			break
		}
		n, ok := g.GetNode(sc.NodeID)
		if !ok {
			continue
		}
		llmCandidates = append(llmCandidates, llm.CandidateNode{
			Node:  n,
			Score: sc.Score,
		})
	}

	if len(llmCandidates) == 0 {
		return llm.ClassifyResult{Action: llm.ActionADD, Reasoning: "no distinct similar nodes found"}, nil
	}

	// Step 9: Use LLM if available.
	if c.LLM != nil {
		return c.LLM.Classify(ctx, llm.ClassifyRequest{
			Incoming:   incoming,
			Candidates: llmCandidates,
		})
	}

	// Step 10: Fallback — use embedding distance thresholds with the best candidate.
	best := filtered[0] // filtered is already sorted by score descending
	switch {
	case best.Score >= ThresholdNOOP:
		return llm.ClassifyResult{
			Action:       llm.ActionNOOP,
			TargetNodeID: best.NodeID,
			Confidence:   best.Score,
			Reasoning:    "near-identical embedding",
		}, nil
	case best.Score >= ThresholdUPDATE:
		return llm.ClassifyResult{
			Action:       llm.ActionUPDATE,
			TargetNodeID: best.NodeID,
			Confidence:   best.Score,
			Reasoning:    "near-identical embedding",
		}, nil
	case best.Score >= ThresholdSUPERSEDE:
		return llm.ClassifyResult{
			Action:       llm.ActionSUPERSEDE,
			TargetNodeID: best.NodeID,
			Confidence:   best.Score,
			Reasoning:    "near-identical embedding",
		}, nil
	default:
		return llm.ClassifyResult{Action: llm.ActionADD, Reasoning: "no sufficiently similar node"}, nil
	}
}
