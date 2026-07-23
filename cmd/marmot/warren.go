package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/gitx"
	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/warren"
	"github.com/nurozen/context-marmot/internal/warrenreg"
)

// gitOutput runs git against dir and returns its trimmed stdout. It is the
// only place production marmot execs git (internal/warren stays exec-free
// and unit-testable without a git binary); failures fold git's stderr into
// the error so callers can surface it verbatim.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// warrenHeadCommit resolves a warren checkout's HEAD for burrow provenance,
// degrading to "" when the checkout is not a git repo (or git is missing):
// provenance then records an unknown source and refresh treats the cache as
// always-stale, which is correct just more copying.
func warrenHeadCommit(warrenRoot string) string {
	head, err := gitOutput(warrenRoot, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return head
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

type repeatedStringFlag []string

func (f *repeatedStringFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *repeatedStringFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func cmdWarren(args []string) int {
	if len(args) == 0 {
		warrenUsage()
		return 1
	}
	sub := args[0]
	subArgs := args[1:]
	switch sub {
	case "init":
		return warrenInit(subArgs)
	case "add":
		return warrenAdd(subArgs)
	case "sync":
		return warrenSync(subArgs)
	case "register":
		return warrenRegister(subArgs)
	case "list":
		return warrenList(subArgs)
	case "project":
		return warrenProject(subArgs)
	case "bridge":
		return warrenBridge(subArgs)
	case "doctor":
		return warrenDoctor(subArgs)
	case "format":
		return warrenFormat(subArgs)
	case "mount":
		return warrenMount(subArgs, false)
	case "unmount":
		return warrenUnmount(subArgs)
	case "burrow":
		return warrenMount(subArgs, true)
	case "unregister":
		return warrenUnregister(subArgs)
	case "status":
		return warrenStatus(subArgs)
	case "edit":
		return warrenEdit(subArgs)
	case "refresh":
		return warrenRefresh(subArgs)
	case "propose":
		return warrenPropose(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "warren: unknown subcommand %q\n", sub)
		warrenUsage()
		return 1
	}
}

func warrenUsage() {
	fmt.Fprintln(os.Stderr, "usage: marmot warren <init|add|sync|project|bridge|doctor|format|register|unregister|list|mount|unmount|burrow|status|edit|refresh|propose> [flags]")
	fmt.Fprintln(os.Stderr, "  add <url> clones a warren into the shared cache ($MARMOT_HOME/warren-cache);")
	fmt.Fprintln(os.Stderr, "  sync [<id>] fetches cached warrens and re-pins their shared read checkouts;")
	fmt.Fprintln(os.Stderr, "  refresh [--pull] reloads warren state (and with --pull fast-forwards the checkout);")
	fmt.Fprintln(os.Stderr, "  propose branches+commits one project's editable-mount edits for review (never pushes)")
}

func warrenInit(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true, "id": true}, nil)
	fs := flag.NewFlagSet("warren init", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	warrenID := fs.String("id", "", "Warren ID")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	idFromFlag := *warrenID != ""
	if !idFromFlag && fs.NArg() == 1 {
		*warrenID = fs.Arg(0)
	}
	if *warrenID == "" || fs.NArg() > 1 || (idFromFlag && fs.NArg() != 0) {
		fmt.Fprintln(os.Stderr, "usage: marmot warren init --id <warren-id> [--warren-dir .]")
		return 1
	}
	if _, err := warren.Init(*root, *warrenID); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "warren init: %v\n", err)
			return 1
		}
		if err := warren.SaveManifest(*root, &warren.Manifest{WarrenID: *warrenID}, ""); err != nil {
			fmt.Fprintf(os.Stderr, "warren init: %v\n", err)
			return 1
		}
	}
	// The manifest flock file lives next to _warren.md inside the (usually
	// git-backed) warren repo; keep it out of version control.
	if err := ensureGitignoreEntry(*root, "_warren.md.lock"); err != nil {
		fmt.Fprintf(os.Stderr, "warren init: warning: could not update .gitignore: %v\n", err)
	}
	fmt.Printf("Initialized Warren %q at %s\n", *warrenID, *root)
	return 0
}

// ensureGitignoreEntry appends entry to <root>/.gitignore (creating the file
// if absent) unless an identical line is already present.
func ensureGitignoreEntry(root, entry string) error {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += entry + "\n"
	return os.WriteFile(path, []byte(out), 0o644)
}

// jsonWarrenAddEnvelope is the schema:1 stdout contract for
// `warren add --json` (additive-only evolution — never remove or repurpose a
// field; stave's memory verbs consume it).
type jsonWarrenAddEnvelope struct {
	Schema        int      `json:"schema"`
	WarrenID      string   `json:"warren_id"`
	URL           string   `json:"url"`
	CachePath     string   `json:"cache_path"`
	CheckoutPath  string   `json:"checkout_path"`
	DefaultBranch string   `json:"default_branch"`
	PinnedCommit  string   `json:"pinned_commit"`
	Reembedded    int      `json:"reembedded"`
	Warnings      []string `json:"warnings"`
}

// warrenIDFromURL derives the default warren id from a clone URL: the last
// path segment (after '/' or the scp-style ':') minus a .git suffix.
func warrenIDFromURL(url string) string {
	s := strings.TrimRight(strings.TrimSpace(url), "/")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSuffix(s, ".git")
}

// warrenAddFail routes an add refusal to the right surface: a schema:1 error
// envelope on stdout with --json, plain stderr otherwise. Always exits 1.
func warrenAddFail(jsonOut bool, code, message, hint string) int {
	if jsonOut {
		return denJSONError(code, message, hint)
	}
	fmt.Fprintf(os.Stderr, "warren add: %s\n", message)
	if hint != "" {
		fmt.Fprintf(os.Stderr, "warren add: hint: %s\n", hint)
	}
	return 1
}

// warrenAdd implements `marmot warren add <url> [--id <id>] [--dry-run]
// [--json]` (§5.2, stave reposAdd sequence): duplicate-id check BEFORE any fs
// op → existing-cache-path guard → CloneBare → refspec rewrite →
// FetchAllPrune → soft RemoteDefaultBranch → shared read checkout (detached
// worktree pinned at origin/<default_branch>) → manifest validation → pin
// stamp → registry save LAST, so a failed clone leaves no dangling registry
// entry and a failed registry save leaves no dangling cache.
func warrenAdd(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"id": true}, map[string]bool{"dry-run": true, "json": true})
	fs := flag.NewFlagSet("warren add", flag.ContinueOnError)
	idFlag := fs.String("id", "", "warren ID (default: URL basename minus .git)")
	dryRun := fs.Bool("dry-run", false, "print planned operations without touching the cache or registry")
	jsonOut := fs.Bool("json", false, "print a schema:1 JSON envelope on stdout")
	const usage = "usage: marmot warren add <url> [--id <id>] [--dry-run] [--json]"
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, usage)
	}
	if fs.NArg() != 1 {
		return warrenAddFail(*jsonOut, "invalid_args", "exactly one clone URL is required", usage)
	}
	url := fs.Arg(0)
	id := *idFlag
	if id == "" {
		id = warrenIDFromURL(url)
	}
	if err := warren.ValidateWarrenID(id); err != nil {
		return warrenAddFail(*jsonOut, "invalid_args", err.Error(), "pass an explicit id with --id <id>")
	}
	cacheRoot := home.WarrenCacheDir()
	barePath := warren.CacheBarePath(id)
	checkoutPath := warren.CacheCheckoutPath(id)
	// checkAddPreconditions is the duplicate-id / existing-cache-path guard.
	// It runs twice: here, pre-lock, as a fast lock-free refusal for the
	// common cases (and for --dry-run, which never takes the lock), and again
	// as the FIRST step inside the cache lock — the authoritative pass. Only
	// the in-lock pass gates filesystem work: a concurrent add of the same id
	// can win the lock between the two passes, and failure cleanup must never
	// remove a live cache this run did not create (F4).
	checkAddPreconditions := func() (code, msg, hint string) {
		reg, err := warrenreg.Load()
		if err != nil {
			return "registry_error", err.Error(), ""
		}
		if existing, dup := reg.Warrens[id]; dup {
			return "duplicate_warren",
				fmt.Sprintf("warren %q is already registered (url %s)", id, existing.URL),
				"pick another id with --id, or sync the existing cache with 'marmot warren sync " + id + "'"
		}
		for _, path := range []string{barePath, checkoutPath} {
			if _, statErr := os.Stat(path); statErr == nil {
				return "duplicate_warren",
					fmt.Sprintf("cache path %s already exists but warren %q is not in the registry", path, id),
					"remove the stale cache dir(s) (or pick another --id) and re-run"
			}
		}
		return "", "", ""
	}
	if code, msg, hint := checkAddPreconditions(); code != "" {
		return warrenAddFail(*jsonOut, code, msg, hint)
	}
	ctx := context.Background()
	if *dryRun {
		// Dry-run touches nothing: no cache lock (the flock sidecar would be
		// created), no clone, no registry write — just the planned ops.
		var ops []string
		client := gitx.New(gitx.WithDryRun(true, func(format string, a ...any) {
			ops = append(ops, strings.TrimPrefix(fmt.Sprintf(format, a...), "dry-run: "))
		}))
		_ = client.CloneBare(ctx, url, barePath)
		_ = client.ConfigureBareRemoteTracking(ctx, barePath)
		_ = client.FetchAllPrune(ctx, barePath)
		_ = client.WorktreeAddDetached(ctx, barePath, checkoutPath, "origin/<default-branch>")
		ops = append(ops,
			"write "+warren.CachePinPath(id),
			fmt.Sprintf("registry add %s -> %s (%s)", id, url, warrenreg.Path()))
		if *jsonOut {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return warrenAddFail(*jsonOut, "clone_failed", fmt.Sprintf("create cache root: %v", err), "")
	}
	client := gitx.New()
	warnings := []string{}
	defaultBranch := ""
	pinnedCommit := ""
	reembedded := 0
	failCode, failMsg, failHint := "", "", ""
	// cleanupCache removes everything this add created, in dependency order
	// (worktree bookkeeping before the bare repo), so a refusal leaves the
	// cache exactly as it was and the command is re-runnable. It only runs
	// inside the cache lock AFTER the in-lock precondition pass proved both
	// paths absent, so it can only ever remove artifacts created by THIS run —
	// never a live cache a concurrent add produced (F4).
	cleanupCache := func() {
		_ = client.WorktreeRemove(ctx, barePath, checkoutPath, true)
		_ = client.WorktreePrune(ctx, barePath)
		_ = os.RemoveAll(checkoutPath)
		_ = os.Remove(warren.CachePinPath(id))
		_ = os.RemoveAll(barePath)
	}
	lockErr := gitx.WithCacheLock(cacheRoot, id, func() error {
		// Authoritative duplicate/exists pass, now that we hold the lock: a
		// concurrent add of the same id may have registered and populated the
		// cache while we waited. Refuse WITHOUT any cleanup — nothing below
		// has run yet, so everything on disk belongs to someone else.
		if code, msg, hint := checkAddPreconditions(); code != "" {
			failCode, failMsg, failHint = code, msg, hint
			return errors.New(msg)
		}
		if err := client.CloneBare(ctx, url, barePath); err != nil {
			_ = os.RemoveAll(barePath)
			failCode, failMsg = "clone_failed", err.Error()
			return err
		}
		if err := client.ConfigureBareRemoteTracking(ctx, barePath); err != nil {
			cleanupCache()
			failCode, failMsg = "clone_failed", err.Error()
			return err
		}
		if err := client.FetchAllPrune(ctx, barePath); err != nil {
			cleanupCache()
			failCode, failMsg = "clone_failed", err.Error()
			return err
		}
		// Soft: an unresolved default branch degrades to the clone's HEAD.
		ref := "HEAD"
		if branch, branchErr := client.RemoteDefaultBranch(ctx, barePath); branchErr == nil && branch != "" {
			defaultBranch = branch
			ref = "origin/" + branch
		} else {
			warnings = append(warnings, "could not determine the remote default branch; pinned the clone's HEAD (sync will re-resolve)")
		}
		if err := client.WorktreeAddDetached(ctx, barePath, checkoutPath, ref); err != nil {
			cleanupCache()
			failCode, failMsg = "clone_failed", err.Error()
			return err
		}
		// The cache only ever holds warren repos: a repository without a
		// loadable _warren.md manifest is refused and everything created here
		// is removed.
		manifest, _, manifestErr := warren.LoadManifest(checkoutPath)
		if manifestErr != nil {
			cleanupCache()
			failCode = "not_a_warren"
			failMsg = fmt.Sprintf("repository at %s is not a warren (no loadable %s manifest): %v", url, warren.ManifestFileName, manifestErr)
			failHint = "initialize the repository as a warren first: 'marmot warren init --id <id>' in a checkout, commit, then re-run 'marmot warren add'"
			return manifestErr
		}
		if manifest.WarrenID != "" && manifest.WarrenID != id {
			warnings = append(warnings, fmt.Sprintf("manifest warren_id %q differs from cache id %q; register-free mount/status resolve by cache id but workspace registration validates against the manifest — re-add with --id %s to align", manifest.WarrenID, id, manifest.WarrenID))
		}
		head, headErr := client.HeadCommit(ctx, checkoutPath)
		if headErr != nil {
			cleanupCache()
			failCode, failMsg = "clone_failed", fmt.Sprintf("resolve pinned commit: %v", headErr)
			return headErr
		}
		pinnedCommit = head
		if err := warren.WriteCachePin(id, head); err != nil {
			cleanupCache()
			failCode, failMsg = "cache_write_failed", fmt.Sprintf("record checkout pin: %v", err)
			return err
		}
		// Registry save LAST, but still INSIDE the cache lock (F4): a failed
		// clone above never reached this point, so no dangling entry; a failed
		// save removes the fresh cache (still exclusively ours under the lock)
		// so re-running does not trip the existing-cache-path guard.
		if err := warrenreg.Update(func(reg *warrenreg.Registry) error {
			if existing, dup := reg.Warrens[id]; dup {
				return fmt.Errorf("warren %q was registered concurrently (url %s)", id, existing.URL)
			}
			reg.Warrens[id] = warrenreg.Entry{URL: url, DefaultBranch: defaultBranch}
			return nil
		}); err != nil {
			cleanupCache()
			failCode, failMsg = "registry_error", err.Error()
			return err
		}
		// Regenerate project-vault embeddings keyed to the fresh pin (F1):
		// the checkout's markdown may not ship vectors for every node.
		// Best-effort — reembed warnings never fail the add.
		reembedded, warnings = reembedCheckoutVaults(checkoutPath, id, warnings)
		return nil
	})
	if lockErr != nil {
		if failCode == "" {
			failCode, failMsg = "clone_failed", lockErr.Error()
		}
		return warrenAddFail(*jsonOut, failCode, failMsg, failHint)
	}
	if *jsonOut {
		return printDenJSON(jsonWarrenAddEnvelope{
			Schema:        1,
			WarrenID:      id,
			URL:           url,
			CachePath:     barePath,
			CheckoutPath:  checkoutPath,
			DefaultBranch: defaultBranch,
			PinnedCommit:  pinnedCommit,
			Reembedded:    reembedded,
			Warnings:      warnings,
		})
	}
	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
	fmt.Printf("Added warren %q to the shared cache\n", id)
	fmt.Printf("  checkout: %s (pinned at %s)\n", checkoutPath, shortCommit(pinnedCommit))
	if reembedded > 0 {
		fmt.Printf("  reembedded %d node(s) across project vaults\n", reembedded)
	}
	fmt.Printf("Keep it fresh with 'marmot warren sync %s'\n", id)
	return 0
}

