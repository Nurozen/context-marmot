package embedding

// Embedder is the interface for generating vector embeddings from text.
type Embedder interface {
	// Embed generates a vector embedding for the given text.
	Embed(text string) ([]float32, error)
	// EmbedBatch generates vector embeddings for multiple texts.
	EmbedBatch(texts []string) ([][]float32, error)
	// Model returns the name of the embedding model.
	Model() string
	// Dimension returns the dimensionality of the embeddings produced.
	Dimension() int
}
