package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type call struct {
	bin  string
	args []string
	dir  string
}

type fakeRunner struct {
	results []Result
	errs    []error
	calls   []call
}

func (f *fakeRunner) Run(ctx context.Context, bin string, args []string, opts RunOptions) (Result, error) {
	f.calls = append(f.calls, call{bin: bin, args: append([]string(nil), args...), dir: opts.Dir})
	var result Result
	if len(f.results) > 0 {
		result = f.results[0]
		f.results = f.results[1:]
	}
	var err error
	if len(f.errs) > 0 {
		err = f.errs[0]
		f.errs = f.errs[1:]
	}
	return result, err
}

func TestCommandArgs(t *testing.T) {
	runner := &fakeRunner{}
	client := New(WithRunner(runner))
	ctx := context.Background()

	_ = client.CloneBare(ctx, "https://example.test/repo.git", "/tmp/repo.git")
	_ = client.FetchAllPrune(ctx, "/tmp/repo.git")
	_ = client.ConfigureBareRemoteTracking(ctx, "/tmp/repo.git")
	_ = client.WorktreeAddBranch(ctx, "/tmp/repo.git", "/tmp/wt", "marmot/x", "origin/main")
	_ = client.WorktreeAddExisting(ctx, "/tmp/repo.git", "/tmp/wt-existing", "marmot/existing")
	_ = client.WorktreeAddDetached(ctx, "/tmp/repo.git", "/tmp/ref", "origin/main")
	_ = client.WorktreeRemove(ctx, "/tmp/repo.git", "/tmp/wt", true)
	_ = client.WorktreeRemove(ctx, "/tmp/repo.git", "/tmp/wt", false)
	_ = client.WorktreePrune(ctx, "/tmp/repo.git")
	_ = client.CheckoutDetached(ctx, "/tmp/wt", "origin/main")
	_ = client.Add(ctx, "/tmp/wt")
	_ = client.Add(ctx, "/tmp/wt", "notes/a.md", "notes/b.md")
	_ = client.Commit(ctx, "/tmp/wt", "msg")
	_ = client.Commit(ctx, "/tmp/wt", "msg", "notes/a.md")
	_, _ = client.StatusPorcelain(ctx, "/tmp/wt")
	_, _, _ = client.IsDirty(ctx, "/tmp/wt", "notes")
	_, _ = client.HeadCommit(ctx, "/tmp/wt")

	wants := [][]string{
		{"clone", "--bare", "https://example.test/repo.git", "/tmp/repo.git"},
		{"--git-dir", "/tmp/repo.git", "fetch", "--all", "--prune"},
		{"--git-dir", "/tmp/repo.git", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"--git-dir", "/tmp/repo.git", "worktree", "add", "--no-track", "-b", "marmot/x", "/tmp/wt", "origin/main"},
		{"--git-dir", "/tmp/repo.git", "worktree", "add", "/tmp/wt-existing", "marmot/existing"},
		{"--git-dir", "/tmp/repo.git", "worktree", "add", "--detach", "/tmp/ref", "origin/main"},
		{"--git-dir", "/tmp/repo.git", "worktree", "remove", "--force", "/tmp/wt"},
		{"--git-dir", "/tmp/repo.git", "worktree", "remove", "/tmp/wt"},
		{"--git-dir", "/tmp/repo.git", "worktree", "prune"},
		{"checkout", "--detach", "origin/main"},
		{"add", "-A"},
		{"add", "--", "notes/a.md", "notes/b.md"},
		{"commit", "-m", "msg"},
		{"commit", "-m", "msg", "--", "notes/a.md"},
		{"status", "--porcelain=v1"},
		{"status", "--porcelain=v1", "--", "notes"},
		{"rev-parse", "HEAD"},
	}
	if len(runner.calls) != len(wants) {
		t.Fatalf("got %d calls, want %d", len(runner.calls), len(wants))
	}
	for i, want := range wants {
		if !reflect.DeepEqual(runner.calls[i].args, want) {
			t.Fatalf("call %d = %#v, want %#v", i, runner.calls[i].args, want)
		}
	}
	if runner.calls[9].dir != "/tmp/wt" {
		t.Fatalf("CheckoutDetached dir = %q", runner.calls[9].dir)
	}
	if runner.calls[10].dir != "/tmp/wt" {
		t.Fatalf("Add dir = %q", runner.calls[10].dir)
	}
}

