package api

import (
	"github.com/nurozen/context-marmot/internal/daemon"
)

// StartWatcher watches the vault directory for .md file changes and reloads
// the engine's in-memory graph when changes are detected. Changes are
// debounced — multiple rapid writes are batched into a single reload. It
// delegates to the shared daemon graph watcher (the same logic the serve
// owner runs, including watching directories created after start) and
// notifies all SSE clients after each successful reload.
// The returned function stops the watcher.
func (s *Server) StartWatcher(vaultDir string) (stop func(), err error) {
	return daemon.StartGraphWatcherNotify(vaultDir, s.engine, s.NotifyChange)
}
