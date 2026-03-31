package verify

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
)

// --- ComputeNodeHash tests ---

func TestComputeNodeHash_Deterministic(t *testing.T) {
	n := &node.Node{
		ID:      "auth/login",
		Summary: "Handles user authentication",
		Context: "Uses JWT tokens for session management",
		Edges: []node.Edge{
			{Target: "auth/session", Relation: node.Contains, Class: node.Structural},
			{Target: "db/users", Relation: node.Reads, Class: node.Behavioral},
		},
	}

	hash1 := ComputeNodeHash(n)
	hash2 := ComputeNodeHash(n)

	if hash1 != hash2 {
		t.Errorf("ComputeNodeHash is not deterministic: %s != %s", hash1, hash2)
	}

	if len(hash1) != 64 {
		t.Errorf("expected 64-char hex SHA-256, got %d chars: %s", len(hash1), hash1)
	}
}

func TestComputeNodeHash_DeterministicEdgeOrder(t *testing.T) {
	// Same edges in different order should produce the same hash.
	n1 := &node.Node{
		ID:      "auth/login",
		Summary: "Handles user authentication",
		Edges: []node.Edge{
			{Target: "b", Relation: node.Reads},
			{Target: "a", Relation: node.Contains},
		},
	}
	n2 := &node.Node{
		ID:      "auth/login",
		Summary: "Handles user authentication",
		Edges: []node.Edge{
			{Target: "a", Relation: node.Contains},
			{Target: "b", Relation: node.Reads},
		},
	}

	if ComputeNodeHash(n1) != ComputeNodeHash(n2) {
		t.Error("ComputeNodeHash should be order-independent for edges")
	}
}

func TestComputeNodeHash_ChangesWithContent(t *testing.T) {
	base := &node.Node{
		ID:      "auth/login",
		Summary: "Original summary",
		Context: "Original context",
		Edges: []node.Edge{
			{Target: "auth/session", Relation: node.Contains},
		},
	}

	tests := []struct {
		name   string
		modify func(n *node.Node)
	}{
		{
			name:   "different summary",
			modify: func(n *node.Node) { n.Summary = "Changed summary" },
		},
		{
			name:   "different context",
			modify: func(n *node.Node) { n.Context = "Changed context" },
		},
		{
			name: "different edge target",
			modify: func(n *node.Node) {
				n.Edges = []node.Edge{{Target: "auth/other", Relation: node.Contains}}
			},
		},
		{
			name: "different edge relation",
			modify: func(n *node.Node) {
				n.Edges = []node.Edge{{Target: "auth/session", Relation: node.Imports}}
			},
		},
		{
			name: "additional edge",
			modify: func(n *node.Node) {
				n.Edges = append(n.Edges, node.Edge{Target: "extra", Relation: node.Calls})
			},
		},
	}

	baseHash := ComputeNodeHash(base)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified := &node.Node{
				ID:      base.ID,
				Summary: base.Summary,
				Context: base.Context,
				Edges:   make([]node.Edge, len(base.Edges)),
			}
			copy(modified.Edges, base.Edges)
			tt.modify(modified)

			modifiedHash := ComputeNodeHash(modified)
			if modifiedHash == baseHash {
				t.Errorf("hash should change when %s", tt.name)
			}
		})
	}
}

// --- ComputeSourceHash tests ---

func TestComputeSourceHash_WholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	content := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hash1, err := ComputeSourceHash(path, [2]int{0, 0})
	if err != nil {
		t.Fatal(err)
	}

	hash2, err := ComputeSourceHash(path, [2]int{0, 0})
	if err != nil {
		t.Fatal(err)
	}

	if hash1 != hash2 {
		t.Error("ComputeSourceHash should be deterministic")
	}
	if len(hash1) != 64 {
		t.Errorf("expected 64-char hex SHA-256, got %d chars", len(hash1))
	}
}

