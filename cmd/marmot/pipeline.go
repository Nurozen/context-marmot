package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/api"
	"github.com/nurozen/context-marmot/internal/classifier"
	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/daemon"
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
	"github.com/nurozen/context-marmot/internal/update"
	"github.com/nurozen/context-marmot/internal/verify"
	"github.com/nurozen/context-marmot/internal/warren"
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
		// Deleting the DB under a live owner's open WAL connection is not
		// safe — the owner would keep writing into the unlinked file.
		if info, alive := ownerAlive(dir); alive {
			return fmt.Errorf("vault is served by marmot daemon (pid %d); index --force would delete the embeddings DB under its open connection — stop the daemon first", info.PID)
		}
		// Remove existing embeddings DB (and WAL sidecars) to start fresh (model may have changed).
		_ = os.Remove(dbPath)
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
	}

	// Local vault store: read-write open is intentional (remote vaults go
	// through VaultRegistry, which opens read-only).
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

// runQueryPipeline executes the same query path used by MCP/UI so cross-vault
// routes and active Warren mounts are included consistently.
func runQueryPipeline(dir, query string, depth, budget int) error {
	result, err := buildEngine(dir)
	if err != nil {
		return err
	}
	defer result.Cleanup()

	res, err := result.Engine.HandleContextQuery(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_query",
			Arguments: map[string]any{
				"query":  query,
				"depth":  depth,
				"budget": budget,
			},
		},
	})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("%s", toolResultText(res))
	}
	fmt.Println(toolResultText(res))
	return nil
}

func toolResultText(res *mcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	if text, ok := res.Content[0].(mcp.TextContent); ok {
		return text.Text
	}
	return fmt.Sprintf("%v", res.Content[0])
}

