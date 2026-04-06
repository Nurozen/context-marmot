package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nurozen/context-marmot/internal/api"
	"github.com/nurozen/context-marmot/internal/classifier"
	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/indexer"
	"github.com/nurozen/context-marmot/internal/llm"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/summary"
	"github.com/nurozen/context-marmot/internal/traversal"
	"github.com/nurozen/context-marmot/internal/update"
	"github.com/nurozen/context-marmot/internal/verify"
	"github.com/nurozen/context-marmot/web"
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
	defer func() { _ = embStore.Close() }()

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
		path := m.FilePath
		if path == "" {
			path = store.NodePath(m.ID)
		}
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
	defer func() { _ = embStore.Close() }()

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

// engineResult holds the fully-wired engine and its cleanup function.
type engineResult struct {
	Engine    *mcpserver.Engine
	HeatMap   *heatmap.HeatMap
	Scheduler *summary.Scheduler
	Cleanup   func()
}

// buildEngine creates a fully-wired MCP engine from a vault directory.
// The returned cleanup function must be called when the engine is no longer needed.
func buildEngine(dir string) (*engineResult, error) {
	embedder, err := loadEmbedder(dir)
	if err != nil {
		return nil, err
	}
	engine, err := mcpserver.NewEngine(dir, embedder)
	if err != nil {
		return nil, fmt.Errorf("create engine: %w", err)
	}

	// Wire namespace manager (best-effort — missing namespaces are fine).
	var nsMgr *namespace.Manager
	if mgr, nsErr := namespace.NewManager(dir); nsErr == nil &&
		(len(mgr.Namespaces) > 0 || len(mgr.Bridges) > 0 || len(mgr.CrossVaultBridges) > 0) {
		nsMgr = mgr
		engine.WithNamespaceManager(nsMgr)
		fmt.Fprintf(os.Stderr, "namespaces: %d loaded, %d bridges, %d cross-vault bridges\n",
			len(nsMgr.Namespaces), len(nsMgr.Bridges), len(nsMgr.CrossVaultBridges))
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

	// Wire vault registry for cross-vault traversal.
	rt, _ := routes.Load() // best-effort; nil is fine
	vaultID := vaultCfg.VaultID
	hasCrossVaultBridges := nsMgr != nil && len(nsMgr.CrossVaultBridges) > 0
	hasRoutes := rt != nil && len(rt.List()) > 0
	if vaultID != "" && (hasCrossVaultBridges || hasRoutes) {
		var bridges []*namespace.Bridge
		if nsMgr != nil {
			bridges = nsMgr.CrossVaultBridges
		}
		vr := namespace.NewVaultRegistry(vaultID, dir, bridges, rt)
		engine.WithVaultRegistry(vr)
		fmt.Fprintf(os.Stderr, "vault registry: %d remote vaults registered\n", len(vr.KnownVaultIDs()))
	}

	var hm *heatmap.HeatMap
	if loaded, hmErr := heatmap.Load(dir, nsName); hmErr == nil {
		hm = loaded
		engine.WithHeatMap(hm)
		fmt.Fprintf(os.Stderr, "heatmap: %d pairs loaded for %s\n", hm.PairCount(), nsName)
	}

	// Wire classifier from vault config.
	var llmProvider llm.Provider
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
	default:
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

	cleanup := func() {
		if sumScheduler != nil {
			sumScheduler.Stop()
		}
		if hm != nil {
			if saveErr := heatmap.Save(dir, hm); saveErr != nil {
				fmt.Fprintf(os.Stderr, "heatmap: save error: %v\n", saveErr)
			}
		}
		_ = engine.Close()
	}

	return &engineResult{
		Engine:    engine,
		HeatMap:   hm,
		Scheduler: sumScheduler,
		Cleanup:   cleanup,
	}, nil
}

// runServePipeline starts the MCP server on stdio.
func runServePipeline(dir string) error {
	result, err := buildEngine(dir)
	if err != nil {
		return err
	}
	defer result.Cleanup()

	ctx := context.Background()
	if result.Scheduler != nil {
		result.Scheduler.Start(ctx)
	}

	srv := mcpserver.NewServer(result.Engine)
	fmt.Fprintln(os.Stderr, "ContextMarmot MCP server ready on stdio")
	return srv.ListenStdio(ctx, os.Stdin, os.Stdout)
}

// runUIPipeline starts the HTTP UI server backed by the shared engine.
func runUIPipeline(dir string, port int, noOpen bool) error {
	result, err := buildEngine(dir)
	if err != nil {
		return err
	}
	defer result.Cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if result.Scheduler != nil {
		result.Scheduler.Start(ctx)
	}

	// Create API server with embedded frontend assets.
	apiServer := api.NewServer(result.Engine, web.Assets)

	addr := fmt.Sprintf(":%d", port)
	url := fmt.Sprintf("http://localhost:%d", port)
	fmt.Fprintf(os.Stderr, "ContextMarmot UI server starting at %s\n", url)

	// Auto-open browser (best-effort).
	if !noOpen {
		go func() {
			// Small delay to let the server start.
			time.Sleep(500 * time.Millisecond)
			openBrowser(url)
		}()
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nShutting down UI server...")
		cancel()
		os.Exit(0)
	}()

	return apiServer.ListenAndServe(addr)
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		return
	}
	_ = exec.Command(cmd, args...).Start()
}

