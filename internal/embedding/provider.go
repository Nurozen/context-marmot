package embedding

import "fmt"

// NewEmbedder creates an embedder based on provider name and config.
// provider: "openai" | "mock" (default)
// model: model name (provider-specific, uses default if empty)
// apiKey: API key (required for non-mock providers)
func NewEmbedder(provider, model, apiKey string) (Embedder, error) {
	switch provider {
	case "openai":
		if apiKey == "" {
			return nil, fmt.Errorf("provider %q requires an API key", provider)
		}
		return NewOpenAIEmbedder(apiKey, model)
	case "mock", "":
		name := model
		if name == "" {
			name = "mock-v1"
		}
		return NewMockEmbedder(name), nil
	default:
		return nil, fmt.Errorf("unknown embedding provider: %q (supported: openai, mock)", provider)
	}
}
