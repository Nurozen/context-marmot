// Package mcp provides the MCP (Model Context Protocol) server for ContextMarmot.
// It exposes three tools — context_query, context_write, and context_verify —
// over a stdio JSON-RPC transport for consumption by LLM agents.
package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nurozen/context-marmot/internal/classifier"
	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/summary"
	"github.com/nurozen/context-marmot/internal/traversal"
	"github.com/nurozen/context-marmot/internal/update"
)

// Engine wires together all ContextMarmot internal components. It is the
// single dependency injected into every MCP tool handler.
type Engine struct {
	NodeStore        *node.Store
	graph            atomic.Pointer[graph.Graph]
	EmbeddingStore   *embedding.Store
	Embedder         embedding.Embedder
	Classifier       *classifier.Classifier   // optional; nil = no CRUD classification
	NSManager        *namespace.Manager       // optional; nil = single-namespace mode
	HeatMap          *heatmap.HeatMap         // optional; nil = no heat-based priority
	SummaryEngine    *summary.Engine          // optional; nil = no summary generation
	UpdateEngine     *update.Engine           // optional; nil = no update detection
	SummaryScheduler *summary.Scheduler       // optional; nil = no async summaries
	VaultRegistry    *namespace.VaultRegistry // optional; nil = single-vault mode
	// MarmotDir is the root .marmot directory.
	MarmotDir    string
	LocalVaultID string   // cached from config; avoids repeated disk reads in handlers
	nsMu         sync.Map // map[string]*sync.Mutex — per-namespace write locks
	nsMgrMu      sync.RWMutex
	// fileCrossVaultBridges snapshots the manager's file-declared cross-vault
	// bridges at WithNamespaceManager time; warrenBridges holds the current
	// warren runtime bridges. ReloadWarrenState recomposes
	// NSManager.CrossVaultBridges = fileCrossVaultBridges ++ warrenBridges so
	// repeated reloads never duplicate. Both guarded by nsMgrMu.
	fileCrossVaultBridges []*namespace.Bridge
	warrenBridges         []*namespace.Bridge
	// reloadMu serializes ReloadWarrenState: it is invoked concurrently from
	// HTTP handler goroutines (the refresh endpoint) and the daemon owner's
	// _warren.md watcher, and each reload is a read-state-then-apply cycle.
	// Unserialized, a reload that read PRE-change state could apply its stale
	// routing table AFTER the reload that read post-change state, leaving
	// e.g. an unmounted vault routable until the next reload, with NSManager
	// bridges and the registry routing table from two different snapshots.
	reloadMu sync.Mutex
	// warnedVaults dedupes best-effort cross-vault degradation warnings so a
	// broken remote vault warns once per vault per process, not per query.
	warnedVaults  sync.Map       // map[string]bool
	reindexWG     sync.WaitGroup // tracks background neighbor reindexes
	closing       atomic.Bool    // set by Close; stops new background reindexes
	reindexOnce   sync.Once      // lazily initializes the reindex context
	reindexCtx    context.Context
	reindexCancel context.CancelFunc
}

// reindexContext returns the shared parent context for background reindexes,
// creating it on first use so zero-value Engines work too. Close cancels it
// so shutdown never waits out a reindex's 30s timeout.
func (e *Engine) reindexContext() context.Context {
	e.reindexOnce.Do(func() {
		e.reindexCtx, e.reindexCancel = context.WithCancel(context.Background())
	})
	return e.reindexCtx
}

// SetGraph atomically replaces the in-memory graph.
func (e *Engine) SetGraph(g *graph.Graph) {
	e.graph.Store(g)
}

// GetGraph returns the current in-memory graph.
func (e *Engine) GetGraph() *graph.Graph {
	return e.graph.Load()
}