func TestComputeSourceHash_LineRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	lines := []string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
	}
	content := ""
	for i, l := range lines {
		content += l
		if i < len(lines)-1 {
			content += "\n"
		}
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Hash lines 2-4.
	rangeHash, err := ComputeSourceHash(path, [2]int{2, 4})
	if err != nil {
		t.Fatal(err)
	}

	// Hash the whole file -- should differ.
	wholeHash, err := ComputeSourceHash(path, [2]int{0, 0})
	if err != nil {
		t.Fatal(err)
	}

	if rangeHash == wholeHash {
		t.Error("line-range hash should differ from whole-file hash")
	}

	// Hash same range again -- should be deterministic.
	rangeHash2, err := ComputeSourceHash(path, [2]int{2, 4})
	if err != nil {
		t.Fatal(err)
	}
	if rangeHash != rangeHash2 {
		t.Error("line-range hash should be deterministic")
	}
}

func TestComputeSourceHash_EmptyPath(t *testing.T) {
	_, err := ComputeSourceHash("", [2]int{0, 0})
	if err == nil {
		t.Error("expected error for empty source path")
	}
}

func TestComputeSourceHash_NonexistentFile(t *testing.T) {
	_, err := ComputeSourceHash("/nonexistent/path/file.go", [2]int{0, 0})
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

// --- VerifyStaleness tests ---

func TestVerifyStaleness_DetectsStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	if err := os.WriteFile(path, []byte("original content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Compute hash of original content.
	originalHash, err := ComputeSourceHash(path, [2]int{0, 0})
	if err != nil {
		t.Fatal(err)
	}

	n := &node.Node{
		ID: "pkg/source",
		Source: node.Source{
			Path: path,
			Hash: originalHash,
		},
	}

	// Verify not stale initially.
	status, err := VerifyStaleness(n)
	if err != nil {
		t.Fatal(err)
	}
	if status.IsStale {
		t.Error("should not be stale before file change")
	}

	// Modify the file.
	if err := os.WriteFile(path, []byte("modified content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Now it should be stale.
	status, err = VerifyStaleness(n)
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsStale {
		t.Error("should be stale after file change")
	}
	if status.StoredHash != originalHash {
		t.Errorf("stored hash mismatch: got %s, want %s", status.StoredHash, originalHash)
	}
	if status.CurrentHash == originalHash {
		t.Error("current hash should differ from stored hash")
	}
}

func TestVerifyStaleness_NotStaleWhenMatching(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	content := "stable content"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hash, err := ComputeSourceHash(path, [2]int{0, 0})
	if err != nil {
		t.Fatal(err)
	}

	n := &node.Node{
		ID: "pkg/stable",
		Source: node.Source{
			Path: path,
			Hash: hash,
		},
	}

	status, err := VerifyStaleness(n)
	if err != nil {
		t.Fatal(err)
	}
	if status.IsStale {
		t.Error("should not be stale when hash matches")
	}
	if status.StoredHash != status.CurrentHash {
		t.Error("stored and current hash should match")
	}
}

func TestVerifyStaleness_NoSourcePath(t *testing.T) {
	n := &node.Node{
		ID:     "pkg/no-source",
		Source: node.Source{},
	}

	status, err := VerifyStaleness(n)
	if err != nil {
		t.Fatal(err)
	}
	if status.IsStale {
		t.Error("node with no source path should not be stale")
	}
}

// --- VerifyIntegrity tests ---

func TestVerifyIntegrity_DetectsDanglingEdges(t *testing.T) {
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Contains, Class: node.Structural},
				{Target: "nonexistent", Relation: node.Calls, Class: node.Behavioral},
			},
		},
		{
			ID: "b",
		},
	}

	issues := VerifyIntegrity(nodes)

	found := false
	for _, issue := range issues {
		if issue.IssueType == DanglingEdge && issue.NodeID == "a" {
			found = true
			break
		}
	}
	if !found {
		t.Error("should detect dangling edge to 'nonexistent'")
	}
}

func TestVerifyIntegrity_DetectsStructuralCycles(t *testing.T) {
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Contains, Class: node.Structural},
			},
		},
		{
			ID: "b",
			Edges: []node.Edge{
				{Target: "c", Relation: node.Imports, Class: node.Structural},
			},
		},
		{
			ID: "c",
			Edges: []node.Edge{
				{Target: "a", Relation: node.Extends, Class: node.Structural},
			},
		},
	}

	issues := VerifyIntegrity(nodes)

	found := false
	for _, issue := range issues {
		if issue.IssueType == StructuralCycle {
			found = true
			break
		}
	}
	if !found {
		t.Error("should detect structural cycle among a -> b -> c -> a")
	}
}

