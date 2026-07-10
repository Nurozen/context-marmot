package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/warren"
)

// modelPreset describes an embedding model option shown to the user.
type modelPreset struct {
	Model string
	Dim   int
}

var openaiModels = []modelPreset{
	{"text-embedding-3-small", 1536},
	{"text-embedding-3-large", 3072},
}

type classifierPreset struct {
	Provider string
	Model    string
	Label    string
}

var classifierPresets = []classifierPreset{
	{"openai", "gpt-5.1-codex-mini", "openai   (gpt-5.1-codex-mini)"},
	{"anthropic", "claude-haiku-4-5-20251001", "anthropic (claude-haiku-4-5-20251001)"},
	{"none", "", "none     (embedding-distance fallback)"},
}

func cmdConfigure(args []string) int {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	vaultID := fs.String("vault-id", "", "set the vault ID (cross-vault identity for warrens and bridges) non-interactively and exit")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}

	if *vaultID != "" {
		if err := setVaultID(*dir, *vaultID); err != nil {
			fmt.Fprintf(os.Stderr, "configure: %v\n", err)
			return 1
		}
		return 0
	}

	if err := runConfigure(*dir, os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "configure: %v\n", err)
		return 1
	}
	return 0
}

// setVaultID persists vault_id into _config.md without the interactive flow,
// so scripts and docs can say 'marmot configure --vault-id <id>'. The
// vault ID is the identity key for warren bridges: a warren project whose
// checkout vault_id matches it is served as this workspace's live vault.
func setVaultID(dir, vaultID string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", dir)
	}
	if err := warren.ValidateProjectID(vaultID); err != nil {
		return fmt.Errorf("invalid vault ID: %w", err)
	}
	cfg, body, err := config.LoadRaw(dir)
	if err != nil {
		return err
	}
	cfg.VaultID = vaultID
	if err := config.Save(dir, cfg, body); err != nil {
		return err
	}
	fmt.Printf("vault_id set to %q\n", vaultID)
	return nil
}

// defaultVaultIDFor derives the interactive prompt's default vault ID: the
// current value when set, else a slug of the workspace directory name (the
// parent of the .marmot dir).
func defaultVaultIDFor(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "vault"
	}
	return warren.GenerateProjectID(filepath.Base(filepath.Dir(abs)))
}

// runConfigure drives the interactive configuration flow.
// The reader parameter allows tests to inject simulated input.
func runConfigure(dir string, input *os.File) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("vault directory %q does not exist; run 'marmot init' first", dir)
	}

	cfg, body, err := config.LoadRaw(dir)
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(input)

	// --- Provider ---
	providers := []string{"openai", "mock"}
	defaultProvider := indexOf(providers, cfg.EmbeddingProvider)
	if defaultProvider < 0 {
		defaultProvider = 1
	}
	providerIdx := promptChoice(scanner, "Embedding provider", providers, defaultProvider)
	cfg.EmbeddingProvider = providers[providerIdx]

	// --- Model (provider-specific) ---
	if cfg.EmbeddingProvider == "openai" {
		labels := make([]string, len(openaiModels))
		defaultModel := 0
		for i, m := range openaiModels {
			labels[i] = fmt.Sprintf("%s (%dd)", m.Model, m.Dim)
			if m.Model == cfg.EmbeddingModel {
				defaultModel = i
			}
		}
		modelIdx := promptChoice(scanner, "Embedding model", labels, defaultModel)
		cfg.EmbeddingModel = openaiModels[modelIdx].Model

		// --- API key ---
		if err := promptAPIKey(scanner, input, dir, cfg.EmbeddingProvider); err != nil {
			return err
		}
	} else {
		cfg.EmbeddingModel = ""
	}

	// --- CRUD Classifier ---
	// Default: openai if embedding provider is openai, else none.
	defaultClassifier := 2 // index of "none"
	for i, p := range classifierPresets {
		if p.Provider == cfg.ClassifierProvider {
			defaultClassifier = i
			break
		}
	}
	// If no existing classifier config, default to openai when openai embedding is selected.
	if cfg.ClassifierProvider == "" && cfg.EmbeddingProvider == "openai" {
		defaultClassifier = 0
	}

	classifierLabels := make([]string, len(classifierPresets))
	for i, p := range classifierPresets {
		classifierLabels[i] = p.Label
	}
	classifierIdx := promptChoice(scanner, "CRUD Classifier", classifierLabels, defaultClassifier)
	selected := classifierPresets[classifierIdx]
	cfg.ClassifierProvider = selected.Provider
	cfg.ClassifierModel = selected.Model

	// Prompt for API key only if classifier uses a different provider than embedding.
	if selected.Provider != "none" && selected.Provider != cfg.EmbeddingProvider {
		if err := promptAPIKey(scanner, input, dir, selected.Provider); err != nil {
			return err
		}
	}

	// --- Vault ID (cross-vault identity for warrens and bridges) ---
	// Placed last so earlier prompt inputs are unaffected; EOF or an empty
	// line accepts the default (current value, else a workspace-name slug).
	defaultVaultID := cfg.VaultID
	if defaultVaultID == "" {
		defaultVaultID = defaultVaultIDFor(dir)
	}
	fmt.Printf("\nVault ID (identity for warren bridges and @-references) [%s]: ", defaultVaultID)
	vaultID := defaultVaultID
	if scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			vaultID = line
		}
	}
	if err := warren.ValidateProjectID(vaultID); err != nil {
		fmt.Printf("  Invalid vault ID (%v); keeping %q\n", err, cfg.VaultID)
	} else {
		cfg.VaultID = vaultID
	}

	// --- Save ---
	if err := config.Save(dir, cfg, body); err != nil {
		return err
	}

	fmt.Println("\nConfiguration saved.")
	fmt.Println("Run 'marmot index --force' to rebuild embeddings with the new settings.")
	return nil
}

