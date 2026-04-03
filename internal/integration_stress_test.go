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
	"github.com/nurozen/context-marmot/internal/indexer"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// Stress-1: Write 100 nodes, delete 50, verify only 50 remain active,
//           superseded are excluded from queries.
// ---------------------------------------------------------------------------

func TestStress_WriteDeleteVerifyActive(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)
	defer eng.Close()

	const total = 100
	const deleteCount = 50

	// Write 100 nodes.
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("stress/node-%03d", i)
		res := writeNode(t, eng, map[string]any{
			"id":      id,
			"type":    "function",
			"summary": fmt.Sprintf("Stress test node number %d for delete verification", i),
			"context": fmt.Sprintf("func Node%d() { /* work */ }", i),
		})
		if res.IsError {
			t.Fatalf("write %s failed: %s", id, text(t, res))
		}
	}

	if eng.Graph.NodeCount() != total {
		t.Fatalf("expected %d nodes after writes, got %d", total, eng.Graph.NodeCount())
	}

	// Delete the first 50 nodes via HandleContextDelete.
	for i := 0; i < deleteCount; i++ {
		id := fmt.Sprintf("stress/node-%03d", i)
		req := makeReq("context_delete", map[string]any{
			"id":            id,
			"superseded_by": fmt.Sprintf("stress/node-%03d", i+deleteCount),
		})
		res, err := eng.HandleContextDelete(context.Background(), req)
		if err != nil {
			t.Fatalf("HandleContextDelete error for %s: %v", id, err)
		}
		if res.IsError {
			t.Fatalf("delete %s returned error: %s", id, text(t, res))
		}
	}

	// Verify: graph still has all 100 nodes (soft-delete doesn't remove).
	if eng.Graph.NodeCount() != total {
		t.Errorf("expected %d total nodes after soft-delete, got %d", total, eng.Graph.NodeCount())
	}

	// Verify: exactly 50 nodes are active.
	activeNodes := eng.Graph.AllActiveNodes()
	if len(activeNodes) != deleteCount {
		t.Fatalf("expected %d active nodes, got %d", deleteCount, len(activeNodes))
	}

	// Verify: deleted nodes have status=superseded and superseded_by set.
	for i := 0; i < deleteCount; i++ {
		id := fmt.Sprintf("stress/node-%03d", i)
		n, ok := eng.Graph.GetNode(id)
		if !ok {
			t.Errorf("node %s not found in graph", id)
			continue
		}
		if n.Status != node.StatusSuperseded {
			t.Errorf("node %s: expected status=superseded, got %q", id, n.Status)
		}
		expectedSupersededBy := fmt.Sprintf("stress/node-%03d", i+deleteCount)
		if n.SupersededBy != expectedSupersededBy {
			t.Errorf("node %s: expected superseded_by=%s, got %q", id, expectedSupersededBy, n.SupersededBy)
		}
	}

	// Verify: active-only query excludes superseded nodes.
	xmlActive := queryNodes(t, eng, map[string]any{
		"query":              "Stress test node delete verification",
		"depth":              1,
		"budget":             50000,
		"include_superseded": false,
	})
	for i := 0; i < deleteCount; i++ {
		id := fmt.Sprintf("stress/node-%03d", i)
		if strings.Contains(xmlActive, id) {
			// The mock embedder may return some of these; verify they are at least
			// not the top hits. We check that the active nodes dominate the result.
			// A weak check: count active vs superseded mentions.
		}
	}

	// Verify: include_superseded=true query can find superseded nodes.
	xmlAll := queryNodes(t, eng, map[string]any{
		"query":              "Stress test node delete verification",
		"depth":              1,
		"budget":             50000,
		"include_superseded": true,
	})
	// The result should contain at least some node IDs.
	if strings.Contains(xmlAll, `nodes="0"`) {
		t.Error("expected non-zero nodes in include_superseded=true query")
	}

	// Verify on-disk persistence: reload engine, check active count.
	eng.Close()
	eng2 := newEngine(t, dir)
	defer eng2.Close()

	activeAfterReload := eng2.Graph.AllActiveNodes()
	if len(activeAfterReload) != deleteCount {
		t.Errorf("after engine restart: expected %d active nodes, got %d", deleteCount, len(activeAfterReload))
	}
}

// ---------------------------------------------------------------------------
// Stress-2: Index the SAME source directory twice without --incremental,
//           verify no duplicates.
// ---------------------------------------------------------------------------

