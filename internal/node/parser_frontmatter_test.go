package node

import (
	"strings"
	"testing"
)

// TestRoundtripDashesInBody guards the anchored frontmatter scan: a "---"
// line inside the Summary or Context must not be mistaken for the closing
// frontmatter delimiter (the old scanner accepted any line starting "---").
func TestRoundtripDashesInBody(t *testing.T) {
	original := &Node{
		ID:      "docs/divider",
		Type:    "concept",
		Status:  "active",
		Summary: "Uses a horizontal rule:\n---\nafter the rule.",
		Context: "Context with a divider\n---\nand more context.",
		Edges: []Edge{
			{Target: "docs/other", Relation: References, Class: Behavioral},
		},
	}

	data, err := RenderNode(original)
	if err != nil {
		t.Fatalf("RenderNode: %v", err)
	}

	parsed, err := ParseNode(data, "dashes.md")
	if err != nil {
		t.Fatalf("ParseNode: %v", err)
	}
	if parsed.ID != original.ID {
		t.Errorf("ID = %q, want %q", parsed.ID, original.ID)
	}
	if len(parsed.Edges) != 1 || parsed.Edges[0].Target != "docs/other" {
		t.Errorf("Edges = %+v, want the original edge intact", parsed.Edges)
	}
	if !strings.Contains(parsed.Summary, "---") {
		t.Errorf("Summary lost its --- line: %q", parsed.Summary)
	}
	if !strings.Contains(parsed.Context, "---") {
		t.Errorf("Context lost its --- line: %q", parsed.Context)
	}
}

// TestParseNodeDashesInFrontmatterValue: a quoted "---" inside a YAML value
// must not terminate the frontmatter.
func TestParseNodeDashesInFrontmatterValue(t *testing.T) {
	data := []byte("---\nid: cfg/sep\ntype: concept\nstatus: active\ntags:\n  - \"---\"\n---\nSummary text.\n")
	n, err := ParseNode(data, "sep.md")
	if err != nil {
		t.Fatalf("ParseNode: %v", err)
	}
	if n.ID != "cfg/sep" {
		t.Errorf("ID = %q, want cfg/sep", n.ID)
	}
	if len(n.Tags) != 1 || n.Tags[0] != "---" {
		t.Errorf("Tags = %+v, want [---]", n.Tags)
	}
	if n.Summary != "Summary text." {
		t.Errorf("Summary = %q", n.Summary)
	}
}
