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

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/embedding"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeReq builds a CallToolRequest with the given tool name and args map.
func makeReq(name string, args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
}

// text extracts the first TextContent string from a CallToolResult.
func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("empty content in result")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

// newEngine creates an Engine in the given directory with a mock embedder.
func newEngine(t *testing.T, dir string) *mcpserver.Engine {
	t.Helper()
	embedder := embedding.NewMockEmbedder("test-model")
	eng, err := mcpserver.NewEngine(dir, embedder)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

// writeNode is a helper to issue a context_write via the engine.
func writeNode(t *testing.T, eng *mcpserver.Engine, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := eng.HandleContextWrite(context.Background(), makeReq("context_write", args))
	if err != nil {
		t.Fatalf("HandleContextWrite error: %v", err)
	}
	return res
}

// queryNodes is a helper to issue a context_query via the engine.
func queryNodes(t *testing.T, eng *mcpserver.Engine, args map[string]any) string {
	t.Helper()
	res, err := eng.HandleContextQuery(context.Background(), makeReq("context_query", args))
	if err != nil {
		t.Fatalf("HandleContextQuery error: %v", err)
	}
	if res.IsError {
		t.Fatalf("query returned error: %s", text(t, res))
	}
	return text(t, res)
}

// verifyGraph is a helper to issue a context_verify via the engine.
func verifyGraph(t *testing.T, eng *mcpserver.Engine, args map[string]any) mcpserver.VerifyResult {
	t.Helper()
	res, err := eng.HandleContextVerify(context.Background(), makeReq("context_verify", args))
	if err != nil {
		t.Fatalf("HandleContextVerify error: %v", err)
	}
	if res.IsError {
		t.Fatalf("verify returned error: %s", text(t, res))
	}
	var vr mcpserver.VerifyResult
	if err := json.Unmarshal([]byte(text(t, res)), &vr); err != nil {
		t.Fatalf("unmarshal verify result: %v", err)
	}
	return vr
}

// ---------------------------------------------------------------------------
// 1. Full write-query-verify lifecycle
// ---------------------------------------------------------------------------

func TestFullWriteQueryVerifyLifecycle(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)
	defer eng.Close()

	// Write 5 interconnected nodes: module -> 2 functions, each calls a helper.
	//
	//   app/math  (module)
	//     |-- contains --> app/math/add
	//     |-- contains --> app/math/mul
	//   app/math/add (function)  -- calls --> app/math/helper
	//   app/math/mul (function)  -- calls --> app/math/helper
	//   app/math/helper (function)
	//   app/math/utils  (module) -- references --> app/math

	nodes := []map[string]any{
		{
			"id":      "app/math",
			"type":    "module",
			"summary": "Core math module providing arithmetic operations",
			"context": "package math\n\nimport \"app/math/helper\"\n",
			"edges": []map[string]any{
				{"target": "app/math/add", "relation": "contains"},
				{"target": "app/math/mul", "relation": "contains"},
			},
		},
		{
			"id":      "app/math/add",
			"type":    "function",
			"summary": "Addition function that adds two integers",
			"context": "func Add(a, b int) int { return helper.Sanitize(a) + helper.Sanitize(b) }",
			"edges": []map[string]any{
				{"target": "app/math/helper", "relation": "calls"},
			},
		},
		{
			"id":      "app/math/mul",
			"type":    "function",
			"summary": "Multiplication function that multiplies two integers",
			"context": "func Mul(a, b int) int { return helper.Sanitize(a) * helper.Sanitize(b) }",
			"edges": []map[string]any{
				{"target": "app/math/helper", "relation": "calls"},
			},
		},
		{
			"id":      "app/math/helper",
			"type":    "function",
			"summary": "Helper utility for sanitizing numeric input values",
			"context": "func Sanitize(v int) int { if v < 0 { return 0 } return v }",
		},
		{
			"id":      "app/math/utils",
			"type":    "module",
			"summary": "Utility module that references the core math module",
			"context": "package utils\n\nimport \"app/math\"\n",
			"edges": []map[string]any{
				{"target": "app/math", "relation": "references"},
			},
		},
	}

	// Write all nodes.
	for _, n := range nodes {
		res := writeNode(t, eng, n)
		if res.IsError {
			t.Fatalf("write %s failed: %s", n["id"], text(t, res))
		}
	}

	// Verify graph has 5 nodes.
	if eng.Graph.NodeCount() != 5 {
		t.Fatalf("expected 5 nodes, got %d", eng.Graph.NodeCount())
	}

	// Query for each function by description and verify it appears in results.
	queries := []struct {
		query    string
		expectID string
	}{
		{"addition function adds integers", "app/math/add"},
		{"multiplication function multiplies integers", "app/math/mul"},
		{"helper sanitize numeric input", "app/math/helper"},
		{"core math module arithmetic", "app/math"},
		{"utility module references math", "app/math/utils"},
	}

	for _, q := range queries {
		xml := queryNodes(t, eng, map[string]any{
			"query":  q.query,
			"depth":  3,
			"budget": 50000,
		})
		if !strings.Contains(xml, q.expectID) {
			t.Errorf("query %q: expected result to contain %q, got:\n%s", q.query, q.expectID, xml)
		}
	}

	// Run verify -- expect no integrity issues among the 5 well-formed nodes.
	vr := verifyGraph(t, eng, map[string]any{"check": "integrity"})
	if vr.Total != 0 {
		t.Errorf("expected 0 integrity issues, got %d: %+v", vr.Total, vr.Issues)
	}
}

