package den

// Classifier-second-pass coverage for Contribute: the branch where the
// target mount has an embeddings.db and the incoming den node's ID is
// absent from the target, so the outcome is decided by the classifier's
// embedding-distance fallback (LLM nil).
//
// The fixture avoids threshold-hunting with real text pairs: the target's
// stored vector is CONSTRUCTED at an exact cosine from the incoming node's
// mock-embedder vector (both sides use the mock embedder — the target vault
// has no _config.md, so config.Load defaults to provider "mock", model
// "mock-v1"). FindSimilar scores similarity as 1/(1+L2distance) over unit
// vectors, so cosine c maps to similarity 1/(1+sqrt(2-2c)); each test
// asserts empirically that its crafted score lands in the intended
// classifier band before running Contribute.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/classifier"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/warren"
)

// contributeMockModel is the model Contribute resolves for a target vault
// with no _config.md: default provider "mock", default model name "mock-v1".
const contributeMockModel = "mock-v1"

// mockVec embeds text exactly as the classifier will: the same deterministic
// mock embedder the target vault's default config yields.
func mockVec(t *testing.T, text string) []float32 {
	t.Helper()
	v, err := embedding.NewMockEmbedder(contributeMockModel).Embed(text)
	if err != nil {
		t.Fatalf("mock embed: %v", err)
	}
	return v
}

// vecAtCosine returns a unit vector at exactly cosTheta from the unit
// vector v (Gram-Schmidt against a basis direction).
func vecAtCosine(t *testing.T, v []float32, cosTheta float64) []float32 {
	t.Helper()
	for dim := 0; dim < len(v); dim++ {
		u := make([]float64, len(v))
		u[dim] = 1
		dot := float64(v[dim]) // <e_dim, v>
		var norm float64
		for i := range u {
			u[i] -= dot * float64(v[i])
			norm += u[i] * u[i]
		}
		norm = math.Sqrt(norm)
		if norm < 1e-6 {
			continue // e_dim (anti)parallel to v; try another basis direction
		}
		sinTheta := math.Sqrt(1 - cosTheta*cosTheta)
		out := make([]float32, len(v))
		for i := range out {
			out[i] = float32(cosTheta*float64(v[i]) + sinTheta*u[i]/norm)
		}
		return out
	}
	t.Fatal("vecAtCosine: no orthogonal direction found")
	return nil
}

// storeSimilarity mirrors FindSimilar's scoring: 1 / (1 + L2 distance).
func storeSimilarity(a, b []float32) float64 {
	var d float64
	for i := range a {
		diff := float64(a[i]) - float64(b[i])
		d += diff * diff
	}
	return 1.0 / (1.0 + math.Sqrt(d))
}

