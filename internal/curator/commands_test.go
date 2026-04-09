package curator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// ParseCommand tests
// ---------------------------------------------------------------------------

func TestParseCommand_Tag(t *testing.T) {
	cmd, ok := ParseCommand("/tag important")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "tag" {
		t.Errorf("name = %q, want %q", cmd.Name, "tag")
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "important" {
		t.Errorf("args = %v, want [important]", cmd.Args)
	}
}

func TestParseCommand_Untag(t *testing.T) {
	cmd, ok := ParseCommand("/untag stale")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "untag" {
		t.Errorf("name = %q, want %q", cmd.Name, "untag")
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "stale" {
		t.Errorf("args = %v, want [stale]", cmd.Args)
	}
}

func TestParseCommand_Type(t *testing.T) {
	cmd, ok := ParseCommand("/type function")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "type" {
		t.Errorf("name = %q, want %q", cmd.Name, "type")
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "function" {
		t.Errorf("args = %v, want [function]", cmd.Args)
	}
}

func TestParseCommand_Merge(t *testing.T) {
	cmd, ok := ParseCommand("/merge node-a node-b")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "merge" {
		t.Errorf("name = %q, want %q", cmd.Name, "merge")
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != "node-a" || cmd.Args[1] != "node-b" {
		t.Errorf("args = %v, want [node-a node-b]", cmd.Args)
	}
}

func TestParseCommand_Delete(t *testing.T) {
	cmd, ok := ParseCommand("/delete")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "delete" {
		t.Errorf("name = %q, want %q", cmd.Name, "delete")
	}
	if len(cmd.Args) != 0 {
		t.Errorf("args = %v, want []", cmd.Args)
	}
}

func TestParseCommand_Link(t *testing.T) {
	cmd, ok := ParseCommand("/link src-node calls tgt-node")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "link" {
		t.Errorf("name = %q, want %q", cmd.Name, "link")
	}
	if len(cmd.Args) != 3 {
		t.Fatalf("args len = %d, want 3", len(cmd.Args))
	}
	if cmd.Args[0] != "src-node" || cmd.Args[1] != "calls" || cmd.Args[2] != "tgt-node" {
		t.Errorf("args = %v, want [src-node calls tgt-node]", cmd.Args)
	}
}

func TestParseCommand_Unlink(t *testing.T) {
	cmd, ok := ParseCommand("/unlink src-node reads tgt-node")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "unlink" {
		t.Errorf("name = %q, want %q", cmd.Name, "unlink")
	}
	if len(cmd.Args) != 3 {
		t.Fatalf("args len = %d, want 3", len(cmd.Args))
	}
	if cmd.Args[0] != "src-node" || cmd.Args[1] != "reads" || cmd.Args[2] != "tgt-node" {
		t.Errorf("args = %v, want [src-node reads tgt-node]", cmd.Args)
	}
}

func TestParseCommand_Verify(t *testing.T) {
	cmd, ok := ParseCommand("/verify")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "verify" {
		t.Errorf("name = %q, want %q", cmd.Name, "verify")
	}
	if len(cmd.Args) != 0 {
		t.Errorf("args = %v, want []", cmd.Args)
	}
}

func TestParseCommand_QuotedArgs(t *testing.T) {
	cmd, ok := ParseCommand(`/tag "multi word tag"`)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "tag" {
		t.Errorf("name = %q, want %q", cmd.Name, "tag")
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "multi word tag" {
		t.Errorf("args = %v, want [multi word tag]", cmd.Args)
	}
}

func TestParseCommand_QuotedArgsMultiple(t *testing.T) {
	cmd, ok := ParseCommand(`/link "my source" calls "my target"`)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(cmd.Args) != 3 {
		t.Fatalf("args len = %d, want 3", len(cmd.Args))
	}
	if cmd.Args[0] != "my source" || cmd.Args[1] != "calls" || cmd.Args[2] != "my target" {
		t.Errorf("args = %v, want [my source, calls, my target]", cmd.Args)
	}
}

func TestParseCommand_NonSlash(t *testing.T) {
	cmd, ok := ParseCommand("hello world")
	if ok {
		t.Error("expected ok=false for non-slash message")
	}
	if cmd != nil {
		t.Error("expected nil command for non-slash message")
	}
}

func TestParseCommand_EmptyMessage(t *testing.T) {
	cmd, ok := ParseCommand("")
	if ok {
		t.Error("expected ok=false for empty message")
	}
	if cmd != nil {
		t.Error("expected nil command for empty message")
	}
}

func TestParseCommand_InvalidCommandName(t *testing.T) {
	cmd, ok := ParseCommand("/foobar arg1")
	if !ok {
		t.Fatal("expected ok=true (slash detected)")
	}
	if cmd.Name != "foobar" {
		t.Errorf("name = %q, want %q", cmd.Name, "foobar")
	}
	// ExecuteCommand should reject it.
	result, err := ExecuteCommand(context.Background(), cmd, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false for unknown command")
	}
}

func TestParseCommand_LeadingWhitespace(t *testing.T) {
	cmd, ok := ParseCommand("  /tag foo")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cmd.Name != "tag" {
		t.Errorf("name = %q, want %q", cmd.Name, "tag")
	}
}

// ---------------------------------------------------------------------------
// ExecuteCommand integration tests (using a temp marmot dir)
// ---------------------------------------------------------------------------

// setupTestEngine creates a temporary .marmot directory with an in-memory graph
// and node store for testing. Returns the engine and a cleanup function.
func setupTestEngine(t *testing.T) *mcp.Engine {
	t.Helper()
	tmpDir := t.TempDir()
	marmotDir := filepath.Join(tmpDir, ".marmot")
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ns := node.NewStore(marmotDir)
	g := graph.NewGraph()

	eng := &mcp.Engine{
		NodeStore: ns,
		MarmotDir: marmotDir,
	}
	eng.SetGraph(g)
	return eng
}

// addTestNode creates and persists a node via the engine's NodeStore and Graph.
func addTestNode(t *testing.T, eng *mcp.Engine, n *node.Node) {
	t.Helper()
	if err := eng.NodeStore.SaveNode(n); err != nil {
		t.Fatal(err)
	}
	if err := eng.GetGraph().UpsertNode(n); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteTag(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "alpha", Type: "concept", Status: "active"})

	cmd := &SlashCommand{Name: "tag", Args: []string{"important"}}
	result, err := ExecuteCommand(context.Background(), cmd, eng, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}
	if len(result.MutatedNodes) != 1 || result.MutatedNodes[0] != "alpha" {
		t.Errorf("mutated = %v, want [alpha]", result.MutatedNodes)
	}

	// Verify the tag persisted.
	reloaded, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tag := range reloaded.Tags {
		if tag == "important" {
			found = true
		}
	}
	if !found {
		t.Error("tag 'important' not found on reloaded node")
	}
}

func TestExecuteUntag(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "beta", Type: "concept", Status: "active", Tags: []string{"old", "keep"}})

	cmd := &SlashCommand{Name: "untag", Args: []string{"old"}}
	result, err := ExecuteCommand(context.Background(), cmd, eng, []string{"beta"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	reloaded, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("beta"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range reloaded.Tags {
		if tag == "old" {
			t.Error("tag 'old' should have been removed")
		}
	}
	foundKeep := false
	for _, tag := range reloaded.Tags {
		if tag == "keep" {
			foundKeep = true
		}
	}
	if !foundKeep {
		t.Error("tag 'keep' should still be present")
	}
}

func TestExecuteType(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "gamma", Type: "concept", Status: "active"})

	cmd := &SlashCommand{Name: "type", Args: []string{"function"}}
	result, err := ExecuteCommand(context.Background(), cmd, eng, []string{"gamma"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	reloaded, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("gamma"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Type != "function" {
		t.Errorf("type = %q, want %q", reloaded.Type, "function")
	}
}

func TestExecuteType_Invalid(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "delta", Type: "concept", Status: "active"})

	cmd := &SlashCommand{Name: "type", Args: []string{"invalid-type"}}
	result, err := ExecuteCommand(context.Background(), cmd, eng, []string{"delta"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Error("expected failure for invalid type")
	}
}

func TestExecuteDelete(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "epsilon", Type: "concept", Status: "active"})

	cmd := &SlashCommand{Name: "delete", Args: nil}
	result, err := ExecuteCommand(context.Background(), cmd, eng, []string{"epsilon"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	// Verify node is now superseded.
	reloaded, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("epsilon"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != node.StatusSuperseded {
		t.Errorf("status = %q, want %q", reloaded.Status, node.StatusSuperseded)
	}
}

func TestExecuteMerge(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{
		ID: "nodeA", Type: "concept", Status: "active",
		Tags:  []string{"tagA"},
		Edges: []node.Edge{{Target: "other", Relation: "calls", Class: node.Behavioral}},
	})
	addTestNode(t, eng, &node.Node{
		ID: "nodeB", Type: "concept", Status: "active",
		Tags:  []string{"tagA", "tagB"},
		Edges: []node.Edge{{Target: "other2", Relation: "reads", Class: node.Behavioral}},
	})

	cmd := &SlashCommand{Name: "merge", Args: []string{"nodeA", "nodeB"}}
	result, err := ExecuteCommand(context.Background(), cmd, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	// A should have B's edges and tags.
	reloadedA, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("nodeA"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reloadedA.Edges) != 2 {
		t.Errorf("expected 2 edges on A, got %d", len(reloadedA.Edges))
	}
	tagSet := make(map[string]bool)
	for _, tag := range reloadedA.Tags {
		tagSet[tag] = true
	}
	if !tagSet["tagA"] || !tagSet["tagB"] {
		t.Errorf("A tags = %v, want tagA and tagB", reloadedA.Tags)
	}

	// B should be superseded.
	reloadedB, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("nodeB"))
	if err != nil {
		t.Fatal(err)
	}
	if reloadedB.Status != node.StatusSuperseded {
		t.Errorf("B status = %q, want %q", reloadedB.Status, node.StatusSuperseded)
	}
}

func TestExecuteLink(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "src", Type: "function", Status: "active"})
	addTestNode(t, eng, &node.Node{ID: "tgt", Type: "module", Status: "active"})

	cmd := &SlashCommand{Name: "link", Args: []string{"src", "calls", "tgt"}}
	result, err := ExecuteCommand(context.Background(), cmd, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	reloaded, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("src"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(reloaded.Edges))
	}
	if reloaded.Edges[0].Target != "tgt" || reloaded.Edges[0].Relation != "calls" {
		t.Errorf("edge = %+v, want target=tgt relation=calls", reloaded.Edges[0])
	}
}

func TestExecuteLink_InvalidRelation(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "src2", Type: "function", Status: "active"})
	addTestNode(t, eng, &node.Node{ID: "tgt2", Type: "module", Status: "active"})

	cmd := &SlashCommand{Name: "link", Args: []string{"src2", "invalid_rel", "tgt2"}}
	result, err := ExecuteCommand(context.Background(), cmd, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Error("expected failure for invalid relation")
	}
}

func TestExecuteUnlink(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{
		ID: "ul-src", Type: "function", Status: "active",
		Edges: []node.Edge{{Target: "ul-tgt", Relation: "calls", Class: node.Behavioral}},
	})
	addTestNode(t, eng, &node.Node{ID: "ul-tgt", Type: "module", Status: "active"})

	cmd := &SlashCommand{Name: "unlink", Args: []string{"ul-src", "calls", "ul-tgt"}}
	result, err := ExecuteCommand(context.Background(), cmd, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	reloaded, err := eng.NodeStore.LoadNode(eng.NodeStore.NodePath("ul-src"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Edges) != 0 {
		t.Errorf("expected 0 edges after unlink, got %d", len(reloaded.Edges))
	}
}

func TestExecuteVerify(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "v-node", Type: "concept", Status: "active"})

	cmd := &SlashCommand{Name: "verify", Args: nil}
	result, err := ExecuteCommand(context.Background(), cmd, eng, []string{"v-node"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}
}

func TestExecuteVerify_AllNodes(t *testing.T) {
	eng := setupTestEngine(t)
	addTestNode(t, eng, &node.Node{ID: "v-all-1", Type: "concept", Status: "active"})
	addTestNode(t, eng, &node.Node{ID: "v-all-2", Type: "function", Status: "active"})

	cmd := &SlashCommand{Name: "verify", Args: nil}
	result, err := ExecuteCommand(context.Background(), cmd, eng, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}
}

// Ensure unused imports don't cause build failures.
var _ = os.TempDir