// warnVaultOnce logs a cross-vault degradation warning to stderr at most
// once per vault for this engine's lifetime (per-query warnings on the
// best-effort search path would be too chatty for a long-lived daemon).
func (e *Engine) warnVaultOnce(vaultID, format string, args ...any) {
	if _, loaded := e.warnedVaults.LoadOrStore(vaultID, true); loaded {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}

// NamespaceLock returns the write mutex for the given namespace, creating it if needed.
func (e *Engine) NamespaceLock(namespace string) *sync.Mutex {
	v, _ := e.nsMu.LoadOrStore(namespace, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// defaultTokenBudget returns the token budget from the vault config, falling
// back to config.DefaultTokenBudget if the config cannot be loaded.
func (e *Engine) defaultTokenBudget() int {
	if e.MarmotDir == "" {
		return config.DefaultTokenBudget
	}
	cfg, err := config.Load(e.MarmotDir)
	if err != nil {
		return config.DefaultTokenBudget
	}
	return cfg.EffectiveTokenBudget()
}

// reindexNeighbors kicks off a background reindex of nodes directly connected
// to the given node ID. This keeps the graph fresh after MCP writes/deletes
// without blocking the response. Only runs if UpdateEngine is wired in.
func (e *Engine) reindexNeighbors(nodeID string) {
	if e.UpdateEngine == nil {
		return
	}

	// Propagate staleness one hop from the changed node to find affected neighbors.
	affected := e.UpdateEngine.PropagateStale([]string{nodeID}, 1)

	// Collect neighbor IDs. The source node (depth=0) is skipped because it
	// was just written/deleted by the handler with a fresh hash and embedding;
	// we only need to refresh nodes that *depend on* it (depth=1 via inbound edges).
	var neighborIDs []string
	for _, a := range affected {
		if a.Depth > 0 {
			neighborIDs = append(neighborIDs, a.NodeID)
		}
	}
	if len(neighborIDs) == 0 {
		return
	}

	// Run reindex in the background so the MCP response returns immediately.
	// The WaitGroup lets Close drain in-flight reindexes before releasing the
	// embedding store, preventing a use-after-close on shutdown.
	if e.closing.Load() {
		return
	}
	parent := e.reindexContext()
	e.reindexWG.Add(1)
	go func() {
		defer e.reindexWG.Done()
		if e.closing.Load() {
			return
		}
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		_ = e.UpdateEngine.Reindex(ctx, neighborIDs)
	}()
}

// NewEngine creates an Engine rooted at marmotDir, using the provided embedder
// for vector operations. It initialises the node store, loads the in-memory
// graph, and opens (or creates) the embedding store.
func NewEngine(marmotDir string, embedder embedding.Embedder) (*Engine, error) {
	// Ensure the marmot directory exists.
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		return nil, fmt.Errorf("engine: create marmot dir: %w", err)
	}

	// Node store rooted at the marmot directory.
	ns := node.NewStore(marmotDir)

	// Load graph from existing node files.
	g, err := graph.LoadGraph(ns)
	if err != nil {
		return nil, fmt.Errorf("engine: load graph: %w", err)
	}

	// Embedding store in .marmot-data/embeddings.db.
	dataDir := filepath.Join(marmotDir, ".marmot-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("engine: create data dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "embeddings.db")
	// Read-write open is correct here: this is the LOCAL vault's own store
	// (remote vault stores are opened read-only via the VaultRegistry).
	es, err := embedding.NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("engine: open embedding store: %w", err)
	}

	eng := &Engine{
		NodeStore:      ns,
		EmbeddingStore: es,
		Embedder:       embedder,
		MarmotDir:      marmotDir,
	}
	eng.SetGraph(g)
	return eng, nil
}

// WithHeatMap attaches a heat map to the engine for traversal priority.
func (e *Engine) WithHeatMap(h *heatmap.HeatMap) {
	e.HeatMap = h
}

// WithNamespaceManager attaches a namespace manager to the engine.
// When set, cross-namespace edges are validated against bridge manifests.
// The manager's file-declared cross-vault bridges are snapshotted so
// ReloadWarrenState can recompose them with warren runtime bridges without
// duplicating either.
func (e *Engine) WithNamespaceManager(mgr *namespace.Manager) {
	e.nsMgrMu.Lock()
	defer e.nsMgrMu.Unlock()
	e.NSManager = mgr
	e.fileCrossVaultBridges = nil
	if mgr != nil {
		e.fileCrossVaultBridges = append([]*namespace.Bridge(nil), mgr.CrossVaultBridges...)
	}
}

// HasNamespace reports whether the namespace manager knows a namespace.
func (e *Engine) HasNamespace(name string) bool {
	e.nsMgrMu.RLock()
	defer e.nsMgrMu.RUnlock()
	if e.NSManager == nil {
		return false
	}
	_, ok := e.NSManager.Namespaces[name]
	return ok
}

// NamespaceNames returns namespace names currently known to the manager.
func (e *Engine) NamespaceNames() []string {
	e.nsMgrMu.RLock()
	defer e.nsMgrMu.RUnlock()
	if e.NSManager == nil {
		return nil
	}
	names := make([]string, 0, len(e.NSManager.Namespaces))
	for name := range e.NSManager.Namespaces {
		names = append(names, name)
	}
	return names
}

// BridgeSnapshot returns copies of bridge slices currently known to the manager.
func (e *Engine) BridgeSnapshot() ([]*namespace.Bridge, []*namespace.Bridge) {
	e.nsMgrMu.RLock()
	defer e.nsMgrMu.RUnlock()
	if e.NSManager == nil {
		return nil, nil
	}
	bridges := make([]*namespace.Bridge, 0, len(e.NSManager.Bridges))
	for _, b := range e.NSManager.Bridges {
		bridges = append(bridges, b)
	}
	crossVault := append([]*namespace.Bridge(nil), e.NSManager.CrossVaultBridges...)
	return bridges, crossVault
}

func (e *Engine) validateCrossNamespaceEdges(edges []node.Edge, currentNamespace string) error {
	e.nsMgrMu.RLock()
	defer e.nsMgrMu.RUnlock()
	if e.NSManager == nil {
		return nil
	}
	for _, edge := range edges {
		qid := e.NSManager.ParseQualifiedID(edge.Target, currentNamespace)
		if qid.VaultID != "" {
			continue
		}
		if qid.Namespace != currentNamespace {
			if err := e.NSManager.ValidateCrossNamespaceEdge(currentNamespace, qid.Namespace, string(edge.Relation)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) validateCrossVaultEdges(edges []node.Edge, currentNamespace string) error {
	e.nsMgrMu.RLock()
	defer e.nsMgrMu.RUnlock()
	if e.NSManager == nil || e.VaultRegistry == nil || e.LocalVaultID == "" {
		return nil
	}
	for _, edge := range edges {
		qid := e.NSManager.ParseQualifiedID(edge.Target, currentNamespace)
		// A "@<LocalVaultID>/x" target is a local edge wearing a costume:
		// it resolves against the live vault, so it needs no
		// LocalVaultID<->LocalVaultID bridge.
		if qid.VaultID != "" && qid.VaultID != e.LocalVaultID {
			if err := e.NSManager.ValidateCrossVaultEdge(e.LocalVaultID, qid.VaultID, string(edge.Relation)); err != nil {
				return err
			}
		}
	}
	return nil
}

// WithLLMClassifier wires up a CRUD classifier on the engine using the
// engine's own EmbeddingStore and Embedder, plus an optional LLM provider.
// Pass nil for llmProvider to use the pure-embedding fallback path.
func (e *Engine) WithLLMClassifier(llmProvider llm.Provider) {
	e.Classifier = &classifier.Classifier{
		Store:    e.EmbeddingStore,
		Embedder: e.Embedder,
		LLM:      llmProvider, // may be nil for embedding-distance fallback
	}
}

// WithSummaryEngine attaches a summary engine to the MCP engine.
func (e *Engine) WithSummaryEngine(se *summary.Engine) {
	e.SummaryEngine = se
}

// WithUpdateEngine attaches an update engine to the MCP engine.
func (e *Engine) WithUpdateEngine(ue *update.Engine) {
	e.UpdateEngine = ue
}

// WithSummaryScheduler attaches a summary scheduler to the MCP engine.
func (e *Engine) WithSummaryScheduler(ss *summary.Scheduler) {
	e.SummaryScheduler = ss
}

// WithVaultRegistry attaches a vault registry for cross-vault traversal.
func (e *Engine) WithVaultRegistry(vr *namespace.VaultRegistry) {
	e.VaultRegistry = vr
	// Cache local vault ID to avoid repeated disk reads in handlers. An
	// unreadable config leaves LocalVaultID empty, silently disabling every
	// alias guard and cross-vault edge validation — degrade loudly.
	if e.MarmotDir != "" {
		if cfg, err := config.Load(e.MarmotDir); err == nil {
			e.LocalVaultID = cfg.VaultID
		} else {
			fmt.Fprintf(os.Stderr, "warning: local vault config unreadable (%v); vault_id unknown — local identity (self-mount aliasing) and cross-vault edge validation disabled\n", err)
		}
	}
}

// graphResolver returns a GraphResolver — either bridged (cross-vault) or plain local.
func (e *Engine) graphResolver() traversal.GraphResolver {
	if e.VaultRegistry != nil {
		return &traversal.BridgedGraphResolver{
			Local:        e.GetGraph(),
			Vaults:       e.VaultRegistry,
			LocalVaultID: e.LocalVaultID,
		}
	}
	return e.GetGraph()
}

// ResolveNodeID looks up a node by ID in the graph. If not found and a
// namespace manager is available, it tries prefixing each known namespace
// (e.g. "render/api-client" → "hl-warde/render/api-client"). This handles
// the common case where LLMs omit the namespace prefix from node IDs.
func (e *Engine) ResolveNodeID(id string) (*node.Node, bool) {
	g := e.GetGraph()
	if n, ok := g.GetNode(id); ok {
		return n, true
	}
	for _, nsName := range e.NamespaceNames() {
		prefixed := nsName + "/" + id
		if n, ok := g.GetNode(prefixed); ok {
			return n, true
		}
	}
	return nil, false
}

// Close releases resources held by the engine. It cancels and waits for
// in-flight background reindexes so the embedding store is not closed under
// them, without waiting out their 30s timeout.
func (e *Engine) Close() error {
	e.closing.Store(true)
	e.reindexContext() // ensure reindexCancel is initialized
	e.reindexCancel()
	e.reindexWG.Wait()
	if e.EmbeddingStore != nil {
		if err := e.EmbeddingStore.Close(); err != nil {
			return err
		}
	}
	if e.VaultRegistry != nil {
		e.VaultRegistry.Close()
	}
	return nil
}
