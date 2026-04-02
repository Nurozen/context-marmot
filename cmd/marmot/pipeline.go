package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/llm"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/summary"
	"github.com/nurozen/context-marmot/internal/traversal"
	"github.com/nurozen/context-marmot/internal/update"
	"github.com/nurozen/context-marmot/internal/verify"
)

// loadEmbedder reads vault config and creates the appropriate embedder.
func loadEmbedder(dir string) (embedding.Embedder, error) {
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return config.NewEmbedderFromVault(cfg)
}

// runIndexPipeline indexes all node files into the embedding store.
// If force is true, clears existing embeddings and re-indexes everything.
func runIndexPipeline(dir string, force bool) error {
	store := node.NewStore(dir)
	metas, err := store.ListNodes()
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}

	if len(metas) == 0 {
		fmt.Println("No nodes found. Nothing to index.")
		return nil
	}

	dbPath := filepath.Join(dir, ".marmot-data", "embeddings.db")
	if force {
		// Remove existing embeddings DB to start fresh (model may have changed).
		_ = os.Remove(dbPath)
	}

	embStore, err := embedding.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("open embedding store: %w", err)
	}
	defer embStore.Close()

	embedder, err := loadEmbedder(dir)
	if err != nil {
		return err
	}

	// Collect summaries for batch embedding.
	type nodeText struct {
		meta node.NodeMeta
		text string
	}
	var batch []nodeText
	for _, m := range metas {
		path := store.NodePath(m.ID)
		n, err := store.LoadNode(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", m.ID, err)
			continue
		}
		text := n.Summary
		if n.Context != "" {
			// Combine summary + context for richer embedding.
			// Truncate context to avoid exceeding embedding model limits (~8000 chars total).
			ctx := n.Context
			if len(ctx) > 6000 {
				ctx = ctx[:6000]
			}
			text = n.Summary + "\n\n" + ctx
		}
		if text == "" {
			text = n.ID
		}

		// Skip if not force and hash hasn't changed.
		if !force {
			summaryHash := fmt.Sprintf("%x", text)
			stale, err := embStore.StaleCheck(n.ID, summaryHash)
			if err == nil && !stale {
				continue
			}
		}

		batch = append(batch, nodeText{meta: node.NodeMeta{ID: n.ID}, text: text})
	}

	if len(batch) == 0 {
		fmt.Println("All nodes up to date. Nothing to index.")
		return nil
	}

	// Batch embed.
	texts := make([]string, len(batch))
	for i, b := range batch {
		texts[i] = b.text
	}
	vectors, err := embedder.EmbedBatch(texts)
	if err != nil {
		return fmt.Errorf("batch embed: %w", err)
	}

	indexed := 0
	for i, b := range batch {
		summaryHash := fmt.Sprintf("%x", b.text)
		if err := embStore.Upsert(b.meta.ID, vectors[i], summaryHash, embedder.Model()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: upsert %s: %v\n", b.meta.ID, err)
			continue
		}
		indexed++
	}

	fmt.Printf("Indexed %d/%d nodes into embedding store.\n", indexed, len(metas))
	return nil
}

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

	// 3. Create embedder from config and embed the query.
	embedder, err := loadEmbedder(dir)
	if err != nil {
		return err
	}
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
	embedder, err := loadEmbedder(dir)
	if err != nil {
		return err
	}
	engine, err := mcpserver.NewEngine(dir, embedder)
	if err != nil {
		return fmt.Errorf("create engine: %w", err)
	}
	defer engine.Close()

	// Wire namespace manager (best-effort — missing namespaces are fine).
	if nsMgr, nsErr := namespace.NewManager(dir); nsErr == nil && len(nsMgr.Namespaces) > 0 {
		engine.WithNamespaceManager(nsMgr)
		fmt.Fprintf(os.Stderr, "namespaces: %d loaded, %d bridges\n", len(nsMgr.Namespaces), len(nsMgr.Bridges))
	}

	// Wire heat map (load from _heat/default.md or create empty).
	vaultCfg, err := config.Load(dir)
	if err != nil {
		vaultCfg = &config.VaultConfig{} // safe default
	}
	nsName := vaultCfg.Namespace
	if nsName == "" {
		nsName = "default"
	}
	hm, hmErr := heatmap.Load(dir, nsName)
	if hmErr == nil {
		engine.WithHeatMap(hm)
		fmt.Fprintf(os.Stderr, "heatmap: %d pairs loaded for %s\n", hm.PairCount(), nsName)
		defer func() {
			if saveErr := heatmap.Save(dir, hm); saveErr != nil {
				fmt.Fprintf(os.Stderr, "heatmap: save error: %v\n", saveErr)
			}
		}()
	}

	// Wire classifier from vault config (reuses vaultCfg loaded above for heat map).
	var llmProvider llm.Provider // captured for summary engine
	switch vaultCfg.ClassifierProvider {
	case "openai":
		if key := config.APIKeyWithVault("openai", dir); key != "" {
			p := llm.NewOpenAIProvider(key)
			llmProvider = p
			engine.WithLLMClassifier(p)
			fmt.Fprintln(os.Stderr, "classifier: using openai/"+vaultCfg.ClassifierModel)
		} else {
			engine.WithLLMClassifier(nil)
			fmt.Fprintln(os.Stderr, "classifier: openai configured but OPENAI_API_KEY not found; using embedding-distance fallback")
		}
	case "anthropic":
		if key := config.APIKeyWithVault("anthropic", dir); key != "" {
			p := llm.NewAnthropicProvider(key)
			llmProvider = p
			engine.WithLLMClassifier(p)
			fmt.Fprintln(os.Stderr, "classifier: using anthropic/"+vaultCfg.ClassifierModel)
		} else {
			engine.WithLLMClassifier(nil)
			fmt.Fprintln(os.Stderr, "classifier: anthropic configured but ANTHROPIC_API_KEY not found; using embedding-distance fallback")
		}
	default: // "none" or ""
		engine.WithLLMClassifier(nil)
		fmt.Fprintln(os.Stderr, "classifier: using embedding-distance fallback")
	}

	// Wire summary engine.
	var sumScheduler *summary.Scheduler
	if summarizer, ok := llmProvider.(llm.Summarizer); ok {
		sumEngine := summary.NewEngine(summarizer)
		engine.WithSummaryEngine(sumEngine)

		sConfig := summary.DefaultSchedulerConfig()
		nodeLoader := func() ([]*node.Node, error) {
			metas, err := engine.NodeStore.ListActiveNodes()
			if err != nil {
				return nil, err
			}
			var nodes []*node.Node
			for _, m := range metas {
				path := engine.NodeStore.NodePath(m.ID)
				n, nerr := engine.NodeStore.LoadNode(path)
				if nerr != nil {
					continue
				}
				nodes = append(nodes, n)
			}
			return nodes, nil
		}
		sumScheduler = summary.NewScheduler(sumEngine, sConfig, dir, nsName, nodeLoader)
		engine.WithSummaryScheduler(sumScheduler)
		fmt.Fprintln(os.Stderr, "summary: engine wired, scheduler starting")
	} else {
		fmt.Fprintln(os.Stderr, "summary: no summarizer available, summaries will not be generated")
	}

	// Wire update engine.
	updateEng := update.NewEngine(engine.NodeStore, engine.Graph, engine.EmbeddingStore, engine.Embedder)
	if sumScheduler != nil {
		updateEng.WithOnChange(func(count int) {
			if metas, err := engine.NodeStore.ListNodes(); err == nil {
				sumScheduler.NotifyChange(len(metas))
			}
		})
	}
	engine.WithUpdateEngine(updateEng)
	fmt.Fprintln(os.Stderr, "update: engine wired")

	// Start summary scheduler in background.
	ctx := context.Background()
	if sumScheduler != nil {
		sumScheduler.Start(ctx)
		defer sumScheduler.Stop()
	}

	srv := mcpserver.NewServer(engine)
	fmt.Fprintln(os.Stderr, "ContextMarmot MCP server ready on stdio")
	return srv.ListenStdio(ctx, os.Stdin, os.Stdout)
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
