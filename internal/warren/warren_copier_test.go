package warren

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeBurrowSource builds a warren checkout project dir with node files,
// secrets, and sidecars for burrow-copier tests. Returns (warrenRoot,
// project, sourceDir).
func writeBurrowSource(t *testing.T) (string, Project, string) {
	t.Helper()
	warrenRoot := t.TempDir()
	project := Project{ProjectID: "api", Path: "projects/api/.marmot"}
	source := filepath.Join(warrenRoot, "projects", "api", ".marmot")
	files := map[string]string{
		"_config.md":                      "---\nversion: \"1\"\nvault_id: api\nnamespace: default\nembedding_provider: mock\n---\n",
		"service/api.md":                  "---\nid: service/api\ntype: function\n---\nAPI node.\n",
		".marmot-data/embeddings.db":      "db-bytes",
		".marmot-data/.env":               "OPENAI_API_KEY=secret\n",
		".marmot-data/embeddings.db-wal":  "", // empty: checkpoint helper must not open the fake db
		".marmot-data/embeddings.db-shm":  "shm",
		".obsidian/workspace.json":        "{}\n",
		".obsidian/workspace-mobile.json": "{}\n",
	}
	for rel, content := range files {
		path := filepath.Join(source, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return warrenRoot, project, source
}

// TestMaterializeSkipsSecretsAndSidecars: the burrow copy must exclude the
// same secrets and DB sidecars as import (the old copyDir copied all of
// them, leaking .env into the local cache).
func TestMaterializeSkipsSecretsAndSidecars(t *testing.T) {
	marmotDir := t.TempDir()
	warrenRoot, project, _ := writeBurrowSource(t)

	target, err := Materialize(marmotDir, "product-platform", project, warrenRoot, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	mustExist(t, filepath.Join(target, "service", "api.md"))
	mustExist(t, filepath.Join(target, ".marmot-data", "embeddings.db"))
	mustNotExist(t, filepath.Join(target, ".marmot-data", ".env"))
	mustNotExist(t, filepath.Join(target, ".marmot-data", "embeddings.db-wal"))
	mustNotExist(t, filepath.Join(target, ".marmot-data", "embeddings.db-shm"))
	mustNotExist(t, filepath.Join(target, ".obsidian", "workspace.json"))
	mustNotExist(t, filepath.Join(target, ".obsidian", "workspace-mobile.json"))
}

// TestMaterializeSkipsSymlinks: symlink-to-file and symlink-to-dir in the
// source are skipped, never followed (the old copyDir treated a
// symlink-to-dir as a file and followed symlink targets).
func TestMaterializeSkipsSymlinks(t *testing.T) {
	marmotDir := t.TempDir()
	warrenRoot, project, source := writeBurrowSource(t)

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(source, "link-to-file.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "link-to-dir")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	target, err := Materialize(marmotDir, "product-platform", project, warrenRoot, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	mustNotExist(t, filepath.Join(target, "link-to-file.md"))
	mustNotExist(t, filepath.Join(target, "link-to-dir"))
	mustExist(t, filepath.Join(target, "service", "api.md"))
}

// TestMaterializeClearsStaleFiles: a re-burrow after deleting a node in the
// source must not resurrect the deleted node in the cache, and a failed
// re-burrow must leave the previous cache intact.
func TestMaterializeClearsStaleFiles(t *testing.T) {
	marmotDir := t.TempDir()
	warrenRoot, project, source := writeBurrowSource(t)
	staleNode := filepath.Join(source, "service", "old.md")
	if err := os.WriteFile(staleNode, []byte("---\nid: service/old\ntype: function\n---\nOld.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	target, err := Materialize(marmotDir, "product-platform", project, warrenRoot, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	mustExist(t, filepath.Join(target, "service", "old.md"))

	// Delete the node in the source; re-burrow must drop it from the cache.
	if err := os.Remove(staleNode); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(marmotDir, "product-platform", project, warrenRoot, ""); err != nil {
		t.Fatalf("re-Materialize: %v", err)
	}
	mustNotExist(t, filepath.Join(target, "service", "old.md"))
	mustExist(t, filepath.Join(target, "service", "api.md"))

	// A failed re-burrow (unreadable source file) leaves the previous cache
	// intact and cleans up its temp dir.
	if os.Getuid() == 0 {
		t.Skip("unreadable-file failure injection does not work as root")
	}
	unreadable := filepath.Join(source, "service", "locked.md")
	if err := os.WriteFile(unreadable, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })
	if _, err := Materialize(marmotDir, "product-platform", project, warrenRoot, ""); err == nil {
		t.Fatal("expected Materialize to fail on unreadable source file")
	}
	mustExist(t, filepath.Join(target, "service", "api.md"))
	mustNotExist(t, target+".tmp")
}

// TestMaterializePermPreserved: permission bits are preserved but setuid is
// stripped (only Mode().Perm() propagates).
func TestMaterializePermPreserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	marmotDir := t.TempDir()
	warrenRoot, project, source := writeBurrowSource(t)

	private := filepath.Join(source, "private.md")
	if err := os.WriteFile(private, []byte("---\nid: private\ntype: concept\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	suid := filepath.Join(source, "suid-file")
	if err := os.WriteFile(suid, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(suid, 0o755|os.ModeSetuid); err != nil {
		t.Skipf("cannot set setuid bit: %v", err)
	}

	target, err := Materialize(marmotDir, "product-platform", project, warrenRoot, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	info, err := os.Stat(filepath.Join(target, "private.md"))
	if err != nil {
		t.Fatalf("stat private: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("private.md perm = %o, want 0600", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Join(target, "suid-file"))
	if err != nil {
		t.Fatalf("stat suid file: %v", err)
	}
	if info.Mode()&os.ModeSetuid != 0 {
		t.Error("setuid bit propagated into burrow cache")
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("suid-file perm = %o, want 0755", info.Mode().Perm())
	}
}
