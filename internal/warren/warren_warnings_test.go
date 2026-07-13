package warren

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureWarnings redirects the package warn writer to a buffer for the
// duration of the test. Tests using it must not run in parallel.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := warnWriter
	warnWriter = buf
	t.Cleanup(func() { warnWriter = old })
	return buf
}

// TestActiveMountsWarnsOnUnreadableManifest (A6 #1): a warren whose manifest
// is corrupt used to silently drop all its mounts; now it warns on stderr
// while local behavior stays fail-open.
func TestActiveMountsWarnsOnUnreadableManifest(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	// Truncate the manifest: unterminated frontmatter.
	if err := os.WriteFile(filepath.Join(warrenRoot, ManifestFileName), []byte("---\nwarren_id: product-pla"), 0o644); err != nil {
		t.Fatalf("corrupt manifest: %v", err)
	}

	buf := captureWarnings(t)
	mounts, err := ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts must stay fail-open: %v", err)
	}
	if len(mounts) != 0 {
		t.Fatalf("mounts = %+v, want none for unreadable manifest", mounts)
	}
	out := buf.String()
	if !strings.Contains(out, "manifest unreadable") || !strings.Contains(out, "product-platform") {
		t.Fatalf("expected manifest-unreadable warning naming the warren, got %q", out)
	}
}

// TestActiveMountsWarnsOnCorruptProjectMetadata (A6 #2): corrupt (not
// missing) project metadata degrades vault-ID resolution; the degradation
// must be visible.
func TestActiveMountsWarnsOnCorruptProjectMetadata(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	metaPath := filepath.Join(warrenRoot, "projects", "project-a", ".marmot", ManifestFileName)
	if err := os.WriteFile(metaPath, []byte("---\nproject_id: [broken\n---\n"), 0o644); err != nil {
		t.Fatalf("corrupt metadata: %v", err)
	}

	buf := captureWarnings(t)
	mounts, err := ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts = %+v, want the degraded mount to survive", mounts)
	}
	// Degraded: vault ID falls back to the project ID.
	if mounts[0].VaultID != "project-a" {
		t.Fatalf("vault ID = %q, want fallback project-a", mounts[0].VaultID)
	}
	out := buf.String()
	if !strings.Contains(out, "metadata unreadable") || !strings.Contains(out, "project-a") {
		t.Fatalf("expected metadata-unreadable warning naming the project, got %q", out)
	}
}

// TestActiveMountsMissingMetadataStaysSilent: a missing metadata file is a
// normal state and must not warn.
func TestActiveMountsMissingMetadataStaysSilent(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := workspaceMarmotDir(workspace)
	warrenRoot := t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", "project-a")

	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := os.Remove(filepath.Join(warrenRoot, "projects", "project-a", ".marmot", ManifestFileName)); err != nil {
		t.Fatalf("remove metadata: %v", err)
	}

	buf := captureWarnings(t)
	if _, err := ActiveMounts(marmotDir); err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if out := buf.String(); out != "" {
		t.Fatalf("expected no warning for missing metadata, got %q", out)
	}
}
