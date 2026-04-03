// Package indexer provides static analysis indexing for source code, extracting
// structured entities (packages, functions, types, etc.) and their relationships
// into ContextMarmot knowledge-graph nodes.
package indexer

// SourceEntity represents a code entity extracted from source analysis.
type SourceEntity struct {
	// ID is the node ID derived from the entity's location (e.g., "internal/auth/Login").
	ID string
	// Type is the node type: "package", "function", "method", "type", "interface", "variable", "module", "file".
	Type string
	// Name is the short, unqualified name (e.g., "Login").
	Name string
	// Summary is an auto-generated one-line description of the entity.
	Summary string
	// Context is the source code content for the entity.
	Context string
	// Source locates the entity in the original file.
	Source SourceRef
	// Edges are the relationships this entity has to other entities.
	Edges []EntityEdge
}

// SourceRef identifies the location and content hash of a source entity.
type SourceRef struct {
	// Path is the absolute file path.
	Path string
	// Lines is the 1-indexed, inclusive line range [start, end].
	Lines [2]int
	// Hash is the SHA-256 of the source content at those lines.
	Hash string
}

// EntityEdge represents a directed relationship from one entity to another.
type EntityEdge struct {
	// Target is the target entity ID.
	Target string
	// Relation describes the edge type: "contains", "imports", "calls", "extends", "implements", "references".
	Relation string
}

// IndexResult holds all entities extracted from a single file.
type IndexResult struct {
	Entities []SourceEntity
}

// Indexer parses source files and extracts entities with relationships.
type Indexer interface {
	// IndexFile parses a single source file and returns extracted entities.
	// path is the absolute file path; relPath is the path relative to the source root;
	// namespace is the target namespace for generated node IDs.
	IndexFile(path string, relPath string, namespace string) (*IndexResult, error)

	// SupportedExtensions returns the file extensions this indexer handles (e.g., ".go", ".ts").
	SupportedExtensions() []string

	// Name returns a human-readable name for this indexer (e.g., "go", "typescript").
	Name() string
}
