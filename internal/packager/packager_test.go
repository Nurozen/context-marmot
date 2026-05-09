package packager

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Helpers ----------------------------------------------------------------

// makeVault builds a minimal but realistic .marmot vault on disk and returns
// its path.
func makeVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	vault := filepath.Join(dir, ".marmot")
	subdirs := []string{
		"",
		".marmot-data",
		".obsidian",
		"_bridges",
		"_heat",
		"auth",
		"api",
	}
	for _, sd := range subdirs {
		if err := os.MkdirAll(filepath.Join(vault, sd), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sd, err)
		}
	}

	mustWrite := func(rel, content string) {
		t.Helper()
		full := filepath.Join(vault, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	mustWrite("_config.md", `---
version: "1"
vault_id: test-vault
namespace: default
embedding_provider: openai
embedding_model: text-embedding-3-small
token_budget: 4096
---
# Test Vault

Notes for the tests.
`)

	mustWrite("_summary.md", "---\ntype: summary\n---\n# Summary\n")

	mustWrite("auth/_namespace.md", "---\nname: auth\n---\n# auth namespace\n")
	mustWrite("api/_namespace.md", "---\nname: api\n---\n# api namespace\n")

	// A pair of node files with edges between them.
	mustWrite("auth/login.md", `---
id: login
type: function
namespace: auth
status: active
edges:
  - target: api/handler
    relation: calls
  - target: auth/session
    relation: writes
---
Authenticates a user.

## Context
Used by the public API surface.
`)
	mustWrite("auth/session.md", `---
id: session
type: function
namespace: auth
status: active
edges: []
---
Manages session tokens.
`)
	mustWrite("api/handler.md", `---
id: handler
type: function
namespace: api
status: active
edges:
  - target: auth/login
    relation: calls
---
HTTP handler.
`)

	// Bridges manifest.
	mustWrite("_bridges/auth--api.md", "---\nfrom: auth\nto: api\n---\n# bridge\n")

	// Heat data (excluded by default).
	mustWrite("_heat/default.md", "---\nnamespace: default\n---\nheat data here\n")

	// Obsidian config files. workspace.json must always be stripped.
	mustWrite(".obsidian/app.json", `{"theme":"obsidian"}`)
	mustWrite(".obsidian/workspace.json", `{"main":{"id":"abc"}}`)

	// .marmot-data: embeddings + .env (must be stripped) + transient files.
	mustWrite(".marmot-data/embeddings.db", "SQLite format 3\x00fakecontent")
	mustWrite(".marmot-data/.env", "OPENAI_API_KEY=sk-shouldnotleak\n")
	mustWrite(".marmot-data/embeddings.db-wal", "wal data")
	mustWrite(".marmot-data/embeddings.db-shm", "shm data")

	return vault
}

// extractTarGz expands a .tar.gz archive into destDir.
func extractTarGz(t *testing.T, archivePath, destDir string) {
	t.Helper()
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", filepath.Dir(target), err)
			}
			out, err := os.Create(target)
			if err != nil {
				t.Fatalf("create %s: %v", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				t.Fatalf("copy %s: %v", target, err)
			}
			_ = out.Close()
		}
	}
}

// extractZip expands a .zip archive into destDir.
func extractZip(t *testing.T, archivePath, destDir string) {
	t.Helper()
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer func() { _ = r.Close() }()
	for _, zf := range r.File {
		target := filepath.Join(destDir, filepath.FromSlash(zf.Name))
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(target), err)
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatalf("open zip entry: %v", err)
		}
		out, err := os.Create(target)
		if err != nil {
			_ = rc.Close()
			t.Fatalf("create %s: %v", target, err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			_ = rc.Close()
			_ = out.Close()
			t.Fatalf("copy: %v", err)
		}
		_ = rc.Close()
		_ = out.Close()
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected %s NOT to exist", path)
	}
}

// --- Tests ------------------------------------------------------------------

