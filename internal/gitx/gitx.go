// Package gitx is the sanctioned git exec point for warren-cache operations
// (bare mirrors, worktrees, provenance pinning). It is a near-verbatim port
// of stave's internal/git client: a small Client with a Runner seam for
// tests, structured GitError with stderr capture, exec hardening
// (GIT_TERMINAL_PROMPT=0, LC_ALL=C), and a dry-run short-circuit that prints
// the exact command instead of running it.
//
// cmd/marmot's gitOutput remains the exec point for workspace verbs;
// internal/warren stays exec-free. This package depends only on the stdlib
// plus internal/flock (see WithCacheLock in lock.go).
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Client runs git commands. The zero value is not usable; construct with New.
type Client struct {
	bin    string
	runner Runner
	DryRun bool
	Logf   func(format string, args ...any)
}

// Runner is the exec seam: tests substitute a fake to record calls.
type Runner interface {
	Run(context.Context, string, []string, RunOptions) (Result, error)
}

// RunOptions carries per-invocation execution options.
type RunOptions struct {
	Dir string
}

// Result holds the captured output of one git invocation.
type Result struct {
	Stdout string
	Stderr string
}

// GitError is returned for failed git invocations, carrying the args, exit
// code, and captured stderr.
type GitError struct {
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *GitError) Error() string {
	msg := "git " + strings.Join(e.Args, " ") + " failed"
	if e.ExitCode >= 0 {
		msg += fmt.Sprintf(" with exit code %d", e.ExitCode)
	}
	if strings.TrimSpace(e.Stderr) != "" {
		msg += ": " + strings.TrimSpace(e.Stderr)
	}
	return msg
}

func (e *GitError) Unwrap() error {
	return e.Err
}

// Option configures a Client.
type Option func(*Client)

// New returns a Client that execs the real git binary unless overridden.
func New(opts ...Option) *Client {
	c := &Client{bin: "git", runner: execRunner{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithRunner substitutes the exec seam (used by tests).
func WithRunner(runner Runner) Option {
	return func(c *Client) {
		if runner != nil {
			c.runner = runner
		}
	}
}

// WithDryRun makes every command a no-op that logs `(cd dir && git …)`.
func WithDryRun(dryRun bool, logf func(format string, args ...any)) Option {
	return func(c *Client) {
		c.DryRun = dryRun
		c.Logf = logf
	}
}

// CloneBare clones url as a bare repository at dest.
func (c *Client) CloneBare(ctx context.Context, url, dest string) error {
	_, err := c.run(ctx, "clone", "--bare", url, dest)
	return err
}

// ConfigureBareRemoteTracking rewrites the fetch refspec so the bare mirror
// tracks remote branches under refs/remotes/origin/* (bare clones default to
// no remote-tracking refs; without this, fetch updates nothing useful).
func (c *Client) ConfigureBareRemoteTracking(ctx context.Context, bareDir string) error {
	_, err := c.run(ctx, "--git-dir", bareDir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	return err
}

// FetchAllPrune fetches all remotes into the bare mirror, pruning gone refs.
func (c *Client) FetchAllPrune(ctx context.Context, bareDir string) error {
	_, err := c.run(ctx, "--git-dir", bareDir, "fetch", "--all", "--prune")
	return err
}

// RemoteDefaultBranch resolves origin's default branch: the local
// origin/HEAD symbolic ref when present, else `git remote show origin`
// (which queries the remote).
func (c *Client) RemoteDefaultBranch(ctx context.Context, bareDir string) (string, error) {
	out, err := c.Output(ctx, "--git-dir", bareDir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimPrefix(strings.TrimSpace(out), "origin/"), nil
	}
	out, err = c.Output(ctx, "--git-dir", bareDir, "remote", "show", "origin")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HEAD branch:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "HEAD branch:")), nil
		}
	}
	return "", fmt.Errorf("could not determine remote default branch")
}

// WorktreeAddBranch creates a new branch at startPoint in a new worktree.
// --no-track keeps the branch from tracking startPoint (no-auto-push).
func (c *Client) WorktreeAddBranch(ctx context.Context, bareDir, path, branch, startPoint string) error {
	_, err := c.run(ctx, "--git-dir", bareDir, "worktree", "add", "--no-track", "-b", branch, path, startPoint)
	return err
}

// WorktreeAddExisting checks out an existing branch into a new worktree.
func (c *Client) WorktreeAddExisting(ctx context.Context, bareDir, path, branch string) error {
	_, err := c.run(ctx, "--git-dir", bareDir, "worktree", "add", path, branch)
	return err
}

// WorktreeAddDetached checks out ref detached into a new worktree.
func (c *Client) WorktreeAddDetached(ctx context.Context, bareDir, path, ref string) error {
	_, err := c.run(ctx, "--git-dir", bareDir, "worktree", "add", "--detach", path, ref)
	return err
}

