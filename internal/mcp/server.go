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

// registerTools registers the ContextMarmot MCP tools.
func (s *Server) registerTools() {
	// context_query tool
	queryTool := mcp.NewTool("context_query",
		mcp.WithDescription(`Search the project knowledge graph for relevant context.

WHEN TO USE: Before reading files directly, query the graph to find relevant code, decisions, and architecture context. This is faster and more targeted than file exploration.

HOW IT WORKS: Your query is embedded and matched against node summaries via semantic search. The top matches become entry points for graph traversal, which follows edges to pull in related nodes. The result is compacted XML that fits your token budget.

TIPS:
- Use natural language queries describing what you need ("how does authentication work", "database connection pooling strategy")
- Increase depth to follow more relationship hops from entry nodes
- Increase budget if you need more detail; decrease it for a quick overview
- Results include full context for the closest matches and compact summaries for neighbors`),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language query describing what context you need, or a specific node ID"),
		),
		mcp.WithNumber("depth",
			mcp.Description("How many relationship hops to follow from entry nodes. 1 = entry nodes only, 2 = entry + direct neighbors (default: 2), 3+ = broader context"),
		),
		mcp.WithNumber("budget",
			mcp.Description("Max token budget for the response. Use 2000-4000 for quick lookups, 8000-16000 for deep exploration. Omit to use vault config default (token_budget)."),
		),
		mcp.WithString("mode",
			mcp.Description("Compaction mode. 'adjacency' (default) expands neighbors breadth-first; 'deep' follows call chains depth-first"),
			mcp.Enum("adjacency", "deep"),
		),
		mcp.WithBoolean("include_superseded",
			mcp.Description("If true, include superseded and archived nodes in results. Default false — only active nodes are returned."),
		),
	)

	// context_write tool
	writeTool := mcp.NewTool("context_write",
		mcp.WithDescription(`Write or update a node in the project knowledge graph.

WHEN TO USE: Record important context that would help you or future agents understand the codebase. Write a node when you:
- Discover how a system works (architecture, data flow, key patterns)
- Make or learn about a design decision and its rationale
- Identify a function, class, or module worth remembering
- Find a non-obvious relationship between components
- Encounter something that took effort to understand

WHEN NOT TO USE: Don't create nodes for trivial facts derivable from reading the code directly (e.g., "this file has 50 lines"). Focus on context that saves future effort.

NODE DESIGN GUIDELINES:
- One concept per node. If you're describing two distinct things, make two nodes with edges between them.
- The 'summary' field is critical — it's what gets embedded for semantic search. Write it as a clear, searchable sentence that distinguishes this node from similar ones. Bad: "auth module". Good: "JWT-based authentication module that validates tokens via RS256 and manages session lifecycle".
- The 'context' field holds the full detail: code snippets, documentation, decision rationale, examples. Be thorough here.
- Use 'source' to link back to the actual file and line range so the node can be verified against the code later.
- IDs should be hierarchical paths reflecting the project structure (e.g., 'auth/login', 'db/connection_pool', 'decisions/chose_postgres').

EDGE GUIDELINES:
- Structural edges (contains, imports, extends, implements) define hierarchy and must not form cycles.
- Behavioral edges (calls, reads, writes, references) describe runtime relationships and may form cycles.
- Always add edges to related nodes you know exist. The graph's value comes from connections, not isolated nodes.
- Prefer specific relations: 'calls' over 'references' when one function invokes another; 'contains' for parent-child module relationships.

When referencing nodes in other namespaces, use qualified IDs (e.g., 'other-namespace/node/path'). Cross-namespace edges require a bridge manifest with the relation type whitelisted.
When referencing nodes in other vaults, use @vault-id/node-id format. Cross-vault edges require a cross-vault bridge with the relation type whitelisted.`),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Hierarchical node ID using '/' separators mirroring project structure. Examples: 'auth/login', 'db/users', 'decisions/caching_strategy'. Must not contain '..' or start with '/'"),
		),
		mcp.WithString("type",
			mcp.Required(),
			mcp.Description("Node type. Use 'function' for individual functions/methods, 'module' for packages/directories, 'class' for classes/interfaces, 'concept' for architectural patterns or domain concepts, 'decision' for design decisions and their rationale, 'reference' for external docs or API references, 'composite' for aggregate nodes spanning multiple concerns"),
			mcp.Enum("function", "module", "class", "concept", "decision", "reference", "composite"),
		),
		mcp.WithString("namespace",
			mcp.Description("Target namespace (defaults to 'default')"),
		),
		mcp.WithString("summary",
			mcp.Description("A clear, searchable 1-2 sentence description. This gets embedded for semantic search — make it specific and distinguishing. Include key terms someone would search for. Bad: 'handles auth'. Good: 'Validates JWT access tokens using RS256, checks expiry and audience claims, returns decoded user ID on success'"),
		),
		mcp.WithString("context",
			mcp.Description("Full context: code snippets, documentation, decision rationale, examples, gotchas, or anything that helps understand this node deeply. Use markdown formatting. Be thorough — this is what gets returned when the node is a top search result"),
		),
		mcp.WithArray("edges",
			mcp.Description("Relationships to other nodes. Structural edges (contains, imports, extends, implements) must not form cycles. Behavioral edges (calls, reads, writes, references, associated) may cycle. Always connect to related nodes you know exist"),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{
						"type":        "string",
						"description": "Target node ID. Must be a valid node ID that exists or will exist in the graph",
					},
					"relation": map[string]any{
						"type":        "string",
						"description": "Structural: 'contains' (parent has child), 'imports' (depends on), 'extends' (inherits from), 'implements' (fulfills interface). Behavioral: 'calls' (invokes at runtime), 'reads' (consumes data from), 'writes' (produces data to), 'references' (general reference), 'cross_project' (link to another namespace), 'associated' (related but no specific direction)",
						"enum":        []string{"contains", "imports", "extends", "implements", "calls", "reads", "writes", "references", "cross_project", "associated"},
					},
				},
				"required": []string{"target", "relation"},
			}),
		),
		mcp.WithObject("source",
			mcp.Description("Link to the source file this node describes. Enables staleness detection — if the source changes, the node can be flagged for review"),
			mcp.Properties(map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path relative to the project root (e.g., 'src/auth/login.ts')",
				},
				"lines": map[string]any{
					"type":        "array",
					"description": "[start_line, end_line] range within the file. Omit for whole-file nodes",
					"items":       map[string]any{"type": "number"},
				},
				"hash": map[string]any{
					"type":        "string",
					"description": "SHA-256 hash of the source content for staleness detection. Omit if unknown — the verifier will compute it",
				},
			}),
		),
	)

	// context_verify tool
	verifyTool := mcp.NewTool("context_verify",
		mcp.WithDescription(`Check the health of nodes in the knowledge graph.

WHEN TO USE:
- After modifying source files, run staleness checks to find nodes whose source has changed
- Before relying on graph context for a critical task, verify integrity to catch dangling edges or broken references
- Periodically as a maintenance check to keep the graph accurate

CHECKS:
- 'staleness': Compares stored source hashes against current files. Flags nodes whose source code has changed since the node was written.
- 'integrity': Detects dangling edges (pointing to non-existent nodes), structural cycles, and missing source files.
- 'all': Runs both checks (default).`),
		mcp.WithArray("node_ids",
			mcp.Description("Specific node IDs to check. Omit to verify the entire graph"),
			mcp.WithStringItems(),
		),
		mcp.WithString("check",
			mcp.Description("Which checks to run: 'staleness' (source hash comparison), 'integrity' (dangling edges, cycles, missing files), or 'all' (both)"),
			mcp.Enum("staleness", "integrity", "all"),
		),
	)

	// context_delete tool
	deleteTool := mcp.NewTool("context_delete",
		mcp.WithDescription(`Soft-delete (supersede) a node in the knowledge graph.

WHEN TO USE: When a node's content is no longer accurate because the source code changed or a new node replaces it. Superseded nodes are excluded from future queries by default but retained for historical reference.

This does NOT permanently delete the node file. The node is marked with status="superseded" and a valid_until timestamp. Use context_write to create the replacement node first, then call context_delete with its ID in superseded_by.

Always await the response from context_write before calling context_delete — do not batch them in the same request.`),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("ID of the node to supersede"),
		),
		mcp.WithString("superseded_by",
			mcp.Description("ID of the node that replaces this one. Omit if retiring without a replacement."),
		),
	)

	s.mcpServer.AddTool(queryTool, s.engine.HandleContextQuery)
	s.mcpServer.AddTool(writeTool, s.engine.HandleContextWrite)
	s.mcpServer.AddTool(verifyTool, s.engine.HandleContextVerify)
	s.mcpServer.AddTool(deleteTool, s.engine.HandleContextDelete)
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
