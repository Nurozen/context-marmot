package main

// CLI tests for the P4 den round: pinned/live links (`den link --link`),
// `den unlink`, `den create --ref` (§15.5 machine grammar), den status
// freshness (§9), and promote-on-destroy wiring (§15.3 sibling). Hermetic:
// temp MARMOT_HOME; git only where a real cache is the point.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/routes"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
	"github.com/nurozen/context-marmot/internal/warrenreg"
)

// denP4LinkEnvelope is denLinkEnvelope plus the additive pinned_commit field.
type denP4LinkEnvelope struct {
	Schema int    `json:"schema"`
	DenID  string `json:"den_id"`
	Link   struct {
		Target  string `json:"target"`
		Mode    string `json:"mode"`
		Warren  string `json:"warren"`
		Project string `json:"project"`
	} `json:"link"`
	PinnedCommit string   `json:"pinned_commit"`
	Warnings     []string `json:"warnings"`
}

type denP4StatusEnvelope struct {
	Schema int    `json:"schema"`
	DenID  string `json:"den_id"`
	Links  []struct {
		Ref          string  `json:"ref"`
		Mode         string  `json:"mode"`
		PinnedCommit *string `json:"pinned_commit"`
		Ahead        int     `json:"ahead"`
		Behind       int     `json:"behind"`
		PendingEdits int     `json:"pending_edits"`
		State        string  `json:"state"`
		SourceCommit string  `json:"source_commit"`
	} `json:"links"`
}

// seedCLICacheWarren registers a warren id in the global registry and builds
// its shared read checkout by hand (no git — resolver/link validation only
// read the manifest and the pin sidecar).
func seedCLICacheWarren(t *testing.T, warrenID string, projects []warrenpkg.Project) {
	t.Helper()
	if err := warrenreg.Update(func(reg *warrenreg.Registry) error {
		reg.Warrens[warrenID] = warrenreg.Entry{URL: "https://example.com/" + warrenID + ".git", DefaultBranch: "main"}
		return nil
	}); err != nil {
		t.Fatalf("registry update: %v", err)
	}
	checkout := warrenpkg.CacheCheckoutPath(warrenID)
	for _, p := range projects {
		marmotDir := filepath.Join(checkout, filepath.FromSlash(p.Path))
		if err := warrenpkg.SaveProjectMetadata(marmotDir, &warrenpkg.ProjectMetadata{
			ProjectID: p.ProjectID,
			WarrenID:  warrenID,
			VaultID:   p.ProjectID + "-vault",
		}, ""); err != nil {
			t.Fatalf("SaveProjectMetadata: %v", err)
		}
	}
	if err := warrenpkg.SaveManifest(checkout, &warrenpkg.Manifest{WarrenID: warrenID, Projects: projects}, ""); err != nil {
		t.Fatalf("SaveManifest checkout: %v", err)
	}
}

