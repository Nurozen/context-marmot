package indexer

import "sync"

// Registry maps file extensions to Indexer implementations and provides
// lookup by extension.
type Registry struct {
	mu       sync.RWMutex
	indexers map[string]Indexer // extension -> indexer
	generic  Indexer            // catch-all for unrecognised extensions
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		indexers: make(map[string]Indexer),
	}
}

// Register registers an indexer for all of its supported extensions.
// If the indexer's Name() is "generic", it is also stored as the catch-all.
func (r *Registry) Register(idx Indexer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, ext := range idx.SupportedExtensions() {
		r.indexers[ext] = idx
	}
	if idx.Name() == "generic" {
		r.generic = idx
	}
}

// IndexerFor returns the indexer registered for the given file extension.
// If no specific indexer is registered it falls back to the generic indexer
// (if one has been registered). The second return value indicates whether an
// indexer was found.
func (r *Registry) IndexerFor(ext string) (Indexer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if idx, ok := r.indexers[ext]; ok {
		return idx, true
	}
	if r.generic != nil {
		return r.generic, true
	}
	return nil, false
}

// NewDefaultRegistry creates a Registry pre-populated with the Go, TypeScript,
// and Generic indexers. The generic indexer is registered last as a catch-all.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(NewGoIndexer())
	r.Register(NewTypeScriptIndexer())
	r.Register(NewGenericIndexer())
	return r
}