func TestPackage_RoundTrip(t *testing.T) {
	vault := makeVault(t)
	out := filepath.Join(t.TempDir(), "bundle.tar.gz")

	mf, err := Package(Options{
		SourceDir:    vault,
		OutPath:      out,
		GeneratorTag: "marmot package-docs test",
	})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}

	if mf.NodeCount != 3 {
		t.Errorf("node_count: want 3, got %d", mf.NodeCount)
	}
	if mf.EdgeCount != 3 {
		t.Errorf("edge_count: want 3, got %d", mf.EdgeCount)
	}
	if got := mf.Namespaces; len(got) != 2 || got[0] != "api" || got[1] != "auth" {
		t.Errorf("namespaces: want [api auth], got %v", got)
	}
	if mf.SourceVaultID != "test-vault" {
		t.Errorf("source_vault_id: want test-vault, got %q", mf.SourceVaultID)
	}
	if !mf.ReadOnly {
		t.Error("manifest read_only should be true")
	}
	if mf.EmbeddingProvider != "openai" || mf.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("embedding fields not preserved: %+v", mf)
	}

	dest := t.TempDir()
	extractTarGz(t, out, dest)
	root := filepath.Join(dest, ".marmot")

	mustExist(t, filepath.Join(root, "_config.md"))
	mustExist(t, filepath.Join(root, "_package.md"))
	mustExist(t, filepath.Join(root, "_summary.md"))
	mustExist(t, filepath.Join(root, ".marmot-data", "embeddings.db"))
	mustExist(t, filepath.Join(root, "auth", "login.md"))
	mustExist(t, filepath.Join(root, "auth", "session.md"))
	mustExist(t, filepath.Join(root, "api", "handler.md"))
	mustExist(t, filepath.Join(root, "_bridges", "auth--api.md"))

	// Verify a node file came through verbatim.
	loginBytes, err := os.ReadFile(filepath.Join(root, "auth", "login.md"))
	if err != nil {
		t.Fatalf("read login: %v", err)
	}
	if !strings.Contains(string(loginBytes), "Authenticates a user.") {
		t.Errorf("login.md content not preserved: %q", string(loginBytes))
	}

	// _package.md frontmatter should advertise the right manifest.
	pkgBytes, err := os.ReadFile(filepath.Join(root, "_package.md"))
	if err != nil {
		t.Fatalf("read _package.md: %v", err)
	}
	pkg := string(pkgBytes)
	for _, want := range []string{
		"package_version:",
		"source_vault_id: test-vault",
		"node_count: 3",
		"edge_count: 3",
		"read_only: true",
	} {
		if !strings.Contains(pkg, want) {
			t.Errorf("_package.md missing %q", want)
		}
	}
}

func TestPackage_StripsSecrets(t *testing.T) {
	vault := makeVault(t)
	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if _, err := Package(Options{SourceDir: vault, OutPath: out, GeneratorTag: "test"}); err != nil {
		t.Fatalf("Package: %v", err)
	}

	dest := t.TempDir()
	extractTarGz(t, out, dest)
	root := filepath.Join(dest, ".marmot")

	mustNotExist(t, filepath.Join(root, ".marmot-data", ".env"))
	mustNotExist(t, filepath.Join(root, ".marmot-data", "embeddings.db-wal"))
	mustNotExist(t, filepath.Join(root, ".marmot-data", "embeddings.db-shm"))
	mustNotExist(t, filepath.Join(root, ".obsidian", "workspace.json"))
	mustNotExist(t, filepath.Join(root, ".obsidian", "workspace-mobile.json"))

	// The kept files should still be there.
	mustExist(t, filepath.Join(root, ".marmot-data", "embeddings.db"))
	mustExist(t, filepath.Join(root, ".obsidian", "app.json"))
}

func TestPackage_SetsReadOnly(t *testing.T) {
	vault := makeVault(t)
	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if _, err := Package(Options{SourceDir: vault, OutPath: out, GeneratorTag: "test"}); err != nil {
		t.Fatalf("Package: %v", err)
	}

	dest := t.TempDir()
	extractTarGz(t, out, dest)
	cfgBytes, err := os.ReadFile(filepath.Join(dest, ".marmot", "_config.md"))
	if err != nil {
		t.Fatalf("read sanitized config: %v", err)
	}
	cfg := string(cfgBytes)
	if !strings.Contains(cfg, "read_only: true") {
		t.Errorf("sanitized config missing read_only: true:\n%s", cfg)
	}
	// Critical fields should still be present.
	for _, want := range []string{"embedding_provider:", "embedding_model:", "namespace:"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("sanitized config missing %q", want)
		}
	}
}

func TestPackage_RefusesEmptyEmbeddings(t *testing.T) {
	vault := makeVault(t)
	// Truncate embeddings.db.
	if err := os.WriteFile(filepath.Join(vault, ".marmot-data", "embeddings.db"), nil, 0o644); err != nil {
		t.Fatalf("truncate embeddings: %v", err)
	}
	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	_, err := Package(Options{SourceDir: vault, OutPath: out, GeneratorTag: "test"})
	if err == nil {
		t.Fatal("expected error for empty embeddings.db")
	}
	if !strings.Contains(err.Error(), "marmot index") {
		t.Errorf("error should suggest 'marmot index': %v", err)
	}

	// Now remove it entirely.
	if err := os.Remove(filepath.Join(vault, ".marmot-data", "embeddings.db")); err != nil {
		t.Fatalf("remove embeddings: %v", err)
	}
	_, err = Package(Options{SourceDir: vault, OutPath: out, GeneratorTag: "test"})
	if err == nil {
		t.Fatal("expected error for missing embeddings.db")
	}
}