func TestDenLinkPinnedCacheBacked(t *testing.T) {
	hermeticDenCLI(t)
	seedCLICacheWarren(t, "platform", []warrenpkg.Project{
		{ProjectID: "docs", Path: "projects/docs/.marmot"},
	})
	const pin = "feedc0ffee00112233445566778899aabbccddee"
	if err := warrenpkg.WriteCachePin("platform", pin); err != nil {
		t.Fatalf("WriteCachePin: %v", err)
	}
	if _, err := den.Create("pin-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatalf("den.Create: %v", err)
	}

	out, code := captureRun([]string{"den", "link", "pin-den", "--link", "platform/docs", "--json"})
	if code != 0 {
		t.Fatalf("den link --link: code=%d out=%s", code, out)
	}
	var env denP4LinkEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v out=%s", err, out)
	}
	if env.Link.Mode != "link" || env.Link.Warren != "platform" || env.Link.Project != "docs" {
		t.Fatalf("link body: %+v", env.Link)
	}
	if env.PinnedCommit != pin {
		t.Fatalf("pinned_commit = %q, want %q", env.PinnedCommit, pin)
	}
	if len(env.Warnings) != 0 {
		t.Fatalf("warnings: %+v", env.Warnings)
	}

	// The link is a pure manifest entry: no mount-state change.
	if mounts, err := warrenpkg.ActiveMounts(den.VaultPath("pin-den")); err == nil && len(mounts) != 0 {
		t.Fatalf("pinned link must not create mounts, got %+v", mounts)
	}
	st, err := den.Status("pin-den")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Links) != 1 || st.Links[0].Mode != den.LinkModeLink || st.Links[0].PinnedRef != pin {
		t.Fatalf("manifest link: %+v", st.Links)
	}

	// Idempotent re-link dedupes with a warning.
	out, code = captureRun([]string{"den", "link", "pin-den", "--link", "platform/docs", "--json"})
	if code != 0 || !strings.Contains(out, "already linked") {
		t.Fatalf("re-link: code=%d out=%s", code, out)
	}

	// Unknown project / unknown warren refusals.
	out, code = captureRun([]string{"den", "link", "pin-den", "--link", "platform/nope", "--json"})
	if code == 0 || !strings.Contains(out, "project_not_found") {
		t.Fatalf("unknown project: code=%d out=%s", code, out)
	}
	out, code = captureRun([]string{"den", "link", "pin-den", "--link", "ghost/nope", "--json"})
	if code == 0 || !strings.Contains(out, "warren_not_registered") {
		t.Fatalf("unknown warren: code=%d out=%s", code, out)
	}

	// --edit and --link are mutually exclusive.
	out, code = captureRun([]string{"den", "link", "pin-den", "--edit", "a/b", "--link", "c/d", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("exclusive flags: code=%d out=%s", code, out)
	}
}

func TestDenLinkLiveAndRefusals(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("consumer", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	if _, err := den.Create("durable-target", den.CreateOptions{Lifetime: den.LifetimeDurable}); err != nil {
		t.Fatal(err)
	}
	if _, err := den.Create("task-target", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{"den", "link", "consumer", "--link", "durable-target", "--json"})
	if code != 0 {
		t.Fatalf("live link: code=%d out=%s", code, out)
	}
	var env denP4LinkEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if env.Link.Mode != "live" || env.Link.Target != "durable-target" || env.PinnedCommit != "" {
		t.Fatalf("live link body: %+v", env)
	}

	// A task-lifetime den may not be a live target (plan §6).
	out, code = captureRun([]string{"den", "link", "consumer", "--link", "task-target", "--json"})
	if code == 0 || !strings.Contains(out, "task_den_refused") {
		t.Fatalf("task target: code=%d out=%s", code, out)
	}
	// Missing target den.
	out, code = captureRun([]string{"den", "link", "consumer", "--link", "no-such-den", "--json"})
	if code == 0 || !strings.Contains(out, "link_target_not_found") {
		t.Fatalf("missing target: code=%d out=%s", code, out)
	}
	// Self link.
	out, code = captureRun([]string{"den", "link", "consumer", "--link", "consumer", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("self link: code=%d out=%s", code, out)
	}

	// Freshness: live target reachable now…
	out, code = captureRun([]string{"den", "status", "consumer", "--json"})
	if code != 0 {
		t.Fatalf("status: code=%d out=%s", code, out)
	}
	var st denP4StatusEnvelope
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status envelope: %v", err)
	}
	if len(st.Links) != 1 || st.Links[0].State != "ok" {
		t.Fatalf("live link state: %+v", st.Links)
	}
	// …and unreachable once the target den is destroyed.
	if out, code = captureRun([]string{"den", "destroy", "durable-target", "--json"}); code != 0 {
		t.Fatalf("destroy target: code=%d out=%s", code, out)
	}
	out, code = captureRun([]string{"den", "status", "consumer", "--json"})
	if code != 0 {
		t.Fatalf("status after destroy: code=%d out=%s", code, out)
	}
	st = denP4StatusEnvelope{}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status envelope: %v", err)
	}
	if len(st.Links) != 1 || st.Links[0].State != "unreachable" {
		t.Fatalf("dead live link must be unreachable: %+v", st.Links)
	}
}

