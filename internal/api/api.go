package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
)

// Server is the HTTP REST API server backed by a ContextMarmot Engine.
type Server struct {
	engine *mcpserver.Engine
	mux    *http.ServeMux
	assets fs.FS // embedded frontend assets (may be nil for API-only mode)

	// Live-reload: file watcher pushes version bumps to SSE clients.
	version    atomic.Int64
	sseClients sync.Map // map of chan struct{} for each connected SSE client
}

// NewServer creates a Server wired to the given engine. If assets is non-nil,
// the server also serves an embedded SPA frontend.
func NewServer(engine *mcpserver.Engine, assets fs.FS) *Server {
	s := &Server{engine: engine, assets: assets}
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
	s.mux.HandleFunc("GET /api/events", s.handleSSE)
	s.mux.HandleFunc("GET /api/version", s.handleVersion)
	s.mux.HandleFunc("GET /sdk.ts", s.handleSDKTS)
	s.mux.HandleFunc("POST /api/sdk/{tool}", s.handleSDKCall)

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