// jsonWarrenSyncResult is one warren's outcome inside the sync envelope.
// Field names are stave-consumed (memory sync) — keep them stable.
type jsonWarrenSyncResult struct {
	ID             string `json:"id"`
	Fetched        bool   `json:"fetched"`
	PreviousCommit string `json:"previous_commit"`
	PinnedCommit   string `json:"pinned_commit"`
	Updated        bool   `json:"updated"`
	// Reembedded counts project-vault embeddings regenerated for this pin
	// (F1: contribute PRs carry markdown only, so merged nodes need their
	// vectors rebuilt with each vault's own embedder). Additive field.
	Reembedded int    `json:"reembedded"`
	Error      string `json:"error,omitempty"`
}

// jsonWarrenSyncEnvelope is the schema:1 stdout contract for
// `warren sync --json`.
type jsonWarrenSyncEnvelope struct {
	Schema   int                    `json:"schema"`
	Warrens  []jsonWarrenSyncResult `json:"warrens"`
	Warnings []string               `json:"warnings"`
}

// warrenSync implements `marmot warren sync [<id>] [--dry-run] [--json]`
// (§5.2): for one or every registered warren, fetch the bare mirror and
// re-pin the shared read checkout at the new origin/<default_branch> head.
// Per-warren failures never abort the loop; exit policy: 0 when at least one
// warren synced (per-warren errors are reported inline), 1 only when every
// warren failed. An empty registry is informational, exit 0.
func warrenSync(args []string) int {
	args = reorderInterspersedFlags(args, nil, map[string]bool{"dry-run": true, "json": true})
	fs := flag.NewFlagSet("warren sync", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print planned operations without touching the cache")
	jsonOut := fs.Bool("json", false, "print a schema:1 JSON envelope on stdout")
	const usage = "usage: marmot warren sync [<warren-id>] [--dry-run] [--json]"
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, usage)
	}
	if fs.NArg() > 1 {
		if *jsonOut {
			return denJSONError("invalid_args", "too many arguments", usage)
		}
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	reg, err := warrenreg.Load()
	if err != nil {
		if *jsonOut {
			return denJSONError("registry_error", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "warren sync: %v\n", err)
		return 1
	}
	var ids []string
	if fs.NArg() == 1 {
		id := fs.Arg(0)
		if _, ok := reg.Warrens[id]; !ok {
			hint := "add it with 'marmot warren add <url> --id " + id + "'"
			if *jsonOut {
				return denJSONError("invalid_args", fmt.Sprintf("warren %q is not in the shared cache registry", id), hint)
			}
			fmt.Fprintf(os.Stderr, "warren sync: warren %q is not in the shared cache registry — %s\n", id, hint)
			return 1
		}
		ids = []string{id}
	} else {
		for id := range reg.Warrens {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	if len(ids) == 0 {
		if *jsonOut {
			return printDenJSON(jsonWarrenSyncEnvelope{Schema: 1, Warrens: []jsonWarrenSyncResult{}, Warnings: []string{}})
		}
		fmt.Println("No warrens registered.")
		return 0
	}
	ctx := context.Background()
	if *dryRun {
		// Dry-run touches nothing: no cache locks, no pin writes.
		var ops []string
		client := gitx.New(gitx.WithDryRun(true, func(format string, a ...any) {
			ops = append(ops, strings.TrimPrefix(fmt.Sprintf(format, a...), "dry-run: "))
		}))
		for _, id := range ids {
			ref := "origin/<default-branch>"
			if branch := reg.Warrens[id].DefaultBranch; branch != "" {
				ref = "origin/" + branch
			}
			_ = client.FetchAllPrune(ctx, warren.CacheBarePath(id))
			_ = client.CheckoutDetached(ctx, warren.CacheCheckoutPath(id), ref)
			ops = append(ops, "write "+warren.CachePinPath(id))
		}
		if *jsonOut {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}
	client := gitx.New()
	warnings := []string{}
	resolvedBranches := map[string]string{}
	results := make([]jsonWarrenSyncResult, 0, len(ids))
	failed := 0
	for _, id := range ids {
		result, resolved, reembedWarnings := syncOneWarren(ctx, client, home.WarrenCacheDir(), id, reg.Warrens[id])
		warnings = append(warnings, reembedWarnings...)
		if resolved != "" {
			resolvedBranches[id] = resolved
		}
		if result.Error != "" {
			failed++
		}
		results = append(results, result)
		if *jsonOut {
			continue
		}
		switch {
		case result.Error != "":
			fmt.Fprintf(os.Stderr, "warren sync: %s: %s\n", id, result.Error)
		case result.Updated && result.PreviousCommit == "":
			fmt.Printf("Warren %q synced: pinned at %s\n", id, shortCommit(result.PinnedCommit))
		case result.Updated:
			fmt.Printf("Warren %q synced: %s -> %s\n", id, shortCommit(result.PreviousCommit), shortCommit(result.PinnedCommit))
		default:
			fmt.Printf("Warren %q already up to date at %s\n", id, shortCommit(result.PinnedCommit))
		}
		if result.Error == "" && result.Reembedded > 0 {
			fmt.Printf("  reembedded %d node(s) across project vaults\n", result.Reembedded)
		}
	}
	// Persist default branches resolved during this sync (registered with an
	// unresolvable one) so later syncs and den links stop guessing.
	// Best-effort: the sync itself already succeeded.
	if len(resolvedBranches) > 0 {
		if err := warrenreg.Update(func(reg *warrenreg.Registry) error {
			for id, branch := range resolvedBranches {
				if entry, ok := reg.Warrens[id]; ok && entry.DefaultBranch == "" {
					entry.DefaultBranch = branch
					reg.Warrens[id] = entry
				}
			}
			return nil
		}); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not record resolved default branch(es) in the registry: %v", err))
		}
	}
	exit := 0
	if failed == len(ids) {
		exit = 1
	}
	if *jsonOut {
		if code := printDenJSON(jsonWarrenSyncEnvelope{Schema: 1, Warrens: results, Warnings: warnings}); code != 0 {
			return code
		}
		return exit
	}
	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
	return exit
}

// syncOneWarren fetches one warren's bare mirror and re-pins its shared read
// checkout under the per-warren cache lock. It returns the per-warren result
// (errors captured, never fatal to the caller's loop), the default branch it
// had to resolve when the registry entry lacked one, and any reembed warnings
// (best-effort, never fatal).
func syncOneWarren(ctx context.Context, client *gitx.Client, cacheRoot, id string, entry warrenreg.Entry) (jsonWarrenSyncResult, string, []string) {
	result := jsonWarrenSyncResult{ID: id, PreviousCommit: warren.ReadCachePin(id)}
	resolvedBranch := ""
	var reembedWarnings []string
	err := gitx.WithCacheLock(cacheRoot, id, func() error {
		barePath := warren.CacheBarePath(id)
		if !dirExistsCLI(barePath) {
			return fmt.Errorf("bare cache missing at %s (re-add with 'marmot warren add %s --id %s')", barePath, entry.URL, id)
		}
		if err := client.FetchAllPrune(ctx, barePath); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
		result.Fetched = true
		branch := entry.DefaultBranch
		if branch == "" {
			if b, branchErr := client.RemoteDefaultBranch(ctx, barePath); branchErr == nil && b != "" {
				branch, resolvedBranch = b, b
			}
		}
		ref := "HEAD"
		if branch != "" {
			ref = "origin/" + branch
		}
		checkoutPath := warren.CacheCheckoutPath(id)
		if !dirExistsCLI(checkoutPath) {
			// The shared checkout disappeared (manual cleanup): recreate it
			// rather than fail — sync's contract is "a fresh pinned checkout".
			if err := client.WorktreeAddDetached(ctx, barePath, checkoutPath, ref); err != nil {
				return fmt.Errorf("recreate shared checkout: %w", err)
			}
		} else if err := client.CheckoutDetached(ctx, checkoutPath, ref); err != nil {
			return fmt.Errorf("re-pin shared checkout: %w", err)
		}
		head, err := client.HeadCommit(ctx, checkoutPath)
		if err != nil {
			return fmt.Errorf("resolve pinned commit: %w", err)
		}
		result.PinnedCommit = head
		result.Updated = head != result.PreviousCommit
		if err := warren.WriteCachePin(id, head); err != nil {
			return fmt.Errorf("record checkout pin: %w", err)
		}
		// Regenerate project-vault embeddings keyed to the new pin (F1):
		// contribute PRs carry markdown only, so nodes merged upstream are
		// invisible to semantic search until their vectors are rebuilt with
		// each vault's own embedder. Runs on every sync — the per-node
		// summary-hash check makes the no-change case cheap, and it also
		// heals checkouts pinned before this feature existed.
		result.Reembedded, reembedWarnings = reembedCheckoutVaults(warren.CacheCheckoutPath(id), id, nil)
		return nil
	})
	if err != nil {
		result.Error = err.Error()
	}
	return result, resolvedBranch, reembedWarnings
}

// reembedCheckoutVaults regenerates missing/stale embeddings for every
// project vault inside a warren's shared cache checkout (F1). Contribute PRs
// carry markdown only — a merged node has no row in the checkout's
// .marmot-data/embeddings.db, so it is invisible to semantic search until its
// vector is regenerated keyed to the current pin. For each manifest project
// it lists active nodes, hash-checks each against the vault's store (cheap
// no-op when nothing changed), and embeds only absent/stale nodes with the
// vault's OWN configured embedder. It never poisons a store: a store already
// holding a different model is skipped with a warning naming both models (F2
// guard), and a configured-but-keyless provider skips the vault instead of
// silently writing mock-model rows. Every failure degrades to a warning —
// add/sync still succeed. Callers must hold the warren's cache lock (the
// checkout must not move underneath the scan). Returns the total upserted
// count and warnings appended to the passed slice.
func reembedCheckoutVaults(checkoutPath, warrenID string, warnings []string) (int, []string) {
	manifest, _, err := warren.LoadManifest(checkoutPath)
	if err != nil {
		return 0, append(warnings, fmt.Sprintf("%s: reembed skipped: manifest unreadable: %v", warrenID, err))
	}
	total := 0
	for _, project := range manifest.Projects {
		vaultDir := filepath.Join(checkoutPath, filepath.FromSlash(project.Path))
		if !dirExistsCLI(vaultDir) {
			continue // project vault absent at this pin — nothing to embed
		}
		n, vaultWarnings := reembedVault(vaultDir, warrenID+"/"+project.ProjectID)
		total += n
		warnings = append(warnings, vaultWarnings...)
	}
	return total, warnings
}

// reembedVault upserts missing/stale embeddings for one project vault's
// active nodes, embedding with the vault's own configured embedder (lazily
// built only when a stale node is found). label prefixes every warning
// ("<warren>/<project>").
func reembedVault(vaultDir, label string) (int, []string) {
	var warnings []string
	warnf := func(format string, a ...any) {
		warnings = append(warnings, label+": "+fmt.Sprintf(format, a...))
	}
	store := node.NewStore(vaultDir)
	metas, err := store.ListActiveNodes()
	if err != nil {
		warnf("reembed skipped: list nodes: %v", err)
		return 0, warnings
	}
	if len(metas) == 0 {
		return 0, warnings
	}
	dbPath := filepath.Join(vaultDir, ".marmot-data", "embeddings.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		warnf("reembed skipped: %v", err)
		return 0, warnings
	}
	embStore, err := embedding.NewStore(dbPath)
	if err != nil {
		warnf("reembed skipped: open embeddings: %v", err)
		return 0, warnings
	}
	defer func() { _ = embStore.Close() }()

	var embedder embedding.Embedder // built lazily on the first stale node
	count := 0
	for _, meta := range metas {
		n, loadErr := store.LoadNode(meta.FilePath)
		if loadErr != nil {
			warnf("node %s unreadable: %v", meta.ID, loadErr)
			continue
		}
		if n.ID == "" {
			n.ID = meta.ID
		}
		text := warren.EmbedText(n)
		if text == "" {
			continue
		}
		hash := embedTextHash(text)
		stale, staleErr := embStore.StaleCheck(n.ID, hash)
		if staleErr != nil {
			warnf("stale check %s: %v", n.ID, staleErr)
			continue
		}
		if !stale {
			continue // fast path: vector already keyed to this content
		}
		if embedder == nil {
			var setupWarning string
			embedder, setupWarning = reembedEmbedder(vaultDir)
			if embedder == nil {
				if setupWarning != "" {
					warnf("%s", setupWarning)
				}
				return count, warnings
			}
			// Whole-store model guard (F2) before the first write: never mix
			// models — one mixed row breaks every future search of the store.
			if ok, compatErr := embStore.CompatibleModel(embedder.Model()); compatErr != nil {
				warnf("reembed skipped: %v", compatErr)
				return count, warnings
			} else if !ok {
				stored, _ := embStore.Models()
				warnf("reembed skipped: store holds model(s) %s but the vault's embedder is %q — refusing to mix embedding models", strings.Join(stored, ","), embedder.Model())
				return count, warnings
			}
		}
		vec, embErr := embedder.Embed(text)
		if embErr != nil {
			warnf("embed %s: %v", n.ID, embErr)
			continue
		}
		if upErr := embStore.UpsertChecked(n.ID, vec, hash, embedder.Model()); upErr != nil {
			warnf("upsert %s: %v", n.ID, upErr)
			continue
		}
		count++
	}
	return count, warnings
}

// reembedEmbedder builds a project vault's own configured embedder for the
// sync reembed. It returns nil (with a warning) when the vault config is
// unreadable, or when a real provider is configured but its API key is
// missing: NewEmbedderFromVault would silently fall back to the mock
// embedder, and writing mock-model rows into a store meant for the real
// model is exactly the poisoning F2 forbids.
func reembedEmbedder(vaultDir string) (embedding.Embedder, string) {
	cfg, err := config.Load(vaultDir)
	if err != nil {
		return nil, fmt.Sprintf("reembed skipped: vault config unreadable: %v", err)
	}
	provider := cfg.EmbeddingProvider
	if provider != "" && provider != "mock" && config.APIKeyWithVault(provider, cfg.VaultDir) == "" {
		return nil, fmt.Sprintf("reembed skipped: %s provider configured but no API key found (set %s) — refusing the mock-embedder fallback", provider, config.EnvKeyName(provider))
	}
	emb, err := config.NewEmbedderFromVault(cfg)
	if err != nil {
		return nil, fmt.Sprintf("reembed skipped: embedder unavailable: %v", err)
	}
	return emb, ""
}

// embedTextHash is the summary-hash formula shared with the MCP/warren write
// paths (sha256 hex of the embed text — staleness detection keys on it).
func embedTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func warrenProject(args []string) int {
	if len(args) == 0 {
		warrenProjectUsage()
		return 1
	}
	sub := args[0]
	subArgs := args[1:]
	switch sub {
	case "add":
		return warrenProjectAdd(subArgs)
	case "import":
		return warrenProjectImport(subArgs)
	case "list":
		return warrenProjectList(subArgs)
	case "remove":
		return warrenProjectRemove(subArgs)
	case "rename":
		return warrenProjectRename(subArgs)
	case "set-readonly":
		return warrenProjectSetReadonly(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "warren project: unknown subcommand %q\n", sub)
		warrenProjectUsage()
		return 1
	}
}

func warrenProjectUsage() {
	fmt.Fprintln(os.Stderr, "usage: marmot warren project <add|import|list|remove|rename|set-readonly> [flags]")
}

// warrenProjectSetReadonly flips the author-side write policy for one
// project (D4). It is a warren-repo-side verb: it mutates the manifest in
// the checkout (flocked, version-checked), not workspace state.
func warrenProjectSetReadonly(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true}, map[string]bool{"off": true})
	fs := flag.NewFlagSet("warren project set-readonly", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	off := fs.Bool("off", false, "clear the read-only policy (consumers may enable edit again)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren project set-readonly [--warren-dir .] [--off] <project-id>")
		return 1
	}
	if _, err := warren.SetProjectReadOnly(*root, fs.Arg(0), !*off); err != nil {
		fmt.Fprintf(os.Stderr, "warren project set-readonly: %v\n", err)
		return 1
	}
	if *off {
		fmt.Printf("Project %q accepts consumer edits again\n", fs.Arg(0))
	} else {
		fmt.Printf("Project %q is read-only for consumers (edits must go through the warren repository)\n", fs.Arg(0))
	}
	return 0
}

func warrenProjectAdd(args []string) int {
	args = reorderInterspersedFlags(args,
		map[string]bool{"warren-dir": true, "path": true, "vault-id": true, "alias": true, "source-url": true, "source-commit": true},
		map[string]bool{"generate-id": true},
	)
	fs := flag.NewFlagSet("warren project add", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	path := fs.String("path", "", "project .marmot path inside the Warren")
	vaultID := fs.String("vault-id", "", "vault ID (default: project ID)")
	generateID := fs.Bool("generate-id", false, "generate the project ID from existing metadata or path")
	sourceURL := fs.String("source-url", "", "source repo URL recorded as manifest v3 provenance")
	sourceCommit := fs.String("source-commit", "", "source repo commit recorded as manifest v3 provenance")
	var aliases repeatedStringFlag
	fs.Var(&aliases, "alias", "project alias (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren project add <project-id> --path <project-.marmot> [--warren-dir .] [--vault-id <id>] [--alias <name>]...")
		return 1
	}
	projectID := ""
	if fs.NArg() == 1 {
		projectID = fs.Arg(0)
	}
	if *generateID {
		projectID = ""
	}
	if projectID == "" && !*generateID {
		fmt.Fprintln(os.Stderr, "usage: marmot warren project add <project-id> --path <project-.marmot> [--warren-dir .] [--vault-id <id>] [--alias <name>]...")
		return 1
	}
	if projectID == "" {
		projectID = generatedProjectID(*root, *path)
	}
	if *path == "" {
		*path = filepath.ToSlash(filepath.Join("projects", projectID, ".marmot"))
	}
	if *vaultID != "" {
		if err := warren.ValidateProjectID(*vaultID); err != nil {
			fmt.Fprintf(os.Stderr, "warren project add: %v\n", err)
			return 1
		}
	}
	project := warren.Project{
		ProjectID:    projectID,
		Path:         filepath.ToSlash(*path),
		Aliases:      aliases,
		SourceURL:    warren.CanonicalRepoURL(*sourceURL),
		SourceCommit: strings.TrimSpace(*sourceCommit),
	}
	if _, err := warren.AddProject(*root, project); err != nil {
		fmt.Fprintf(os.Stderr, "warren project add: %v\n", err)
		return 1
	}
	if *vaultID != "" {
		marmotDir := filepath.Join(*root, filepath.FromSlash(project.Path))
		meta, body, err := warren.LoadProjectMetadata(marmotDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warren project add: %v\n", err)
			return 1
		}
		meta.VaultID = *vaultID
		if err := warren.SaveProjectMetadata(marmotDir, meta, body); err != nil {
			fmt.Fprintf(os.Stderr, "warren project add: %v\n", err)
			return 1
		}
	}
	fmt.Printf("Added project %q -> %s\n", project.ProjectID, project.Path)
	return 0
}

func generatedProjectID(root, projectPath string) string {
	if projectPath != "" {
		marmotDir := filepath.Join(root, filepath.FromSlash(projectPath))
		if meta, _, err := warren.LoadProjectMetadata(marmotDir); err == nil && meta.ProjectID != "" {
			return meta.ProjectID
		}
		clean := filepath.Clean(projectPath)
		if filepath.Base(clean) == ".marmot" {
			return warren.GenerateProjectID(filepath.Base(filepath.Dir(clean)))
		}
		return warren.GenerateProjectID(filepath.Base(clean))
	}
	return "project"
}

func warrenProjectImport(args []string) int {
	args = reorderInterspersedFlags(args,
		map[string]bool{"warren-dir": true, "path": true, "vault-id": true, "alias": true, "source-url": true, "source-commit": true},
		map[string]bool{"generate-id": true, "include-heat": true, "no-obsidian": true},
	)
	fs := flag.NewFlagSet("warren project import", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	path := fs.String("path", "", "destination .marmot path inside the Warren")
	vaultID := fs.String("vault-id", "", "vault ID (default: source vault_id or project ID)")
	generateID := fs.Bool("generate-id", false, "generate the project ID from existing metadata or source path")
	includeHeat := fs.Bool("include-heat", false, "include _heat/ files")
	noObsidian := fs.Bool("no-obsidian", false, "exclude .obsidian/ files")
	sourceURL := fs.String("source-url", "", "source repo URL recorded as manifest v3 provenance (default: auto-detect origin of the source checkout)")
	sourceCommit := fs.String("source-commit", "", "source repo commit recorded as manifest v3 provenance (default: auto-detect HEAD of the source checkout)")
	var aliases repeatedStringFlag
	fs.Var(&aliases, "alias", "project alias (repeatable)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	*root = resolveWarrenRoot(*root)

	var projectID, source string
	switch fs.NArg() {
	case 1:
		source = fs.Arg(0)
	case 2:
		projectID = fs.Arg(0)
		source = fs.Arg(1)
	default:
		fmt.Fprintln(os.Stderr, "usage: marmot warren project import <project-id> <source-.marmot> [--warren-dir .] [--path projects/<project-id>/.marmot] [--vault-id <id>] [--alias <name>]...")
		return 1
	}
	if *generateID {
		projectID = ""
	}
	if projectID == "" {
		if !*generateID {
			fmt.Fprintln(os.Stderr, "usage: marmot warren project import <project-id> <source-.marmot> [--warren-dir .] [--path projects/<project-id>/.marmot] [--vault-id <id>] [--alias <name>]...")
			return 1
		}
		projectID = generatedImportProjectID(source)
	}
	if *path == "" {
		*path = filepath.ToSlash(filepath.Join("projects", projectID, ".marmot"))
	}
	project := warren.Project{
		ProjectID: projectID,
		Path:      filepath.ToSlash(*path),
		Aliases:   aliases,
	}
	autoURL, autoCommit := detectImportSource(source)
	if *sourceURL == "" {
		*sourceURL = autoURL
	}
	if *sourceCommit == "" {
		*sourceCommit = autoCommit
	}
	opts := warren.ImportOptions{
		IncludeHeat:  *includeHeat,
		NoObsidian:   *noObsidian,
		VaultID:      *vaultID,
		SourceURL:    *sourceURL,
		SourceCommit: *sourceCommit,
	}
	if _, err := warren.ImportProject(*root, source, project, opts); err != nil {
		fmt.Fprintf(os.Stderr, "warren project import: %v\n", err)
		return 1
	}
	fmt.Printf("Imported project %q from %s -> %s\n", project.ProjectID, source, project.Path)
	return 0
}

// detectImportSource captures manifest v3 provenance for an import: when the
// source .marmot dir's parent is inside a git repo, the repo's HEAD commit
// and origin remote URL (canonicalized at write time by ImportProject) are
// recorded automatically. Best-effort by design — a non-git source (or a
// repo without an origin remote) degrades to empty fields and never blocks
// the import.
func detectImportSource(sourceMarmotDir string) (url, commit string) {
	abs, err := filepath.Abs(sourceMarmotDir)
	if err != nil {
		return "", ""
	}
	parent := filepath.Dir(abs)
	ctx := context.Background()
	client := gitx.New()
	head, err := client.HeadCommit(ctx, parent)
	if err != nil {
		return "", "" // not a git repo (or empty repo): no provenance
	}
	if origin, originErr := client.OutputIn(ctx, parent, "remote", "get-url", "origin"); originErr == nil {
		url = strings.TrimSpace(origin)
	}
	return url, head
}

func generatedImportProjectID(source string) string {
	if meta, _, err := warren.LoadProjectMetadata(source); err == nil && meta.ProjectID != "" {
		return meta.ProjectID
	}
	clean := filepath.Clean(source)
	if filepath.Base(clean) == ".marmot" {
		return warren.GenerateProjectID(filepath.Base(filepath.Dir(clean)))
	}
	return warren.GenerateProjectID(filepath.Base(clean))
}

func warrenProjectList(args []string) int {
	fs := flag.NewFlagSet("warren project list", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren project list [--warren-dir .] [--json]")
		return 1
	}
	projects, err := warren.ListProjects(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren project list: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(projects)
	}
	if len(projects) == 0 {
		fmt.Println("No projects registered.")
		return 0
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT_ID\tPATH\tALIASES")
	for _, project := range projects {
		fmt.Fprintf(w, "%s\t%s\t%s\n", project.ProjectID, project.Path, strings.Join(project.Aliases, ","))
	}
	_ = w.Flush()
	return 0
}

func warrenProjectRemove(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true}, nil)
	fs := flag.NewFlagSet("warren project remove", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren project remove [--warren-dir .] <project-id>")
		return 1
	}
	if _, err := warren.RemoveProject(*root, fs.Arg(0)); err != nil {
		fmt.Fprintf(os.Stderr, "warren project remove: %v\n", err)
		return 1
	}
	fmt.Printf("Removed project %q\n", fs.Arg(0))
	return 0
}

func warrenProjectRename(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true}, map[string]bool{"keep-path": true})
	fs := flag.NewFlagSet("warren project rename", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	keepPath := fs.Bool("keep-path", false, "rename the project ID only; do not move projects/<old-id>/ to projects/<new-id>/")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren project rename [--warren-dir .] [--keep-path] <old-project-id> <new-project-id>")
		return 1
	}
	result, err := warren.RenameProject(*root, fs.Arg(0), fs.Arg(1), *keepPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren project rename: %v\n", err)
		return 1
	}
	if result.Moved {
		fmt.Printf("Renamed project %q -> %q (moved %s -> %s)\n", fs.Arg(0), fs.Arg(1), result.OldDir, result.NewDir)
		if len(result.Repointed) > 0 {
			fmt.Printf("note: repointed nested project checkout paths moved with %s: %s\n", result.OldDir, strings.Join(result.Repointed, ", "))
		}
	} else {
		fmt.Printf("Renamed project %q -> %q (path %s kept)\n", fs.Arg(0), fs.Arg(1), result.PathKept)
	}
	// Rename never rewrites vault_id — it is the identity key. Only say so
	// when the old vault_id==project_id default now visibly diverges.
	if result.VaultID != "" && result.VaultID != fs.Arg(1) {
		fmt.Printf("note: vault_id %q unchanged — vault identity is stable across renames; re-import with --vault-id to change it\n", result.VaultID)
	}
	return 0
}

