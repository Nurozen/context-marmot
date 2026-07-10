// Package api provides the HTTP REST API layer for ContextMarmot.
// It exposes graph, node, search, and namespace operations as JSON endpoints
// backed by the MCP Engine.
package api

import (
	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/warren"
)

// APINode is the JSON representation of a knowledge-graph node.
type APINode struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Namespace    string             `json:"namespace"`
	Status       string             `json:"status"`
	ValidFrom    string             `json:"valid_from,omitempty"`
	ValidUntil   string             `json:"valid_until,omitempty"`
	SupersededBy string             `json:"superseded_by,omitempty"`
	Summary      string             `json:"summary"`
	Context      string             `json:"context"`
	Source       *APISource         `json:"source,omitempty"`
	Edges        []APIEdge          `json:"edges"`
	EdgeCount    int                `json:"edge_count"` // total in+out degree
	IsStale      bool               `json:"is_stale"`
	Tags         []string           `json:"tags"`
	Provenance   *warren.Provenance `json:"provenance,omitempty"`
}

// APISource locates the original source code that a node was derived from.
type APISource struct {
	Path  string `json:"path"`
	Lines [2]int `json:"lines"`
	Hash  string `json:"hash"`
}

// APIEdge is the JSON representation of a directed, typed edge.
type APIEdge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Relation string `json:"relation"`
	Class    string `json:"class"` // "structural", "behavioral", or "bridge"
}

// GraphResponse is returned by the GET /api/graph/{namespace} and /api/graph/_all endpoints.
type GraphResponse struct {
	Namespace  string        `json:"namespace"`
	Nodes      []APINode     `json:"nodes"`
	Edges      []APIEdge     `json:"edges"`
	NodeCount  int           `json:"node_count"`
	EdgeCount  int           `json:"edge_count"`
	HeatPairs  []APIHeatPair `json:"heat_pairs,omitempty"`
	Namespaces []string      `json:"namespaces,omitempty"` // populated only for _all view
	// Skipped lists Warren project IDs whose mounts were unavailable or
	// whose graphs failed to load (Warren graph view only; additive field).
	Skipped []string `json:"skipped,omitempty"`
}

// APIHeatPair represents a co-access frequency pair.
type APIHeatPair struct {
	A      string  `json:"a"`
	B      string  `json:"b"`
	Weight float64 `json:"weight"`
	Hits   int     `json:"hits"`
	Last   string  `json:"last,omitempty"`
}

// SearchResponse is returned by the GET /api/search endpoint.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

// SearchResult is a single semantic search hit.
type SearchResult struct {
	NodeID     string             `json:"node_id"`
	Score      float64            `json:"score"`
	Summary    string             `json:"summary"`
	Type       string             `json:"type"`
	Namespace  string             `json:"namespace"`
	Provenance *warren.Provenance `json:"provenance,omitempty"`
}

// WarrensResponse lists local workspace Warren registrations.
type WarrensResponse struct {
	Warrens map[string]WarrenEntry `json:"warrens"`
}

// WarrenEntry is a registered warren's workspace state plus its computed
// identified projects (checkout vault_id matches this workspace's vault).
// Identity is derived at read time, never stored, so the field lives on the
// response type — additive over the raw WorkspaceWarren shape.
type WarrenEntry struct {
	warren.WorkspaceWarren
	IdentifiedProjects []string `json:"identified_projects,omitempty"`
}

// WarrenStatusResponse describes a registered Warren in this workspace.
type WarrenStatusResponse struct {
	WarrenID string                 `json:"warren_id"`
	Path     string                 `json:"path"`
	Projects []warren.ProjectStatus `json:"projects"`
}

// NamespacesResponse is returned by the GET /api/namespaces endpoint.
type NamespacesResponse struct {
	Namespaces []NamespaceInfo `json:"namespaces"`
}

// NamespaceInfo describes a single namespace.
type NamespaceInfo struct {
	Name       string `json:"name"`
	NodeCount  int    `json:"node_count"`
	HasSummary bool   `json:"has_summary"`
}

// BridgesResponse is returned by the GET /api/bridges endpoint.
type BridgesResponse struct {
	Bridges []BridgeInfo `json:"bridges"`
}

// BridgeInfo describes a cross-namespace or cross-vault bridge.
type BridgeInfo struct {
	Source           string   `json:"source"`
	Target           string   `json:"target"`
	AllowedRelations []string `json:"allowed_relations"`
	IsCrossVault     bool     `json:"is_cross_vault"`
}

// SummaryResponse is returned by the GET /api/summary/{namespace} endpoint.
type SummaryResponse struct {
	Namespace   string `json:"namespace"`
	Content     string `json:"content"`
	NodeCount   int    `json:"node_count"`
	GeneratedAt string `json:"generated_at"`
}

// NodeUpdateRequest is the JSON body for PUT /api/node/{id...}.
type NodeUpdateRequest struct {
	Summary string    `json:"summary,omitempty"`
	Context string    `json:"context,omitempty"`
	Tags    *[]string `json:"tags,omitempty"`
}

// NodeUpdateResponse is returned by PUT /api/node/{id...}.
type NodeUpdateResponse struct {
	NodeID string `json:"node_id"`
	Hash   string `json:"hash"`
	Status string `json:"status"`
	// Warning is set when the node write succeeded but a secondary effect
	// (e.g. the embedding refresh on an editable Warren mount) failed.
	Warning string `json:"warning,omitempty"`
}

// ChatUndoRequest is the JSON body for POST /api/chat/undo. When UndoID is
// empty the server pops the top of the session's undo stack (legacy LIFO
// behavior). When UndoID is set the server finds and removes that specific
// entry — used by the per-row Undo button in the code-mode audit trail.
type ChatUndoRequest struct {
	SessionID string `json:"session_id"`
	UndoID    string `json:"undo_id,omitempty"`
}

// ChatUndoResponse is returned by POST /api/chat/undo.
type ChatUndoResponse struct {
	Restored int    `json:"restored"`
	UndoID   string `json:"undo_id"`
}

// SuggestionsResponse is returned by GET /api/curator/suggestions.
type SuggestionsResponse struct {
	Suggestions []curator.Suggestion `json:"suggestions"`
	NodeCount   int                  `json:"node_count"`
}

// VersionResponse is returned by GET /api/version. Version is the live-reload
// graph version counter (bumped on every vault change); AppVersion is the
// marmot build version injected via ldflags in cmd/marmot.
type VersionResponse struct {
	Version    int64  `json:"version"`
	AppVersion string `json:"app_version"`
}

// ErrorResponse is returned for any API error.
type ErrorResponse struct {
	Error string `json:"error"`
}
