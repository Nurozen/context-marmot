// Package config handles vault configuration parsing.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// VaultConfig represents the settings in _config.md frontmatter.
type VaultConfig struct {
	Version           string `yaml:"version"`
	Namespace         string `yaml:"namespace"`
	EmbeddingProvider string `yaml:"embedding_provider"` // openai | mock
	EmbeddingModel    string `yaml:"embedding_model"`    // model name
}

// Load reads and parses _config.md from the given vault directory.
// Returns default config if the file doesn't exist.
func Load(vaultDir string) (*VaultConfig, error) {
	configPath := filepath.Join(vaultDir, "_config.md")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultConfig(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	return parse(data)
}

func parse(data []byte) (*VaultConfig, error) {
	content := string(data)

	// Extract YAML frontmatter between --- delimiters.
	if !strings.HasPrefix(content, "---") {
		return defaultConfig(), nil
	}

	end := strings.Index(content[3:], "---")
	if end < 0 {
		return defaultConfig(), nil
	}

	yamlBlock := content[3 : end+3]
	cfg := defaultConfig()
	if err := yaml.Unmarshal([]byte(yamlBlock), cfg); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}

	return cfg, nil
}

func defaultConfig() *VaultConfig {
	return &VaultConfig{
		Version:           "1",
		Namespace:         "default",
		EmbeddingProvider: "mock",
		EmbeddingModel:    "",
	}
}

// APIKey returns the appropriate API key from environment variables
// for the given provider.
func APIKey(provider string) string {
	switch provider {
	case "openai":
		return os.Getenv("OPENAI_API_KEY")
	case "voyage":
		return os.Getenv("VOYAGE_API_KEY")
	default:
		return ""
	}
}
