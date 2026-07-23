package den

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nurozen/context-marmot/internal/warren"
)

func TestValidateLink(t *testing.T) {
	valid := Link{Target: "w/p", Mode: LinkModeEdit, Warren: "w", Project: "p"}
	if err := ValidateLink(valid); err != nil {
		t.Fatalf("valid edit link: %v", err)
	}
	cases := []struct {
		name string
		link Link
		want string
	}{
		{"bad mode", Link{Target: "w/p", Mode: "clone", Warren: "w", Project: "p"}, "mode"},
		{"empty mode", Link{Target: "w/p", Warren: "w", Project: "p"}, "mode"},
		{"empty target", Link{Mode: LinkModeEdit, Warren: "w", Project: "p"}, "target"},
		{"edit without warren", Link{Target: "w/p", Mode: LinkModeEdit, Project: "p"}, "warren"},
		{"edit without project", Link{Target: "w/p", Mode: LinkModeEdit, Warren: "w"}, "warren"},
		{"half warren ref", Link{Target: "w/p", Mode: LinkModeLink, Warren: "w"}, "warren"},
	}
	for _, tc := range cases {
		err := ValidateLink(tc.link)
		if err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
	// Non-warren link modes need no warren/project.
	if err := ValidateLink(Link{Target: "https://example.com/warren.git", Mode: LinkModeLink}); err != nil {
		t.Fatalf("plain link mode: %v", err)
	}
}

func TestAddLinkAppendsAndDedupes(t *testing.T) {
	hermetic(t)
	if _, err := Create("link-den", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	l := Link{Target: "w/p", Mode: LinkModeEdit, Warren: "w", Project: "p"}
	added, err := AddLink("link-den", l)
	if err != nil || !added {
		t.Fatalf("first AddLink: added=%v err=%v", added, err)
	}
	// Same (warren, project, mode) dedupes.
	added, err = AddLink("link-den", l)
	if err != nil || added {
		t.Fatalf("duplicate AddLink: added=%v err=%v", added, err)
	}
	// Different mode is a distinct link.
	added, err = AddLink("link-den", Link{Target: "w/p", Mode: LinkModeLive, Warren: "w", Project: "p"})
	if err != nil || !added {
		t.Fatalf("different-mode AddLink: added=%v err=%v", added, err)
	}
	m, _, err := LoadManifest("link-den")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Links) != 2 {
		t.Fatalf("links = %+v, want 2", m.Links)
	}
	if m.Links[0].Target != "w/p" || m.Links[0].Mode != LinkModeEdit {
		t.Fatalf("first link = %+v", m.Links[0])
	}
	// Non-warren links dedupe on target.
	url := Link{Target: "https://example.com/warren.git", Mode: LinkModeLink}
	if added, err := AddLink("link-den", url); err != nil || !added {
		t.Fatalf("url link: added=%v err=%v", added, err)
	}
	if added, err := AddLink("link-den", url); err != nil || added {
		t.Fatalf("dup url link: added=%v err=%v", added, err)
	}
	if added, err := AddLink("link-den", Link{Target: "https://example.com/other.git", Mode: LinkModeLink}); err != nil || !added {
		t.Fatalf("other url link: added=%v err=%v", added, err)
	}
}

func TestAddLinkErrors(t *testing.T) {
	hermetic(t)
	// Invalid link never touches disk.
	if _, err := AddLink("nope", Link{Target: "w/p", Mode: "bogus"}); err == nil {
		t.Fatal("expected mode validation error")
	}
	// Missing den.
	if _, err := AddLink("nope", Link{Target: "w/p", Mode: LinkModeEdit, Warren: "w", Project: "p"}); err == nil {
		t.Fatal("expected missing-den error")
	}
}

func TestEnsureEditableMount(t *testing.T) {
	hermetic(t)
	if _, err := Create("mount-den", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	vaultDir := VaultPath("mount-den")

	// Not registered yet.
	if _, err := EnsureEditableMount(vaultDir, "w", "p"); err == nil {
		t.Fatal("expected not-registered error")
	}

	// Register the warren directly in the vault's workspace state.
	state, body, err := warren.LoadWorkspaceStateFromMarmot(vaultDir)
	if err != nil {
		t.Fatalf("LoadWorkspaceStateFromMarmot: %v", err)
	}
	if state.Warrens == nil {
		state.Warrens = map[string]warren.WorkspaceWarren{}
	}
	state.Warrens["w"] = warren.WorkspaceWarren{Path: t.TempDir()}
	if err := warren.SaveWorkspaceStateToMarmot(vaultDir, state, body); err != nil {
		t.Fatalf("SaveWorkspaceStateToMarmot: %v", err)
	}

	changed, err := EnsureEditableMount(vaultDir, "w", "p")
	if err != nil || !changed {
		t.Fatalf("first EnsureEditableMount: changed=%v err=%v", changed, err)
	}
	state, _, err = warren.LoadWorkspaceStateFromMarmot(vaultDir)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	entry := state.Warrens["w"]
	if len(entry.ActiveProjects) != 1 || entry.ActiveProjects[0] != "p" {
		t.Fatalf("active = %+v", entry.ActiveProjects)
	}
	if len(entry.EditableProjects) != 1 || entry.EditableProjects[0] != "p" {
		t.Fatalf("editable = %+v", entry.EditableProjects)
	}

	// Idempotent.
	changed, err = EnsureEditableMount(vaultDir, "w", "p")
	if err != nil || changed {
		t.Fatalf("second EnsureEditableMount: changed=%v err=%v", changed, err)
	}

	// Invalid IDs refuse before touching state.
	if _, err := EnsureEditableMount(vaultDir, "../w", "p"); err == nil {
		t.Fatal("expected warren id validation error")
	}
	if _, err := EnsureEditableMount(vaultDir, "w", "a/b"); err == nil {
		t.Fatal("expected project id validation error")
	}

	// State file is written where warren.ActiveMounts reads it.
	if _, err := os.Stat(filepath.Join(vaultDir, warren.ManifestFileName)); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
}

// TestEnsureEditableMountAt: the cache-backed variant creates the workspace
// state entry when absent and (re)points its Path at the given worktree, so
// every mount of that warren in this den routes through the edit worktree.
func TestEnsureEditableMountAt(t *testing.T) {
	hermetic(t)
	if _, err := Create("wt-den", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	vaultDir := VaultPath("wt-den")
	worktree := t.TempDir()

	// Creates the entry with Path when the warren was never registered.
	changed, err := EnsureEditableMountAt(vaultDir, "w", "p", worktree)
	if err != nil || !changed {
		t.Fatalf("first EnsureEditableMountAt: changed=%v err=%v", changed, err)
	}
	state, _, err := warren.LoadWorkspaceStateFromMarmot(vaultDir)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	entry := state.Warrens["w"]
	if entry.Path != worktree {
		t.Fatalf("entry.Path = %q, want worktree %q", entry.Path, worktree)
	}
	if len(entry.ActiveProjects) != 1 || entry.ActiveProjects[0] != "p" ||
		len(entry.EditableProjects) != 1 || entry.EditableProjects[0] != "p" {
		t.Fatalf("entry = %+v", entry)
	}

	// Idempotent with the same path.
	if changed, err := EnsureEditableMountAt(vaultDir, "w", "p", worktree); err != nil || changed {
		t.Fatalf("idempotent EnsureEditableMountAt: changed=%v err=%v", changed, err)
	}

	// A second project of the same warren shares the entry (and worktree).
	if changed, err := EnsureEditableMountAt(vaultDir, "w", "p2", worktree); err != nil || !changed {
		t.Fatalf("second project: changed=%v err=%v", changed, err)
	}

	// Repointing the path is a change even when the project is already mounted.
	other := t.TempDir()
	if changed, err := EnsureEditableMountAt(vaultDir, "w", "p", other); err != nil || !changed {
		t.Fatalf("repoint: changed=%v err=%v", changed, err)
	}
	state, _, err = warren.LoadWorkspaceStateFromMarmot(vaultDir)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if got := state.Warrens["w"].Path; got != other {
		t.Fatalf("repointed Path = %q, want %q", got, other)
	}

	// Empty path keeps the legacy require-registered behavior.
	if _, err := EnsureEditableMountAt(vaultDir, "unregistered", "p", ""); err == nil {
		t.Fatal("expected not-registered error with empty path")
	}
}

// TestAddLinkConcurrentNoLostUpdate (F8): two goroutines adding DIFFERENT
// links race the manifest read-modify-write; with the whole RMW under the
// manifest flock (UpdateManifest) both links must survive. Repeated a few
// times to give the old Load→mutate→Save race a chance to show if it ever
// regresses.
func TestAddLinkConcurrentNoLostUpdate(t *testing.T) {
	hermetic(t)
	for iter := 0; iter < 5; iter++ {
		denID := fmt.Sprintf("race-den-%d", iter)
		if _, err := Create(denID, CreateOptions{Lifetime: LifetimeTask}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		links := []Link{
			{Target: "w1/p1", Mode: LinkModeEdit, Warren: "w1", Project: "p1"},
			{Target: "w2/p2", Mode: LinkModeLink, Warren: "w2", Project: "p2"},
		}
		var wg sync.WaitGroup
		errs := make([]error, len(links))
		for i, l := range links {
			wg.Add(1)
			go func(i int, l Link) {
				defer wg.Done()
				added, err := AddLink(denID, l)
				if err == nil && !added {
					err = fmt.Errorf("link %s reported added=false", l.Target)
				}
				errs[i] = err
			}(i, l)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("iter %d: AddLink[%d]: %v", iter, i, err)
			}
		}
		m, _, err := LoadManifest(denID)
		if err != nil {
			t.Fatalf("LoadManifest: %v", err)
		}
		if len(m.Links) != 2 {
			t.Fatalf("iter %d: lost update — manifest links = %+v, want both", iter, m.Links)
		}
	}
}

// TestUpdateManifestSkipsWriteAndPropagatesErrors pins UpdateManifest's
// contract: write=false leaves the file untouched; an fn error aborts.
func TestUpdateManifestSkipsWriteAndPropagatesErrors(t *testing.T) {
	hermetic(t)
	if _, err := Create("um-den", CreateOptions{Lifetime: LifetimeTask}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, err := os.ReadFile(ManifestPath("um-den"))
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateManifest("um-den", func(m *Manifest) (bool, error) {
		m.Links = append(m.Links, Link{Target: "x", Mode: LinkModeLive})
		return false, nil // decline the write
	}); err != nil {
		t.Fatalf("UpdateManifest(write=false): %v", err)
	}
	after, err := os.ReadFile(ManifestPath("um-den"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("write=false must leave the manifest untouched")
	}
	wantErr := fmt.Errorf("boom")
	if err := UpdateManifest("um-den", func(m *Manifest) (bool, error) {
		return true, wantErr
	}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("UpdateManifest error propagation = %v", err)
	}
}
