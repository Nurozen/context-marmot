// Package config handles vault configuration parsing.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// VaultConfig represents the settings in _config.md frontmatter.
type VaultConfig struct {
	Version            string `yaml:"version"`
	Namespace          string `yaml:"namespace"`
	EmbeddingProvider  string `yaml:"embedding_provider"`           // openai | mock
	EmbeddingModel     string `yaml:"embedding_model"`              // model name
	ClassifierProvider string `yaml:"classifier_provider,omitempty"` // openai | anthropic | none
	ClassifierModel    string `yaml:"classifier_model,omitempty"`    // model name; empty = provider default
}

// Load reads and parses _config.md from the given vault directory.
// Returns default config if the file doesn't exist.
func Load(vaultDir string) (*VaultConfig, error) {
	cfg, _, err := LoadRaw(vaultDir)
	return cfg, err
}

// LoadRaw reads _config.md and returns both the parsed config and the
// markdown body below the frontmatter. The body can be passed back to
// Save to preserve user content.
func LoadRaw(vaultDir string) (*VaultConfig, string, error) {
	configPath := filepath.Join(vaultDir, "_config.md")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultConfig(), "", nil
		}
		return nil, "", fmt.Errorf("read config: %w", err)
	}

	cfg, body, err := parseRaw(data)
	return cfg, body, err
}

// Save writes the config as YAML frontmatter to _config.md, appending
// the provided markdown body. The write is atomic (tmp + rename).
func Save(vaultDir string, cfg *VaultConfig, body string) error {
	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString(body)
	}

	configPath := filepath.Join(vaultDir, "_config.md")
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(buf.String()), 0o644); err != nil {
		return fmt.Errorf("write tmp config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

func parseRaw(data []byte) (*VaultConfig, string, error) {
	content := string(data)

	// Extract YAML frontmatter between --- delimiters.
	if !strings.HasPrefix(content, "---") {
		return defaultConfig(), content, nil
	}

	end := strings.Index(content[3:], "---")
	if end < 0 {
		return defaultConfig(), content, nil
	}

	yamlBlock := content[3 : end+3]
	body := content[end+3+3:] // skip closing "---"
	if len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	}

	cfg := defaultConfig()
	if err := yaml.Unmarshal([]byte(yamlBlock), cfg); err != nil {
		return nil, "", fmt.Errorf("parse config yaml: %w", err)
	}

	return cfg, body, nil
}

func parse(data []byte) (*VaultConfig, error) {
	cfg, _, err := parseRaw(data)
	return cfg, err
}

func defaultConfig() *VaultConfig {
	return &VaultConfig{
		Version:           "1",
		Namespace:         "default",
		EmbeddingProvider: "mock",
		EmbeddingModel:    "",
	}
}

// APIKey returns the appropriate API key for the given provider.
// It checks environment variables first, then falls back to the vault's
// .marmot-data/.env file if vaultDir is provided.
func APIKey(provider string) string {
	return APIKeyWithVault(provider, "")
}

// APIKeyWithVault returns the API key for the given provider, checking
// environment variables first, then the vault's .marmot-data/.env file.
func APIKeyWithVault(provider, vaultDir string) string {
	envName := EnvKeyName(provider)
	if envName == "" {
		return ""
	}

	// Check environment first.
	if v := os.Getenv(envName); v != "" {
		return v
	}

	// Fall back to vault .env file.
	if vaultDir != "" {
		if v := loadDotEnvKey(vaultDir, envName); v != "" {
			return v
		}
	}
	return ""
}

// SaveDotEnv writes a key=value pair to the vault's .marmot-data/.env file.
// Creates the file if it doesn't exist; updates the key if it does.
func SaveDotEnv(vaultDir, key, value string) error {
	envPath := filepath.Join(vaultDir, ".marmot-data", ".env")

	lines := make(map[string]string)

	// Read existing .env if present.
	if data, err := os.ReadFile(envPath); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				lines[k] = v
			}
		}
	}

	// Set (or overwrite) the target key.
	lines[key] = value

	var buf strings.Builder
	for k, v := range lines {
		fmt.Fprintf(&buf, "%s=%s\n", k, v)
	}

	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		return fmt.Errorf("create .marmot-data: %w", err)
	}
	return os.WriteFile(envPath, []byte(buf.String()), 0o600)
}

// loadDotEnvKey reads a single key from the vault's .marmot-data/.env file.
func loadDotEnvKey(vaultDir, key string) string {
	data, err := os.ReadFile(filepath.Join(vaultDir, ".marmot-data", ".env"))
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if k, v, ok := strings.Cut(line, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// EnvKeyName returns the environment variable name for a provider's API key.
func EnvKeyName(provider string) string {
	switch provider {
	case "openai":
		return "OPENAI_API_KEY"
	case "voyage":
		return "VOYAGE_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	default:
		return ""
	}
}