// WorktreeRemove removes a worktree; force discards local modifications.
func (c *Client) WorktreeRemove(ctx context.Context, bareDir, path string, force bool) error {
	args := []string{"--git-dir", bareDir, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err := c.run(ctx, args...)
	return err
}

// WorktreePrune drops stale worktree bookkeeping under the bare repo.
func (c *Client) WorktreePrune(ctx context.Context, bareDir string) error {
	_, err := c.run(ctx, "--git-dir", bareDir, "worktree", "prune")
	return err
}

// CheckoutDetached re-pins an existing worktree at ref (detached HEAD).
func (c *Client) CheckoutDetached(ctx context.Context, dir, ref string) error {
	_, err := c.runIn(ctx, dir, "checkout", "--detach", ref)
	return err
}

// BranchExists reports whether refs/heads/<branch> exists in the bare repo.
func (c *Client) BranchExists(ctx context.Context, bareDir, branch string) (bool, error) {
	_, err := c.run(ctx, "--git-dir", bareDir, "show-ref", "--verify", "--quiet", "refs/heads/"+strings.TrimPrefix(branch, "refs/heads/"))
	if err == nil {
		return true, nil
	}
	if IsExitCode(err, 1) {
		return false, nil
	}
	return false, err
}

// IsDirty reports whether dir has uncommitted changes (optionally limited to
// pathspecs), returning the porcelain output for display.
func (c *Client) IsDirty(ctx context.Context, dir string, pathspecs ...string) (bool, string, error) {
	out, err := c.StatusPorcelain(ctx, dir, pathspecs...)
	if err != nil {
		return false, "", err
	}
	return strings.TrimSpace(out) != "", out, nil
}

// StatusPorcelain returns `git status --porcelain=v1` output for dir,
// optionally limited to pathspecs.
func (c *Client) StatusPorcelain(ctx context.Context, dir string, pathspecs ...string) (string, error) {
	args := []string{"status", "--porcelain=v1"}
	if len(pathspecs) > 0 {
		args = append(args, "--")
		args = append(args, pathspecs...)
	}
	return c.OutputIn(ctx, dir, args...)
}

// AheadBehind counts commits branch has that upstream lacks (ahead) and
// commits upstream has that branch lacks (behind), via
// `rev-list --left-right --count upstream...branch`.
func (c *Client) AheadBehind(ctx context.Context, dir, upstream, branch string) (ahead, behind int, err error) {
	out, err := c.OutputIn(ctx, dir, "rev-list", "--left-right", "--count", upstream+"..."+branch)
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %q", out)
	}
	behind, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	ahead, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

// Add stages pathspecs in dir; with no pathspecs it stages everything (-A).
func (c *Client) Add(ctx context.Context, dir string, pathspecs ...string) error {
	args := []string{"add"}
	if len(pathspecs) == 0 {
		args = append(args, "-A")
	} else {
		args = append(args, "--")
		args = append(args, pathspecs...)
	}
	_, err := c.runIn(ctx, dir, args...)
	return err
}

// Commit records staged changes in dir with msg; pathspecs, when given,
// limit the commit to those paths.
func (c *Client) Commit(ctx context.Context, dir, msg string, pathspecs ...string) error {
	args := []string{"commit", "-m", msg}
	if len(pathspecs) > 0 {
		args = append(args, "--")
		args = append(args, pathspecs...)
	}
	_, err := c.runIn(ctx, dir, args...)
	return err
}

// HeadCommit returns the full HEAD commit hash of dir.
func (c *Client) HeadCommit(ctx context.Context, dir string) (string, error) {
	out, err := c.OutputIn(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Output runs git with args (no working directory) and returns stdout.
func (c *Client) Output(ctx context.Context, args ...string) (string, error) {
	result, err := c.run(ctx, args...)
	return result.Stdout, err
}

// OutputIn runs git with args in dir and returns stdout.
func (c *Client) OutputIn(ctx context.Context, dir string, args ...string) (string, error) {
	result, err := c.runIn(ctx, dir, args...)
	return result.Stdout, err
}

func (c *Client) runIn(ctx context.Context, dir string, args ...string) (Result, error) {
	return c.runWithOptions(ctx, args, RunOptions{Dir: dir})
}

func (c *Client) run(ctx context.Context, args ...string) (Result, error) {
	return c.runWithOptions(ctx, args, RunOptions{})
}

func (c *Client) runWithOptions(ctx context.Context, args []string, opts RunOptions) (Result, error) {
	if c == nil {
		c = New()
	}
	if c.bin == "" {
		c.bin = "git"
	}
	if c.runner == nil {
		c.runner = execRunner{}
	}
	if c.DryRun {
		if c.Logf != nil {
			prefix := ""
			if opts.Dir != "" {
				prefix = "(cd " + opts.Dir + " && "
			}
			msg := "git " + strings.Join(args, " ")
			if prefix != "" {
				msg += ")"
			}
			c.Logf("dry-run: %s%s", prefix, msg)
		}
		return Result{}, nil
	}
	return c.runner.Run(ctx, c.bin, args, opts)
}

// IsExitCode reports whether err is a GitError with the given exit code.
func IsExitCode(err error, code int) bool {
	var gitErr *GitError
	return errors.As(err, &gitErr) && gitErr.ExitCode == code
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, bin string, args []string, opts RunOptions) (Result, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = opts.Dir
	// Fail fast instead of hanging on a credential prompt, and pin the
	// locale so output parsing is not localization-dependent.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return result, &GitError{Args: append([]string(nil), args...), ExitCode: exitCode, Stderr: result.Stderr, Err: err}
}
