package curator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// BuildSystemPrompt
// ---------------------------------------------------------------------------

func TestBuildSystemPrompt_Minimal(t *testing.T) {
	got := BuildSystemPrompt(GraphStats{NodeCount: 3, EdgeCount: 2}, nil)
	if !strings.Contains(got, "knowledge graph curator") {
		t.Error("prompt missing curator intro")
	}
	if !strings.Contains(got, "Nodes: 3") {
		t.Errorf("prompt missing node count: %q", got)
	}
	if strings.Contains(got, "Selected Nodes") {
		t.Error("prompt should not mention selected nodes when none provided")
	}
}

func TestBuildSystemPrompt_Full(t *testing.T) {
	longSummary := strings.Repeat("x", 250)
	stats := GraphStats{
		NodeCount:  10,
		EdgeCount:  5,
		Namespaces: []string{"auth", "api"},
		IssueCount: 4,
	}
	selected := []APINodeSummary{
		{ID: "n1", Type: "function", Summary: longSummary, Tags: []string{"a", "b"}, Edges: 3},
		{ID: "n2", Type: "concept", Summary: "", Edges: 0},
	}
	got := BuildSystemPrompt(stats, selected)
	for _, want := range []string{"Namespaces: auth, api", "Issues detected: 4", "Selected Nodes", "**n1**", "[tags: a, b]", "..."} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q in:\n%s", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// detectDuplicates (via Analyze with a real embedding store)
// ---------------------------------------------------------------------------

func TestDetectDuplicates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "emb.db")
	store, err := embedding.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	embedder := embedding.NewMockEmbedder("mock-model")

	g := graph.NewGraph()
	// Two nodes with identical summaries -> near-identical vectors -> duplicate.
	summary := "The authentication handler validates user credentials."
	dupA := &node.Node{ID: "dup-a", Type: "function", Summary: summary}
	dupB := &node.Node{ID: "dup-b", Type: "function", Summary: summary}
	other := &node.Node{ID: "other", Type: "concept", Summary: "Something entirely unrelated about parsers."}
	for _, n := range []*node.Node{dupA, dupB, other} {
		if err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
		vec, embErr := embedder.Embed(n.Summary)
		if embErr != nil {
			t.Fatal(embErr)
		}
		if err := store.Upsert(n.ID, vec, "hash-"+n.ID, embedder.Model()); err != nil {
			t.Fatal(err)
		}
	}

	results := Analyze(g, nil, store, embedder, AnalyzeOpts{})
	var dups []Suggestion
	for _, s := range results {
		if s.Type == "duplicate" {
			dups = append(dups, s)
		}
	}
	if len(dups) != 1 {
		t.Fatalf("expected 1 duplicate suggestion, got %d: %+v", len(dups), dups)
	}
	if len(dups[0].NodeIDs) != 2 {
		t.Errorf("duplicate should reference 2 nodes, got %v", dups[0].NodeIDs)
	}
	if !strings.HasPrefix(dups[0].Fix.Command, "/merge ") {
		t.Errorf("duplicate fix should be a merge, got %q", dups[0].Fix.Command)
	}
}

func TestDetectDuplicates_NilStore(t *testing.T) {
	g := graph.NewGraph()
	_ = g.AddNode(&node.Node{ID: "x", Type: "concept", Summary: "hi"})
	// embedStore nil -> detectDuplicates returns nil (no panic).
	results := Analyze(g, nil, nil, embedding.NewMockEmbedder("m"), AnalyzeOpts{})
	for _, s := range results {
		if s.Type == "duplicate" {
			t.Error("did not expect duplicates without an embed store")
		}
	}
}

// ---------------------------------------------------------------------------
// detectStale (via Analyze with CheckStale)
// ---------------------------------------------------------------------------