func warrenBridge(args []string) int {
	if len(args) == 0 {
		warrenBridgeUsage()
		return 1
	}
	sub := args[0]
	subArgs := args[1:]
	switch sub {
	case "add":
		return warrenBridgeAdd(subArgs)
	case "list":
		return warrenBridgeList(subArgs)
	case "remove":
		return warrenBridgeRemove(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "warren bridge: unknown subcommand %q\n", sub)
		warrenBridgeUsage()
		return 1
	}
}

func warrenBridgeUsage() {
	fmt.Fprintln(os.Stderr, "usage: marmot warren bridge <add|list|remove> [flags]")
}

func warrenBridgeAdd(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true, "relations": true}, nil)
	fs := flag.NewFlagSet("warren bridge add", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	relations := fs.String("relations", "references", "comma-separated allowed relations")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren bridge add [--warren-dir .] [--relations references,calls] <source-project> <target-project>")
		return 1
	}
	bridge := warren.Bridge{
		Source:    fs.Arg(0),
		Target:    fs.Arg(1),
		Relations: splitCSV(*relations),
	}
	if _, err := warren.AddBridge(*root, bridge); err != nil {
		fmt.Fprintf(os.Stderr, "warren bridge add: %v\n", err)
		return 1
	}
	fmt.Printf("Added bridge %q -> %q\n", bridge.Source, bridge.Target)
	return 0
}