// runVerifyEnhanced loads nodes (optionally filtered by namespace) and runs
// integrity verification with optional staleness checks.
func runVerifyEnhanced(dir, ns string, checkStaleness, checkBridges bool) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist", dir)
	}

	store := node.NewStore(dir)
	metas, err := store.ListNodes()
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}

	if len(metas) == 0 {
		fmt.Println("No nodes found. Nothing to verify.")
		return nil
	}

	var nodes []*node.Node
	for _, m := range metas {
		// Filter by namespace if specified.
		if ns != "" && m.Namespace != ns {
			continue
		}
		path := m.FilePath
		if path == "" {
			path = store.NodePath(m.ID)
		}
		n, err := store.LoadNode(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", m.ID, err)
			continue
		}
		nodes = append(nodes, n)
	}

	if len(nodes) == 0 {
		if ns != "" {
			fmt.Printf("No nodes found for namespace %q.\n", ns)
		} else {
			fmt.Println("No nodes found. Nothing to verify.")
		}
		return nil
	}

	projectRoot := filepath.Dir(dir)
	issues := verify.VerifyIntegrity(nodes, projectRoot)

	// Also check staleness if requested.
	if checkStaleness {
		for _, n := range nodes {
			if n.Source.Path == "" || n.Source.Hash == "" {
				continue
			}
			status, err := verify.VerifyStaleness(n, projectRoot)
			if err != nil {
				continue
			}
			if status.IsStale {
				issues = append(issues, verify.IntegrityIssue{
					NodeID:    n.ID,
					IssueType: "stale_source",
					Message:   fmt.Sprintf("source hash mismatch: stored %s, current %s", truncateHashForDisplay(status.StoredHash), truncateHashForDisplay(status.CurrentHash)),
					Severity:  verify.Warning,
				})
			}
		}
	}

	// Check cross-vault bridge connectivity.
	if checkBridges {
		nsMgr, nsErr := namespace.NewManager(dir)
		if nsErr != nil {
			issues = append(issues, verify.IntegrityIssue{
				NodeID:    "",
				IssueType: "bridge_error",
				Message:   fmt.Sprintf("failed to load namespace manager: %v", nsErr),
				Severity:  verify.Error,
			})
		} else if len(nsMgr.CrossVaultBridges) > 0 {
			vaultCfg, _ := config.Load(dir)
			localVaultID := ""
			if vaultCfg != nil {
				localVaultID = vaultCfg.VaultID
			}

			if localVaultID == "" {
				issues = append(issues, verify.IntegrityIssue{
					NodeID:    "",
					IssueType: "bridge_config",
					Message:   "vault has no vault_id set; cross-vault bridge verification requires vault_id in _config.md",
					Severity:  verify.Warning,
				})
			} else {
				for _, b := range nsMgr.CrossVaultBridges {
					// Check that the remote vault path exists and is accessible.
					remoteDir := b.TargetVaultPath
					if b.SourceVaultPath != "" && b.SourceVaultID != localVaultID {
						remoteDir = b.SourceVaultPath
					}

					if remoteDir == "" {
						issues = append(issues, verify.IntegrityIssue{
							NodeID:    "",
							IssueType: "bridge_missing_path",
							Message:   fmt.Sprintf("cross-vault bridge %s <-> %s has no remote vault path", b.SourceVaultID, b.TargetVaultID),
							Severity:  verify.Warning,
						})
						continue
					}

					if _, err := os.Stat(remoteDir); os.IsNotExist(err) {
						issues = append(issues, verify.IntegrityIssue{
							NodeID:    "",
							IssueType: "bridge_unreachable",
							Message:   fmt.Sprintf("cross-vault bridge target %q is unreachable (path: %s)", b.TargetVaultID, remoteDir),
							Severity:  verify.Error,
						})
						continue
					}

					// Check that the remote vault config is loadable and vault_id matches.
					remoteCfg, err := config.Load(remoteDir)
					if err != nil {
						issues = append(issues, verify.IntegrityIssue{
							NodeID:    "",
							IssueType: "bridge_config_error",
							Message:   fmt.Sprintf("cross-vault bridge: cannot load config from %s: %v", remoteDir, err),
							Severity:  verify.Warning,
						})
						continue
					}

					expectedID := b.TargetVaultID
					if b.SourceVaultID != localVaultID {
						expectedID = b.SourceVaultID
					}
					if remoteCfg.VaultID != expectedID {
						issues = append(issues, verify.IntegrityIssue{
							NodeID:    "",
							IssueType: "bridge_id_mismatch",
							Message:   fmt.Sprintf("cross-vault bridge expects vault_id %q but remote vault has %q", expectedID, remoteCfg.VaultID),
							Severity:  verify.Error,
						})
					}
				}
			}
		}
	}

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

