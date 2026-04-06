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
	NodeStore      *node.Store
	Graph          *graph.Graph
	EmbeddingStore *embedding.Store
	Embedder       embedding.Embedder
	Classifier       *classifier.Classifier // optional; nil = no CRUD classification
	NSManager        *namespace.Manager     // optional; nil = single-namespace mode
	HeatMap          *heatmap.HeatMap       // optional; nil = no heat-based priority
	SummaryEngine    *summary.Engine        // optional; nil = no summary generation
	UpdateEngine     *update.Engine         // optional; nil = no update detection
	SummaryScheduler *summary.Scheduler     // optional; nil = no async summaries
	VaultRegistry    *namespace.VaultRegistry // optional; nil = single-vault mode
	// MarmotDir is the root .marmot directory.
	MarmotDir    string
	LocalVaultID string // cached from config; avoids repeated disk reads in handlers
	nsMu      sync.Map // map[string]*sync.Mutex — per-namespace write locks
}

// namespaceLock returns the write mutex for the given namespace, creating it if needed.
func (e *Engine) namespaceLock(namespace string) *sync.Mutex {
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	es, err := embedding.NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("engine: open embedding store: %w", err)
	}

	return &Engine{
		NodeStore:      ns,
		Graph:          g,
		EmbeddingStore: es,
		Embedder:       embedder,
		MarmotDir:      marmotDir,
	}, nil
}

// WithHeatMap attaches a heat map to the engine for traversal priority.
func (e *Engine) WithHeatMap(h *heatmap.HeatMap) {
	e.HeatMap = h
}

// WithNamespaceManager attaches a namespace manager to the engine.
// When set, cross-namespace edges are validated against bridge manifests.
func (e *Engine) WithNamespaceManager(mgr *namespace.Manager) {
	e.NSManager = mgr
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
	// Cache local vault ID to avoid repeated disk reads in handlers.
	if e.MarmotDir != "" {
		if cfg, err := config.Load(e.MarmotDir); err == nil {
			e.LocalVaultID = cfg.VaultID
		}
	}
}

// graphResolver returns a GraphResolver — either bridged (cross-vault) or plain local.
func (e *Engine) graphResolver() traversal.GraphResolver {
	if e.VaultRegistry != nil {
		return &traversal.BridgedGraphResolver{
			Local:  e.Graph,
			Vaults: e.VaultRegistry,
		}
	}
	return e.Graph
}

// ResolveNodeID looks up a node by ID in the graph. If not found and a
// namespace manager is available, it tries prefixing each known namespace
// (e.g. "render/api-client" → "hl-warde/render/api-client"). This handles
// the common case where LLMs omit the namespace prefix from node IDs.
func (e *Engine) ResolveNodeID(id string) (*node.Node, bool) {
	if n, ok := e.Graph.GetNode(id); ok {
		return n, true
	}
	if e.NSManager != nil {
		for nsName := range e.NSManager.Namespaces {
			prefixed := nsName + "/" + id
			if n, ok := e.Graph.GetNode(prefixed); ok {
				return n, true
			}
		}
	}
	return nil, false
}

// Close releases resources held by the engine.
func (e *Engine) Close() error {
	if e.EmbeddingStore != nil {
		return e.EmbeddingStore.Close()
	}
	return nil
}