func warrenBridgeList(args []string) int {
	fs := flag.NewFlagSet("warren bridge list", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren bridge list [--warren-dir .] [--json]")
		return 1
	}
	bridges, err := warren.ListBridges(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren bridge list: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(bridges)
	}
	if len(bridges) == 0 {
		fmt.Println("No bridges registered.")
		return 0
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tTARGET\tRELATIONS")
	for _, bridge := range bridges {
		fmt.Fprintf(w, "%s\t%s\t%s\n", bridge.Source, bridge.Target, strings.Join(bridge.Relations, ","))
	}
	_ = w.Flush()
	return 0
}

func warrenBridgeRemove(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true}, nil)
	fs := flag.NewFlagSet("warren bridge remove", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren bridge remove [--warren-dir .] <source-project> <target-project>")
		return 1
	}
	if _, err := warren.RemoveBridge(*root, fs.Arg(0), fs.Arg(1)); err != nil {
		fmt.Fprintf(os.Stderr, "warren bridge remove: %v\n", err)
		return 1
	}
	fmt.Printf("Removed bridge %q -> %q\n", fs.Arg(0), fs.Arg(1))
	return 0
}

func warrenDoctor(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true, "dir": true}, map[string]bool{"json": true, "workspace": true})
	fs := flag.NewFlagSet("warren doctor", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	dir := fs.String("dir", "", "marmot vault directory for --workspace (default: auto-discover or .marmot)")
	workspaceMode := fs.Bool("workspace", false, "check this workspace's warren state (vault-ID collisions) instead of a warren repository")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren doctor [--warren-dir .] [--workspace [--dir .marmot]] [--json]")
		return 1
	}
	if *workspaceMode {
		// Workspace-side mode (D5.3): read-only, so it must not fabricate a
		// workspace — locateWorkspace, never ensureWorkspace.
		marmotDir, workspaceRoot, err := locateWorkspace(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warren doctor: %v\n", err)
			return 1
		}
		report, err := warren.DoctorWorkspace(marmotDir, workspaceRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warren doctor: %v\n", err)
			return 1
		}
		return printDoctorReport(report, *jsonOut, "Workspace warren state looks healthy.")
	}
	*root = resolveWarrenRoot(*root)
	report, err := warren.Doctor(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren doctor: %v\n", err)
		return 1
	}
	return printDoctorReport(report, *jsonOut, fmt.Sprintf("Warren %q manifest looks healthy.", report.WarrenID))
}

// printDoctorReport renders a doctor report (text or JSON) and maps its
// health to the exit code: issues print but only error severity fails.
func printDoctorReport(report warren.DoctorReport, jsonOut bool, healthyMsg string) int {
	if jsonOut {
		if code := printJSON(report); code != 0 {
			return code
		}
		if !report.OK() {
			return 1
		}
		return 0
	}
	if len(report.Issues) > 0 {
		errs, warnings, infos := 0, 0, 0
		for _, issue := range report.Issues {
			fmt.Fprintf(os.Stderr, "%s\t%s\t%s\n", issue.Severity, issue.Code, issue.Message)
			switch issue.Severity {
			case "error":
				errs++
			case "warning":
				warnings++
			default:
				infos++
			}
		}
		fmt.Fprintf(os.Stderr, "doctor: %d error(s), %d warning(s), %d info\n", errs, warnings, infos)
		if !report.OK() {
			return 1
		}
		return 0
	}
	fmt.Println(healthyMsg)
	return 0
}

func warrenFormat(args []string) int {
	fs := flag.NewFlagSet("warren format", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren format [--warren-dir .]")
		return 1
	}
	if _, err := warren.Format(*root); err != nil {
		fmt.Fprintf(os.Stderr, "warren format: %v\n", err)
		return 1
	}
	fmt.Printf("Formatted Warren manifest at %s\n", *root)
	return 0
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func resolveWarrenRoot(root string) string {
	if root != "." {
		return root
	}
	dir, err := os.Getwd()
	if err != nil {
		return root
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, warren.ManifestFileName)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return root
		}
		dir = parent
	}
}

func warrenRegister(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"dir": true}, nil)
	fs := flag.NewFlagSet("warren register", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren register [--dir .marmot] <warren-id> <path>")
		return 1
	}
	if *dir == "" {
		*dir = resolveVaultDir("")
	}
	// ensureWorkspace nudges when it fabricates a vault_id-less config; the
	// pre-existing-config case is nudged below, so track which one this is.
	configExisted := false
	if _, statErr := os.Stat(filepath.Join(*dir, "_config.md")); statErr == nil {
		configExisted = true
	}
	marmotDir, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren register: %v\n", err)
		return 1
	}
	if err := registerWorkspaceWarrenFor(marmotDir, workspaceRoot, fs.Arg(0), fs.Arg(1)); err != nil {
		fmt.Fprintf(os.Stderr, "warren register: %v\n", err)
		return 1
	}
	// Deprecation posture (§5.2): behavior kept identical, stderr-only note —
	// e2e greps pin the stdout strings below.
	fmt.Fprintln(os.Stderr, "note: registering a user-managed checkout is legacy; prefer 'marmot warren add <url>' (shared cache)")
	fmt.Printf("Registered Warren %q -> %s\n", fs.Arg(0), fs.Arg(1))
	// Identity is automatic (derived from vault_id, never mounted); register
	// is the moment it becomes discoverable, so announce every match — and
	// nudge when no identity can ever match because vault_id is unset.
	if local := warren.LocalVaultID(marmotDir); local != "" {
		for _, projectID := range identifiedProjectsByWarren(marmotDir)[fs.Arg(0)] {
			fmt.Printf("note: project %q in warren %q matches this workspace's vault ID %q — served as your live vault; manifest bridges involving it activate once their other endpoint is mounted\n", projectID, fs.Arg(0), local)
		}
	} else if configExisted {
		nudgeMissingVaultID()
	}
	return 0
}

// identifiedProjectsByWarren derives each registered warren's identified
// projects (checkout vault_id matches this workspace's) from the same
// ActiveMounts scan the engine uses, so the CLI can never disagree with the
// engine about who is identified. Best-effort: no vault_id or an unreadable
// state derives nothing.
func identifiedProjectsByWarren(marmotDir string) map[string][]string {
	if warren.LocalVaultID(marmotDir) == "" {
		return nil
	}
	mounts, err := warren.ActiveMounts(marmotDir)
	if err != nil {
		return nil
	}
	var out map[string][]string
	for _, mount := range mounts {
		if !mount.SelfAlias {
			continue
		}
		if out == nil {
			out = make(map[string][]string)
		}
		out[mount.WarrenID] = append(out[mount.WarrenID], mount.ProjectID)
	}
	return out
}

