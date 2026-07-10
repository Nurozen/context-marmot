package warren

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
)

// newLiveSourceVault creates a .marmot source vault at marmotDir with a real
// embeddings.db whose WAL is hot: the returned store is still open and has
// un-checkpointed rows, simulating a marmot serve holding the DB while an
// import/burrow copies the vault.
func newLiveSourceVault(t *testing.T, marmotDir string, rows int) *embedding.Store {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(marmotDir, ".marmot-data"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	config := "---\nversion: \"1\"\nvault_id: source-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(config), 0o644); err != nil {
		t.Fatalf("write _config.md: %v", err)
	}
	dbPath := filepath.Join(marmotDir, ".marmot-data", "embeddings.db")
	store, err := embedding.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	emb := embedding.NewMockEmbedder("test-model")
	for i := 0; i < rows; i++ {
		vec, _ := emb.Embed(fmt.Sprintf("node %d", i))
		if err := store.Upsert(fmt.Sprintf("node/%d", i), vec, fmt.Sprintf("hash%d", i), "test-model"); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	// The WAL must actually be hot for the regression to mean anything.
	if fi, err := os.Stat(dbPath + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("expected hot WAL at %s-wal (err=%v)", dbPath, err)
	}
	return store
}

// countRows opens a copied embeddings.db (main file only) and returns its
// row count.
func countRows(t *testing.T, dbPath string) int {
	t.Helper()
	store, err := embedding.NewStoreReadOnly(dbPath)
	if err != nil {
		t.Fatalf("open copied db %s: %v", dbPath, err)
	}
	defer func() { _ = store.Close() }()
	return store.Count()
}

// TestImportProjectCheckpointsWAL: rows living only in the source WAL must
// survive an import even though -wal/-shm sidecars are excluded from the
// copy — ImportProject checkpoints before copying.
func TestImportProjectCheckpointsWAL(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "product-platform"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source := filepath.Join(t.TempDir(), "api", ".marmot")
	const rows = 5
	_ = newLiveSourceVault(t, source, rows) // conn stays open: WAL stays hot

	if _, err := ImportProject(root, source, Project{ProjectID: "api"}, ImportOptions{}); err != nil {
		t.Fatalf("ImportProject: %v", err)
	}

	dest := filepath.Join(root, "projects", "api", ".marmot")
	mustNotExist(t, filepath.Join(dest, ".marmot-data", "embeddings.db-wal"))
	mustNotExist(t, filepath.Join(dest, ".marmot-data", "embeddings.db-shm"))
	if got := countRows(t, filepath.Join(dest, ".marmot-data", "embeddings.db")); got != rows {
		t.Fatalf("imported copy has %d rows, want %d (WAL not checkpointed before copy)", got, rows)
	}
}

// TestMaterializeCheckpointsWAL: same regression through the burrow path.
func TestMaterializeCheckpointsWAL(t *testing.T) {
	marmotDir := t.TempDir()
	warrenRoot := t.TempDir()
	project := Project{ProjectID: "api", Path: "projects/api/.marmot"}
	source := filepath.Join(warrenRoot, "projects", "api", ".marmot")
	const rows = 4
	_ = newLiveSourceVault(t, source, rows)

	target, err := Materialize(marmotDir, "product-platform", project, warrenRoot, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	mustNotExist(t, filepath.Join(target, ".marmot-data", "embeddings.db-wal"))
	mustNotExist(t, filepath.Join(target, ".marmot-data", "embeddings.db-shm"))
	if got := countRows(t, filepath.Join(target, ".marmot-data", "embeddings.db")); got != rows {
		t.Fatalf("burrow copy has %d rows, want %d (WAL not checkpointed before copy)", got, rows)
	}
}

// TestCheckpointHelperNoDB: a source without an embeddings DB is a no-op —
// no error, no file created.
func TestCheckpointHelperNoDB(t *testing.T) {
	dir := t.TempDir()
	checkpointEmbeddings(dir)
	mustNotExist(t, filepath.Join(dir, ".marmot-data", "embeddings.db"))

	// A DB without a hot WAL is also left completely untouched (no rw open,
	// no journal flip on some future non-WAL file).
	if err := os.MkdirAll(filepath.Join(dir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, ".marmot-data", "embeddings.db")
	if err := os.WriteFile(fake, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkpointEmbeddings(dir)
	data, err := os.ReadFile(fake)
	if err != nil || string(data) != "not a database" {
		t.Fatalf("checkpoint helper touched a WAL-less file: %v %q", err, data)
	}
}
