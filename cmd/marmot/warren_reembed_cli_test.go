package main

// CLI tests for F1 (pin-keyed embedding regeneration on warren add/sync) and
// F4 (warren add duplicate/exists guards inside the cache lock — a second add
// of an existing id must never delete a live cache). Hermetic: temp
// MARMOT_HOME, local git remotes, mock embedder.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/warrenreg"
)

// reembedSyncEnvelope mirrors jsonWarrenSyncEnvelope including the additive
// reembedded field.
type reembedSyncEnvelope struct {
	Schema  int `json:"schema"`
	Warrens []struct {
		ID         string `json:"id"`
		Updated    bool   `json:"updated"`
		Reembedded int    `json:"reembedded"`
		Error      string `json:"error"`
	} `json:"warrens"`
	Warnings []string `json:"warnings"`
}

type reembedAddEnvelope struct {
	Schema       int      `json:"schema"`
	CheckoutPath string   `json:"checkout_path"`
	Reembedded   int      `json:"reembedded"`
	Warnings     []string `json:"warnings"`
}

// writeRemoteVaultConfig gives one project vault in the remote warren a mock
// embedder config so reembed can build the vault's own embedder.
func writeRemoteVaultConfig(t *testing.T, remote, projectID string) {
	t.Helper()
	vault := filepath.Join(remote, "projects", projectID, ".marmot")
	cfg := "---\nversion: \"1\"\nvault_id: " + projectID + "\nnamespace: default\nembedding_provider: mock\nembedding_model: test-model\n---\n"
	if err := os.WriteFile(filepath.Join(vault, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write _config.md: %v", err)
	}
}

// writeRemoteNode adds a node markdown file to the remote project vault and
// commits it.
func writeRemoteNode(t *testing.T, remote, projectID, rel, id, summary string) {
	t.Helper()
	path := filepath.Join(remote, "projects", projectID, ".marmot", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir node dir: %v", err)
	}
	body := "---\nid: " + id + "\ntype: function\nsummary: " + summary + "\n---\nbody\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write node: %v", err)
	}
	gitCLI(t, remote, "add", "-A")
	gitCLI(t, remote, "commit", "-q", "-m", "node "+id)
}

func checkoutVaultDB(homeRoot, warrenID, projectID string) string {
	return filepath.Join(homeRoot, "warren-cache", "checkouts", warrenID,
		"projects", projectID, ".marmot", ".marmot-data", "embeddings.db")
}

func TestWarrenAddAndSyncReembed(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "rewarren", "project-a")
	writeRemoteVaultConfig(t, remote, "project-a")
	writeRemoteNode(t, remote, "project-a", "service/api.md", "service/api", "The API surface")

	// add: the checkout ships markdown only, so the vault's node must be
	// embedded keyed to the fresh pin.
	out, code := captureRun([]string{"warren", "add", remote, "--id", "rewarren", "--json"})
	if code != 0 {
		t.Fatalf("warren add: code=%d out=%s", code, out)
	}
	var addEnv reembedAddEnvelope
	if err := json.Unmarshal([]byte(out), &addEnv); err != nil {
		t.Fatalf("add envelope: %v out=%s", err, out)
	}
	if addEnv.Reembedded != 1 {
		t.Fatalf("add reembedded = %d, want 1 (warnings %v)", addEnv.Reembedded, addEnv.Warnings)
	}
	db := checkoutVaultDB(homeRoot, "rewarren", "project-a")
	store, err := embedding.NewStoreReadOnly(db)
	if err != nil {
		t.Fatalf("open checkout embeddings: %v", err)
	}
	if got := store.Count(); got != 1 {
		t.Fatalf("checkout store rows = %d, want 1", got)
	}
	models, _ := store.Models()
	_ = store.Close()
	if len(models) != 1 || models[0] != "test-model" {
		t.Fatalf("checkout store models = %v, want [test-model]", models)
	}

	// sync with a newly merged node: only the new node is stale -> 1 reembed.
	writeRemoteNode(t, remote, "project-a", "service/db.md", "service/db", "The DB layer")
	out, code = captureRun([]string{"warren", "sync", "rewarren", "--json"})
	if code != 0 {
		t.Fatalf("warren sync: code=%d out=%s", code, out)
	}
	var syncEnv reembedSyncEnvelope
	if err := json.Unmarshal([]byte(out), &syncEnv); err != nil {
		t.Fatalf("sync envelope: %v out=%s", err, out)
	}
	if len(syncEnv.Warrens) != 1 || !syncEnv.Warrens[0].Updated || syncEnv.Warrens[0].Reembedded != 1 {
		t.Fatalf("sync result = %+v (warnings %v), want updated with reembedded=1", syncEnv.Warrens, syncEnv.Warnings)
	}

	// no-change sync: hash checks find nothing stale, reembedded stays 0.
	out, code = captureRun([]string{"warren", "sync", "rewarren", "--json"})
	if code != 0 {
		t.Fatalf("warren sync (noop): code=%d out=%s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &syncEnv); err != nil {
		t.Fatalf("sync envelope (noop): %v out=%s", err, out)
	}
	if syncEnv.Warrens[0].Updated || syncEnv.Warrens[0].Reembedded != 0 {
		t.Fatalf("noop sync result = %+v, want no update, reembedded=0", syncEnv.Warrens)
	}

	store, err = embedding.NewStoreReadOnly(db)
	if err != nil {
		t.Fatalf("reopen checkout embeddings: %v", err)
	}
	defer func() { _ = store.Close() }()
	if got := store.Count(); got != 2 {
		t.Fatalf("checkout store rows after sync = %d, want 2", got)
	}
}

