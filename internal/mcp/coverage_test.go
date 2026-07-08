package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/update"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// ctxEmbedder wraps an embedder and additionally implements the
// contextEmbedder interface so embedWithContext exercises the EmbedContext path.
type ctxEmbedder struct {
	embedding.Embedder
	calls int
}

func (c *ctxEmbedder) EmbedContext(_ context.Context, text string) ([]float32, error) {
	c.calls++
	return c.Embedder.Embed(text)
}

// failEmbedder always fails, exercising embed error branches.
type failEmbedder struct{}

func (failEmbedder) Embed(string) ([]float32, error) { return nil, fmt.Errorf("embed boom") }
func (failEmbedder) EmbedBatch([]string) ([][]float32, error) {
	return nil, fmt.Errorf("embed boom")
}
func (failEmbedder) Model() string  { return "fail-model" }
func (failEmbedder) Dimension() int { return 8 }

// ---------------------------------------------------------------------------
// Engine option setters
// ---------------------------------------------------------------------------

func TestEngineSetters(t *testing.T) {
	eng := testEngine(t)

	eng.WithHeatMap(heatmap.New("default"))
	if eng.HeatMap == nil {
		t.Error("WithHeatMap did not set HeatMap")
	}

	eng.WithSummaryEngine(nil)
	eng.WithUpdateEngine(nil)
	eng.WithSummaryScheduler(nil)
	eng.WithLLMClassifier(nil)
	if eng.Classifier == nil {
		t.Error("WithLLMClassifier did not set Classifier")
	}

	mgr := &namespace.Manager{
		Namespaces: map[string]*namespace.Namespace{"proj": {Name: "proj"}},
		Bridges:    map[string]*namespace.Bridge{},
	}
	eng.WithNamespaceManager(mgr)
	if eng.NSManager != mgr {
		t.Error("WithNamespaceManager did not set NSManager")
	}
}

func TestWithVaultRegistryCachesLocalVaultID(t *testing.T) {
	eng := testEngine(t)

	// Write a config with a vault_id so the setter caches it.
	cfg := &config.VaultConfig{Version: "1", VaultID: "local-vault", Namespace: "default"}
	if err := config.Save(eng.MarmotDir, cfg, ""); err != nil {
		t.Fatalf("save config: %v", err)
	}

	reg := namespace.NewVaultRegistry("local-vault", eng.MarmotDir, nil, nil)
	eng.WithVaultRegistry(reg)

	if eng.VaultRegistry == nil {
		t.Fatal("WithVaultRegistry did not set VaultRegistry")
	}
	if eng.LocalVaultID != "local-vault" {
		t.Errorf("expected cached LocalVaultID=local-vault, got %q", eng.LocalVaultID)
	}
}

// ---------------------------------------------------------------------------
// HasNamespace / NamespaceNames / BridgeSnapshot
// ---------------------------------------------------------------------------

func TestNamespaceIntrospectionNilManager(t *testing.T) {
	eng := testEngine(t)
	if eng.HasNamespace("anything") {
		t.Error("HasNamespace should be false with nil manager")
	}
	if names := eng.NamespaceNames(); names != nil {
		t.Errorf("NamespaceNames should be nil with nil manager, got %v", names)
	}
	local, cross := eng.BridgeSnapshot()
	if local != nil || cross != nil {
		t.Error("BridgeSnapshot should be nil,nil with nil manager")
	}
}

func TestNamespaceIntrospectionWithManager(t *testing.T) {
	eng := testEngine(t)
	crossBridge := &namespace.Bridge{
		SourceVaultID:    "local",
		TargetVaultID:    "remote",
		AllowedRelations: []string{"calls"},
	}
	localBridge := &namespace.Bridge{Source: "a", Target: "b", AllowedRelations: []string{"calls"}}
	mgr := &namespace.Manager{
		Namespaces: map[string]*namespace.Namespace{
			"a": {Name: "a"},
			"b": {Name: "b"},
		},
		Bridges:           map[string]*namespace.Bridge{"a--b": localBridge},
		CrossVaultBridges: []*namespace.Bridge{crossBridge},
	}
	eng.WithNamespaceManager(mgr)

	if !eng.HasNamespace("a") {
		t.Error("HasNamespace(a) should be true")
	}
	if eng.HasNamespace("zzz") {
		t.Error("HasNamespace(zzz) should be false")
	}
	names := eng.NamespaceNames()
	if len(names) != 2 {
		t.Errorf("expected 2 namespace names, got %v", names)
	}
	local, cross := eng.BridgeSnapshot()
	if len(local) != 1 {
		t.Errorf("expected 1 local bridge, got %d", len(local))
	}
	if len(cross) != 1 {
		t.Errorf("expected 1 cross-vault bridge, got %d", len(cross))
	}
}

