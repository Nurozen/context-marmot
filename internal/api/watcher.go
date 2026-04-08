package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// StartWatcher watches the vault directory for .md file changes and reloads
// the engine's in-memory graph when changes are detected. Changes are
// debounced — multiple rapid writes are batched into a single reload.
// The returned function stops the watcher.
func (s *Server) StartWatcher(vaultDir string) (stop func(), err error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	// Watch the vault dir and all immediate subdirectories (where node .md files live).
	if err := fw.Add(vaultDir); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("watch %q: %w", vaultDir, err)
	}
	entries, _ := os.ReadDir(vaultDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip hidden and system dirs.
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		subDir := filepath.Join(vaultDir, name)
		_ = fw.Add(subDir) // best-effort
	}

	stopCh := make(chan struct{})
	go func() {
		const debounce = 1 * time.Second
		var timer *time.Timer
		pending := false

		for {
			select {
			case <-stopCh:
				if timer != nil {
					timer.Stop()
				}
				_ = fw.Close()
				return
			case event, ok := <-fw.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
					continue
				}
				// Only react to .md files.
				if !strings.HasSuffix(event.Name, ".md") {
					continue
				}
				// Ignore underscore-prefixed files (_config.md, _summary.md, etc.)
				base := filepath.Base(event.Name)
				if strings.HasPrefix(base, "_") {
					continue
				}
				if !pending {
					pending = true
					timer = time.NewTimer(debounce)
				}
			case _, ok := <-fw.Errors:
				if !ok {
					return
				}
			case <-func() <-chan time.Time {
				if timer != nil {
					return timer.C
				}
				return nil
			}():
				pending = false
				s.reloadGraph(vaultDir)
			}
		}
	}()

	return func() { close(stopCh) }, nil
}

// reloadGraph re-reads all nodes from disk and replaces the engine's
// in-memory graph, then notifies all SSE clients.
func (s *Server) reloadGraph(vaultDir string) {
	store := node.NewStore(vaultDir)
	newGraph, err := graph.LoadGraph(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live-reload: failed to reload graph: %v\n", err)
		return
	}
	s.engine.Graph = newGraph
	fmt.Fprintf(os.Stderr, "live-reload: graph reloaded (%d nodes)\n", len(newGraph.AllNodes()))
	s.NotifyChange()
}
