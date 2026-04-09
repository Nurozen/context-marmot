// Package curator provides curation analysis for ContextMarmot knowledge graphs.
// It detects quality issues such as orphan nodes, missing summaries, duplicates,
// stale source references, untyped nodes, and disconnected subgraphs.
package curator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/verify"
)

// Suggestion represents a curation recommendation for improving graph quality.
type Suggestion struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`     // "orphan" | "missing_summary" | "duplicate" | "stale" | "untyped" | "disconnected"
	Severity string   `json:"severity"` // "error" | "warning" | "info"
	NodeIDs  []string `json:"node_ids"`
	Message  string   `json:"message"`
	Fix      FixAction `json:"fix"`
}

// FixAction describes a remediation action for a suggestion.
type FixAction struct {
	Command string         `json:"command"`
	Auto    bool           `json:"auto"`
	Args    map[string]any `json:"args,omitempty"`
}

// AnalyzeOpts controls the scope and pagination of the analysis.
type AnalyzeOpts struct {
	NodeIDs    []string // scope to these nodes (nil = all)
	CheckStale bool     // run stale checks (expensive)
	ProjectRoot string  // project root for staleness verification
	Limit      int
	Offset     int
}

// Analyze scans the graph and node store for quality issues and returns
// a sorted, paginated list of suggestions.
func Analyze(g *graph.Graph, ns *node.Store, embedStore *embedding.Store, embedder embedding.Embedder, opts AnalyzeOpts) []Suggestion {
	if g == nil {
		return []Suggestion{}
	}

	// Collect target nodes.
	var nodes []*node.Node
	if len(opts.NodeIDs) > 0 {
		for _, id := range opts.NodeIDs {
			if n, ok := g.GetNode(id); ok {
				nodes = append(nodes, n)
			}
		}
	} else {
		nodes = g.AllActiveNodes()
	}

	if len(nodes) == 0 {
		return []Suggestion{}
	}

	// Build a set for quick lookup.
	nodeSet := make(map[string]*node.Node, len(nodes))
	for _, n := range nodes {
		nodeSet[n.ID] = n
	}

	var suggestions []Suggestion

	// 1. Orphan detection: nodes with 0 in+out edges.
	suggestions = append(suggestions, detectOrphans(g, nodes)...)

	// 2. Missing summaries.
	suggestions = append(suggestions, detectMissingSummaries(nodes)...)

	// 3. Duplicate detection via embeddings.
	suggestions = append(suggestions, detectDuplicates(g, nodes, embedStore, embedder)...)

	// 4. Stale source references.
	if opts.CheckStale {
		suggestions = append(suggestions, detectStale(nodes, opts.ProjectRoot)...)
	}

	// 5. Untyped nodes.
	suggestions = append(suggestions, detectUntyped(nodes)...)

	// 6. Disconnected subgraphs.
	suggestions = append(suggestions, detectDisconnected(g, nodes)...)

	// Deduplicate by suggestion ID.
	suggestions = dedup(suggestions)

	// Sort: error > warning > info, then by node count descending.
	sortSuggestions(suggestions)

	// Paginate.
	if opts.Offset > len(suggestions) {
		return []Suggestion{}
	}
	suggestions = suggestions[opts.Offset:]
	if opts.Limit > 0 && opts.Limit < len(suggestions) {
		suggestions = suggestions[:opts.Limit]
	}

	return suggestions
}

// suggestionID generates a deterministic ID from the suggestion type and sorted node IDs.
func suggestionID(typ string, nodeIDs []string) string {
	sorted := make([]string, len(nodeIDs))
	copy(sorted, nodeIDs)
	sort.Strings(sorted)
	raw := typ + ":" + strings.Join(sorted, ",")
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:16]) // 32 hex chars
}

// detectOrphans finds nodes with zero in-degree and zero out-degree.
func detectOrphans(g *graph.Graph, nodes []*node.Node) []Suggestion {
	var out []Suggestion
	for _, n := range nodes {
		outEdges := g.GetEdges(n.ID, graph.Outbound)
		inEdges := g.GetEdges(n.ID, graph.Inbound)
		if len(outEdges) == 0 && len(inEdges) == 0 {
			ids := []string{n.ID}
			out = append(out, Suggestion{
				ID:       suggestionID("orphan", ids),
				Type:     "orphan",
				Severity: "warning",
				NodeIDs:  ids,
				Message:  fmt.Sprintf("Node %q has no edges — it is disconnected from the graph", n.ID),
				Fix: FixAction{
					Command: fmt.Sprintf("/delete %s", n.ID),
					Auto:    false,
				},
			})
		}
	}
	return out
}

// detectMissingSummaries finds nodes with empty summaries.
func detectMissingSummaries(nodes []*node.Node) []Suggestion {
	var out []Suggestion
	for _, n := range nodes {
		if n.Summary == "" {
			ids := []string{n.ID}
			out = append(out, Suggestion{
				ID:       suggestionID("missing_summary", ids),
				Type:     "missing_summary",
				Severity: "info",
				NodeIDs:  ids,
				Message:  fmt.Sprintf("Node %q has no summary", n.ID),
				Fix: FixAction{
					Command: fmt.Sprintf("/edit %s", n.ID),
					Auto:    false,
					Args:    map[string]any{"field": "summary"},
				},
			})
		}
	}
	return out
}

// detectDuplicates uses the embedding store to find near-duplicate nodes.
func detectDuplicates(g *graph.Graph, nodes []*node.Node, embedStore *embedding.Store, embedder embedding.Embedder) []Suggestion {
	if embedStore == nil || embedder == nil {
		return nil
	}

	var out []Suggestion
	seen := make(map[string]bool) // track pairs to avoid reporting A-B and B-A

	for _, n := range nodes {
		if n.Summary == "" {
			continue
		}

		vec, err := embedder.Embed(n.Summary)
		if err != nil {
			continue
		}

		// Search for the top-2 (self + nearest neighbor).
		results, err := embedStore.Search(vec, 2, embedder.Model())
		if err != nil {
			continue
		}

		for _, r := range results {
			if r.NodeID == n.ID {
				continue
			}
			if r.Score > 0.95 {
				// Build a canonical pair key to avoid duplicates.
				a, b := n.ID, r.NodeID
				if a > b {
					a, b = b, a
				}
				pairKey := a + "|" + b
				if seen[pairKey] {
					continue
				}
				seen[pairKey] = true

				ids := []string{a, b}
				out = append(out, Suggestion{
					ID:       suggestionID("duplicate", ids),
					Type:     "duplicate",
					Severity: "warning",
					NodeIDs:  ids,
					Message:  fmt.Sprintf("Nodes %q and %q appear to be near-duplicates (similarity: %.3f)", a, b, r.Score),
					Fix: FixAction{
						Command: fmt.Sprintf("/merge %s %s", a, b),
						Auto:    false,
					},
				})
			}
		}
	}
	return out
}

// detectStale checks nodes with source references for staleness.
func detectStale(nodes []*node.Node, projectRoot string) []Suggestion {
	var out []Suggestion
	for _, n := range nodes {
		if n.Source.Path == "" {
			continue
		}

		status, err := verify.VerifyStaleness(n, projectRoot)
		if err != nil {
			continue
		}
		if status.IsStale {
			ids := []string{n.ID}
			out = append(out, Suggestion{
				ID:       suggestionID("stale", ids),
				Type:     "stale",
				Severity: "error",
				NodeIDs:  ids,
				Message:  fmt.Sprintf("Node %q has a stale source reference (stored hash differs from current file)", n.ID),
				Fix: FixAction{
					Command: fmt.Sprintf("/verify %s", n.ID),
					Auto:    true,
					Args:    map[string]any{"action": "re-index"},
				},
			})
		}
	}
	return out
}

// detectUntyped finds nodes with empty type fields.
func detectUntyped(nodes []*node.Node) []Suggestion {
	var out []Suggestion
	for _, n := range nodes {
		if n.Type == "" {
			ids := []string{n.ID}

			// Suggest a type based on simple heuristics.
			suggestedType := inferType(n)

			out = append(out, Suggestion{
				ID:       suggestionID("untyped", ids),
				Type:     "untyped",
				Severity: "warning",
				NodeIDs:  ids,
				Message:  fmt.Sprintf("Node %q has no type", n.ID),
				Fix: FixAction{
					Command: fmt.Sprintf("/type %s", suggestedType),
					Auto:    false,
					Args:    map[string]any{"node_id": n.ID, "suggested_type": suggestedType},
				},
			})
		}
	}
	return out
}

// inferType attempts to guess a node type from its ID and content.
func inferType(n *node.Node) string {
	id := strings.ToLower(n.ID)
	switch {
	case strings.Contains(id, "func") || strings.Contains(id, "handler"):
		return "function"
	case strings.Contains(id, "struct") || strings.Contains(id, "type") || strings.Contains(id, "model"):
		return "type"
	case strings.Contains(id, "pkg") || strings.Contains(id, "package") || strings.Contains(id, "module"):
		return "package"
	case strings.Contains(id, "api") || strings.Contains(id, "endpoint"):
		return "api"
	case strings.Contains(id, "test"):
		return "test"
	default:
		return "concept"
	}
}

// detectDisconnected finds connected components with fewer than 3 nodes using union-find.
func detectDisconnected(g *graph.Graph, nodes []*node.Node) []Suggestion {
	if len(nodes) < 3 {
		return nil
	}

	// Union-Find.
	parent := make(map[string]string, len(nodes))
	rank := make(map[string]int, len(nodes))
	for _, n := range nodes {
		parent[n.ID] = n.ID
	}

	var find func(string) string
	find = func(x string) string {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}

	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if rank[ra] < rank[rb] {
			ra, rb = rb, ra
		}
		parent[rb] = ra
		if rank[ra] == rank[rb] {
			rank[ra]++
		}
	}

	// Build node set for filtering edges.
	nodeSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		nodeSet[n.ID] = true
	}

	// Union edges.
	for _, n := range nodes {
		for _, e := range g.GetEdges(n.ID, graph.Outbound) {
			if nodeSet[e.Target] {
				union(n.ID, e.Target)
			}
		}
		for _, e := range g.GetEdges(n.ID, graph.Inbound) {
			if nodeSet[e.Target] {
				union(n.ID, e.Target)
			}
		}
	}

	// Group nodes by component root.
	components := make(map[string][]string)
	for _, n := range nodes {
		root := find(n.ID)
		components[root] = append(components[root], n.ID)
	}

	var out []Suggestion
	for _, members := range components {
		if len(members) < 3 {
			sort.Strings(members)
			out = append(out, Suggestion{
				ID:       suggestionID("disconnected", members),
				Type:     "disconnected",
				Severity: "info",
				NodeIDs:  members,
				Message:  fmt.Sprintf("Small disconnected subgraph with %d node(s): %s", len(members), strings.Join(members, ", ")),
				Fix: FixAction{
					Command: "/link",
					Auto:    false,
					Args:    map[string]any{"node_ids": members},
				},
			})
		}
	}
	return out
}

// dedup removes suggestions with the same ID, keeping the first occurrence.
func dedup(suggestions []Suggestion) []Suggestion {
	seen := make(map[string]bool, len(suggestions))
	out := make([]Suggestion, 0, len(suggestions))
	for _, s := range suggestions {
		if !seen[s.ID] {
			seen[s.ID] = true
			out = append(out, s)
		}
	}
	return out
}

// severityOrder returns a numeric priority for sorting (lower = higher priority).
func severityOrder(s string) int {
	switch s {
	case "error":
		return 0
	case "warning":
		return 1
	case "info":
		return 2
	default:
		return 3
	}
}

// sortSuggestions sorts by severity (error > warning > info), then by node count descending.
func sortSuggestions(suggestions []Suggestion) {
	sort.SliceStable(suggestions, func(i, j int) bool {
		si, sj := severityOrder(suggestions[i].Severity), severityOrder(suggestions[j].Severity)
		if si != sj {
			return si < sj
		}
		return len(suggestions[i].NodeIDs) > len(suggestions[j].NodeIDs)
	})
}

// ProjectRootFromMarmotDir derives the project root from a .marmot directory path.
func ProjectRootFromMarmotDir(marmotDir string) string {
	return filepath.Dir(marmotDir)
}
