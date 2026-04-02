// Package mcp provides the MCP (Model Context Protocol) server for ContextMarmot.
// It exposes three tools — context_query, context_write, and context_verify —
// over a stdio JSON-RPC transport for consumption by LLM agents.
package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/nurozen/context-marmot/internal/classifier"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/node"
)

// Engine wires together all ContextMarmot internal components. It is the
// single dependency injected into every MCP tool handler.
type Engine struct {
	NodeStore      *node.Store
	Graph          *graph.Graph
	EmbeddingStore *embedding.Store
	Embedder       embedding.Embedder
	Classifier     *classifier.Classifier // optional; nil = no CRUD classification
	NSManager      *namespace.Manager    // optional; nil = single-namespace mode
	HeatMap        *heatmap.HeatMap      // optional; nil = no heat-based priority
	// MarmotDir is the root .marmot directory.
	MarmotDir string
	nsMu      sync.Map // map[string]*sync.Mutex — per-namespace write locks
}

// namespaceLock returns the write mutex for the given namespace, creating it if needed.
func (e *Engine) namespaceLock(namespace string) *sync.Mutex {
	v, _ := e.nsMu.LoadOrStore(namespace, &sync.Mutex{})
	return v.(*sync.Mutex)
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

// Close releases resources held by the engine.
func (e *Engine) Close() error {
	if e.EmbeddingStore != nil {
		return e.EmbeddingStore.Close()
	}
	return nil
}