func TestDenUnlinkEditRemovesLinkAndMount(t *testing.T) {
	hermeticDenCLI(t)
	vaultDir, _ := denLinkFixture(t, "unlink-den", "product-platform", "project-a")
	if out, code := captureRun([]string{"den", "link", "unlink-den", "--edit", "product-platform/project-a", "--json"}); code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}

	// Dry-run touches nothing.
	out, code := captureRun([]string{"den", "unlink", "unlink-den", "product-platform/project-a", "--dry-run", "--json"})
	if code != 0 || !strings.Contains(out, "remove link") {
		t.Fatalf("dry-run: code=%d out=%s", code, out)
	}
	if st, _ := den.Status("unlink-den"); len(st.Links) != 1 {
		t.Fatalf("dry-run must not remove the link: %+v", st.Links)
	}

	out, code = captureRun([]string{"den", "unlink", "unlink-den", "product-platform/project-a", "--json"})
	if code != 0 {
		t.Fatalf("unlink: code=%d out=%s", code, out)
	}
	var env struct {
		Schema  int    `json:"schema"`
		DenID   string `json:"den_id"`
		Removed []struct {
			Target string `json:"target"`
			Mode   string `json:"mode"`
		} `json:"removed"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v out=%s", err, out)
	}
	if len(env.Removed) != 1 || env.Removed[0].Mode != "edit" || env.Removed[0].Target != "product-platform/project-a" {
		t.Fatalf("removed: %+v", env.Removed)
	}
	if env.Warnings == nil {
		t.Fatal("warnings must be present (empty array, not null)")
	}

	// Link gone from the manifest AND the mount gone from warren state.
	st, err := den.Status("unlink-den")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Links) != 0 {
		t.Fatalf("link must be removed: %+v", st.Links)
	}
	mounts, err := warrenpkg.ActiveMounts(vaultDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	for _, m := range mounts {
		if m.WarrenID == "product-platform" && m.ProjectID == "project-a" && m.Active {
			t.Fatalf("mount must be removed: %+v", m)
		}
	}

	// Unlinking again: link_not_found.
	out, code = captureRun([]string{"den", "unlink", "unlink-den", "product-platform/project-a", "--json"})
	if code == 0 || !strings.Contains(out, "link_not_found") {
		t.Fatalf("second unlink: code=%d out=%s", code, out)
	}
}

func TestDenUnlinkRefusesUnpushedEdits(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hermeticDenCLI(t)
	vaultDir, _, worktree := cacheEditFixture(t, "unlink-wt-den", "wt-warren", "project-a")
	_ = worktree

	// Stage an unpushed contribute commit on the edit branch.
	nsDir := filepath.Join(vaultDir, "default")
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	node := "---\nid: default/finding\ntype: insight\nsummary: finding\nstatus: active\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(nsDir, "finding.md"), []byte(node), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := captureRun([]string{"den", "contribute", "unlink-wt-den", "--json"}); code != 0 {
		t.Fatalf("contribute: code=%d out=%s", code, out)
	}

	out, code := captureRun([]string{"den", "unlink", "unlink-wt-den", "wt-warren/project-a", "--json"})
	if code == 0 || !strings.Contains(out, "unpushed_edits") {
		t.Fatalf("unlink must refuse unpushed edits: code=%d out=%s", code, out)
	}
	// Still linked.
	if st, _ := den.Status("unlink-wt-den"); len(st.Links) != 1 {
		t.Fatalf("refusal must keep the link: %+v", st.Links)
	}

	// --force acknowledges; the worktree and branch survive (destroy owns
	// worktree cleanup — only the mount and manifest link go away).
	out, code = captureRun([]string{"den", "unlink", "unlink-wt-den", "wt-warren/project-a", "--force", "--json"})
	if code != 0 {
		t.Fatalf("forced unlink: code=%d out=%s", code, out)
	}
	if st, _ := den.Status("unlink-wt-den"); len(st.Links) != 0 {
		t.Fatalf("forced unlink must remove the link: %+v", st.Links)
	}
	if _, err := os.Stat(warrenpkg.CacheEditWorktreePath("wt-warren", "unlink-wt-den")); err != nil {
		t.Fatalf("edit worktree must survive unlink: %v", err)
	}
}

func TestDenCreateWithRefs(t *testing.T) {
	hermeticDenCLI(t)
	seedCLICacheWarren(t, "platform", []warrenpkg.Project{
		{ProjectID: "docs", Path: "projects/docs/.marmot", SourceURL: "github.com/acme/docs", SourceCommit: "abc1234"},
	})
	const pin = "0011223344556677889900aabbccddeeff001122"
	if err := warrenpkg.WriteCachePin("platform", pin); err != nil {
		t.Fatal(err)
	}
	// A reference checkout carrying its own vault.
	checkoutRoot := t.TempDir()
	writeCLIImportSourceVault(t, filepath.Join(checkoutRoot, ".marmot"), "ref-vault")

	project := t.TempDir()
	out, code := captureRun([]string{
		"den", "create", "ref-den", "--lifetime", "task", "--project", project, "--no-pointer",
		"--ref", "name=platform-docs,url=https://github.com/acme/docs.git",
		"--ref", "name=local-checkout,path=" + checkoutRoot,
		"--ref", "name=unknown-repo,url=https://nowhere.example/x",
		"--json",
	})
	if code != 0 {
		t.Fatalf("den create --ref: code=%d out=%s", code, out)
	}
	var env struct {
		Schema int `json:"schema"`
		Links  []struct {
			Ref         string  `json:"ref"`
			Mode        *string `json:"mode"`
			ResolvedVia string  `json:"resolved_via"`
		} `json:"links"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v out=%s", err, out)
	}
	if len(env.Links) != 3 {
		t.Fatalf("links: %+v", env.Links)
	}
	byVia := map[string]int{}
	for i, l := range env.Links {
		byVia[l.ResolvedVia] = i
	}
	wu := env.Links[byVia["warren-url"]]
	if wu.Ref != "platform/docs" || wu.Mode == nil || *wu.Mode != "link" {
		t.Fatalf("warren-url link: %+v", wu)
	}
	cv := env.Links[byVia["checkout-vault"]]
	if cv.Ref != "local-checkout" || cv.Mode == nil || *cv.Mode != "live" {
		t.Fatalf("checkout-vault link: %+v", cv)
	}
	no := env.Links[byVia["none"]]
	if no.Ref != "unknown-repo" || no.Mode != nil {
		t.Fatalf("none link: %+v", no)
	}
	foundSkip := false
	for _, w := range env.Warnings {
		if strings.Contains(w, "unknown-repo") && strings.Contains(w, "skipped") {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("skip warning missing: %+v", env.Warnings)
	}

	// Manifest carries the two resolved links; the pinned one is pinned.
	st, err := den.Status("ref-den")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Links) != 2 {
		t.Fatalf("manifest links: %+v", st.Links)
	}
	for _, l := range st.Links {
		switch l.Mode {
		case den.LinkModeLink:
			if l.Target != "platform/docs" || l.PinnedRef != pin {
				t.Fatalf("pinned link: %+v", l)
			}
		case den.LinkModeLive:
			if l.Target != "ref-vault" {
				t.Fatalf("live link: %+v", l)
			}
		default:
			t.Fatalf("unexpected link mode: %+v", l)
		}
	}

	// The checkout vault id is route-registered so federation can resolve it.
	rt, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir, ok := rt.Get("ref-vault")
	if !ok || dir != filepath.Join(checkoutRoot, ".marmot") {
		t.Fatalf("route for ref-vault: %q ok=%v", dir, ok)
	}

	// Malformed spec → invalid_args before any persistence.
	out, code = captureRun([]string{"den", "create", "bad-ref-den", "--project", t.TempDir(), "--ref", "name=only", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("bad spec: code=%d out=%s", code, out)
	}
	out, code = captureRun([]string{"den", "create", "bad-ref-den2", "--project", t.TempDir(), "--ref", "bogus-key=1,url=x", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("bad key: code=%d out=%s", code, out)
	}
}

func TestDenStatusPinnedSourceCommitSkew(t *testing.T) {
	hermeticDenCLI(t)
	seedCLICacheWarren(t, "platform", []warrenpkg.Project{
		{ProjectID: "docs", Path: "projects/docs/.marmot", SourceURL: "github.com/acme/docs", SourceCommit: "77aa88bb99cc"},
	})
	if _, err := den.Create("skew-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	if _, err := den.AddLink("skew-den", den.Link{
		Target: "platform/docs", Mode: den.LinkModeLink,
		Warren: "platform", Project: "docs", PinnedRef: "abc123def456",
	}); err != nil {
		t.Fatal(err)
	}

	out, code := captureRun([]string{"den", "status", "skew-den", "--json"})
	if code != 0 {
		t.Fatalf("status: code=%d out=%s", code, out)
	}
	var st denP4StatusEnvelope
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("envelope: %v out=%s", err, out)
	}
	if len(st.Links) != 1 {
		t.Fatalf("links: %+v", st.Links)
	}
	l := st.Links[0]
	if l.PinnedCommit == nil || *l.PinnedCommit != "abc123def456" {
		t.Fatalf("pinned_commit: %+v", l)
	}
	if l.SourceCommit != "77aa88bb99cc" {
		t.Fatalf("source_commit skew: %+v", l)
	}
	// No bare cache repo here: behind stays 0, state ok.
	if l.Behind != 0 || l.State != "ok" {
		t.Fatalf("freshness without bare cache: %+v", l)
	}

	// Human output renders the skew line.
	human, code := captureRun([]string{"den", "status", "skew-den"})
	if code != 0 || !strings.Contains(human, "vault snapshot from source commit 77aa88bb99cc") {
		t.Fatalf("human skew line missing: %s", human)
	}
}

