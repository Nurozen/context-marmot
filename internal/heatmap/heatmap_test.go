package heatmap

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestNewHeatMap(t *testing.T) {
	h := New("test-ns")
	if h.Namespace != "test-ns" {
		t.Errorf("Namespace = %q, want %q", h.Namespace, "test-ns")
	}
	if h.DecayRate != DefaultDecayRate {
		t.Errorf("DecayRate = %f, want %f", h.DecayRate, DefaultDecayRate)
	}
	if h.DecayFloor != DefaultDecayFloor {
		t.Errorf("DecayFloor = %f, want %f", h.DecayFloor, DefaultDecayFloor)
	}
	if h.PairCount() != 0 {
		t.Errorf("PairCount = %d, want 0", h.PairCount())
	}
}

func TestRecordCoAccess(t *testing.T) {
	h := New("test")
	h.RecordCoAccess([]string{"a", "b", "c"}, 0.1)

	// Should create 3 pairs: a-b, a-c, b-c
	if h.PairCount() != 3 {
		t.Fatalf("PairCount = %d, want 3", h.PairCount())
	}

	// Each pair starts at learningRate = 0.1
	w := h.GetWeight("a", "b")
	if math.Abs(w-0.1) > 0.001 {
		t.Errorf("Weight(a,b) = %f, want 0.1", w)
	}

	// Second co-access should increase weight.
	h.RecordCoAccess([]string{"a", "b"}, 0.1)
	w2 := h.GetWeight("a", "b")
	expected := math.Min(1.0, 0.1+(1.0-0.1)*0.1) // 0.1 + 0.09 = 0.19
	if math.Abs(w2-expected) > 0.001 {
		t.Errorf("Weight(a,b) after 2nd = %f, want %f", w2, expected)
	}

	// Check hits count.
	h.mu.Lock()
	idx := h.index[PairKey("a", "b")]
	hits := h.Pairs[idx].Hits
	h.mu.Unlock()
	if hits != 2 {
		t.Errorf("Hits(a,b) = %d, want 2", hits)
	}
}

func TestRecordCoAccessSingleNode(t *testing.T) {
	h := New("test")
	h.RecordCoAccess([]string{"a"}, 0.1)
	// No pairs should be created for a single node.
	if h.PairCount() != 0 {
		t.Errorf("PairCount = %d, want 0 for single node", h.PairCount())
	}
}

func TestRecordCoAccessDuplicateIDs(t *testing.T) {
	h := New("test")
	h.RecordCoAccess([]string{"x", "x", "y"}, 0.1)
	// Should create x-y pair but NOT x-x self-pair.
	if h.PairCount() != 1 {
		t.Errorf("PairCount = %d, want 1 (no self-pairs)", h.PairCount())
	}
	if w := h.GetWeight("x", "y"); w == 0 {
		t.Error("Weight(x,y) = 0, want > 0")
	}
	if w := h.GetWeight("x", "x"); w != 0 {
		t.Error("Weight(x,x) != 0, want 0 (no self-pairs)")
	}
}

func TestRecordCoAccessCanonicalization(t *testing.T) {
	h := New("test")
	h.RecordCoAccess([]string{"b", "a"}, 0.1)
	// Should be canonical: a < b
	w := h.GetWeight("a", "b")
	if w == 0 {
		t.Error("Weight(a,b) = 0 after recording (b,a)")
	}
	// Reverse lookup should work too.
	w2 := h.GetWeight("b", "a")
	if w != w2 {
		t.Errorf("Weight(b,a) = %f != Weight(a,b) = %f", w2, w)
	}
}

func TestGetWeights(t *testing.T) {
	h := New("test")
	h.RecordCoAccess([]string{"a", "b", "c"}, 0.1)
	h.RecordCoAccess([]string{"d", "e"}, 0.1)

	// Get weights for nodes a and b — should include a-b, a-c, b-c but not d-e.
	weights := h.GetWeights([]string{"a", "b"})
	if _, ok := weights[PairKey("a", "b")]; !ok {
		t.Error("missing a-b weight")
	}
	if _, ok := weights[PairKey("a", "c")]; !ok {
		t.Error("missing a-c weight")
	}
	if _, ok := weights[PairKey("d", "e")]; ok {
		t.Error("d-e should not appear in a,b filter")
	}
}

