package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nurozen/context-marmot/internal/codemode"
	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/llm"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
)

// Server is the HTTP REST API server backed by a ContextMarmot Engine.
type Server struct {
	engine *mcpserver.Engine
	mux    *http.ServeMux
	assets fs.FS // embedded frontend assets (may be nil for API-only mode)

	// Marmot build version (set via ldflags in cmd/marmot); "dev" when unset.
	appVersion string

	// Live-reload: file watcher pushes version bumps to SSE clients.
	version    atomic.Int64
	sseClients sync.Map // map of chan struct{} for each connected SSE client

	// Mutation undo system.
	undoStack *curator.UndoStack

	// LLM chat provider (optional; nil = slash-commands only).
	llmChat llm.ChatProvider

	// Code-mode executor (lazy: built on first chat call).
	codeExecutor *codemode.Executor

	// warnedVaults dedupes best-effort cross-vault degradation warnings so a
	// broken remote vault warns once per vault per process, not per query.
	warnedVaults sync.Map // map[string]bool
}

// warnVaultOnce logs a cross-vault degradation warning to stderr at most
// once per key for this server's lifetime (best-effort search paths would
// otherwise repeat it on every query).
func (s *Server) warnVaultOnce(key, format string, args ...any) {
	if _, loaded := s.warnedVaults.LoadOrStore(key, true); loaded {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}

// NewServer creates a Server wired to the given engine. If assets is non-nil,
// the server also serves an embedded SPA frontend.
func NewServer(engine *mcpserver.Engine, assets fs.FS) *Server {
	s := &Server{
		engine:       engine,
		assets:       assets,
		appVersion:   "dev",
		undoStack:    curator.NewUndoStack(),
		codeExecutor: codemode.NewExecutor(engine),
	}
	s.mux = http.NewServeMux()
	s.registerRoutes()
	return s
}

// registerRoutes wires all API endpoints using Go 1.22+ pattern routing.
func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/graph/_all", s.handleGraphAll)
	s.mux.HandleFunc("GET /api/graph/{namespace}", s.handleGraph)
	s.mux.HandleFunc("GET /api/node/{namespace}/{id...}", s.handleNode)
	s.mux.HandleFunc("PUT /api/node/{id...}", s.handleNodeUpdate)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/heat/{namespace}", s.handleHeat)
	s.mux.HandleFunc("GET /api/namespaces", s.handleNamespaces)
	s.mux.HandleFunc("GET /api/bridges", s.handleBridges)
	s.mux.HandleFunc("GET /api/summary/{namespace}", s.handleSummary)
	s.mux.HandleFunc("GET /api/warrens", s.handleWarrens)
	// GET /api/warren/{id}/status is the canonical status route; the bare
	// GET /api/warren/{id} spelling is kept as a legacy alias (removing a
	// route breaks deployed clients under the auto-release train).
	s.mux.HandleFunc("GET /api/warren/{id}", s.handleWarrenStatus)
	s.mux.HandleFunc("GET /api/warren/{id}/graph", s.handleWarrenGraph)
	s.mux.HandleFunc("GET /api/warren/{id}/status", s.handleWarrenStatus)
	s.mux.HandleFunc("POST /api/warren/{id}/refresh", s.handleWarrenRefresh)
	// Warren workspace management (U5a): mount/unmount reuse the warren
	// layer's flock'd state writes and refusal messages; doctor returns the
	// workspace report verbatim. Deliberately NOT over HTTP:
	// register/unregister (filesystem paths from a browser), burrow
	// --materialize/--drop (heavy IO + cache lifecycle), edit toggle
	// (write-policy change), propose and refresh --pull (git operations).
	s.mux.HandleFunc("POST /api/warren/{id}/mount", s.handleWarrenMount)
	s.mux.HandleFunc("POST /api/warren/{id}/unmount", s.handleWarrenUnmount)
	s.mux.HandleFunc("GET /api/doctor/workspace", s.handleDoctorWorkspace)
	s.mux.HandleFunc("GET /api/events", s.handleSSE)
	s.mux.HandleFunc("GET /api/version", s.handleVersion)
	s.mux.HandleFunc("GET /sdk.ts", s.handleSDKTS)
	s.mux.HandleFunc("POST /api/sdk/{tool}", s.handleSDKCall)
	s.mux.HandleFunc("POST /api/chat", s.handleChat)
	s.mux.HandleFunc("POST /api/chat/undo", s.handleChatUndo)
	s.mux.HandleFunc("GET /api/curator/suggestions", s.handleSuggestions)

	// Serve frontend assets (SPA fallback: serve index.html for non-API, non-asset paths).
	if s.assets != nil {
		subFS, err := fs.Sub(s.assets, "dist")
		if err != nil {
			subFS = s.assets
		}
		fileServer := http.FileServer(http.FS(subFS))
		// Pre-read index.html for SPA fallback (avoids redirect loop with http.FileServer).
		indexHTML, _ := fs.ReadFile(subFS, "index.html")
		s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			// Unknown /api/* paths (or wrong methods on known ones) reach
			// this catch-all — return a JSON 404 instead of serving
			// index.html with status 200, which confused API consumers.
			if path == "/api" || strings.HasPrefix(path, "/api/") {
				writeError(w, http.StatusNotFound, "unknown API endpoint: "+r.Method+" "+path)
				return
			}
			if path == "/" || path == "/index.html" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(indexHTML)
				return
			}
			// Check if the file exists in the embedded FS.
			f, err := subFS.Open(strings.TrimPrefix(path, "/"))
			if err != nil {
				// SPA fallback: serve index.html for client-side routing.
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(indexHTML)
				return
			}
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
		})
	}
}

// Handler returns the root http.Handler with CORS middleware applied.
func (s *Server) Handler() http.Handler {
	return corsMiddleware(s.mux)
}

// ListenAndServe starts the HTTP server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}

// WithLLMChat sets the LLM chat provider. When set, the POST /api/chat
// endpoint supports natural language messages in addition to slash commands.
func (s *Server) WithLLMChat(provider llm.ChatProvider) {
	s.llmChat = provider
}

// WithAppVersion sets the marmot build version surfaced by GET /api/version.
// cmd/marmot threads the ldflags-injected version string through here.
func (s *Server) WithAppVersion(version string) {
	if version != "" {
		s.appVersion = version
	}
}

// corsMiddleware adds permissive CORS headers for local development.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
