package config

import (
	"fmt"
	"os"

	"github.com/nurozen/context-marmot/internal/embedding"
)

// NewEmbedderFromVault creates an embedder based on vault config and environment.
// It reads the provider/model from the config, the API key from env vars,
// and falls back to mock if no provider is configured.
func NewEmbedderFromVault(cfg *VaultConfig) (embedding.Embedder, error) {
	provider := cfg.EmbeddingProvider
	model := cfg.EmbeddingModel
	apiKey := APIKeyWithVault(provider, cfg.VaultDir)

	// If a real provider is configured but no API key is set, fall back to mock with a warning.
	if provider != "" && provider != "mock" && apiKey == "" {
		fmt.Fprintf(os.Stderr, "warning: %s provider configured but no API key found (set %s); falling back to mock embedder\n",
			provider, EnvKeyName(provider))
		provider = "mock"
		model = "mock-v1"
	}

	emb, err := embedding.NewEmbedder(provider, model, apiKey)
	if err != nil {
		return nil, fmt.Errorf("create embedder: %w", err)
	}

	// Log which provider is being used (to stderr, not stdout).
	if provider == "mock" || provider == "" {
		fmt.Fprintln(os.Stderr, "embedding: using mock embedder (lexical only)")
	} else {
		fmt.Fprintf(os.Stderr, "embedding: using %s/%s\n", provider, emb.Model())
	}

	return emb, nil
}