func warrenList(args []string) int {
	fs := flag.NewFlagSet("warren list", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	marmotDir, workspaceRoot, err := locateWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren list: %v\n", err)
		return 1
	}
	state, _, err := loadWorkspaceStateFor(marmotDir, workspaceRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren list: %v\n", err)
		return 1
	}
	// Identity is derived, never stored, so the state passthrough alone
	// cannot show it — graft the computed per-warren identified projects on.
	identified := identifiedProjectsByWarren(marmotDir)
	// Cache-backed warrens (shared cache registry, §5.2) that are not in the
	// workspace state are listed additively, marked [cache]: they resolve
	// register-free for read verbs, so the list must show them.
	cacheOnly := make(map[string]warren.WorkspaceWarren)
	for _, id := range warren.CacheWarrenIDs() {
		if _, ok := state.Warrens[id]; ok {
			continue
		}
		if entry, ok := warren.CacheWorkspaceWarren(id); ok {
			cacheOnly[id] = entry
		}
	}
	if *jsonOut {
		// Same shape as the raw workspace state plus additive per-warren
		// "reachable" (whether the registered checkout still exists),
		// "identified_projects" (vault_id matches this workspace), and
		// "cache" (resolved from the shared cache, not workspace state) fields.
		type listEntry struct {
			warren.WorkspaceWarren
			Reachable          bool     `json:"reachable"`
			IdentifiedProjects []string `json:"identified_projects,omitempty"`
			Cache              bool     `json:"cache,omitempty"`
		}
		out := struct {
			Warrens map[string]listEntry `json:"Warrens"`
		}{Warrens: make(map[string]listEntry, len(state.Warrens)+len(cacheOnly))}
		for id, entry := range state.Warrens {
			out.Warrens[id] = listEntry{WorkspaceWarren: entry, Reachable: dirExistsCLI(entry.Path), IdentifiedProjects: identified[id]}
		}
		for id, entry := range cacheOnly {
			out.Warrens[id] = listEntry{WorkspaceWarren: entry, Reachable: dirExistsCLI(entry.Path), Cache: true}
		}
		return printJSON(out)
	}
	if len(state.Warrens) == 0 && len(cacheOnly) == 0 {
		fmt.Println("No Warrens registered.")
		return 0
	}
	ids := make([]string, 0, len(state.Warrens))
	for id := range state.Warrens {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "WARREN_ID\tPATH\tREACHABLE\tACTIVE\tEDITABLE\tMATERIALIZED\tIDENTITY")
	for _, id := range ids {
		entry := state.Warrens[id]
		identity := "-"
		if len(identified[id]) > 0 {
			identity = strings.Join(identified[id], ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%d\t%d\t%t\t%s\n", id, entry.Path, dirExistsCLI(entry.Path), len(entry.ActiveProjects), len(entry.EditableProjects), entry.Materialized, identity)
	}
	cacheIDs := make([]string, 0, len(cacheOnly))
	for id := range cacheOnly {
		cacheIDs = append(cacheIDs, id)
	}
	sort.Strings(cacheIDs)
	for _, id := range cacheIDs {
		entry := cacheOnly[id]
		fmt.Fprintf(w, "%s [cache]\t%s\t%t\t0\t0\tfalse\t-\n", id, entry.Path, dirExistsCLI(entry.Path))
	}
	_ = w.Flush()
	return 0
}

func dirExistsCLI(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func warrenMount(args []string, isBurrow bool) int {
	name := "mount"
	if isBurrow {
		name = "burrow"
	}
	args = reorderInterspersedFlags(args, map[string]bool{"dir": true, "warren": true}, map[string]bool{"all": true, "drop": true})
	fs := flag.NewFlagSet("warren "+name, flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	all := fs.Bool("all", false, "expand to every project registered in the Warren")
	drop := fs.Bool("drop", false, "delete burrow caches for the named projects instead of mounting (burrow only)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *warrenID == "" {
		fmt.Fprintf(os.Stderr, "warren %s: --warren is required\n", name)
		return 1
	}
	if *drop && !isBurrow {
		fmt.Fprintln(os.Stderr, "warren mount: --drop is only valid with 'marmot warren burrow'")
		return 1
	}
	if len(fs.Args()) > 0 && *all {
		fmt.Fprintf(os.Stderr, "warren %s: cannot combine --all with explicit project IDs\n", name)
		return 1
	}
	if *drop {
		return warrenBurrowDrop(*dir, *warrenID, *all, fs.Args())
	}
	// Burrow's whole point is the materialized cache; without one the verb
	// would be exactly `mount`. Materialization is what distinguishes the
	// two verbs — there is no flag for it.
	materialize := isBurrow
	marmotDir, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren %s: %v\n", name, err)
		return 1
	}
	state, _, err := loadWorkspaceStateFor(marmotDir, workspaceRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren %s: %v\n", name, err)
		return 1
	}
	entry, ok := state.Warrens[*warrenID]
	if !ok {
		// Cache-backed fallback (§5.2): a warren added with 'warren add' is
		// mountable without a prior 'warren register' — record the shared
		// checkout into the workspace state (mount state must reference a
		// registered path) and proceed.
		if cacheEntry, cached := warren.CacheWorkspaceWarren(*warrenID); cached {
			if err := registerWorkspaceWarrenFor(marmotDir, workspaceRoot, *warrenID, cacheEntry.Path); err != nil {
				fmt.Fprintf(os.Stderr, "warren %s: %v\n", name, err)
				return 1
			}
			fmt.Fprintf(os.Stderr, "note: warren %q resolved from the shared cache; recorded checkout %s in this workspace\n", *warrenID, cacheEntry.Path)
			entry, ok = cacheEntry, true
		}
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "warren %s: Warren %q is not registered\n", name, *warrenID)
		return 1
	}
	manifest, _, err := warren.LoadManifest(entry.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren %s: %v\n", name, err)
		return 1
	}
	projects := fs.Args()
	if len(projects) == 0 {
		// Nothing becomes queryable by accident: expanding to every manifest
		// project requires an explicit --all.
		if !*all {
			fmt.Fprintf(os.Stderr, "warren %s: specify project IDs or --all (%d project(s) registered in warren %q)\n", name, len(manifest.Projects), *warrenID)
			return 1
		}
		for _, project := range manifest.Projects {
			projects = append(projects, project.ProjectID)
		}
	}
	if len(projects) == 0 {
		fmt.Fprintf(os.Stderr, "warren %s: no projects to mount\n", name)
		return 1
	}
	previouslyActive := make(map[string]bool, len(entry.ActiveProjects))
	for _, id := range entry.ActiveProjects {
		previouslyActive[id] = true
	}
	projectMap := make(map[string]warren.Project, len(manifest.Projects))
	for _, project := range manifest.Projects {
		projectMap[project.ProjectID] = project
	}
	if err := mountWarrenFor(marmotDir, workspaceRoot, *warrenID, projects, materialize); err != nil {
		fmt.Fprintf(os.Stderr, "warren %s: %v\n", name, err)
		return 1
	}
	// D5.1 (workspace side): a mounted project indexed with a different
	// embedding model never matches this workspace's query vectors, so
	// cross-vault semantic search silently returns nothing. Warn at mount
	// time; mounting stays legal (the user may be about to re-index).
	warnModelSkewOnMount(marmotDir, entry.Path, projects, projectMap)
	if materialize {
		sourceCommit := warrenHeadCommit(entry.Path)
		for i, id := range projects {
			project, ok := projectMap[id]
			var err error
			if !ok {
				err = fmt.Errorf("project %q is not registered in Warren %q", id, *warrenID)
			} else {
				_, err = warren.Materialize(marmotDir, *warrenID, project, entry.Path, sourceCommit)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "warren %s: materialize %s: %v\n", name, id, err)
				// Roll back what this command mounted but never cached, so a
				// mid-loop failure cannot leave mounted-but-uncached projects.
				// Projects that were already mounted before this command stay
				// mounted; projects materialized before the failure stay too.
				var rollback []string
				for _, rest := range projects[i:] {
					if !previouslyActive[rest] {
						rollback = append(rollback, rest)
					}
				}
				if len(rollback) > 0 {
					if unmountErr := unmountWarrenFor(marmotDir, workspaceRoot, *warrenID, rollback); unmountErr != nil {
						fmt.Fprintf(os.Stderr, "warren %s: rollback unmount failed: %v\n", name, unmountErr)
					} else {
						fmt.Fprintf(os.Stderr, "warren %s: unmounted not-yet-cached project(s): %s\n", name, strings.Join(rollback, ", "))
					}
				}
				if i > 0 {
					fmt.Fprintf(os.Stderr, "warren %s: %d project(s) cached before the failure stay mounted: %s\n", name, i, strings.Join(projects[:i], ", "))
				}
				// Mount set the warren-level Materialized flag before any
				// cache existed; if the failure left zero caches, clear it —
				// a stale flag suppresses the A6 "mounts skipped" warning and
				// no drop verb would ever reset it (nothing to drop).
				if syncErr := warren.ClearStaleMaterialized(marmotDir, workspaceRoot, *warrenID); syncErr != nil {
					fmt.Fprintf(os.Stderr, "warren %s: clear materialized flag: %v\n", name, syncErr)
				}
				return 1
			}
		}
	}
	fmt.Printf("Mounted %d project(s) from Warren %q\n", len(projects), *warrenID)
	return 0
}

// warnModelSkewOnMount prints a stderr warning for each mounted project
// whose stored embedding models do not include the workspace's configured
// one. Purely advisory: every failure (no config, no DB, unreadable DB)
// stays silent, because absence of evidence is not skew.
func warnModelSkewOnMount(marmotDir, warrenRoot string, projects []string, projectMap map[string]warren.Project) {
	cfg, err := config.Load(marmotDir)
	if err != nil || cfg.EmbeddingModel == "" {
		return
	}
	for _, id := range projects {
		project, ok := projectMap[id]
		if !ok {
			continue
		}
		dbPath := filepath.Join(warrenRoot, filepath.FromSlash(project.Path), ".marmot-data", "embeddings.db")
		models := storedEmbeddingModels(dbPath)
		if len(models) == 0 {
			continue
		}
		matched := false
		for _, model := range models {
			if model == cfg.EmbeddingModel {
				matched = true
				break
			}
		}
		if !matched {
			fmt.Fprintf(os.Stderr, "warning: project %q embeddings use model(s) %s but this workspace is configured for %q; cross-vault semantic search will return no results until they match (re-index the project or reconfigure)\n", id, strings.Join(models, ","), cfg.EmbeddingModel)
		}
	}
}

// storedEmbeddingModels reads the distinct models of an embeddings DB
// strictly read-only, returning nil on any failure (advisory callers only).
func storedEmbeddingModels(dbPath string) []string {
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	store, err := embedding.NewStoreReadOnly(dbPath)
	if err != nil {
		return nil
	}
	defer func() { _ = store.Close() }()
	models, err := store.Models()
	if err != nil {
		return nil
	}
	return models
}

// warrenBurrowDrop implements `marmot warren burrow --drop`: it deletes
// burrow caches (before the state write, so live observers reload against
// the final layout) and clears the Materialized flag when the last cache for
// the warren is gone.
func warrenBurrowDrop(dirFlag, warrenID string, all bool, projects []string) int {
	marmotDir, workspaceRoot, err := locateWorkspace(dirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren burrow: %v\n", err)
		return 1
	}
	if len(projects) == 0 {
		if !all {
			fmt.Fprintln(os.Stderr, "warren burrow --drop: specify project IDs or --all")
			return 1
		}
		projects = warren.MaterializedProjects(marmotDir, warrenID)
		if len(projects) == 0 {
			// Recovery verb for a stranded Materialized flag (set by a mount
			// whose materialization then failed): with zero caches on disk
			// the flag must not survive a --drop --all.
			if err := warren.ClearStaleMaterialized(marmotDir, workspaceRoot, warrenID); err != nil {
				fmt.Fprintf(os.Stderr, "warren burrow: %v\n", err)
				return 1
			}
			fmt.Printf("No burrow caches for Warren %q\n", warrenID)
			return 0
		}
	}
	if err := warren.DropMaterialized(marmotDir, workspaceRoot, warrenID, projects); err != nil {
		fmt.Fprintf(os.Stderr, "warren burrow: %v\n", err)
		return 1
	}
	for _, project := range projects {
		fmt.Printf("Dropped burrow cache for %q in Warren %q\n", project, warrenID)
	}
	return 0
}

// warrenUnmount deactivates projects without touching burrow caches, so a
// mount→unmount round-trip is non-destructive and works even when the
// warren checkout has disappeared.
func warrenUnmount(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"dir": true, "warren": true}, map[string]bool{"all": true})
	fs := flag.NewFlagSet("warren unmount", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	all := fs.Bool("all", false, "unmount every active project of the Warren")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *warrenID == "" {
		fmt.Fprintln(os.Stderr, "warren unmount: --warren is required")
		return 1
	}
	if len(fs.Args()) > 0 && *all {
		fmt.Fprintln(os.Stderr, "warren unmount: cannot combine --all with explicit project IDs")
		return 1
	}
	marmotDir, workspaceRoot, err := locateWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren unmount: %v\n", err)
		return 1
	}
	state, _, err := loadWorkspaceStateFor(marmotDir, workspaceRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren unmount: %v\n", err)
		return 1
	}
	entry, ok := state.Warrens[*warrenID]
	if !ok {
		fmt.Fprintf(os.Stderr, "warren unmount: Warren %q is not registered\n", *warrenID)
		return 1
	}
	projects := fs.Args()
	if len(projects) == 0 {
		if !*all {
			fmt.Fprintf(os.Stderr, "warren unmount: specify project IDs or --all (%d active project(s) in warren %q)\n", len(entry.ActiveProjects), *warrenID)
			return 1
		}
		if len(entry.ActiveProjects) == 0 {
			fmt.Printf("No active projects in Warren %q\n", *warrenID)
			return 0
		}
		projects = append([]string(nil), entry.ActiveProjects...)
	}
	if err := unmountWarrenFor(marmotDir, workspaceRoot, *warrenID, projects); err != nil {
		fmt.Fprintf(os.Stderr, "warren unmount: %v\n", err)
		return 1
	}
	for _, project := range projects {
		fmt.Printf("Unmounted %q from Warren %q\n", project, *warrenID)
	}
	if cached := warren.MaterializedProjects(marmotDir, *warrenID); len(cached) > 0 {
		fmt.Printf("Burrow caches kept for %s; run 'marmot warren burrow --drop --warren %s --all' to delete them\n", strings.Join(cached, ", "), *warrenID)
	}
	return 0
}

