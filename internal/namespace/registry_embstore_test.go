package namespace

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
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

// seedRemoteEmbeddingDB writes a real embeddings.db with one row into the
// vault's .marmot-data dir and closes it (checkpointing the WAL away).
// Remote stores are opened read-only, so the DB must genuinely exist.
func seedRemoteEmbeddingDB(t *testing.T, vaultDir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(vaultDir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(vaultDir, ".marmot-data", "embeddings.db")
	store, err := embedding.NewStore(dbPath)
	if err != nil {
		t.Fatalf("seed remote embeddings.db: %v", err)
	}
	emb := embedding.NewMockEmbedder("mock-test")
	vec, _ := emb.Embed("A concept.")
	if err := store.Upsert("concept-a", vec, "hash", emb.Model()); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	return dbPath
}

func TestResolveEmbeddingStore(t *testing.T) {
	vaultDir := setupRemoteVault(t, "emb-vault")
	seedRemoteEmbeddingDB(t, vaultDir)

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
	seedRemoteEmbeddingDB(t, vaultDir)
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
	seedRemoteEmbeddingDB(t, vaultDir)
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

// TestResolveEmbeddingStoreDoesNotMutateRemote is the A1 regression: a
// cross-vault search through the registry must not migrate schema, write
// data, or change a byte of the remote vault's embeddings.db (the remote is
// someone else's git checkout). Missing remote DBs must error, not be
// created.
func TestResolveEmbeddingStoreDoesNotMutateRemote(t *testing.T) {
	vaultDir := setupRemoteVault(t, "ro-vault")
	dbPath := seedRemoteEmbeddingDB(t, vaultDir)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read remote db: %v", err)
	}

	bridges := []*Bridge{
		{SourceVaultID: "local", TargetVaultID: "ro-vault", SourceVaultPath: "/tmp/local", TargetVaultPath: vaultDir},
	}
	r := NewVaultRegistry("local", "/tmp/local", bridges, nil)
	defer r.Close()

	store, err := r.ResolveEmbeddingStore("ro-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore: %v", err)
	}
	emb := embedding.NewMockEmbedder("mock-test")
	query, _ := emb.Embed("A concept.")
	results, err := store.SearchActive(query, 5, emb.Model())
	if err != nil {
		t.Fatalf("SearchActive on remote store: %v", err)
	}
	if len(results) != 1 || results[0].NodeID != "concept-a" {
		t.Fatalf("results = %+v, want concept-a", results)
	}

	// A write through the resolved store must be rejected, not silently
	// mutate the remote checkout.
	vec, _ := emb.Embed("sneaky write")
	if err := store.Upsert("sneaky", vec, "h", emb.Model()); err == nil {
		t.Fatal("expected Upsert on remote store to be rejected")
	}

	// The remote main DB file is byte-identical (no journal-mode flip, no
	// schema migration, no data). The WAL sidecar, if the RO open created
	// one, stays empty.
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read remote db after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("cross-vault read mutated the remote embeddings.db")
	}
	if fi, err := os.Stat(dbPath + "-wal"); err == nil && fi.Size() != 0 {
		t.Errorf("cross-vault read wrote %d bytes into the remote WAL", fi.Size())
	}

	// Missing remote DB: resolving errors instead of creating a file.
	emptyVault := setupRemoteVault(t, "no-db-vault")
	r2 := NewVaultRegistry("local", "/tmp/local", []*Bridge{
		{SourceVaultID: "local", TargetVaultID: "no-db-vault", SourceVaultPath: "/tmp/local", TargetVaultPath: emptyVault},
	}, nil)
	defer r2.Close()
	if _, err := r2.ResolveEmbeddingStore("no-db-vault"); err == nil {
		t.Fatal("expected error resolving store for vault without embeddings.db")
	}
	if _, err := os.Stat(filepath.Join(emptyVault, ".marmot-data", "embeddings.db")); !os.IsNotExist(err) {
		t.Fatalf("resolve created a remote embeddings.db (stat err=%v)", err)
	}
}

// TestRefreshDropsEmbeddingStore (F7): Refresh closes the cached embedding
// store and the next resolve reopens the current DB, so a re-pinned checkout's
// new rows are visible rather than served from a stale handle.
func TestRefreshDropsEmbeddingStore(t *testing.T) {
	vaultDir := setupRemoteVault(t, "refresh-vault")
	seedRemoteEmbeddingDB(t, vaultDir)
	bridges := []*Bridge{
		{SourceVaultID: "local", TargetVaultID: "refresh-vault", SourceVaultPath: "/tmp/local", TargetVaultPath: vaultDir},
	}
	r := NewVaultRegistry("local", "/tmp/local", bridges, nil)
	defer r.Close()

	store, err := r.ResolveEmbeddingStore("refresh-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore: %v", err)
	}

	if err := r.Refresh("refresh-vault"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// The old handle is closed by Refresh's swap-then-close.
	emb := embedding.NewMockEmbedder("mock-test")
	vecA, _ := emb.Embed("A concept.")
	if _, err := store.SearchActive(vecA, 1, emb.Model()); !errors.Is(err, embedding.ErrStoreClosed) {
		t.Errorf("Refresh did not close the cached store: err = %v", err)
	}

	// A row added on disk (as a re-pin would) is visible after re-resolve.
	upsertRemoteEmbeddingRow(t, vaultDir, "concept-b", "Another concept.")
	store2, err := r.ResolveEmbeddingStore("refresh-vault")
	if err != nil {
		t.Fatalf("ResolveEmbeddingStore after Refresh: %v", err)
	}
	if store2 == store {
		t.Error("Refresh reused the stale embedding store handle")
	}
	vecB, _ := emb.Embed("Another concept.")
	res, err := store2.SearchActive(vecB, 5, emb.Model())
	if err != nil {
		t.Fatalf("reopened store search: %v", err)
	}
	found := false
	for _, sr := range res {
		if sr.NodeID == "concept-b" {
			found = true
		}
	}
	if !found {
		t.Error("reopened store did not see the row added after Refresh")
	}
}

// TestResolveEmbeddingStoreUnknownVaultHint (U4.3): the unknown-vault error
// matches ResolveGraph's wording so both resolvers explain where vault IDs
// come from.
func TestResolveEmbeddingStoreUnknownVaultHint(t *testing.T) {
	r := NewVaultRegistry("local", "/tmp/local", nil, nil)
	_, err := r.ResolveEmbeddingStore("nope")
	if err == nil || !strings.Contains(err.Error(), `unknown vault "nope": not in routing table or bridge manifests`) {
		t.Fatalf("err = %v, want routing-table hint", err)
	}
}