func TestDetectStale(t *testing.T) {
	projectRoot := t.TempDir()
	srcRel := "src/foo.go"
	srcAbs := filepath.Join(projectRoot, srcRel)
	if err := os.MkdirAll(filepath.Dir(srcAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcAbs, []byte("package foo\n// changed content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := graph.NewGraph()
	// Stored hash deliberately wrong -> stale.
	staleNode := &node.Node{
		ID:      "stale-node",
		Type:    "function",
		Summary: "A function",
		Source:  node.Source{Path: srcRel, Hash: "deadbeef-not-the-real-hash"},
	}
	// A node without a source path should be skipped by detectStale.
	noSource := &node.Node{ID: "no-source", Type: "concept", Summary: "no source"}
	for _, n := range []*node.Node{staleNode, noSource} {
		if err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}

	results := Analyze(g, nil, nil, nil, AnalyzeOpts{CheckStale: true, ProjectRoot: projectRoot})
	var stale []Suggestion
	for _, s := range results {
		if s.Type == "stale" {
			stale = append(stale, s)
		}
	}
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale suggestion, got %d", len(stale))
	}
	if stale[0].Severity != "error" {
		t.Errorf("stale severity = %q, want error", stale[0].Severity)
	}
	if !stale[0].Fix.Auto {
		t.Error("stale fix should be auto")
	}
}

func TestDetectStale_NotStale(t *testing.T) {
	projectRoot := t.TempDir()
	// Node references a source file that does not exist -> VerifyStaleness
	// returns an error and the node is skipped (no suggestion).
	g := graph.NewGraph()
	n := &node.Node{
		ID:     "missing-src",
		Type:   "function",
		Source: node.Source{Path: "nope/missing.go", Hash: "abc"},
	}
	if err := g.AddNode(n); err != nil {
		t.Fatal(err)
	}
	results := Analyze(g, nil, nil, nil, AnalyzeOpts{CheckStale: true, ProjectRoot: projectRoot})
	for _, s := range results {
		if s.Type == "stale" {
			t.Error("did not expect a stale suggestion for a missing source file")
		}
	}
}

// ---------------------------------------------------------------------------
// inferType (via untyped detection)
// ---------------------------------------------------------------------------

func TestInferType(t *testing.T) {
	cases := map[string]string{
		"authHandler":    "function",
		"myFunc":         "function",
		"UserStruct":     "type",
		"dataModel":      "type",
		"pkgMain":        "package",
		"moduleCore":     "package",
		"apiEndpoint":    "api",
		"loginTest":      "test",
		"randomThing123": "concept",
	}
	for id, want := range cases {
		got := inferType(&node.Node{ID: id})
		if got != want {
			t.Errorf("inferType(%q) = %q, want %q", id, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// severityOrder default case & ProjectRootFromMarmotDir
// ---------------------------------------------------------------------------

func TestSeverityOrderDefault(t *testing.T) {
	if severityOrder("bogus") != 3 {
		t.Errorf("severityOrder(bogus) = %d, want 3", severityOrder("bogus"))
	}
	if severityOrder("error") != 0 || severityOrder("warning") != 1 || severityOrder("info") != 2 {
		t.Error("severityOrder produced unexpected ordering")
	}
}

func TestProjectRootFromMarmotDir(t *testing.T) {
	got := ProjectRootFromMarmotDir("/home/user/proj/.marmot")
	if got != "/home/user/proj" {
		t.Errorf("ProjectRootFromMarmotDir = %q, want /home/user/proj", got)
	}
}

// ---------------------------------------------------------------------------
// UndoStack.PopByID
// ---------------------------------------------------------------------------

func TestPopByID(t *testing.T) {
	us := NewUndoStack()
	us.Push("s1", UndoEntry{ID: "a", SessionID: "s1", Timestamp: time.Now()})
	us.Push("s1", UndoEntry{ID: "b", SessionID: "s1", Timestamp: time.Now()})
	us.Push("s1", UndoEntry{ID: "c", SessionID: "s1", Timestamp: time.Now()})

	// Pop a middle entry by ID.
	got := us.PopByID("s1", "b")
	if got == nil || got.ID != "b" {
		t.Fatalf("expected entry 'b', got %v", got)
	}
	if us.Len("s1") != 2 {
		t.Fatalf("expected len 2 after PopByID, got %d", us.Len("s1"))
	}

	// Non-existent ID returns nil.
	if got := us.PopByID("s1", "zzz"); got != nil {
		t.Errorf("expected nil for unknown ID, got %v", got)
	}
	// Unknown session returns nil.
	if got := us.PopByID("no-session", "a"); got != nil {
		t.Errorf("expected nil for unknown session, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// SnapshotNodes: invalid ID (SafeNodePath error path)
// ---------------------------------------------------------------------------

func TestSnapshotNodes_InvalidID(t *testing.T) {
	dir := t.TempDir()
	store := node.NewStore(dir)
	// A path-traversal ID should fail SafeNodePath -> Existed=false, nil Node.
	snaps := SnapshotNodes(store, []string{"../escape"})
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].Existed || snaps[0].Node != nil {
		t.Errorf("expected Existed=false, nil Node for invalid ID, got %+v", snaps[0])
	}
}

// ---------------------------------------------------------------------------
// ParseCommand edge cases
// ---------------------------------------------------------------------------

func TestParseCommand_BareSlash(t *testing.T) {
	cmd, ok := ParseCommand("/")
	if ok || cmd != nil {
		t.Errorf("expected nil,false for bare slash, got %v,%v", cmd, ok)
	}
}

func TestParseCommand_SlashWithSpaces(t *testing.T) {
	// Tokenizes to ["/"] then name becomes "" -> rejected.
	cmd, ok := ParseCommand("/   ")
	if ok || cmd != nil {
		t.Errorf("expected nil,false for slash+spaces, got %v,%v", cmd, ok)
	}
}

// ---------------------------------------------------------------------------
// ExecuteCommand argument/selection validation branches
// ---------------------------------------------------------------------------

func TestExecuteTag_NoArgs(t *testing.T) {
	eng := setupTestEngine(t)
	res, err := ExecuteCommand(context.Background(), &SlashCommand{Name: "tag"}, eng, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Error("expected failure for /tag with no args")
	}
}

func TestExecuteTag_NoSelection(t *testing.T) {
	eng := setupTestEngine(t)
	res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "tag", Args: []string{"foo"}}, eng, nil)
	if res.Success {
		t.Error("expected failure for /tag with no selected nodes")
	}
}

func TestExecuteTag_OnlyEmptyArgs(t *testing.T) {
	eng := setupTestEngine(t)
	// Args present but all whitespace -> uniqueNonEmpty yields none.
	res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "tag", Args: []string{"  "}}, eng, []string{"x"})
	if res.Success {
		t.Error("expected failure when all tag args are empty")
	}
}

func TestExecuteUntag_Validation(t *testing.T) {
	eng := setupTestEngine(t)
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "untag"}, eng, []string{"x"}); res.Success {
		t.Error("expected failure for /untag with no args")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "untag", Args: []string{"t"}}, eng, nil); res.Success {
		t.Error("expected failure for /untag with no selection")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "untag", Args: []string{" "}}, eng, []string{"x"}); res.Success {
		t.Error("expected failure for /untag with empty tag")
	}
}