func TestVerifyIntegrity_PassesBehavioralCycles(t *testing.T) {
	// Behavioral cycles should NOT be flagged as issues.
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Calls, Class: node.Behavioral},
			},
		},
		{
			ID: "b",
			Edges: []node.Edge{
				{Target: "a", Relation: node.Calls, Class: node.Behavioral},
			},
		},
	}

	issues := VerifyIntegrity(nodes)

	for _, issue := range issues {
		if issue.IssueType == StructuralCycle {
			t.Error("behavioral cycles should not be flagged as structural cycles")
		}
	}
}

func TestVerifyIntegrity_CleanGraph(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	content := "package main"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hash, err := ComputeSourceHash(path, [2]int{0, 0})
	if err != nil {
		t.Fatal(err)
	}

	nodes := []*node.Node{
		{
			ID: "a",
			Source: node.Source{
				Path: path,
				Hash: hash,
			},
			Edges: []node.Edge{
				{Target: "b", Relation: node.Contains, Class: node.Structural},
			},
		},
		{
			ID: "b",
			Edges: []node.Edge{
				{Target: "a", Relation: node.Calls, Class: node.Behavioral},
			},
		},
	}

	issues := VerifyIntegrity(nodes)
	if len(issues) != 0 {
		t.Errorf("expected zero issues for clean graph, got %d:", len(issues))
		for _, i := range issues {
			t.Logf("  %s: %s (%s)", i.NodeID, i.Message, i.IssueType)
		}
	}
}

func TestVerifyIntegrity_DetectsHashMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	if err := os.WriteFile(path, []byte("current content"), 0644); err != nil {
		t.Fatal(err)
	}

	nodes := []*node.Node{
		{
			ID: "a",
			Source: node.Source{
				Path: path,
				Hash: "stale_hash_that_does_not_match",
			},
		},
	}

	issues := VerifyIntegrity(nodes)

	found := false
	for _, issue := range issues {
		if issue.IssueType == HashMismatch {
			found = true
			break
		}
	}
	if !found {
		t.Error("should detect hash mismatch when stored hash differs from current")
	}
}

func TestVerifyIntegrity_DetectsMissingSource(t *testing.T) {
	nodes := []*node.Node{
		{
			ID: "a",
			Source: node.Source{
				Path: "/nonexistent/file.go",
				Hash: "somehash",
			},
		},
	}

	issues := VerifyIntegrity(nodes)

	found := false
	for _, issue := range issues {
		if issue.IssueType == MissingSource {
			found = true
			break
		}
	}
	if !found {
		t.Error("should detect missing source file")
	}
}

// --- CheckStructuralAcyclicity tests ---

func TestCheckStructuralAcyclicity_LinearChain(t *testing.T) {
	// a -> b -> c (DAG, no cycle)
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Contains, Class: node.Structural},
			},
		},
		{
			ID: "b",
			Edges: []node.Edge{
				{Target: "c", Relation: node.Contains, Class: node.Structural},
			},
		},
		{ID: "c"},
	}

	acyclic, cycleNodes := CheckStructuralAcyclicity(nodes)
	if !acyclic {
		t.Errorf("linear chain should be acyclic, got cycle nodes: %v", cycleNodes)
	}
}

func TestCheckStructuralAcyclicity_Diamond(t *testing.T) {
	// a -> b, a -> c, b -> d, c -> d (DAG diamond)
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Contains, Class: node.Structural},
				{Target: "c", Relation: node.Contains, Class: node.Structural},
			},
		},
		{
			ID: "b",
			Edges: []node.Edge{
				{Target: "d", Relation: node.Imports, Class: node.Structural},
			},
		},
		{
			ID: "c",
			Edges: []node.Edge{
				{Target: "d", Relation: node.Imports, Class: node.Structural},
			},
		},
		{ID: "d"},
	}

	acyclic, cycleNodes := CheckStructuralAcyclicity(nodes)
	if !acyclic {
		t.Errorf("diamond should be acyclic, got cycle nodes: %v", cycleNodes)
	}
}

