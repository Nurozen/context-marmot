package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/embedding"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
)

// cloneRepo clones a repo at a specific ref into workDir/repos/{name}.
func cloneRepo(repo RepoConfig, workDir string) (string, error) {
	dest := filepath.Join(workDir, "repos", repo.Name)

	// Skip if already cloned.
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		fmt.Printf("  [clone] %s already exists, skipping\n", repo.Name)
		return dest, nil
	}

	fmt.Printf("  [clone] %s @ %s...\n", repo.Name, repo.Ref)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}

	cmd := exec.Command("git", "clone", "--depth", "1", "--branch", repo.Ref, repo.URL, dest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone %s: %w", repo.Name, err)
	}
	return dest, nil
}

// seedVault creates a .marmot vault from evaluation questions for a single repo.
func seedVault(questions []EvalQuestion, repo string, workDir string) (string, error) {
	vaultDir := filepath.Join(workDir, "vaults", repo, ".marmot")
	if err := os.MkdirAll(filepath.Join(vaultDir, ".marmot-data"), 0o755); err != nil {
		return "", err
	}
	repoRoot := filepath.Join(workDir, "repos", repo)

	// Check for OpenAI key.
	apiKey := os.Getenv("OPENAI_API_KEY")
	var emb embedding.Embedder
	if apiKey != "" {
		var err error
		emb, err = embedding.NewEmbedder("openai", "text-embedding-3-small", apiKey)
		if err != nil {
			return "", fmt.Errorf("create embedder: %w", err)
		}
		fmt.Printf("  [seed] Using OpenAI embeddings for %s\n", repo)
	} else {
		emb = embedding.NewMockEmbedder("eval-model")
		fmt.Printf("  [seed] Using mock embeddings for %s (no OPENAI_API_KEY)\n", repo)
	}

	eng, err := mcpserver.NewEngine(vaultDir, emb)
	if err != nil {
		return "", fmt.Errorf("create engine: %w", err)
	}
	defer eng.Close()

	// Collect unique nodes across all questions for this repo.
	type nodeInfo struct {
		id, file, summary string
		snippet           string
		lineCount         int
	}
	seen := map[string]bool{}
	var nodes []nodeInfo

	for _, q := range questions {
		for _, file := range q.ReferencedFiles {
			nodeID := fileToNodeID(repo, file)
			if seen[nodeID] {
				continue
			}
			seen[nodeID] = true
			ni := nodeInfo{
				id:      nodeID,
				file:    file,
				summary: extractFileSummary(q.Answer, file),
			}
			fullPath := filepath.Join(repoRoot, file)
			if content, err := os.ReadFile(fullPath); err == nil {
				ni.lineCount = strings.Count(string(content), "\n") + 1
				ni.snippet = string(content)
				if len(ni.snippet) > 8000 {
					ni.snippet = ni.snippet[:8000] + "\n// [truncated]"
				}
			} else {
				ni.snippet = fmt.Sprintf("Source file: %s\nPart of %s", file, repo)
				ni.lineCount = 1
			}
			nodes = append(nodes, ni)
		}
	}

	// Seed repo root node.
	ctx := context.Background()
	repoReq := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_write",
			Arguments: map[string]any{
				"id":      repo,
				"type":    "module",
				"summary": fmt.Sprintf("Repository: %s", repo),
			},
		},
	}
	if _, err := eng.HandleContextWrite(ctx, repoReq); err != nil {
		return "", fmt.Errorf("seed repo node: %w", err)
	}

	// Seed file nodes.
	for i, ni := range nodes {
		if (i+1)%10 == 0 || i == len(nodes)-1 {
			fmt.Printf("  [seed] %s: node %d/%d\n", repo, i+1, len(nodes))
		}
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: "context_write",
				Arguments: map[string]any{
					"id":      ni.id,
					"type":    "function",
					"summary": ni.summary,
					"context": ni.snippet,
					"source": map[string]any{
						"path":  ni.file,
						"lines": []int{1, ni.lineCount},
					},
					"edges": []map[string]string{{"target": repo, "relation": "contains"}},
				},
			},
		}
		if _, err := eng.HandleContextWrite(ctx, req); err != nil {
			continue
		}
	}

	fmt.Printf("  [seed] %s: %d nodes seeded\n", repo, eng.Graph.NodeCount())
	return vaultDir, nil
}

func fileToNodeID(repo, file string) string {
	id := strings.TrimSuffix(file, ".py")
	id = strings.TrimSuffix(id, ".js")
	id = strings.TrimSuffix(id, ".ts")
	id = strings.ReplaceAll(id, "\\", "/")
	return repo + "/" + id
}

func extractFileSummary(answer, file string) string {
	sentences := strings.Split(answer, ".")
	var relevant []string
	for _, s := range sentences {
		if strings.Contains(s, file) || strings.Contains(s, filepath.Base(file)) {
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				relevant = append(relevant, trimmed)
			}
		}
	}
	if len(relevant) > 0 {
		summary := strings.Join(relevant, ". ")
		if len(summary) > 300 {
			summary = summary[:300]
		}
		return summary
	}
	if len(answer) > 200 {
		return answer[:200]
	}
	return answer
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8])
}