// TestWarrenSyncReembedModelGuard proves the F2 guard inside the sync
// reembed: a checkout store already holding a different model is never
// written to — the vault is skipped with a warning naming both models.
func TestWarrenSyncReembedModelGuard(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "guardwarren", "project-a")
	writeRemoteVaultConfig(t, remote, "project-a")
	// No nodes yet: add pins the checkout without reembedding anything.
	gitCLI(t, remote, "add", "-A")
	gitCLI(t, remote, "commit", "-q", "-m", "config", "--allow-empty")
	if out, code := captureRun([]string{"warren", "add", remote, "--id", "guardwarren", "--json"}); code != 0 {
		t.Fatalf("warren add: code=%d out=%s", code, out)
	}

	// Seed the checkout's store with a foreign-model row.
	db := checkoutVaultDB(homeRoot, "guardwarren", "project-a")
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	seed, err := embedding.NewStore(db)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := seed.Upsert("seed/node", []float32{1, 2, 3}, "h", "other-model"); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	_ = seed.Close()

	// A merged node arrives; sync must refuse to mix test-model into the
	// other-model store.
	writeRemoteNode(t, remote, "project-a", "service/api.md", "service/api", "The API surface")
	out, code := captureRun([]string{"warren", "sync", "guardwarren", "--json"})
	if code != 0 {
		t.Fatalf("warren sync: code=%d out=%s", code, out)
	}
	var syncEnv reembedSyncEnvelope
	if err := json.Unmarshal([]byte(out), &syncEnv); err != nil {
		t.Fatalf("sync envelope: %v out=%s", err, out)
	}
	if syncEnv.Warrens[0].Reembedded != 0 {
		t.Fatalf("reembedded = %d, want 0 (guard must skip)", syncEnv.Warrens[0].Reembedded)
	}
	joined := strings.Join(syncEnv.Warnings, "\n")
	if !strings.Contains(joined, "other-model") || !strings.Contains(joined, "test-model") {
		t.Fatalf("warnings must name both models, got %v", syncEnv.Warnings)
	}
	store, err := embedding.NewStoreReadOnly(db)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = store.Close() }()
	models, _ := store.Models()
	if len(models) != 1 || models[0] != "other-model" {
		t.Fatalf("store models = %v, want only [other-model] (never poisoned)", models)
	}
}

// TestWarrenAddDuplicateNeverDeletesCache pins F4: re-running add for an id
// whose cache already exists (registered or not) refuses without deleting the
// live bare mirror, checkout, or pin.
func TestWarrenAddDuplicateNeverDeletesCache(t *testing.T) {
	homeRoot := hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "dupwarren", "project-a")
	if out, code := captureRun([]string{"warren", "add", remote, "--id", "dupwarren", "--json"}); code != 0 {
		t.Fatalf("warren add: code=%d out=%s", code, out)
	}
	barePath := filepath.Join(homeRoot, "warren-cache", "dupwarren.git")
	checkoutPath := filepath.Join(homeRoot, "warren-cache", "checkouts", "dupwarren")
	pinPath := checkoutPath + ".pin"
	pinBefore, err := os.ReadFile(pinPath)
	if err != nil {
		t.Fatalf("read pin: %v", err)
	}

	assertCacheIntact := func(step string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(barePath, "HEAD")); err != nil {
			t.Fatalf("%s: live bare mirror was deleted: %v", step, err)
		}
		if _, err := os.Stat(filepath.Join(checkoutPath, "_warren.md")); err != nil {
			t.Fatalf("%s: live checkout was deleted: %v", step, err)
		}
		pinAfter, err := os.ReadFile(pinPath)
		if err != nil || string(pinAfter) != string(pinBefore) {
			t.Fatalf("%s: pin changed/deleted: %v (%q -> %q)", step, err, pinBefore, pinAfter)
		}
	}

	// Registered duplicate: refused, nothing deleted.
	out, code := captureRun([]string{"warren", "add", remote, "--id", "dupwarren", "--json"})
	if code == 0 || !strings.Contains(out, "duplicate_warren") {
		t.Fatalf("duplicate add: code=%d out=%s", code, out)
	}
	assertCacheIntact("registered duplicate")

	// Unregistered but cache-on-disk (stale registry): still refused, still
	// nothing deleted — this is the path that formerly risked RemoveAll on a
	// live cache.
	if err := warrenreg.Update(func(reg *warrenreg.Registry) error {
		delete(reg.Warrens, "dupwarren")
		return nil
	}); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	out, code = captureRun([]string{"warren", "add", remote, "--id", "dupwarren", "--json"})
	if code == 0 || !strings.Contains(out, "duplicate_warren") || !strings.Contains(out, "already exists") {
		t.Fatalf("unregistered duplicate add: code=%d out=%s", code, out)
	}
	assertCacheIntact("unregistered duplicate")
}