// ---------------------------------------------------------------------------
// context_tag
// ---------------------------------------------------------------------------

func writeSimpleNode(t *testing.T, eng *Engine, id, ns, summary string) {
	t.Helper()
	args := map[string]any{"id": id, "type": "concept", "summary": summary}
	if ns != "" {
		args["namespace"] = ns
	}
	res, err := eng.HandleContextWrite(context.Background(), makeCallToolRequest("context_write", args))
	if err != nil || res.IsError {
		t.Fatalf("write %s failed: %v / %s", id, err, resultText(t, res))
	}
}

func TestHandleContextTag(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeSimpleNode(t, eng, "auth/login", "", "OAuth2 login flow with tokens")
	writeSimpleNode(t, eng, "auth/session", "", "OAuth2 session management")

	res, err := eng.HandleContextTag(ctx, makeCallToolRequest("context_tag", map[string]any{
		"query": "OAuth2 authentication",
		"tag":   "security",
		"limit": 5,
	}))
	if err != nil {
		t.Fatalf("HandleContextTag: %v", err)
	}
	if res.IsError {
		t.Fatalf("tag returned error: %s", resultText(t, res))
	}
	var tr TagResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tr.Tag != "security" {
		t.Errorf("expected tag=security, got %s", tr.Tag)
	}
	if tr.Count == 0 {
		t.Error("expected at least one tagged node")
	}
	// The tag must be persisted on at least one node.
	found := false
	for _, id := range tr.TaggedIDs {
		if n, ok := eng.GetGraph().GetNode(id); ok {
			for _, tag := range n.Tags {
				if tag == "security" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected security tag persisted on a node")
	}

	// Second identical tag call hits the "already has tag" branch.
	res2, err := eng.HandleContextTag(ctx, makeCallToolRequest("context_tag", map[string]any{
		"query": "OAuth2 authentication",
		"tag":   "security",
	}))
	if err != nil || res2.IsError {
		t.Fatalf("second tag failed: %v", err)
	}
}

func TestHandleContextTagValidation(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Missing query.
	res, _ := eng.HandleContextTag(ctx, makeCallToolRequest("context_tag", map[string]any{"tag": "x"}))
	if !res.IsError {
		t.Error("expected error when query missing")
	}
	// Missing tag.
	res, _ = eng.HandleContextTag(ctx, makeCallToolRequest("context_tag", map[string]any{"query": "x"}))
	if !res.IsError {
		t.Error("expected error when tag missing")
	}
	// Empty store: SearchActive returns error -> empty TagResult, no error.
	res, _ = eng.HandleContextTag(ctx, makeCallToolRequest("context_tag", map[string]any{
		"query": "nothing here", "tag": "t", "limit": 9999,
	}))
	if res.IsError {
		t.Errorf("expected graceful empty result, got error: %s", resultText(t, res))
	}
}

func TestHandleContextTagNamespaceFilter(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeSimpleNode(t, eng, "billing/invoice", "billing", "Invoice generation subsystem")

	// Tag with a mismatched namespace filter — node should be skipped.
	res, err := eng.HandleContextTag(ctx, makeCallToolRequest("context_tag", map[string]any{
		"query":     "invoice generation",
		"tag":       "finance",
		"namespace": "other",
	}))
	if err != nil || res.IsError {
		t.Fatalf("tag failed: %v", err)
	}
	var tr TagResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tr.Count != 0 {
		t.Errorf("expected 0 tagged (namespace mismatch), got %d", tr.Count)
	}
}

func TestMatchNamespace(t *testing.T) {
	cases := []struct {
		nodeNS, requested string
		want              bool
	}{
		{"default", "default", true},
		{"", "default", true},
		{"default", "", true},
		{"a", "a", true},
		{"a", "b", false},
		{"", "a", false},
	}
	for _, c := range cases {
		if got := matchNamespace(c.nodeNS, c.requested); got != c.want {
			t.Errorf("matchNamespace(%q,%q)=%v want %v", c.nodeNS, c.requested, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// context_namespace remove / update / errors
// ---------------------------------------------------------------------------

func nsReq(action, name string, extra map[string]any) map[string]any {
	m := map[string]any{"action": action}
	if name != "" {
		m["name"] = name
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestHandleContextNamespaceRemove(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Create then remove an empty namespace.
	if res, _ := eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("create", "emptyns", nil))); res.IsError {
		t.Fatalf("create emptyns: %s", resultText(t, res))
	}
	res, err := eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("remove", "emptyns", nil)))
	if err != nil || res.IsError {
		t.Fatalf("remove emptyns failed: %v / %s", err, resultText(t, res))
	}
	var out NamespaceToolResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.Removed {
		t.Error("expected Removed=true")
	}

	// Namespace with nodes: removing without force fails, with force succeeds.
	writeSimpleNode(t, eng, "proj/thing", "proj", "A project thing")
	res, _ = eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("remove", "proj", nil)))
	if !res.IsError {
		t.Error("expected error removing populated namespace without force")
	}
	res, _ = eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("remove", "proj", map[string]any{"force": true})))
	if res.IsError {
		t.Errorf("force remove should succeed: %s", resultText(t, res))
	}
}

