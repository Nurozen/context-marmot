package graph

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/nurozen/context-marmot/internal/node"
)

// LoadGraph loads all node markdown files from the given store into a new
// Graph. Files that fail to parse are skipped with a logged warning.
func LoadGraph(store *node.Store) (*Graph, error) {
	g := NewGraph()

	basePath := store.BasePath()

	err := filepath.Walk(basePath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && path != basePath {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip underscore-prefixed files (e.g., _summary.md).
		if strings.HasPrefix(info.Name(), "_") {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		n, err := store.LoadNode(path)
		if err != nil {
			log.Printf("graph: skipping %s: %v", path, err)
			return nil
		}

		if err := g.AddNode(n); err != nil {
			log.Printf("graph: skipping node %q from %s: %v", n.ID, path, err)
			return nil
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load graph: %w", err)
	}

	return g, nil
}
