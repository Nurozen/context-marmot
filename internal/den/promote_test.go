package den

// Promote engine coverage (§15.3 sibling — promote-on-destroy). Promote
// reuses Contribute's classify-and-apply core but writes into a LIVE local
// target vault: node files through the target node.Store AND the target's
// embeddings.db (unlike contribute, which never touches embeddings). These
// tests reuse the contribute test helpers (contributeMount, saveVaultNode,
// saveTargetNode, seedTargetEmbedding, mockVec, vecAtCosine, withMockClassifierLLM)
// and treat the mount's Path as the target vault directory.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/classifier"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/warren"
)

// promoteEmbeddingsPath is the target vault's embeddings.db.
func promoteEmbeddingsPath(vaultDir string) string {
	return filepath.Join(vaultDir, ".marmot-data", "embeddings.db")
}

// openTargetEmbeddings opens the target vault's embeddings.db read-only for
// assertions. Fails the test if it is absent.
func openTargetEmbeddings(t *testing.T, vaultDir string) *embedding.Store {
	t.Helper()
	st, err := embedding.NewStoreReadOnly(promoteEmbeddingsPath(vaultDir))
	if err != nil {
		t.Fatalf("open target embeddings.db read-only: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// activeEmbeddingIDs returns the set of active node ids whose stored vector is
// similar to want (used to prove a promoted node was embedded). FindSimilar
// filters to active status and to the model, matching the target embedder.
func activeEmbeddingIDs(t *testing.T, st *embedding.Store, want []float32) map[string]bool {
	t.Helper()
	matches, err := st.FindSimilar(want, 0.5, contributeMockModel)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range matches {
		ids[m.NodeID] = true
	}
	return ids
}

// TestPromoteFreshTargetAddsAndEmbeds: a fresh target (no embeddings.db) folds
// the source's active nodes in as plain ADDs, creates the embeddings.db, and
// upserts a vector for every promoted node.
func TestPromoteFreshTargetAddsAndEmbeds(t *testing.T) {
	source := contributeVault(t)
	target := contributeMount(t).Path
	saveVaultNode(t, source, "notes/alpha", "Alpha summary", "Alpha context")
	saveVaultNode(t, source, "notes/beta", "Beta summary", "")

	res, err := Promote(context.Background(), source, target, false)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.Counts != (FlowCounts{Added: 2}) {
		t.Fatalf("counts = %+v, want {Added:2} (warnings: %v)", res.Counts, res.Warnings)
	}
	// Node files written into the target.
	tgt := node.NewStore(target)
	for _, id := range []string{"notes/alpha", "notes/beta"} {
		n, err := tgt.LoadNode(tgt.NodePath(id))
		if err != nil {
			t.Fatalf("target node %s not written: %v", id, err)
		}
		if n.Status != node.StatusActive {
			t.Fatalf("target node %s status = %q", id, n.Status)
		}
	}
	// Embeddings.db created and holds a vector for each promoted node.
	if _, err := os.Stat(promoteEmbeddingsPath(target)); err != nil {
		t.Fatalf("promote did not create embeddings.db: %v", err)
	}
	st := openTargetEmbeddings(t, target)
	if got := st.Count(); got != 2 {
		t.Fatalf("embedding row count = %d, want 2", got)
	}
	alphaVec := mockVec(t, warren.EmbedText(&node.Node{Summary: "Alpha summary", Context: "Alpha context"}))
	if !activeEmbeddingIDs(t, st, alphaVec)["notes/alpha"] {
		t.Fatalf("notes/alpha not embedded in target")
	}
}

// TestPromoteDeterministicNoopAndUpdate: same id present in the target — noop
// on identical content, update (re-embedding) on changed content.
func TestPromoteDeterministicNoopAndUpdate(t *testing.T) {
	source := contributeVault(t)
	mount := contributeMount(t)
	target := mount.Path
	saveVaultNode(t, source, "notes/alpha", "Alpha summary", "Alpha context")

	// First promote: ADD.
	if _, err := Promote(context.Background(), source, target, false); err != nil {
		t.Fatalf("first Promote: %v", err)
	}
	// Second promote, unchanged: NOOP (the idempotency backbone).
	res, err := Promote(context.Background(), source, target, false)
	if err != nil {
		t.Fatalf("second Promote: %v", err)
	}
	if res.Counts != (FlowCounts{Noop: 1}) {
		t.Fatalf("unchanged re-promote counts = %+v, want {Noop:1}", res.Counts)
	}

	// Change the source node: deterministic UPDATE, and the embedding is
	// re-upserted with the new content's vector.
	saveVaultNode(t, source, "notes/alpha", "Alpha summary v2", "Alpha context v2")
	res, err = Promote(context.Background(), source, target, false)
	if err != nil {
		t.Fatalf("update Promote: %v", err)
	}
	if res.Counts != (FlowCounts{Updated: 1}) {
		t.Fatalf("update counts = %+v, want {Updated:1}", res.Counts)
	}
	got := loadTargetNode(t, mount, "notes/alpha")
	if got.Summary != "Alpha summary v2" {
		t.Fatalf("target not updated: %q", got.Summary)
	}
	st := openTargetEmbeddings(t, target)
	newVec := mockVec(t, warren.EmbedText(&node.Node{Summary: "Alpha summary v2", Context: "Alpha context v2"}))
	if !activeEmbeddingIDs(t, st, newVec)["notes/alpha"] {
		t.Fatalf("updated node embedding not re-upserted with new content")
	}
}

// TestPromoteDryRunWritesNothing: dry-run plans the ADD but writes no node
// file and — crucially — does not even create the embeddings.db/.marmot-data.
func TestPromoteDryRunWritesNothing(t *testing.T) {
	source := contributeVault(t)
	target := contributeMount(t).Path
	saveVaultNode(t, source, "notes/alpha", "Alpha summary", "")

	res, err := Promote(context.Background(), source, target, true)
	if err != nil {
		t.Fatalf("Promote dry-run: %v", err)
	}
	if res.Counts != (FlowCounts{Added: 1}) {
		t.Fatalf("dry-run counts = %+v, want {Added:1}", res.Counts)
	}
	if len(res.Ops) != 1 || res.Ops[0].String() != "add node notes/alpha" {
		t.Fatalf("ops = %+v", res.Ops)
	}
	if _, err := os.Stat(filepath.Join(target, "notes", "alpha.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a node file (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".marmot-data")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created .marmot-data (stat err=%v)", err)
	}
	if len(res.CreatedFiles) != 0 || len(res.ModifiedFiles) != 0 {
		t.Fatalf("dry-run recorded file effects: created %v modified %v", res.CreatedFiles, res.ModifiedFiles)
	}
}

// TestPromoteClassifierLLMUpdate: the injected LLM says UPDATE against an
// existing target id (the source id is absent from the target). Promote
// applies the source content onto the target node and re-embeds it.
func TestPromoteClassifierLLMUpdate(t *testing.T) {
	source := contributeVault(t)
	mount := contributeMount(t)
	target := mount.Path
	const summary = "Sessions are stored in Redis with a 30m sliding TTL"
	const contextBody = "Moved from sticky in-memory sessions to Redis in the v3 rollout."
	saveVaultNode(t, source, "notes/sessions", summary, contextBody)
	saveTargetNode(t, mount, "legacy/session-notes", "Old session summary", "Old session context")
	incoming := mockVec(t, warren.EmbedText(&node.Node{Summary: summary, Context: contextBody}))
	seedTargetEmbedding(t, mount, "legacy/session-notes", vecAtCosine(t, incoming, 0.90))

	mock := &llm.MockProvider{Result: llm.ClassifyResult{
		Action:       llm.ActionUPDATE,
		TargetNodeID: "legacy/session-notes",
		Confidence:   0.9,
	}}
	withMockClassifierLLM(t, mock)

	res, err := Promote(context.Background(), source, target, false)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if mock.Calls != 1 {
		t.Fatalf("LLM Classify calls = %d, want 1", mock.Calls)
	}
	if res.Counts != (FlowCounts{Updated: 1}) {
		t.Fatalf("counts = %+v, want {Updated:1} (warnings: %v)", res.Counts, res.Warnings)
	}
	got := loadTargetNode(t, mount, "legacy/session-notes")
	if got.Status != node.StatusActive || got.Summary != summary {
		t.Fatalf("target after LLM update = status %q summary %q", got.Status, got.Summary)
	}
	if _, serr := os.Stat(filepath.Join(target, "notes", "sessions.md")); !os.IsNotExist(serr) {
		t.Fatalf("LLM UPDATE also created the incoming id (stat err=%v)", serr)
	}
	// The target node's embedding was re-upserted with the source content.
	st := openTargetEmbeddings(t, target)
	if !activeEmbeddingIDs(t, st, incoming)["legacy/session-notes"] {
		t.Fatalf("updated target embedding not re-upserted")
	}
}

// TestPromoteClassifierSupersede: embedding-distance fallback (LLM nil) lands
// in the SUPERSEDE band — the target node file is retired (status superseded,
// superseded_by set) and its embedding row flipped to superseded, while the
// incoming node is created active with a fresh embedding row. Unlike
// contribute, promote DOES maintain the embeddings.db here.
func TestPromoteClassifierSupersede(t *testing.T) {
	source := contributeVault(t)
	mount := contributeMount(t)
	target := mount.Path
	const summary = "Auth tokens are rotated hourly by the session service"
	const contextBody = "Rotation moved from the gateway into the session service in v2."
	saveVaultNode(t, source, "notes/token-rotation", summary, contextBody)
	saveTargetNode(t, mount, "legacy/gateway-rotation", "Auth tokens are rotated by the gateway", "Pre-v2 design.")

	incoming := mockVec(t, warren.EmbedText(&node.Node{Summary: summary, Context: contextBody}))
	stored := vecAtCosine(t, incoming, 0.90)
	if sim := storeSimilarity(incoming, stored); sim < classifier.ThresholdSUPERSEDE || sim >= classifier.ThresholdUPDATE {
		t.Fatalf("crafted similarity %v outside SUPERSEDE band", sim)
	}
	seedTargetEmbedding(t, mount, "legacy/gateway-rotation", stored)

	res, err := Promote(context.Background(), source, target, false)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.Counts != (FlowCounts{Superseded: 1}) {
		t.Fatalf("counts = %+v, want {Superseded:1} (warnings: %v)", res.Counts, res.Warnings)
	}
	old := loadTargetNode(t, mount, "legacy/gateway-rotation")
	if old.Status != node.StatusSuperseded || old.SupersededBy != "notes/token-rotation" {
		t.Fatalf("retired target = status %q superseded_by %q", old.Status, old.SupersededBy)
	}
	repl := loadTargetNode(t, mount, "notes/token-rotation")
	if repl.Status != node.StatusActive || repl.Summary != summary {
		t.Fatalf("replacement = status %q summary %q", repl.Status, repl.Summary)
	}
	// Embeddings maintained: replacement present (active), retired row flipped
	// to superseded (so FindSimilar's active-only scan no longer returns it).
	st := openTargetEmbeddings(t, target)
	active := activeEmbeddingIDs(t, st, incoming)
	if !active["notes/token-rotation"] {
		t.Fatalf("replacement embedding not upserted: active=%v", active)
	}
	if active["legacy/gateway-rotation"] {
		t.Fatalf("retired target embedding still active (UpdateStatus not applied)")
	}
}

// TestPromoteEmbeddingFailureDegradesToWarning: an embeddings.db seeded at a
// mismatched dimension makes the vector upsert fail, but the node file is
// still written and the promote succeeds with a per-node warning — knowledge
// is never lost to an embedding problem.
func TestPromoteEmbeddingFailureDegradesToWarning(t *testing.T) {
	source := contributeVault(t)
	mount := contributeMount(t)
	target := mount.Path
	saveVaultNode(t, source, "notes/solo", "Solo summary", "Solo context")

	// Seed a wrong-dimension row so any real (256-dim mock) upsert is rejected
	// with a dimension mismatch — the store exists, so promote opens it and
	// tries to upsert the promoted node's vector.
	dataDir := filepath.Join(target, ".marmot-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed, err := embedding.NewStore(promoteEmbeddingsPath(target))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Upsert("seed/wrong-dim", []float32{1, 0, 0}, "h", "other-model"); err != nil {
		t.Fatalf("seed wrong-dim embedding: %v", err)
	}
	_ = seed.Close()

	res, err := Promote(context.Background(), source, target, false)
	if err != nil {
		t.Fatalf("Promote must not fail on embedding errors: %v", err)
	}
	if res.Counts != (FlowCounts{Added: 1}) {
		t.Fatalf("counts = %+v, want {Added:1} (warnings: %v)", res.Counts, res.Warnings)
	}
	// Node file durably written despite the embedding failure.
	if got := loadTargetNode(t, mount, "notes/solo").Summary; got != "Solo summary" {
		t.Fatalf("node file not written on embedding failure: %q", got)
	}
	if !hasWarning(res.Warnings, "embedding upsert failed") {
		t.Fatalf("warnings = %v, want an 'embedding upsert failed' warning", res.Warnings)
	}
}

// TestPromoteConfigUnreadableDisablesEmbedding: an unreadable target
// _config.md (a directory in its place) disables classification AND embedding
// with a warning; nodes are still folded in as plain ADDs (file-only).
func TestPromoteConfigUnreadableDisablesEmbedding(t *testing.T) {
	source := contributeVault(t)
	target := contributeMount(t).Path
	saveVaultNode(t, source, "notes/solo", "Solo summary", "")
	// config.Load reads _config.md; a directory there makes it unreadable.
	if err := os.MkdirAll(filepath.Join(target, "_config.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Promote(context.Background(), source, target, false)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.Counts != (FlowCounts{Added: 1}) {
		t.Fatalf("counts = %+v, want {Added:1}", res.Counts)
	}
	if !hasWarning(res.Warnings, "config unreadable") {
		t.Fatalf("warnings = %v, want a 'config unreadable' warning", res.Warnings)
	}
	tgt := node.NewStore(target)
	if _, err := tgt.LoadNode(tgt.NodePath("notes/solo")); err != nil {
		t.Fatalf("node not written with embedding disabled: %v", err)
	}
	// No embeddings.db created when embedding is disabled.
	if _, err := os.Stat(promoteEmbeddingsPath(target)); !os.IsNotExist(err) {
		t.Fatalf("embeddings.db created despite disabled embedding (stat err=%v)", err)
	}
}

// TestPromoteEmptySourceIsZeroWork: an empty/absent source vault folds nothing
// and never crashes (the CLI teardown owns the source's existence checks).
func TestPromoteEmptySourceIsZeroWork(t *testing.T) {
	target := contributeMount(t).Path
	source := filepath.Join(t.TempDir(), "nonexistent-vault")
	res, err := Promote(context.Background(), source, target, false)
	if err != nil {
		t.Fatalf("absent source dir should walk to zero nodes, got err: %v", err)
	}
	if res.Counts != (FlowCounts{}) || len(res.Ops) != 0 {
		t.Fatalf("absent source result = %+v, want empty", res)
	}
}