// promptChoice displays a numbered menu and returns the selected index.
func promptChoice(scanner *bufio.Scanner, label string, options []string, defaultIdx int) int {
	fmt.Printf("\n%s:\n", label)
	for i, opt := range options {
		marker := "  "
		if i == defaultIdx {
			marker = "> "
		}
		fmt.Printf("  %s%d) %s\n", marker, i+1, opt)
	}
	fmt.Printf("Choice [%d]: ", defaultIdx+1)

	if !scanner.Scan() {
		return defaultIdx
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return defaultIdx
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(options) {
		fmt.Printf("  Invalid choice, using default: %s\n", options[defaultIdx])
		return defaultIdx
	}
	return n - 1
}

// promptAPIKey handles API key input — checks env, reads from terminal.
func promptAPIKey(scanner *bufio.Scanner, input *os.File, vaultDir, provider string) error {
	envName := config.EnvKeyName(provider)

	// Check existing sources.
	envKey := os.Getenv(envName)
	dotenvKey := ""
	if vaultDir != "" {
		dotenvKey = config.APIKeyWithVault(provider, vaultDir)
		// If both env and dotenv are set, env wins (which APIKeyWithVault returns).
		// Only show dotenv status if env is NOT set.
		if envKey != "" {
			dotenvKey = ""
		}
	}

	if envKey != "" {
		fmt.Printf("\n%s: [set via environment]\n", envName)
		fmt.Print("Keep current key? [Y/n]: ")
		if !scanner.Scan() {
			return nil
		}
		if line := strings.TrimSpace(strings.ToLower(scanner.Text())); line == "" || line == "y" || line == "yes" {
			return nil
		}
	} else if dotenvKey != "" {
		fmt.Printf("\n%s: [set in vault .env]\n", envName)
		fmt.Print("Keep current key? [Y/n]: ")
		if !scanner.Scan() {
			return nil
		}
		if line := strings.TrimSpace(strings.ToLower(scanner.Text())); line == "" || line == "y" || line == "yes" {
			return nil
		}
	}

	// Read new key.
	fmt.Printf("\nEnter %s: ", envName)
	var key string
	if term.IsTerminal(int(input.Fd())) {
		raw, err := term.ReadPassword(int(input.Fd()))
		if err != nil {
			return fmt.Errorf("read API key: %w", err)
		}
		key = string(raw)
		fmt.Println() // newline after hidden input
	} else {
		// Non-terminal (piped input / tests).
		if !scanner.Scan() {
			return fmt.Errorf("expected API key input")
		}
		key = strings.TrimSpace(scanner.Text())
	}

	if key == "" {
		fmt.Println("  No key entered; skipping.")
		return nil
	}

	// Show masked confirmation.
	masked := maskKey(key)
	fmt.Printf("  Key: %s\n", masked)

	// Save to vault .env.
	if err := config.SaveDotEnv(vaultDir, config.EnvKeyName(provider), key); err != nil {
		return fmt.Errorf("save API key: %w", err)
	}
	fmt.Println("  Saved to vault .env file.")
	return nil
}

// maskKey shows first 5 and last 3 chars, stars in between.
func maskKey(key string) string {
	if len(key) <= 10 {
		return strings.Repeat("*", len(key))
	}
	return key[:5] + strings.Repeat("*", len(key)-8) + key[len(key)-3:]
}

func indexOf(slice []string, val string) int {
	for i, s := range slice {
		if s == val {
			return i
		}
	}
	return -1
}