func TestDenStatusPinnedStaleAfterSync(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hermeticDenCLI(t)
	remote := cacheRemoteWarren(t, "stale-warren", "project-a")
	if out, code := captureRun([]string{"warren", "add", remote, "--id", "stale-warren", "--json"}); code != 0 {
		t.Fatalf("warren add: code=%d out=%s", code, out)
	}
	if _, err := den.Create("stale-den", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	// Link pins to the current cache pin…
	if out, code := captureRun([]string{"den", "link", "stale-den", "--link", "stale-warren/project-a", "--json"}); code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}
	// …then the warren moves on and the cache syncs.
	commitRemoteChange(t, remote, "update.md")
	if out, code := captureRun([]string{"warren", "sync", "stale-warren", "--json"}); code != 0 {
		t.Fatalf("warren sync: code=%d out=%s", code, out)
	}

	out, code := captureRun([]string{"den", "status", "stale-den", "--json"})
	if code != 0 {
		t.Fatalf("status: code=%d out=%s", code, out)
	}
	var st denP4StatusEnvelope
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("envelope: %v out=%s", err, out)
	}
	if len(st.Links) != 1 {
		t.Fatalf("links: %+v", st.Links)
	}
	if st.Links[0].Behind < 1 || st.Links[0].State != "stale" {
		t.Fatalf("pinned link must be stale behind>=1: %+v", st.Links[0])
	}
}

