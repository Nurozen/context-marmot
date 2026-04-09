package curator

import (
	"fmt"
	"strings"
)

// BuildSystemPrompt assembles the system prompt for the chat LLM. It includes
// graph statistics and optional context about selected nodes. The result is
// kept concise (under ~500 tokens).
func BuildSystemPrompt(stats GraphStats, selectedNodes []APINodeSummary) string {
	var sb strings.Builder

	sb.WriteString("You are a knowledge graph curator for ContextMarmot. ")
	sb.WriteString("Help the user understand, navigate, and improve their knowledge graph.\n\n")

	// Graph stats.
	sb.WriteString("## Graph Overview\n")
	fmt.Fprintf(&sb, "- Nodes: %d\n", stats.NodeCount)
	fmt.Fprintf(&sb, "- Edges: %d\n", stats.EdgeCount)
	if len(stats.Namespaces) > 0 {
		fmt.Fprintf(&sb, "- Namespaces: %s\n", strings.Join(stats.Namespaces, ", "))
	}
	if stats.IssueCount > 0 {
		fmt.Fprintf(&sb, "- Issues detected: %d\n", stats.IssueCount)
	}

	// Selected node context.
	if len(selectedNodes) > 0 {
		sb.WriteString("\n## Selected Nodes\n")
		for _, n := range selectedNodes {
			fmt.Fprintf(&sb, "- **%s** (type: %s, edges: %d)", n.ID, n.Type, n.Edges)
			if n.Summary != "" {
				summary := n.Summary
				if len(summary) > 200 {
					summary = summary[:200] + "..."
				}
				fmt.Fprintf(&sb, ": %s", summary)
			}
			if len(n.Tags) > 0 {
				fmt.Fprintf(&sb, " [tags: %s]", strings.Join(n.Tags, ", "))
			}
			sb.WriteByte('\n')
		}
	}

	sb.WriteString("\n## Capabilities\n")
	sb.WriteString("You can suggest slash commands for the user to run. Available commands:\n")
	sb.WriteString("- `/tag <tag>` — add a tag to selected nodes\n")
	sb.WriteString("- `/type <type>` — change the type of selected nodes\n")
	sb.WriteString("- `/verify` — verify staleness of selected or all nodes\n")
	sb.WriteString("- `/delete` — delete the selected node\n")
	sb.WriteString("- `/link <src> <rel> <tgt>` — create an edge between two nodes\n")
	sb.WriteString("- `/merge <A> <B>` — merge node B into node A\n")
	sb.WriteString("\nWhen suggesting a command, use the exact slash syntax so the user can copy-paste it.")

	return sb.String()
}