func TestHandleContextNamespaceRemoveErrors(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	cases := []struct{ name string }{
		{""},          // missing name
		{"default"},   // protected
		{"../escape"}, // invalid name
		{"ghost"},     // valid name, no manifest -> os.Remove fails
	}
	for _, c := range cases {
		res, err := eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("remove", c.name, nil)))
		if err != nil {
			t.Fatalf("remove %q: %v", c.name, err)
		}
		if !res.IsError {
			t.Errorf("expected error removing %q", c.name)
		}
	}
}

func TestHandleContextNamespaceUpdate(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	if res, _ := eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("create", "ops", map[string]any{"root_path": "../ops"}))); res.IsError {
		t.Fatalf("create ops: %s", resultText(t, res))
	}
	res, err := eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("update", "ops", map[string]any{"root_path": "../ops-v2"})))
	if err != nil || res.IsError {
		t.Fatalf("update ops failed: %v / %s", err, resultText(t, res))
	}
	var out NamespaceToolResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Namespace == nil || out.Namespace.RootPath != "../ops-v2" {
		t.Errorf("expected updated root_path, got %+v", out.Namespace)
	}

	// Update missing name.
	if res, _ := eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("update", "", nil))); !res.IsError {
		t.Error("expected error updating without name")
	}
	// Update namespace that has no manifest -> load fails.
	if res, _ := eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("update", "nonexistent", nil))); !res.IsError {
		t.Error("expected error updating nonexistent namespace")
	}
}

func TestHandleContextNamespaceCreateMissingName(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	if res, _ := eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("create", "", nil))); !res.IsError {
		t.Error("expected error creating without name")
	}
}

func TestHandleContextNamespaceUnknownAction(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	res, err := eng.HandleContextNamespace(ctx, makeCallToolRequest("context_namespace", nsReq("bogus", "", nil)))
	if err != nil {
		t.Fatalf("HandleContextNamespace: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for unknown action")
	}
}

// ---------------------------------------------------------------------------
// cross-namespace & cross-vault edge validation
// ---------------------------------------------------------------------------