func TestPackage_Zip(t *testing.T) {
	vault := makeVault(t)
	out := filepath.Join(t.TempDir(), "bundle.zip")
	mf, err := Package(Options{SourceDir: vault, OutPath: out, Zip: true, GeneratorTag: "test"})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if mf.NodeCount != 3 {
		t.Errorf("node_count: want 3, got %d", mf.NodeCount)
	}

	dest := t.TempDir()
	extractZip(t, out, dest)
	root := filepath.Join(dest, ".marmot")
	mustExist(t, filepath.Join(root, "_config.md"))
	mustExist(t, filepath.Join(root, "_package.md"))
	mustExist(t, filepath.Join(root, ".marmot-data", "embeddings.db"))
	mustExist(t, filepath.Join(root, "auth", "login.md"))
	mustNotExist(t, filepath.Join(root, ".marmot-data", ".env"))
	mustNotExist(t, filepath.Join(root, ".obsidian", "workspace.json"))
}

func TestPackage_ExcludesHeatByDefault(t *testing.T) {
	vault := makeVault(t)

	// Default: heat excluded.
	out := filepath.Join(t.TempDir(), "noheat.tar.gz")
	if _, err := Package(Options{SourceDir: vault, OutPath: out, GeneratorTag: "test"}); err != nil {
		t.Fatalf("Package: %v", err)
	}
	dest := t.TempDir()
	extractTarGz(t, out, dest)
	mustNotExist(t, filepath.Join(dest, ".marmot", "_heat", "default.md"))

	// With --include-heat: heat present.
	out2 := filepath.Join(t.TempDir(), "withheat.tar.gz")
	if _, err := Package(Options{SourceDir: vault, OutPath: out2, IncludeHeat: true, GeneratorTag: "test"}); err != nil {
		t.Fatalf("Package: %v", err)
	}
	dest2 := t.TempDir()
	extractTarGz(t, out2, dest2)
	mustExist(t, filepath.Join(dest2, ".marmot", "_heat", "default.md"))
}

func TestPackage_NoObsidianFlag(t *testing.T) {
	vault := makeVault(t)
	out := filepath.Join(t.TempDir(), "no-obsidian.tar.gz")
	if _, err := Package(Options{SourceDir: vault, OutPath: out, NoObsidian: true, GeneratorTag: "test"}); err != nil {
		t.Fatalf("Package: %v", err)
	}
	dest := t.TempDir()
	extractTarGz(t, out, dest)
	mustNotExist(t, filepath.Join(dest, ".marmot", ".obsidian", "app.json"))
	mustNotExist(t, filepath.Join(dest, ".marmot", ".obsidian", "workspace.json"))
}

func TestPackage_StripsInlineSecretFromConfig(t *testing.T) {
	vault := makeVault(t)
	// Inject a defensive secret into _config.md frontmatter.
	tampered := `---
version: "1"
vault_id: test-vault
namespace: default
embedding_provider: openai
embedding_model: text-embedding-3-small
openai_api_key: sk-leakedkey1234567890abcdef
custom_field: harmless
---
# body
`
	if err := os.WriteFile(filepath.Join(vault, "_config.md"), []byte(tampered), 0o644); err != nil {
		t.Fatalf("write tampered config: %v", err)
	}

	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if _, err := Package(Options{SourceDir: vault, OutPath: out, GeneratorTag: "test"}); err != nil {
		t.Fatalf("Package: %v", err)
	}
	dest := t.TempDir()
	extractTarGz(t, out, dest)
	cfgBytes, err := os.ReadFile(filepath.Join(dest, ".marmot", "_config.md"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg := string(cfgBytes)
	if strings.Contains(cfg, "openai_api_key") || strings.Contains(cfg, "sk-leakedkey") {
		t.Errorf("config still contains api key fields:\n%s", cfg)
	}
	if !strings.Contains(cfg, "custom_field") {
		t.Errorf("benign custom field was dropped:\n%s", cfg)
	}
	if !strings.Contains(cfg, "read_only: true") {
		t.Errorf("read_only not set:\n%s", cfg)
	}
}
