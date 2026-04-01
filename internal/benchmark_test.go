//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
)

// ---------------------------------------------------------------------------
// SWE-QA Benchmark: ContextMarmot vs Vanilla File Reading
// ---------------------------------------------------------------------------

// sweQA represents a single SWE-QA benchmark entry.
type sweQA struct {
	Repo            string   `json:"repo"`
	Question        string   `json:"question"`
	Answer          string   `json:"answer"`
	ReferencedFiles []string `json:"referenced_files"`
}

// benchResult holds metrics for a single benchmark run.
type benchResult struct {
	Repo            string
	Question        string
	VanillaCalls    int
	VanillaTokens   int
	MCPCalls        int
	MCPTokens       int
	ReferencedFiles int
	MCPHits         int // how many referenced files appear in MCP result
	VanillaHits     int // always == ReferencedFiles (reads everything)
}

func loadSWEQA(t *testing.T) []sweQA {
	t.Helper()
	// Look for testdata relative to the repo root.
	path := filepath.Join("..", "testdata", "swe_qa_subset.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("SWE-QA dataset not found at %s: %v", path, err)
	}
	var questions []sweQA
	if err := json.Unmarshal(data, &questions); err != nil {
		t.Fatalf("parse SWE-QA dataset: %v", err)
	}
	return questions
}


// fileToNodeID converts a file path like "src/flask/json/provider.py" to a node ID.
func fileToNodeID(repo, file string) string {
	// Strip extension and normalize.
	id := strings.TrimSuffix(file, ".py")
	id = strings.TrimSuffix(id, ".js")
	id = strings.TrimSuffix(id, ".ts")
	id = strings.ReplaceAll(id, "\\", "/")
	return repo + "/" + id
}

// extractFileSummary extracts sentences from the answer that mention the file.
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
	// Fallback: use first 200 chars of answer.
	if len(answer) > 200 {
		return answer[:200]
	}
	return answer
}

// tokensApprox estimates tokens from a string (chars/4 heuristic).
func tokensApprox(s string) int {
	return (len(s) + 3) / 4
}

// newBenchEngine creates an engine using real OpenAI embeddings if
// OPENAI_API_KEY is set, otherwise falls back to mock.
func newBenchEngine(t *testing.T, dir string) (*mcpserver.Engine, string) {
	t.Helper()
	apiKey := os.Getenv("OPENAI_API_KEY")
	var emb embedding.Embedder
	var provider string
	if apiKey != "" {
		var err error
		emb, err = embedding.NewEmbedder("openai", "text-embedding-3-small", apiKey)
		if err != nil {
			t.Fatalf("create OpenAI embedder: %v", err)
		}
		provider = "openai/text-embedding-3-small"
	} else {
		emb = embedding.NewMockEmbedder("test-model")
		provider = "mock"
	}
	eng, err := mcpserver.NewEngine(dir, emb)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng, provider
}