func TestValidateCrossNamespaceEdges(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Create two namespaces on disk.
	if _, _, err := namespace.EnsureNamespace(eng.MarmotDir, "svc", ""); err != nil {
		t.Fatalf("ensure svc: %v", err)
	}
	if _, _, err := namespace.EnsureNamespace(eng.MarmotDir, "lib", ""); err != nil {
		t.Fatalf("ensure lib: %v", err)
	}
	mgr, err := namespace.NewManager(eng.MarmotDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	eng.WithNamespaceManager(mgr)

	// Write a node in svc with a cross-namespace edge to lib without a bridge -> rejected.
	res, _ := eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":        "handler",
		"namespace": "svc",
		"type":      "function",
		"summary":   "svc handler calling into lib",
		"edges":     []map[string]any{{"target": "lib/util", "relation": "calls"}},
	}))
	if !res.IsError {
		t.Fatal("expected cross-namespace edge without bridge to be rejected")
	}

	// Create a bridge allowing calls, refresh manager, retry -> allowed.
	if _, err := namespace.CreateBridge(eng.MarmotDir, "svc", "lib", []string{"calls"}); err != nil {
		t.Fatalf("create bridge: %v", err)
	}
	mgr2, err := namespace.NewManager(eng.MarmotDir)
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	eng.WithNamespaceManager(mgr2)

	res, _ = eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":        "handler",
		"namespace": "svc",
		"type":      "function",
		"summary":   "svc handler calling into lib",
		"edges":     []map[string]any{{"target": "lib/util", "relation": "calls"}},
	}))
	if res.IsError {
		t.Fatalf("cross-namespace edge with bridge should be allowed: %s", resultText(t, res))
	}
}

func TestValidateCrossVaultEdges(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	eng.LocalVaultID = "local"
	eng.VaultRegistry = namespace.NewVaultRegistry("local", eng.MarmotDir, nil, nil)
	eng.WithNamespaceManager(&namespace.Manager{
		Namespaces: map[string]*namespace.Namespace{},
		Bridges:    map[string]*namespace.Bridge{},
		CrossVaultBridges: []*namespace.Bridge{{
			SourceVaultID:    "local",
			TargetVaultID:    "remote",
			AllowedRelations: []string{"calls"},
		}},
	})

	// Allowed relation to the bridged vault.
	res, _ := eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "gw/allowed",
		"type":    "module",
		"summary": "gateway calling remote",
		"edges":   []map[string]any{{"target": "@remote/svc/api", "relation": "calls"}},
	}))
	if res.IsError {
		t.Fatalf("allowed cross-vault edge rejected: %s", resultText(t, res))
	}

	// Disallowed relation on the same bridge.
	res, _ = eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "gw/disallowed",
		"type":    "module",
		"summary": "gateway writing remote",
		"edges":   []map[string]any{{"target": "@remote/svc/api", "relation": "writes"}},
	}))
	if !res.IsError {
		t.Fatal("expected disallowed cross-vault relation to be rejected")
	}

	// Unknown vault -> no bridge.
	res, _ = eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "gw/unknown",
		"type":    "module",
		"summary": "gateway calling unknown vault",
		"edges":   []map[string]any{{"target": "@nowhere/svc/api", "relation": "calls"}},
	}))
	if !res.IsError {
		t.Fatal("expected edge to unbridged vault to be rejected")
	}
}

// ---------------------------------------------------------------------------
// ResolveNodeID & reindexNeighbors
// ---------------------------------------------------------------------------

func TestResolveNodeIDWithNamespacePrefix(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Writing in a namespace registers it with the manager and prefixes the ID.
	writeSimpleNode(t, eng, "widget", "ui", "A UI widget")

	// Bare ID should resolve to the namespace-prefixed node.
	n, ok := eng.ResolveNodeID("widget")
	if !ok {
		t.Fatal("expected bare 'widget' to resolve via namespace prefix")
	}
	if n.ID != "ui/widget" {
		t.Errorf("expected resolved ID ui/widget, got %s", n.ID)
	}

	// Fully-qualified ID resolves directly.
	if _, ok := eng.ResolveNodeID("ui/widget"); !ok {
		t.Error("expected ui/widget to resolve directly")
	}

	// Unknown ID does not resolve.
	if _, ok := eng.ResolveNodeID("does/not-exist"); ok {
		t.Error("expected unknown ID to not resolve")
	}

	// Delete via the bare (prefixed-resolvable) ID exercises the delete resolve path.
	res, _ := eng.HandleContextDelete(ctx, makeCallToolRequest("context_delete", map[string]any{"id": "widget"}))
	if res.IsError {
		t.Errorf("delete via bare id failed: %s", resultText(t, res))
	}
}