func TestDenStatusEditUnpushed(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hermeticDenCLI(t)
	vaultDir, _, _ := cacheEditFixture(t, "fresh-den", "fresh-warren", "project-a")

	// Baseline: no pending edits, state ok.
	out, code := captureRun([]string{"den", "status", "fresh-den", "--json"})
	if code != 0 {
		t.Fatalf("status: code=%d out=%s", code, out)
	}
	var st denP4StatusEnvelope
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if len(st.Links) != 1 || st.Links[0].State != "ok" || st.Links[0].PendingEdits != 0 {
		t.Fatalf("baseline: %+v", st.Links)
	}

	// A contribute commit on the edit branch makes the link unpushed.
	nsDir := filepath.Join(vaultDir, "default")
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	node := "---\nid: default/finding\ntype: insight\nsummary: finding\nstatus: active\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(nsDir, "finding.md"), []byte(node), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := captureRun([]string{"den", "contribute", "fresh-den", "--json"}); code != 0 {
		t.Fatalf("contribute: code=%d out=%s", code, out)
	}

	out, code = captureRun([]string{"den", "status", "fresh-den", "--json"})
	if code != 0 {
		t.Fatalf("status: code=%d out=%s", code, out)
	}
	st = denP4StatusEnvelope{}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	l := st.Links[0]
	if l.Ahead < 1 || l.PendingEdits < l.Ahead || l.State != "unpushed" {
		t.Fatalf("unpushed freshness: %+v", l)
	}
}

