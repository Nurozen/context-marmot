package den

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/warren"
)

// contributeVault creates a bare source vault dir (no den machinery needed:
// the engine only reads node files from it).
func contributeVault(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// contributeMount builds a minimal editable target mount (no git, no warren
// checkout: WarrenPath stays empty so the manifest re-check is skipped).
func contributeMount(t *testing.T) warren.ProjectStatus {
	t.Helper()
	target := filepath.Join(t.TempDir(), ".marmot")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	return warren.ProjectStatus{
		WarrenID:   "w",
		ProjectID:  "p",
		Path:       target,
		Registered: true,
		Active:     true,
		Editable:   true,
		Available:  true,
	}
}

func saveVaultNode(t *testing.T, dir, id, summary, context string, tags ...string) {
	t.Helper()
	st := node.NewStore(dir)
	n := &node.Node{ID: id, Type: "concept", Namespace: "default", Status: node.StatusActive, Summary: summary, Context: context, Tags: tags}
	if err := st.SaveNode(n); err != nil {
		t.Fatalf("SaveNode %s: %v", id, err)
	}
}

func loadTargetNode(t *testing.T, mount warren.ProjectStatus, id string) *node.Node {
	t.Helper()
	st := node.NewStore(mount.Path)
	n, err := st.LoadNode(st.NodePath(id))
	if err != nil {
		t.Fatalf("load target node %s: %v", id, err)
	}
	return n
}

func TestContributeDeterministicAddUpdateNoop(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	saveVaultNode(t, vault, "notes/alpha", "Alpha summary", "Alpha context")
	saveVaultNode(t, vault, "notes/beta", "Beta summary", "")

	// First run: both nodes are new -> ADD.
	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	if res.Counts != (FlowCounts{Added: 2}) {
		t.Fatalf("counts = %+v, want {Added:2}", res.Counts)
	}
	if got := loadTargetNode(t, mount, "notes/alpha").Summary; got != "Alpha summary" {
		t.Fatalf("target alpha summary = %q", got)
	}
	if len(res.Ops) != 2 || res.Ops[0].Action != "add" || res.Ops[1].Action != "add" {
		t.Fatalf("ops = %+v", res.Ops)
	}

	// Second run, unchanged: all NOOP (the idempotency backbone).
	res, err = Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts != (FlowCounts{Noop: 2}) {
		t.Fatalf("second run counts = %+v, want {Noop:2}", res.Counts)
	}
	if res.Counts.Changes() != 0 {
		t.Fatalf("Changes() = %d, want 0", res.Counts.Changes())
	}

	// Modify one source node: deterministic UPDATE, the other stays NOOP.
	saveVaultNode(t, vault, "notes/alpha", "Alpha summary v2", "Alpha context v2")
	res, err = Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts != (FlowCounts{Updated: 1, Noop: 1}) {
		t.Fatalf("update run counts = %+v, want {Updated:1 Noop:1}", res.Counts)
	}
	if got := loadTargetNode(t, mount, "notes/alpha").Summary; got != "Alpha summary v2" {
		t.Fatalf("target alpha not updated: %q", got)
	}
}

func TestContributeUpdatePreservesTargetValidFrom(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	// Target already has the node, with a creation timestamp.
	tgt := node.NewStore(mount.Path)
	if err := tgt.SaveNode(&node.Node{
		ID: "notes/alpha", Type: "concept", Namespace: "default",
		Status: node.StatusActive, ValidFrom: "2020-01-02T03:04:05Z",
		Summary: "Old summary",
	}); err != nil {
		t.Fatal(err)
	}
	saveVaultNode(t, vault, "notes/alpha", "New summary", "")

	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts != (FlowCounts{Updated: 1}) {
		t.Fatalf("counts = %+v", res.Counts)
	}
	got := loadTargetNode(t, mount, "notes/alpha")
	if got.Summary != "New summary" || got.ValidFrom != "2020-01-02T03:04:05Z" {
		t.Fatalf("updated node = summary %q valid_from %q", got.Summary, got.ValidFrom)
	}
}

func TestContributeReactivatesSupersededTarget(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	tgt := node.NewStore(mount.Path)
	if err := tgt.SaveNode(&node.Node{
		ID: "notes/alpha", Type: "concept", Namespace: "default",
		Status: node.StatusSuperseded, Summary: "Alpha summary",
	}); err != nil {
		t.Fatal(err)
	}
	// Identical content but target is superseded: update (reactivate), not noop.
	saveVaultNode(t, vault, "notes/alpha", "Alpha summary", "")
	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts != (FlowCounts{Updated: 1}) {
		t.Fatalf("counts = %+v, want {Updated:1}", res.Counts)
	}
	if got := loadTargetNode(t, mount, "notes/alpha").Status; got != node.StatusActive {
		t.Fatalf("status = %q, want active", got)
	}
}

func TestContributeDryRunWritesNothing(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	saveVaultNode(t, vault, "notes/alpha", "Alpha summary", "")

	res, err := Contribute(context.Background(), vault, mount, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts != (FlowCounts{Added: 1}) {
		t.Fatalf("dry-run counts = %+v", res.Counts)
	}
	if len(res.Ops) != 1 || res.Ops[0].String() != "add node notes/alpha" {
		t.Fatalf("ops = %+v", res.Ops)
	}
	// Target untouched: no node file, no embeddings sidecar.
	if _, err := os.Stat(filepath.Join(mount.Path, "notes", "alpha.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a node file (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(mount.Path, ".marmot-data")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created .marmot-data (stat err=%v)", err)
	}
}

func TestContributeEmptyVaultIsZeroCounts(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts != (FlowCounts{}) || len(res.Ops) != 0 {
		t.Fatalf("empty vault result = %+v", res)
	}
}

func TestContributeRefusesNonEditableMount(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	mount.Editable = false
	if _, err := Contribute(context.Background(), vault, mount, false); err == nil {
		t.Fatal("expected refusal for non-editable mount")
	}
}

func TestContributeOpStrings(t *testing.T) {
	cases := map[string]ContributeOp{
		"add node a":           {Action: "add", NodeID: "a"},
		"update node b":        {Action: "update", NodeID: "a", TargetID: "b"},
		"supersede old with a": {Action: "supersede", NodeID: "a", TargetID: "old"},
		"noop node a":          {Action: "noop", NodeID: "a", TargetID: "a"},
	}
	for want, op := range cases {
		if got := op.String(); got != want {
			t.Errorf("op %+v String() = %q, want %q", op, got, want)
		}
	}
}

func TestSameNodeContent(t *testing.T) {
	base := func() *node.Node {
		return &node.Node{
			ID: "a", Type: "concept", Summary: "s", Context: "c",
			Tags:  []string{"t1"},
			Edges: []node.Edge{{Target: "b", Relation: node.References}},
		}
	}
	if !sameNodeContent(base(), base()) {
		t.Fatal("identical nodes must match")
	}
	for name, mutate := range map[string]func(*node.Node){
		"type":    func(n *node.Node) { n.Type = "file" },
		"summary": func(n *node.Node) { n.Summary = "x" },
		"context": func(n *node.Node) { n.Context = "x" },
		"tags":    func(n *node.Node) { n.Tags = []string{"other"} },
		"edges":   func(n *node.Node) { n.Edges[0].Target = "z" },
	} {
		m := base()
		mutate(m)
		if sameNodeContent(base(), m) {
			t.Errorf("%s change must not match", name)
		}
	}
}