// ---------------------------------------------------------------------------
// 2. Structural cycle rejection
// ---------------------------------------------------------------------------

func TestStructuralCycleRejection(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)
	defer eng.Close()

	// Write A with structural edge to B.
	resA := writeNode(t, eng, map[string]any{
		"id":      "pkg/a",
		"type":    "module",
		"summary": "Package A",
		"edges": []map[string]any{
			{"target": "pkg/b", "relation": "contains"},
		},
	})
	if resA.IsError {
		t.Fatalf("write A failed: %s", text(t, resA))
	}

	// Write B with structural edge back to A -- should be rejected.
	resB := writeNode(t, eng, map[string]any{
		"id":      "pkg/b",
		"type":    "module",
		"summary": "Package B",
		"edges": []map[string]any{
			{"target": "pkg/a", "relation": "contains"},
		},
	})

	if !resB.IsError {
		t.Fatal("expected structural cycle B->A to be rejected, but write succeeded")
	}

	errMsg := text(t, resB)
	if !strings.Contains(strings.ToLower(errMsg), "cycle") {
		t.Errorf("expected error mentioning 'cycle', got: %s", errMsg)
	}

	// Verify B was NOT added to the graph (only A should be present).
	if eng.Graph.NodeCount() != 1 {
		t.Errorf("expected 1 node in graph after rejection, got %d", eng.Graph.NodeCount())
	}
}

// ---------------------------------------------------------------------------
// 3. Behavioral cycle acceptance
// ---------------------------------------------------------------------------

func TestBehavioralCycleAcceptance(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)
	defer eng.Close()

	// Write A with behavioral edge (calls) to B.
	resA := writeNode(t, eng, map[string]any{
		"id":      "fn/a",
		"type":    "function",
		"summary": "Function A calls Function B for mutual recursion",
		"context": "func A() { B() }",
		"edges": []map[string]any{
			{"target": "fn/b", "relation": "calls"},
		},
	})
	if resA.IsError {
		t.Fatalf("write A failed: %s", text(t, resA))
	}

	// Write B with behavioral edge (calls) back to A -- should succeed.
	resB := writeNode(t, eng, map[string]any{
		"id":      "fn/b",
		"type":    "function",
		"summary": "Function B calls Function A for mutual recursion",
		"context": "func B() { A() }",
		"edges": []map[string]any{
			{"target": "fn/a", "relation": "calls"},
		},
	})
	if resB.IsError {
		t.Fatalf("behavioral cycle should be allowed: %s", text(t, resB))
	}

	// Both nodes should exist.
	if eng.Graph.NodeCount() != 2 {
		t.Fatalf("expected 2 nodes, got %d", eng.Graph.NodeCount())
	}

	// Query for A and verify both A and B appear (they are connected).
	xml := queryNodes(t, eng, map[string]any{
		"query":  "mutual recursion function A calls B",
		"depth":  2,
		"budget": 50000,
	})
	if !strings.Contains(xml, "fn/a") {
		t.Errorf("expected fn/a in query result, got:\n%s", xml)
	}
	if !strings.Contains(xml, "fn/b") {
		t.Errorf("expected fn/b in query result, got:\n%s", xml)
	}
}