func TestReindexNeighbors(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	ue := update.NewEngine(eng.NodeStore, eng.GetGraph(), eng.EmbeddingStore, eng.Embedder)
	eng.WithUpdateEngine(ue)

	// A depends on B (A -> B). Writing/deleting B should schedule a reindex of A.
	writeSimpleNode(t, eng, "svc/a", "", "service A")
	res, err := eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "svc/a",
		"type":    "function",
		"summary": "service A depends on B",
		"edges":   []map[string]any{{"target": "svc/b", "relation": "calls"}},
	}))
	if err != nil || res.IsError {
		t.Fatalf("write svc/a with edge failed")
	}
	// Write B — reindexNeighbors(svc/b) finds svc/a as a dependent neighbor.
	writeSimpleNode(t, eng, "svc/b", "", "service B")

	// Delete B — again triggers the neighbor reindex path.
	if res, _ := eng.HandleContextDelete(ctx, makeCallToolRequest("context_delete", map[string]any{"id": "svc/b"})); res.IsError {
		t.Errorf("delete svc/b failed: %s", resultText(t, res))
	}

	// Close drains the background reindex goroutines deterministically before
	// releasing the embedding store (no timing dependence).
	if err := eng.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// context_delete error paths
// ---------------------------------------------------------------------------

func TestHandleContextDeleteErrors(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Missing id.
	if res, _ := eng.HandleContextDelete(ctx, makeCallToolRequest("context_delete", map[string]any{})); !res.IsError {
		t.Error("expected error when id missing")
	}
	// Nonexistent node.
	if res, _ := eng.HandleContextDelete(ctx, makeCallToolRequest("context_delete", map[string]any{"id": "no/such"})); !res.IsError {
		t.Error("expected error deleting nonexistent node")
	}

	// Delete twice: second delete sees a superseded node.
	writeSimpleNode(t, eng, "del/twice", "", "to be deleted twice")
	if res, _ := eng.HandleContextDelete(ctx, makeCallToolRequest("context_delete", map[string]any{"id": "del/twice"})); res.IsError {
		t.Fatalf("first delete failed: %s", resultText(t, res))
	}
	if res, _ := eng.HandleContextDelete(ctx, makeCallToolRequest("context_delete", map[string]any{"id": "del/twice"})); !res.IsError {
		t.Error("expected error deleting already-superseded node")
	}
}

func TestHandleContextDeleteWithSupersededBy(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	writeSimpleNode(t, eng, "old/impl", "", "old implementation")
	writeSimpleNode(t, eng, "new/impl", "", "new implementation")

	res, err := eng.HandleContextDelete(ctx, makeCallToolRequest("context_delete", map[string]any{
		"id":            "old/impl",
		"superseded_by": "new/impl",
	}))
	if err != nil || res.IsError {
		t.Fatalf("delete with superseded_by failed: %v / %s", err, resultText(t, res))
	}
	var dr DeleteResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &dr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dr.SupersededBy != "new/impl" {
		t.Errorf("expected superseded_by=new/impl, got %s", dr.SupersededBy)
	}
}

// ---------------------------------------------------------------------------
// query variations: heatmap, embed error, clamping, EmbedContext, superseded
// ---------------------------------------------------------------------------

func TestQueryWithHeatMap(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	eng.WithHeatMap(heatmap.New("default"))

	// Connected nodes so traversal yields >= 2 nodes and co-access is recorded.
	res, err := eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "auth/login",
		"type":    "function",
		"summary": "OAuth2 login handler",
		"edges":   []map[string]any{{"target": "auth/token", "relation": "calls"}},
	}))
	if err != nil || res.IsError {
		t.Fatalf("write login failed")
	}
	writeSimpleNode(t, eng, "auth/token", "", "OAuth2 token issuance")

	qres, err := eng.HandleContextQuery(ctx, makeCallToolRequest("context_query", map[string]any{
		"query":  "OAuth2 login",
		"budget": 8192,
	}))
	if err != nil || qres.IsError {
		t.Fatalf("query failed: %v", err)
	}
	// Heat data should have been persisted.
	if _, err := os.Stat(filepath.Join(eng.MarmotDir, "_heat")); err != nil && !os.IsNotExist(err) {
		t.Logf("heat dir stat: %v", err)
	}
}