func TestCheckStructuralAcyclicity_SimpleCycle(t *testing.T) {
	// a -> b -> a (cycle)
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Contains, Class: node.Structural},
			},
		},
		{
			ID: "b",
			Edges: []node.Edge{
				{Target: "a", Relation: node.Imports, Class: node.Structural},
			},
		},
	}

	acyclic, cycleNodes := CheckStructuralAcyclicity(nodes)
	if acyclic {
		t.Error("should detect cycle in a -> b -> a")
	}
	if len(cycleNodes) != 2 {
		t.Errorf("expected 2 cycle nodes, got %d: %v", len(cycleNodes), cycleNodes)
	}
}

func TestCheckStructuralAcyclicity_SelfLoop(t *testing.T) {
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "a", Relation: node.Contains, Class: node.Structural},
			},
		},
	}

	acyclic, cycleNodes := CheckStructuralAcyclicity(nodes)
	if acyclic {
		t.Error("should detect self-loop")
	}
	if len(cycleNodes) != 1 || cycleNodes[0] != "a" {
		t.Errorf("expected cycle node [a], got %v", cycleNodes)
	}
}

func TestCheckStructuralAcyclicity_MixedEdges(t *testing.T) {
	// Structural: a -> b (no cycle)
	// Behavioral: b -> a (cycle, but should be ignored)
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Contains, Class: node.Structural},
			},
		},
		{
			ID: "b",
			Edges: []node.Edge{
				{Target: "a", Relation: node.Calls, Class: node.Behavioral},
			},
		},
	}

	acyclic, cycleNodes := CheckStructuralAcyclicity(nodes)
	if !acyclic {
		t.Errorf("behavioral back-edge should be ignored, got cycle nodes: %v", cycleNodes)
	}
}

func TestCheckStructuralAcyclicity_EmptyGraph(t *testing.T) {
	acyclic, cycleNodes := CheckStructuralAcyclicity(nil)
	if !acyclic {
		t.Errorf("empty graph should be acyclic, got cycle nodes: %v", cycleNodes)
	}
}

func TestCheckStructuralAcyclicity_IsolatedNodes(t *testing.T) {
	nodes := []*node.Node{
		{ID: "a"},
		{ID: "b"},
		{ID: "c"},
	}

	acyclic, cycleNodes := CheckStructuralAcyclicity(nodes)
	if !acyclic {
		t.Errorf("isolated nodes should be acyclic, got cycle nodes: %v", cycleNodes)
	}
}

func TestCheckStructuralAcyclicity_LargerCycle(t *testing.T) {
	// a -> b -> c -> d -> b (cycle among b, c, d; a is not in cycle)
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Contains, Class: node.Structural},
			},
		},
		{
			ID: "b",
			Edges: []node.Edge{
				{Target: "c", Relation: node.Contains, Class: node.Structural},
			},
		},
		{
			ID: "c",
			Edges: []node.Edge{
				{Target: "d", Relation: node.Imports, Class: node.Structural},
			},
		},
		{
			ID: "d",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Extends, Class: node.Structural},
			},
		},
	}

	acyclic, cycleNodes := CheckStructuralAcyclicity(nodes)
	if acyclic {
		t.Error("should detect cycle among b, c, d")
	}

	// The cycle nodes should include b, c, d but not necessarily a.
	cycleSet := make(map[string]bool)
	for _, id := range cycleNodes {
		cycleSet[id] = true
	}
	for _, expected := range []string{"b", "c", "d"} {
		if !cycleSet[expected] {
			t.Errorf("expected %s in cycle nodes, got %v", expected, cycleNodes)
		}
	}
}

func TestCheckStructuralAcyclicity_BehavioralOnlyCycle(t *testing.T) {
	// All edges behavioral: a -> b -> c -> a
	nodes := []*node.Node{
		{
			ID: "a",
			Edges: []node.Edge{
				{Target: "b", Relation: node.Calls, Class: node.Behavioral},
			},
		},
		{
			ID: "b",
			Edges: []node.Edge{
				{Target: "c", Relation: node.Reads, Class: node.Behavioral},
			},
		},
		{
			ID: "c",
			Edges: []node.Edge{
				{Target: "a", Relation: node.Writes, Class: node.Behavioral},
			},
		},
	}

	acyclic, _ := CheckStructuralAcyclicity(nodes)
	if !acyclic {
		t.Error("purely behavioral cycle should be treated as acyclic (structural only)")
	}
}