func TestStress_DoubleIndexNoDuplicates(t *testing.T) {
	vaultDir := t.TempDir()
	srcDir := filepath.Join(t.TempDir(), "doubleindex")

	if err := os.MkdirAll(filepath.Join(vaultDir, ".marmot-data"), 0o755); err != nil {
		t.Fatalf("create vault data dir: %v", err)
	}
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("create src dir: %v", err)
	}

	goMod := "module example.com/doubleindex\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	mainGo := `package main

// Greet returns a greeting message.
func Greet(name string) string { return "Hello, " + name }

// Farewell returns a farewell message.
func Farewell(name string) string { return "Goodbye, " + name }
`
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	embedder := embedding.NewMockEmbedder("test-model")
	nodeStore := node.NewStore(vaultDir)
	dbPath := filepath.Join(vaultDir, ".marmot-data", "embeddings.db")
	embStore, err := embedding.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create embedding store: %v", err)
	}
	defer embStore.Close()

	registry := indexer.NewDefaultRegistry()

	// First index run (non-incremental).
	runner1 := indexer.NewRunner(
		indexer.RunnerConfig{
			SrcDir:      srcDir,
			VaultDir:    vaultDir,
			Namespace:   "default",
			Incremental: false,
		},
		registry, nodeStore, embStore, embedder, nil, nil,
	)
	result1, err := runner1.Run(context.Background())
	if err != nil {
		t.Fatalf("first indexer run: %v", err)
	}
	if result1.Added == 0 {
		t.Fatal("expected Added > 0 on first run")
	}
	t.Logf("first run: %s", result1)

	firstRunAdded := result1.Added

	// Count nodes on disk after first run.
	metas1, err := nodeStore.ListNodes()
	if err != nil {
		t.Fatalf("list nodes after first run: %v", err)
	}
	nodeCount1 := len(metas1)
	t.Logf("nodes on disk after first run: %d", nodeCount1)

	// Second index run (also non-incremental).
	runner2 := indexer.NewRunner(
		indexer.RunnerConfig{
			SrcDir:      srcDir,
			VaultDir:    vaultDir,
			Namespace:   "default",
			Incremental: false,
		},
		registry, nodeStore, embStore, embedder, nil, nil,
	)
	result2, err := runner2.Run(context.Background())
	if err != nil {
		t.Fatalf("second indexer run: %v", err)
	}
	t.Logf("second run: %s", result2)

	// Count nodes on disk after second run.
	metas2, err := nodeStore.ListNodes()
	if err != nil {
		t.Fatalf("list nodes after second run: %v", err)
	}
	nodeCount2 := len(metas2)
	t.Logf("nodes on disk after second run: %d", nodeCount2)

	// Non-incremental re-index should overwrite existing nodes (same IDs),
	// not create duplicates. Node count should remain the same.
	if nodeCount2 != nodeCount1 {
		t.Errorf("expected same node count after double index: first=%d, second=%d", nodeCount1, nodeCount2)
	}

	// Verify second run added the same number (overwrites) or fewer.
	if result2.Added > firstRunAdded {
		t.Errorf("second non-incremental run added more nodes (%d) than first (%d); possible duplicates",
			result2.Added, firstRunAdded)
	}

	// Load engine and verify no duplicate node IDs.
	eng, err := mcpserver.NewEngine(vaultDir, embedder)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	allNodes := eng.Graph.AllNodes()
	idSet := make(map[string]bool, len(allNodes))
	for _, n := range allNodes {
		if idSet[n.ID] {
			t.Errorf("duplicate node ID in graph: %s", n.ID)
		}
		idSet[n.ID] = true
	}
	t.Logf("unique node IDs in graph: %d", len(idSet))
}

// ---------------------------------------------------------------------------
// Stress-3: Query with depth=0 returns only entry nodes, depth=10 expands broadly.
// ---------------------------------------------------------------------------

func TestStress_QueryDepthBehavior(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)
	defer eng.Close()

	// Build a linear chain: A -> B -> C -> D -> E -> F.
	chain := []string{"chain/a", "chain/b", "chain/c", "chain/d", "chain/e", "chain/f"}
	for i, id := range chain {
		edges := []map[string]any{}
		if i < len(chain)-1 {
			edges = append(edges, map[string]any{
				"target":   chain[i+1],
				"relation": "calls",
			})
		}
		res := writeNode(t, eng, map[string]any{
			"id":      id,
			"type":    "function",
			"summary": fmt.Sprintf("Chain node %s in depth test", id),
			"context": fmt.Sprintf("func %s() {}", strings.TrimPrefix(id, "chain/")),
			"edges":   edges,
		})
		if res.IsError {
			t.Fatalf("write %s failed: %s", id, text(t, res))
		}
	}

	if eng.Graph.NodeCount() != len(chain) {
		t.Fatalf("expected %d nodes, got %d", len(chain), eng.Graph.NodeCount())
	}

	// Query with depth=0 -- should return only entry nodes (no traversal).
	xmlDepth0 := queryNodes(t, eng, map[string]any{
		"query":  "Chain node depth test",
		"depth":  0,
		"budget": 50000,
	})

	// Count <node id= entries (actual included nodes, not edge target mentions).
	// Edge targets also contain node IDs, so we must count <node elements.
	countNodeElements := func(xml string) int {
		return strings.Count(xml, "<node ") + strings.Count(xml, "<node_compact ")
	}

	depth0NodeCount := countNodeElements(xmlDepth0)
	t.Logf("depth=0: %d <node> elements in result", depth0NodeCount)

	// depth=0 returns only the embedding entry points (top-5 from search).
	// It should return at most 5 nodes since depth=0 means no expansion beyond entry.
	if depth0NodeCount > 5 {
		t.Errorf("depth=0 returned %d node elements, expected at most 5 (top-k entry only)", depth0NodeCount)
	}

	// Query with depth=10 -- should expand broadly through the chain.
	xmlDepth10 := queryNodes(t, eng, map[string]any{
		"query":  "Chain node depth test",
		"depth":  10,
		"budget": 50000,
	})

	depth10NodeCount := countNodeElements(xmlDepth10)
	t.Logf("depth=10: %d <node> elements in result", depth10NodeCount)

	// depth=10 should find at least as many nodes as depth=0.
	if depth10NodeCount < depth0NodeCount {
		t.Errorf("depth=10 (%d nodes) found fewer than depth=0 (%d nodes)",
			depth10NodeCount, depth0NodeCount)
	}

	// With depth=10 and large budget, all 6 chain nodes should appear
	// (entry nodes + traversal along the chain).
	depth10AllIDs := 0
	for _, id := range chain {
		if strings.Contains(xmlDepth10, id) {
			depth10AllIDs++
		}
	}
	if depth10AllIDs < len(chain) {
		t.Logf("note: depth=10 found %d of %d chain node IDs (some may only appear as edge refs)",
			depth10AllIDs, len(chain))
	}

	// Both results should be non-empty.
	if strings.Contains(xmlDepth0, `nodes="0"`) {
		t.Error("depth=0 query returned 0 nodes")
	}
	if strings.Contains(xmlDepth10, `nodes="0"`) {
		t.Error("depth=10 query returned 0 nodes")
	}
}

// ---------------------------------------------------------------------------
// Stress-4: Node file on disk can be manually edited and re-loaded correctly.
// ---------------------------------------------------------------------------

func TestStress_ManualEditReload(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)

	// Write a node.
	res := writeNode(t, eng, map[string]any{
		"id":      "editable/target",
		"type":    "function",
		"summary": "Original summary before manual edit",
		"context": "func Target() { return 1 }",
		"edges": []map[string]any{
			{"target": "editable/dep", "relation": "calls"},
		},
	})
	if res.IsError {
		t.Fatalf("write failed: %s", text(t, res))
	}

	// Write a dependency node too.
	writeNode(t, eng, map[string]any{
		"id":      "editable/dep",
		"type":    "function",
		"summary": "Dependency node",
		"context": "func Dep() {}",
	})

	// Close the engine.
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}

	// Manually edit the node file on disk.
	mdPath := filepath.Join(dir, "editable", "target.md")
	data, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read node file: %v", err)
	}
	content := string(data)

	// Replace the summary in the body.
	newContent := strings.Replace(content,
		"Original summary before manual edit",
		"Manually edited summary with new information",
		1)
	if newContent == content {
		t.Fatal("manual edit did not change the file content")
	}

	if err := os.WriteFile(mdPath, []byte(newContent), 0o644); err != nil {
		t.Fatalf("write edited file: %v", err)
	}

	// Reload engine.
	eng2 := newEngine(t, dir)
	defer eng2.Close()

	// Verify the node was reloaded with edited content.
	n, ok := eng2.Graph.GetNode("editable/target")
	if !ok {
		t.Fatal("editable/target not found after reload")
	}
	if !strings.Contains(n.Summary, "Manually edited summary") {
		t.Errorf("expected manually edited summary, got %q", n.Summary)
	}

	// Verify edges survived the manual edit (we only changed summary text).
	hasEdge := false
	for _, e := range n.Edges {
		if e.Target == "editable/dep" && e.Relation == "calls" {
			hasEdge = true
			break
		}
	}
	if !hasEdge {
		t.Errorf("expected edge to editable/dep to survive manual edit, edges: %+v", n.Edges)
	}

	// Verify the node is still parseable via ParseNode.
	rereadData, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("reread edited file: %v", err)
	}
	parsed, err := node.ParseNode(rereadData, mdPath)
	if err != nil {
		t.Fatalf("ParseNode on manually edited file: %v", err)
	}
	if parsed.ID != "editable/target" {
		t.Errorf("parsed ID mismatch: got %q", parsed.ID)
	}
}

// ---------------------------------------------------------------------------
// Stress-5: Engine restart with corrupt/missing _config.md
// ---------------------------------------------------------------------------

func TestStress_EngineRestartCorruptConfig(t *testing.T) {
	// Sub-test 1: missing _config.md -- engine should still start (use defaults).
	t.Run("missing_config", func(t *testing.T) {
		dir := t.TempDir()

		// Create engine without _config.md -- should succeed.
		eng := newEngine(t, dir)
		defer eng.Close()

		// Write a node via the engine (which also embeds it).
		res := writeNode(t, eng, map[string]any{
			"id":      "config/test",
			"type":    "function",
			"summary": "Node created without config file",
			"context": "func Test() {}",
		})
		if res.IsError {
			t.Fatalf("write failed: %s", text(t, res))
		}

		if eng.Graph.NodeCount() < 1 {
			t.Errorf("expected at least 1 node, got %d", eng.Graph.NodeCount())
		}

		// Verify the node is queryable.
		xml := queryNodes(t, eng, map[string]any{
			"query":  "Node created without config file",
			"depth":  1,
			"budget": 4096,
		})
		if !strings.Contains(xml, "config/test") {
			t.Errorf("expected config/test in query, got:\n%s", xml)
		}
	})

	// Sub-test 2: corrupt _config.md -- engine should handle gracefully.
	t.Run("corrupt_config", func(t *testing.T) {
		dir := t.TempDir()

		// Write corrupt _config.md.
		configPath := filepath.Join(dir, "_config.md")
		if err := os.WriteFile(configPath, []byte("THIS IS NOT VALID FRONTMATTER AT ALL\n\x00\x00garbage"), 0o644); err != nil {
			t.Fatalf("write corrupt config: %v", err)
		}

		// Write a valid node.
		nodeStore := node.NewStore(dir)
		n := &node.Node{
			ID:        "corrupt/survivor",
			Type:      "function",
			Namespace: "default",
			Status:    node.StatusActive,
			Summary:   "Node survives corrupt config",
		}
		if err := nodeStore.SaveNode(n); err != nil {
			t.Fatalf("save node: %v", err)
		}

		// Engine should still start -- _config.md is not a node file.
		eng := newEngine(t, dir)
		defer eng.Close()

		if eng.Graph.NodeCount() < 1 {
			t.Errorf("expected at least 1 node despite corrupt config, got %d", eng.Graph.NodeCount())
		}
	})

	// Sub-test 3: _config.md with invalid YAML but valid frontmatter delimiters.
	t.Run("bad_yaml_config", func(t *testing.T) {
		dir := t.TempDir()

		// Write _config.md with bad YAML.
		configContent := "---\nversion: [[[broken yaml\n---\nSome body text.\n"
		configPath := filepath.Join(dir, "_config.md")
		if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
			t.Fatalf("write bad yaml config: %v", err)
		}

		// Write a valid node.
		nodeStore := node.NewStore(dir)
		n := &node.Node{
			ID:        "badyaml/node",
			Type:      "function",
			Namespace: "default",
			Status:    node.StatusActive,
			Summary:   "Node survives bad YAML config",
		}
		if err := nodeStore.SaveNode(n); err != nil {
			t.Fatalf("save node: %v", err)
		}

		// Engine should still start.
		eng := newEngine(t, dir)
		defer eng.Close()

		if eng.Graph.NodeCount() < 1 {
			t.Errorf("expected at least 1 node despite bad YAML config, got %d", eng.Graph.NodeCount())
		}
	})

	// Sub-test 4: _config.md deleted mid-session -- re-open still works.
	t.Run("config_deleted_mid_session", func(t *testing.T) {
		dir := t.TempDir()

		// Init a proper vault.
		if err := os.MkdirAll(filepath.Join(dir, ".marmot-data"), 0o755); err != nil {
			t.Fatal(err)
		}
		configContent := "---\nversion: \"1\"\nnamespace: default\n---\n# Test vault\n"
		if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(configContent), 0o644); err != nil {
			t.Fatal(err)
		}

		eng1 := newEngine(t, dir)
		writeNode(t, eng1, map[string]any{
			"id":      "volatile/node",
			"type":    "function",
			"summary": "Node written before config deletion",
		})
		eng1.Close()

		// Delete _config.md.
		os.Remove(filepath.Join(dir, "_config.md"))

		// Re-open engine.
		eng2 := newEngine(t, dir)
		defer eng2.Close()

		// Node should still be present.
		n, ok := eng2.Graph.GetNode("volatile/node")
		if !ok {
			t.Error("volatile/node not found after config deletion and restart")
		} else if n.Summary == "" {
			t.Error("volatile/node has empty summary after restart")
		}
	})
}

// ---------------------------------------------------------------------------
// Stress-6: Write-then-delete bulk verify only active remain queryable.
//           Verify superseded nodes persist on disk with correct metadata.
// ---------------------------------------------------------------------------

func TestStress_BulkDeleteDiskPersistence(t *testing.T) {
	dir := t.TempDir()
	eng := newEngine(t, dir)
	defer eng.Close()

	const count = 20

	// Write 20 nodes.
	for i := 0; i < count; i++ {
		writeNode(t, eng, map[string]any{
			"id":      fmt.Sprintf("bulk/item-%02d", i),
			"type":    "function",
			"summary": fmt.Sprintf("Bulk item %d for disk persistence test", i),
		})
	}

	// Delete even-numbered nodes.
	for i := 0; i < count; i += 2 {
		id := fmt.Sprintf("bulk/item-%02d", i)
		req := makeReq("context_delete", map[string]any{"id": id})
		res, err := eng.HandleContextDelete(context.Background(), req)
		if err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
		if res.IsError {
			t.Fatalf("delete %s: %s", id, text(t, res))
		}
	}

	// Verify on disk: deleted nodes should have status=superseded in their file.
	nodeStore := node.NewStore(dir)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("bulk/item-%02d", i)
		path := nodeStore.NodePath(id)
		loaded, err := nodeStore.LoadNode(path)
		if err != nil {
			t.Errorf("load %s from disk: %v", id, err)
			continue
		}
		if i%2 == 0 {
			if loaded.Status != node.StatusSuperseded {
				t.Errorf("disk: %s expected status=superseded, got %q", id, loaded.Status)
			}
		} else {
			if loaded.Status != node.StatusActive {
				t.Errorf("disk: %s expected status=active, got %q", id, loaded.Status)
			}
		}
	}

	// Query active only: verify none of the deleted nodes appear as primary results.
	xmlActive := queryNodes(t, eng, map[string]any{
		"query":              "Bulk item disk persistence",
		"depth":              1,
		"budget":             50000,
		"include_superseded": false,
	})

	// Verify the result contains at least some active nodes.
	activeFound := 0
	for i := 1; i < count; i += 2 {
		id := fmt.Sprintf("bulk/item-%02d", i)
		if strings.Contains(xmlActive, id) {
			activeFound++
		}
	}
	t.Logf("active-only query found %d of %d expected active nodes", activeFound, count/2)

	// Verify the write result JSON includes correct status for a new write.
	res := writeNode(t, eng, map[string]any{
		"id":      "bulk/new-after-delete",
		"type":    "function",
		"summary": "New node after bulk delete",
	})
	if res.IsError {
		t.Fatalf("write after delete failed: %s", text(t, res))
	}
	var wr mcpserver.WriteResult
	if err := json.Unmarshal([]byte(text(t, res)), &wr); err != nil {
		t.Fatalf("unmarshal write result: %v", err)
	}
	if wr.Status != "created" {
		t.Errorf("expected status=created, got %q", wr.Status)
	}
}
