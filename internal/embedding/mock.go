package embedding

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
)

const mockDimension = 1536

// MockEmbedder generates deterministic pseudo-embeddings from text hashes.
// Similar texts produce similar vectors by using overlapping hash windows.
type MockEmbedder struct {
	model string
}

// NewMockEmbedder creates a MockEmbedder with the given model name.
func NewMockEmbedder(model string) *MockEmbedder {
	return &MockEmbedder{model: model}
}

// Model returns the model name.
func (m *MockEmbedder) Model() string {
	return m.model
}

// Dimension returns the embedding dimension (1536 for mock).
func (m *MockEmbedder) Dimension() int {
	return mockDimension
}

// Embed generates a deterministic 1536-dimensional vector from text.
// The approach: hash overlapping character trigrams and distribute their
// contributions across the vector. Texts sharing trigrams will have
// similar vectors, providing a rough semantic similarity signal.
func (m *MockEmbedder) Embed(text string) ([]float32, error) {
	vec := make([]float32, mockDimension)

	if len(text) == 0 {
		// Return a zero vector for empty text, then normalize.
		vec[0] = 1.0
		return vec, nil
	}

	// Use overlapping trigrams (or shorter for short strings) to build the vector.
	// Each trigram's hash determines which dimensions get activated.
	windowSize := 3
	if len(text) < windowSize {
		windowSize = len(text)
	}

	for i := 0; i <= len(text)-windowSize; i++ {
		gram := text[i : i+windowSize]
		h := sha256.Sum256([]byte(gram))

		// Use the hash to activate several dimensions per trigram.
		// Each 4-byte chunk of the hash picks a dimension and a value.
		for j := 0; j+4 <= len(h); j += 4 {
			dim := binary.LittleEndian.Uint32(h[j:j+4]) % uint32(mockDimension)
			// Convert hash bytes to a float in [-1, 1].
			val := float32(int32(binary.LittleEndian.Uint32(h[j:j+4]))) / float32(math.MaxInt32)
			vec[dim] += val
		}
	}

	// Also add unigram activations so even single-character overlap helps.
	for i := 0; i < len(text); i++ {
		h := sha256.Sum256([]byte{text[i], '_', 'u'})
		for j := 0; j+4 <= len(h); j += 8 {
			dim := binary.LittleEndian.Uint32(h[j:j+4]) % uint32(mockDimension)
			val := float32(int32(binary.LittleEndian.Uint32(h[j:j+4]))) / float32(math.MaxInt32) * 0.3
			vec[dim] += val
		}
	}

	// L2-normalize the vector.
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}

	return vec, nil
}

// EmbedBatch generates embeddings for multiple texts by calling Embed for each.
func (m *MockEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := m.Embed(text)
		if err != nil {
			return nil, err
		}
		results[i] = vec
	}
	return results, nil
}
