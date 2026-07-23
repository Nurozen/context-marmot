package config

import (
	"github.com/nurozen/context-marmot/internal/llm"
)

// NewClassifierLLM builds the classifier LLM provider for a vault from its
// config: openai/anthropic via the vault-aware API key chain (env first, then
// <vaultDir>/.marmot-data/.env). It returns (nil, note) when no provider is
// configured or the key is missing — the caller keeps the embedding-distance
// fallback. The note is a human-readable status line (the serve/index
// pipelines print it as "classifier: <note>").
//
// Shared by cmd/marmot/pipeline.go (serve + reindex) and den contribute so
// provider construction has exactly one implementation.
func NewClassifierLLM(cfg *VaultConfig, vaultDir string) (llm.Provider, string) {
	if cfg == nil {
		return nil, "using embedding-distance fallback"
	}
	switch cfg.ClassifierProvider {
	case "openai":
		if key := APIKeyWithVault("openai", vaultDir); key != "" {
			p := llm.NewOpenAIProviderWithModel(key, cfg.ClassifierModel)
			return p, "using openai/" + p.Model()
		}
		return nil, "openai configured but OPENAI_API_KEY not found; using embedding-distance fallback"
	case "anthropic":
		if key := APIKeyWithVault("anthropic", vaultDir); key != "" {
			p := llm.NewAnthropicProviderWithModel(key, cfg.ClassifierModel)
			return p, "using anthropic/" + p.Model()
		}
		return nil, "anthropic configured but ANTHROPIC_API_KEY not found; using embedding-distance fallback"
	default:
		return nil, "using embedding-distance fallback"
	}
}
