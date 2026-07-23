package main

// CLI tests for the §15.4/§15.5 reference-resolution foundations: manifest
// v3 source provenance capture at `warren project import`/`add` (explicit
// flags + git auto-detection) and the `marmot resolve` diagnostic verb.
// Hermetic: temp MARMOT_HOME (hermeticDenCLI), local git fixtures, no
// network.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

type resolveEnvelope struct {
	Schema      int    `json:"schema"`
	ResolvedVia string `json:"resolved_via"`
	Warren      string `json:"warren"`
	Project     string `json:"project"`
	VaultID     string `json:"vault_id"`
	Detail      string `json:"detail"`
}

// gitSourceRepo builds a git-initialized source checkout containing a
// .marmot vault, with an origin remote and one commit. Returns the repo root
// and its HEAD commit.
func gitSourceRepo(t *testing.T, originURL string) (repoRoot, head string) {
	t.Helper()
	repoRoot = t.TempDir()
	writeCLIImportSourceVault(t, filepath.Join(repoRoot, ".marmot"), "src-vault")
	gitCLI(t, repoRoot, "init", "-q")
	gitCLI(t, repoRoot, "checkout", "-q", "-b", "main")
	gitCLI(t, repoRoot, "config", "user.email", "test@example.com")
	gitCLI(t, repoRoot, "config", "user.name", "Test")
	gitCLI(t, repoRoot, "add", "-A")
	gitCLI(t, repoRoot, "commit", "-q", "-m", "init source")
	if originURL != "" {
		gitCLI(t, repoRoot, "remote", "add", "origin", originURL)
	}
	return repoRoot, gitCLI(t, repoRoot, "rev-parse", "HEAD")
}

// TestWarrenImportAutoDetectsSourceProvenance: importing from a git-backed
// source captures the canonicalized origin URL and HEAD commit into the
// manifest (v3), without any flags.
func TestWarrenImportAutoDetectsSourceProvenance(t *testing.T) {
	hermeticDenCLI(t)
	warrenRoot := testWarrenRoot(t, "product-platform")
	repoRoot, head := gitSourceRepo(t, "git@GitHub.com:Acme/project-a.git")

	out, code := captureRun([]string{"warren", "project", "import", "project-a", filepath.Join(repoRoot, ".marmot"), "--warren-dir", warrenRoot})
	if code != 0 {
		t.Fatalf("import exit = %d, out=%s", code, out)
	}
	manifest, _, err := warrenpkg.LoadManifest(warrenRoot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 3 {
		t.Fatalf("Version = %d, want 3", manifest.Version)
	}
	project := manifest.Projects[0]
	if project.SourceURL != "github.com/Acme/project-a" {
		t.Fatalf("SourceURL = %q, want canonicalized github.com/Acme/project-a", project.SourceURL)
	}
	if project.SourceCommit != head {
		t.Fatalf("SourceCommit = %q, want %q", project.SourceCommit, head)
	}
}

// TestWarrenImportExplicitSourceOverrides: --source-url/--source-commit win
// over auto-detection.
func TestWarrenImportExplicitSourceOverrides(t *testing.T) {
	hermeticDenCLI(t)
	warrenRoot := testWarrenRoot(t, "product-platform")
	repoRoot, head := gitSourceRepo(t, "git@github.com:acme/wrong.git")

	out, code := captureRun([]string{
		"warren", "project", "import", "project-a", filepath.Join(repoRoot, ".marmot"),
		"--warren-dir", warrenRoot,
		"--source-url", "https://github.com/acme/right.git",
		"--source-commit", "feedfacefeedfacefeedfacefeedfacefeedface",
	})
	if code != 0 {
		t.Fatalf("import exit = %d, out=%s", code, out)
	}
	manifest, _, err := warrenpkg.LoadManifest(warrenRoot)
	if err != nil {
		t.Fatal(err)
	}
	project := manifest.Projects[0]
	if project.SourceURL != "github.com/acme/right" {
		t.Fatalf("SourceURL = %q, want explicit github.com/acme/right", project.SourceURL)
	}
	if project.SourceCommit != "feedfacefeedfacefeedfacefeedfacefeedface" || project.SourceCommit == head {
		t.Fatalf("SourceCommit = %q, want the explicit commit", project.SourceCommit)
	}
}

// TestWarrenImportNonGitSourceLeavesFieldsEmpty: a plain directory source
// yields no provenance and the manifest stays pre-v3.
func TestWarrenImportNonGitSourceLeavesFieldsEmpty(t *testing.T) {
	hermeticDenCLI(t)
	warrenRoot := testWarrenRoot(t, "product-platform")
	source := writeCLIImportSourceVault(t, filepath.Join(t.TempDir(), ".marmot"), "src-vault")

	out, code := captureRun([]string{"warren", "project", "import", "project-a", source, "--warren-dir", warrenRoot})
	if code != 0 {
		t.Fatalf("import exit = %d, out=%s", code, out)
	}
	manifest, _, err := warrenpkg.LoadManifest(warrenRoot)
	if err != nil {
		t.Fatal(err)
	}
	project := manifest.Projects[0]
	if project.SourceURL != "" || project.SourceCommit != "" {
		t.Fatalf("non-git source must leave provenance empty: %+v", project)
	}
	if manifest.Version >= 3 {
		t.Fatalf("Version = %d, want < 3 without source fields", manifest.Version)
	}
}

// TestWarrenProjectAddSourceFlags: `warren project add` records explicit
// source provenance (canonicalized) — no auto-detection on the add path.
func TestWarrenProjectAddSourceFlags(t *testing.T) {
	hermeticDenCLI(t)
	warrenRoot := testWarrenRoot(t, "product-platform")

	out, code := captureRun([]string{
		"warren", "project", "add", "project-b", "--warren-dir", warrenRoot,
		"--source-url", "ssh://git@github.com/acme/project-b/",
		"--source-commit", "abc1234",
	})
	if code != 0 {
		t.Fatalf("add exit = %d, out=%s", code, out)
	}
	manifest, _, err := warrenpkg.LoadManifest(warrenRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range manifest.Projects {
		if project.ProjectID != "project-b" {
			continue
		}
		if project.SourceURL != "github.com/acme/project-b" || project.SourceCommit != "abc1234" {
			t.Fatalf("project-b provenance = %+v", project)
		}
		if manifest.Version != 3 {
			t.Fatalf("Version = %d, want 3", manifest.Version)
		}
		return
	}
	t.Fatal("project-b not found in manifest")
}

// resolveCacheFixture: a cache-backed warren whose manifest carries a
// source_url, added to the shared cache via `warren add` (real git, local
// path remote).
func resolveCacheFixture(t *testing.T, warrenID, projectID, sourceURL, sourceCommit string) {
	t.Helper()
	root := testWarrenRoot(t, warrenID, projectID)
	manifest, body, err := warrenpkg.LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := range manifest.Projects {
		if manifest.Projects[i].ProjectID == projectID {
			manifest.Projects[i].SourceURL = sourceURL
			manifest.Projects[i].SourceCommit = sourceCommit
		}
	}
	if err := warrenpkg.SaveManifest(root, manifest, body); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, root, "init", "-q")
	gitCLI(t, root, "checkout", "-q", "-b", "main")
	gitCLI(t, root, "config", "user.email", "test@example.com")
	gitCLI(t, root, "config", "user.name", "Test")
	gitCLI(t, root, "add", "-A")
	gitCLI(t, root, "commit", "-q", "-m", "init warren")
	if out, code := captureRun([]string{"warren", "add", root, "--id", warrenID, "--json"}); code != 0 {
		t.Fatalf("warren add: code=%d out=%s", code, out)
	}
}

// TestResolveCLIWarrenURL: `marmot resolve --url` matches a cache-backed
// warren project by canonical source_url.
func TestResolveCLIWarrenURL(t *testing.T) {
	hermeticDenCLI(t)
	resolveCacheFixture(t, "product-platform", "project-a", "github.com/acme/project-a", "abc1234def")

	out, code := captureRun([]string{"resolve", "--url", "git@github.com:acme/project-a.git", "--json"})
	if code != 0 {
		t.Fatalf("resolve exit = %d, out=%s", code, out)
	}
	var env resolveEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope parse: %v out=%s", err, out)
	}
	if env.Schema != 1 || env.ResolvedVia != "warren-url" {
		t.Fatalf("envelope = %+v", env)
	}
	if env.Warren != "product-platform" || env.Project != "project-a" {
		t.Fatalf("match = %+v", env)
	}
	if env.VaultID != "project-a" {
		t.Fatalf("vault_id = %q, want project-a (fixture metadata)", env.VaultID)
	}
	if !strings.Contains(env.Detail, "abc1234def") {
		t.Fatalf("detail %q should mention the source commit", env.Detail)
	}

	// Plain-text path.
	out, code = captureRun([]string{"resolve", "--url", "https://github.com/acme/project-a"})
	if code != 0 || !strings.Contains(out, "warren-url: product-platform/project-a") {
		t.Fatalf("plain resolve: code=%d out=%s", code, out)
	}
}

