package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/config"
)

// newTestVault creates a minimal vault directory for testing.
func newTestVault(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".marmot")
	if err := os.MkdirAll(filepath.Join(dir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\nembedding_model: \"\"\n---\n# Test Vault\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestConfigureSelectMock(t *testing.T) {
	dir := newTestVault(t)

	// Simulate: select "2" (mock) at provider prompt, then accept default classifier (none).
	input := createInput(t, "2\n\n")
	defer input.Close()

	if err := runConfigure(dir, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.EmbeddingProvider != "mock" {
		t.Errorf("expected provider mock, got %q", cfg.EmbeddingProvider)
	}
	if cfg.EmbeddingModel != "" {
		t.Errorf("expected empty model, got %q", cfg.EmbeddingModel)
	}
}

func TestConfigureSelectOpenAI(t *testing.T) {
	dir := newTestVault(t)
	t.Setenv("OPENAI_API_KEY", "")

	// Simulate: "1" (openai), "1" (text-embedding-3-small), "sk-test-key-1234567890" (API key), then accept default openai classifier.
	input := createInput(t, "1\n1\nsk-test-key-1234567890\n\n")
	defer input.Close()

	if err := runConfigure(dir, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.EmbeddingProvider != "openai" {
		t.Errorf("expected provider openai, got %q", cfg.EmbeddingProvider)
	}
	if cfg.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("expected model text-embedding-3-small, got %q", cfg.EmbeddingModel)
	}

	// Verify API key was saved to .env.
	key := config.APIKeyWithVault("openai", dir)
	if key != "sk-test-key-1234567890" {
		t.Errorf("expected API key sk-test-key-1234567890, got %q", key)
	}
}

func TestConfigureSelectOpenAILargeModel(t *testing.T) {
	dir := newTestVault(t)
	t.Setenv("OPENAI_API_KEY", "")

	// Simulate: "1" (openai), "2" (text-embedding-3-large), "sk-big-key" (API key), then accept default openai classifier.
	input := createInput(t, "1\n2\nsk-big-key\n\n")
	defer input.Close()

	if err := runConfigure(dir, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.EmbeddingModel != "text-embedding-3-large" {
		t.Errorf("expected model text-embedding-3-large, got %q", cfg.EmbeddingModel)
	}
}

func TestConfigureDefaultsOnEmptyInput(t *testing.T) {
	dir := newTestVault(t)

	// Simulate: just press enter (defaults to mock), then accept default classifier (none).
	input := createInput(t, "\n\n")
	defer input.Close()

	if err := runConfigure(dir, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.EmbeddingProvider != "mock" {
		t.Errorf("expected provider mock, got %q", cfg.EmbeddingProvider)
	}
}

func TestConfigurePreservesBody(t *testing.T) {
	dir := newTestVault(t)

	// Select mock (default), then accept default classifier (none).
	input := createInput(t, "\n\n")
	defer input.Close()

	if err := runConfigure(dir, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read raw file and check body is preserved.
	data, err := os.ReadFile(filepath.Join(dir, "_config.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# Test Vault") {
		t.Error("expected body '# Test Vault' to be preserved in _config.md")
	}
}

func TestConfigureNonexistentVault(t *testing.T) {
	input := createInput(t, "")
	defer input.Close()

	err := runConfigure("/nonexistent/path", input)
	if err == nil {
		t.Fatal("expected error for nonexistent vault")
	}
}

func TestConfigureClassifierOpenAI(t *testing.T) {
	dir := newTestVault(t)
	t.Setenv("OPENAI_API_KEY", "")
	// Select openai embedding (1), model small (1), key, then openai classifier (1, default)
	input := createInput(t, "1\n1\nsk-embed-key\n\n")
	defer input.Close()
	if err := runConfigure(dir, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, _ := config.Load(dir)
	if cfg.ClassifierProvider != "openai" {
		t.Errorf("expected classifier openai, got %q", cfg.ClassifierProvider)
	}
	if cfg.ClassifierModel != "gpt-5.1-codex-mini" {
		t.Errorf("expected classifier model gpt-5.1-codex-mini, got %q", cfg.ClassifierModel)
	}
}

func TestConfigureClassifierNone(t *testing.T) {
	dir := newTestVault(t)
	t.Setenv("OPENAI_API_KEY", "")
	// Select openai embedding (1), model small (1), key, then none classifier (3)
	input := createInput(t, "1\n1\nsk-embed-key\n3\n")
	defer input.Close()
	if err := runConfigure(dir, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, _ := config.Load(dir)
	if cfg.ClassifierProvider != "none" {
		t.Errorf("expected classifier none, got %q", cfg.ClassifierProvider)
	}
}

func TestConfigureClassifierAnthropic(t *testing.T) {
	dir := newTestVault(t)
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ANTHROPIC_API_KEY", "")
	// Select openai embedding (1), model small (1), keep key (Y), then anthropic classifier (2), enter key
	input := createInput(t, "1\n1\ny\n2\nsk-ant-testkey\n")
	defer input.Close()
	if err := runConfigure(dir, input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, _ := config.Load(dir)
	if cfg.ClassifierProvider != "anthropic" {
		t.Errorf("expected classifier anthropic, got %q", cfg.ClassifierProvider)
	}
	if cfg.ClassifierModel != "claude-haiku-4-5-20251001" {
		t.Errorf("expected classifier model claude-haiku, got %q", cfg.ClassifierModel)
	}
	// Verify ANTHROPIC_API_KEY was saved
	key := config.APIKeyWithVault("anthropic", dir)
	if key != "sk-ant-testkey" {
		t.Errorf("expected anthropic key sk-ant-testkey, got %q", key)
	}
}

// TestConfigureSetsVaultID (U1 piece 0): the interactive prompt and the
// non-interactive --vault-id flag both persist vault_id through
// config.Save and round-trip through config.Load.
func TestConfigureSetsVaultID(t *testing.T) {
	dir := newTestVault(t)

	// Interactive: provider mock (2), classifier default (empty), vault ID.
	input := createInput(t, "2\n\nmy-vault\n")
	defer input.Close()
	if err := runConfigure(dir, input); err != nil {
		t.Fatalf("runConfigure: %v", err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.VaultID != "my-vault" {
		t.Fatalf("VaultID after prompt = %q, want my-vault", cfg.VaultID)
	}

	// Non-interactive flag path.
	if code := run([]string{"configure", "--dir", dir, "--vault-id", "flag-vault"}); code != 0 {
		t.Fatalf("configure --vault-id exit code = %d", code)
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatalf("load config after flag: %v", err)
	}
	if cfg.VaultID != "flag-vault" {
		t.Fatalf("VaultID after flag = %q, want flag-vault", cfg.VaultID)
	}
	if cfg.EmbeddingProvider != "mock" {
		t.Fatalf("flag path clobbered provider: %q", cfg.EmbeddingProvider)
	}

	// An empty prompt line keeps the current value as the default.
	input2 := createInput(t, "2\n\n\n")
	defer input2.Close()
	if err := runConfigure(dir, input2); err != nil {
		t.Fatalf("runConfigure (defaults): %v", err)
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatalf("load config after defaults: %v", err)
	}
	if cfg.VaultID != "flag-vault" {
		t.Fatalf("VaultID after empty prompt = %q, want flag-vault kept", cfg.VaultID)
	}

	// Invalid vault IDs are refused and change nothing.
	if code := run([]string{"configure", "--dir", dir, "--vault-id", "../bad"}); code != 1 {
		t.Fatalf("configure --vault-id ../bad exit code = %d, want 1", code)
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatalf("load config after refusal: %v", err)
	}
	if cfg.VaultID != "flag-vault" {
		t.Fatalf("VaultID after refused flag = %q, want flag-vault", cfg.VaultID)
	}
}

func TestMaskKey(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"short", "*****"},
		{"sk-1234567890abcdef", "sk-12***********def"},
	}
	for _, tt := range tests {
		got := maskKey(tt.in)
		if got != tt.want {
			t.Errorf("maskKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// createInput writes content to a temp file and opens it as *os.File.
func createInput(t *testing.T, content string) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	return f
}
