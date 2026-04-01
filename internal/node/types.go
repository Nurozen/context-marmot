// Package node provides the file I/O layer for reading, writing, and parsing
// Obsidian-compatible markdown node files used by ContextMarmot.
package node

// NodeStatus values for Node.Status field.
const (
	StatusActive     = "active"
	StatusSuperseded = "superseded"
	StatusArchived   = "archived"
)

// EdgeClass distinguishes structural edges (DAG-enforced) from behavioral
// edges (cycles allowed).
type EdgeClass string

const (
	Structural EdgeClass = "structural"
	Behavioral EdgeClass = "behavioral"
)

// EdgeRelation enumerates the supported directed-edge relation types.
type EdgeRelation string

const (
	// Structural relations (acyclicity enforced).
	Contains   EdgeRelation = "contains"
	Imports    EdgeRelation = "imports"
	Extends    EdgeRelation = "extends"
	Implements EdgeRelation = "implements"

	// Behavioral relations (cycles allowed).
	Calls        EdgeRelation = "calls"
	Reads        EdgeRelation = "reads"
	Writes       EdgeRelation = "writes"
	References   EdgeRelation = "references"
	CrossProject EdgeRelation = "cross_project"
	Associated   EdgeRelation = "associated"
)

// structuralRelations is the set of relation types that are structural.
var structuralRelations = map[EdgeRelation]bool{
	Contains:   true,
	Imports:    true,
	Extends:    true,
	Implements: true,
}

// ClassifyRelation returns the EdgeClass for the given relation string.
// Unknown relations default to Behavioral.
func ClassifyRelation(relation string) EdgeClass {
	if structuralRelations[EdgeRelation(relation)] {
		return Structural
	}
	return Behavioral
}

// Edge represents a directed, typed edge from the containing node to
// another node.
type Edge struct {
	Target   string       `yaml:"target"`
	Relation EdgeRelation `yaml:"relation"`
	Class    EdgeClass    `yaml:"-"` // derived, not serialized
}

// Source locates the original source code that a node was derived from.
type Source struct {
	Path  string `yaml:"path,omitempty"`
	Lines [2]int `yaml:"lines,omitempty,flow"`
	Hash  string `yaml:"hash,omitempty"`
}

// Node is the primary knowledge-graph entity stored as an Obsidian-compatible
// markdown file with YAML frontmatter.
type Node struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	Namespace string `yaml:"namespace"`
	Status      string `yaml:"status"`
	ValidFrom   string `yaml:"valid_from,omitempty"`    // RFC3339 timestamp, set on creation
	ValidUntil  string `yaml:"valid_until,omitempty"`   // RFC3339 timestamp, set on soft-delete/supersede
	SupersededBy string `yaml:"superseded_by,omitempty"` // node ID of the node that replaces this one
	Source      Source `yaml:"source,omitempty"`
	Edges     []Edge `yaml:"edges,omitempty"`

	// Body sections (not in YAML frontmatter).
	Summary string `yaml:"-"`
	Context string `yaml:"-"`
	RawBody string `yaml:"-"`
}

// IsActive returns true if the node has an active status (or no status set).
func (n *Node) IsActive() bool {
	return n.Status == "" || n.Status == StatusActive
}

// NodeMeta is a lightweight projection of Node used for directory listings
// where only the frontmatter identification fields are needed.
type NodeMeta struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	Namespace string `yaml:"namespace"`
	Status    string `yaml:"status"`
}