func TestDecay(t *testing.T) {
	h := New("test")
	h.RecordCoAccess([]string{"a", "b"}, 0.5)

	w0 := h.GetWeight("a", "b")
	h.Decay()
	w1 := h.GetWeight("a", "b")

	expected := math.Max(h.DecayFloor, w0*h.DecayRate)
	if math.Abs(w1-expected) > 0.001 {
		t.Errorf("Weight after decay = %f, want %f", w1, expected)
	}
}

func TestDecayFloor(t *testing.T) {
	h := New("test")
	h.DecayFloor = 0.05
	h.RecordCoAccess([]string{"a", "b"}, 0.001) // very small weight

	// Decay many times — should never go below floor.
	for i := 0; i < 100; i++ {
		h.Decay()
	}

	w := h.GetWeight("a", "b")
	if w < h.DecayFloor {
		t.Errorf("Weight %f < DecayFloor %f", w, h.DecayFloor)
	}
}

func TestDecaySetsLastDecay(t *testing.T) {
	h := New("test")
	if h.LastDecay != "" {
		t.Errorf("LastDecay should be empty initially, got %q", h.LastDecay)
	}
	h.Decay()
	if h.LastDecay == "" {
		t.Error("LastDecay should be set after Decay()")
	}
}

func TestPromotionCandidates(t *testing.T) {
	h := New("test")
	h.PromotionThreshold = 0.5

	// Create a high-weight pair.
	h.RecordCoAccess([]string{"a", "b"}, 0.8)
	// Create a low-weight pair.
	h.RecordCoAccess([]string{"c", "d"}, 0.1)

	candidates := h.PromotionCandidates()
	if len(candidates) != 1 {
		t.Fatalf("PromotionCandidates length = %d, want 1", len(candidates))
	}
	if PairKey(candidates[0].A, candidates[0].B) != PairKey("a", "b") {
		t.Errorf("candidate = %s-%s, want a-b", candidates[0].A, candidates[0].B)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	h := New("test-ns")
	h.RecordCoAccess([]string{"auth/login", "auth/validate", "db/users"}, 0.1)

	if err := Save(dir, h); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists.
	path := FilePath(dir, "test-ns")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("heat map file not found: %v", err)
	}

	// Load and verify.
	loaded, err := Load(dir, "test-ns")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Namespace != "test-ns" {
		t.Errorf("Namespace = %q, want %q", loaded.Namespace, "test-ns")
	}
	if loaded.PairCount() != 3 {
		t.Errorf("PairCount = %d, want 3", loaded.PairCount())
	}

	w := loaded.GetWeight("auth/login", "auth/validate")
	if math.Abs(w-0.1) > 0.001 {
		t.Errorf("loaded Weight = %f, want 0.1", w)
	}
}

func TestLoadNonexistent(t *testing.T) {
	dir := t.TempDir()
	h, err := Load(dir, "missing")
	if err != nil {
		t.Fatalf("Load nonexistent: %v", err)
	}
	if h.Namespace != "missing" {
		t.Errorf("Namespace = %q, want %q", h.Namespace, "missing")
	}
	if h.PairCount() != 0 {
		t.Errorf("PairCount = %d, want 0", h.PairCount())
	}
}

func TestPairKey(t *testing.T) {
	k1 := PairKey("a", "b")
	k2 := PairKey("b", "a")
	if k1 != k2 {
		t.Errorf("PairKey(a,b) = %q != PairKey(b,a) = %q", k1, k2)
	}
}

func TestFilePath(t *testing.T) {
	p := FilePath("/vault", "default")
	expected := filepath.Join("/vault", "_heat", "default.md")
	if p != expected {
		t.Errorf("FilePath = %q, want %q", p, expected)
	}
}

func TestWeightConvergesToOne(t *testing.T) {
	h := New("test")
	// Record same pair many times.
	for i := 0; i < 100; i++ {
		h.RecordCoAccess([]string{"a", "b"}, 0.1)
	}
	w := h.GetWeight("a", "b")
	if w > 1.0 {
		t.Errorf("Weight %f > 1.0", w)
	}
	if w < 0.95 {
		t.Errorf("Weight %f should converge near 1.0 after 100 co-accesses", w)
	}
}
