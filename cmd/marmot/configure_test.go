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

	// Simulate: select "2" (mock) at provider prompt.
	input := createInput(t, "2\n")
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

	// Simulate: "1" (openai), "1" (text-embedding-3-small), "sk-test-key-1234567890" (API key).
	input := createInput(t, "1\n1\nsk-test-key-1234567890\n")
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

	// Simulate: "1" (openai), "2" (text-embedding-3-large), "sk-big-key" (API key).
	input := createInput(t, "1\n2\nsk-big-key\n")
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

	// Simulate: just press enter (defaults to mock).
	input := createInput(t, "\n")
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

	// Select mock (default).
	input := createInput(t, "\n")
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