// truncateHashForDisplay returns the first 8 characters of a hash string.
func truncateHashForDisplay(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// runStatusPipeline prints vault statistics.
func runStatusPipeline(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist", dir)
	}

	store := node.NewStore(dir)
	allMetas, err := store.ListNodes()
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}

	// Count by status.
	var activeCount, supersededCount, archivedCount int
	for _, m := range allMetas {
		switch m.Status {
		case "superseded":
			supersededCount++
		case "archived":
			archivedCount++
		default: // "active" or ""
			activeCount++
		}
	}

	// Load graph for edge count.
	g, err := graph.LoadGraph(store)
	if err != nil {
		return fmt.Errorf("load graph: %w", err)
	}

	// Check for namespaces.
	nsMgr, _ := namespace.NewManager(dir)

	// Check embedding store.
	dbPath := filepath.Join(dir, ".marmot-data", "embeddings.db")
	var embeddingCount int
	if embStore, err := embedding.NewStore(dbPath); err == nil {
		embeddingCount = embStore.Count()
		_ = embStore.Close()
	}

	// Check for stale nodes (those with source.path set).
	projectRoot := filepath.Dir(dir)
	var staleCount int
	for _, m := range allMetas {
		path := m.FilePath
		if path == "" {
			path = store.NodePath(m.ID)
		}
		n, err := store.LoadNode(path)
		if err != nil || n.Source.Path == "" || n.Source.Hash == "" {
			continue
		}
		status, err := verify.VerifyStaleness(n, projectRoot)
		if err == nil && status.IsStale {
			staleCount++
		}
	}

	// Print summary.
	fmt.Printf("Vault: %s\n", dir)
	fmt.Printf("Nodes: %d total (%d active, %d superseded, %d archived)\n",
		len(allMetas), activeCount, supersededCount, archivedCount)
	fmt.Printf("Edges: %d\n", g.EdgeCount())
	fmt.Printf("Embeddings: %d\n", embeddingCount)
	fmt.Printf("Stale: %d\n", staleCount)

	if nsMgr != nil && len(nsMgr.Namespaces) > 0 {
		fmt.Printf("Namespaces: %d\n", len(nsMgr.Namespaces))
		for name := range nsMgr.Namespaces {
			fmt.Printf("  - %s\n", name)
		}
		fmt.Printf("Bridges: %d\n", len(nsMgr.Bridges))
	}

	// Check for heat map.
	vaultCfg, _ := config.Load(dir)
	nsName := "default"
	if vaultCfg != nil && vaultCfg.Namespace != "" {
		nsName = vaultCfg.Namespace
	}
	if hm, err := heatmap.Load(dir, nsName); err == nil {
		fmt.Printf("Heat map: %d pairs\n", hm.PairCount())
	}

	// Check for summary.
	if result, err := summary.ReadSummary(dir, nsName); err == nil {
		fmt.Printf("Summary: generated at %s (%d nodes)\n", result.GeneratedAt.Format("2006-01-02 15:04"), result.NodeCount)
	} else {
		fmt.Printf("Summary: not generated\n")
	}

	return nil
}

