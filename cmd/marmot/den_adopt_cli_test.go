package main

// Full `den adopt` coverage (§18.1 completion): vault move, pointer, MCP
// config rewrites (--dir → --den) across the four generators' outputs,
// opt-outs, dry-run inertness, and structured refusal codes.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/routes"
)

// seedAdoptProjectVault writes a minimal in-repo .marmot vault into proj
// (config + a node + embeddings.db sidecar).
func seedAdoptProjectVault(t *testing.T, proj string) {
	t.Helper()
	vault := filepath.Join(proj, ".marmot")
	for _, dir := range []string{filepath.Join(vault, "notes"), filepath.Join(vault, ".marmot-data")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"_config.md":                 "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\n---\n# vault\n",
		"notes/alpha.md":             "---\nid: notes/alpha\ntype: concept\nstatus: active\n---\nAlpha body\n",
		".marmot-data/embeddings.db": "fake-db-bytes\x00\x01",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(vault, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// seedAdoptMCPConfigs writes the four generators' outputs with the vault path
// embedded, plus unrelated keys that must survive the rewrite. The --dir
// value uses the NORMALIZED project key (what adopt compares against).
func seedAdoptMCPConfigs(t *testing.T, proj string) (projectKey, vaultAbs string) {
	t.Helper()
	key, err := routes.NormalizeProjectKey(proj)
	if err != nil {
		t.Fatal(err)
	}
	vaultAbs = filepath.Join(key, ".marmot")

	mcp := map[string]any{
		"mcpServers": map[string]any{
			"context-marmot": map[string]any{"command": "/usr/local/bin/marmot", "args": []any{"serve", "--dir", vaultAbs}},
			"other-server":   map[string]any{"command": "other", "args": []any{"--flag"}},
		},
		"unrelatedTopLevel": "keep-me",
	}
	writeJSONFileForTest(t, filepath.Join(proj, ".mcp.json"), mcp)

	vscode := map[string]any{
		"servers": map[string]any{
			"context-marmot": map[string]any{"type": "stdio", "command": "/usr/local/bin/marmot", "args": []any{"serve", "--dir", vaultAbs}},
		},
	}
	if err := os.MkdirAll(filepath.Join(proj, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFileForTest(t, filepath.Join(proj, ".vscode", "mcp.json"), vscode)

	cursor := map[string]any{
		"mcpServers": map[string]any{
			"context-marmot": map[string]any{"command": "/usr/local/bin/marmot", "args": []any{"serve", "--dir", vaultAbs}},
		},
	}
	if err := os.MkdirAll(filepath.Join(proj, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFileForTest(t, filepath.Join(proj, ".cursor", "mcp.json"), cursor)

	if err := os.MkdirAll(filepath.Join(proj, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	codex := fmt.Sprintf("# user settings kept\nmodel = \"gpt-x\"\n\n[mcp_servers.context-marmot]\nenabled = true\ncommand = \"/usr/local/bin/marmot\"\nargs = [\"serve\", \"--dir\", %q]\n", vaultAbs)
	if err := os.WriteFile(filepath.Join(proj, ".codex", "config.toml"), []byte(codex), 0o644); err != nil {
		t.Fatal(err)
	}
	return key, vaultAbs
}

func writeJSONFileForTest(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readTreeForTest maps rel path -> content for every regular file under root.
func readTreeForTest(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		data, derr := os.ReadFile(path)
		if derr != nil {
			return derr
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type denAdoptEnvelope struct {
	Schema           int               `json:"schema"`
	DenID            string            `json:"den_id"`
	DenPath          string            `json:"den_path"`
	VaultID          string            `json:"vault_id"`
	Routes           map[string]string `json:"routes"`
	VaultMoved       bool              `json:"vault_moved"`
	PointerWritten   bool              `json:"pointer_written"`
	ConfigsRewritten []string          `json:"configs_rewritten"`
	Warnings         []string          `json:"warnings"`
}

func TestDenAdoptFullFlow(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()
	seedAdoptProjectVault(t, proj)
	key, vaultAbs := seedAdoptMCPConfigs(t, proj)
	before := readTreeForTest(t, vaultAbs)

	out, code := captureRun([]string{"den", "adopt", "--from", proj, "--id", "full-adopt", "--json"})
	if code != 0 {
		t.Fatalf("adopt: code=%d out=%s", code, out)
	}
	var env denAdoptEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope parse: %v out=%s", err, out)
	}
	if env.Schema != 1 || env.DenID != "full-adopt" || env.VaultID != "full-adopt" {
		t.Fatalf("envelope head: %+v", env)
	}
	if !env.VaultMoved || !env.PointerWritten {
		t.Fatalf("vault_moved/pointer_written: %+v", env)
	}
	if env.Routes["project_path"] != key {
		t.Fatalf("routes = %+v, want project_path %s", env.Routes, key)
	}
	if env.Warnings == nil || env.ConfigsRewritten == nil {
		t.Fatalf("warnings/configs_rewritten must be arrays: %s", out)
	}
	// All four configs rewritten.
	wantRewritten := map[string]bool{
		filepath.Join(key, ".mcp.json"):             false,
		filepath.Join(key, ".vscode", "mcp.json"):   false,
		filepath.Join(key, ".cursor", "mcp.json"):   false,
		filepath.Join(key, ".codex", "config.toml"): false,
	}
	for _, p := range env.ConfigsRewritten {
		if _, ok := wantRewritten[p]; ok {
			wantRewritten[p] = true
		}
	}
	for p, seen := range wantRewritten {
		if !seen {
			t.Fatalf("config %s not rewritten (got %v)", p, env.ConfigsRewritten)
		}
	}

	// Vault moved byte-identically, embeddings.db present, source gone.
	movedVault := den.VaultPath("full-adopt")
	after := readTreeForTest(t, movedVault)
	if len(after) != len(before) {
		t.Fatalf("file counts differ: before=%d after=%d", len(before), len(after))
	}
	for rel, want := range before {
		if after[rel] != want {
			t.Fatalf("moved file %s not byte-identical", rel)
		}
	}
	if _, ok := after[".marmot-data/embeddings.db"]; !ok {
		t.Fatal("embeddings.db missing from moved vault")
	}
	if _, err := os.Stat(vaultAbs); !os.IsNotExist(err) {
		t.Fatalf(".marmot left behind (stat err=%v)", err)
	}

	// Route + pointer.
	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := rt.GetProject(key); !ok || id != "full-adopt" {
		t.Fatalf("route = %q ok=%v", id, ok)
	}
	if ptr, perr := den.ReadPointer(key); perr != nil || ptr != "full-adopt" {
		t.Fatalf("pointer = %q err=%v", ptr, perr)
	}

	// .mcp.json: rewritten to --den, unrelated keys preserved.
	data, err := os.ReadFile(filepath.Join(proj, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mcp map[string]any
	if err := json.Unmarshal(data, &mcp); err != nil {
		t.Fatalf("rewritten .mcp.json invalid: %v\n%s", err, data)
	}
	if mcp["unrelatedTopLevel"] != "keep-me" {
		t.Fatalf("unrelated top-level key lost: %s", data)
	}
	servers := mcp["mcpServers"].(map[string]any)
	if _, ok := servers["other-server"]; !ok {
		t.Fatalf("unrelated server entry lost: %s", data)
	}
	cm := servers["context-marmot"].(map[string]any)
	gotArgs := fmt.Sprintf("%v", cm["args"])
	if gotArgs != "[serve --den full-adopt]" {
		t.Fatalf(".mcp.json args = %s", gotArgs)
	}
	if cm["command"] != "/usr/local/bin/marmot" {
		t.Fatalf("command mutated: %v", cm["command"])
	}

	// VS Code (servers key, type preserved) and Cursor.
	for _, pathKeys := range []struct {
		path string
		key  string
	}{
		{filepath.Join(proj, ".vscode", "mcp.json"), "servers"},
		{filepath.Join(proj, ".cursor", "mcp.json"), "mcpServers"},
	} {
		data, err := os.ReadFile(pathKeys.path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s invalid after rewrite: %v", pathKeys.path, err)
		}
		entry := doc[pathKeys.key].(map[string]any)["context-marmot"].(map[string]any)
		if got := fmt.Sprintf("%v", entry["args"]); got != "[serve --den full-adopt]" {
			t.Fatalf("%s args = %s", pathKeys.path, got)
		}
	}
	vs, _ := os.ReadFile(filepath.Join(proj, ".vscode", "mcp.json"))
	if !strings.Contains(string(vs), `"type": "stdio"`) {
		t.Fatalf("vscode type key lost: %s", vs)
	}

	// Codex TOML: args line swapped, other lines intact.
	codex, err := os.ReadFile(filepath.Join(proj, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codex), `args = ["serve", "--den", "full-adopt"]`) {
		t.Fatalf("codex args not rewritten: %s", codex)
	}
	if strings.Contains(string(codex), "--dir") {
		t.Fatalf("codex still references --dir: %s", codex)
	}
	if !strings.Contains(string(codex), `model = "gpt-x"`) || !strings.Contains(string(codex), "# user settings kept") {
		t.Fatalf("codex unrelated lines lost: %s", codex)
	}
}

func TestDenAdoptNoRewriteNoPointer(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()
	seedAdoptProjectVault(t, proj)
	_, vaultAbs := seedAdoptMCPConfigs(t, proj)
	mcpBefore, _ := os.ReadFile(filepath.Join(proj, ".mcp.json"))

	out, code := captureRun([]string{"den", "adopt", "--from", proj, "--id", "optout-adopt", "--no-rewrite", "--no-pointer", "--json"})
	if code != 0 {
		t.Fatalf("adopt: code=%d out=%s", code, out)
	}
	var env denAdoptEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if !env.VaultMoved {
		t.Fatalf("vault must still move: %+v", env)
	}
	if env.PointerWritten || len(env.ConfigsRewritten) != 0 {
		t.Fatalf("opt-outs ignored: %+v", env)
	}
	if _, err := os.Stat(filepath.Join(proj, ".marmot-vault")); !os.IsNotExist(err) {
		t.Fatalf("--no-pointer wrote a pointer (stat err=%v)", err)
	}
	mcpAfter, _ := os.ReadFile(filepath.Join(proj, ".mcp.json"))
	if string(mcpAfter) != string(mcpBefore) {
		t.Fatalf("--no-rewrite touched .mcp.json:\n%s", mcpAfter)
	}
	if _, err := os.Stat(vaultAbs); !os.IsNotExist(err) {
		t.Fatal("vault not moved")
	}
}

func TestDenAdoptDryRunTouchesNothing(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()
	seedAdoptProjectVault(t, proj)
	key, vaultAbs := seedAdoptMCPConfigs(t, proj)
	beforeProj := readTreeForTest(t, proj)

	out, code := captureRun([]string{"den", "adopt", "--from", proj, "--id", "dry-adopt", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("dry-run: code=%d out=%s", code, out)
	}
	var env struct {
		Schema int      `json:"schema"`
		DryRun bool     `json:"dry_run"`
		Ops    []string `json:"ops"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("dry-run envelope: %v out=%s", err, out)
	}
	if env.Schema != 1 || !env.DryRun {
		t.Fatalf("dry-run env: %+v", env)
	}
	// Planned ops disclose the move, the pointer, and every config rewrite.
	joined := strings.Join(env.Ops, "\n")
	for _, want := range []string{
		"move " + vaultAbs,
		".marmot-vault",
		"rewrite " + filepath.Join(key, ".mcp.json"),
		"rewrite " + filepath.Join(key, ".vscode", "mcp.json"),
		"rewrite " + filepath.Join(key, ".cursor", "mcp.json"),
		"rewrite " + filepath.Join(key, ".codex", "config.toml"),
		"--den dry-adopt",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("dry-run ops missing %q:\n%s", want, joined)
		}
	}
	// Nothing changed: project tree byte-identical, no den created.
	afterProj := readTreeForTest(t, proj)
	if len(afterProj) != len(beforeProj) {
		t.Fatalf("dry-run changed the project file set")
	}
	for rel, want := range beforeProj {
		if afterProj[rel] != want {
			t.Fatalf("dry-run modified %s", rel)
		}
	}
	if _, err := os.Stat(den.Path("dry-adopt")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the den (stat err=%v)", err)
	}
}

func TestDenAdoptRefusalCodes(t *testing.T) {
	hermeticDenCLI(t)

	// not_a_vault (json + plain).
	empty := t.TempDir()
	out, code := captureRun([]string{"den", "adopt", "--from", empty, "--id", "nv", "--json"})
	if code == 0 || !strings.Contains(out, "not_a_vault") {
		t.Fatalf("not_a_vault: code=%d out=%s", code, out)
	}
	if code := run([]string{"den", "adopt", "--from", empty, "--id", "nv"}); code == 0 {
		t.Fatal("plain not_a_vault should fail")
	}

	// den_vault_exists.
	if _, err := den.Create("busy", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	seedAdoptProjectVault(t, proj)
	out, code = captureRun([]string{"den", "adopt", "--from", proj, "--id", "busy", "--json"})
	if code == 0 || !strings.Contains(out, "den_vault_exists") {
		t.Fatalf("den_vault_exists: code=%d out=%s", code, out)
	}
	// Source untouched by the refusal.
	if _, err := os.Stat(filepath.Join(proj, ".marmot", "_config.md")); err != nil {
		t.Fatal("refusal moved the source vault")
	}
}

// TestDenAdoptUnparseableConfigWarnsAndSkips: a broken .mcp.json is never
// clobbered — adopt succeeds with a warning and leaves the file untouched.
func TestDenAdoptUnparseableConfigWarnsAndSkips(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()
	seedAdoptProjectVault(t, proj)
	broken := filepath.Join(proj, ".mcp.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{"den", "adopt", "--from", proj, "--id", "warned-adopt", "--json"})
	if code != 0 {
		t.Fatalf("adopt: code=%d out=%s", code, out)
	}
	var env denAdoptEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if !env.VaultMoved {
		t.Fatalf("adopt must still succeed: %+v", env)
	}
	found := false
	for _, w := range env.Warnings {
		if strings.Contains(w, ".mcp.json") && strings.Contains(w, "not valid JSON") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want unparseable-json skip warning", env.Warnings)
	}
	data, _ := os.ReadFile(broken)
	if string(data) != "{not json" {
		t.Fatalf("unparseable config was clobbered: %q", data)
	}
}

// TestDenCreateEmbeddingFlags: den create wires a real embedding provider
// into the identity vault via flags; an unknown provider is a structured
// refusal.
func TestDenCreateEmbeddingFlags(t *testing.T) {
	hermeticDenCLI(t)
	out, code := captureRun([]string{
		"den", "create", "emb-cli",
		"--project", t.TempDir(), "--no-pointer",
		"--embedding-provider", "openai",
		"--embedding-model", "text-embedding-3-large",
		"--json",
	})
	if code != 0 {
		t.Fatalf("create: code=%d out=%s", code, out)
	}
	cfgData, err := os.ReadFile(filepath.Join(den.VaultPath("emb-cli"), "_config.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfgData), "embedding_provider: openai") ||
		!strings.Contains(string(cfgData), `embedding_model: "text-embedding-3-large"`) {
		t.Fatalf("vault _config.md = %s", cfgData)
	}

	out, code = captureRun([]string{
		"den", "create", "emb-bad-cli",
		"--project", t.TempDir(), "--no-pointer",
		"--embedding-provider", "bogus",
		"--json",
	})
	if code == 0 || !strings.Contains(out, "den_create_failed") || !strings.Contains(out, "unknown embedding provider") {
		t.Fatalf("bogus provider: code=%d out=%s", code, out)
	}
}

// TestDenAdoptRewritesConfigsThroughSymlinkedPath (F16): the MCP configs
// embed the path the user typed — through a symlink (macOS /tmp,/var style)
// — while adopt normalizes the project key via EvalSymlinks. The rewrite
// match must resolve symlinks on both sides instead of comparing Clean-only
// strings, or symlinked projects silently keep serve --dir configs pointing
// at the moved vault.
func TestDenAdoptRewritesConfigsThroughSymlinkedPath(t *testing.T) {
	hermeticDenCLI(t)
	base := t.TempDir()
	real := filepath.Join(base, "real-proj")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(base, "links")
	if err := os.MkdirAll(linkParent, 0o755); err != nil {
		t.Fatal(err)
	}
	symProj := filepath.Join(linkParent, "proj")
	if err := os.Symlink(real, symProj); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	seedAdoptProjectVault(t, real)

	// Configs carry the SYMLINKED vault path (what a user in /tmp/... sees).
	symVault := filepath.Join(symProj, ".marmot")
	mcp := map[string]any{
		"mcpServers": map[string]any{
			"context-marmot": map[string]any{"command": "/usr/local/bin/marmot", "args": []any{"serve", "--dir", symVault}},
		},
	}
	writeJSONFileForTest(t, filepath.Join(real, ".mcp.json"), mcp)
	if err := os.MkdirAll(filepath.Join(real, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	codex := fmt.Sprintf("[mcp_servers.context-marmot]\ncommand = \"/usr/local/bin/marmot\"\nargs = [\"serve\", \"--dir\", %q]\n", symVault)
	if err := os.WriteFile(filepath.Join(real, ".codex", "config.toml"), []byte(codex), 0o644); err != nil {
		t.Fatal(err)
	}

	// Adopt through the symlinked path.
	out, code := captureRun([]string{"den", "adopt", "--from", symProj, "--id", "sym-den", "--json"})
	if code != 0 {
		t.Fatalf("adopt: code=%d out=%s", code, out)
	}
	var env denAdoptEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v out=%s", err, out)
	}
	if len(env.ConfigsRewritten) != 2 {
		t.Fatalf("configs_rewritten = %v, want both .mcp.json and .codex/config.toml (symlinked --dir not matched)", env.ConfigsRewritten)
	}
	mcpData, err := os.ReadFile(filepath.Join(real, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mcpData), `"--den"`) || strings.Contains(string(mcpData), "--dir") {
		t.Fatalf(".mcp.json not rewritten: %s", mcpData)
	}
	codexData, err := os.ReadFile(filepath.Join(real, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codexData), `args = ["serve", "--den", "sym-den"]`) {
		t.Fatalf(".codex/config.toml not rewritten: %s", codexData)
	}
}
