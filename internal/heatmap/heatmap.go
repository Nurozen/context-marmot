// Package heatmap provides co-access frequency tracking for ContextMarmot.
// Heat map weights affect graph traversal edge priority only — they never
// affect node existence or discoverability via semantic search.
package heatmap

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Default heat map parameters.
const (
	DefaultDecayRate           = 0.95
	DefaultDecayFloor          = 0.05
	DefaultPromotionThreshold  = 0.80
	DefaultLearningRate        = 0.1
)

// Pair represents a co-accessed pair of node IDs with its accumulated weight.
type Pair struct {
	A      string  `yaml:"a"`
	B      string  `yaml:"b"`
	Weight float64 `yaml:"weight"`
	Hits   int     `yaml:"hits"`
	Last   string  `yaml:"last"` // RFC3339 timestamp
}

// PairKey returns a canonical key for a pair (alphabetically ordered).
func PairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// HeatMap tracks co-access frequency for a namespace.
type HeatMap struct {
	Namespace          string  `yaml:"namespace"`
	LastDecay          string  `yaml:"last_decay,omitempty"`
	DecayRate          float64 `yaml:"decay_rate"`
	DecayFloor         float64 `yaml:"decay_floor"`
	PromotionThreshold float64 `yaml:"promotion_threshold"`
	Pairs              []Pair  `yaml:"pairs"`

	// Internal index for fast lookups (not serialized).
	index map[string]int // PairKey -> index into Pairs
	mu    sync.Mutex
}

// New creates a new empty HeatMap with default parameters.
func New(namespace string) *HeatMap {
	return &HeatMap{
		Namespace:          namespace,
		DecayRate:          DefaultDecayRate,
		DecayFloor:         DefaultDecayFloor,
		PromotionThreshold: DefaultPromotionThreshold,
		index:              make(map[string]int),
	}
}

// buildIndex populates the internal index from the Pairs slice.
func (h *HeatMap) buildIndex() {
	h.index = make(map[string]int, len(h.Pairs))
	for i, p := range h.Pairs {
		h.index[PairKey(p.A, p.B)] = i
	}
}

// RecordCoAccess records that the given node IDs were returned together in a
// query result. All pairwise combinations are updated.
// Weight formula: weight = min(1.0, weight + (1.0 - weight) * learningRate)
func (h *HeatMap) RecordCoAccess(nodeIDs []string, learningRate float64) {
	if len(nodeIDs) < 2 {
		return
	}
	if learningRate <= 0 {
		learningRate = DefaultLearningRate
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)

	for i := 0; i < len(nodeIDs); i++ {
		for j := i + 1; j < len(nodeIDs); j++ {
			if nodeIDs[i] == nodeIDs[j] {
				continue // skip self-pairs from duplicate IDs
			}
			a, b := nodeIDs[i], nodeIDs[j]
			key := PairKey(a, b)

			if idx, ok := h.index[key]; ok {
				p := &h.Pairs[idx]
				p.Weight = math.Min(1.0, p.Weight+(1.0-p.Weight)*learningRate)
				p.Hits++
				p.Last = now
			} else {
				// Canonicalize order.
				if a > b {
					a, b = b, a
				}
				h.Pairs = append(h.Pairs, Pair{
					A:      a,
					B:      b,
					Weight: learningRate, // first co-access starts at learningRate
					Hits:   1,
					Last:   now,
				})
				h.index[key] = len(h.Pairs) - 1
			}
		}
	}
}

// GetWeight returns the weight for a specific pair, or 0 if not tracked.
func (h *HeatMap) GetWeight(a, b string) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := PairKey(a, b)
	if idx, ok := h.index[key]; ok {
		return h.Pairs[idx].Weight
	}
	return 0
}

// GetWeights returns a map of pair keys to weights for the given node IDs.
// Only pairs involving at least one of the given IDs are returned.
func (h *HeatMap) GetWeights(nodeIDs []string) map[string]float64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	idSet := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		idSet[id] = true
	}

	result := make(map[string]float64)
	for _, p := range h.Pairs {
		if idSet[p.A] || idSet[p.B] {
			result[PairKey(p.A, p.B)] = p.Weight
		}
	}
	return result
}

// Decay applies exponential decay to all pair weights, enforcing the decay floor.
// Formula: weight = max(decay_floor, weight * decay_rate)
func (h *HeatMap) Decay() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.Pairs {
		h.Pairs[i].Weight = math.Max(h.DecayFloor, h.Pairs[i].Weight*h.DecayRate)
	}
	h.LastDecay = time.Now().UTC().Format(time.RFC3339)
}

// PromotionCandidates returns pairs whose weight is at or above the promotion threshold.
func (h *HeatMap) PromotionCandidates() []Pair {
	h.mu.Lock()
	defer h.mu.Unlock()

	var candidates []Pair
	for _, p := range h.Pairs {
		if p.Weight >= h.PromotionThreshold {
			candidates = append(candidates, p)
		}
	}
	return candidates
}

// PairCount returns the number of tracked pairs.
func (h *HeatMap) PairCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.Pairs)
}

// --- File I/O ---

// Load reads a heat map file from _heat/<namespace>.md under the given vault dir.
func Load(vaultDir, namespace string) (*HeatMap, error) {
	path := FilePath(vaultDir, namespace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(namespace), nil
		}
		return nil, fmt.Errorf("load heatmap: %w", err)
	}
	return parse(data, namespace)
}

// parse extracts YAML frontmatter from heat map file content.
func parse(data []byte, namespace string) (*HeatMap, error) {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return New(namespace), nil
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return New(namespace), nil
	}
	yamlBlock := content[3 : end+3]

	h := New(namespace)
	if err := yaml.Unmarshal([]byte(yamlBlock), h); err != nil {
		return nil, fmt.Errorf("parse heatmap: %w", err)
	}
	h.buildIndex()
	return h, nil
}

// Save writes the heat map to _heat/<namespace>.md under the given vault dir.
// The write is atomic (temp file + rename).
func Save(vaultDir string, h *HeatMap) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	heatDir := filepath.Join(vaultDir, "_heat")
	if err := os.MkdirAll(heatDir, 0o755); err != nil {
		return fmt.Errorf("create heat dir: %w", err)
	}

	// Sort pairs by weight descending for human readability.
	sorted := make([]Pair, len(h.Pairs))
	copy(sorted, h.Pairs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Weight > sorted[j].Weight
	})

	out := &HeatMap{
		Namespace:          h.Namespace,
		LastDecay:          h.LastDecay,
		DecayRate:          h.DecayRate,
		DecayFloor:         h.DecayFloor,
		PromotionThreshold: h.PromotionThreshold,
		Pairs:              sorted,
	}

	yamlBytes, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal heatmap: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n\n")
	buf.WriteString(fmt.Sprintf("Heat map for %s. Auto-generated by ContextMarmot.\nDo not edit manually unless you know what you're doing.\n", h.Namespace))

	path := FilePath(vaultDir, h.Namespace)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(buf.String()), 0o644); err != nil {
		return fmt.Errorf("write tmp heatmap: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename heatmap: %w", err)
	}
	return nil
}

// FilePath returns the on-disk path for a namespace's heat map file.
func FilePath(vaultDir, namespace string) string {
	return filepath.Join(vaultDir, "_heat", namespace+".md")
}
