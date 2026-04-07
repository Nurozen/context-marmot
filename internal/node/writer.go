package node

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatterFields holds the YAML-serializable subset of a Node for
// frontmatter rendering. We use a separate struct so that body-only fields
// (Summary, Context, RawBody) are never emitted in frontmatter.
type frontmatterFields struct {
	ID           string   `yaml:"id"`
	Type         string   `yaml:"type"`
	Namespace    string   `yaml:"namespace"`
	Status       string   `yaml:"status"`
	ValidFrom    string   `yaml:"valid_from,omitempty"`
	ValidUntil   string   `yaml:"valid_until,omitempty"`
	SupersededBy string   `yaml:"superseded_by,omitempty"`
	Source       *Source  `yaml:"source,omitempty"`
	Edges        []fmEdge `yaml:"edges,omitempty"`
	Tags         []string `yaml:"tags,omitempty"`
}

// fmEdge is the YAML representation of an edge (Class is omitted).
type fmEdge struct {
	Target   string `yaml:"target"`
	Relation string `yaml:"relation"`
}

// RenderNode serializes a Node into Obsidian-compatible markdown with YAML
// frontmatter, summary paragraph, relationships section, and context section.
func RenderNode(node *Node) ([]byte, error) {
	if node == nil {
		return nil, fmt.Errorf("render: nil node")
	}

	var buf bytes.Buffer

	// --- Frontmatter ---
	fm := frontmatterFields{
		ID:           node.ID,
		Type:         node.Type,
		Namespace:    node.Namespace,
		Status:       node.Status,
		ValidFrom:    node.ValidFrom,
		ValidUntil:   node.ValidUntil,
		SupersededBy: node.SupersededBy,
	}
	if node.Source.Path != "" || node.Source.Hash != "" || node.Source.Lines != [2]int{} {
		fm.Source = &node.Source
	}
	for _, e := range node.Edges {
		fm.Edges = append(fm.Edges, fmEdge{
			Target:   e.Target,
			Relation: string(e.Relation),
		})
	}
	fm.Tags = node.Tags

	yamlBytes, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("render: marshal frontmatter: %w", err)
	}

	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")

	// --- Summary ---
	if s := strings.TrimSpace(node.Summary); s != "" {
		buf.WriteString("\n")
		buf.WriteString(s)
		buf.WriteString("\n")
	}

	// --- Relationships ---
	if len(node.Edges) > 0 {
		buf.WriteString("\n## Relationships\n\n")

		// Group edges by relation for readability.
		for _, e := range node.Edges {
			fmt.Fprintf(&buf, "- **%s** [[%s]]\n", e.Relation, e.Target)
		}
	}

	// --- Context ---
	if c := strings.TrimSpace(node.Context); c != "" {
		buf.WriteString("\n## Context\n\n")
		buf.WriteString(c)
		buf.WriteString("\n")
	}

	return buf.Bytes(), nil
}