func TestExecuteUntag_NoMatchingTag(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "u1", Type: "concept", Status: "active", Tags: []string{"keep"}})
	res, err := ExecuteCommand(context.Background(), &SlashCommand{Name: "untag", Args: []string{"absent"}}, eng, []string{"u1"})
	if err != nil {
		t.Fatal(err)
	}
	// Command succeeds but nothing mutated (tag wasn't present).
	if len(res.MutatedNodes) != 0 {
		t.Errorf("expected no mutations, got %v", res.MutatedNodes)
	}
}

func TestExecuteType_Validation(t *testing.T) {
	eng := setupTestEngine(t)
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "type"}, eng, []string{"x"}); res.Success {
		t.Error("expected failure for /type with no args")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "type", Args: []string{"function"}}, eng, nil); res.Success {
		t.Error("expected failure for /type with no selection")
	}
}

func TestExecuteDelete_NoSelection(t *testing.T) {
	eng := setupTestEngine(t)
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "delete"}, eng, nil); res.Success {
		t.Error("expected failure for /delete with no selection")
	}
}

func TestExecuteDelete_UnresolvableNode(t *testing.T) {
	eng := setupTestEngine(t)
	// Selected node doesn't exist -> ResolveNodeID fails -> no mutation, still success.
	res, err := ExecuteCommand(context.Background(), &SlashCommand{Name: "delete"}, eng, []string{"ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Errorf("expected success with 0 mutations, got %s", res.Message)
	}
	if len(res.MutatedNodes) != 0 {
		t.Errorf("expected no mutations, got %v", res.MutatedNodes)
	}
}

func TestExecuteLink_Validation(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "ls", Type: "function", Status: "active"})
	addTestNode(t, eng, &node.Node{ID: "lt", Type: "module", Status: "active"})

	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "link", Args: []string{"ls", "calls"}}, eng, nil); res.Success {
		t.Error("expected failure for /link with too few args")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "link", Args: []string{"missing", "calls", "lt"}}, eng, nil); res.Success {
		t.Error("expected failure for /link with missing source")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "link", Args: []string{"ls", "calls", "missing"}}, eng, nil); res.Success {
		t.Error("expected failure for /link with missing target")
	}
}

func TestExecuteLink_CycleRejected(t *testing.T) {
	eng := setupTestEngine(t)
	// Structural relation "contains": a->b then b->a would create a cycle.
	addTestNode(t, eng, &node.Node{
		ID: "pa", Type: "module", Status: "active",
		Edges: []node.Edge{{Target: "pb", Relation: node.Contains, Class: node.Structural}},
	})
	addTestNode(t, eng, &node.Node{ID: "pb", Type: "module", Status: "active"})

	res, err := ExecuteCommand(context.Background(), &SlashCommand{Name: "link", Args: []string{"pb", string(node.Contains), "pa"}}, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Errorf("expected cycle rejection, got success: %s", res.Message)
	}
	if !strings.Contains(res.Message, "cycle") {
		t.Errorf("expected cycle message, got %q", res.Message)
	}
}

