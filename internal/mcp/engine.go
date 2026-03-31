// Package mcp provides the MCP (Model Context Protocol) server for ContextMarmot.
// It exposes three tools — context_query, context_write, and context_verify —
// over a stdio JSON-RPC transport for consumption by LLM agents.
package mcp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// Engine wires together all ContextMarmot internal components. It is the
// single dependency injected into every MCP tool handler.
type Engine struct {
	NodeStore      *node.Store
	Graph          *graph.Graph
	EmbeddingStore *embedding.Store
	Embedder       embedding.Embedder
	// MarmotDir is the root .marmot directory.
	MarmotDir string
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

// Close releases resources held by the engine.
func (e *Engine) Close() error {
	if e.EmbeddingStore != nil {
		return e.EmbeddingStore.Close()
	}
	return nil
}