func TestBranchExistsHandlesMissingRef(t *testing.T) {
	runner := &fakeRunner{errs: []error{&GitError{ExitCode: 1}}}
	client := New(WithRunner(runner))
	exists, err := client.BranchExists(context.Background(), "/tmp/repo.git", "missing")
	if err != nil {
		t.Fatalf("BranchExists() error = %v", err)
	}
	if exists {
		t.Fatal("missing branch reported as existing")
	}
}

func TestBranchExistsReturnsUnexpectedErrors(t *testing.T) {
	wantErr := errors.New("boom")
	runner := &fakeRunner{errs: []error{wantErr}}
	client := New(WithRunner(runner))
	_, err := client.BranchExists(context.Background(), "/tmp/repo.git", "main")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestBranchExistsTrimsFullRef(t *testing.T) {
	runner := &fakeRunner{}
	client := New(WithRunner(runner))
	exists, err := client.BranchExists(context.Background(), "/tmp/repo.git", "refs/heads/main")
	if err != nil || !exists {
		t.Fatalf("BranchExists() = %v, %v", exists, err)
	}
	want := []string{"--git-dir", "/tmp/repo.git", "show-ref", "--verify", "--quiet", "refs/heads/main"}
	if !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("args = %#v, want %#v", runner.calls[0].args, want)
	}
}

func TestRemoteDefaultBranchParsesSymbolicRefAndRemoteShowFallback(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: "origin/trunk\n"}}}
	client := New(WithRunner(runner))
	branch, err := client.RemoteDefaultBranch(context.Background(), "/tmp/repo.git")
	if err != nil {
		t.Fatalf("RemoteDefaultBranch(symbolic-ref) error = %v", err)
	}
	if branch != "trunk" {
		t.Fatalf("symbolic-ref branch = %q", branch)
	}

	runner = &fakeRunner{
		errs:    []error{&GitError{ExitCode: 1}, nil},
		results: []Result{{}, {Stdout: "* remote origin\n  HEAD branch: main\n"}},
	}
	client = New(WithRunner(runner))
	branch, err = client.RemoteDefaultBranch(context.Background(), "/tmp/repo.git")
	if err != nil {
		t.Fatalf("RemoteDefaultBranch(fallback) error = %v", err)
	}
	if branch != "main" {
		t.Fatalf("fallback branch = %q", branch)
	}

	runner = &fakeRunner{
		errs:    []error{&GitError{ExitCode: 1}, nil},
		results: []Result{{}, {Stdout: "no head here\n"}},
	}
	client = New(WithRunner(runner))
	if _, err := client.RemoteDefaultBranch(context.Background(), "/tmp/repo.git"); err == nil {
		t.Fatal("RemoteDefaultBranch accepted remote show output without HEAD branch")
	}
}

func TestAheadBehindParsesAndRejects(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: "2\t3\n"}}}
	client := New(WithRunner(runner))
	ahead, behind, err := client.AheadBehind(context.Background(), "/tmp/wt", "origin/main", "HEAD")
	if err != nil {
		t.Fatalf("AheadBehind() error = %v", err)
	}
	if ahead != 3 || behind != 2 {
		t.Fatalf("ahead/behind = %d/%d, want 3/2", ahead, behind)
	}
	want := []string{"rev-list", "--left-right", "--count", "origin/main...HEAD"}
	if !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("args = %#v, want %#v", runner.calls[0].args, want)
	}

	for _, stdout := range []string{"only-one-field\n", "bad\t2\n", "1\tbad\n"} {
		client = New(WithRunner(&fakeRunner{results: []Result{{Stdout: stdout}}}))
		if _, _, err := client.AheadBehind(context.Background(), "/tmp/wt", "origin/main", "HEAD"); err == nil {
			t.Fatalf("AheadBehind accepted %q", stdout)
		}
	}

	client = New(WithRunner(&fakeRunner{errs: []error{errors.New("rev-list failed")}}))
	if _, _, err := client.AheadBehind(context.Background(), "/tmp/wt", "origin/main", "HEAD"); err == nil {
		t.Fatal("AheadBehind swallowed runner error")
	}
}