func TestExecuteUnlink_Validation(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "us", Type: "function", Status: "active"})
	addTestNode(t, eng, &node.Node{ID: "ut", Type: "module", Status: "active"})

	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "unlink", Args: []string{"us", "calls"}}, eng, nil); res.Success {
		t.Error("expected failure for /unlink too few args")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "unlink", Args: []string{"us", "bogus_rel", "ut"}}, eng, nil); res.Success {
		t.Error("expected failure for /unlink invalid relation")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "unlink", Args: []string{"missing", "calls", "ut"}}, eng, nil); res.Success {
		t.Error("expected failure for /unlink missing source")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "unlink", Args: []string{"us", "calls", "missing"}}, eng, nil); res.Success {
		t.Error("expected failure for /unlink missing target")
	}
	// Valid nodes/relation but no such edge present.
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "unlink", Args: []string{"us", "calls", "ut"}}, eng, nil); res.Success {
		t.Error("expected failure for /unlink when edge absent")
	}
}

func TestExecuteMerge_Validation(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "ma", Type: "concept", Status: "active"})

	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "merge", Args: []string{"ma"}}, eng, nil); res.Success {
		t.Error("expected failure for /merge with one arg")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "merge", Args: []string{"missing", "ma"}}, eng, nil); res.Success {
		t.Error("expected failure for /merge missing A")
	}
	if res, _ := ExecuteCommand(context.Background(), &SlashCommand{Name: "merge", Args: []string{"ma", "missing"}}, eng, nil); res.Success {
		t.Error("expected failure for /merge missing B")
	}
}

// TestExecuteMerge_RedirectsInbound exercises the inbound-edge redirect logic:
// a third node points at B; after merging B into A, its edge should target A.
func TestExecuteMerge_RedirectsInbound(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "mA", Type: "concept", Status: "active"})
	addTestNode(t, eng, &node.Node{ID: "mB", Type: "concept", Status: "active"})
	// mC -> mB (should be rewritten to mC -> mA).
	addTestNode(t, eng, &node.Node{
		ID: "mC", Type: "function", Status: "active",
		Edges: []node.Edge{{Target: "mB", Relation: node.Calls, Class: node.Behavioral}},
	})

	res, err := ExecuteCommand(context.Background(), &SlashCommand{Name: "merge", Args: []string{"mA", "mB"}}, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("merge failed: %s", res.Message)
	}
	reloadedC, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("mC"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reloadedC.Edges) != 1 || reloadedC.Edges[0].Target != "mA" {
		t.Errorf("expected mC edge redirected to mA, got %+v", reloadedC.Edges)
	}
}

// TestExecuteMerge_InboundDuplicateRemoved covers the branch where the inbound
// source already has an equivalent edge to A, so the B-edge is dropped, not
// rewritten.
func TestExecuteMerge_InboundDuplicateRemoved(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "dA", Type: "concept", Status: "active"})
	addTestNode(t, eng, &node.Node{ID: "dB", Type: "concept", Status: "active"})
	// dC already calls dA AND calls dB; after merge the dB edge is a dup -> removed.
	addTestNode(t, eng, &node.Node{
		ID: "dC", Type: "function", Status: "active",
		Edges: []node.Edge{
			{Target: "dA", Relation: node.Calls, Class: node.Behavioral},
			{Target: "dB", Relation: node.Calls, Class: node.Behavioral},
		},
	})

	res, err := ExecuteCommand(context.Background(), &SlashCommand{Name: "merge", Args: []string{"dA", "dB"}}, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("merge failed: %s", res.Message)
	}
	reloadedC, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("dC"))
	if err != nil {
		t.Fatal(err)
	}
	// Only the single dA edge should remain.
	if len(reloadedC.Edges) != 1 || reloadedC.Edges[0].Target != "dA" {
		t.Errorf("expected single dA edge after dedup, got %+v", reloadedC.Edges)
	}
}

// TestExecuteVerify_WithIssues creates a node with a dangling edge so verify
// reports issues (exercises the issue-reporting branch).
func TestExecuteVerify_WithIssues(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{
		ID: "vi", Type: "function", Status: "active",
		Edges: []node.Edge{{Target: "does-not-exist", Relation: node.Calls, Class: node.Behavioral}},
	})
	res, err := ExecuteCommand(context.Background(), &SlashCommand{Name: "verify"}, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("verify returned error result: %s", res.Message)
	}
	if !strings.Contains(res.Message, "issue") {
		t.Errorf("expected issues reported, got %q", res.Message)
	}
}

func TestExecuteVerify_NoNodes(t *testing.T) {
	eng := setupTestEngine(t)
	res, err := ExecuteCommand(context.Background(), &SlashCommand{Name: "verify"}, eng, []string{"ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || !strings.Contains(res.Message, "no nodes") {
		t.Errorf("expected 'no nodes to verify', got %q", res.Message)
	}
}
