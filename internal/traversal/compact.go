package traversal

import (
	"fmt"
	"html"
	"strings"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// CompactedResult holds the XML output of graph compaction together with
// accounting metadata.
type CompactedResult struct {
	XML          string   // Well-formed XML string.
	TokenEstimate int     // Approximate token count (chars/4).
	NodeCount    int      // Number of nodes included (full + compact).
	TruncatedIDs []string // Node IDs that exceeded the token budget.
}

// Compact serialises a Subgraph into token-budget-aware XML.
//
// Entry/depth-0 nodes receive full <node> elements (summary, edges, context).
// Deeper nodes receive <node_compact> elements (summary, source ref only).
// Nodes that would exceed the budget are listed in a <truncated> section.
func Compact(g *graph.Graph, subgraph *Subgraph, budget int) *CompactedResult {
	if subgraph == nil || len(subgraph.Nodes) == 0 {
		xml := `<context_result tokens="0" nodes="0">` + "\n" + `</context_result>`
		return &CompactedResult{
			XML:          xml,
			TokenEstimate: estimateTokens(xml),
			NodeCount:    0,
		}
	}

	var (
		fullParts      []string
		compactParts   []string
		truncatedIDs   []string
		usedChars      int
		includedCount  int
	)

	// Reserve some budget for the wrapper elements and truncated section.
	wrapperOverhead := 120 // rough estimate for context_result tags + truncated section
	effectiveBudget := budget * 4 // convert token budget to char budget
	if effectiveBudget < wrapperOverhead {
		effectiveBudget = wrapperOverhead
	}

	for _, n := range subgraph.Nodes {
		depth := subgraph.Depths[n.ID]
		isFull := subgraph.EntryNodes[n.ID] || depth == 0

		var part string
		if isFull {
			part = renderFullNode(g, n, depth)
		} else {
			part = renderCompactNode(n, depth)
		}

		partChars := len(part)
		if usedChars+partChars+wrapperOverhead <= effectiveBudget {
			if isFull {
				fullParts = append(fullParts, part)
			} else {
				compactParts = append(compactParts, part)
			}
			usedChars += partChars
			includedCount++
		} else {
			truncatedIDs = append(truncatedIDs, n.ID)
		}
	}

	// Build complete XML document.
	var b strings.Builder

	totalTokens := estimateTokens(strings.Join(fullParts, "") + strings.Join(compactParts, ""))

	b.WriteString(fmt.Sprintf(`<context_result tokens="%d" nodes="%d">`, totalTokens, includedCount))
	b.WriteString("\n")

	for _, p := range fullParts {
		b.WriteString(p)
	}
	for _, p := range compactParts {
		b.WriteString(p)
	}

	// Truncated section.
	if len(truncatedIDs) > 0 {
		b.WriteString("  <truncated>\n")
		for _, id := range truncatedIDs {
			b.WriteString(fmt.Sprintf("    <node_ref id=%q reason=\"budget\"/>\n", id))
		}
		b.WriteString("  </truncated>\n")
	}

	b.WriteString("</context_result>")

	xml := b.String()
	return &CompactedResult{
		XML:           xml,
		TokenEstimate: estimateTokens(xml),
		NodeCount:     includedCount,
		TruncatedIDs:  truncatedIDs,
	}
}

// renderFullNode produces a <node> element with summary, edges, and context.
func renderFullNode(g *graph.Graph, n *node.Node, depth int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  <node id=%q type=%q depth=\"%d\">\n",
		n.ID, n.Type, depth))

	b.WriteString(fmt.Sprintf("    <summary>%s</summary>\n",
		html.EscapeString(n.Summary)))

	// Edges from graph (outbound).
	edges := g.GetEdges(n.ID, graph.Outbound)
	if len(edges) > 0 {
		b.WriteString("    <edges>\n")
		for _, e := range edges {
			b.WriteString(fmt.Sprintf("      <edge target=%q relation=%q/>\n",
				e.Target, string(e.Relation)))
		}
		b.WriteString("    </edges>\n")
	}

	// Context section (code content).
	if n.Context != "" {
		b.WriteString(fmt.Sprintf("    <context language=\"\">%s</context>\n",
			html.EscapeString(n.Context)))
	}

	b.WriteString("  </node>\n")
	return b.String()
}

// renderCompactNode produces a <node_compact> element with summary and source
// reference only — no context block.
func renderCompactNode(n *node.Node, depth int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  <node_compact id=%q type=%q depth=\"%d\">\n",
		n.ID, n.Type, depth))

	b.WriteString(fmt.Sprintf("    <summary>%s</summary>\n",
		html.EscapeString(n.Summary)))

	if n.Source.Path != "" {
		if n.Source.Lines[0] != 0 || n.Source.Lines[1] != 0 {
			b.WriteString(fmt.Sprintf("    <source path=%q lines=\"%d-%d\"/>\n",
				n.Source.Path, n.Source.Lines[0], n.Source.Lines[1]))
		} else {
			b.WriteString(fmt.Sprintf("    <source path=%q/>\n", n.Source.Path))
		}
	}

	b.WriteString("  </node_compact>\n")
	return b.String()
}

// estimateTokens applies the chars/4 heuristic for token estimation.
func estimateTokens(s string) int {
	return len(s) / 4
}
