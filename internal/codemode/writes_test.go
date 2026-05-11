package codemode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/embedding"
	mcpserver "github.com/nurozen/context-marmot/internal/mcp"
)

// ---------------------------------------------------------------------------
// Write-mode tests
//
// These tests reach across packages (codemode + curator + mcp + node) and
// require a real engine. The fixture is built inline so tests stay
// self-contained.
// ---------------------------------------------------------------------------

func TestExecute_Writes_Tag(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	write := &WriteContext{
		SessionID: "test-session",
		Namespace: "default",
		UndoStack: curator.NewUndoStack(),
	}

	r := ex.ExecuteWithWrites(context.Background(), `
        const r = client.tag("auth/login", "critical");
        return r.applied;
    `, write)

	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	if r.Value != true {
		t.Fatalf("expected applied=true, got %v", r.Value)
	}
	if len(r.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(r.Mutations))
	}
	m := r.Mutations[0]
	if m.Op != "tag" || !m.Success || m.UndoID == "" {
		t.Errorf("unexpected mutation record: %+v", m)
	}
	// Undo stack should have one entry.
	if write.UndoStack.Len("test-session") != 1 {
		t.Errorf("expected 1 undo entry, got %d", write.UndoStack.Len("test-session"))
	}
}

func TestExecute_Writes_NoWriteContext(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	// Plain Execute = no write context = write methods not exposed.
	r := ex.Execute(context.Background(), `return typeof client.tag`)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	if r.Value != "undefined" {
		t.Errorf("expected client.tag to be undefined when no write context; got %v", r.Value)
	}
}

func TestExecute_Writes_MutationCap(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	// Spawn 60 nodes (custom type so we can filter without colliding with
	// the seeded ones) and reload so the engine's graph sees them.
	for i := 0; i < 60; i++ {
		rig.writeNode(t, "bulk", "n"+itoa(i), "bulktype", "node "+itoa(i), nil)
	}
	rig.reloadGraph(t)

	ex := NewExecutor(rig.engine)
	write := &WriteContext{
		SessionID: "test-session",
		Namespace: "default",
		UndoStack: curator.NewUndoStack(),
	}

	r := ex.ExecuteWithWrites(context.Background(), `
        const targets = client.listByType("bulktype").map(n => n.id);
        let applied = 0, threw = false;
        try {
            for (const id of targets) {
                client.tag(id, "spammed");
                applied++;
            }
        } catch (e) { threw = true; }
        return { applied, threw, total: targets.length };
    `, write)

	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	res := r.Value.(map[string]any)
	if res["threw"] != true {
		t.Errorf("expected cap to throw on overflow; got %v", res)
	}
	// Successful mutations on the scope should be exactly the cap.
	successCount := 0
	for _, m := range r.Mutations {
		if m.Success {
			successCount++
		}
	}
	if successCount != MaxMutationsPerTurn {
		t.Errorf("expected %d successful mutations, got %d", MaxMutationsPerTurn, successCount)
	}
}

func TestExecute_Writes_Delete(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	write := &WriteContext{
		SessionID: "test-session",
		Namespace: "default",
		UndoStack: curator.NewUndoStack(),
	}

	r := ex.ExecuteWithWrites(context.Background(), `
        return client.delete("db/users");
    `, write)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	res := r.Value.(map[string]any)
	if res["applied"] != true {
		t.Fatalf("expected applied=true, got %v", res)
	}
	if len(r.Mutations) != 1 || r.Mutations[0].Op != "delete" {
		t.Errorf("expected 1 delete mutation, got %+v", r.Mutations)
	}
}

func TestExecute_Writes_Link(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	write := &WriteContext{
		SessionID: "test-session",
		Namespace: "default",
		UndoStack: curator.NewUndoStack(),
	}

	r := ex.ExecuteWithWrites(context.Background(), `
        return client.link("auth/login", "references", "db/users");
    `, write)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	res := r.Value.(map[string]any)
	if res["applied"] != true {
		t.Fatalf("expected applied=true, got %+v", res)
	}
}

func TestExecute_Writes_BadCommand(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	stack := curator.NewUndoStack()
	write := &WriteContext{
		SessionID: "test-session",
		Namespace: "default",
		UndoStack: stack,
	}

	// Tag a node that doesn't exist — should record a failure, not crash.
	r := ex.ExecuteWithWrites(context.Background(), `
        return client.tag("does/not/exist", "x");
    `, write)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	res := r.Value.(map[string]any)
	if res["applied"] != false {
		t.Errorf("expected applied=false for missing node, got %v", res)
	}
	if len(r.Mutations) != 1 || r.Mutations[0].Success {
		t.Errorf("expected one failed mutation record, got %+v", r.Mutations)
	}
	// Failed mutations MUST NOT push an undo entry — otherwise the user
	// could undo a non-mutation and silently corrupt the stack.
	if got := stack.Len("test-session"); got != 0 {
		t.Errorf("expected empty undo stack after failed write, got len=%d", got)
	}
}

