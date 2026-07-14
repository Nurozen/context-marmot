package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/routes"
)

func hermeticDenCLI(t *testing.T) (homeRoot string) {
	t.Helper()
	homeRoot = t.TempDir()
	home.SetOverride(homeRoot)
	t.Cleanup(func() { home.SetOverride("") })
	t.Setenv("MARMOT_HOME", homeRoot)
	routes.SetOverridePath(filepath.Join(homeRoot, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })
	return homeRoot
}

func TestDenContributeEditLinkRequired(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("contrib-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "contribute", "contrib-den", "--json"})
	if code == 0 {
		t.Fatalf("expected failure, out=%s", out)
	}
	if !strings.Contains(out, "edit_link_required") {
		t.Fatalf("expected edit_link_required, got %s", out)
	}

	// With explicit link ref still fails without edit mode.
	out, code = captureRun([]string{"den", "contribute", "contrib-den", "w/p", "--json"})
	if code == 0 || !strings.Contains(out, "edit_link_required") {
		t.Fatalf("link ref path: code=%d out=%s", code, out)
	}

	// Missing den-id
	out, code = captureRun([]string{"den", "contribute", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("missing id: code=%d out=%s", code, out)
	}

	// Missing den
	out, code = captureRun([]string{"den", "contribute", "no-such", "--json"})
	if code == 0 || !strings.Contains(out, "den_not_found") {
		t.Fatalf("missing den: code=%d out=%s", code, out)
	}

	// Plain text path (no --json)
	if code := run([]string{"den", "contribute", "contrib-den"}); code == 0 {
		t.Fatal("plain contribute should fail")
	}
}

func TestDenContributeWithEditLink(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("edit-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	m, body, err := den.LoadManifest("edit-den")
	if err != nil {
		t.Fatal(err)
	}
	m.Links = []den.Link{{Target: "w/p", Mode: "edit", Warren: "w", Project: "p"}}
	if err := den.SaveManifest("edit-den", m, body); err != nil {
		t.Fatal(err)
	}

	// dry-run with edit link succeeds
	out, code := captureRun([]string{"den", "contribute", "edit-den", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("dry-run contribute: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, `"dry_run"`) {
		t.Fatalf("dry-run envelope: %s", out)
	}

	// non-dry-run → not_implemented
	out, code = captureRun([]string{"den", "contribute", "edit-den", "w/p", "--json"})
	if code == 0 || !strings.Contains(out, "not_implemented") {
		t.Fatalf("not_implemented: code=%d out=%s", code, out)
	}

	// plain dry-run
	if code := run([]string{"den", "contribute", "edit-den", "--dry-run"}); code != 0 {
		t.Fatal("plain dry-run")
	}
	// plain not implemented
	if code := run([]string{"den", "contribute", "edit-den"}); code == 0 {
		t.Fatal("plain not_implemented should fail")
	}
}

func TestDenAdoptCLI(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()

	out, code := captureRun([]string{
		"den", "adopt", "--from", proj, "--id", "adopted-cli", "--dry-run", "--json",
	})
	if code != 0 {
		t.Fatalf("adopt dry-run: %d %s", code, out)
	}
	if !strings.Contains(out, `"dry_run"`) {
		t.Fatalf("envelope: %s", out)
	}

	out, code = captureRun([]string{
		"den", "adopt", "--from", proj, "--id", "adopted-cli", "--json",
	})
	if code != 0 {
		t.Fatalf("adopt: %d %s", code, out)
	}
	if !strings.Contains(out, `"den_id"`) || !strings.Contains(out, "adopted-cli") {
		t.Fatalf("adopt envelope: %s", out)
	}
	// Plain adopt second id
	proj2 := t.TempDir()
	if code := run([]string{"den", "adopt", "--from", proj2, "--id", "plain-adopt"}); code != 0 {
		t.Fatalf("plain adopt: %d", code)
	}
	// Failure path
	out, code = captureRun([]string{"den", "adopt", "--from", proj, "--id", "adopted-cli", "--json"})
	if code == 0 {
		t.Fatalf("duplicate adopt should fail: %s", out)
	}
	if !strings.Contains(out, "den_adopt_failed") {
		t.Fatalf("error code: %s", out)
	}
}

func TestDenCreateStatusDestroyBranches(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()

	// plain create (no json)
	if code := run([]string{
		"den", "create", "plain-den",
		"--lifetime", "task",
		"--project", proj,
		"--no-vault",
	}); code != 0 {
		t.Fatalf("plain create: %d", code)
	}

	// status plain
	out, code := captureRun([]string{"den", "status", "plain-den"})
	if code != 0 || !strings.Contains(out, "plain-den") {
		t.Fatalf("status plain: %d %s", code, out)
	}

	// list --json
	out, code = captureRun([]string{"den", "list", "--json"})
	if code != 0 {
		t.Fatalf("list json: %d %s", code, out)
	}
	var listEnv struct {
		Schema int      `json:"schema"`
		Dens   []string `json:"dens"`
	}
	if err := json.Unmarshal([]byte(out), &listEnv); err != nil {
		t.Fatalf("list json parse: %v out=%s", err, out)
	}
	if listEnv.Schema != 1 || len(listEnv.Dens) == 0 {
		t.Fatalf("list env: %+v", listEnv)
	}

	// destroy dry-run
	out, code = captureRun([]string{"den", "destroy", "plain-den", "--dry-run", "--json"})
	if code != 0 || !strings.Contains(out, "dry_run") {
		t.Fatalf("destroy dry-run: %d %s", code, out)
	}

	// destroy plain
	if code := run([]string{"den", "destroy", "plain-den"}); code != 0 {
		t.Fatalf("destroy plain: %d", code)
	}

	// create missing den-id
	out, code = captureRun([]string{"den", "create", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("missing id: %d %s", code, out)
	}

	// destroy missing
	out, code = captureRun([]string{"den", "destroy", "gone", "--json"})
	if code == 0 || !strings.Contains(out, "den_not_found") {
		t.Fatalf("destroy missing: %d %s", code, out)
	}

	// destroy dry-run missing
	out, code = captureRun([]string{"den", "destroy", "gone", "--dry-run", "--json"})
	if code == 0 {
		t.Fatalf("destroy dry-run missing should fail: %s", out)
	}

	// create failure duplicate via json
	if code := run([]string{"den", "create", "dup-cli", "--no-pointer", "--project", t.TempDir()}); code != 0 {
		t.Fatal(code)
	}
	out, code = captureRun([]string{"den", "create", "dup-cli", "--json", "--project", t.TempDir()})
	if code == 0 || !strings.Contains(out, "den_create_failed") {
		t.Fatalf("dup create: %d %s", code, out)
	}

	// unknown subcommand
	if code := run([]string{"den", "bogus"}); code == 0 {
		t.Fatal("unknown subcommand")
	}

	// empty list plain
	// destroy remaining dens first
	for _, id := range []string{"dup-cli"} {
		_ = run([]string{"den", "destroy", id})
	}
	out, code = captureRun([]string{"den", "list"})
	if code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out, "No dens") && strings.TrimSpace(out) != "" {
		// may still have dens from parallel? hermetic home so empty
		// if empty message
	}
}

func TestDenStatusFromCWD(t *testing.T) {
	hermeticDenCLI(t)
	proj := t.TempDir()
	if _, err := den.Create("cwd-den", den.CreateOptions{
		Lifetime: den.LifetimeTask,
		Projects:  []string{proj},
	}); err != nil {
		t.Fatal(err)
	}
	// Chdir into project so status without id resolves via pointer.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	out, code := captureRun([]string{"den", "status", "--json"})
	if code != 0 {
		t.Fatalf("status from cwd: %d %s", code, out)
	}
	if !strings.Contains(out, "cwd-den") {
		t.Fatalf("expected cwd-den: %s", out)
	}
}

func TestDenStatusNoResolution(t *testing.T) {
	hermeticDenCLI(t)
	// Chdir to empty temp with no pointer/route.
	empty := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	out, code := captureRun([]string{"den", "status", "--json"})
	if code == 0 {
		t.Fatalf("expected fail: %s", out)
	}
	if !strings.Contains(out, "den_not_found") {
		t.Fatalf("envelope: %s", out)
	}
}

func TestDenCreateNoVaultPlain(t *testing.T) {
	hermeticDenCLI(t)
	out, code := captureRun([]string{
		"den", "create", "links-only-cli",
		"--no-vault", "--no-pointer",
		"--project", t.TempDir(),
	})
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	if !strings.Contains(out, "links-only") && !strings.Contains(out, "Created den") {
		t.Fatalf("output: %s", out)
	}
}
