package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ValidateNodeID checks that a node ID is safe for use as a file path
// component. It rejects IDs that could cause path traversal or contain
// problematic characters.
func ValidateNodeID(id string) error {
	if id == "" {
		return fmt.Errorf("node ID must not be empty")
	}
	if len(id) > 512 {
		return fmt.Errorf("node ID exceeds maximum length of 512 characters")
	}

	// Reject path traversal sequences.
	cleaned := filepath.ToSlash(id)
	if strings.Contains(cleaned, "..") {
		return fmt.Errorf("node ID %q contains path traversal sequence", id)
	}

	// Reject absolute paths.
	if filepath.IsAbs(id) || strings.HasPrefix(cleaned, "/") {
		return fmt.Errorf("node ID %q must not be an absolute path", id)
	}

	// Reject null bytes.
	if strings.ContainsRune(id, 0) {
		return fmt.Errorf("node ID %q contains null byte", id)
	}

	// Reject IDs that start with a dot (hidden files/directories).
	parts := strings.Split(cleaned, "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return fmt.Errorf("node ID %q contains hidden path component %q", id, part)
		}
		if part == "" && len(parts) > 1 {
			return fmt.Errorf("node ID %q contains empty path component", id)
		}
	}

	return nil
}

// Store provides file-system operations for node markdown files rooted at a
// base directory (typically a namespace folder inside the .marmot vault).
type Store struct {
	basePath string
}

// NewStore creates a Store rooted at basePath. The directory must already exist.
func NewStore(basePath string) *Store {
	return &Store{basePath: basePath}
}

// BasePath returns the root directory of the store.
func (s *Store) BasePath() string {
	return s.basePath
}

// NodePath derives the on-disk file path for a given node ID by appending
// ".md" and joining with the base path. The caller must validate the ID
// before calling this method if using untrusted input; for safe path
// resolution, use SafeNodePath instead.
func (s *Store) NodePath(id string) string {
	return filepath.Join(s.basePath, id+".md")
}

// SafeNodePath validates the node ID and returns the on-disk file path.
// It returns an error if the ID is invalid or would escape the base directory.
func (s *Store) SafeNodePath(id string) (string, error) {
	if err := ValidateNodeID(id); err != nil {
		return "", err
	}
	target := filepath.Join(s.basePath, id+".md")

	// Double-check: resolved path must be under basePath.
	absBase, err := filepath.Abs(s.basePath)
	if err != nil {
		return "", fmt.Errorf("resolve base path: %w", err)
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve target path: %w", err)
	}
	if !strings.HasPrefix(absTarget, absBase+string(filepath.Separator)) {
		return "", fmt.Errorf("node ID %q resolves outside vault directory", id)
	}

	return target, nil
}

// IDFromPath derives a node ID from an absolute file path by stripping the
// base path prefix and the ".md" extension.
func (s *Store) IDFromPath(filePath string) (string, error) {
	rel, err := filepath.Rel(s.basePath, filePath)
	if err != nil {
		return "", fmt.Errorf("id from path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("id from path: %s is outside base %s", filePath, s.basePath)
	}
	// Strip .md extension.
	id := strings.TrimSuffix(rel, ".md")
	// Normalise to forward slashes (for Windows compat, though primary target is macOS/Linux).
	id = filepath.ToSlash(id)
	return id, nil
}

// LoadNode reads a node markdown file from the given path and parses it.
func (s *Store) LoadNode(path string) (*Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load node: %w", err)
	}
	return ParseNode(data, path)
}

// SaveNode writes a node to disk using atomic write (temp file + rename).
// The node's ID determines the file path. The ID is validated to prevent
// path traversal attacks.
func (s *Store) SaveNode(node *Node) error {
	data, err := RenderNode(node)
	if err != nil {
		return fmt.Errorf("save node: %w", err)
	}

	target, err := s.SafeNodePath(node.ID)
	if err != nil {
		return fmt.Errorf("save node: %w", err)
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("save node: mkdir: %w", err)
	}

	// Atomic write: create temp file in the same directory, write, then rename.
	tmp, err := os.CreateTemp(dir, ".node-*.md.tmp")
	if err != nil {
		return fmt.Errorf("save node: create temp: %w", err)
	}
	tmpPath := tmp.Name()

	// Clean up temp file on any failure path.
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("save node: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save node: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("save node: rename: %w", err)
	}

	success = true
	return nil
}

// DeleteNode removes the markdown file for the given node ID. The ID is
// validated to prevent path traversal attacks.
func (s *Store) DeleteNode(id string) error {
	path, err := s.SafeNodePath(id)
	if err != nil {
		return fmt.Errorf("delete node %q: %w", id, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete node %q: %w", id, err)
	}
	return nil
}

// SoftDeleteNode marks a node as superseded, setting its status, valid_until timestamp,
// and optionally the ID of the node that supersedes it.
// supersededBy may be empty if the node is being retired without a replacement.
func (s *Store) SoftDeleteNode(id, supersededBy string) error {
	path, err := s.SafeNodePath(id)
	if err != nil {
		return fmt.Errorf("invalid node ID %q: %w", id, err)
	}
	n, err := s.LoadNode(path)
	if err != nil {
		return fmt.Errorf("load node %q: %w", id, err)
	}
	n.Status = StatusSuperseded
	n.ValidUntil = time.Now().UTC().Format(time.RFC3339)
	if supersededBy != "" {
		n.SupersededBy = supersededBy
	}
	return s.SaveNode(n)
}

// ListActiveNodes returns metadata for all nodes with status "active" (or no status set).
// Superseded and archived nodes are excluded.
func (s *Store) ListActiveNodes() ([]NodeMeta, error) {
	all, err := s.ListNodes()
	if err != nil {
		return nil, err
	}
	active := all[:0]
	for _, m := range all {
		if m.Status == "" || m.Status == StatusActive {
			active = append(active, m)
		}
	}
	return active, nil
}

// ListNodes scans the base directory recursively for .md files, parsing only
// the lightweight frontmatter metadata from each. Files that fail to parse are
// silently skipped.
func (s *Store) ListNodes() ([]NodeMeta, error) {
	var metas []NodeMeta

	err := filepath.Walk(s.basePath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if info.IsDir() {
			name := info.Name()
			// Skip hidden dirs (.obsidian, .marmot-data) and system dirs (_bridges, _heat).
			if path != s.basePath && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip files starting with _ (e.g., _summary.md, _namespace.md).
		if strings.HasPrefix(info.Name(), "_") {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		meta, err := ParseNodeMeta(data, path)
		if err != nil {
			return nil // skip malformed files
		}

		meta.FilePath = path
		metas = append(metas, *meta)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	return metas, nil
}