// warrenUnregister removes a warren from the workspace. Without --force it
// refuses while projects are mounted or burrow caches exist, naming the
// exact commands to run first.
func warrenUnregister(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"dir": true, "warren": true}, map[string]bool{"force": true})
	fs := flag.NewFlagSet("warren unregister", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	force := fs.Bool("force", false, "unregister even with mounted projects or burrow caches (deletes the caches)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *warrenID == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren unregister --warren <id> [--force]")
		return 1
	}
	marmotDir, workspaceRoot, err := locateWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren unregister: %v\n", err)
		return 1
	}
	if err := warren.Unregister(marmotDir, workspaceRoot, *warrenID, *force); err != nil {
		fmt.Fprintf(os.Stderr, "warren unregister: %v\n", err)
		return 1
	}
	fmt.Printf("Unregistered Warren %q\n", *warrenID)
	return 0
}

func warrenStatus(args []string) int {
	fs := flag.NewFlagSet("warren status", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	marmotDir, workspaceRoot, err := locateWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren status: %v\n", err)
		return 1
	}
	id, entry, err := resolveWarrenEntry(marmotDir, workspaceRoot, *warrenID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren status: %v\n", err)
		return 1
	}
	if !dirExistsCLI(entry.Path) {
		fmt.Fprintf(os.Stderr, "warren %q UNREACHABLE at %s — re-run 'marmot warren register %s <path>' or 'marmot warren unregister --warren %s'\n", id, entry.Path, id, id)
	}
	statuses, err := warrenStatusesFor(marmotDir, workspaceRoot, id)
	if err != nil {
		// Cache-backed fallback: the warren is not in the workspace state but
		// resolveWarrenEntry synthesized it from the shared cache — derive
		// statuses from the shared checkout (no mounts, purely manifest rows).
		if cacheEntry, cached := warren.CacheWorkspaceWarren(id); cached {
			statuses, err = warren.CacheStatuses(marmotDir, id, cacheEntry)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "warren status: %v\n", err)
			return 1
		}
	}
	if *jsonOut {
		return printJSON(statuses)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSTATE\tEDITABLE\tAVAILABLE\tPATH")
	for _, status := range statuses {
		state := "dormant"
		switch {
		case status.SelfAlias:
			// Identified with this workspace: served as the live vault, no
			// mount needed (or honored).
			state = "identity"
		case status.Active:
			state = "mounted"
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%t\t%s\n", status.ProjectID, state, status.Editable, status.Available, status.Path)
	}
	_ = w.Flush()
	for _, status := range statuses {
		if status.Materialized {
			fmt.Println(burrowCacheLine(marmotDir, id, entry.Path, status.ProjectID))
		}
	}
	return 0
}

// burrowCacheLine renders a materialized project's D2 provenance: pinned
// commit plus behind-count when git can compute one, otherwise the
// materialized date, otherwise a stale note. Every failure degrades one
// step — status must keep working without git or provenance.
func burrowCacheLine(marmotDir, warrenID, warrenPath, projectID string) string {
	prov, err := warren.LoadBurrowProvenance(marmotDir, warrenID, projectID)
	if err != nil {
		return fmt.Sprintf("burrow cache for %q: no provenance recorded (treated as stale by 'marmot warren refresh --pull')", projectID)
	}
	if prov.SourceCommit != "" {
		if behind, gitErr := gitOutput(warrenPath, "rev-list", "--count", prov.SourceCommit+"..HEAD"); gitErr == nil {
			return fmt.Sprintf("burrow cache for %q: cache at %s (%s behind)", projectID, shortCommit(prov.SourceCommit), behind)
		}
	}
	return fmt.Sprintf("burrow cache for %q: cache from %s", projectID, prov.MaterializedAt)
}

func warrenEdit(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"dir": true, "warren": true}, map[string]bool{"off": true})
	fs := flag.NewFlagSet("warren edit", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	off := fs.Bool("off", false, "turn editability off for the project")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *warrenID == "" || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren edit [--off] --warren <id> <project-id>")
		return 1
	}
	marmotDir, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren edit: %v\n", err)
		return 1
	}
	// Edit implies mount; make the auto-mount audible instead of silent.
	wasActive := false
	if state, _, loadErr := loadWorkspaceStateFor(marmotDir, workspaceRoot); loadErr == nil {
		if entry, ok := state.Warrens[*warrenID]; ok {
			for _, active := range entry.ActiveProjects {
				if active == fs.Arg(0) {
					wasActive = true
					break
				}
			}
		} else if cacheEntry, cached := warren.CacheWorkspaceWarren(*warrenID); cached {
			// Cache-backed fallback (§5.2): like mount, edit records into the
			// workspace state, so the shared checkout is registered first.
			if err := registerWorkspaceWarrenFor(marmotDir, workspaceRoot, *warrenID, cacheEntry.Path); err != nil {
				fmt.Fprintf(os.Stderr, "warren edit: %v\n", err)
				return 1
			}
			fmt.Fprintf(os.Stderr, "note: warren %q resolved from the shared cache; recorded checkout %s in this workspace\n", *warrenID, cacheEntry.Path)
		}
	}
	if err := setEditableWarrenFor(marmotDir, workspaceRoot, *warrenID, fs.Arg(0), !*off); err != nil {
		fmt.Fprintf(os.Stderr, "warren edit: %v\n", err)
		return 1
	}
	switch {
	case *off:
		fmt.Printf("Project %q in Warren %q is read-only\n", fs.Arg(0), *warrenID)
	case wasActive:
		fmt.Printf("Project %q in Warren %q is editable in this workspace\n", fs.Arg(0), *warrenID)
	default:
		fmt.Printf("Project %q in Warren %q is editable in this workspace (also mounted — edit implies mount)\n", fs.Arg(0), *warrenID)
	}
	return 0
}

// warrenRefresh reloads warren state from disk for live observers: it
// touches the workspace _warren.md (atomic no-op rewrite under the state
// flock) so a live daemon owner's watcher fires and calls ReloadWarrenState,
// then reports the active mounts. With --pull it first fast-forwards the
// warren's git checkout (refusing on a dirty tree — editable-mount edits
// live there and must never be stashed away) and re-materializes burrow
// caches whose pinned provenance commit no longer matches HEAD.
func warrenRefresh(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"dir": true, "warren": true}, map[string]bool{"pull": true})
	fs := flag.NewFlagSet("warren refresh", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	pull := fs.Bool("pull", false, "git pull --ff-only the warren checkout and re-materialize stale burrow caches first")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	marmotDir, workspaceRoot, err := locateWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: %v\n", err)
		return 1
	}
	id, entry, err := resolveWarrenEntry(marmotDir, workspaceRoot, *warrenID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: %v\n", err)
		return 1
	}
	if fi, statErr := os.Stat(entry.Path); statErr != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "warren refresh: warren %q is UNREACHABLE at %s — re-run 'marmot warren register %s <path>' with the current checkout location\n", id, entry.Path, id)
		return 1
	}
	if *pull {
		// Cache-backed warrens are synced via the bare mirror; --pull against
		// the shared detached checkout is the legacy path. Hint, never error
		// (§5.2 deprecation posture).
		if _, cached := warren.CacheWorkspaceWarren(id); cached {
			fmt.Fprintf(os.Stderr, "note: warren %q is cache-backed; prefer 'marmot warren sync %s' (refresh --pull targets user-managed checkouts)\n", id, id)
		}
		if code := warrenRefreshPull(marmotDir, id, entry); code != 0 {
			return code
		}
	}
	// Signal live observers (daemon owners, API watchers) via the file they
	// already watch.
	if err := touchWorkspaceStateFor(marmotDir, workspaceRoot); err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: %v\n", err)
		return 1
	}
	mounts, err := warren.ActiveMounts(marmotDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: %v\n", err)
		return 1
	}
	count := 0
	for _, mount := range mounts {
		if mount.WarrenID == id {
			count++
		}
	}
	fmt.Printf("Warren %q refreshed: %d active project mount(s)\n", id, count)
	if info, alive := ownerAlive(marmotDir); alive {
		fmt.Printf("Live daemon owner (pid %d) will pick up the change within ~1s\n", info.PID)
	}
	if !*pull {
		fmt.Println("Note: refresh reloads local warren state; add --pull (or run 'git -C " + entry.Path + " pull' first) to fetch upstream warren changes.")
	}
	return 0
}