func TestDenDestroyPromote(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("keeper", den.CreateOptions{Lifetime: den.LifetimeDurable}); err != nil {
		t.Fatal(err)
	}
	if _, err := den.Create("scratch", den.CreateOptions{Lifetime: den.LifetimeTask}); err != nil {
		t.Fatal(err)
	}
	nsDir := filepath.Join(den.VaultPath("scratch"), "default")
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	node := "---\nid: default/keeper-fact\ntype: insight\nsummary: promoted fact\nstatus: active\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(nsDir, "keeper-fact.md"), []byte(node), 0o644); err != nil {
		t.Fatal(err)
	}

	// Missing target: structured refusal, nothing destroyed.
	out, code := captureRun([]string{"den", "destroy", "scratch", "--promote", "no-such-den", "--json"})
	if code == 0 || !strings.Contains(out, "promote_target_not_found") {
		t.Fatalf("missing target: code=%d out=%s", code, out)
	}
	if _, err := den.Status("scratch"); err != nil {
		t.Fatalf("refusal must not destroy: %v", err)
	}
	// Self target refused.
	out, code = captureRun([]string{"den", "destroy", "scratch", "--promote", "scratch", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("self target: code=%d out=%s", code, out)
	}

	// Dry-run composes promote + destroy ops and touches nothing.
	out, code = captureRun([]string{"den", "destroy", "scratch", "--promote", "keeper", "--dry-run", "--json"})
	if code != 0 || !strings.Contains(out, "promote:") || !strings.Contains(out, "rm -rf") {
		t.Fatalf("dry-run: code=%d out=%s", code, out)
	}
	if _, err := den.Status("scratch"); err != nil {
		t.Fatalf("dry-run must not destroy: %v", err)
	}

	// Real promote-on-destroy: counts in the envelope, node in the target.
	out, code = captureRun([]string{"den", "destroy", "scratch", "--promote", "keeper", "--json"})
	if code != 0 {
		t.Fatalf("destroy --promote: code=%d out=%s", code, out)
	}
	var env struct {
		Schema    int  `json:"schema"`
		Destroyed bool `json:"destroyed"`
		Promoted  *struct {
			Added      int `json:"added"`
			Updated    int `json:"updated"`
			Superseded int `json:"superseded"`
			Noop       int `json:"noop"`
		} `json:"promoted"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v out=%s", err, out)
	}
	if !env.Destroyed || env.Promoted == nil || env.Promoted.Added != 1 {
		t.Fatalf("promote counts: %s", out)
	}
	if _, err := den.Status("scratch"); err == nil {
		t.Fatal("scratch must be destroyed")
	}
	promoted := filepath.Join(den.VaultPath("keeper"), "default", "keeper-fact.md")
	if _, err := os.Stat(promoted); err != nil {
		t.Fatalf("promoted node missing in target vault: %v", err)
	}
}
