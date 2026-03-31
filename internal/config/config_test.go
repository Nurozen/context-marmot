package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbeddingProvider != "mock" {
		t.Errorf("expected provider mock, got %q", cfg.EmbeddingProvider)
	}
	if cfg.Namespace != "default" {
		t.Errorf("expected namespace default, got %q", cfg.Namespace)
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	content := `---
version: "1"
namespace: myproject
embedding_provider: openai
embedding_model: text-embedding-3-small
---
# Test vault
`
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbeddingProvider != "openai" {
		t.Errorf("expected provider openai, got %q", cfg.EmbeddingProvider)
	}
	if cfg.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("expected model text-embedding-3-small, got %q", cfg.EmbeddingModel)
	}
	if cfg.Namespace != "myproject" {
		t.Errorf("expected namespace myproject, got %q", cfg.Namespace)
	}
}

func TestAPIKeyFromEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-123")
	key := APIKey("openai")
	if key != "sk-test-123" {
		t.Errorf("expected sk-test-123, got %q", key)
	}

	key = APIKey("mock")
	if key != "" {
		t.Errorf("expected empty key for mock, got %q", key)
	}
}

func TestNewEmbedderFromVaultMock(t *testing.T) {
	cfg := &VaultConfig{
		EmbeddingProvider: "mock",
	}
	emb, err := NewEmbedderFromVault(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if emb.Model() != "mock-v1" {
		t.Errorf("expected mock-v1, got %q", emb.Model())
	}
}

func TestNewEmbedderFromVaultFallbackNoKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	cfg := &VaultConfig{
		EmbeddingProvider: "openai",
		EmbeddingModel:    "text-embedding-3-small",
	}
	// Should fall back to mock with a warning, not error.
	emb, err := NewEmbedderFromVault(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if emb.Model() != "mock-v1" {
		t.Errorf("expected fallback to mock-v1, got %q", emb.Model())
	}
}

func TestNewEmbedderFromVaultOpenAI(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-456")
	cfg := &VaultConfig{
		EmbeddingProvider: "openai",
		EmbeddingModel:    "text-embedding-3-small",
	}
	emb, err := NewEmbedderFromVault(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if emb.Model() != "text-embedding-3-small" {
		t.Errorf("expected text-embedding-3-small, got %q", emb.Model())
	}
	if emb.Dimension() != 1536 {
		t.Errorf("expected dimension 1536, got %d", emb.Dimension())
	}
}