// seedTargetEmbedding writes vec for nodeID into the target mount's
// .marmot-data/embeddings.db (creating it, as embedding.NewStore does).
func seedTargetEmbedding(t *testing.T, mount warren.ProjectStatus, nodeID string, vec []float32) {
	t.Helper()
	dataDir := filepath.Join(mount.Path, ".marmot-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := embedding.NewStore(filepath.Join(dataDir, "embeddings.db"))
	if err != nil {
		t.Fatalf("open target embeddings.db: %v", err)
	}
	defer func() { _ = st.Close() }()
	sum := sha256.Sum256([]byte("seed:" + nodeID))
	if err := st.Upsert(nodeID, vec, hex.EncodeToString(sum[:]), contributeMockModel); err != nil {
		t.Fatalf("seed embedding %s: %v", nodeID, err)
	}
}

// saveTargetNode seeds an ACTIVE node file directly in the target mount.
func saveTargetNode(t *testing.T, mount warren.ProjectStatus, id, summary, context string) {
	t.Helper()
	st := node.NewStore(mount.Path)
	if err := st.SaveNode(&node.Node{
		ID: id, Type: "concept", Namespace: "default",
		Status: node.StatusActive, Summary: summary, Context: context,
	}); err != nil {
		t.Fatalf("save target node %s: %v", id, err)
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestContributeClassifierNoop: identical content under a DIFFERENT target
// id — the stored vector is the exact mock embedding of the incoming embed
// text, so similarity is 1.0 (>= ThresholdNOOP) and the classifier NOOPs
// against the target node; nothing is written under the incoming id.
func TestContributeClassifierNoop(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	const summary = "Retry queue drains via exponential backoff"
	const contextBody = "The worker retries failed jobs with capped exponential backoff and jitter."
	saveVaultNode(t, vault, "notes/retry", summary, contextBody)
	saveTargetNode(t, mount, "old/retry-copy", summary, contextBody)
	// warren.EmbedText matches the classifier's embed-text formula
	// (summary + "\n\n" + bounded context).
	seedTargetEmbedding(t, mount, "old/retry-copy",
		mockVec(t, warren.EmbedText(&node.Node{Summary: summary, Context: contextBody})))

	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	if res.Counts != (FlowCounts{Noop: 1}) {
		t.Fatalf("counts = %+v, want {Noop:1} (warnings: %v)", res.Counts, res.Warnings)
	}
	if len(res.Ops) != 1 || res.Ops[0].Action != "noop" || res.Ops[0].NodeID != "notes/retry" || res.Ops[0].TargetID != "old/retry-copy" {
		t.Fatalf("ops = %+v, want classifier noop against old/retry-copy", res.Ops)
	}
	// The incoming id must NOT have been created in the target.
	if _, err := os.Stat(filepath.Join(mount.Path, "notes", "retry.md")); !os.IsNotExist(err) {
		t.Fatalf("classifier NOOP wrote the incoming node (stat err=%v)", err)
	}
}

// TestContributeClassifierUpdate: crafted similarity in the UPDATE band —
// the den content is applied onto the TARGET node id, and no node is
// created under the incoming id.
func TestContributeClassifierUpdate(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	const summary = "Cache invalidation happens on write, with a 5m TTL backstop"
	const contextBody = "Writers publish invalidation events; a TTL sweep catches missed events."
	saveVaultNode(t, vault, "notes/cache", summary, contextBody)
	saveTargetNode(t, mount, "legacy/cache-notes", "Old cache summary", "Old cache context")

	incoming := mockVec(t, warren.EmbedText(&node.Node{Summary: summary, Context: contextBody}))
	stored := vecAtCosine(t, incoming, 0.98) // sim = 1/(1+sqrt(2-1.96)) ≈ 0.833
	if sim := storeSimilarity(incoming, stored); sim < classifier.ThresholdUPDATE || sim >= classifier.ThresholdNOOP {
		t.Fatalf("crafted similarity %v outside UPDATE band [%v,%v)", sim, classifier.ThresholdUPDATE, classifier.ThresholdNOOP)
	}
	seedTargetEmbedding(t, mount, "legacy/cache-notes", stored)

	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	if res.Counts != (FlowCounts{Updated: 1}) {
		t.Fatalf("counts = %+v, want {Updated:1} (warnings: %v)", res.Counts, res.Warnings)
	}
	if len(res.Ops) != 1 || res.Ops[0].Action != "update" || res.Ops[0].TargetID != "legacy/cache-notes" {
		t.Fatalf("ops = %+v, want classifier update of legacy/cache-notes", res.Ops)
	}
	got := loadTargetNode(t, mount, "legacy/cache-notes")
	if got.Summary != summary || got.Context != contextBody || got.Status != node.StatusActive {
		t.Fatalf("target after update = summary %q context %q status %q", got.Summary, got.Context, got.Status)
	}
	if _, err := os.Stat(filepath.Join(mount.Path, "notes", "cache.md")); !os.IsNotExist(err) {
		t.Fatalf("classifier UPDATE created the incoming id too (stat err=%v)", err)
	}
}

// TestContributeClassifierSupersede: crafted similarity in the SUPERSEDE
// band — the target node file is retired (status superseded, SupersededBy
// set) and the incoming node created active. The target's embeddings.db is
// deliberately NOT touched: git carries the markdown only, and consumers
// regenerate embeddings on consume (reindex/reembed after the PR merges).
func TestContributeClassifierSupersede(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	const summary = "Auth tokens are rotated hourly by the session service"
	const contextBody = "Rotation moved from the gateway into the session service in v2."
	saveVaultNode(t, vault, "notes/token-rotation", summary, contextBody)
	saveTargetNode(t, mount, "legacy/gateway-rotation", "Auth tokens are rotated by the gateway", "Pre-v2 design.")

	incoming := mockVec(t, warren.EmbedText(&node.Node{Summary: summary, Context: contextBody}))
	stored := vecAtCosine(t, incoming, 0.90) // sim = 1/(1+sqrt(2-1.8)) ≈ 0.691
	if sim := storeSimilarity(incoming, stored); sim < classifier.ThresholdSUPERSEDE || sim >= classifier.ThresholdUPDATE {
		t.Fatalf("crafted similarity %v outside SUPERSEDE band [%v,%v)", sim, classifier.ThresholdSUPERSEDE, classifier.ThresholdUPDATE)
	}
	seedTargetEmbedding(t, mount, "legacy/gateway-rotation", stored)

	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	if res.Counts != (FlowCounts{Superseded: 1}) {
		t.Fatalf("counts = %+v, want {Superseded:1} (warnings: %v)", res.Counts, res.Warnings)
	}
	if len(res.Ops) != 1 || res.Ops[0].Action != "supersede" || res.Ops[0].NodeID != "notes/token-rotation" || res.Ops[0].TargetID != "legacy/gateway-rotation" {
		t.Fatalf("ops = %+v, want supersede legacy/gateway-rotation with notes/token-rotation", res.Ops)
	}

	// Target soft-deleted with the replacement recorded.
	old := loadTargetNode(t, mount, "legacy/gateway-rotation")
	if old.Status != node.StatusSuperseded || old.SupersededBy != "notes/token-rotation" {
		t.Fatalf("superseded target = status %q superseded_by %q", old.Status, old.SupersededBy)
	}
	// Replacement created active under the incoming id.
	repl := loadTargetNode(t, mount, "notes/token-rotation")
	if repl.Status != node.StatusActive || repl.Summary != summary {
		t.Fatalf("replacement = status %q summary %q", repl.Status, repl.Summary)
	}
	// The embeddings.db must be UNTOUCHED (no status flip, no upsert of the
	// replacement): contribute stages edit-branch-only markdown, so mutating
	// the checkout's live DB would poison the main branch's derived state.
	embStore, err := embedding.NewStore(filepath.Join(mount.Path, ".marmot-data", "embeddings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = embStore.Close() }()
	matches, err := embStore.FindSimilar(stored, 0.99, contributeMockModel)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	found := false
	for _, m := range matches {
		if m.NodeID == "legacy/gateway-rotation" {
			found = true
		}
		if m.NodeID == "notes/token-rotation" {
			t.Fatalf("contribute upserted the replacement's embedding: %+v", matches)
		}
	}
	if !found {
		t.Fatalf("contribute mutated the target's embedding row (still expected active): %+v", matches)
	}

	// File-effect bookkeeping for failure recovery: replacement created,
	// retired target modified.
	tgt := node.NewStore(mount.Path)
	wantCreated, wantModified := tgt.NodePath("notes/token-rotation"), tgt.NodePath("legacy/gateway-rotation")
	if len(res.CreatedFiles) != 1 || res.CreatedFiles[0] != wantCreated {
		t.Fatalf("CreatedFiles = %v, want [%s]", res.CreatedFiles, wantCreated)
	}
	if len(res.ModifiedFiles) != 1 || res.ModifiedFiles[0] != wantModified {
		t.Fatalf("ModifiedFiles = %v, want [%s]", res.ModifiedFiles, wantModified)
	}
}

// TestContributeSupersedeWritesReplacementBeforeRetiring: F3 ordering — when
// the retirement of the old target fails, the replacement has already been
// written and the contribute succeeds with a warning; the target is never
// left superseded with no successor. The failure is induced by making the
// retired node's parent directory read-only (SaveNode's temp-file create
// fails) while the replacement lands in a different directory.
func TestContributeSupersedeWritesReplacementBeforeRetiring(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	const summary = "Auth tokens are rotated hourly by the session service"
	const contextBody = "Rotation moved from the gateway into the session service in v2."
	saveVaultNode(t, vault, "notes/token-rotation", summary, contextBody)
	saveTargetNode(t, mount, "legacy/gateway-rotation", "Auth tokens are rotated by the gateway", "Pre-v2 design.")

	incoming := mockVec(t, warren.EmbedText(&node.Node{Summary: summary, Context: contextBody}))
	stored := vecAtCosine(t, incoming, 0.90)
	if sim := storeSimilarity(incoming, stored); sim < classifier.ThresholdSUPERSEDE || sim >= classifier.ThresholdUPDATE {
		t.Fatalf("crafted similarity %v outside SUPERSEDE band", sim)
	}
	seedTargetEmbedding(t, mount, "legacy/gateway-rotation", stored)

	legacyDir := filepath.Join(mount.Path, "legacy")
	if err := os.Chmod(legacyDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(legacyDir, 0o755) })

	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatalf("Contribute must succeed when only the retirement fails: %v", err)
	}
	if res.Counts != (FlowCounts{Superseded: 1}) {
		t.Fatalf("counts = %+v, want {Superseded:1}", res.Counts)
	}
	// Replacement written and active.
	repl := loadTargetNode(t, mount, "notes/token-rotation")
	if repl.Status != node.StatusActive || repl.Summary != summary {
		t.Fatalf("replacement = status %q summary %q", repl.Status, repl.Summary)
	}
	// Old target untouched (retirement failed) — still active, and the
	// warning tells the caller the retirement is retryable.
	old := loadTargetNode(t, mount, "legacy/gateway-rotation")
	if old.Status != node.StatusActive || old.SupersededBy != "" {
		t.Fatalf("old target = status %q superseded_by %q, want untouched active", old.Status, old.SupersededBy)
	}
	if !hasWarning(res.Warnings, "retiring legacy/gateway-rotation failed") {
		t.Fatalf("warnings = %v, want a retirement-failure warning", res.Warnings)
	}
	// Only the replacement is in the file-effect lists.
	if len(res.CreatedFiles) != 1 || len(res.ModifiedFiles) != 0 {
		t.Fatalf("file effects = created %v modified %v", res.CreatedFiles, res.ModifiedFiles)
	}
}

// TestContributeClassifierErrorDegradesToAdd: an embeddings.db path that
// exists but cannot be opened (a directory) disables classification with a
// warning and the unmatched node degrades to a plain ADD — classification
// problems never fail the contribute.
func TestContributeClassifierErrorDegradesToAdd(t *testing.T) {
	vault := contributeVault(t)
	mount := contributeMount(t)
	saveVaultNode(t, vault, "notes/solo", "Solo summary", "Solo context")
	// os.Stat succeeds, embedding.NewStoreReadOnly fails: corrupt-db shape.
	if err := os.MkdirAll(filepath.Join(mount.Path, ".marmot-data", "embeddings.db"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	if res.Counts != (FlowCounts{Added: 1}) {
		t.Fatalf("counts = %+v, want {Added:1} (warnings: %v)", res.Counts, res.Warnings)
	}
	if len(res.Ops) != 1 || res.Ops[0].Action != "add" || res.Ops[0].NodeID != "notes/solo" {
		t.Fatalf("ops = %+v, want add notes/solo", res.Ops)
	}
	if !hasWarning(res.Warnings, "target embeddings unreadable") {
		t.Fatalf("warnings = %v, want a 'target embeddings unreadable' warning", res.Warnings)
	}
	if got := loadTargetNode(t, mount, "notes/solo").Summary; got != "Solo summary" {
		t.Fatalf("added node summary = %q", got)
	}
}
