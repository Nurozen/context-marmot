package warren

// Tests for the §15.4/§15.5 reference-resolution foundations: canonical repo
// URL normalization, manifest v3 source provenance (capture at import +
// version-bump discipline), and ResolveReference's warren-url /
// checkout-vault / none legs. Hermetic: the resolver tests run against a
// temp MARMOT_HOME registry + hand-built cache checkouts (no git).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/warrenreg"
)

func TestCanonicalRepoURL(t *testing.T) {
	cases := []struct {
		raw, want string
	}{
		{"", ""},
		{"   ", ""},
		// The §15.5 equivalence class: scp ≡ https ≡ ssh.
		{"git@github.com:x/y.git", "github.com/x/y"},
		{"https://github.com/x/y", "github.com/x/y"},
		{"ssh://git@github.com/x/y/", "github.com/x/y"},
		// Host lowercased, path case preserved.
		{"https://GitHub.COM/Acme/Repo.git", "github.com/Acme/Repo"},
		// User with password, git scheme, trailing slashes.
		{"https://user:pass@gitlab.example.com/group/proj.git/", "gitlab.example.com/group/proj"},
		{"git://Host.Example/path", "host.example/path"},
		// scp form without user.
		{"github.com:x/y.git", "github.com/x/y"},
		// file scheme and plain local paths pass through (no host).
		{"file:///tmp/warrens/w.git", "/tmp/warrens/w"},
		{"/tmp/warrens/w.git/", "/tmp/warrens/w"},
		{"/tmp/Warrens/w", "/tmp/Warrens/w"},
		// '@' inside the path is not user info.
		{"https://example.com/scope/@pkg/repo", "example.com/scope/@pkg/repo"},
		// bare host.
		{"https://GitHub.com", "github.com"},
	}
	for _, tc := range cases {
		if got := CanonicalRepoURL(tc.raw); got != tc.want {
			t.Errorf("CanonicalRepoURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestImportCapturesSourceProvenance: ImportOptions source fields land on the
// manifest project (URL canonicalized at write time) and lift the manifest to
// version 3.
func TestImportCapturesSourceProvenance(t *testing.T) {
	warrenRoot := t.TempDir()
	if _, err := Init(warrenRoot, "product-platform"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source := writeImportSourceVault(t, filepath.Join(t.TempDir(), ".marmot"), "src-vault")
	manifest, err := ImportProject(warrenRoot, source, Project{ProjectID: "project-a"}, ImportOptions{
		SourceURL:    "git@GitHub.com:Acme/project-a.git",
		SourceCommit: "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatalf("ImportProject: %v", err)
	}
	if manifest.Version != 3 {
		t.Fatalf("Version = %d, want 3 (source fields present)", manifest.Version)
	}
	project := manifest.Projects[0]
	if project.SourceURL != "github.com/Acme/project-a" {
		t.Fatalf("SourceURL = %q, want canonicalized github.com/Acme/project-a", project.SourceURL)
	}
	if project.SourceCommit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("SourceCommit = %q", project.SourceCommit)
	}
	// Round-trip: fields survive a Load and the version stays 3.
	reloaded, _, err := LoadManifest(warrenRoot)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if reloaded.Version != 3 || reloaded.Projects[0].SourceURL != project.SourceURL || reloaded.Projects[0].SourceCommit != project.SourceCommit {
		t.Fatalf("reloaded = %+v", reloaded)
	}
	data, err := os.ReadFile(filepath.Join(warrenRoot, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "source_url: github.com/Acme/project-a") || !strings.Contains(string(data), "version: 3") {
		t.Fatalf("manifest on disk missing v3 fields:\n%s", data)
	}
}

// TestImportWithoutSourceStaysPreV3: an import with no source provenance
// leaves the manifest at its pre-v3 version — older binaries can keep
// editing it (the ceiling only rises when a v3 field is actually written).
func TestImportWithoutSourceStaysPreV3(t *testing.T) {
	warrenRoot := t.TempDir()
	if _, err := Init(warrenRoot, "product-platform"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source := writeImportSourceVault(t, filepath.Join(t.TempDir(), ".marmot"), "src-vault")
	manifest, err := ImportProject(warrenRoot, source, Project{ProjectID: "project-a"}, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportProject: %v", err)
	}
	if manifest.Version != 1 {
		t.Fatalf("Version = %d, want 1 (no v2/v3 fields)", manifest.Version)
	}
	if manifest.Projects[0].SourceURL != "" || manifest.Projects[0].SourceCommit != "" {
		t.Fatalf("unexpected source fields: %+v", manifest.Projects[0])
	}
}

// TestManifestV2RoundTripsUntouchedWithoutSourceFields: a v2-era manifest
// (readonly, no source fields) loads and saves at version 2 under the v3
// binary — no gratuitous version churn.
func TestManifestV2RoundTripsUntouchedWithoutSourceFields(t *testing.T) {
	warrenRoot := t.TempDir()
	v2 := "---\nwarren_id: product-platform\nversion: 2\nprojects:\n  - project_id: project-a\n    path: projects/project-a/.marmot\n    readonly: true\n---\n"
	if err := os.WriteFile(filepath.Join(warrenRoot, ManifestFileName), []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, body, err := LoadManifest(warrenRoot)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if manifest.Version != 2 {
		t.Fatalf("Version = %d, want 2 preserved", manifest.Version)
	}
	if err := SaveManifest(warrenRoot, manifest, body); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(warrenRoot, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "version: 2") || strings.Contains(string(data), "source_url") {
		t.Fatalf("v2 round-trip changed the manifest:\n%s", data)
	}
}

// hermeticResolverHome points MARMOT_HOME (and the home override) at a temp
// dir so registry reads/writes stay test-local.
func hermeticResolverHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	t.Setenv("MARMOT_HOME", root)
	return root
}

// seedCacheWarren registers a warren id in the global registry and builds
// its shared read checkout by hand (SaveManifest + project metadata — the
// resolver never needs git).
func seedCacheWarren(t *testing.T, warrenID string, projects []Project) {
	t.Helper()
	if err := warrenreg.Update(func(reg *warrenreg.Registry) error {
		reg.Warrens[warrenID] = warrenreg.Entry{URL: "https://example.com/" + warrenID + ".git", DefaultBranch: "main"}
		return nil
	}); err != nil {
		t.Fatalf("registry update: %v", err)
	}
	checkout := CacheCheckoutPath(warrenID)
	manifest := &Manifest{WarrenID: warrenID, Projects: projects}
	for _, p := range projects {
		marmotDir := filepath.Join(checkout, filepath.FromSlash(p.Path))
		if err := SaveProjectMetadata(marmotDir, &ProjectMetadata{
			ProjectID: p.ProjectID,
			WarrenID:  warrenID,
			VaultID:   p.ProjectID + "-vault",
		}, ""); err != nil {
			t.Fatalf("SaveProjectMetadata: %v", err)
		}
	}
	if err := SaveManifest(checkout, manifest, ""); err != nil {
		t.Fatalf("SaveManifest checkout: %v", err)
	}
}

func TestResolveReferenceWarrenURL(t *testing.T) {
	hermeticResolverHome(t)
	seedCacheWarren(t, "product-platform", []Project{
		{ProjectID: "project-a", Path: "projects/project-a/.marmot", SourceURL: "github.com/acme/project-a", SourceCommit: "abc1234"},
		{ProjectID: "project-b", Path: "projects/project-b/.marmot"},
	})

	// Every spelling of the source URL resolves to the same project.
	for _, url := range []string{
		"https://github.com/acme/project-a",
		"git@github.com:acme/project-a.git",
		"ssh://git@github.com/acme/project-a/",
	} {
		res := ResolveReference(RefSpec{URL: url})
		if res.Via != ResolvedViaWarrenURL {
			t.Fatalf("ResolveReference(%q).Via = %q, want warren-url (%s)", url, res.Via, res.Detail)
		}
		if res.WarrenID != "product-platform" || res.ProjectID != "project-a" {
			t.Fatalf("ResolveReference(%q) = %+v", url, res)
		}
		if res.VaultID != "project-a-vault" {
			t.Fatalf("VaultID = %q, want project-a-vault", res.VaultID)
		}
		if !strings.Contains(res.Detail, "abc1234") {
			t.Fatalf("Detail %q should mention the source commit", res.Detail)
		}
	}

	// URL match wins over a path leg (order (a) before (b)).
	checkoutDir := t.TempDir()
	writeImportSourceVault(t, filepath.Join(checkoutDir, ".marmot"), "checkout-vault-id")
	res := ResolveReference(RefSpec{URL: "https://github.com/acme/project-a", Path: checkoutDir})
	if res.Via != ResolvedViaWarrenURL {
		t.Fatalf("URL leg must win: %+v", res)
	}
}

func TestResolveReferenceCheckoutVault(t *testing.T) {
	hermeticResolverHome(t)
	checkoutDir := t.TempDir()
	writeImportSourceVault(t, filepath.Join(checkoutDir, ".marmot"), "checkout-vault-id")

	res := ResolveReference(RefSpec{URL: "https://github.com/acme/unknown", Path: checkoutDir})
	if res.Via != ResolvedViaCheckoutVault || res.VaultID != "checkout-vault-id" {
		t.Fatalf("ResolveReference = %+v, want checkout-vault/checkout-vault-id", res)
	}
	if res.WarrenID != "" || res.ProjectID != "" {
		t.Fatalf("checkout-vault resolution must not name a warren project: %+v", res)
	}

	// Pointing directly at the .marmot dir also resolves.
	res = ResolveReference(RefSpec{Path: filepath.Join(checkoutDir, ".marmot")})
	if res.Via != ResolvedViaCheckoutVault || res.VaultID != "checkout-vault-id" {
		t.Fatalf("direct .marmot path: %+v", res)
	}
}

func TestResolveReferenceNone(t *testing.T) {
	hermeticResolverHome(t)
	// Unknown URL, no path.
	res := ResolveReference(RefSpec{URL: "https://github.com/acme/unknown"})
	if res.Via != ResolvedViaNone || res.Detail == "" {
		t.Fatalf("ResolveReference = %+v, want none with detail", res)
	}
	// Path without a vault.
	res = ResolveReference(RefSpec{Path: t.TempDir()})
	if res.Via != ResolvedViaNone {
		t.Fatalf("vault-less path: %+v", res)
	}
	// Empty spec.
	res = ResolveReference(RefSpec{Name: "dep"})
	if res.Via != ResolvedViaNone || !strings.Contains(res.Detail, "neither url nor path") {
		t.Fatalf("empty spec: %+v", res)
	}
}
