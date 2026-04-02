// Package summary generates and manages namespace-level summary nodes (_summary.md).
// Summaries are synthesized from active nodes using an LLM provider.
// When no LLM is available, existing summaries remain (go stale, not broken).
package summary

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/node"
)

// SummaryResult holds a generated summary.
type SummaryResult struct {
	Namespace   string
	Content     string // Markdown text with [[wikilinks]]
	NodeCount   int    // Number of nodes included
	GeneratedAt time.Time
}

// Engine orchestrates summary generation.
type Engine struct {
	summarizer llm.Summarizer // nil = no generation possible
	mu         sync.Mutex     // protects generation
}

// NewEngine creates a new Engine. summarizer can be nil; in that case
// GenerateSummary will always return an error.
func NewEngine(summarizer llm.Summarizer) *Engine {
	return &Engine{summarizer: summarizer}
}

// GenerateSummary builds a SummarizeRequest from the given nodes (skipping
// superseded ones) and calls the configured summarizer.
func (e *Engine) GenerateSummary(ctx context.Context, namespace string, nodes []*node.Node) (*SummaryResult, error) {
	if e.summarizer == nil {
		return nil, errors.New("no summarizer configured")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	var inputs []llm.NodeSummaryInput
	for _, n := range nodes {
		if !n.IsActive() {
			continue
		}
		var edges []string
		for _, edge := range n.Edges {
			edges = append(edges, edge.Target)
		}
		inputs = append(inputs, llm.NodeSummaryInput{
			ID:      n.ID,
			Type:    n.Type,
			Summary: n.Summary,
			Edges:   edges,
		})
	}

	if len(inputs) == 0 {
		return &SummaryResult{
			Namespace:   namespace,
			Content:     "No active nodes in namespace.",
			NodeCount:   0,
			GeneratedAt: time.Now().UTC(),
		}, nil
	}

	req := llm.SummarizeRequest{
		Namespace: namespace,
		Nodes:     inputs,
	}

	content, err := e.summarizer.Summarize(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("generate summary: %w", err)
	}

	return &SummaryResult{
		Namespace:   namespace,
		Content:     content,
		NodeCount:   len(inputs),
		GeneratedAt: time.Now().UTC(),
	}, nil
}

// summaryPath returns the on-disk path for a namespace's _summary.md file.
func summaryPath(dir, namespace string) string {
	if namespace == "default" || namespace == "" {
		return filepath.Join(dir, "_summary.md")
	}
	return filepath.Join(dir, namespace, "_summary.md")
}

// WriteSummary writes _summary.md to the namespace directory using an atomic
// temp-file-then-rename pattern.
func WriteSummary(dir string, namespace string, result *SummaryResult) error {
	target := summaryPath(dir, namespace)

	// Ensure parent directory exists.
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("write summary: mkdir: %w", err)
	}

	// Build file content: YAML frontmatter + body.
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "id: _summary\n")
	fmt.Fprintf(&sb, "type: summary\n")
	fmt.Fprintf(&sb, "namespace: %s\n", result.Namespace)
	fmt.Fprintf(&sb, "generated_at: %s\n", result.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&sb, "node_count: %d\n", result.NodeCount)
	sb.WriteString("---\n\n")
	sb.WriteString(result.Content)
	sb.WriteByte('\n')

	// Atomic write: temp file in same dir, then rename.
	tmp, err := os.CreateTemp(parent, ".summary-*.md.tmp")
	if err != nil {
		return fmt.Errorf("write summary: create temp: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.WriteString(sb.String()); err != nil {
		return fmt.Errorf("write summary: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write summary: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("write summary: rename: %w", err)
	}

	success = true
	return nil
}

// ReadSummary reads and parses an existing _summary.md from the namespace directory.
func ReadSummary(dir string, namespace string) (*SummaryResult, error) {
	target := summaryPath(dir, namespace)

	data, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("read summary: %w", err)
	}

	content := string(data)

	// Parse YAML frontmatter between --- delimiters.
	result := &SummaryResult{Namespace: namespace}

	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("read summary: invalid frontmatter format")
	}

	frontmatter := parts[1]
	body := strings.TrimSpace(parts[2])
	result.Content = body

	// Parse frontmatter fields line by line (simple key: value parsing).
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		switch key {
		case "namespace":
			result.Namespace = val
		case "node_count":
			fmt.Sscanf(val, "%d", &result.NodeCount)
		case "generated_at":
			if t, err := time.Parse(time.RFC3339, val); err == nil {
				result.GeneratedAt = t
			}
		}
	}

	return result, nil
}
