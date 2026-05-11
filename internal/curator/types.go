// Package curator provides chat, command, and suggestion types for the
// ContextMarmot graph curation system.
package curator

// ChatRequest is the JSON body for POST /api/chat.
type ChatRequest struct {
	Message       string        `json:"message"`
	History       []ChatMessage `json:"history,omitempty"`
	SelectedNodes []string      `json:"selected_nodes,omitempty"`
	Namespace     string        `json:"namespace,omitempty"`
	SessionID     string        `json:"session_id"`
}

// ChatMessage is a single message in a chat conversation.
type ChatMessage struct {
	Role    string       `json:"role"` // "user" | "assistant" | "system"
	Content string       `json:"content"`
	Actions []ChatAction `json:"actions,omitempty"`
}

// ChatAction is an action embedded in a chat message (e.g. highlight nodes,
// invoke an SDK call, or suggest a slash command).
type ChatAction struct {
	Type    string `json:"type"`    // "highlight" | "sdk_call" | "suggestion"
	Payload any    `json:"payload"`
}

// ChatResponse is the JSON response for POST /api/chat.
type ChatResponse struct {
	Message ChatMessage  `json:"message"`
	UndoID  string       `json:"undo_id,omitempty"`
	CodeRun *CodeRunInfo `json:"code_run,omitempty"`
}

// CodeRunInfo describes a single code-mode execution that took place during
// a chat turn. The frontend renders it as a collapsible panel above the
// final assistant message.
type CodeRunInfo struct {
	Code       string           `json:"code"`
	Result     any              `json:"result"`
	Logs       []string         `json:"logs"`
	Error      string           `json:"error,omitempty"`
	DurationMS int64            `json:"duration_ms"`
	Truncated  bool             `json:"truncated,omitempty"`
	Mutations  []MutationRecord `json:"mutations,omitempty"`
}

// MutationRecord describes a single graph mutation produced by a code-mode
// turn. The frontend renders the list as an audit trail with per-mutation
// Undo buttons, mirroring how slash commands surface their changes.
type MutationRecord struct {
	// Op is one of: tag, untag, type, link, unlink, merge, delete, verify.
	Op string `json:"op"`
	// Message is a human-readable summary ("tagged auth/login with 'critical'").
	Message string `json:"message"`
	// Nodes lists the node IDs affected by this mutation.
	Nodes []string `json:"nodes,omitempty"`
	// UndoID is the per-mutation undo stack entry ID. Empty when no undo is
	// available (e.g. /verify has no inverse).
	UndoID string `json:"undo_id,omitempty"`
	// Success is false when the underlying handler refused the write
	// (validation error, missing node, read-only vault, etc.).
	Success bool `json:"success"`
}

// GraphStats summarises the current state of the knowledge graph for the
// system prompt.
type GraphStats struct {
	NodeCount  int      `json:"node_count"`
	EdgeCount  int      `json:"edge_count"`
	Namespaces []string `json:"namespaces"`
	IssueCount int      `json:"issue_count"`
}

// APINodeSummary is a lightweight node representation included in the system
// prompt when the user has selected specific nodes.
type APINodeSummary struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
	Edges   int      `json:"edges"`
}
