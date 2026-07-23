package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewClassifierLLMNone: unset/none provider → nil LLM, fallback note.
func TestNewClassifierLLMNone(t *testing.T) {
	for _, provider := range []string{"", "none"} {
		p, note := NewClassifierLLM(&VaultConfig{ClassifierProvider: provider}, t.TempDir())
		if p != nil {
			t.Fatalf("provider %q: expected nil LLM, got %T", provider, p)
		}
		if note != "using embedding-distance fallback" {
			t.Fatalf("provider %q: note = %q", provider, note)
		}
	}
	// Nil config is safe (defensive: callers pass Load results).
	if p, _ := NewClassifierLLM(nil, ""); p != nil {
		t.Fatalf("nil config: expected nil LLM, got %T", p)
	}
}

func TestNewClassifierLLMOpenAI(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	cfg := &VaultConfig{ClassifierProvider: "openai", ClassifierModel: "gpt-test-model"}
	p, note := NewClassifierLLM(cfg, t.TempDir())
	if p == nil {
		t.Fatal("expected an LLM provider with the key set")
	}
	if p.Model() != "gpt-test-model" {
		t.Fatalf("model = %q, want the configured classifier_model", p.Model())
	}
	if note != "using openai/gpt-test-model" {
		t.Fatalf("note = %q", note)
	}
}

func TestNewClassifierLLMAnthropic(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	cfg := &VaultConfig{ClassifierProvider: "anthropic", ClassifierModel: "claude-test-model"}
	p, note := NewClassifierLLM(cfg, t.TempDir())
	if p == nil {
		t.Fatal("expected an LLM provider with the key set")
	}
	if p.Model() != "claude-test-model" {
		t.Fatalf("model = %q", p.Model())
	}
	if note != "using anthropic/claude-test-model" {
		t.Fatalf("note = %q", note)
	}
}

// TestNewClassifierLLMMissingKey: a configured provider without a resolvable
// key returns a TRUE nil interface (never a typed-nil) plus the fallback note
// naming the missing env var.
func TestNewClassifierLLMMissingKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	for provider, envName := range map[string]string{"openai": "OPENAI_API_KEY", "anthropic": "ANTHROPIC_API_KEY"} {
		p, note := NewClassifierLLM(&VaultConfig{ClassifierProvider: provider}, t.TempDir())
		if p != nil {
			t.Fatalf("%s without key: expected nil, got %T", provider, p)
		}
		if !strings.Contains(note, envName+" not found") || !strings.Contains(note, "embedding-distance fallback") {
			t.Fatalf("%s note = %q", provider, note)
		}
	}
}

// TestNewClassifierLLMVaultDotEnv: the key chain falls back to the vault's
// .marmot-data/.env (APIKeyWithVault), so a den/warren vault can carry its own
// classifier credentials.
func TestNewClassifierLLMVaultDotEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	vaultDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".marmot-data", ".env"), []byte("OPENAI_API_KEY=sk-from-vault\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, note := NewClassifierLLM(&VaultConfig{ClassifierProvider: "openai"}, vaultDir)
	if p == nil {
		t.Fatalf("expected provider from vault .env (note %q)", note)
	}
	if !strings.HasPrefix(note, "using openai/") {
		t.Fatalf("note = %q", note)
	}
}