func TestQueryEmbedError(t *testing.T) {
	eng := testEngine(t)
	eng.Embedder = failEmbedder{}
	res, err := eng.HandleContextQuery(context.Background(), makeCallToolRequest("context_query", map[string]any{
		"query": "anything",
	}))
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	if !res.IsError {
		t.Error("expected embed error to surface")
	}
}

func TestWriteEmbedError(t *testing.T) {
	eng := testEngine(t)
	eng.Embedder = failEmbedder{}
	res, err := eng.HandleContextWrite(context.Background(), makeCallToolRequest("context_write", map[string]any{
		"id":      "x/y",
		"type":    "concept",
		"summary": "will fail embedding",
	}))
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if !res.IsError {
		t.Error("expected embed error to surface on write")
	}
}

func TestQueryEmbedContextPathAndClamping(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	wrap := &ctxEmbedder{Embedder: eng.Embedder}
	eng.Embedder = wrap

	writeSimpleNode(t, eng, "doc/one", "", "documentation about pagination")

	// depth and budget out of range should be clamped, mode + include_superseded set.
	res, err := eng.HandleContextQuery(ctx, makeCallToolRequest("context_query", map[string]any{
		"query":              "pagination docs",
		"depth":              99,     // clamped to default
		"budget":             500000, // clamped to 100000
		"mode":               "priority",
		"include_superseded": true,
	}))
	if err != nil || res.IsError {
		t.Fatalf("query failed: %v", err)
	}
	if wrap.calls == 0 {
		t.Error("expected EmbedContext path to be used")
	}

	// Negative depth also clamps.
	if res, _ := eng.HandleContextQuery(ctx, makeCallToolRequest("context_query", map[string]any{
		"query": "pagination docs",
		"depth": -5,
	})); res.IsError {
		t.Errorf("negative depth query failed: %s", resultText(t, res))
	}
}

// ---------------------------------------------------------------------------
// verify: staleness error + all check
// ---------------------------------------------------------------------------

func TestVerifyStalenessMissingSourceFile(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Node references a source file that does not exist, with a stored hash.
	res, err := eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "stale/node",
		"type":    "function",
		"summary": "references a missing file",
		"source":  map[string]any{"path": "does/not/exist.go", "hash": "abc123"},
	}))
	if err != nil || res.IsError {
		t.Fatalf("write failed: %s", resultText(t, res))
	}

	vres, err := eng.HandleContextVerify(ctx, makeCallToolRequest("context_verify", map[string]any{
		"node_ids": []string{"stale/node"},
		"check":    "staleness",
	}))
	if err != nil || vres.IsError {
		t.Fatalf("verify failed: %v", err)
	}
	var vr VerifyResult
	if err := json.Unmarshal([]byte(resultText(t, vres)), &vr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if vr.Total == 0 {
		t.Error("expected a staleness issue for missing source file")
	}
}

// ---------------------------------------------------------------------------
// defaultTokenBudget with malformed config
// ---------------------------------------------------------------------------

func TestDefaultTokenBudgetMalformedConfig(t *testing.T) {
	eng := testEngine(t)
	// Write a malformed _config.md so config.Load returns an error.
	if err := os.WriteFile(filepath.Join(eng.MarmotDir, "_config.md"), []byte("---\n: not valid yaml :\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := eng.defaultTokenBudget(); got != config.DefaultTokenBudget {
		t.Errorf("expected fallback budget %d, got %d", config.DefaultTokenBudget, got)
	}

	// Empty MarmotDir path also falls back.
	empty := &Engine{}
	if got := empty.defaultTokenBudget(); got != config.DefaultTokenBudget {
		t.Errorf("expected fallback budget for empty dir, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// ListenStdio
// ---------------------------------------------------------------------------

func TestListenStdioReturnsOnEOF(t *testing.T) {
	eng := testEngine(t)
	srv := NewServer(eng)

	// An empty stdin yields immediate EOF, so Listen returns promptly.
	var out bytes.Buffer
	err := srv.ListenStdio(context.Background(), bytes.NewReader(nil), &out)
	// EOF is reported as an error by the underlying transport; either nil or a
	// non-panic error is acceptable — we only need the call to return.
	_ = err
}
