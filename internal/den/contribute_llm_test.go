package den

// LLM classifier wiring for Contribute (§18.4 upgrade from LLM:nil): the
// classifier used in the second pass carries the TARGET vault's configured
// LLM provider. These tests inject a fake llm.Provider through the
// newClassifierLLM seam and prove (a) the classifier honors the LLM verdict
// over the embedding-distance bands, and (b) an LLM error degrades to
// ADD+warning — classification failures never fail a contribute.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/warren"
)

// withMockClassifierLLM swaps the newClassifierLLM seam for the test.
func withMockClassifierLLM(t *testing.T, p llm.Provider) {
	t.Helper()
	orig := newClassifierLLM
	newClassifierLLM = func(cfg *config.VaultConfig, vaultDir string) (llm.Provider, string) {
		return p, "using mock (test seam)"
	}
	t.Cleanup(func() { newClassifierLLM = orig })
}

// llmFixture seeds a den node plus a target node with an embedding close
// enough (SUPERSEDE band without an LLM) that FindSimilar surfaces it as a
// candidate — so classifier.Classify reaches step 9 and consults the LLM.
func llmFixture(t *testing.T) (vault string, mount warren.ProjectStatus) {
	t.Helper()
	vault = contributeVault(t)
	mount = contributeMount(t)
	const summary = "Sessions are stored in Redis with a 30m sliding TTL"
	const contextBody = "Moved from sticky in-memory sessions to Redis in the v3 rollout."
	saveVaultNode(t, vault, "notes/sessions", summary, contextBody)
	saveTargetNode(t, mount, "legacy/session-notes", "Old session summary", "Old session context")
	incoming := mockVec(t, warren.EmbedText(&node.Node{Summary: summary, Context: contextBody}))
	stored := vecAtCosine(t, incoming, 0.90) // fallback would say SUPERSEDE
	seedTargetEmbedding(t, mount, "legacy/session-notes", stored)
	return vault, mount
}

// TestContributeLLMVerdictHonored: the injected LLM says UPDATE against the
// target id; without the LLM the embedding-distance fallback would SUPERSEDE.
// The contribute must follow the LLM.
func TestContributeLLMVerdictHonored(t *testing.T) {
	vault, mount := llmFixture(t)
	mock := &llm.MockProvider{Result: llm.ClassifyResult{
		Action:       llm.ActionUPDATE,
		TargetNodeID: "legacy/session-notes",
		Confidence:   0.9,
		Reasoning:    "same concept, richer content",
	}}
	withMockClassifierLLM(t, mock)

	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	if mock.Calls != 1 {
		t.Fatalf("LLM Classify calls = %d, want 1", mock.Calls)
	}
	if res.Counts != (FlowCounts{Updated: 1}) {
		t.Fatalf("counts = %+v, want {Updated:1} (LLM verdict must beat the distance fallback; warnings: %v)", res.Counts, res.Warnings)
	}
	if len(res.Ops) != 1 || res.Ops[0].Action != "update" || res.Ops[0].TargetID != "legacy/session-notes" {
		t.Fatalf("ops = %+v", res.Ops)
	}
	got := loadTargetNode(t, mount, "legacy/session-notes")
	if got.Status != node.StatusActive || got.Summary != "Sessions are stored in Redis with a 30m sliding TTL" {
		t.Fatalf("target after LLM update = status %q summary %q", got.Status, got.Summary)
	}
	// No node created under the incoming id, and the old target was NOT
	// superseded (which the nil-LLM fallback would have done).
	if _, serr := os.Stat(filepath.Join(mount.Path, "notes", "sessions.md")); !os.IsNotExist(serr) {
		t.Fatalf("LLM UPDATE also created the incoming id (stat err=%v)", serr)
	}
}

// TestContributeLLMErrorDegradesToAdd: an erroring LLM degrades that node to
// a plain ADD with a warning; the contribute still succeeds and the target
// node is untouched.
func TestContributeLLMErrorDegradesToAdd(t *testing.T) {
	vault, mount := llmFixture(t)
	mock := &llm.MockProvider{Err: errors.New("llm exploded")}
	withMockClassifierLLM(t, mock)

	res, err := Contribute(context.Background(), vault, mount, false)
	if err != nil {
		t.Fatalf("Contribute must not fail on LLM errors: %v", err)
	}
	if mock.Calls != 1 {
		t.Fatalf("LLM Classify calls = %d, want 1", mock.Calls)
	}
	if res.Counts != (FlowCounts{Added: 1}) {
		t.Fatalf("counts = %+v, want {Added:1}", res.Counts)
	}
	if !hasWarning(res.Warnings, "classify notes/sessions failed") || !hasWarning(res.Warnings, "llm exploded") {
		t.Fatalf("warnings = %v, want classify-failed warning naming the LLM error", res.Warnings)
	}
	// Degraded ADD: incoming node written, target untouched and still active.
	added := loadTargetNode(t, mount, "notes/sessions")
	if added.Status != node.StatusActive {
		t.Fatalf("added node status = %q", added.Status)
	}
	old := loadTargetNode(t, mount, "legacy/session-notes")
	if old.Status != node.StatusActive || old.Summary != "Old session summary" {
		t.Fatalf("target must be untouched after degraded ADD: %+v", old)
	}
}
