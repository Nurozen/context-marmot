package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoadRaw(t *testing.T) {
	dir := t.TempDir()
	content := "---\nversion: \"1\"\nnamespace: test\nembedding_provider: openai\nembedding_model: text-embedding-3-small\n---\n# My Vault\n\nSome notes here.\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, body, err := LoadRaw(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Namespace != "test" {
		t.Errorf("expected namespace test, got %q", cfg.Namespace)
	}
	if cfg.EmbeddingProvider != "openai" {
		t.Errorf("expected provider openai, got %q", cfg.EmbeddingProvider)
	}
	if !strings.Contains(body, "# My Vault") {
		t.Errorf("expected body to contain '# My Vault', got %q", body)
	}
}

func TestLoadRawNoFile(t *testing.T) {
	dir := t.TempDir()
	cfg, body, err := LoadRaw(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbeddingProvider != "mock" {
		t.Errorf("expected default provider mock, got %q", cfg.EmbeddingProvider)
	}
	if body != "" {
		t.Errorf("expected empty body, got %q", body)
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	cfg := &VaultConfig{
		Version:           "1",
		Namespace:         "saved",
		EmbeddingProvider: "openai",
		EmbeddingModel:    "text-embedding-3-large",
	}
	body := "# Preserved Body\n"

	if err := Save(dir, cfg, body); err != nil {
		t.Fatalf("save error: %v", err)
	}

	// Reload and verify.
	loaded, loadedBody, err := LoadRaw(dir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	if loaded.EmbeddingProvider != "openai" {
		t.Errorf("expected provider openai, got %q", loaded.EmbeddingProvider)
	}
	if loaded.EmbeddingModel != "text-embedding-3-large" {
		t.Errorf("expected model text-embedding-3-large, got %q", loaded.EmbeddingModel)
	}
	if loaded.Namespace != "saved" {
		t.Errorf("expected namespace saved, got %q", loaded.Namespace)
	}
	if !strings.Contains(loadedBody, "# Preserved Body") {
		t.Errorf("expected body preserved, got %q", loadedBody)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := "---\nversion: \"1\"\nnamespace: rt\nembedding_provider: mock\nembedding_model: \"\"\n---\n# Round Trip\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, body, err := LoadRaw(dir)
	if err != nil {
		t.Fatal(err)
	}

	cfg.EmbeddingProvider = "openai"
	cfg.EmbeddingModel = "text-embedding-3-small"

	if err := Save(dir, cfg, body); err != nil {
		t.Fatal(err)
	}

	reloaded, reloadedBody, err := LoadRaw(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.EmbeddingProvider != "openai" {
		t.Errorf("expected openai, got %q", reloaded.EmbeddingProvider)
	}
	if !strings.Contains(reloadedBody, "# Round Trip") {
		t.Errorf("body not preserved: %q", reloadedBody)
	}
}

func TestSaveDotEnvAndLoad(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, ".marmot-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := SaveDotEnv(dir, "OPENAI_API_KEY", "sk-saved-123"); err != nil {
		t.Fatalf("save dot env: %v", err)
	}

	// Unset env var so we test .env fallback.
	t.Setenv("OPENAI_API_KEY", "")
	key := APIKeyWithVault("openai", dir)
	if key != "sk-saved-123" {
		t.Errorf("expected sk-saved-123, got %q", key)
	}
}

func TestSaveDotEnvUpdatesExisting(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, ".marmot-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write initial key.
	if err := SaveDotEnv(dir, "OPENAI_API_KEY", "sk-old"); err != nil {
		t.Fatal(err)
	}
	// Update it.
	if err := SaveDotEnv(dir, "OPENAI_API_KEY", "sk-new"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPENAI_API_KEY", "")
	key := APIKeyWithVault("openai", dir)
	if key != "sk-new" {
		t.Errorf("expected sk-new, got %q", key)
	}
}

func TestAPIKeyEnvTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, ".marmot-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := SaveDotEnv(dir, "OPENAI_API_KEY", "sk-dotenv"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "sk-env-wins")

	key := APIKeyWithVault("openai", dir)
	if key != "sk-env-wins" {
		t.Errorf("expected env to take precedence, got %q", key)
	}
}

func TestEnvKeyName(t *testing.T) {
	if got := EnvKeyName("openai"); got != "OPENAI_API_KEY" {
		t.Errorf("expected OPENAI_API_KEY, got %q", got)
	}
	if got := EnvKeyName("voyage"); got != "VOYAGE_API_KEY" {
		t.Errorf("expected VOYAGE_API_KEY, got %q", got)
	}
	if got := EnvKeyName("mock"); got != "" {
		t.Errorf("expected empty for mock, got %q", got)
	}
}