// ---------------------------------------------------------------------------
// 4. Compaction respects token budget
// ---------------------------------------------------------------------------

func TestCompactionRespectsTokenBudget(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)
	defer eng.Close()

	// Write 10 nodes with large context content.
	// Use a hub node so they are all connected and will appear in traversal.
	writeNode(t, eng, map[string]any{
		"id":      "hub/root",
		"type":    "module",
		"summary": "Hub root node connecting all large nodes",
		"context": strings.Repeat("// root context line\n", 50),
	})

	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("hub/node%d", i)
		res := writeNode(t, eng, map[string]any{
			"id":      id,
			"type":    "function",
			"summary": fmt.Sprintf("Large function number %d with substantial content", i),
			"context": strings.Repeat(fmt.Sprintf("// function %d body line\n", i), 100),
			"edges": []map[string]any{
				{"target": "hub/root", "relation": "calls"},
			},
		})
		if res.IsError {
			t.Fatalf("write %s failed: %s", id, text(t, res))
		}
	}

	// Also give hub/root edges back so traversal from any node reaches others.
	writeNode(t, eng, map[string]any{
		"id":      "hub/root",
		"type":    "module",
		"summary": "Hub root node connecting all large nodes",
		"context": strings.Repeat("// root context line\n", 50),
		"edges": func() []map[string]any {
			edges := make([]map[string]any, 10)
			for i := 0; i < 10; i++ {
				edges[i] = map[string]any{
					"target":   fmt.Sprintf("hub/node%d", i),
					"relation": "references",
				}
			}
			return edges
		}(),
	})

	// Query with small budget (500 tokens) -- should produce truncated nodes.
	smallXML := queryNodes(t, eng, map[string]any{
		"query":  "large function substantial content",
		"depth":  2,
		"budget": 500,
	})
	// With a tiny budget, we expect truncation to occur.
	hasTruncated := strings.Contains(smallXML, "truncated") || strings.Contains(smallXML, "node_ref")
	// Count the number of full <node id= entries.
	smallNodeCount := strings.Count(smallXML, "<node ") + strings.Count(smallXML, "<node_compact ")

	// Query with large budget (50000 tokens) -- all nodes should be included.
	largeXML := queryNodes(t, eng, map[string]any{
		"query":  "large function substantial content",
		"depth":  2,
		"budget": 50000,
	})
	largeNodeCount := strings.Count(largeXML, "<node ") + strings.Count(largeXML, "<node_compact ")
	largeTruncated := strings.Contains(largeXML, "node_ref")

	// With large budget, we expect more nodes and no truncation.
	if largeTruncated {
		t.Errorf("expected no truncation with large budget, but found node_ref in:\n%s", largeXML)
	}

	// The small budget result should have fewer included nodes OR have truncation.
	if !hasTruncated && smallNodeCount >= largeNodeCount && largeNodeCount > 0 {
		t.Errorf("expected small budget (%d nodes) to be less than large budget (%d nodes) or to have truncation",
			smallNodeCount, largeNodeCount)
	}

	t.Logf("small budget: %d nodes, truncated=%v", smallNodeCount, hasTruncated)
	t.Logf("large budget: %d nodes, truncated=%v", largeNodeCount, largeTruncated)
}

