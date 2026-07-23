package warren

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/node"
)

func editHermeticHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home.SetOverride(root)
	t.Cleanup(func() { home.SetOverride("") })
	return root
}

func editGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCacheEditPathHelpers(t *testing.T) {
	editHermeticHome(t)
	wt := CacheEditWorktreePath("product-platform", "demo")
	want := filepath.Join(CacheEditsDir(), "product-platform", "demo")
	if wt != want {
		t.Fatalf("CacheEditWorktreePath = %q, want %q", wt, want)
	}
	if got := CacheEditBranch("demo", "product-platform"); got != "marmot/edit/demo/product-platform" {
		t.Fatalf("CacheEditBranch = %q", got)
	}

	// Paths inside a worktree (any depth) classify and split correctly.
	inner := filepath.Join(wt, "projects", "project-a", ".marmot")
	for _, p := range []string{wt, inner} {
		root, warrenID, denID, ok := SplitCacheEditPath(p)
		if !ok || root != wt || warrenID != "product-platform" || denID != "demo" {
			t.Fatalf("SplitCacheEditPath(%q) = (%q,%q,%q,%v)", p, root, warrenID, denID, ok)
		}
		if !IsCacheEditPath(p) {
			t.Fatalf("IsCacheEditPath(%q) = false", p)
		}
	}

	// Non-worktree paths refuse: shared checkout, edits root itself, one
	// segment only, outside the cache, empty.
	for _, p := range []string{
		CacheCheckoutPath("product-platform"),
		CacheEditsDir(),
		filepath.Join(CacheEditsDir(), "product-platform"),
		t.TempDir(),
		"",
	} {
		if IsCacheEditPath(p) {
			t.Fatalf("IsCacheEditPath(%q) = true, want false", p)
		}
	}
}

// editWorktreeFixture builds a git repo shaped like a cache edit worktree
// (the auto-commit path only needs a repo at the derived worktree root, not
// real `git worktree` bookkeeping) with one editable project mount.
func editWorktreeFixture(t *testing.T) (worktree string, mount ProjectStatus) {
	t.Helper()
	editHermeticHome(t)
	worktree = CacheEditWorktreePath("cachew", "wt-den")
	projectVault := filepath.Join(worktree, "projects", "project-a", ".marmot")
	if err := os.MkdirAll(projectVault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectVault, "_config.md"), []byte("---\nversion: \"1\"\nvault_id: vp\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	editGit(t, worktree, "init", "-q")
	editGit(t, worktree, "checkout", "-q", "-b", "marmot/edit/wt-den/cachew")
	editGit(t, worktree, "config", "user.email", "test@example.com")
	editGit(t, worktree, "config", "user.name", "Test")
	editGit(t, worktree, "add", "-A")
	editGit(t, worktree, "commit", "-q", "-m", "init")
	mount = ProjectStatus{
		WarrenID:  "cachew",
		ProjectID: "project-a",
		VaultID:   "vp",
		Path:      projectVault,
		Editable:  true,
	}
	return worktree, mount
}

// TestWriteEditableNodeAutoCommitsInEditWorktree: an MCP-style write into a
// cache edit worktree commits the node file (pathspec-limited, OQ7) with the
// documented message; an identical re-write commits nothing; the worktree is
// left clean.
func TestWriteEditableNodeAutoCommitsInEditWorktree(t *testing.T) {
	worktree, mount := editWorktreeFixture(t)
	before := editGit(t, worktree, "rev-list", "--count", "HEAD")

	n := &node.Node{ID: "notes/auto", Type: "concept", Namespace: "default", Status: node.StatusActive, Summary: "auto"}
	warning, err := WriteEditableNode(mount, n, nil, "", "")
	if err != nil {
		t.Fatalf("WriteEditableNode: %v", err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %q", warning)
	}
	if subject := editGit(t, worktree, "log", "-1", "--format=%s"); subject != "marmot edit: vp/notes/auto (write)" {
		t.Fatalf("commit subject = %q", subject)
	}
	after := editGit(t, worktree, "rev-list", "--count", "HEAD")
	if before == after {
		t.Fatal("auto-commit did not add a commit")
	}
	if dirty := editGit(t, worktree, "status", "--porcelain"); dirty != "" {
		t.Fatalf("worktree dirty after auto-commit: %q", dirty)
	}

	// Idempotent re-write: nothing changed on disk, nothing committed.
	if _, err := WriteEditableNode(mount, n, nil, "", ""); err != nil {
		t.Fatalf("re-write: %v", err)
	}
	if again := editGit(t, worktree, "rev-list", "--count", "HEAD"); again != after {
		t.Fatalf("idempotent re-write committed: %s -> %s", after, again)
	}
}

// TestWriteEditableNodeLegacyMountNoAutoCommit pins the legacy behavior:
// writes into a registered-checkout mount are NEVER auto-committed —
// `warren propose` / `den contribute` package them.
func TestWriteEditableNodeLegacyMountNoAutoCommit(t *testing.T) {
	editHermeticHome(t)
	checkout := t.TempDir()
	projectVault := filepath.Join(checkout, "projects", "project-a", ".marmot")
	if err := os.MkdirAll(projectVault, 0o755); err != nil {
		t.Fatal(err)
	}
	editGit(t, checkout, "init", "-q")
	editGit(t, checkout, "config", "user.email", "test@example.com")
	editGit(t, checkout, "config", "user.name", "Test")
	editGit(t, checkout, "add", "-A")
	editGit(t, checkout, "commit", "-q", "-m", "init", "--allow-empty")

	mount := ProjectStatus{ProjectID: "project-a", VaultID: "vp", Path: projectVault, Editable: true}
	n := &node.Node{ID: "notes/legacy", Type: "concept", Namespace: "default", Status: node.StatusActive, Summary: "legacy"}
	if warning, err := WriteEditableNode(mount, n, nil, "", ""); err != nil || warning != "" {
		t.Fatalf("WriteEditableNode: warning=%q err=%v", warning, err)
	}
	if count := editGit(t, checkout, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("legacy write must not commit (count=%s)", count)
	}
	if dirty := editGit(t, checkout, "status", "--porcelain", "-uall"); !strings.Contains(dirty, "notes/legacy.md") {
		t.Fatalf("expected uncommitted node file, status=%q", dirty)
	}
}