// runWatchPipeline starts a file watcher that auto-reindexes on changes.
func runWatchPipeline(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist", dir)
	}

	store := node.NewStore(dir)
	g, err := graph.LoadGraph(store)
	if err != nil {
		return fmt.Errorf("load graph: %w", err)
	}

	dbPath := filepath.Join(dir, ".marmot-data", "embeddings.db")
	embStore, err := embedding.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("open embedding store: %w", err)
	}
	defer func() { _ = embStore.Close() }()

	embedder, err := loadEmbedder(dir)
	if err != nil {
		return err
	}

	updateEng := update.NewEngine(store, g, embStore, embedder)

	// Determine paths to watch — use the vault dir itself.
	watchCfg := update.DefaultWatcherConfig()
	watchCfg.Paths = []string{dir}

	watcher, err := update.NewWatcher(updateEng, watchCfg)
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher.Start(ctx)
	fmt.Fprintf(os.Stderr, "Watching %s for changes (Ctrl+C to stop)...\n", dir)

	// Wait for interrupt signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	fmt.Fprintln(os.Stderr, "\nStopping watcher...")
	return watcher.Stop()
}

// runBridgePipeline creates a bridge manifest between two namespaces.
func runBridgePipeline(dir, nsA, nsB, relationsStr string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist", dir)
	}

	relations := strings.Split(relationsStr, ",")
	for i := range relations {
		relations[i] = strings.TrimSpace(relations[i])
	}

	// CreateBridge both creates and saves the bridge manifest.
	_, err := namespace.CreateBridge(dir, nsA, nsB, relations)
	if err != nil {
		return fmt.Errorf("create bridge: %w", err)
	}

	fmt.Printf("Created bridge %s <-> %s\n", nsA, nsB)
	fmt.Printf("Allowed relations: %s\n", strings.Join(relations, ", "))
	return nil
}

// runCrossVaultBridgePipeline creates a cross-vault bridge between two vault directories.
func runCrossVaultBridgePipeline(localDir, remoteDir, relationsStr string) error {
	// Validate both directories exist.
	if info, err := os.Stat(localDir); err != nil || !info.IsDir() {
		return fmt.Errorf("local vault directory %q does not exist or is not a directory", localDir)
	}
	if info, err := os.Stat(remoteDir); err != nil || !info.IsDir() {
		return fmt.Errorf("remote vault directory %q does not exist or is not a directory", remoteDir)
	}

	relations := strings.Split(relationsStr, ",")
	for i := range relations {
		relations[i] = strings.TrimSpace(relations[i])
	}

	bridge, err := namespace.CreateCrossVaultBridge(localDir, remoteDir, relations)
	if err != nil {
		return fmt.Errorf("create cross-vault bridge: %w", err)
	}

	fmt.Printf("Created cross-vault bridge %s <-> %s\n", bridge.Source, bridge.Target)
	fmt.Printf("  Local:  %s\n", localDir)
	fmt.Printf("  Remote: %s\n", remoteDir)
	fmt.Printf("  Allowed relations: %s\n", strings.Join(relations, ", "))
	return nil
}

// runSummarizePipeline regenerates the namespace summary using an LLM provider.
func runSummarizePipeline(dir, ns string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist", dir)
	}

	// Load config for namespace and LLM provider.
	vaultCfg, err := config.Load(dir)
	if err != nil {
		vaultCfg = &config.VaultConfig{}
	}
	if ns == "" {
		ns = vaultCfg.Namespace
		if ns == "" {
			ns = "default"
		}
	}

	// Create LLM provider based on config.
	var summarizer llm.Summarizer
	switch vaultCfg.ClassifierProvider {
	case "openai":
		if key := config.APIKeyWithVault("openai", dir); key != "" {
			summarizer = llm.NewOpenAIProvider(key)
		}
	case "anthropic":
		if key := config.APIKeyWithVault("anthropic", dir); key != "" {
			summarizer = llm.NewAnthropicProvider(key)
		}
	}

	if summarizer == nil {
		return fmt.Errorf("no LLM provider configured; run 'marmot configure' to set up a classifier provider")
	}

	sumEngine := summary.NewEngine(summarizer)

	// Load active nodes.
	store := node.NewStore(dir)
	activeMetas, err := store.ListActiveNodes()
	if err != nil {
		return fmt.Errorf("list active nodes: %w", err)
	}

	var nodes []*node.Node
	for _, m := range activeMetas {
		path := m.FilePath
		if path == "" {
			path = store.NodePath(m.ID)
		}
		n, err := store.LoadNode(path)
		if err != nil {
			continue
		}
		nodes = append(nodes, n)
	}

	if len(nodes) == 0 {
		fmt.Println("No active nodes to summarize.")
		return nil
	}

	ctx := context.Background()
	result, err := sumEngine.GenerateSummary(ctx, ns, nodes)
	if err != nil {
		return fmt.Errorf("generate summary: %w", err)
	}

	if err := summary.WriteSummary(dir, ns, result); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	fmt.Printf("Summary generated for %s (%d nodes, %d chars)\n", ns, result.NodeCount, len(result.Content))
	return nil
}

