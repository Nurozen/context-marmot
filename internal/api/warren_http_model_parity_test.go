package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
)

// TestHTTPSearchFederatesAcrossEmbeddingModels (F15): the HTTP search must be
// per-vault-model aware, mirroring context_query. A warren-mounted vault whose
// embedding store was built with a DIFFERENT model than the local vault used
// to embed the same model-string gate that Store.Search/SearchActive enforce.
// Before the fix, the handler embedded the query once with the LOCAL model and
// searched every namespace with it, so the remote store rejected the query on
// model mismatch and the federated result silently vanished. The fix re-embeds
// the query with the remote vault's own embedder (resolved from its _config.md)
// so the mismatched vault returns results.
func TestHTTPSearchFederatesAcrossEmbeddingModels(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)

	// Local engine embeds with "mock-test" (see setupTestEngine). Mount a
	// warren vault and rebuild its store under a DISTINCT model so the single
	// local query vector would be rejected on model mismatch.
	warrenRoot := setupAPIWarren(t, workspaceRoot, "product-platform", "project-a", "remote-vault")
	remoteMarmot := filepath.Join(warrenRoot, "projects", "project-a", ".marmot")

	// The remote vault's _config.md must resolve (via config.NewEmbedderFromVault)
	// to an embedder that produces the same distinct model its store carries,
	// otherwise remoteQueryVector cannot re-embed the query for it.
	const remoteModel = "mock-remote"
	remoteConfig := "---\nversion: \"1\"\nvault_id: remote-vault\nnamespace: default\nembedding_provider: mock\nembedding_model: " + remoteModel + "\n---\n"
	if err := os.WriteFile(filepath.Join(remoteMarmot, "_config.md"), []byte(remoteConfig), 0o644); err != nil {
		t.Fatalf("write remote config: %v", err)
	}

	// Rebuild the remote store from scratch under the distinct model (a
	// mixed-model store would poison every search).
	dbPath := filepath.Join(remoteMarmot, ".marmot-data", "embeddings.db")
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove remote db: %v", err)
	}
	reseedRemoteEmbedding(t, dbPath, remoteModel, "service/api", "Service API")

	wireWarrenVaultRegistry(t, engine)

	rec := doRequest(t, handler, "GET", "/api/search?q=Service%20API&ns=_warren/product-platform", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("warren-scoped search = %d: %s", rec.Code, rec.Body.String())
	}
	var resp SearchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	var hit *SearchResult
	for i := range resp.Results {
		if resp.Results[i].NodeID == "@remote-vault/service/api" {
			hit = &resp.Results[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("federated search missing @remote-vault/service/api (a vault on model %q returned nothing to a %q query — F15 regression): %+v",
			remoteModel, engine.Embedder.Model(), resp.Results)
	}
	if hit.Summary != "Service API" {
		t.Fatalf("federated hit summary = %q, want %q", hit.Summary, "Service API")
	}
}

// reseedRemoteEmbedding writes a single node embedding into a fresh remote
// store under the given model (distinct from the local vault's model).
func reseedRemoteEmbedding(t *testing.T, dbPath, model, nodeID, text string) {
	t.Helper()
	embedder := embedding.NewMockEmbedder(model)
	vec, err := embedder.Embed(text)
	if err != nil {
		t.Fatalf("embed remote node %s: %v", nodeID, err)
	}
	store, err := embedding.NewStore(dbPath)
	if err != nil {
		t.Fatalf("open remote store: %v", err)
	}
	defer func() { _ = store.Close() }()
	h := sha256.Sum256([]byte(text))
	if err := store.Upsert(nodeID, vec, hex.EncodeToString(h[:]), model); err != nil {
		t.Fatalf("upsert remote embedding %s: %v", nodeID, err)
	}
}
