package llm

import (
	"context"

	"github.com/nurozen/context-marmot/internal/node"
)

// Action is the CRUD classification result.
type Action string

const (
	ActionADD       Action = "ADD"       // New concept, no similar node exists
	ActionUPDATE    Action = "UPDATE"    // Updates an existing node (same concept, richer/newer content)
	ActionSUPERSEDE Action = "SUPERSEDE" // Replaces an existing node (concept evolved significantly)
	ActionNOOP      Action = "NOOP"      // No change needed (content is nearly identical to existing)
)

// CandidateNode is a similar existing node with its similarity score.
type CandidateNode struct {
	Node  *node.Node
	Score float64
}

// ClassifyRequest is the input to the LLM classifier.
type ClassifyRequest struct {
	Incoming   *node.Node      // The node being written
	Candidates []CandidateNode // Similar existing nodes from embedding search (top candidates)
}

// ClassifyResult is the output from the LLM classifier.
type ClassifyResult struct {
	Action       Action  // What to do with the incoming node
	TargetNodeID string  // For UPDATE/SUPERSEDE: the existing node affected. Empty for ADD/NOOP.
	Confidence   float64 // 0.0–1.0
	Reasoning    string  // Human-readable explanation
}

// Provider classifies incoming nodes against similar existing candidates.
type Provider interface {
	Classify(ctx context.Context, req ClassifyRequest) (ClassifyResult, error)
	Model() string
}