// TestResolveCLICheckoutVault: an unmatched URL falls through to the
// in-checkout vault leg.
func TestResolveCLICheckoutVault(t *testing.T) {
	hermeticDenCLI(t)
	checkout := t.TempDir()
	writeCLIImportSourceVault(t, filepath.Join(checkout, ".marmot"), "checkout-vault-id")

	out, code := captureRun([]string{"resolve", "--url", "https://github.com/acme/unknown", "--path", checkout, "--json"})
	if code != 0 {
		t.Fatalf("resolve exit = %d, out=%s", code, out)
	}
	var env resolveEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope parse: %v out=%s", err, out)
	}
	if env.ResolvedVia != "checkout-vault" || env.VaultID != "checkout-vault-id" {
		t.Fatalf("envelope = %+v", env)
	}
	if env.Warren != "" || env.Project != "" {
		t.Fatalf("checkout-vault must not name a warren project: %+v", env)
	}
}

// TestResolveCLINoneAndInvalidArgs: unknown refs resolve to none (exit 0 —
// diagnostic verb), and a spec with neither url nor path is invalid_args.
func TestResolveCLINoneAndInvalidArgs(t *testing.T) {
	hermeticDenCLI(t)

	out, code := captureRun([]string{"resolve", "--url", "https://github.com/acme/unknown", "--json"})
	if code != 0 {
		t.Fatalf("none resolution is a valid diagnostic answer: code=%d out=%s", code, out)
	}
	var env resolveEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope parse: %v out=%s", err, out)
	}
	if env.ResolvedVia != "none" || env.Detail == "" {
		t.Fatalf("envelope = %+v", env)
	}

	out, code = captureRun([]string{"resolve", "--json"})
	if code != 1 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("missing url/path: code=%d out=%s", code, out)
	}
	// Plain-text refusal goes to stderr, not stdout.
	stdout, stderr, code := captureRunBoth(t, []string{"resolve"})
	if code != 1 || stdout != "" || !strings.Contains(stderr, "at least one of --url or --path") {
		t.Fatalf("plain refusal: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
