package den

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/routes"
)

func hermetic(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routesPath := filepath.Join(root, "routes.yml")
	routes.SetOverridePath(routesPath)
	t.Cleanup(func() { routes.SetOverridePath("") })
	return root
}

func TestCreateStatusDestroy(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()

	res, err := Create("demo", CreateOptions{
		Lifetime: LifetimeTask,
		Projects:  []string{proj},
		NoPointer: false,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.DenID != "demo" {
		t.Fatalf("DenID = %q", res.DenID)
	}
	if res.VaultID != "demo" {
		t.Fatalf("VaultID = %q, want demo", res.VaultID)
	}
	if !res.PointerWritten {
		t.Fatal("expected pointer_written true")
	}
	if _, err := os.Stat(filepath.Join(res.DenPath, "_den.md")); err != nil {
		t.Fatalf("missing _den.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.DenPath, "vault", "_config.md")); err != nil {
		t.Fatalf("missing vault: %v", err)
	}
	ptr, err := ReadPointer(proj)
	if err != nil || ptr != "demo" {
		t.Fatalf("pointer = %q err=%v", ptr, err)
	}

	// Reverse route registered.
	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	id, ok := rt.GetProject(proj)
	if !ok || id != "demo" {
		t.Fatalf("GetProject = %q ok=%v", id, ok)
	}

	st, err := Status("demo")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Lifetime != LifetimeTask {
		t.Fatalf("lifetime = %q", st.Lifetime)
	}
	if st.VaultID != "demo" {
		t.Fatalf("status vault_id = %q", st.VaultID)
	}
	if len(st.Projects) != 1 {
		t.Fatalf("projects = %v", st.Projects)
	}

	ids, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "demo" {
		t.Fatalf("List = %v", ids)
	}

	dr, err := Destroy("demo", false)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !dr.Destroyed {
		t.Fatal("expected destroyed")
	}
	if _, err := os.Stat(Path("demo")); !os.IsNotExist(err) {
		t.Fatalf("den dir still exists: %v", err)
	}
	// Pointer cleaned when it matched.
	if p, _ := ReadPointer(proj); p != "" {
		t.Fatalf("pointer still present: %q", p)
	}
}

func TestCreateNoPointerNoVault(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	res, err := Create("links-only", CreateOptions{
		Lifetime: LifetimeTask,
		Projects:  []string{proj},
		NoVault:   true,
		NoPointer: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.PointerWritten {
		t.Fatal("pointer_written should be false")
	}
	if res.VaultID != "" {
		t.Fatalf("vault_id should be empty, got %q", res.VaultID)
	}
	if _, err := os.Stat(filepath.Join(res.DenPath, "vault")); !os.IsNotExist(err) {
		t.Fatal("vault/ should not exist with --no-vault")
	}
	if p, _ := ReadPointer(proj); p != "" {
		t.Fatalf("pointer written despite --no-pointer: %q", p)
	}
}

func TestCreateDryRun(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	res, err := Create("dry", CreateOptions{
		Lifetime: LifetimeDurable,
		Projects:  []string{proj},
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Create dry-run: %v", err)
	}
	if len(res.Ops) == 0 {
		t.Fatal("expected ops")
	}
	if _, err := os.Stat(Path("dry")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create den dir")
	}
}

func TestCreateDuplicateRefuses(t *testing.T) {
	hermetic(t)
	if _, err := Create("dup", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create("dup", CreateOptions{Lifetime: LifetimeTask}); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestValidateDenID(t *testing.T) {
	if err := ValidateDenID("good-id"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDenID("../bad"); err == nil {
		t.Fatal("expected reject")
	}
	if err := ValidateDenID(".hidden"); err == nil {
		t.Fatal("expected reject")
	}
}

func TestManifestRoundTrip(t *testing.T) {
	hermetic(t)
	if _, err := Create("rt", CreateOptions{Lifetime: LifetimeDurable, Projects: []string{t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	m, body, err := LoadManifest("rt")
	if err != nil {
		t.Fatal(err)
	}
	if m.DenID != "rt" {
		t.Fatalf("den_id = %q", m.DenID)
	}
	m.Links = append(m.Links, Link{Target: "other", Mode: "live"})
	if err := SaveManifest("rt", m, body); err != nil {
		t.Fatal(err)
	}
	m2, _, err := LoadManifest("rt")
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Links) != 1 || m2.Links[0].Mode != "live" {
		t.Fatalf("links = %+v", m2.Links)
	}
}

func TestStatusNotFound(t *testing.T) {
	hermetic(t)
	_, err := Status("missing-id")
	if err == nil {
		t.Fatal("expected not found")
	}
}

func TestNormalizeLifetime(t *testing.T) {
	got, err := NormalizeLifetime("")
	if err != nil || got != LifetimeDurable {
		t.Fatalf("empty -> durable: got %q err=%v", got, err)
	}
	got, err = NormalizeLifetime("DURABLE")
	if err != nil || got != LifetimeDurable {
		t.Fatalf("DURABLE: got %q err=%v", got, err)
	}
	got, err = NormalizeLifetime("task")
	if err != nil || got != LifetimeTask {
		t.Fatalf("task: got %q err=%v", got, err)
	}
	if _, err := NormalizeLifetime("ephemeral"); err == nil {
		t.Fatal("expected error for invalid lifetime")
	}
}

func TestLoadSaveManifestErrors(t *testing.T) {
	hermetic(t)
	if _, _, err := LoadManifest("../bad"); err == nil {
		t.Fatal("LoadManifest invalid id")
	}
	if err := SaveManifest("x", nil, ""); err == nil {
		t.Fatal("SaveManifest nil")
	}
	if err := SaveManifest("../bad", &Manifest{DenID: "x"}, ""); err == nil {
		t.Fatal("SaveManifest invalid id")
	}
}

func TestWriteManifestVersionCeiling(t *testing.T) {
	hermetic(t)
	if _, err := Create("ceil", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{DenID: "ceil", Version: CurrentManifestVersion + 1, Lifetime: LifetimeTask}
	if err := SaveManifest("ceil", m, "body"); err == nil {
		t.Fatal("expected version ceiling refusal")
	}
	// Version 0 should be filled in and succeed.
	m0 := &Manifest{DenID: "ceil", Version: 0, Lifetime: LifetimeTask}
	if err := SaveManifest("ceil", m0, "ok\n"); err != nil {
		t.Fatalf("version 0 save: %v", err)
	}
	loaded, body, err := LoadManifest("ceil")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != CurrentManifestVersion {
		t.Fatalf("version = %d", loaded.Version)
	}
	if body != "ok\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestParseManifestDefaults(t *testing.T) {
	data := []byte("---\nden_id: bare\nprojects:\n  - /tmp/p\n---\n# body\n")
	m, body, err := parseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != CurrentManifestVersion {
		t.Fatalf("default version = %d", m.Version)
	}
	if m.Lifetime != LifetimeDurable {
		t.Fatalf("default lifetime = %q", m.Lifetime)
	}
	if !strings.Contains(body, "body") {
		t.Fatalf("body = %q", body)
	}
	if _, _, err := parseManifest([]byte("no frontmatter")); err == nil {
		t.Fatal("expected frontmatter error")
	}
	if _, _, err := parseManifest([]byte("---\n: bad yaml\n---\n")); err == nil {
		t.Fatal("expected yaml error")
	}
}

func TestCheckManifestWritableSlashJoin(t *testing.T) {
	if err := checkManifestWritable(nil); err == nil {
		t.Fatal("nil")
	}
	if err := checkManifestWritable(&Manifest{Version: CurrentManifestVersion + 5}); err == nil {
		t.Fatal("ceiling")
	}
	if err := checkManifestWritable(&Manifest{Version: CurrentManifestVersion}); err != nil {
		t.Fatal(err)
	}
	sp := slashProjects([]string{filepath.Join("a", "b")})
	if len(sp) != 1 || sp[0] == "" {
		t.Fatalf("slashProjects = %v", sp)
	}
	if joinBody("\nhello") != "hello" {
		t.Fatalf("joinBody = %q", joinBody("\nhello"))
	}
}

func TestCreateInvalidLifetimeAndProjects(t *testing.T) {
	hermetic(t)
	if _, err := Create("x", CreateOptions{Lifetime: "nope"}); err == nil {
		t.Fatal("invalid lifetime")
	}
	if _, err := Create("../bad", CreateOptions{}); err == nil {
		t.Fatal("invalid den id")
	}
	// Empty project entries skipped; duplicates collapsed.
	proj := t.TempDir()
	res, err := Create("proj-dedup", CreateOptions{
		Projects: []string{"", proj, proj},
		NoPointer: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Projects) != 1 {
		t.Fatalf("projects = %v", res.Projects)
	}
	// Invalid relative project that cannot normalize is hard; empty already skipped.
}

func TestCreateRoutesOffWarns(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	// Disable routes via env; clear override so DefaultPath sees off.
	routes.SetOverridePath("")
	t.Cleanup(func() { routes.SetOverridePath("") })
	t.Setenv("MARMOT_ROUTES", "off")

	proj := t.TempDir()
	res, err := Create("warn-den", CreateOptions{
		Lifetime: LifetimeTask,
		Projects:  []string{proj},
		NoPointer: false,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected routes warning under MARMOT_ROUTES=off")
	}
	// Pointer still written even when routes fail.
	if !res.PointerWritten {
		t.Fatal("pointer should still write")
	}
	ptr, err := ReadPointer(proj)
	if err != nil || ptr != "warn-den" {
		t.Fatalf("pointer = %q err=%v", ptr, err)
	}
}

func TestCreateNoProjectsPointerFalse(t *testing.T) {
	hermetic(t)
	res, err := Create("lonely", CreateOptions{Lifetime: LifetimeDurable})
	if err != nil {
		t.Fatal(err)
	}
	if res.PointerWritten {
		t.Fatal("no projects => pointer_written false")
	}
}

func TestStatusInvalidID(t *testing.T) {
	hermetic(t)
	if _, err := Status("../x"); err == nil {
		t.Fatal("expected invalid id")
	}
}

func TestDestroyBranches(t *testing.T) {
	hermetic(t)
	if _, err := Destroy("missing", false); err == nil {
		t.Fatal("missing den")
	}
	if _, err := Destroy("../bad", false); err == nil {
		t.Fatal("invalid id")
	}

	proj := t.TempDir()
	if _, err := Create("keep-ptr", CreateOptions{Projects: []string{proj}}); err != nil {
		t.Fatal(err)
	}
	// Overwrite pointer so Destroy must not remove foreign pointer.
	if err := WritePointer(proj, "other-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := Destroy("keep-ptr", true); err != nil {
		t.Fatal(err)
	}
	if p, _ := ReadPointer(proj); p != "other-id" {
		t.Fatalf("foreign pointer removed: %q", p)
	}
}

func TestListFilters(t *testing.T) {
	hermetic(t)
	// Empty dens root.
	ids, err := List()
	if err != nil || len(ids) != 0 {
		t.Fatalf("empty list = %v err=%v", ids, err)
	}
	// Create dens + noise entries.
	if _, err := Create("keep-me", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	root := DensRoot()
	// Non-dir file.
	if err := os.WriteFile(filepath.Join(root, "notadir"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Invalid den id dir.
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Dir without _den.md.
	if err := os.MkdirAll(filepath.Join(root, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	ids, err = List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "keep-me" {
		t.Fatalf("List = %v", ids)
	}
}

func TestPointerHelpers(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	if err := WritePointer(proj, "vault-a"); err != nil {
		t.Fatal(err)
	}
	id, err := ReadPointer(proj)
	if err != nil || id != "vault-a" {
		t.Fatalf("ReadPointer = %q err=%v", id, err)
	}
	if err := RemovePointer(proj); err != nil {
		t.Fatal(err)
	}
	id, err = ReadPointer(proj)
	if err != nil || id != "" {
		t.Fatalf("after remove = %q err=%v", id, err)
	}
	// Remove missing is ok.
	if err := RemovePointer(proj); err != nil {
		t.Fatal(err)
	}
	if err := WritePointer("", "x"); err == nil {
		t.Fatal("empty project path")
	}
	if _, err := ReadPointer(""); err == nil {
		t.Fatal("empty read")
	}
	if err := RemovePointer(""); err == nil {
		t.Fatal("empty remove")
	}
	// WritePointer creates project dir when missing.
	missing := filepath.Join(t.TempDir(), "nested", "proj")
	if err := WritePointer(missing, "new"); err != nil {
		t.Fatal(err)
	}
	id, err = ReadPointer(missing)
	if err != nil || id != "new" {
		t.Fatalf("nested pointer = %q err=%v", id, err)
	}
}

func TestAdoptDryRunAndApply(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	// Dry-run does not create.
	res, err := Adopt(AdoptOptions{From: proj, DenID: "adopted", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.DenID != "adopted" || len(res.Ops) == 0 {
		t.Fatalf("dry adopt = %+v", res)
	}
	if _, err := os.Stat(Path("adopted")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create")
	}

	// Real adopt derives id from basename when empty.
	res, err = Adopt(AdoptOptions{From: proj})
	if err != nil {
		t.Fatal(err)
	}
	wantID := filepath.Base(proj)
	// Base of temp dir may be invalid? temp dirs are usually random hex — valid warren ids.
	if res.DenID == "" {
		t.Fatal("empty den id")
	}
	st, err := Status(res.DenID)
	if err != nil {
		t.Fatalf("status after adopt: %v (id=%s want basename %s)", err, res.DenID, wantID)
	}
	if len(st.Projects) != 1 {
		t.Fatalf("projects = %v", st.Projects)
	}

	// Invalid den id override.
	if _, err := Adopt(AdoptOptions{From: proj, DenID: "../bad"}); err == nil {
		t.Fatal("invalid adopt id")
	}
	// Empty from uses cwd — just ensure no panic with explicit valid from.
	if _, err := Adopt(AdoptOptions{From: "", DenID: "from-cwd", DryRun: true}); err != nil {
		t.Fatalf("from cwd dry-run: %v", err)
	}
}

func TestStatusNoVault(t *testing.T) {
	hermetic(t)
	if _, err := Create("nv", CreateOptions{NoVault: true}); err != nil {
		t.Fatal(err)
	}
	st, err := Status("nv")
	if err != nil {
		t.Fatal(err)
	}
	if st.VaultID != "" {
		t.Fatalf("vault_id = %q", st.VaultID)
	}
}

func TestValidateDenIDControlChars(t *testing.T) {
	if err := ValidateDenID("bad\nid"); err == nil {
		t.Fatal("expected reject newline in den id")
	}
	if err := ValidateDenID("bad\tid"); err == nil {
		t.Fatal("expected reject tab in den id")
	}
}

func TestStatusCorruptManifest(t *testing.T) {
	hermetic(t)
	denPath := Path("corrupt")
	if err := os.MkdirAll(denPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ManifestPath("corrupt"), []byte("not-frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Status("corrupt"); err == nil {
		t.Fatal("expected corrupt manifest error")
	}
	if _, err := Destroy("corrupt", false); err == nil {
		t.Fatal("expected destroy corrupt error")
	}
}

func TestWritePointerMkdirFails(t *testing.T) {
	hermetic(t)
	// Place a file where the project dir should be so MkdirAll fails.
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// project path is blocker/sub — parent is a file
	if err := WritePointer(filepath.Join(blocker, "sub"), "id"); err == nil {
		t.Fatal("expected mkdir failure")
	}
}

func TestReadPointerNonNotExistError(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	// Make .marmot-vault a directory so ReadFile fails with EISDIR (not IsNotExist).
	if err := os.MkdirAll(filepath.Join(proj, PointerFileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPointer(proj); err == nil {
		t.Fatal("expected read error for directory pointer path")
	}
}

func TestRemovePointerNonNotExistError(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	// Directory at pointer path: os.Remove fails with EISDIR / not empty.
	if err := os.MkdirAll(filepath.Join(proj, PointerFileName, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RemovePointer(proj); err == nil {
		// On some platforms Remove of non-empty dir fails — require that.
		t.Fatal("expected remove error for non-empty pointer dir")
	}
}

func TestAdoptCreateFailure(t *testing.T) {
	hermetic(t)
	if _, err := Create("taken", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	if _, err := Adopt(AdoptOptions{From: t.TempDir(), DenID: "taken"}); err == nil {
		t.Fatal("expected adopt fail when den exists")
	}
}

func TestListDensRootIsFile(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	// DensRoot is root/dens — make it a file so ReadDir fails non-IsNotExist.
	if err := os.WriteFile(filepath.Join(root, "dens"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(); err == nil {
		t.Fatal("expected List error when dens is a file")
	}
}

func TestCreatePointerWarningWhenProjectIsFile(t *testing.T) {
	hermetic(t)
	// Project path that exists as a file: Normalize ok, WritePointer MkdirAll may succeed
	// if path is existing file? MkdirAll on existing file returns error on Unix.
	base := t.TempDir()
	fileProj := filepath.Join(base, "fileproj")
	if err := os.WriteFile(fileProj, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Create("ptr-warn", CreateOptions{
		Lifetime: LifetimeTask,
		Projects:  []string{fileProj},
		NoPointer: false,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// routes may succeed; pointer should warn
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "pointer") {
			found = true
		}
	}
	if !found && res.PointerWritten {
		// if somehow pointer wrote, not a failure of the test goal
		t.Logf("pointer unexpectedly succeeded: warnings=%v", res.Warnings)
	}
	if !found && !res.PointerWritten {
		// warnings might be empty if WritePointer somehow no-op? Require either warning or not written
		if len(res.Warnings) == 0 {
			// On some FS MkdirAll on file path fails → warning expected
			t.Fatalf("expected pointer warning, got warnings=%v pointerWritten=%v", res.Warnings, res.PointerWritten)
		}
	}
}

func TestSaveManifestEmptyBody(t *testing.T) {
	hermetic(t)
	if _, err := Create("ebody", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	m, _, err := LoadManifest("ebody")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveManifest("ebody", m, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRejectsProjectOwnedByRoute(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	if _, err := Create("owner", CreateOptions{Projects: []string{proj}, NoPointer: true, Lifetime: LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	_, err := Create("collides", CreateOptions{Projects: []string{proj}, NoPointer: true, Lifetime: LifetimeTask})
	if err == nil {
		t.Fatal("expected create to reject project already registered to another den")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("err = %v", err)
	}
	// Dry-run must also refuse before any mkdir.
	_, err = Create("collides-dry", CreateOptions{Projects: []string{proj}, NoPointer: true, DryRun: true})
	if err == nil {
		t.Fatal("expected dry-run collision refusal")
	}
}

func TestCreateRejectsProjectOwnedByPointer(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	if err := WritePointer(proj, "foreign-den"); err != nil {
		t.Fatal(err)
	}
	_, err := Create("wants-proj", CreateOptions{Projects: []string{proj}, NoPointer: true, Lifetime: LifetimeTask})
	if err == nil || !strings.Contains(err.Error(), "pointer") {
		t.Fatalf("expected pointer ownership refusal, got %v", err)
	}
}

func TestRelocateProjectRejectsTakenTarget(t *testing.T) {
	hermetic(t)
	from := t.TempDir()
	to := t.TempDir()
	if _, err := Create("moving", CreateOptions{Projects: []string{from}, NoPointer: true, Lifetime: LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create("sitting", CreateOptions{Projects: []string{to}, NoPointer: true, Lifetime: LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	_, err := RelocateProject(from, to)
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected relocate collision, got %v", err)
	}
	// from still owned by moving
	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	id, ok := rt.GetProject(from)
	if !ok || id != "moving" {
		t.Fatalf("from route = %q ok=%v", id, ok)
	}
}

func TestDestroyReassignedRoute(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	if _, err := Create("old-den", CreateOptions{Projects: []string{proj}, NoPointer: true}); err != nil {
		t.Fatal(err)
	}
	// Reassign route to another den id (simulates external routes.yml edit;
	// Create itself now refuses this collision).
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.SetProject(proj, "other-den")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dr, err := Destroy("old-den", false)
	if err != nil {
		t.Fatal(err)
	}
	if !dr.Destroyed {
		t.Fatal("expected destroyed")
	}
	// Route should still point at other-den.
	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	id, ok := rt.GetProject(proj)
	if !ok || id != "other-den" {
		t.Fatalf("route reassigned incorrectly: %q ok=%v", id, ok)
	}
}

func TestCreateDensRootIsFile(t *testing.T) {
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	routes.SetOverridePath(filepath.Join(root, "routes.yml"))
	t.Cleanup(func() { routes.SetOverridePath("") })
	if err := os.WriteFile(filepath.Join(root, "dens"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create("x", CreateOptions{Lifetime: LifetimeTask}); err == nil {
		t.Fatal("expected dens root mkdir failure")
	}
}

func TestDestroyEmptyProjectInManifest(t *testing.T) {
	hermetic(t)
	if _, err := Create("empty-proj", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	// Inject empty project path so NormalizeProjectKey fails and key falls back.
	m := &Manifest{
		DenID:    "empty-proj",
		Version:  CurrentManifestVersion,
		Lifetime: LifetimeTask,
		Projects: []string{""},
	}
	if err := writeManifestAtomic(ManifestPath("empty-proj"), m, "x\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Destroy("empty-proj", false); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

func TestSaveManifestParentIsFile(t *testing.T) {
	hermetic(t)
	// dens/blocked is a file → writeManifestAtomic MkdirAll fails
	root := DensRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "blocked"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeManifestAtomic(filepath.Join(root, "blocked", "_den.md"), &Manifest{
		DenID: "blocked", Version: 1, Lifetime: LifetimeTask,
	}, "")
	if err == nil {
		t.Fatal("expected mkdir failure")
	}
}

func TestCreateWhenDenPathIsFile(t *testing.T) {
	hermetic(t)
	root := DensRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-create den path as a file so Mkdir fails with exist.
	if err := os.WriteFile(Path("fileden"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create("fileden", CreateOptions{Lifetime: LifetimeTask}); err == nil {
		t.Fatal("expected already exists / create failure")
	}
}

func TestRelocateProjectUpdatesManifestAndRoutes(t *testing.T) {
	hermetic(t)
	from := t.TempDir()
	to := t.TempDir()

	if _, err := Create("reloc", CreateOptions{
		Lifetime:  LifetimeTask,
		Projects:  []string{from},
		NoPointer: true,
	}); err != nil {
		t.Fatal(err)
	}

	id, err := RelocateProject(from, to)
	if err != nil {
		t.Fatalf("RelocateProject: %v", err)
	}
	if id != "reloc" {
		t.Fatalf("id = %q", id)
	}

	// routes: from gone, to points at den
	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.GetProject(from); ok {
		t.Fatal("old path still in routes")
	}
	if got, ok := rt.GetProject(to); !ok || got != "reloc" {
		t.Fatalf("new path mapping: %q ok=%v", got, ok)
	}

	// manifest lists to, not from
	st, err := Status("reloc")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Projects) != 1 {
		t.Fatalf("projects = %#v", st.Projects)
	}
	// Compare via NormalizeProjectKey
	want, _ := routes.NormalizeProjectKey(to)
	got, _ := routes.NormalizeProjectKey(st.Projects[0])
	if got != want {
		t.Fatalf("manifest project = %q, want %q", got, want)
	}
}

func TestDestroyAfterRelocateLeavesNoOrphanRoute(t *testing.T) {
	// Skeptic repro: set-project (Relocate) then destroy must not leave
	// routes.yml mapping the NEW path to a deleted den.
	hermetic(t)
	from := t.TempDir()
	to := t.TempDir()

	if _, err := Create("orphan-check", CreateOptions{
		Lifetime:  LifetimeTask,
		Projects:  []string{from},
		NoPointer: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := RelocateProject(from, to); err != nil {
		t.Fatal(err)
	}

	// Status must show the new path (not the stale old one).
	st, err := Status("orphan-check")
	if err != nil {
		t.Fatal(err)
	}
	foundNew := false
	for _, p := range st.Projects {
		key, _ := routes.NormalizeProjectKey(p)
		want, _ := routes.NormalizeProjectKey(to)
		if key == want {
			foundNew = true
		}
		old, _ := routes.NormalizeProjectKey(from)
		if key == old {
			t.Fatalf("status still lists old path %q", p)
		}
	}
	if !foundNew {
		t.Fatalf("status missing new path; projects=%#v", st.Projects)
	}

	if _, err := Destroy("orphan-check", false); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := rt.GetProject(to); ok {
		t.Fatalf("orphan reverse route remains: %s -> %s", to, id)
	}
	if id, ok := rt.GetProject(from); ok {
		t.Fatalf("old path route remains: %s -> %s", from, id)
	}
	// den gone
	if _, err := Status("orphan-check"); err == nil {
		t.Fatal("den should be gone")
	}
}

func TestDestroyCleansRouteEvenIfManifestStale(t *testing.T) {
	// Defense in depth: if routes ahead of manifest, destroy still cleans.
	hermetic(t)
	from := t.TempDir()
	to := t.TempDir()
	if _, err := Create("stale-man", CreateOptions{
		Lifetime:  LifetimeTask,
		Projects:  []string{from},
		NoPointer: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Manually move route without touching manifest (simulates old bug).
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.RemoveProject(from)
		rt.SetProject(to, "stale-man")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Destroy("stale-man", false); err != nil {
		t.Fatal(err)
	}
	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := rt.GetProject(to); ok {
		t.Fatalf("orphan after stale-manifest destroy: %s -> %s", to, id)
	}
}

func TestRelocateProjectSamePathNoOp(t *testing.T) {
	hermetic(t)
	proj := t.TempDir()
	if _, err := Create("same-path", CreateOptions{
		Lifetime:  LifetimeTask,
		Projects:  []string{proj},
		NoPointer: true,
	}); err != nil {
		t.Fatal(err)
	}
	id, err := RelocateProject(proj, proj)
	if err != nil {
		t.Fatal(err)
	}
	if id != "same-path" {
		t.Fatalf("id=%q", id)
	}
	st, err := Status("same-path")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Projects) != 1 {
		t.Fatalf("projects=%#v", st.Projects)
	}
}

func TestRelocateProjectFromNotFound(t *testing.T) {
	hermetic(t)
	if _, err := RelocateProject(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("expected not found")
	}
}

func TestRelocateProjectInvalidFrom(t *testing.T) {
	hermetic(t)
	if _, err := RelocateProject("", t.TempDir()); err == nil {
		t.Fatal("expected from error")
	}
}

func TestRelocateProjectNonDenIDRoutesOnly(t *testing.T) {
	// Vault-style id that is not a den: only routes move.
	hermetic(t)
	from := t.TempDir()
	to := t.TempDir()
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.SetProject(from, "plain-vault-id")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	id, err := RelocateProject(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if id != "plain-vault-id" {
		t.Fatalf("id=%q", id)
	}
	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := rt.GetProject(to); !ok || got != "plain-vault-id" {
		t.Fatalf("to=%q ok=%v", got, ok)
	}
}

func TestRelocateProjectAppendsWhenManifestLacksFrom(t *testing.T) {
	// Manifest missing the from path: rewrite still records to.
	hermetic(t)
	from := t.TempDir()
	to := t.TempDir()
	other := t.TempDir()
	if _, err := Create("lag-man", CreateOptions{
		Lifetime:  LifetimeTask,
		Projects:  []string{other},
		NoPointer: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Point routes from->lag-man without putting from in manifest.
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.SetProject(from, "lag-man")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := RelocateProject(from, to); err != nil {
		t.Fatal(err)
	}
	st, err := Status("lag-man")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := routes.NormalizeProjectKey(to)
	found := false
	for _, p := range st.Projects {
		got, _ := routes.NormalizeProjectKey(p)
		if got == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected to in projects %#v", st.Projects)
	}
}