// runReembedPipeline rebuilds all embeddings by delegating to the index pipeline with force=true.
func runReembedPipeline(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist", dir)
	}

	fmt.Println("Rebuilding all embeddings...")
	return runIndexPipeline(dir, true)
}

// classifierAdapter wraps a *classifier.Classifier so it satisfies
// indexer.Classifier. Both GraphReader interfaces are structurally identical
// (GetNode(id) (*node.Node, bool)) but Go treats them as distinct named types.
type classifierAdapter struct {
	cls *classifier.Classifier
}

func (a *classifierAdapter) Classify(ctx context.Context, incoming *node.Node, g indexer.GraphReader) (llm.ClassifyResult, error) {
	return a.cls.Classify(ctx, incoming, g)
}

// runStaticIndexPipeline runs the static analysis indexer on a source directory,
// producing knowledge-graph nodes with embeddings for all supported source files.
func runStaticIndexPipeline(dir string, srcDir string, incremental bool) error {
	// 1. Validate vault dir exists.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", dir)
	}

	// 2. Load vault config.
	vaultCfg, err := config.Load(dir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 3. Create node store from vault dir.
	nodeStore := node.NewStore(dir)

	// 4. Open embedding store.
	dbPath := filepath.Join(dir, ".marmot-data", "embeddings.db")
	embStore, err := embedding.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("open embedding store: %w", err)
	}
	defer func() { _ = embStore.Close() }()

	// 5. Load embedder from vault config.
	embedder, err := loadEmbedder(dir)
	if err != nil {
		return err
	}

	// 6. Load graph (for classifier lookups) — best effort, nil if fails.
	var g *graph.Graph
	if loaded, gErr := graph.LoadGraph(nodeStore); gErr == nil {
		g = loaded
	}

	// 7. Create classifier if LLM provider configured — best effort, nil if fails.
	var cls *classifier.Classifier
	switch vaultCfg.ClassifierProvider {
	case "openai":
		if key := config.APIKeyWithVault("openai", dir); key != "" {
			cls = &classifier.Classifier{
				Store:    embStore,
				Embedder: embedder,
				LLM:      llm.NewOpenAIProvider(key),
			}
		}
	case "anthropic":
		if key := config.APIKeyWithVault("anthropic", dir); key != "" {
			cls = &classifier.Classifier{
				Store:    embStore,
				Embedder: embedder,
				LLM:      llm.NewAnthropicProvider(key),
			}
		}
	}

	// 8. Create default registry.
	registry := indexer.NewDefaultRegistry()

	// 9. Resolve srcDir to absolute path.
	absSrcDir, err := filepath.Abs(srcDir)
	if err != nil {
		return fmt.Errorf("resolve source path: %w", err)
	}

	// Validate source directory exists.
	if info, err := os.Stat(absSrcDir); err != nil || !info.IsDir() {
		return fmt.Errorf("source directory %q does not exist or is not a directory", absSrcDir)
	}

	// 10. Create RunnerConfig with namespace from config (default: "default").
	nsName := vaultCfg.Namespace
	if nsName == "" {
		nsName = "default"
	}

	runnerCfg := indexer.RunnerConfig{
		SrcDir:      absSrcDir,
		VaultDir:    dir,
		Namespace:   nsName,
		Incremental: incremental,
	}

	// 11. Create and run Runner.
	fmt.Printf("Indexing source directory: %s\n", absSrcDir)

	ctx := context.Background()
	var idxClassifier indexer.Classifier
	if cls != nil {
		idxClassifier = &classifierAdapter{cls: cls}
	}
	runner := indexer.NewRunner(runnerCfg, registry, nodeStore, embStore, embedder, idxClassifier, g)
	result, err := runner.Run(ctx)
	if err != nil {
		return fmt.Errorf("indexer run: %w", err)
	}

	// 12. Print results.
	fmt.Printf("Static analysis complete: %s\n", result)
	return nil
}