func TestIsDirtyError(t *testing.T) {
	client := New(WithRunner(&fakeRunner{errs: []error{errors.New("status failed")}}))
	if dirty, output, err := client.IsDirty(context.Background(), "/tmp/wt"); err == nil || dirty || output != "" {
		t.Fatalf("IsDirty(error) = %v %q %v", dirty, output, err)
	}
}

func TestDryRunLogsCommandsWithoutCallingRunner(t *testing.T) {
	runner := &fakeRunner{}
	var logs []string
	client := New(
		WithRunner(runner),
		WithDryRun(true, func(format string, args ...any) {
			logs = append(logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
		}),
	)
	if _, err := client.OutputIn(context.Background(), "/tmp/wt", "status", "--short"); err != nil {
		t.Fatalf("OutputIn(dry-run) error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dry-run called runner: %#v", runner.calls)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "dry-run: (cd /tmp/wt && git status --short)") {
		t.Fatalf("logs = %#v", logs)
	}

	client = New(WithRunner(runner), WithDryRun(true, nil))
	if err := client.FetchAllPrune(context.Background(), "/tmp/repo.git"); err != nil {
		t.Fatalf("FetchAllPrune(dry-run no log) error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dry-run (nil Logf) called runner: %#v", runner.calls)
	}
}

func TestGitErrorFormattingAndExecRunner(t *testing.T) {
	cause := errors.New("exit")
	err := &GitError{Args: []string{"status"}, ExitCode: 7, Stderr: " fatal\n", Err: cause}
	if !strings.Contains(err.Error(), "exit code 7") || !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("GitError message = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatalf("GitError did not unwrap cause")
	}
	if !IsExitCode(err, 7) || IsExitCode(cause, 7) {
		t.Fatalf("IsExitCode mismatch")
	}

	client := &Client{bin: "sh"}
	out, runErr := client.Output(context.Background(), "-c", "printf ok")
	if runErr != nil || out != "ok" {
		t.Fatalf("exec success = %q %v", out, runErr)
	}
	_, runErr = client.Output(context.Background(), "-c", "printf err >&2; exit 6")
	var gitErr *GitError
	if !errors.As(runErr, &gitErr) || gitErr.ExitCode != 6 || strings.TrimSpace(gitErr.Stderr) != "err" {
		t.Fatalf("exec error = %#v", runErr)
	}
}

// --- Real-git fixture tests ---

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func runGitTestCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitTestOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// makeSourceRepo creates a local repo on branch main with one commit.
func makeSourceRepo(t *testing.T, dir string) {
	t.Helper()
	runGitTestCommand(t, "", "init", "--initial-branch=main", dir)
	configureTestIdentity(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, dir, "add", "README.md")
	runGitTestCommand(t, dir, "commit", "-m", "initial")
}

func configureTestIdentity(t *testing.T, gitDirOrWorktree string) {
	t.Helper()
	runGitTestCommand(t, gitDirOrWorktree, "config", "user.email", "test@example.invalid")
	runGitTestCommand(t, gitDirOrWorktree, "config", "user.name", "Marmot Test")
	runGitTestCommand(t, gitDirOrWorktree, "config", "commit.gpgsign", "false")
}

func TestRealRepoCloneFetchAndDefaultBranch(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	bare := filepath.Join(tmp, "cache", "repo.git")
	makeSourceRepo(t, source)

	client := New()
	if err := client.CloneBare(ctx, source, bare); err != nil {
		t.Fatalf("CloneBare() error = %v", err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, bare); err != nil {
		t.Fatalf("ConfigureBareRemoteTracking() error = %v", err)
	}
	// Verify the refspec rewrite landed in the bare repo's config.
	if got := gitTestOutput(t, "", "--git-dir", bare, "config", "--get", "remote.origin.fetch"); got != "+refs/heads/*:refs/remotes/origin/*" {
		t.Fatalf("remote.origin.fetch = %q", got)
	}
	if err := client.FetchAllPrune(ctx, bare); err != nil {
		t.Fatalf("FetchAllPrune() error = %v", err)
	}
	if got := gitTestOutput(t, "", "--git-dir", bare, "rev-parse", "--verify", "refs/remotes/origin/main"); got == "" {
		t.Fatal("fetch did not create refs/remotes/origin/main")
	}

	// Bare clones have no origin/HEAD symbolic ref, so this exercises the
	// `remote show origin` fallback.
	branch, err := client.RemoteDefaultBranch(ctx, bare)
	if err != nil {
		t.Fatalf("RemoteDefaultBranch(fallback) error = %v", err)
	}
	if branch != "main" {
		t.Fatalf("RemoteDefaultBranch(fallback) = %q, want main", branch)
	}
	// After set-head, the symbolic-ref fast path resolves it locally.
	runGitTestCommand(t, "", "--git-dir", bare, "remote", "set-head", "origin", "--auto")
	branch, err = client.RemoteDefaultBranch(ctx, bare)
	if err != nil {
		t.Fatalf("RemoteDefaultBranch(symbolic-ref) error = %v", err)
	}
	if branch != "main" {
		t.Fatalf("RemoteDefaultBranch(symbolic-ref) = %q, want main", branch)
	}

	exists, err := client.BranchExists(ctx, bare, "main")
	if err != nil || !exists {
		t.Fatalf("BranchExists(main) = %v, %v", exists, err)
	}
	exists, err = client.BranchExists(ctx, bare, "no-such-branch")
	if err != nil || exists {
		t.Fatalf("BranchExists(no-such-branch) = %v, %v", exists, err)
	}
}

func TestRealRepoWorktreeLifecycle(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	bare := filepath.Join(tmp, "repo.git")
	wt := filepath.Join(tmp, "wt")
	wtDetached := filepath.Join(tmp, "wt-detached")
	wtExisting := filepath.Join(tmp, "wt-existing")
	makeSourceRepo(t, source)

	client := New()
	if err := client.CloneBare(ctx, source, bare); err != nil {
		t.Fatalf("CloneBare() error = %v", err)
	}
	if err := client.ConfigureBareRemoteTracking(ctx, bare); err != nil {
		t.Fatalf("ConfigureBareRemoteTracking() error = %v", err)
	}
	if err := client.FetchAllPrune(ctx, bare); err != nil {
		t.Fatalf("FetchAllPrune() error = %v", err)
	}
	// Bare-repo config is shared by all its worktrees.
	runGitTestCommand(t, "", "--git-dir", bare, "config", "user.email", "test@example.invalid")
	runGitTestCommand(t, "", "--git-dir", bare, "config", "user.name", "Marmot Test")
	runGitTestCommand(t, "", "--git-dir", bare, "config", "commit.gpgsign", "false")

	if err := client.WorktreeAddBranch(ctx, bare, wt, "marmot/edit", "origin/main"); err != nil {
		t.Fatalf("WorktreeAddBranch() error = %v", err)
	}
	// --no-track: the new branch must not track its start point.
	cmd := exec.CommandContext(ctx, "git", "-C", wt, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("new worktree branch unexpectedly tracks %q", strings.TrimSpace(string(out)))
	}

	// Dirty detection: clean, then untracked file, then pathspec-scoped.
	dirty, _, err := client.IsDirty(ctx, wt)
	if err != nil || dirty {
		t.Fatalf("IsDirty(clean) = %v, %v", dirty, err)
	}
	if err := os.WriteFile(filepath.Join(wt, "note.txt"), []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "other.txt"), []byte("o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, out, err := client.IsDirty(ctx, wt)
	if err != nil || !dirty || !strings.Contains(out, "note.txt") {
		t.Fatalf("IsDirty(untracked) = %v %q %v", dirty, out, err)
	}
	dirty, out, err = client.IsDirty(ctx, wt, "note.txt")
	if err != nil || !dirty || strings.Contains(out, "other.txt") {
		t.Fatalf("IsDirty(pathspec) = %v %q %v", dirty, out, err)
	}

	// Add/Commit/HeadCommit/StatusPorcelain.
	if err := client.Add(ctx, wt, "note.txt", "other.txt"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	status, err := client.StatusPorcelain(ctx, wt)
	if err != nil || !strings.Contains(status, "A  note.txt") {
		t.Fatalf("StatusPorcelain(staged) = %q, %v", status, err)
	}
	if err := client.Commit(ctx, wt, "add notes"); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	head, err := client.HeadCommit(ctx, wt)
	if err != nil {
		t.Fatalf("HeadCommit() error = %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40,}$`).MatchString(head) {
		t.Fatalf("HeadCommit() = %q, want a full hash", head)
	}
	dirty, _, err = client.IsDirty(ctx, wt)
	if err != nil || dirty {
		t.Fatalf("IsDirty(after commit) = %v, %v", dirty, err)
	}

	// Ahead/behind: one local commit ahead; then a new remote commit behind.
	ahead, behind, err := client.AheadBehind(ctx, wt, "origin/main", "HEAD")
	if err != nil || ahead != 1 || behind != 0 {
		t.Fatalf("AheadBehind(ahead) = %d/%d, %v, want 1/0", ahead, behind, err)
	}
	if err := os.WriteFile(filepath.Join(source, "upstream.txt"), []byte("u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, source, "add", "upstream.txt")
	runGitTestCommand(t, source, "commit", "-m", "upstream change")
	if err := client.FetchAllPrune(ctx, bare); err != nil {
		t.Fatalf("FetchAllPrune(refresh) error = %v", err)
	}
	ahead, behind, err = client.AheadBehind(ctx, wt, "origin/main", "HEAD")
	if err != nil || ahead != 1 || behind != 1 {
		t.Fatalf("AheadBehind(diverged) = %d/%d, %v, want 1/1", ahead, behind, err)
	}

	// Detached worktree + re-pin via CheckoutDetached.
	if err := client.WorktreeAddDetached(ctx, bare, wtDetached, "origin/main~1"); err != nil {
		t.Fatalf("WorktreeAddDetached() error = %v", err)
	}
	if err := client.CheckoutDetached(ctx, wtDetached, "origin/main"); err != nil {
		t.Fatalf("CheckoutDetached() error = %v", err)
	}
	detachedHead, err := client.HeadCommit(ctx, wtDetached)
	if err != nil {
		t.Fatalf("HeadCommit(detached) error = %v", err)
	}
	if want := gitTestOutput(t, "", "--git-dir", bare, "rev-parse", "origin/main"); detachedHead != want {
		t.Fatalf("detached HEAD = %q, want %q", detachedHead, want)
	}

	// Remove + re-attach the existing branch, then prune.
	if err := client.WorktreeRemove(ctx, bare, wt, false); err != nil {
		t.Fatalf("WorktreeRemove() error = %v", err)
	}
	if err := client.WorktreeAddExisting(ctx, bare, wtExisting, "marmot/edit"); err != nil {
		t.Fatalf("WorktreeAddExisting() error = %v", err)
	}
	if got := gitTestOutput(t, wtExisting, "rev-parse", "--abbrev-ref", "HEAD"); got != "marmot/edit" {
		t.Fatalf("existing worktree branch = %q", got)
	}
	if err := client.WorktreeRemove(ctx, bare, wtDetached, true); err != nil {
		t.Fatalf("WorktreeRemove(force) error = %v", err)
	}
	if err := client.WorktreePrune(ctx, bare); err != nil {
		t.Fatalf("WorktreePrune() error = %v", err)
	}
}

func TestWithCacheLockMutualExclusion(t *testing.T) {
	cacheRoot := t.TempDir()
	var inside int32
	var overlaps int32
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = WithCacheLock(cacheRoot, "warren-a", func() error {
				if !atomic.CompareAndSwapInt32(&inside, 0, 1) {
					atomic.AddInt32(&overlaps, 1)
				}
				time.Sleep(50 * time.Millisecond)
				atomic.StoreInt32(&inside, 0)
				return nil
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("WithCacheLock[%d] error = %v", i, err)
		}
	}
	if atomic.LoadInt32(&overlaps) != 0 {
		t.Fatal("critical sections overlapped under WithCacheLock")
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, "warren-a.git.lock")); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
}

func TestWithCacheLockPropagatesFnError(t *testing.T) {
	wantErr := errors.New("boom")
	err := WithCacheLock(t.TempDir(), "warren-a", func() error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestWithCacheLockRejectsInvalidIDs(t *testing.T) {
	for _, id := range []string{"", "a/b", "../escape", ".hidden"} {
		if err := WithCacheLock(t.TempDir(), id, func() error { return nil }); err == nil {
			t.Fatalf("WithCacheLock accepted invalid id %q", id)
		}
	}
}