func TestExecute_Writes_ReadOnlyGate(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	stack := curator.NewUndoStack()
	write := &WriteContext{
		SessionID: "test-session",
		Namespace: "default",
		UndoStack: stack,
		ReadOnly:  true, // vault is read-only
	}

	// Reads still work; writes throw.
	r := ex.ExecuteWithWrites(context.Background(), `
        const stats = client.getStats();
        try {
            client.tag("auth/login", "x");
            return { read: stats.node_count, threw: false };
        } catch (e) {
            return { read: stats.node_count, threw: true, err: String(e) };
        }
    `, write)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	res := r.Value.(map[string]any)
	if res["threw"] != true {
		t.Errorf("expected write to throw in read-only mode, got %v", res)
	}
	errStr, _ := res["err"].(string)
	if !strings.Contains(strings.ToLower(errStr), "read-only") {
		t.Errorf("expected read-only error message, got %q", errStr)
	}
	// No mutation records, no undo entries.
	if len(r.Mutations) != 0 {
		t.Errorf("expected no mutation records in read-only mode, got %+v", r.Mutations)
	}
	if got := stack.Len("test-session"); got != 0 {
		t.Errorf("expected empty undo stack in read-only mode, got len=%d", got)
	}
}

func TestExecute_Writes_TypeChange(t *testing.T) {
	rig := newTestRig(t)
	defer rig.cleanup()

	ex := NewExecutor(rig.engine)
	write := &WriteContext{
		SessionID: "test-session",
		Namespace: "default",
		UndoStack: curator.NewUndoStack(),
	}

	r := ex.ExecuteWithWrites(context.Background(), `
        return client.setType("auth/login", "module");
    `, write)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	res := r.Value.(map[string]any)
	if res["applied"] != true {
		t.Errorf("expected applied=true, got %v", res)
	}
}

// itoa avoids strconv import.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	out := []byte{}
	for i > 0 {
		out = append([]byte{byte('0' + i%10)}, out...)
		i /= 10
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Test rig (minimal engine fixture)
// ---------------------------------------------------------------------------

// testRig wraps an engine + temp dir for tests that need a real graph and
// node store on disk so write mutations actually persist.
type testRig struct {
	dir    string
	engine *mcpserver.Engine
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	dir := t.TempDir()
	marmotDir := filepath.Join(dir, ".marmot")
	if err := os.MkdirAll(filepath.Join(marmotDir, ".marmot-data"), 0o755); err != nil {
		t.Fatalf("mkdir .marmot-data: %v", err)
	}

	cfg := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write _config.md: %v", err)
	}

	r := &testRig{dir: marmotDir}

	// Seed nodes.
	r.writeNode(t, "auth", "login", "function", "JWT login handler", [][2]string{
		{"db/users", "reads"},
	})
	r.writeNode(t, "auth", "logout", "function", "Session logout", nil)
	r.writeNode(t, "db", "users", "module", "User database access", nil)

	emb := embedding.NewMockEmbedder("mock-test")
	engine, err := mcpserver.NewEngine(marmotDir, emb)
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	r.engine = engine

	// Seed embeddings so search works.
	for _, n := range engine.GetGraph().AllActiveNodes() {
		text := n.Summary
		if text == "" {
			text = n.ID
		}
		vec, err := engine.Embedder.Embed(text)
		if err != nil {
			t.Fatalf("embed %s: %v", n.ID, err)
		}
		h := sha256.Sum256([]byte(text))
		hash := hex.EncodeToString(h[:])
		if err := engine.EmbeddingStore.Upsert(n.ID, vec, hash, engine.Embedder.Model()); err != nil {
			t.Fatalf("upsert embedding for %s: %v", n.ID, err)
		}
	}
	return r
}

func (r *testRig) cleanup() {
	if r.engine != nil {
		_ = r.engine.Close()
	}
}

// writeNode writes a node markdown file at <dir>/<folder>/<name>.md with the
// given type, summary, and outbound edges.
func (r *testRig) writeNode(t *testing.T, folder, name, nodeType, summary string, edges [][2]string) {
	t.Helper()
	id := folder + "/" + name
	if folder == "" {
		id = name
	}
	var buf strings.Builder
	buf.WriteString("---\n")
	fmt.Fprintf(&buf, "id: %s\n", id)
	fmt.Fprintf(&buf, "type: %s\n", nodeType)
	buf.WriteString("namespace: default\n")
	buf.WriteString("status: active\n")
	if len(edges) > 0 {
		buf.WriteString("edges:\n")
		for _, e := range edges {
			fmt.Fprintf(&buf, "    - target: %s\n      relation: %s\n", e[0], e[1])
		}
	}
	buf.WriteString("---\n\n")
	buf.WriteString(summary)
	buf.WriteString("\n")
	path := filepath.Join(r.dir, id+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// reloadGraph forces the engine's graph to reload from disk so subsequent
// calls see nodes added after engine construction.
func (r *testRig) reloadGraph(t *testing.T) {
	t.Helper()
	if r.engine == nil {
		return
	}
	// The engine has no public Reload — re-seed by mutating the graph in
	// place. Tests that need this should use a fresh rig instead.
	g := r.engine.GetGraph()
	metas, err := r.engine.NodeStore.ListNodes()
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	for _, m := range metas {
		path := m.FilePath
		if path == "" {
			path = r.engine.NodeStore.NodePath(m.ID)
		}
		n, err := r.engine.NodeStore.LoadNode(path)
		if err != nil {
			continue
		}
		_ = g.UpsertNode(n)
	}
}
