package node

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// wikilinkRe matches [[wikilink]] patterns in markdown body text.
var wikilinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// ParseNode parses an Obsidian-compatible markdown file (YAML frontmatter +
// markdown body) into a Node struct. filePath is used only for error messages.
func ParseNode(data []byte, filePath string) (*Node, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("parse %s: empty file", filePath)
	}

	frontmatter, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filePath, err)
	}

	var n Node
	if err := yaml.Unmarshal(frontmatter, &n); err != nil {
		return nil, fmt.Errorf("parse %s: invalid YAML: %w", filePath, err)
	}

	// Derive edge class from relation type.
	for i := range n.Edges {
		n.Edges[i].Class = ClassifyRelation(string(n.Edges[i].Relation))
	}

	// Parse body sections.
	n.RawBody = body
	n.Summary = extractSummary(body)
	n.Context = extractSection(body, "Context")

	return &n, nil
}

// splitFrontmatter separates YAML frontmatter (between --- delimiters) from
// the remaining markdown body. Returns (frontmatter bytes, body string, error).
func splitFrontmatter(data []byte) ([]byte, string, error) {
	s := string(data)

	// Frontmatter must start with "---" possibly preceded by whitespace/BOM.
	trimmed := strings.TrimLeft(s, "\xef\xbb\xbf \t\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return nil, "", fmt.Errorf("missing YAML frontmatter (no opening ---)")
	}

	// Find the closing "---" delimiter.
	afterOpen := strings.Index(trimmed, "---") + 3
	rest := trimmed[afterOpen:]

	closingIdx := strings.Index(rest, "\n---")
	if closingIdx < 0 {
		return nil, "", fmt.Errorf("missing YAML frontmatter closing ---")
	}

	fm := rest[:closingIdx]
	body := strings.TrimLeft(rest[closingIdx+4:], "\r\n")

	return []byte(fm), body, nil
}

// extractSummary returns the text between the end of frontmatter and the first
// ## heading. Leading/trailing whitespace is trimmed.
func extractSummary(body string) string {
	idx := strings.Index(body, "\n## ")
	if idx < 0 {
		// Also check for body starting with ##
		if strings.HasPrefix(body, "## ") {
			return ""
		}
		// No heading at all -- entire body is the summary.
		return strings.TrimSpace(body)
	}
	return strings.TrimSpace(body[:idx])
}

// extractSection returns the content under a given ## heading, up to the next
// ## heading or end of body. The heading line itself is excluded.
func extractSection(body, heading string) string {
	marker := "## " + heading
	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}

	// Skip past the heading line.
	start := idx + len(marker)
	nlIdx := strings.Index(body[start:], "\n")
	if nlIdx < 0 {
		return ""
	}
	start += nlIdx + 1

	// Find the next ## heading or end of body.
	rest := body[start:]
	nextH2 := strings.Index(rest, "\n## ")
	if nextH2 >= 0 {
		rest = rest[:nextH2]
	}

	return strings.TrimSpace(rest)
}

// ExtractWikilinks returns all unique [[wikilink]] targets found in the body.
func ExtractWikilinks(body string) []string {
	matches := wikilinkRe.FindAllStringSubmatch(body, -1)
	seen := make(map[string]bool, len(matches))
	var links []string
	for _, m := range matches {
		target := m[1]
		if !seen[target] {
			seen[target] = true
			links = append(links, target)
		}
	}
	return links
}

// ParseNodeMeta extracts only the lightweight identification fields from YAML
// frontmatter without parsing the full body. Suitable for directory listings.
func ParseNodeMeta(data []byte, filePath string) (*NodeMeta, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("parse meta %s: empty file", filePath)
	}

	frontmatter, _, err := splitFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("parse meta %s: %w", filePath, err)
	}

	var meta NodeMeta
	if err := yaml.Unmarshal(frontmatter, &meta); err != nil {
		return nil, fmt.Errorf("parse meta %s: invalid YAML: %w", filePath, err)
	}
	return &meta, nil
}