// warrenRefreshPull is refresh's --pull leg (D1): fast-forward the checkout
// and re-materialize stale burrow caches. It never merges, rebases, resets,
// or stashes on the user's behalf — a dirty or diverged checkout is the
// user's to resolve, loudly.
func warrenRefreshPull(marmotDir, id string, entry warren.WorkspaceWarren) int {
	if _, err := gitOutput(entry.Path, "rev-parse", "--is-inside-work-tree"); err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: warren %q at %s is not a git checkout; --pull requires git (run 'marmot warren refresh' without --pull to reload state only)\n", id, entry.Path)
		return 1
	}
	// Refuse a dirty checkout instead of stashing: editable mounts
	// legitimately write into the checkout (that IS the edit feature), so
	// auto-stash or checkout --force would destroy user work.
	if porcelain, err := gitOutput(entry.Path, "status", "--porcelain"); err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: %v\n", err)
		return 1
	} else if porcelain != "" {
		dirty := len(strings.Split(porcelain, "\n"))
		fmt.Fprintf(os.Stderr, "warren refresh: warren checkout has %d uncommitted change(s) (editable-mount edits?); commit or stash them, or run refresh without --pull\n", dirty)
		return 1
	}
	oldHead := warrenHeadCommit(entry.Path)
	if _, err := gitOutput(entry.Path, "pull", "--ff-only"); err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: %v\nresolve in the checkout manually, then re-run refresh\n", err)
		return 1
	}
	newHead := warrenHeadCommit(entry.Path)
	if oldHead == newHead {
		fmt.Printf("Warren %q checkout already up to date at %s\n", id, shortCommit(newHead))
	} else {
		fmt.Printf("Warren %q checkout pulled: %s -> %s\n", id, shortCommit(oldHead), shortCommit(newHead))
	}
	// Re-materialize stale burrow caches: any active project whose cache
	// provenance is missing, unreadable, or pinned to a different commit.
	manifest, _, err := warren.LoadManifest(entry.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: %v\n", err)
		return 1
	}
	projectMap := make(map[string]warren.Project, len(manifest.Projects))
	for _, project := range manifest.Projects {
		projectMap[project.ProjectID] = project
	}
	cached := make(map[string]bool)
	for _, projectID := range warren.MaterializedProjects(marmotDir, id) {
		cached[projectID] = true
	}
	var refreshed []string
	for _, projectID := range entry.ActiveProjects {
		if !cached[projectID] {
			continue
		}
		prov, provErr := warren.LoadBurrowProvenance(marmotDir, id, projectID)
		if provErr == nil && prov.SourceCommit != "" && prov.SourceCommit == newHead {
			continue // cache already pinned to the fresh HEAD
		}
		project, ok := projectMap[projectID]
		if !ok {
			fmt.Fprintf(os.Stderr, "warren refresh: warning: burrowed project %q is no longer in the warren manifest; cache left as-is (drop it with 'marmot warren burrow --drop --warren %s %s')\n", projectID, id, projectID)
			continue
		}
		// Legacy self cache (pre-alias state: self-mount + burrow cache):
		// Materialize now refuses self-alias projects, and a hard fail here
		// would brick the whole refresh --pull over inert legacy state — skip
		// with the drop hint instead (doctor owns the durable diagnostic).
		if local := warren.LocalVaultID(marmotDir); local != "" {
			checkoutDir := filepath.Join(entry.Path, filepath.FromSlash(project.Path))
			vaultID := project.ProjectID
			if meta, _, metaErr := warren.LoadProjectMetadata(checkoutDir); metaErr == nil && meta != nil && meta.VaultID != "" {
				vaultID = meta.VaultID
			}
			if vaultID == local {
				fmt.Fprintf(os.Stderr, "warren refresh: warning: burrow cache for %q shadows this workspace's own vault; skipping re-materialize — drop it with 'marmot warren burrow --drop --warren %s %s'\n", projectID, id, projectID)
				continue
			}
		}
		if _, err := warren.Materialize(marmotDir, id, project, entry.Path, newHead); err != nil {
			fmt.Fprintf(os.Stderr, "warren refresh: re-materialize %s: %v\n", projectID, err)
			return 1
		}
		refreshed = append(refreshed, projectID)
	}
	if len(refreshed) > 0 {
		fmt.Printf("Re-materialized burrow cache(s): %s\n", strings.Join(refreshed, ", "))
	}
	return 0
}

// jsonProposeEnvelope is the schema:1 stdout contract for
// `warren propose --json` (consumed by stave's runJSON; additive-only
// evolution — never remove or repurpose a field).
type jsonProposeEnvelope struct {
	Schema           int      `json:"schema"`
	WarrenID         string   `json:"warren_id"`
	ProjectID        string   `json:"project_id"`
	Branch           string   `json:"branch"`
	Commit           string   `json:"commit"`
	Committed        bool     `json:"committed"`
	NothingToPropose bool     `json:"nothing_to_propose"`
	PushCommand      string   `json:"push_command"`
	Warnings         []string `json:"warnings"`
}

// proposeExcludePathspecs keeps embeddings DB sidecars (embeddings.db and
// its WAL/SHM files under any .marmot-data dir) out of both the dirty-scope
// check and the proposal commit: derived binary state never belongs in a
// warren PR. Both forms are listed so the directory itself (as git reports
// untracked dirs) and files inside it are matched. Excludes MUST precede the
// positive pathspec: `git add` (observed on git 2.50) stages nothing when an
// exclude pathspec follows the positive one, but behaves correctly with the
// excludes first — pathspec matching itself is documented as order-independent,
// so this ordering is safe everywhere else too.
var proposeExcludePathspecs = []string{
	":(exclude,glob)**/.marmot-data",
	":(exclude,glob)**/.marmot-data/**",
}

// warrenPropose packages editable-mount edits into a reviewable git
// artifact (D3): a local branch holding one pathspec-limited commit of the
// project's changes. It is local-only by design — marmot never pulls,
// merges, rebases, force-pushes, or pushes; divergence from upstream is
// discovered and resolved by humans at push/PR time through normal git
// flow. Concurrent proposes are serialized by git's own index lock plus the
// branch-exists refusal.
//
// With --json every outcome is a schema:1 envelope on stdout: success and
// nothing-to-propose both exit 0 (a clean tree is not a failure for the
// stave flow), every refusal exits 1 with an error envelope. Human output
// is unchanged when --json is absent, with one deliberate exception (OQ7):
// a project whose only changes are embeddings DB sidecars under
// .marmot-data used to produce a proposal and now reports
// nothing-to-propose — derived binary state never belongs in a warren PR.
func warrenPropose(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"dir": true, "warren": true}, map[string]bool{"json": true})
	fs := flag.NewFlagSet("warren propose", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	jsonOut := fs.Bool("json", false, "print a schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		// *jsonOut is unusable when parsing itself failed; honor the contract
		// by scanning the raw args for the flag.
		if proposeJSONRequested(args) {
			return denJSONError("invalid_args", err.Error(), "usage: marmot warren propose [--warren <id>] [--json] [<project-id>]")
		}
		return 1
	}
	if fs.NArg() > 1 {
		if *jsonOut {
			return denJSONError("invalid_args", "too many arguments", "usage: marmot warren propose [--warren <id>] [--json] [<project-id>]")
		}
		fmt.Fprintln(os.Stderr, "usage: marmot warren propose [--warren <id>] [<project-id>]")
		return 1
	}
	marmotDir, workspaceRoot, err := locateWorkspace(*dir)
	if err != nil {
		if *jsonOut {
			return denJSONError("workspace_not_found", err.Error(), "run a mutating warren command, or marmot init, to create a workspace")
		}
		fmt.Fprintf(os.Stderr, "warren propose: %v\n", err)
		return 1
	}
	id, entry, err := resolveWarrenEntry(marmotDir, workspaceRoot, *warrenID)
	if err != nil {
		if *jsonOut {
			return denJSONError("warren_unreachable", err.Error(), "register the warren with 'marmot warren register <id> <path>' or pass --warren")
		}
		fmt.Fprintf(os.Stderr, "warren propose: %v\n", err)
		return 1
	}
	if !dirExistsCLI(entry.Path) {
		if *jsonOut {
			return denJSONError("warren_unreachable", fmt.Sprintf("warren %q is UNREACHABLE at %s", id, entry.Path), fmt.Sprintf("re-run 'marmot warren register %s <path>' with the current checkout location", id))
		}
		fmt.Fprintf(os.Stderr, "warren propose: warren %q is UNREACHABLE at %s — re-run 'marmot warren register %s <path>' with the current checkout location\n", id, entry.Path, id)
		return 1
	}
	if _, err := gitOutput(entry.Path, "rev-parse", "--is-inside-work-tree"); err != nil {
		if *jsonOut {
			return denJSONError("propose_failed", fmt.Sprintf("warren %q at %s is not a git checkout; propose creates a git branch and needs one", id, entry.Path), "")
		}
		fmt.Fprintf(os.Stderr, "warren propose: warren %q at %s is not a git checkout; propose creates a git branch and needs one\n", id, entry.Path)
		return 1
	}
	// Propose must be able to return to a branch afterwards.
	prevBranch, err := gitOutput(entry.Path, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		if *jsonOut {
			return denJSONError("detached_head", fmt.Sprintf("warren checkout at %s is on a detached HEAD", entry.Path), "check out a branch first (propose needs a branch to return to)")
		}
		fmt.Fprintf(os.Stderr, "warren propose: warren checkout at %s is on a detached HEAD; check out a branch first (propose needs a branch to return to)\n", entry.Path)
		return 1
	}
	// Project selection: explicit argument, else the sole editable project.
	projectID := ""
	switch {
	case fs.NArg() == 1:
		projectID = fs.Arg(0)
	case len(entry.EditableProjects) == 1:
		projectID = entry.EditableProjects[0]
	case len(entry.EditableProjects) > 1:
		if *jsonOut {
			return denJSONError("invalid_args", fmt.Sprintf("warren %q has %d editable projects (%s)", id, len(entry.EditableProjects), strings.Join(entry.EditableProjects, ", ")), "name the one project to propose")
		}
		fmt.Fprintf(os.Stderr, "warren propose: warren %q has %d editable projects (%s); name the one to propose\n", id, len(entry.EditableProjects), strings.Join(entry.EditableProjects, ", "))
		return 1
	default:
		if *jsonOut {
			return denJSONError("invalid_args", fmt.Sprintf("no editable projects in warren %q", id), fmt.Sprintf("name a project explicitly, or enable editing with 'marmot warren edit --warren %s <project-id>' first", id))
		}
		fmt.Fprintf(os.Stderr, "warren propose: no editable projects in warren %q; name a project explicitly, or enable editing with 'marmot warren edit --warren %s <project-id>' first\n", id, id)
		return 1
	}
	manifest, _, err := warren.LoadManifest(entry.Path)
	if err != nil {
		if *jsonOut {
			return denJSONError("propose_failed", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "warren propose: %v\n", err)
		return 1
	}
	var project warren.Project
	found := false
	for _, candidate := range manifest.Projects {
		if candidate.ProjectID == projectID {
			project, found = candidate, true
			break
		}
	}
	if !found {
		if *jsonOut {
			return denJSONError("invalid_args", fmt.Sprintf("project %q is not registered in warren %q", projectID, id), "")
		}
		fmt.Fprintf(os.Stderr, "warren propose: project %q is not registered in warren %q\n", projectID, id)
		return 1
	}
	// An identified project's edits live in the workspace vault and never
	// touch the checkout — a pathspec-limited commit of projects/<pid>/ would
	// be meaningless. Default selection can never pick one (identified
	// projects are never editable); only an explicit argument reaches this.
	if local := warren.LocalVaultID(marmotDir); local != "" {
		checkoutDir := filepath.Join(entry.Path, filepath.FromSlash(project.Path))
		vaultID := project.ProjectID
		if meta, _, metaErr := warren.LoadProjectMetadata(checkoutDir); metaErr == nil && meta != nil && meta.VaultID != "" {
			vaultID = meta.VaultID
		}
		if vaultID == local {
			if *jsonOut {
				return denJSONError("invalid_args", fmt.Sprintf("project %q is this workspace (vault ID %q); its live context never lands in the warren checkout", projectID, vaultID), "refresh the warren's copy in the warren repo (project remove + project import) and commit there")
			}
			fmt.Fprintf(os.Stderr, "warren propose: project %q is this workspace (vault ID %q); its live context never lands in the warren checkout — refresh the warren's copy in the warren repo (project remove + project import) and commit there\n", projectID, vaultID)
			return 1
		}
	}
	// Scope check, pathspec-limited: only changes under the project count,
	// and embeddings DB sidecars never count (same excludes as the commit,
	// so a DB-only dirty tree is nothing-to-propose, not an empty commit).
	statusArgs := append(append([]string{"status", "--porcelain", "--"}, proposeExcludePathspecs...), project.Path)
	porcelain, err := gitOutput(entry.Path, statusArgs...)
	if err != nil {
		if *jsonOut {
			return denJSONError("propose_failed", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "warren propose: %v\n", err)
		return 1
	}
	if porcelain == "" {
		if *jsonOut {
			return printDenJSON(jsonProposeEnvelope{
				Schema:           1,
				WarrenID:         id,
				ProjectID:        projectID,
				NothingToPropose: true,
				Warnings:         []string{},
			})
		}
		fmt.Printf("nothing to propose for %q (no changes under %s)\n", projectID, project.Path)
		return 0
	}
	branch := fmt.Sprintf("marmot/propose/%s-%s", projectID, time.Now().Format("20060102-150405"))
	// Timestamped names make an existing branch near-impossible; the check
	// is belt-and-braces so we never move a branch the user already had.
	if _, err := gitOutput(entry.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if *jsonOut {
			return denJSONError("propose_failed", fmt.Sprintf("branch %q already exists in %s", branch, entry.Path), "re-run to get a fresh timestamp")
		}
		fmt.Fprintf(os.Stderr, "warren propose: branch %q already exists in %s; re-run to get a fresh timestamp\n", branch, entry.Path)
		return 1
	}
	if _, err := gitOutput(entry.Path, "checkout", "-b", branch); err != nil {
		if *jsonOut {
			return denJSONError("propose_failed", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "warren propose: %v\n", err)
		return 1
	}
	// From here on any failure tries to return to the previous branch and
	// reports the exact repo state — never delete or reset anything.
	// Pathspec-limited add/commit: unrelated files the user had staged stay
	// staged and out of the proposal, and DB sidecars stay in the worktree.
	proposePathspec := append(append([]string{"--"}, proposeExcludePathspecs...), project.Path)
	steps := [][]string{
		append([]string{"add"}, proposePathspec...),
		append([]string{"commit", "-m", fmt.Sprintf("marmot propose: %s context updates", projectID)}, proposePathspec...),
		{"checkout", prevBranch},
	}
	commitSHA := ""
	for _, step := range steps {
		if _, err := gitOutput(entry.Path, step...); err != nil {
			if *jsonOut {
				_, _ = gitOutput(entry.Path, "checkout", prevBranch)
				currentBranch, _ := gitOutput(entry.Path, "rev-parse", "--abbrev-ref", "HEAD")
				staged, _ := gitOutput(entry.Path, "diff", "--cached", "--name-only")
				return denJSONError("propose_failed", err.Error(), fmt.Sprintf("repo state left untouched otherwise — current branch %q, staged files: %s", currentBranch, staged))
			}
			fmt.Fprintf(os.Stderr, "warren propose: %v\n", err)
			_, _ = gitOutput(entry.Path, "checkout", prevBranch)
			currentBranch, _ := gitOutput(entry.Path, "rev-parse", "--abbrev-ref", "HEAD")
			staged, _ := gitOutput(entry.Path, "diff", "--cached", "--name-only")
			fmt.Fprintf(os.Stderr, "warren propose: repo state left untouched otherwise — current branch %q, staged files: %s\n", currentBranch, staged)
			return 1
		}
		if step[0] == "commit" {
			// Best-effort sha capture while the proposal branch is still
			// checked out; the contract allows commit to be empty.
			commitSHA, _ = gitOutput(entry.Path, "rev-parse", "HEAD")
		}
	}
	if *jsonOut {
		return printDenJSON(jsonProposeEnvelope{
			Schema:      1,
			WarrenID:    id,
			ProjectID:   projectID,
			Branch:      branch,
			Commit:      commitSHA,
			Committed:   true,
			PushCommand: fmt.Sprintf("git -C %s push -u origin %s", shellQuoteArg(entry.Path), branch),
			Warnings:    []string{},
		})
	}
	fmt.Printf("Created branch %q with the %s context updates (back on %q).\n", branch, projectID, prevBranch)
	fmt.Printf("Publish it with:\n  git -C %s push -u origin %s\nthen open a pull request in the warren repository. marmot never pushes for you.\n", shellQuoteArg(entry.Path), branch)
	return 0
}

// proposeJSONRequested reports whether the raw args ask for --json; used only
// on the flag-parse-failure path, where the parsed flag value is unavailable
// but the stdout contract still requires an error envelope. It mirrors the
// flag package's own boolean handling: bare --json/-json means true, and
// --json=VALUE forms go through strconv.ParseBool (so --json=TRUE and
// --json=1 count), with unparseable or false values ignored. Args after a
// bare "--" terminator are positional and never flags.
func proposeJSONRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if name != "--json" && name != "-json" {
			continue
		}
		if !hasValue {
			return true
		}
		if v, err := strconv.ParseBool(value); err == nil && v {
			return true
		}
	}
	return false
}

// locateWorkspace resolves the marmot dir for read-only (or purely
// state-mutating) warren verbs without fabricating a workspace: unlike
// ensureWorkspace it never MkdirAll's .marmot or writes a mock-provider
// _config.md, so `warren list` in a random directory errors instead of
// planting a vault there.
//
// The returned workspaceRoot is only meaningful for .marmot-named dirs
// (state at <workspaceRoot>/.marmot/_warren.md == <marmotDir>/_warren.md);
// for direct-state marmot dirs (see directStateMarmotDir) callers must
// route state access through marmotDir — use loadWorkspaceStateFor and
// friends rather than the workspaceRoot-based warren APIs.
func locateWorkspace(dirFlag string) (marmotDir, workspaceRoot string, err error) {
	if dirFlag == "" {
		dirFlag = resolveVaultDir("")
	}
	if fi, statErr := os.Stat(dirFlag); statErr != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("no marmot workspace at %s (run a mutating warren command, or marmot init, to create one)", dirFlag)
	}
	return dirFlag, filepath.Dir(dirFlag), nil
}

