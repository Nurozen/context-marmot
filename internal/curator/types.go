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
	Message ChatMessage `json:"message"`
	UndoID  string      `json:"undo_id,omitempty"`
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