// ---------------------------------------------------------------------------
// 5. Node update / overwrite
// ---------------------------------------------------------------------------

func TestNodeUpdateOverwrite(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)
	defer eng.Close()

	// Write initial node.
	res1 := writeNode(t, eng, map[string]any{
		"id":      "item/x",
		"type":    "function",
		"summary": "Original summary for item X",
		"context": "func X() { return 1 }",
	})
	if res1.IsError {
		t.Fatalf("first write failed: %s", text(t, res1))
	}

	// Overwrite with different content.
	res2 := writeNode(t, eng, map[string]any{
		"id":      "item/x",
		"type":    "function",
		"summary": "Updated summary for item X with new behavior",
		"context": "func X() { return 42 }",
	})
	if res2.IsError {
		t.Fatalf("second write failed: %s", text(t, res2))
	}

	// Should still be exactly 1 node.
	if eng.Graph.NodeCount() != 1 {
		t.Errorf("expected 1 node after update, got %d", eng.Graph.NodeCount())
	}

	// Verify content was updated in-memory.
	n, ok := eng.Graph.GetNode("item/x")
	if !ok {
		t.Fatal("node not found after update")
	}
	if n.Summary != "Updated summary for item X with new behavior" {
		t.Errorf("expected updated summary, got %q", n.Summary)
	}
	if n.Context != "func X() { return 42 }" {
		t.Errorf("expected updated context, got %q", n.Context)
	}

	// Query for the updated content.
	xml := queryNodes(t, eng, map[string]any{
		"query":  "Updated summary item X new behavior",
		"depth":  1,
		"budget": 4096,
	})
	if !strings.Contains(xml, "item/x") {
		t.Errorf("expected query to find updated node, got:\n%s", xml)
	}
}

// ---------------------------------------------------------------------------
// 6. Verify detects dangling edges
// ---------------------------------------------------------------------------