func ensureWorkspace(dirFlag string) (marmotDir, workspaceRoot string, err error) {
	if dirFlag == "" {
		dirFlag = resolveVaultDir("")
	}
	marmotDir = dirFlag
	workspaceRoot = filepath.Dir(marmotDir)
	if err := os.MkdirAll(filepath.Join(marmotDir, ".marmot-data"), 0o755); err != nil {
		return "", "", err
	}
	configPath := filepath.Join(marmotDir, "_config.md")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		content := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\nembedding_model: \"\"\n---\n# ContextMarmot Warren Workspace\n"
		if writeErr := os.WriteFile(configPath, []byte(content), 0o644); writeErr != nil {
			return "", "", writeErr
		}
		// The fabricated config carries no vault_id, which silently opts the
		// workspace out of warren identity — say so once, at fabrication time.
		nudgeMissingVaultID()
	}
	return marmotDir, workspaceRoot, nil
}

// directStateMarmotDir reports whether marmotDir keeps its warren workspace
// state directly inside itself (<marmotDir>/_warren.md) instead of resolving
// through a parent workspace root's .marmot/. Den identity vaults
// (~/.marmot/dens/<id>/vault, what resolveDenID returns) are the canonical
// case: they carry the vault identity marker _config.md but are not named
// .marmot, so <workspaceRoot>/.marmot-style resolution can never address
// them — warren.ActiveMounts (what den serves read) looks at
// <vault>/_warren.md. Detection is deliberately conservative: only a dir
// that itself contains _config.md qualifies (a workspace root never does,
// its config lives in .marmot/), and .marmot-named dirs are excluded so
// classic workspaces keep resolving through their root unchanged.
func directStateMarmotDir(marmotDir string) bool {
	if marmotDir == "" || filepath.Base(marmotDir) == warren.MarmotDirName {
		return false
	}
	fi, err := os.Stat(filepath.Join(marmotDir, "_config.md"))
	return err == nil && fi.Mode().IsRegular()
}

// loadWorkspaceStateFor reads the warren workspace state from wherever it
// actually lives for this workspace shape: directly in a direct-state marmot
// dir (den vaults), or under <workspaceRoot>/.marmot for classic workspaces.
// Both paths are identical for .marmot-named dirs.
func loadWorkspaceStateFor(marmotDir, workspaceRoot string) (*warren.WorkspaceState, string, error) {
	if directStateMarmotDir(marmotDir) {
		return warren.LoadWorkspaceStateFromMarmot(marmotDir)
	}
	return warren.LoadWorkspaceState(workspaceRoot)
}

// registerWorkspaceWarrenFor registers warrenID -> warrenRoot in the
// workspace state. Classic workspaces delegate to
// warren.RegisterWorkspaceWarren; direct-state marmot dirs mirror it against
// <marmotDir>/_warren.md via warren.UpdateWorkspaceStateInMarmot (the same
// flocked primitive den.EnsureEditableMount uses), so `warren register
// --dir <den-vault>` lands the state where warren.ActiveMounts reads it
// instead of planting <den-root>/.marmot/_warren.md.
func registerWorkspaceWarrenFor(marmotDir, workspaceRoot, warrenID, warrenRoot string) error {
	if !directStateMarmotDir(marmotDir) {
		_, err := warren.RegisterWorkspaceWarren(workspaceRoot, warrenID, warrenRoot)
		return err
	}
	absRoot, err := filepath.Abs(warrenRoot)
	if err != nil {
		return fmt.Errorf("resolve Warren path: %w", err)
	}
	manifest, _, err := warren.LoadManifest(absRoot)
	if err != nil {
		return err
	}
	if manifest.WarrenID != warrenID {
		return fmt.Errorf("warren ID mismatch: manifest has %q, command used %q", manifest.WarrenID, warrenID)
	}
	return warren.UpdateWorkspaceStateInMarmot(marmotDir, func(state *warren.WorkspaceState) (bool, error) {
		if state.Warrens == nil {
			state.Warrens = map[string]warren.WorkspaceWarren{}
		}
		entry := state.Warrens[warrenID]
		entry.Path = absRoot
		state.Warrens[warrenID] = entry
		return true, nil
	})
}

// touchWorkspaceStateFor rewrites the workspace _warren.md unchanged (the
// change signal live daemon owners watch), targeting the same location
// loadWorkspaceStateFor reads.
func touchWorkspaceStateFor(marmotDir, workspaceRoot string) error {
	if !directStateMarmotDir(marmotDir) {
		return warren.TouchWorkspaceState(workspaceRoot)
	}
	return warren.UpdateWorkspaceStateInMarmot(marmotDir, func(*warren.WorkspaceState) (bool, error) {
		return true, nil // unchanged rewrite IS the touch
	})
}

// mountWarrenFor routes Mount to wherever this workspace shape keeps its
// state: <marmotDir>/_warren.md for direct-state marmot dirs (den vaults),
// <workspaceRoot>/.marmot/_warren.md for classic workspaces. The remaining
// *For helpers below do the same for their verbs, so every state-consuming
// warren verb resolves state through one abstraction and `status` can never
// disagree with `list` about what is registered.
func mountWarrenFor(marmotDir, workspaceRoot, warrenID string, projects []string, materialize bool) error {
	if directStateMarmotDir(marmotDir) {
		_, err := warren.MountFromMarmot(marmotDir, warrenID, projects, materialize)
		return err
	}
	_, err := warren.Mount(workspaceRoot, warrenID, projects, materialize)
	return err
}

// unmountWarrenFor routes Unmount like mountWarrenFor.
func unmountWarrenFor(marmotDir, workspaceRoot, warrenID string, projects []string) error {
	if directStateMarmotDir(marmotDir) {
		_, err := warren.UnmountFromMarmot(marmotDir, warrenID, projects)
		return err
	}
	_, err := warren.Unmount(workspaceRoot, warrenID, projects)
	return err
}

// setEditableWarrenFor routes SetEditable like mountWarrenFor.
func setEditableWarrenFor(marmotDir, workspaceRoot, warrenID, projectID string, editable bool) error {
	if directStateMarmotDir(marmotDir) {
		_, err := warren.SetEditableFromMarmot(marmotDir, warrenID, projectID, editable)
		return err
	}
	_, err := warren.SetEditable(workspaceRoot, warrenID, projectID, editable)
	return err
}

// warrenStatusesFor routes Status like mountWarrenFor.
func warrenStatusesFor(marmotDir, workspaceRoot, warrenID string) ([]warren.ProjectStatus, error) {
	if directStateMarmotDir(marmotDir) {
		return warren.StatusFromMarmot(marmotDir, warrenID)
	}
	return warren.Status(workspaceRoot, warrenID)
}

// nudgeMissingVaultID prints the no-vault_id onboarding nudge: without a
// vault_id in _config.md, warren bridges can never identify this workspace's
// own project (identity is derived by vault_id comparison).
func nudgeMissingVaultID() {
	fmt.Fprintln(os.Stderr, "note: this workspace has no vault_id in _config.md; warren bridges involving this project cannot identify it — set one with 'marmot configure --vault-id <id>'")
}

// resolveWarrenEntry resolves the requested (or sole registered) Warren ID
// and returns its workspace state entry alongside, so callers get the
// checkout path without a second state load. An explicitly requested id that
// is absent from the workspace state falls back to the shared warren cache
// (§5.2): a cache-backed warren resolves to its shared read checkout without
// any prior 'warren register', so read verbs (status, refresh) just work.
func resolveWarrenEntry(marmotDir, workspaceRoot, requested string) (string, warren.WorkspaceWarren, error) {
	state, _, err := loadWorkspaceStateFor(marmotDir, workspaceRoot)
	if err != nil {
		return "", warren.WorkspaceWarren{}, err
	}
	if requested != "" {
		entry, ok := state.Warrens[requested]
		if !ok {
			if cacheEntry, cached := warren.CacheWorkspaceWarren(requested); cached {
				return requested, cacheEntry, nil
			}
			return "", warren.WorkspaceWarren{}, fmt.Errorf("warren %q is not registered", requested)
		}
		return requested, entry, nil
	}
	if len(state.Warrens) == 1 {
		for id, entry := range state.Warrens {
			return id, entry, nil
		}
	}
	if len(state.Warrens) == 0 {
		return "", warren.WorkspaceWarren{}, fmt.Errorf("no Warrens registered")
	}
	return "", warren.WorkspaceWarren{}, fmt.Errorf("--warren is required when multiple Warrens are registered")
}

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "json: %v\n", err)
		return 1
	}
	return 0
}
