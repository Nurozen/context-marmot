package namespace

import (
	"os"
	"path/filepath"
	"testing"
)

// setupRemoteVault creates a minimal remote vault dir with a config and one
// node, returning its directory. The vault_id is the given id.
func setupRemoteVault(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := "---\nversion: \"1\"\nvault_id: " + id + "\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	nodeContent := "---\nid: concept-a\ntype: concept\nstatus: active\n---\n\nA concept.\n"
	if err := os.WriteFile(filepath.Join(dir, "concept-a.md"), []byte(nodeContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveEmbeddingStore(t *testing.T) {
	vaultDir := setupRemoteVault(t, "emb-vault")
	if err := os.MkdirAll(filepath.Join(vaultDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}

	bridges := []*Bridge{
		{SourceVaultID: "local", TargetVaultID: "emb-vault", SourceVaultPath: "/tmp/local", TargetVaultPath: vaultDir},
	}
	r := NewVaultRegistry("local", "/tmp/local", bridges, nil)
	defer r.Close()

	store, err := r.ResolveEmbeddingStore("emb-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil embedding store")
	}

	// Second call returns the cached store (fast path).
	store2, err := r.ResolveEmbeddingStore("emb-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore (cached): %v", err)
	}
	if store2 != store {
		t.Error("expected cached embedding store on second call")
	}
}

func TestResolveEmbeddingStoreUnknownVault(t *testing.T) {
	r := NewVaultRegistry("local", "/tmp/local", nil, nil)
	if _, err := r.ResolveEmbeddingStore("nope"); err == nil {
		t.Fatal("expected error for unknown vault")
	}
}

func TestResolveEmbeddingStoreAfterGraphLoad(t *testing.T) {
	vaultDir := setupRemoteVault(t, "pre-loaded")
	if err := os.MkdirAll(filepath.Join(vaultDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	bridges := []*Bridge{
		{SourceVaultID: "local", TargetVaultID: "pre-loaded", SourceVaultPath: "/tmp/local", TargetVaultPath: vaultDir},
	}
	r := NewVaultRegistry("local", "/tmp/local", bridges, nil)
	defer r.Close()

	// Load the graph first so the RemoteVault entry already exists when
	// ResolveEmbeddingStore runs.
	if _, err := r.ResolveGraph("pre-loaded"); err != nil {
		t.Fatalf("ResolveGraph: %v", err)
	}
	if _, err := r.ResolveEmbeddingStore("pre-loaded"); err != nil {
		t.Fatalf("ResolveEmbeddingStore: %v", err)
	}
}

func TestRegistryClose(t *testing.T) {
	vaultDir := setupRemoteVault(t, "close-vault")
	if err := os.MkdirAll(filepath.Join(vaultDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	bridges := []*Bridge{
		{SourceVaultID: "local", TargetVaultID: "close-vault", SourceVaultPath: "/tmp/local", TargetVaultPath: vaultDir},
	}
	r := NewVaultRegistry("local", "/tmp/local", bridges, nil)

	if _, err := r.ResolveEmbeddingStore("close-vault"); err != nil {
		t.Fatalf("ResolveEmbeddingStore: %v", err)
	}
	// Close should release the cached embedding store without panicking.
	r.Close()

	// Close is safe to call again with no loaded stores.
	empty := NewVaultRegistry("local", "/tmp/local", nil, nil)
	empty.Close()
}