func TestVerifyDetectsDanglingEdges(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)
	defer eng.Close()

	// Write a node with edge to non-existent target.
	res := writeNode(t, eng, map[string]any{
		"id":      "real/node",
		"type":    "function",
		"summary": "A real node pointing to a ghost",
		"edges": []map[string]any{
			{"target": "ghost/node", "relation": "calls"},
		},
	})
	if res.IsError {
		t.Fatalf("write failed: %s", text(t, res))
	}

	// Run verify with check="integrity".
	vr := verifyGraph(t, eng, map[string]any{"check": "integrity"})

	if vr.Total == 0 {
		t.Fatal("expected at least one issue (dangling edge), got 0")
	}

	found := false
	for _, issue := range vr.Issues {
		if issue.Type == "dangling_edge" && issue.NodeID == "real/node" {
			found = true
			if !strings.Contains(issue.Message, "ghost/node") {
				t.Errorf("expected message to mention ghost/node, got: %s", issue.Message)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected dangling_edge issue for real/node, got: %+v", vr.Issues)
	}
}

// ---------------------------------------------------------------------------
// 7. Persistence across engine restarts
// ---------------------------------------------------------------------------

func TestPersistenceAcrossEngineRestarts(t *testing.T) {
	dir := t.TempDir()

	// Phase 1: create engine, write nodes, close.
	{
		eng := newEngine(t, dir)

		writeNode(t, eng, map[string]any{
			"id":      "persist/alpha",
			"type":    "function",
			"summary": "Alpha function for persistence test",
			"context": "func Alpha() {}",
		})
		writeNode(t, eng, map[string]any{
			"id":      "persist/beta",
			"type":    "function",
			"summary": "Beta function for persistence test",
			"context": "func Beta() {}",
			"edges": []map[string]any{
				{"target": "persist/alpha", "relation": "calls"},
			},
		})

		if eng.Graph.NodeCount() != 2 {
			t.Fatalf("phase 1: expected 2 nodes, got %d", eng.Graph.NodeCount())
		}

		if err := eng.Close(); err != nil {
			t.Fatalf("close engine: %v", err)
		}
	}

	// Phase 2: create a brand-new engine on the same directory.
	{
		eng := newEngine(t, dir)
		defer eng.Close()

		// Graph should have reloaded both nodes from disk.
		if eng.Graph.NodeCount() != 2 {
			t.Fatalf("phase 2: expected 2 nodes after restart, got %d", eng.Graph.NodeCount())
		}

		// Verify specific node existence.
		for _, id := range []string{"persist/alpha", "persist/beta"} {
			n, ok := eng.Graph.GetNode(id)
			if !ok {
				t.Errorf("node %q not found after restart", id)
				continue
			}
			if n.Summary == "" {
				t.Errorf("node %q has empty summary after restart", id)
			}
		}

		// Verify edges survived restart.
		beta, _ := eng.Graph.GetNode("persist/beta")
		hasEdge := false
		for _, e := range beta.Edges {
			if e.Target == "persist/alpha" {
				hasEdge = true
				break
			}
		}
		if !hasEdge {
			t.Error("expected persist/beta to have edge to persist/alpha after restart")
		}

		// Query for one of the persisted nodes (embedding store is separate;
		// re-embed so the new engine's embedding store has entries).
		// Note: embeddings are stored in the SQLite DB which persists across
		// engine restarts, so the query should find them.
		xml := queryNodes(t, eng, map[string]any{
			"query":  "Alpha function persistence",
			"depth":  2,
			"budget": 4096,
		})
		if !strings.Contains(xml, "persist/alpha") {
			t.Errorf("expected query to find persist/alpha after restart, got:\n%s", xml)
		}
	}
}

// ---------------------------------------------------------------------------
// 8. CLI init + query flow (via direct function calls)
// ---------------------------------------------------------------------------

func TestCLIInitAndQueryFlow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".marmot")

	// runInit creates the vault directory structure.
	// We replicate what main.runInit does since it is in package main.
	err := initVault(dir)
	if err != nil {
		t.Fatalf("initVault: %v", err)
	}

	// Verify directory structure created.
	for _, sub := range []string{"", ".marmot-data", ".obsidian"} {
		p := filepath.Join(dir, sub)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", p)
		}
	}

	// Verify _config.md was written.
	configPath := filepath.Join(dir, "_config.md")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected _config.md to exist: %v", err)
	}

	// Create engine on the init'd dir and write a node.
	eng := newEngine(t, dir)
	defer eng.Close()

	res := writeNode(t, eng, map[string]any{
		"id":      "cli/test",
		"type":    "concept",
		"summary": "A node created after CLI init for query testing",
		"context": "This is the context for the CLI test node.",
	})
	if res.IsError {
		t.Fatalf("write failed: %s", text(t, res))
	}

	// Simulate runQueryPipeline: embed, search, traverse, compact.
	xml := queryNodes(t, eng, map[string]any{
		"query":  "CLI init query testing node",
		"depth":  2,
		"budget": 4096,
	})
	if !strings.Contains(xml, "cli/test") {
		t.Errorf("expected query result to contain cli/test, got:\n%s", xml)
	}
	if strings.Contains(xml, `nodes="0"`) {
		t.Errorf("expected non-zero nodes in result, got:\n%s", xml)
	}
}

// initVault replicates the vault initialization logic from cmd/marmot/main.go
// (runInit) so we can test it without importing package main.
func initVault(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("directory %q already exists", dir)
	}

	dirs := []string{
		dir,
		filepath.Join(dir, ".marmot-data"),
		filepath.Join(dir, ".obsidian"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	configContent := `---
version: "1"
namespace: default
embedding_model: mock
---
# ContextMarmot Vault

This is the root configuration for a ContextMarmot vault.
`
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(configContent), 0o644); err != nil {
		return fmt.Errorf("write _config.md: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, ".obsidian", "graph.json"), []byte("{}\n"), 0o644); err != nil {
		return fmt.Errorf("write graph.json: %w", err)
	}

	return nil
}
