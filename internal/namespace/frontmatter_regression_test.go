package namespace

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseBridgeDashesInContent guards the anchored frontmatter parser: a
// "---" inside a quoted YAML value or the manifest body must not truncate the
// bridge frontmatter (which would silently drop bridge policy enforcement).
func TestParseBridgeDashesInContent(t *testing.T) {
	content := "---\nsource: api\ntarget: web\nallowed_relations:\n  - reads\n  - calls\ncreated: \"2026-01-01T00:00:00Z\"\ndescription: \"separator --- inside a value\"\n---\n\nBridge notes\n\n---\n\nMore notes after a horizontal rule.\n"
	b, err := parseBridge([]byte(content))
	if err != nil {
		t.Fatalf("parseBridge: %v", err)
	}
	if b.Source != "api" || b.Target != "web" {
		t.Fatalf("bridge endpoints corrupted: %+v", b)
	}
	if len(b.AllowedRelations) != 2 {
		t.Fatalf("allowed relations lost: %+v", b.AllowedRelations)
	}
}

// TestParseNamespaceDashesInBody: the _namespace.md body may contain --- lines.
func TestParseNamespaceDashesInBody(t *testing.T) {
	content := "---\nname: core\nroot_path: src\n---\nNamespace docs\n---\nafter rule\n"
	ns, err := parseNamespace([]byte(content))
	if err != nil {
		t.Fatalf("parseNamespace: %v", err)
	}
	if ns.Name != "core" {
		t.Fatalf("name = %q, want core", ns.Name)
	}
}

// TestExtractEdgesDashesInFrontmatterValue: edges after a quoted "---" value
// must still be discovered.
func TestExtractEdgesDashesInFrontmatterValue(t *testing.T) {
	dir := t.TempDir()
	content := "---\nid: a\ntype: concept\nsummary: \"uses --- as a divider\"\nedges:\n  - target: other/b\n    relation: references\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	refs := extractEdgesFromFrontmatter([]byte(content))
	if len(refs) != 1 || refs[0].target != "other/b" {
		t.Fatalf("edges = %+v, want other/b", refs)
	}
}

// TestCreateCrossVaultBridgeWarnsWhenRoutesDisabled (A6 #10): the bridge is
// still created, but the failed route auto-registration is announced on
// stderr instead of being swallowed.
func TestCreateCrossVaultBridgeWarnsWhenRoutesDisabled(t *testing.T) {
	t.Setenv("MARMOT_ROUTES", "off")

	localDir := t.TempDir()
	remoteDir := t.TempDir()
	localConfig := "---\nversion: \"1\"\nvault_id: local-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	remoteConfig := "---\nversion: \"1\"\nvault_id: remote-vault\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(localDir, "_config.md"), []byte(localConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteDir, "_config.md"), []byte(remoteConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	var bridgeErr error
	func() {
		defer func() { os.Stderr = old }()
		_, bridgeErr = CreateCrossVaultBridge(localDir, remoteDir, []string{"references"})
		_ = w.Close()
	}()
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read stderr pipe: %v", readErr)
	}
	if bridgeErr != nil {
		t.Fatalf("CreateCrossVaultBridge: %v", bridgeErr)
	}
	if !strings.Contains(string(out), "not auto-registered in routes.yml") {
		t.Fatalf("expected auto-register warning on stderr, got %q", out)
	}
}
