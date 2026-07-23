package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/api"
	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/web"
)

// seedDenVaultNode writes one active node into a den identity vault and seeds
// its embeddings DB so semantic search can find it. The model tag matches the
// identity vault's default config (mock provider, empty model -> "mock-v1").
func seedDenVaultNode(t *testing.T, vaultDir, nodeID, text string) {
	t.Helper()
	nodePath := filepath.Join(vaultDir, filepath.FromSlash(nodeID)+".md")
	if err := os.MkdirAll(filepath.Dir(nodePath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: " + nodeID + "\ntype: module\nnamespace: default\nstatus: active\nsummary: " + text + "\n---\n\n" + text + "\n"
	if err := os.WriteFile(nodePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	emb := embedding.NewMockEmbedder("mock-v1")
	vec, err := emb.Embed(text)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	store, err := embedding.NewStore(filepath.Join(vaultDir, ".marmot-data", "embeddings.db"))
	if err != nil {
		t.Fatalf("open embeddings db: %v", err)
	}
	defer func() { _ = store.Close() }()
	h := sha256.Sum256([]byte(text))
	if err := store.Upsert(nodeID, vec, hex.EncodeToString(h[:]), emb.Model()); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
}

// TestUIServerFederatesDenLinks reproduces the live-validation defect: two
// dens with indexed nodes, `den link alpha --link beta` (live den-to-den
// link), then the HTTP API built through the SAME construction path `marmot
// ui`/serve use (buildEngine, which runs LoadDenLinks/LoadDenBridges). The
// /api/search endpoint must return beta's @-qualified federated result — it
// previously only federated under an explicit _warren/ scope, so den-linked
// results were silently missing from the UI while context_query returned
// them — and /api/warrens must carry the additive den_links field.
func TestUIServerFederatesDenLinks(t *testing.T) {
	hermeticDenCLI(t)
	for _, id := range []string{"alpha", "beta"} {
		if _, err := den.Create(id, den.CreateOptions{Lifetime: den.LifetimeDurable}); err != nil {
			t.Fatalf("den.Create(%s): %v", id, err)
		}
	}
	betaText := "Beta den zeppelin blueprint archive"
	seedDenVaultNode(t, den.VaultPath("alpha"), "alpha-note", "Alpha den squirrel logistics")
	seedDenVaultNode(t, den.VaultPath("beta"), "beta-note", betaText)

	if out, code := captureRun([]string{"den", "link", "alpha", "--link", "beta"}); code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}

	// The exact engine construction runUIPipeline/serve use — no hand-wiring.
	result := hermeticEngine(t, den.VaultPath("alpha"))
	srv := httptest.NewServer(api.NewServer(result.Engine, web.Assets).Handler())
	defer srv.Close()

	// Unscoped /api/search must include the den-linked vault's result,
	// @-qualified with beta's vault id (den identity vaults use the den id).
	res, err := http.Get(srv.URL + "/api/search?q=" + url.QueryEscape(betaText))
	if err != nil {
		t.Fatalf("GET /api/search: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d", res.StatusCode)
	}
	var search struct {
		Results []struct {
			NodeID  string `json:"node_id"`
			Summary string `json:"summary"`
		} `json:"results"`
	}
	if err := json.NewDecoder(res.Body).Decode(&search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	found := false
	for _, r := range search.Results {
		if r.NodeID == "@beta/beta-note" {
			found = true
			if r.Summary != betaText {
				t.Errorf("federated result summary = %q, want %q", r.Summary, betaText)
			}
		}
	}
	if !found {
		t.Fatalf("/api/search missing federated @beta/beta-note; results = %+v", search.Results)
	}

	// /api/warrens carries den_links additively (existing keys untouched).
	res2, err := http.Get(srv.URL + "/api/warrens")
	if err != nil {
		t.Fatalf("GET /api/warrens: %v", err)
	}
	defer func() { _ = res2.Body.Close() }()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("warrens status = %d", res2.StatusCode)
	}
	var warrens struct {
		Warrens  map[string]json.RawMessage `json:"warrens"`
		DenLinks []struct {
			Target          string `json:"target"`
			Mode            string `json:"mode"`
			State           string `json:"state"`
			ResolvedVaultID string `json:"resolved_vault_id"`
		} `json:"den_links"`
	}
	if err := json.NewDecoder(res2.Body).Decode(&warrens); err != nil {
		t.Fatalf("decode warrens: %v", err)
	}
	if warrens.Warrens == nil {
		t.Fatal("warrens key must survive the additive den_links change")
	}
	if len(warrens.DenLinks) != 1 {
		t.Fatalf("den_links = %+v, want exactly one", warrens.DenLinks)
	}
	dl := warrens.DenLinks[0]
	if dl.Target != "beta" || dl.Mode != "live" || dl.State != "resolved" || dl.ResolvedVaultID != "beta" {
		t.Fatalf("den_links[0] = %+v, want target=beta mode=live state=resolved resolved_vault_id=beta", dl)
	}
}

// TestUIServerFederatesIDlessNode covers the live-validation defect where a
// hand-written vault node WITHOUT `id:` frontmatter indexed "successfully"
// but surfaced everywhere with an empty node ID (graph nodes with id:"",
// federation returning degenerate "@beta/" qualified IDs). The ID must now be
// derived from the vault-relative file path at the parse/load seam: the real
// `index` pipeline embeds it under the derived ID, the graph shows the
// derived ID, and den-link federation returns a proper @beta/<derived-id>.
func TestUIServerFederatesIDlessNode(t *testing.T) {
	hermeticDenCLI(t)
	for _, id := range []string{"alpha", "beta"} {
		if _, err := den.Create(id, den.CreateOptions{Lifetime: den.LifetimeDurable}); err != nil {
			t.Fatalf("den.Create(%s): %v", id, err)
		}
	}
	seedDenVaultNode(t, den.VaultPath("alpha"), "alpha-note", "Alpha den squirrel logistics")

	// Hand-written node in beta with NO `id:` frontmatter.
	betaVault := den.VaultPath("beta")
	betaText := "Quasar pangolin federation beacon"
	nodePath := filepath.Join(betaVault, "notes", "quasar-beacon.md")
	if err := os.MkdirAll(filepath.Dir(nodePath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntype: note\nnamespace: default\nstatus: active\n---\n\n" + betaText + "\n"
	if err := os.WriteFile(nodePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real index pipeline (den vaults default to the mock embedder).
	if err := runIndexPipeline(betaVault, true); err != nil {
		t.Fatalf("runIndexPipeline: %v", err)
	}

	// The graph must show the derived ID, not an empty one.
	g, err := graph.LoadGraph(node.NewStore(betaVault))
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if _, ok := g.GetNode("notes/quasar-beacon"); !ok {
		t.Fatal("graph missing derived-id node notes/quasar-beacon")
	}
	if _, ok := g.GetNode(""); ok {
		t.Fatal("graph contains a node with an empty ID")
	}

	if out, code := captureRun([]string{"den", "link", "alpha", "--link", "beta"}); code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}

	result := hermeticEngine(t, den.VaultPath("alpha"))
	srv := httptest.NewServer(api.NewServer(result.Engine, web.Assets).Handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/search?q=" + url.QueryEscape(betaText))
	if err != nil {
		t.Fatalf("GET /api/search: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d", res.StatusCode)
	}
	var search struct {
		Results []struct {
			NodeID string `json:"node_id"`
		} `json:"results"`
	}
	if err := json.NewDecoder(res.Body).Decode(&search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	found := false
	for _, r := range search.Results {
		if r.NodeID == "@beta/notes/quasar-beacon" {
			found = true
		}
		if r.NodeID == "@beta/" || r.NodeID == "" {
			t.Errorf("degenerate node_id %q in results", r.NodeID)
		}
	}
	if !found {
		t.Fatalf("/api/search missing federated @beta/notes/quasar-beacon; results = %+v", search.Results)
	}
}
