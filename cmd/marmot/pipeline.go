package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/traversal"
	"github.com/nurozen/context-marmot/internal/verify"
)

// runQueryPipeline executes the full query pipeline:
// load nodes -> build graph -> embed query -> search -> traverse -> compact -> print XML.
func runQueryPipeline(dir, query string, depth, budget int) error {
	// 1. Load node store and graph.
	store := node.NewStore(dir)
	g, err := graph.LoadGraph(store)
	if err != nil {
		return fmt.Errorf("load graph: %w", err)
	}

	if g.NodeCount() == 0 {
		fmt.Println(`<context_result tokens="0" nodes="0">
</context_result>`)
		return nil
	}

	// 2. Open embedding store.
	dbPath := filepath.Join(dir, ".marmot-data", "embeddings.db")
	embStore, err := embedding.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("open embedding store: %w", err)
	}
	defer embStore.Close()

	// 3. Create mock embedder and embed the query.
	embedder := embedding.NewMockEmbedder("mock-v1")
	queryVec, err := embedder.Embed(query)
	if err != nil {
		return fmt.Errorf("embed query: %w", err)
	}

	// 4. Search for top-K entry nodes.
	topK := 5
	results, err := embStore.Search(queryVec, topK, embedder.Model())
	if err != nil {
		// If no embeddings exist yet, fall back to empty result.
		fmt.Println(`<context_result tokens="0" nodes="0">
</context_result>`)
		return nil
	}

	if len(results) == 0 {
		fmt.Println(`<context_result tokens="0" nodes="0">
</context_result>`)
		return nil
	}

	// Collect entry IDs from search results.
	entryIDs := make([]string, 0, len(results))
	for _, r := range results {
		entryIDs = append(entryIDs, r.NodeID)
	}

	// 5. Traverse the graph.
	config := traversal.TraversalConfig{
		EntryIDs:    entryIDs,
		MaxDepth:    depth,
		TokenBudget: budget,
		Mode:        "adjacency",
	}
	subgraph := traversal.Traverse(g, config)

	// 6. Compact and print.
	result := traversal.Compact(g, subgraph, budget)
	fmt.Println(result.XML)
	return nil
}

// runServePipeline starts the MCP server on stdio.
func runServePipeline(dir string) error {
	embedder := embedding.NewMockEmbedder("mock-v1")
	engine, err := mcpserver.NewEngine(dir, embedder)
	if err != nil {
		return fmt.Errorf("create engine: %w", err)
	}
	defer engine.Close()

	srv := mcpserver.NewServer(engine)
	fmt.Fprintln(os.Stderr, "ContextMarmot MCP server ready on stdio")
	return srv.ListenStdio(context.Background(), os.Stdin, os.Stdout)
}

// runVerifyPipeline loads all nodes and runs integrity verification.
func runVerifyPipeline(dir string) error {
	store := node.NewStore(dir)
	metas, err := store.ListNodes()
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}

	if len(metas) == 0 {
		fmt.Println("No nodes found. Nothing to verify.")
		return nil
	}

	// Load full nodes for verification.
	var nodes []*node.Node
	for _, m := range metas {
		path := store.NodePath(m.ID)
		n, err := store.LoadNode(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", m.ID, err)
			continue
		}
		nodes = append(nodes, n)
	}

	issues := verify.VerifyIntegrity(nodes)
	if len(issues) == 0 {
		fmt.Println("No issues found.")
		return nil
	}

	fmt.Printf("Found %d issue(s):\n", len(issues))
	for _, issue := range issues {
		fmt.Printf("  [%s] %s: %s (%s)\n", issue.Severity, issue.NodeID, issue.Message, issue.IssueType)
	}
	return nil
}
