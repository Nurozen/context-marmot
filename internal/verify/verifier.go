// Package verify provides structural acyclicity enforcement and hash-based
// integrity checking for ContextMarmot knowledge-graph nodes.
package verify

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nurozen/context-marmot/internal/node"
)

// IssueType enumerates the kinds of integrity issues the verifier can detect.
type IssueType string

const (
	DanglingEdge    IssueType = "dangling_edge"
	HashMismatch    IssueType = "hash_mismatch"
	StructuralCycle IssueType = "structural_cycle"
	MissingSource   IssueType = "missing_source"
)

// Severity indicates the impact level of an integrity issue.
type Severity string

const (
	Warning Severity = "warning"
	Error   Severity = "error"
)

// IntegrityIssue describes a single problem found during integrity verification.
type IntegrityIssue struct {
	NodeID    string
	IssueType IssueType
	Message   string
	Severity  Severity
}

// StaleStatus reports whether a node's source reference is still current.
type StaleStatus struct {
	NodeID      string
	IsStale     bool
	StoredHash  string
	CurrentHash string
}

// ComputeNodeHash computes a deterministic SHA-256 hash of a node's content.
// The hash covers: Summary, Context, and edges serialized in a canonical order.
func ComputeNodeHash(n *node.Node) string {
	h := sha256.New()

	// Hash summary and context.
	h.Write([]byte("summary:"))
	h.Write([]byte(n.Summary))
	h.Write([]byte("\ncontext:"))
	h.Write([]byte(n.Context))

	// Serialize edges deterministically: sort by (target, relation).
	edges := make([]string, len(n.Edges))
	for i, e := range n.Edges {
		edges[i] = fmt.Sprintf("%s|%s", e.Target, e.Relation)
	}
	sort.Strings(edges)

	h.Write([]byte("\nedges:"))
	for _, e := range edges {
		h.Write([]byte(e))
		h.Write([]byte(";"))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// ComputeSourceHash computes a SHA-256 hash of the referenced source file.
// If lines specifies a non-zero range (lines[0] > 0), only those lines
// (1-indexed, inclusive) are hashed. Otherwise the entire file is hashed.
func ComputeSourceHash(sourcePath string, lines [2]int) (string, error) {
	if sourcePath == "" {
		return "", fmt.Errorf("empty source path")
	}

	f, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("open source %s: %w", sourcePath, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()

	if lines[0] > 0 {
		// Hash only the specified line range (1-indexed, inclusive).
		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			if lineNum < lines[0] {
				continue
			}
			if lineNum > lines[1] {
				break
			}
			if lineNum > lines[0] {
				h.Write([]byte("\n"))
			}
			h.Write(scanner.Bytes())
		}
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read source %s: %w", sourcePath, err)
		}
	} else {
		// Hash the entire file using the already-open file handle.
		if _, err := io.Copy(h, f); err != nil {
			return "", fmt.Errorf("read source %s: %w", sourcePath, err)
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ResolveSourcePath resolves a source path that may be relative (to projectRoot)
// or absolute. Returns the path as-is if absolute or projectRoot is empty.
func ResolveSourcePath(sourcePath, projectRoot string) string {
	if filepath.IsAbs(sourcePath) || projectRoot == "" {
		return sourcePath
	}
	return filepath.Join(projectRoot, sourcePath)
}

// VerifyStaleness checks whether a node's source reference is still current
// by comparing the stored hash against the current file content hash.
func VerifyStaleness(n *node.Node, projectRoot string) (*StaleStatus, error) {
	status := &StaleStatus{
		NodeID:     n.ID,
		StoredHash: n.Source.Hash,
	}

	if n.Source.Path == "" {
		// No source reference -- not stale by definition.
		status.IsStale = false
		return status, nil
	}

	resolved := ResolveSourcePath(n.Source.Path, projectRoot)
	currentHash, err := ComputeSourceHash(resolved, n.Source.Lines)
	if err != nil {
		return nil, fmt.Errorf("verify staleness for %s: %w", n.ID, err)
	}

	status.CurrentHash = currentHash
	status.IsStale = status.StoredHash != currentHash
	return status, nil
}

// VerifyIntegrity checks a set of nodes for structural issues:
//   - Dangling edges: edge targets a node ID not present in the set
//   - Hash mismatches: node content hash differs from stored source hash
//   - Structural cycles: cycles among structural edges (via Kahn's algorithm)
//   - Missing sources: node references a source file that doesn't exist
func VerifyIntegrity(nodes []*node.Node, projectRoot string) []IntegrityIssue {
	var issues []IntegrityIssue

	// Build ID index for dangling-edge detection.
	idSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		idSet[n.ID] = true
	}

	for _, n := range nodes {
		// Check for dangling edges. Skip @-prefixed targets (cross-vault
		// references that can't be validated against the local node set).
		for _, e := range n.Edges {
			if !strings.HasPrefix(e.Target, "@") && !idSet[e.Target] {
				issues = append(issues, IntegrityIssue{
					NodeID:    n.ID,
					IssueType: DanglingEdge,
					Message:   fmt.Sprintf("edge targets unknown node %q (relation: %s)", e.Target, e.Relation),
					Severity:  Error,
				})
			}
		}

		// Check for missing source files.
		if n.Source.Path != "" {
			resolved := ResolveSourcePath(n.Source.Path, projectRoot)
			if _, err := os.Stat(resolved); os.IsNotExist(err) {
				issues = append(issues, IntegrityIssue{
					NodeID:    n.ID,
					IssueType: MissingSource,
					Message:   fmt.Sprintf("source file %q does not exist", n.Source.Path),
					Severity:  Warning,
				})
			}
		}

		// Check for hash mismatches (source hash vs current content).
		if n.Source.Path != "" && n.Source.Hash != "" {
			resolved := ResolveSourcePath(n.Source.Path, projectRoot)
			currentHash, err := ComputeSourceHash(resolved, n.Source.Lines)
			if err == nil && currentHash != n.Source.Hash {
				issues = append(issues, IntegrityIssue{
					NodeID:    n.ID,
					IssueType: HashMismatch,
					Message: fmt.Sprintf("source hash mismatch: stored=%s current=%s",
						truncateHash(n.Source.Hash), truncateHash(currentHash)),
					Severity: Warning,
				})
			}
		}
	}

	// Check for structural cycles.
	isAcyclic, cycleNodes := CheckStructuralAcyclicity(nodes)
	if !isAcyclic {
		issues = append(issues, IntegrityIssue{
			NodeID:    cycleNodes[0],
			IssueType: StructuralCycle,
			Message:   fmt.Sprintf("structural cycle detected involving nodes: %s", strings.Join(cycleNodes, " -> ")),
			Severity:  Error,
		})
	}

	// Check superseded-chain integrity.
	for _, n := range nodes {
		// SupersededBy must reference an existing node.
		if n.SupersededBy != "" && !idSet[n.SupersededBy] {
			issues = append(issues, IntegrityIssue{
				NodeID:    n.ID,
				IssueType: DanglingEdge,
				Message:   fmt.Sprintf("superseded_by references unknown node %q", n.SupersededBy),
				Severity:  Warning,
			})
		}
		// ValidUntil must be after ValidFrom if both are set.
		if n.ValidFrom != "" && n.ValidUntil != "" {
			from, errF := time.Parse(time.RFC3339, n.ValidFrom)
			until, errU := time.Parse(time.RFC3339, n.ValidUntil)
			if errF == nil && errU == nil && !until.After(from) {
				issues = append(issues, IntegrityIssue{
					NodeID:    n.ID,
					IssueType: HashMismatch, // reuse closest existing type
					Message:   fmt.Sprintf("valid_until %q is not after valid_from %q", n.ValidUntil, n.ValidFrom),
					Severity:  Warning,
				})
			}
		}
		// Superseded node should have SupersededBy or ValidUntil set.
		if n.Status == node.StatusSuperseded && n.SupersededBy == "" && n.ValidUntil == "" {
			issues = append(issues, IntegrityIssue{
				NodeID:    n.ID,
				IssueType: HashMismatch,
				Message:   "node has status=superseded but no superseded_by or valid_until set",
				Severity:  Warning,
			})
		}
	}

	return issues
}

// truncateHash returns the first 12 characters of a hash for display.
func truncateHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