func TestSWEQABenchmark(t *testing.T) {
	questions := loadSWEQA(t)

	// Group by repo and take a subset for speed.
	repos := map[string][]sweQA{}
	for _, q := range questions {
		repos[q.Repo] = append(repos[q.Repo], q)
	}

	var allResults []benchResult
	var embeddingProvider string

	for repo, repoQs := range repos {
		t.Run(repo, func(t *testing.T) {
			// Create a fresh engine per repo.
			dir := t.TempDir()
			eng, provider := newBenchEngine(t, dir)
			embeddingProvider = provider
			defer eng.Close()

			// Collect unique nodes to seed across all questions for this repo.
			type nodeInfo struct {
				id      string
				file    string
				summary string
			}
			seeded := map[string]bool{}
			var toSeed []nodeInfo

			for _, q := range repoQs {
				for _, file := range q.ReferencedFiles {
					nodeID := fileToNodeID(q.Repo, file)
					if seeded[nodeID] {
						continue
					}
					seeded[nodeID] = true
					toSeed = append(toSeed, nodeInfo{
						id:      nodeID,
						file:    file,
						summary: extractFileSummary(q.Answer, file),
					})
				}
			}

			// Seed repo root node.
			t.Logf("[%s] Seeding %d unique nodes...", repo, len(toSeed)+1)
			ctx := context.Background()
			repoReq := makeReq("context_write", map[string]any{
				"id":      repo,
				"type":    "module",
				"summary": fmt.Sprintf("Repository: %s", repo),
			})
			if _, err := eng.HandleContextWrite(ctx, repoReq); err != nil {
				t.Fatalf("seed repo node: %v", err)
			}

			// Seed file nodes.
			for i, ni := range toSeed {
				if (i+1)%10 == 0 || i == len(toSeed)-1 {
					t.Logf("[%s] Seeding node %d/%d: %s", repo, i+1, len(toSeed), ni.id)
				}
				req := makeReq("context_write", map[string]any{
					"id":      ni.id,
					"type":    "function",
					"summary": ni.summary,
					"context": fmt.Sprintf("Source file: %s\nPart of %s", ni.file, repo),
					"edges":   []map[string]string{{"target": repo, "relation": "contains"}},
				})
				if _, err := eng.HandleContextWrite(ctx, req); err != nil {
					continue
				}
			}

			totalNodes := eng.Graph.NodeCount()
			t.Logf("[%s] Seeded %d nodes. Running %d questions...", repo, totalNodes, len(repoQs))

			// Benchmark each question.
			for i, q := range repoQs {
				if len(q.ReferencedFiles) == 0 {
					continue
				}
				if (i+1)%10 == 0 {
					t.Logf("[%s] Query %d/%d...", repo, i+1, len(repoQs))
				}

				result := benchResult{
					Repo:            repo,
					Question:        q.Question,
					ReferencedFiles: len(q.ReferencedFiles),
				}

				// --- Vanilla approach: read ALL nodes ---
				// Simulate reading every .md file in the vault.
				allNodes := eng.Graph.AllNodes()
				vanillaTotal := 0
				for _, n := range allNodes {
					vanillaTotal += tokensApprox(n.Summary + n.Context)
				}
				result.VanillaCalls = len(allNodes) // 1 file read per node
				result.VanillaTokens = vanillaTotal
				result.VanillaHits = len(q.ReferencedFiles) // vanilla always has all files

				// --- MCP approach: 1 context_query ---
				queryResult := queryNodes(t, eng, map[string]any{
					"query":  q.Question,
					"depth":  2,
					"budget": 4096,
				})
				result.MCPCalls = 1
				result.MCPTokens = tokensApprox(queryResult)

				// Count how many referenced files appear in the query result.
				hits := 0
				for _, file := range q.ReferencedFiles {
					nodeID := fileToNodeID(q.Repo, file)
					if strings.Contains(queryResult, nodeID) {
						hits++
					}
				}
				result.MCPHits = hits

				allResults = append(allResults, result)

				if i < 3 {
					// Log first few for visibility.
					t.Logf("  Q%d: vanilla=%d calls/%d tok | mcp=%d call/%d tok | hits=%d/%d",
						i, result.VanillaCalls, result.VanillaTokens,
						result.MCPCalls, result.MCPTokens,
						result.MCPHits, result.ReferencedFiles)
				}
			}
		})
	}

	// --- Aggregate results ---
	if len(allResults) == 0 {
		t.Skip("No results to aggregate")
	}

	var (
		totalVanillaCalls  int
		totalVanillaTokens int
		totalMCPCalls      int
		totalMCPTokens     int
		totalRefFiles      int
		totalMCPHits       int
	)
	for _, r := range allResults {
		totalVanillaCalls += r.VanillaCalls
		totalVanillaTokens += r.VanillaTokens
		totalMCPCalls += r.MCPCalls
		totalMCPTokens += r.MCPTokens
		totalRefFiles += r.ReferencedFiles
		totalMCPHits += r.MCPHits
	}

	n := len(allResults)
	avgVanillaCalls := float64(totalVanillaCalls) / float64(n)
	avgVanillaTokens := float64(totalVanillaTokens) / float64(n)
	avgMCPCalls := float64(totalMCPCalls) / float64(n)
	avgMCPTokens := float64(totalMCPTokens) / float64(n)
	hitRate := float64(totalMCPHits) / float64(totalRefFiles) * 100
	callReduction := avgVanillaCalls / avgMCPCalls
	tokenReduction := avgVanillaTokens / avgMCPTokens

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║          SWE-QA Benchmark: ContextMarmot vs Vanilla         ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Embedding provider:      %-34s ║\n", embeddingProvider)
	fmt.Printf("║  Questions evaluated:     %-34d ║\n", n)
	fmt.Printf("║  Repos:                   %-34d ║\n", len(repos))
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Println("║                          Vanilla         MCP               ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Avg tool calls:          %-16.1f%-18.1f║\n", avgVanillaCalls, avgMCPCalls)
	fmt.Printf("║  Avg tokens consumed:     %-16.0f%-18.0f║\n", avgVanillaTokens, avgMCPTokens)
	fmt.Printf("║  Retrieval hit rate:      %-16s%-18s║\n", "100%", fmt.Sprintf("%.1f%%", hitRate))
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Call reduction:          %.1fx fewer calls with MCP%-9s║\n", callReduction, "")
	fmt.Printf("║  Token reduction:         %.1f%% fewer tokens with MCP%-7s║\n", (1-1/tokenReduction)*100, "")
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Println("║  MVP Targets:                                              ║")
	fmt.Printf("║    Tool calls (3x fewer): %-5s (%.1fx)%-23s║\n",
		passOrFail(callReduction >= 3), callReduction, "")
	fmt.Printf("║    Tokens (50%% reduction): %-5s (%.0f%%)%-22s║\n",
		passOrFail((1-1/tokenReduction)*100 >= 50), (1-1/tokenReduction)*100, "")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")

	// Per-repo breakdown.
	fmt.Println()
	fmt.Println("Per-repo breakdown:")
	fmt.Println("  Repo           Questions  Call Reduction  Token Reduction  Hit Rate")
	fmt.Println("  ─────────────  ─────────  ──────────────  ───────────────  ────────")
	for repo, repoQs := range repos {
		var vc, vt, mc, mt, rf, mh int
		counted := 0
		for _, r := range allResults {
			if r.Repo == repo {
				vc += r.VanillaCalls
				vt += r.VanillaTokens
				mc += r.MCPCalls
				mt += r.MCPTokens
				rf += r.ReferencedFiles
				mh += r.MCPHits
				counted++
			}
		}
		_ = repoQs
		if counted == 0 || mc == 0 || mt == 0 {
			continue
		}
		cr := float64(vc) / float64(mc)
		tr := (1 - float64(mt)/float64(vt)) * 100
		hr := float64(mh) / float64(rf) * 100
		fmt.Printf("  %-15s%-11d%.1fx            %.0f%%             %.1f%%\n", repo, counted, cr, tr, hr)
	}
}

func passOrFail(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