// engineResult holds the fully-wired engine and its cleanup function.
type engineResult struct {
	Engine    *mcpserver.Engine
	HeatMap   *heatmap.HeatMap
	Scheduler *summary.Scheduler
	LLM       llm.Provider // may be nil when no LLM is configured
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

	// Load namespace manager (best-effort — missing namespaces are fine).
	// It is attached after Warren bridge discovery so runtime Warren bridges
	// participate in the same cross-vault validation path.
	var nsMgr *namespace.Manager
	if mgr, nsErr := namespace.NewManager(dir); nsErr == nil {
		nsMgr = mgr
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

	// Wire vault registry for cross-vault and Warren-mounted traversal.
	rt, _ := routes.Load() // best-effort; nil is fine
	if rt == nil {
		rt = &routes.RoutingTable{Vaults: make(map[string]routes.VaultEntry)}
	}
	vaultID := vaultCfg.VaultID
	warrenBridgeDeclarations := false
	if mounts, mountErr := warren.ActiveMounts(dir); mountErr == nil {
		for _, mount := range mounts {
			if mount.VaultID != "" && mount.Available {
				rt.Set(mount.VaultID, mount.Path)
			}
		}
		if bridges, declared := warrenRuntimeBridges(dir, mounts); declared {
			warrenBridgeDeclarations = true
			if nsMgr == nil {
				nsMgr = emptyNamespaceManager(dir)
			}
			nsMgr.CrossVaultBridges = append(nsMgr.CrossVaultBridges, bridges...)
		}
		if len(mounts) > 0 {
			fmt.Fprintf(os.Stderr, "warren: %d active project mounts loaded\n", len(mounts))
		}
	}
	if nsMgr != nil &&
		(len(nsMgr.Namespaces) > 0 || len(nsMgr.Bridges) > 0 || len(nsMgr.CrossVaultBridges) > 0 || warrenBridgeDeclarations) {
		engine.WithNamespaceManager(nsMgr)
		fmt.Fprintf(os.Stderr, "namespaces: %d loaded, %d bridges, %d cross-vault bridges\n",
			len(nsMgr.Namespaces), len(nsMgr.Bridges), len(nsMgr.CrossVaultBridges))
	}
	hasCrossVaultBridges := nsMgr != nil && len(nsMgr.CrossVaultBridges) > 0
	hasRoutes := rt != nil && len(rt.List()) > 0
	if hasCrossVaultBridges || hasRoutes {
		var bridges []*namespace.Bridge
		if nsMgr != nil {
			bridges = nsMgr.CrossVaultBridges
		}
		vr := namespace.NewVaultRegistry(vaultID, dir, bridges, rt)
		engine.WithVaultRegistry(vr)
		fmt.Fprintf(os.Stderr, "vault registry: %d remote vaults registered (global routing table; MARMOT_ROUTES=off disables)\n", len(vr.KnownVaultIDs()))
	}

	// Detach the heat map when a live serve owner exists: it records heat for
	// this vault itself, and a second attached map would clobber its saves
	// (both the per-query save in HandleContextQuery and the Cleanup save are
	// nil-gated, so skipping WithHeatMap suppresses both). Inert without a
	// daemon owner — nothing publishes daemon.info.json then.
	var hm *heatmap.HeatMap
	if info, alive := ownerAlive(dir); alive {
		fmt.Fprintf(os.Stderr, "heatmap: detached — vault owner (pid %d) records heat\n", info.PID)
	} else if loaded, hmErr := heatmap.Load(dir, nsName); hmErr == nil {
		hm = loaded
		engine.WithHeatMap(hm)
		fmt.Fprintf(os.Stderr, "heatmap: %d pairs loaded for %s\n", hm.PairCount(), nsName)
	}

	// Wire classifier from vault config.
	var llmProvider llm.Provider
	switch vaultCfg.ClassifierProvider {
	case "openai":
		if key := config.APIKeyWithVault("openai", dir); key != "" {
			p := llm.NewOpenAIProviderWithModel(key, vaultCfg.ClassifierModel)
			llmProvider = p
			engine.WithLLMClassifier(p)
			fmt.Fprintln(os.Stderr, "classifier: using openai/"+p.Model())
		} else {
			engine.WithLLMClassifier(nil)
			fmt.Fprintln(os.Stderr, "classifier: openai configured but OPENAI_API_KEY not found; using embedding-distance fallback")
		}
	case "anthropic":
		if key := config.APIKeyWithVault("anthropic", dir); key != "" {
			p := llm.NewAnthropicProviderWithModel(key, vaultCfg.ClassifierModel)
			llmProvider = p
			engine.WithLLMClassifier(p)
			fmt.Fprintln(os.Stderr, "classifier: using anthropic/"+p.Model())
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
	updateEng := update.NewEngine(engine.NodeStore, engine.GetGraph(), engine.EmbeddingStore, engine.Embedder)
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
		LLM:       llmProvider,
		Cleanup:   cleanup,
	}, nil
}

func emptyNamespaceManager(dir string) *namespace.Manager {
	return &namespace.Manager{
		VaultDir:   dir,
		Namespaces: make(map[string]*namespace.Namespace),
		Bridges:    make(map[string]*namespace.Bridge),
	}
}

func warrenRuntimeBridges(marmotDir string, mounts []warren.ProjectStatus) ([]*namespace.Bridge, bool) {
	state, _, err := warren.LoadWorkspaceStateFromMarmot(marmotDir)
	if err != nil {
		// Fail-open is today's semantic (a broken warren must not brick local
		// queries), but the missing enforcement must not be silent.
		fmt.Fprintf(os.Stderr, "warning: warren workspace state unreadable (%s): %v — cross-vault bridge policy NOT enforced\n", marmotDir, err)
		return nil, false
	}

	active := make(map[string]map[string]warren.ProjectStatus)
	for _, mount := range mounts {
		if !mount.Active || !mount.Available || mount.VaultID == "" {
			continue
		}
		projects := active[mount.WarrenID]
		if projects == nil {
			projects = make(map[string]warren.ProjectStatus)
			active[mount.WarrenID] = projects
		}
		projects[mount.ProjectID] = mount
	}

	declared := false
	merged := make(map[string]*namespace.Bridge)
	relationSets := make(map[string]map[string]bool)
	for warrenID, entry := range state.Warrens {
		manifest, _, err := warren.LoadManifest(entry.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: warren %q bridge manifest unreadable (%s): %v — cross-vault bridge policy NOT enforced for this warren\n", warrenID, entry.Path, err)
			continue
		}
		if len(manifest.Bridges) > 0 {
			declared = true
		}
		activeProjects := active[warrenID]
		if len(activeProjects) == 0 {
			continue
		}
		for _, bridge := range manifest.Bridges {
			source, sourceOK := activeProjects[bridge.Source]
			target, targetOK := activeProjects[bridge.Target]
			if !sourceOK || !targetOK || source.VaultID == "" || target.VaultID == "" {
				continue
			}
			key := runtimeBridgeKey(source.VaultID, target.VaultID)
			runtimeBridge, ok := merged[key]
			if !ok {
				runtimeBridge = &namespace.Bridge{
					Source:          bridge.Source,
					Target:          bridge.Target,
					SourceVaultID:   source.VaultID,
					TargetVaultID:   target.VaultID,
					SourceVaultPath: source.Path,
					TargetVaultPath: target.Path,
				}
				merged[key] = runtimeBridge
				relationSets[key] = make(map[string]bool)
			}
			for _, relation := range bridge.Relations {
				if relation == "" || relationSets[key][relation] {
					continue
				}
				relationSets[key][relation] = true
				runtimeBridge.AllowedRelations = append(runtimeBridge.AllowedRelations, relation)
			}
		}
	}

	bridges := make([]*namespace.Bridge, 0, len(merged))
	for _, bridge := range merged {
		bridges = append(bridges, bridge)
	}
	return bridges, declared
}

func runtimeBridgeKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// ownerWedgeWait bounds how long a serve process retries while the flock is
// held but no owner is reachable — daemon.info.json missing (owner wedged
// between winning the flock and listening) OR readable but pointing at a
// socket that refuses the dial (stale info from a SIGKILLed predecessor, a
// reaped tmp socket, a wedged accept loop). Either way the election must
// yield a clear error, not a silent 50ms spin (plan 2.11). A var so tests
// can shrink it.
var ownerWedgeWait = 10 * time.Second

// runServePipeline starts the MCP server on stdio. When daemon mode is
// enabled (dark launch: MARMOT_DAEMON=1) it runs the single-owner election —
// the first serve per vault wins the flock and owns the engine; every other
// serve relays its stdio session to the owner over a unix socket. Otherwise
// (the default, an explicit opt-out, or Windows where flock does not exist)
// it serves standalone exactly as before.
func runServePipeline(dir string, noDaemon bool) error {
	if os.Getenv("MARMOT_DAEMON") != "1" ||
		noDaemon || os.Getenv("MARMOT_NO_DAEMON") == "1" || runtime.GOOS == "windows" {
		return runServeStandalone(dir)
	}

	daemon.Version = version
	dataDir := filepath.Join(dir, ".marmot-data")
	// One ClientSession per process: exactly one goroutine ever reads stdin,
	// so switching roles (proxy re-entry, owner promotion) can never leave a
	// competing reader behind to steal a client line. It also carries the
	// recorded MCP handshake across proxy re-entries.
	client := daemon.NewClientSession(os.Stdin)
	var wedgedSince time.Time
	for {
		lock, err := daemon.TryAcquire(dataDir)
		switch {
		case err == nil:
			return runServeOwner(dir, lock, client)
		case errors.Is(err, daemon.ErrHeld):
			// cause is the reason no owner is reachable this iteration.
			var cause error
			info, ierr := daemon.ReadInfo(dataDir)
			if ierr != nil {
				// Owner mid-startup: the flock is held but daemon.info.json
				// is not published yet.
				cause = ierr
			} else {
				perr := daemon.RunProxySession(client, os.Stdout, info.Socket)
				switch {
				case errors.Is(perr, daemon.ErrNoOwner):
					// Stale info.json / wedged owner: the socket never
					// accepted the dial, so no progress was made — this
					// counts toward the wedge bound below. (Checked before
					// ErrOwnerGone, which ErrNoOwner wraps.)
					cause = perr
				case errors.Is(perr, daemon.ErrOwnerGone):
					// We were attached to a live owner and it died: real
					// progress, so reset the wedge clock and re-elect.
					wedgedSince = time.Time{}
					time.Sleep(50 * time.Millisecond)
					continue
				default:
					// Client EOF (nil), MARMOT_PROXY_NO_RESUME exit
					// (ErrNoResume, nonzero so the MCP client restarts
					// serve), or a fatal relay error.
					return perr
				}
			}
			// Flock held but no reachable owner: bound the retry so a wedged
			// or stale owner yields a clear error instead of a silent spin.
			if wedgedSince.IsZero() {
				wedgedSince = time.Now()
			} else if time.Since(wedgedSince) > ownerWedgeWait {
				return fmt.Errorf("daemon lock is held but no reachable owner appeared within %s: %w", ownerWedgeWait, cause)
			}
			time.Sleep(50 * time.Millisecond)
		default:
			return err
		}
	}
}

// runServeStandalone starts the MCP server on stdio without daemon election —
// today's serve behavior, kept bit-identical for the default/opt-out path.
func runServeStandalone(dir string) error {
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

// runServeOwner runs the elected owner: the single engine, summary scheduler,
// and graph watcher for the vault, serving its own MCP client on stdin (the
// process's ClientSession, so a defeated proxy's read-ahead is preserved
// across promotion) and every proxy over the unix socket (fresh
// mcpserver.Server per connection, one shared Engine). After its own client
// disconnects it lingers until the last proxy session ends, then tears down
// in strict order — Owner.Close (stop accepting, remove socket) runs before
// the shared engine closes (so no session executes against a closed DB) and
// before lock.Release (so a late proxy either fails the dial or gets a clean
// drop and re-enters the election), never a half-served session.
func runServeOwner(dir string, lock *daemon.Lock, stdin io.Reader) error {
	result, err := buildEngine(dir)
	if err != nil {
		_ = lock.Release()
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Scoped signal registration (unregistered on return) so SIGTERM/SIGINT
	// take the same graceful shutdown path as client EOF.
	sigCtx, sigStop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer sigStop()

	if result.Scheduler != nil {
		result.Scheduler.Start(ctx) // the vault's single scheduler
	}
	stopWatch, watchErr := daemon.StartGraphWatcher(dir, result.Engine)
	if watchErr != nil {
		// Non-fatal: the owner still serves, but won't see external node writes.
		fmt.Fprintf(os.Stderr, "daemon: graph watcher failed to start: %v\n", watchErr)
		stopWatch = func() {}
	}

	dataDir := filepath.Join(dir, ".marmot-data")
	own := daemon.NewOwner(dataDir, lock, func(c net.Conn) error {
		// Fresh Server per connection over the shared, concurrency-safe Engine.
		srv := mcpserver.NewServer(result.Engine)
		return srv.ListenStdio(sigCtx, c, c)
	})
	if err := own.Listen(); err != nil {
		cancel()
		stopWatch()
		daemon.BoundedStop(result.Scheduler, 3*time.Second)
		saveHeatmapAndClose(dir, result)
		_ = lock.Release()
		return err
	}

	// Serve our own MCP client on stdio.
	ownSrv := mcpserver.NewServer(result.Engine)
	fmt.Fprintln(os.Stderr, "ContextMarmot MCP server ready on stdio (vault owner)")
	stdioErr := ownSrv.ListenStdio(sigCtx, stdin, os.Stdout) // returns on client EOF

	// Linger headless: our client is gone, but proxies may still be attached.
	// On SIGINT/SIGTERM this returns immediately with sessions possibly still
	// active — the bounded drain below covers them.
	own.WaitIdle(sigCtx)

	// Stop accepting and remove the socket BEFORE tearing anything down, so a
	// late proxy fails the dial (and re-elects) instead of reaching a closing
	// owner; then give in-flight sessions a bounded window to finish against
	// the still-open engine. On the client-EOF path WaitIdle already drained
	// to zero, so both steps are instant; the bound only bites on the signal
	// path, where sigCtx's cancellation is already winding the sessions down.
	if cerr := own.Close(); cerr != nil { // before lock.Release — see doc comment
		fmt.Fprintf(os.Stderr, "daemon: close owner: %v\n", cerr)
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Second)
	own.WaitIdle(drainCtx)
	drainCancel()

	cancel()
	stopWatch()
	daemon.BoundedStop(result.Scheduler, 3*time.Second)
	saveHeatmapAndClose(dir, result)
	if rerr := lock.Release(); rerr != nil {
		fmt.Fprintf(os.Stderr, "daemon: release lock: %v\n", rerr)
	}
	return stdioErr
}

// saveHeatmapAndClose persists the heat map (when attached) and closes the
// engine — the non-scheduler tail of engineResult.Cleanup. The owner path
// calls it directly so the scheduler stop can be bounded separately and the
// heatmap save never waits on an in-flight LLM regeneration.
func saveHeatmapAndClose(dir string, result *engineResult) {
	if result.HeatMap != nil {
		if saveErr := heatmap.Save(dir, result.HeatMap); saveErr != nil {
			fmt.Fprintf(os.Stderr, "heatmap: save error: %v\n", saveErr)
		}
	}
	_ = result.Engine.Close()
}

// ownerAlive reports whether a live `marmot serve` owner is attached to the
// vault: daemon.info.json is readable and its socket accepts a connection. A
// stale info file (owner SIGKILLed before cleanup) fails the dial and reads
// as "no owner", so standalone behavior is unchanged by leftovers.
func ownerAlive(dir string) (daemon.Info, bool) {
	info, err := daemon.ReadInfo(filepath.Join(dir, ".marmot-data"))
	if err != nil {
		return daemon.Info{}, false
	}
	conn, err := net.Dial("unix", info.Socket)
	if err != nil {
		return daemon.Info{}, false
	}
	_ = conn.Close()
	return info, true
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

	// Suppress the summary scheduler when a live serve owner exists — it runs
	// the vault's single scheduler, and a second one would duplicate LLM calls
	// and race _summary.md writes. UI keeps its scheduler when it is the only
	// marmot process.
	if result.Scheduler != nil {
		if info, alive := ownerAlive(dir); alive {
			fmt.Fprintf(os.Stderr, "summary: scheduler suppressed — vault owner (pid %d) runs it\n", info.PID)
		} else {
			result.Scheduler.Start(ctx)
		}
	}

	// Create API server with embedded frontend assets.
	apiServer := api.NewServer(result.Engine, web.Assets)
	apiServer.WithAppVersion(version)

	// Wire the Graph Curator chat provider. Both OpenAI and Anthropic providers
	// implement llm.ChatProvider; reuse whichever was configured for the
	// classifier so the UI chat works without extra setup.
	if chatProvider, ok := result.LLM.(llm.ChatProvider); ok && chatProvider != nil {
		apiServer.WithLLMChat(chatProvider)
		fmt.Fprintln(os.Stderr, "chat: curator LLM provider wired")
	} else {
		fmt.Fprintln(os.Stderr, "chat: no LLM provider configured — curator slash commands only (run 'marmot configure' to enable NL chat)")
	}

	// Start file watcher for live-reload.
	stopWatcher, watchErr := apiServer.StartWatcher(dir)
	if watchErr != nil {
		fmt.Fprintf(os.Stderr, "live-reload: watcher failed to start: %v\n", watchErr)
	} else {
		defer stopWatcher()
		fmt.Fprintln(os.Stderr, "live-reload: watching vault for changes")
	}

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

	// When filtering by namespace, resolve edge targets against all vault
	// nodes so cross-namespace edges aren't flagged dangling.
	var knownIDs map[string]bool
	if ns != "" {
		knownIDs = make(map[string]bool, len(metas))
		for _, m := range metas {
			knownIDs[m.ID] = true
		}
	}
	issues := verify.VerifyIntegrityScoped(nodes, knownIDs, projectRoot)

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
	// Local vault store: read-write open is intentional (remote vaults go
	// through VaultRegistry, which opens read-only).
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

// runWatchPipeline starts a file watcher that auto-reindexes on changes,
// running until an interrupt signal is received.
func runWatchPipeline(dir string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel the watch loop on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	return watchLoop(ctx, dir)
}

// watchLoop wires up the file watcher and blocks until ctx is cancelled. It is
// separated from signal handling so tests can drive it with a cancellable
// context instead of delivering real process signals.
func watchLoop(ctx context.Context, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist", dir)
	}

	// A live serve owner already watches the vault and reindexes on change;
	// a second watcher is a strict duplicate of that role.
	if info, alive := ownerAlive(dir); alive {
		return fmt.Errorf("vault is served by marmot daemon (pid %d); watch is redundant", info.PID)
	}

	store := node.NewStore(dir)
	g, err := graph.LoadGraph(store)
	if err != nil {
		return fmt.Errorf("load graph: %w", err)
	}

	dbPath := filepath.Join(dir, ".marmot-data", "embeddings.db")
	// Local vault store: read-write open is intentional (remote vaults go
	// through VaultRegistry, which opens read-only).
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

	watcher.Start(ctx)
	fmt.Fprintf(os.Stderr, "Watching %s for changes (Ctrl+C to stop)...\n", dir)

	// Wait for cancellation (signal in production, context in tests).
	<-ctx.Done()

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
			summarizer = llm.NewOpenAIProviderWithModel(key, vaultCfg.ClassifierModel)
		}
	case "anthropic":
		if key := config.APIKeyWithVault("anthropic", dir); key != "" {
			summarizer = llm.NewAnthropicProviderWithModel(key, vaultCfg.ClassifierModel)
		}
	}

	if summarizer == nil {
		return fmt.Errorf("no LLM provider configured; run 'marmot configure' to set up a classifier provider")
	}

	return summarizeWithProvider(dir, ns, summarizer)
}

// summarizeWithProvider generates and writes a namespace summary using the given
// summarizer. It is separated from provider selection so tests can inject a
// summarizer without configuring a real LLM.
func summarizeWithProvider(dir, ns string, summarizer llm.Summarizer) error {
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
	// Local vault store: read-write open is intentional (remote vaults go
	// through VaultRegistry, which opens read-only).
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

	// 7. Create classifier. Always attach one — with the configured LLM when a
	// key is available, otherwise with a nil LLM so the embedding-distance
	// fallback still deduplicates on re-index.
	cls := &classifier.Classifier{
		Store:    embStore,
		Embedder: embedder,
	}
	switch vaultCfg.ClassifierProvider {
	case "openai":
		if key := config.APIKeyWithVault("openai", dir); key != "" {
			p := llm.NewOpenAIProviderWithModel(key, vaultCfg.ClassifierModel)
			cls.LLM = p
			fmt.Fprintln(os.Stderr, "classifier: using openai/"+p.Model())
		} else {
			fmt.Fprintln(os.Stderr, "classifier: openai configured but OPENAI_API_KEY not found; using embedding-distance fallback")
		}
	case "anthropic":
		if key := config.APIKeyWithVault("anthropic", dir); key != "" {
			p := llm.NewAnthropicProviderWithModel(key, vaultCfg.ClassifierModel)
			cls.LLM = p
			fmt.Fprintln(os.Stderr, "classifier: using anthropic/"+p.Model())
		} else {
			fmt.Fprintln(os.Stderr, "classifier: anthropic configured but ANTHROPIC_API_KEY not found; using embedding-distance fallback")
		}
	default:
		fmt.Fprintln(os.Stderr, "classifier: using embedding-distance fallback")
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
	idxClassifier := indexer.Classifier(&classifierAdapter{cls: cls})
	runner := indexer.NewRunner(runnerCfg, registry, nodeStore, embStore, embedder, idxClassifier, g)
	result, err := runner.Run(ctx)
	if err != nil {
		return fmt.Errorf("indexer run: %w", err)
	}

	// 12. Print results, with a diagnostic line for every counted error.
	for _, detail := range result.ErrorDetails {
		fmt.Fprintf(os.Stderr, "index error: %s\n", detail)
	}
	fmt.Printf("Static analysis complete: %s\n", result)
	return nil
}
