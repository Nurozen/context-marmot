package mcp

import (
	"context"
	"io"
	"log"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server is the ContextMarmot MCP server. It wraps an Engine and exposes
// three tools (context_query, context_write, context_verify) over the MCP
// protocol via a stdio JSON-RPC transport.
type Server struct {
	engine    *Engine
	mcpServer *server.MCPServer
}

// NewServer creates an MCP server wired to the given Engine.
func NewServer(engine *Engine) *Server {
	s := &Server{engine: engine}
	s.mcpServer = server.NewMCPServer(
		"context-marmot",
		"0.1.0",
		server.WithToolCapabilities(false),
	)
	s.registerTools()
	return s
}

// registerTools registers the three ContextMarmot MCP tools.
func (s *Server) registerTools() {
	// context_query tool
	queryTool := mcp.NewTool("context_query",
		mcp.WithDescription("Search the project knowledge graph for relevant context. Returns a structured subgraph compacted to fit the token budget."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language query or node ID"),
		),
		mcp.WithNumber("depth",
			mcp.Description("Max traversal depth from entry nodes (default: 2)"),
		),
		mcp.WithNumber("budget",
			mcp.Description("Max token budget for response (default: 4096)"),
		),
		mcp.WithString("mode",
			mcp.Description("Compaction mode (default: adjacency)"),
			mcp.Enum("adjacency", "deep"),
		),
	)

	// context_write tool
	writeTool := mcp.NewTool("context_write",
		mcp.WithDescription("Write or update a node in the knowledge graph. Enforces structural acyclicity."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Node ID (e.g., 'auth/login')"),
		),
		mcp.WithString("type",
			mcp.Required(),
			mcp.Description("Node type"),
			mcp.Enum("function", "module", "class", "concept", "decision", "reference", "composite"),
		),
		mcp.WithString("namespace",
			mcp.Description("Target namespace (ignored in MVP, defaults to 'default')"),
		),
		mcp.WithString("summary",
			mcp.Description("Short description of the node"),
		),
		mcp.WithString("context",
			mcp.Description("Full context content (code, documentation, etc.)"),
		),
		mcp.WithArray("edges",
			mcp.Description("Typed directed edges from this node"),
		),
		mcp.WithObject("source",
			mcp.Description("Source file reference with path, lines, and hash"),
		),
	)

	// context_verify tool
	verifyTool := mcp.NewTool("context_verify",
		mcp.WithDescription("Check node staleness and graph integrity."),
		mcp.WithArray("node_ids",
			mcp.Description("Node IDs to verify. Omit for full verification."),
		),
		mcp.WithString("check",
			mcp.Description("Check type (default: all)"),
			mcp.Enum("staleness", "integrity", "all"),
		),
	)

	s.mcpServer.AddTool(queryTool, s.engine.HandleContextQuery)
	s.mcpServer.AddTool(writeTool, s.engine.HandleContextWrite)
	s.mcpServer.AddTool(verifyTool, s.engine.HandleContextVerify)
}

// ListenStdio starts the MCP server on stdin/stdout. It blocks until the
// context is cancelled or the input stream closes.
func (s *Server) ListenStdio(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	stdio := server.NewStdioServer(s.mcpServer)
	stdio.SetErrorLogger(log.New(io.Discard, "", 0))
	return stdio.Listen(ctx, stdin, stdout)
}

// Serve starts the MCP server on os.Stdin/os.Stdout. Convenience wrapper
// for the common CLI use case.
func (s *Server) Serve(ctx context.Context) error {
	stdio := server.NewStdioServer(s.mcpServer)
	return stdio.Listen(ctx, nil, nil)
}
