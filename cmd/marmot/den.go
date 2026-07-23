package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/gitx"
	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
	"github.com/nurozen/context-marmot/internal/warrenreg"
)

// ---------------------------------------------------------------------------
// JSON envelopes (schema: 1) — never marshal internal types directly.
// ---------------------------------------------------------------------------

type jsonErrorEnvelope struct {
	Schema int `json:"schema"`
	Error  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint,omitempty"`
	} `json:"error"`
}

type jsonCreateEnvelope struct {
	Schema         int               `json:"schema"`
	DenID          string            `json:"den_id"`
	DenPath        string            `json:"den_path"`
	VaultID        string            `json:"vault_id"`
	Routes         map[string]string `json:"routes"`
	PointerWritten bool              `json:"pointer_written"`
	Links          []jsonCreateLink  `json:"links"`
	Warnings       []string          `json:"warnings"`
}

type jsonCreateLink struct {
	Ref         string  `json:"ref"`
	Mode        *string `json:"mode"`
	ResolvedVia string  `json:"resolved_via"`
}

type jsonStatusEnvelope struct {
	Schema   int              `json:"schema"`
	DenID    string           `json:"den_id"`
	Lifetime string           `json:"lifetime"`
	VaultID  string           `json:"vault_id"`
	Projects []string         `json:"projects"`
	Links    []jsonStatusLink `json:"links"`
}

type jsonStatusLink struct {
	Ref          string  `json:"ref"`
	Mode         string  `json:"mode"`
	PinnedCommit *string `json:"pinned_commit"`
	Ahead        int     `json:"ahead"`
	Behind       int     `json:"behind"`
	PendingEdits int     `json:"pending_edits"`
	State        string  `json:"state"`
	// SourceCommit (additive, §9 skew disclosure) is the source-repo commit
	// a pinned link's warren project vault was snapshotted from (manifest v3
	// provenance); empty when unknown.
	SourceCommit string `json:"source_commit,omitempty"`
}

type jsonDestroyEnvelope struct {
	Schema        int             `json:"schema"`
	DenID         string          `json:"den_id"`
	Destroyed     bool            `json:"destroyed"`
	Kept          bool            `json:"kept"`
	UnpushedEdits int             `json:"unpushed_edits"`
	Promoted      *jsonFlowCounts `json:"promoted"`
	Contributed   *jsonFlowCounts `json:"contributed"`
	// Warnings carries the same non-fatal notices destroy prints to stderr
	// (unpushed-count degradation, promote warnings, worktree-removal
	// failures). Additive; always an array, never null.
	Warnings []string `json:"warnings"`
}

type jsonFlowCounts struct {
	Added      int `json:"added"`
	Updated    int `json:"updated"`
	Superseded int `json:"superseded"`
	Noop       int `json:"noop"`
}

type jsonDryRunEnvelope struct {
	Schema int      `json:"schema"`
	DryRun bool     `json:"dry_run"`
	Ops    []string `json:"ops"`
}

type jsonListEnvelope struct {
	Schema int      `json:"schema"`
	Dens   []string `json:"dens"`
}

type jsonLinkEnvelope struct {
	Schema int          `json:"schema"`
	DenID  string       `json:"den_id"`
	Link   jsonLinkBody `json:"link"`
	// Worktree and Branch are set (additively) for edit links into a
	// cache-backed warren: the dedicated edit worktree serving this den's
	// writes and the branch it is permanently checked out on. Empty for
	// legacy registered-checkout links.
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// PinnedCommit is set (additively) for mode=link links: the warren
	// commit this link is pinned to (shared-checkout cache pin, or the
	// registered checkout's HEAD for legacy warrens).
	PinnedCommit string   `json:"pinned_commit,omitempty"`
	Warnings     []string `json:"warnings"`
}

// jsonUnlinkEnvelope is the schema:1 success envelope for den unlink
// (testdata/contracts/den_unlink.v1.json).
type jsonUnlinkEnvelope struct {
	Schema   int            `json:"schema"`
	DenID    string         `json:"den_id"`
	Removed  []jsonLinkBody `json:"removed"`
	Warnings []string       `json:"warnings"`
}

type jsonLinkBody struct {
	Target  string `json:"target"`
	Mode    string `json:"mode"`
	Warren  string `json:"warren"`
	Project string `json:"project"`
}

func printDenJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "json: %v\n", err)
		return 1
	}
	return 0
}

func denJSONError(code, message, hint string) int {
	var env jsonErrorEnvelope
	env.Schema = 1
	env.Error.Code = code
	env.Error.Message = message
	env.Error.Hint = hint
	_ = printDenJSON(env)
	return 1
}

// denParseFail honors the --json stdout contract when flag parsing itself
// failed: the parsed *asJSON value is unusable (Parse errored before setting
// it), so scan the raw args for the flag — same convention as warren
// propose's proposeJSONRequested — and emit a schema:1 invalid_args envelope.
func denParseFail(args []string, err error, hint string) int {
	if denJSONRequested(args) {
		return denJSONError("invalid_args", err.Error(), hint)
	}
	return 1
}

// denJSONRequested wraps proposeJSONRequested and additionally accepts every
// `--json=<value>` / `-json=<value>` spelling the flag package itself would
// accept (`--json=TRUE`, `--json=1`, …) via strconv.ParseBool, which the
// exact-string scanner misses.
func denJSONRequested(args []string) bool {
	if proposeJSONRequested(args) {
		return true
	}
	for _, arg := range args {
		for _, prefix := range []string{"--json=", "-json="} {
			if strings.HasPrefix(arg, prefix) {
				if v, err := strconv.ParseBool(arg[len(prefix):]); err == nil && v {
					return true
				}
			}
		}
	}
	return false
}

// isGitRefComponentSafe reports whether s can be embedded as one path
// component of a git ref name (branch marmot/edit/<den>/<project>) without
// tripping git-check-ref-format rules. Conservative allowlist: ASCII
// [A-Za-z0-9._-], no leading '.' or '-', no "..", no trailing '.' and no
// ".lock" suffix.
func isGitRefComponentSafe(s string) bool {
	if s == "" || s[0] == '.' || s[0] == '-' {
		return false
	}
	if strings.Contains(s, "..") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// shellQuoteArg quotes p for POSIX shells when it contains anything outside
// a conservative safe set (single-quote wrapping, '\” escaping), so
// push_command stays copy-pasteable for checkouts with spaces or specials.
func shellQuoteArg(p string) string {
	if p == "" {
		return "''"
	}
	safe := true
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '@', r == '%', r == '_', r == '+', r == '=', r == ':', r == ',', r == '.', r == '/', r == '-':
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

func denUsage() {
	fmt.Fprintln(os.Stderr, "usage: marmot den <command> [flags]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  create <den-id>  [--lifetime task|durable] [--project <abs>]... [--ref name=<n>,url=<u>,path=<p>,ref=<r>]... [--no-pointer] [--no-vault] [--embedding-provider openai|mock] [--embedding-model <name>] [--dry-run] [--json]")
	fmt.Fprintln(os.Stderr, "  status  [<den-id>] [--json]")
	fmt.Fprintln(os.Stderr, "  destroy <den-id> [--promote <target-den-id>] [--force] [--dry-run] [--json]")
	fmt.Fprintln(os.Stderr, "  list    [--json]")
	fmt.Fprintln(os.Stderr, "  adopt   [--from <project>] [--id <den-id>] [--no-pointer] [--no-rewrite] [--dry-run] [--json]")
	fmt.Fprintln(os.Stderr, "  link    <den-id> (--edit <warren-id>/<project-id> | --link <warren-id>/<project-id> | --link <den-id>) [--dry-run] [--json]")
	fmt.Fprintln(os.Stderr, "  contribute <den-id> [<link>] [--dry-run] [--json]  # stage den knowledge as a commit on marmot/edit/<den>/<project> (cache-backed warrens: dedicated edit worktree on marmot/edit/<den>/<warren>); requires edit-mode link")
	fmt.Fprintln(os.Stderr, "  unlink  <den-id> <target> [--force] [--dry-run] [--json]")
	fmt.Fprintln(os.Stderr, "  bridge  add <den-id> <from> <to> [--relation r]... [--json] | list <den-id> [--json] | rm <den-id> <from> <to> [--json]")
}

func cmdDen(args []string) int {
	if len(args) == 0 {
		denUsage()
		return 0 // `marmot den --help` / bare den: capability probe exits 0
	}
	// Treat --help / -h as success (stave capability probe).
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		denUsage()
		return 0
	}

	sub := args[0]
	subArgs := args[1:]
	switch sub {
	case "create":
		return denCreate(subArgs)
	case "status":
		return denStatus(subArgs)
	case "destroy":
		return denDestroy(subArgs)
	case "list":
		return denList(subArgs)
	case "adopt":
		return denAdopt(subArgs)
	case "link":
		return denLink(subArgs)
	case "unlink":
		return denUnlink(subArgs)
	case "contribute":
		return denContribute(subArgs)
	case "bridge":
		return denBridge(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "den: unknown subcommand %q\n", sub)
		denUsage()
		return 1
	}
}

// denLink wires a knowledge source into a den:
//
//	--edit <warren>/<project>  editable mount + mode=edit link (contribute flow)
//	--link <warren>/<project>  pinned read-only reference (mode=link, pinned
//	                           to the warren's current cache pin / checkout HEAD)
//	--link <den-id>            live den-to-den link (mode=live)
//
// Pinned and live links change NO mount state: federation resolves them
// read-only at serve time (engine.LoadDenLinks).
func denLink(args []string) int {
	// Flags may follow the den-id positional (stave: `den link demo --edit w/p --json`).
	args = reorderInterspersedFlags(args,
		map[string]bool{"edit": true, "link": true},
		map[string]bool{"dry-run": true, "json": true},
	)
	fs := flag.NewFlagSet("den link", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	editTarget := fs.String("edit", "", "link <warren-id>/<project-id> as an editable mount")
	linkTarget := fs.String("link", "", "pinned warren project (<warren-id>/<project-id>) or live den (<den-id>) reference")
	dryRun := fs.Bool("dry-run", false, "print planned ops without writing")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	const usage = "usage: marmot den link <den-id> (--edit <warren-id>/<project-id> | --link <warren-id>/<project-id> | --link <den-id>) [--dry-run] [--json]"
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, usage)
	}
	invalidArgs := func(msg string) int {
		if *asJSON {
			return denJSONError("invalid_args", msg, usage)
		}
		fmt.Fprintf(os.Stderr, "den link: %s\n", msg)
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	if fs.NArg() < 1 {
		return invalidArgs("missing den-id")
	}
	if fs.NArg() > 1 {
		return invalidArgs(fmt.Sprintf("unexpected extra arguments: %s", strings.Join(fs.Args()[1:], " ")))
	}
	denID := fs.Arg(0)
	if *editTarget != "" && *linkTarget != "" {
		return invalidArgs("--edit and --link are mutually exclusive")
	}
	if *editTarget == "" && *linkTarget == "" {
		return invalidArgs("missing --edit <warren-id>/<project-id> or --link <target>")
	}
	if *linkTarget != "" {
		return denLinkNonEdit(denID, *linkTarget, *dryRun, *asJSON)
	}
	parts := strings.Split(*editTarget, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return invalidArgs(fmt.Sprintf("link target %q must be <warren-id>/<project-id>", *editTarget))
	}
	warrenID, projectID := parts[0], parts[1]

	info, err := den.Status(denID)
	if err != nil {
		if *asJSON {
			return denJSONError("den_not_found", err.Error(), "create one: marmot den create "+denID+" --lifetime task --project /path --json")
		}
		fmt.Fprintf(os.Stderr, "den link: %v\n", err)
		return 1
	}

	// Links live in the den vault's warren workspace state (_warren.md in the
	// vault dir). Links-only dens have no vault dir; the den-root fallback has
	// no warren-state support in this slice.
	vaultDir := den.VaultPath(denID)
	if fi, statErr := os.Stat(vaultDir); statErr != nil || !fi.IsDir() {
		msg := fmt.Sprintf("den %q has no identity vault at %s; links need a marmot dir", info.DenID, vaultDir)
		hint := "recreate the den without --no-vault: marmot den destroy " + denID + " && marmot den create " + denID
		if *asJSON {
			return denJSONError("vault_required", msg, hint)
		}
		fmt.Fprintf(os.Stderr, "den link: %s\n", msg)
		fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
		return 1
	}

	state, _, err := warren.LoadWorkspaceStateFromMarmot(vaultDir)
	if err != nil {
		if *asJSON {
			return denJSONError("warren_state_unreadable", err.Error(), "inspect "+filepath.Join(vaultDir, warren.ManifestFileName))
		}
		fmt.Fprintf(os.Stderr, "den link: %v\n", err)
		return 1
	}
	entry, registered := state.Warrens[warrenID]
	// Cache-backed warrens (added via `warren add`, global registry + shared
	// cache) need no per-den checkout registration: the edit link gets a
	// DEDICATED edit worktree of the warren repo, so agent writes never touch
	// a user checkout or the shared read checkout. A den vault whose state
	// already points this warren at a user-managed checkout keeps the legacy
	// registered-checkout behavior untouched.
	cacheEntry, cacheBacked := warren.CacheWorkspaceWarren(warrenID)
	useWorktree := cacheBacked && (!registered ||
		entry.Path == warren.CacheCheckoutPath(warrenID) ||
		warren.IsCacheEditPath(entry.Path))
	if !registered && !useWorktree {
		msg := fmt.Sprintf("warren %q is not registered in den %q's vault", warrenID, denID)
		hint := fmt.Sprintf("register the warren checkout for this den: marmot warren register %s <checkout-path> --dir %s", warrenID, vaultDir)
		if *asJSON {
			return denJSONError("warren_not_registered", msg, hint)
		}
		fmt.Fprintf(os.Stderr, "den link: %s\n", msg)
		fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
		return 1
	}

	// Validation source: the registered checkout for legacy links, the shared
	// read checkout for cache-backed ones (the edit worktree may not exist yet).
	manifestPath := entry.Path
	if useWorktree {
		manifestPath = cacheEntry.Path
	}
	manifest, _, err := warren.LoadManifest(manifestPath)
	if err != nil {
		msg := fmt.Sprintf("warren %q manifest unreadable at %s: %v", warrenID, manifestPath, err)
		if *asJSON {
			return denJSONError("warren_manifest_unreadable", msg, "check the registered checkout path")
		}
		fmt.Fprintf(os.Stderr, "den link: %s\n", msg)
		return 1
	}
	var project *warren.Project
	known := make([]string, 0, len(manifest.Projects))
	for i := range manifest.Projects {
		known = append(known, manifest.Projects[i].ProjectID)
		if manifest.Projects[i].ProjectID == projectID {
			project = &manifest.Projects[i]
		}
	}
	if project == nil {
		msg := fmt.Sprintf("project %q is not registered in warren %q", projectID, warrenID)
		hint := "registered projects: " + strings.Join(known, ", ")
		if len(known) == 0 {
			hint = "the warren manifest lists no projects"
		}
		if *asJSON {
			return denJSONError("project_not_found", msg, hint)
		}
		fmt.Fprintf(os.Stderr, "den link: %s\n", msg)
		fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
		return 1
	}
	if project.ReadOnly {
		msg := fmt.Sprintf("warren author marked project %q read-only; an edit link would be pointless (edits must go through the warren repository)", projectID)
		if *asJSON {
			// Same code den contribute uses for the same author-side policy,
			// so stave sees one readonly refusal code across both verbs.
			return denJSONError("readonly_refused", msg, "")
		}
		fmt.Fprintf(os.Stderr, "den link: %s\n", msg)
		return 1
	}

	target := warrenID + "/" + projectID

	// Branch-name safety for the cache flow: the edit branch
	// marmot/edit/<den-id>/<warren-id> embeds both ids verbatim, so hostile
	// components are refused before ANY git operation (same rule contribute
	// applies to its legacy branch components).
	worktreePath, editBranch := "", ""
	if useWorktree {
		for _, comp := range []struct{ what, val string }{{"den id", denID}, {"warren id", warrenID}} {
			if !isGitRefComponentSafe(comp.val) {
				msg := fmt.Sprintf("%s %q cannot form a safe git branch component (branch marmot/edit/<den-id>/<warren-id>)", comp.what, comp.val)
				hint := "allowed: [A-Za-z0-9._-], no leading '.' or '-', no '..', no trailing '.' or '.lock'"
				if *asJSON {
					return denJSONError("invalid_branch_component", msg, hint)
				}
				fmt.Fprintf(os.Stderr, "den link: %s\n", msg)
				fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
				return 1
			}
		}
		worktreePath = warren.CacheEditWorktreePath(warrenID, denID)
		editBranch = warren.CacheEditBranch(denID, warrenID)
	}

	if *dryRun {
		var ops []string
		if useWorktree {
			if dirExistsCLI(worktreePath) {
				ops = append(ops, fmt.Sprintf("reuse edit worktree %s (branch %s)", worktreePath, editBranch))
			} else {
				ops = append(ops, fmt.Sprintf("git worktree add %s (branch %s)", worktreePath, editBranch))
			}
		}
		ops = append(ops,
			fmt.Sprintf("warren state: mount+edit %s in %s", target, filepath.Join(vaultDir, warren.ManifestFileName)),
			fmt.Sprintf("den manifest: append link %s mode=edit in %s", target, den.ManifestPath(denID)),
		)
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}

	if useWorktree {
		if err := ensureEditWorktree(context.Background(), warrenID, denID, worktreePath, editBranch); err != nil {
			msg := fmt.Sprintf("could not create the edit worktree at %s: %v", worktreePath, err)
			hint := "sync the cache and retry: marmot warren sync " + warrenID
			if *asJSON {
				return denJSONError("worktree_failed", msg, hint)
			}
			fmt.Fprintf(os.Stderr, "den link: %s\n", msg)
			fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
			return 1
		}
	}

	// Cache-backed links route the den's mounts of this warren through the
	// dedicated edit worktree (EnsureEditableMountAt creates/repoints the
	// state entry); legacy links leave the registered checkout path alone.
	var stateChanged bool
	if useWorktree {
		stateChanged, err = den.EnsureEditableMountAt(vaultDir, warrenID, projectID, worktreePath)
	} else {
		stateChanged, err = den.EnsureEditableMount(vaultDir, warrenID, projectID)
	}
	if err != nil {
		if *asJSON {
			return denJSONError("link_failed", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "den link: %v\n", err)
		return 1
	}
	added, err := den.AddLink(denID, den.Link{
		Target:  target,
		Mode:    den.LinkModeEdit,
		Warren:  warrenID,
		Project: projectID,
	})
	if err != nil {
		if *asJSON {
			return denJSONError("link_failed", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "den link: %v\n", err)
		return 1
	}

	warnings := []string{}
	if !stateChanged && !added {
		warnings = append(warnings, "already linked")
	}
	if *asJSON {
		return printDenJSON(jsonLinkEnvelope{
			Schema: 1,
			DenID:  denID,
			Link: jsonLinkBody{
				Target:  target,
				Mode:    den.LinkModeEdit,
				Warren:  warrenID,
				Project: projectID,
			},
			Worktree: worktreePath,
			Branch:   editBranch,
			Warnings: warnings,
		})
	}
	fmt.Printf("Linked den %q to %s (mode=edit)\n", denID, target)
	if worktreePath != "" {
		fmt.Printf("  edit worktree: %s (branch %s)\n", worktreePath, editBranch)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return 0
}

// denLinkNonEdit handles `den link <den> --link <target>`: a target with a
// '/' is a PINNED warren-project reference (mode=link, recorded with the
// warren's current pin); a bare target is a LIVE den-to-den link (mode=live).
// Neither touches mount state — federation resolves both read-only at serve
// time — so links-only dens (no identity vault) can hold them too.
func denLinkNonEdit(denID, target string, dryRun, asJSON bool) int {
	linkFail := func(code, msg, hint string) int {
		if asJSON {
			return denJSONError(code, msg, hint)
		}
		fmt.Fprintf(os.Stderr, "den link: %s\n", msg)
		if hint != "" {
			fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
		}
		return 1
	}

	if _, err := den.Status(denID); err != nil {
		return linkFail("den_not_found", err.Error(), "create one: marmot den create "+denID+" --lifetime task --project /path --json")
	}

	var l den.Link
	warnings := []string{}
	if strings.Contains(target, "/") {
		// Pinned warren project reference.
		parts := strings.Split(target, "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return linkFail("invalid_args", fmt.Sprintf("link target %q must be <warren-id>/<project-id> or a bare <den-id>", target), "")
		}
		warrenID, projectID := parts[0], parts[1]

		// Validation source: the shared cache checkout for cache-backed
		// warrens, else a checkout registered in the den vault's warren state
		// (legacy). A warren known to neither is refused.
		manifestPath := ""
		cacheEntry, cacheBacked := warren.CacheWorkspaceWarren(warrenID)
		if cacheBacked {
			manifestPath = cacheEntry.Path
		} else if vaultDir := den.VaultPath(denID); dirExistsCLI(vaultDir) {
			if state, _, err := warren.LoadWorkspaceStateFromMarmot(vaultDir); err == nil {
				if entry, ok := state.Warrens[warrenID]; ok {
					manifestPath = entry.Path
				}
			}
		}
		if manifestPath == "" {
			return linkFail("warren_not_registered",
				fmt.Sprintf("warren %q has no shared cache checkout and is not registered in den %q's vault", warrenID, denID),
				"add it to the shared cache first: marmot warren add <url> --id "+warrenID)
		}
		manifest, _, err := warren.LoadManifest(manifestPath)
		if err != nil {
			return linkFail("warren_manifest_unreadable",
				fmt.Sprintf("warren %q manifest unreadable at %s: %v", warrenID, manifestPath, err), "")
		}
		found := false
		known := make([]string, 0, len(manifest.Projects))
		for i := range manifest.Projects {
			p := &manifest.Projects[i]
			known = append(known, p.ProjectID)
			if p.ProjectID == projectID || sliceContains(p.Aliases, projectID) {
				found = true
			}
		}
		if !found {
			hint := "registered projects: " + strings.Join(known, ", ")
			if len(known) == 0 {
				hint = "the warren manifest lists no projects"
			}
			return linkFail("project_not_found",
				fmt.Sprintf("project %q is not registered in warren %q", projectID, warrenID), hint)
		}

		// Pin: cache-backed warrens pin to the shared checkout's current pin
		// commit; legacy registered checkouts pin to the checkout's HEAD.
		pin := ""
		if cacheBacked {
			pin = warren.ReadCachePin(warrenID)
		} else if head, headErr := gitOutput(manifestPath, "rev-parse", "HEAD"); headErr == nil {
			pin = head
		}
		if pin == "" {
			warnings = append(warnings, fmt.Sprintf("warren %q has no resolvable pin commit; the link records no pin (run 'marmot warren sync %s')", warrenID, warrenID))
		}
		l = den.Link{Target: target, Mode: den.LinkModeLink, Warren: warrenID, Project: projectID, PinnedRef: pin}
	} else {
		// Live den-to-den link.
		if target == denID {
			return linkFail("invalid_args", fmt.Sprintf("den %q cannot live-link itself", denID), "")
		}
		tgt, err := den.Status(target)
		if err != nil {
			return linkFail("link_target_not_found",
				fmt.Sprintf("live link target den %q not found: %v", target, err),
				"live links target an existing den: marmot den list")
		}
		// A task den can vanish at any moment (plan §6): a live link into it
		// would dangle silently, so only durable dens may be targets.
		if tgt.Lifetime == den.LifetimeTask {
			return linkFail("task_den_refused",
				fmt.Sprintf("den %q has lifetime task; live links may only target durable dens (a task den is expected to be destroyed)", target),
				"pin its knowledge instead (den contribute to a warren) or target a durable den")
		}
		l = den.Link{Target: target, Mode: den.LinkModeLive}
	}

	if dryRun {
		op := fmt.Sprintf("den manifest: append link %s mode=%s in %s", l.Target, l.Mode, den.ManifestPath(denID))
		if l.PinnedRef != "" {
			op = fmt.Sprintf("den manifest: append link %s mode=%s pinned=%s in %s", l.Target, l.Mode, l.PinnedRef, den.ManifestPath(denID))
		}
		if asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: []string{op}})
		}
		fmt.Println("dry-run:", op)
		return 0
	}

	added, err := den.AddLink(denID, l)
	if err != nil {
		return linkFail("link_failed", err.Error(), "")
	}
	if !added {
		warnings = append(warnings, "already linked")
	}
	if asJSON {
		return printDenJSON(jsonLinkEnvelope{
			Schema: 1,
			DenID:  denID,
			Link: jsonLinkBody{
				Target:  l.Target,
				Mode:    l.Mode,
				Warren:  l.Warren,
				Project: l.Project,
			},
			PinnedCommit: l.PinnedRef,
			Warnings:     warnings,
		})
	}
	if l.Mode == den.LinkModeLink {
		if l.PinnedRef != "" {
			fmt.Printf("Linked den %q to %s (mode=link, pinned %s)\n", denID, l.Target, l.PinnedRef)
		} else {
			fmt.Printf("Linked den %q to %s (mode=link, unpinned)\n", denID, l.Target)
		}
	} else {
		fmt.Printf("Linked den %q to %s (mode=live)\n", denID, l.Target)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return 0
}

// denUnlink removes den links matching a target ref (any mode). Edit links
// with unpushed edit-branch commits are refused without --force (mirroring
// den destroy's unpushed_edits refusal); on success the workspace-state
// mount for that project is removed while the edit worktree and branch stay
// in place — den destroy owns worktree cleanup, and the branch always
// survives in the shared cache.
func denUnlink(args []string) int {
	args = reorderInterspersedFlags(args, nil, map[string]bool{"force": true, "dry-run": true, "json": true})
	fs := flag.NewFlagSet("den unlink", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "unlink even when the edit branch carries unpushed commits")
	dryRun := fs.Bool("dry-run", false, "print planned ops without writing")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	const usage = "usage: marmot den unlink <den-id> <target> [--force] [--dry-run] [--json]"
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, usage)
	}
	unlinkFail := func(code, msg, hint string) int {
		if *asJSON {
			return denJSONError(code, msg, hint)
		}
		fmt.Fprintf(os.Stderr, "den unlink: %s\n", msg)
		if hint != "" {
			fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
		}
		return 1
	}
	if fs.NArg() != 2 {
		return unlinkFail("invalid_args", "expected <den-id> and <target>", usage)
	}
	denID, target := fs.Arg(0), fs.Arg(1)

	info, err := den.Status(denID)
	if err != nil {
		return unlinkFail("den_not_found", err.Error(), "marmot den list")
	}
	var matched []den.Link
	for _, l := range info.Links {
		if l.MatchesRef(target) {
			matched = append(matched, l)
		}
	}
	if len(matched) == 0 {
		return unlinkFail("link_not_found",
			fmt.Sprintf("den %q has no link matching %q", denID, target),
			"see its links: marmot den status "+denID)
	}

	// Unpushed-edit refusal for edit links (same posture as den destroy):
	// removing the mount would orphan knowledge staged for review. --force
	// acknowledges; the branch always survives in the shared cache.
	vaultDir := den.VaultPath(denID)
	warnings := []string{}
	hasEdit := false
	for _, l := range matched {
		if l.Mode == den.LinkModeEdit {
			hasEdit = true
		}
	}
	if hasEdit {
		wts := denEditWorktrees(denID, matched)
		if len(wts) > 0 {
			unpushed, countWarnings, degraded := denUnpushedEdits(context.Background(), gitx.New(), wts)
			warnings = append(warnings, countWarnings...)
			if degraded && !*force {
				return unlinkFail("unpushed_unknown",
					fmt.Sprintf("link %s: could not determine unpushed edit state (git upstream/status checks failed); refusing so review-staged work is not orphaned", target),
					"resolve the git environment and retry, or re-run with --force — the edit branch always survives in the shared cache")
			}
			if unpushed > 0 && !*force {
				return unlinkFail("unpushed_edits",
					fmt.Sprintf("link %s has %d unpushed edit(s) on its edit branch; they were never pushed for review", target, unpushed),
					"push them first (see den contribute's push_command) or re-run with --force — the edit branch always survives in the shared cache")
			}
		}
	}

	if *dryRun {
		ops := make([]string, 0, len(matched)+1)
		for _, l := range matched {
			ops = append(ops, fmt.Sprintf("den manifest: remove link %s mode=%s in %s", l.Target, l.Mode, den.ManifestPath(denID)))
			if l.Mode == den.LinkModeEdit && l.Warren != "" && l.Project != "" && dirExistsCLI(vaultDir) {
				ops = append(ops, fmt.Sprintf("warren state: unmount %s/%s in %s (worktree and branch kept; den destroy cleans worktrees)", l.Warren, l.Project, filepath.Join(vaultDir, warren.ManifestFileName)))
			}
		}
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}

	removed, err := den.RemoveLinks(denID, target)
	if err != nil {
		return unlinkFail("unlink_failed", err.Error(), "")
	}
	for _, l := range removed {
		if l.Mode != den.LinkModeEdit || l.Warren == "" || l.Project == "" || !dirExistsCLI(vaultDir) {
			continue
		}
		if _, merr := den.RemoveMount(vaultDir, l.Warren, l.Project); merr != nil {
			warnings = append(warnings, fmt.Sprintf("removing the %s/%s mount from warren state failed: %v", l.Warren, l.Project, merr))
		}
	}

	if *asJSON {
		env := jsonUnlinkEnvelope{Schema: 1, DenID: denID, Removed: make([]jsonLinkBody, 0, len(removed)), Warnings: warnings}
		for _, l := range removed {
			env.Removed = append(env.Removed, jsonLinkBody{Target: l.Target, Mode: l.Mode, Warren: l.Warren, Project: l.Project})
		}
		return printDenJSON(env)
	}
	for _, l := range removed {
		fmt.Printf("Unlinked %s (mode=%s) from den %q\n", l.Target, l.Mode, denID)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return 0
}

// sliceContains reports whether list contains s (tiny local helper; keep in
// sync with warren alias matching semantics).
func sliceContains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// ensureEditWorktree idempotently creates the dedicated edit worktree for a
// (warren, den) pair under the per-warren cache lock: branch
// marmot/edit/<den-id>/<warren-id> checked out at
// $MARMOT_HOME/warren-cache/edits/<warren-id>/<den-id>. An existing branch is
// re-attached (WorktreeAddExisting), a fresh one starts from the shared
// checkout's pin, falling back to origin/<default-branch>. An existing
// worktree directory is reused as-is.
func ensureEditWorktree(ctx context.Context, warrenID, denID, worktreePath, branch string) error {
	bare := warren.CacheBarePath(warrenID)
	client := gitx.New()
	return gitx.WithCacheLock(home.WarrenCacheDir(), warrenID, func() error {
		if !dirExistsCLI(worktreePath) {
			// Drop stale bookkeeping first: a manually deleted worktree dir
			// would otherwise make `worktree add` refuse the branch as
			// already checked out.
			_ = client.WorktreePrune(ctx, bare)
			if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
				return err
			}
			exists, err := client.BranchExists(ctx, bare, branch)
			if err != nil {
				return err
			}
			if exists {
				if err := client.WorktreeAddExisting(ctx, bare, worktreePath, branch); err != nil {
					return err
				}
			} else {
				start := warren.ReadCachePin(warrenID)
				if start == "" {
					if reg, regErr := warrenreg.Load(); regErr == nil {
						if entry, ok := reg.Warrens[warrenID]; ok && entry.DefaultBranch != "" {
							start = "origin/" + entry.DefaultBranch
						}
					}
				}
				if start == "" {
					if b, bErr := client.RemoteDefaultBranch(ctx, bare); bErr == nil && b != "" {
						start = "origin/" + b
					}
				}
				if start == "" {
					return fmt.Errorf("cannot resolve a start point for edit branch %q (no cache pin and no default branch; run 'marmot warren sync %s')", branch, warrenID)
				}
				if err := client.WorktreeAddBranch(ctx, bare, worktreePath, branch, start); err != nil {
					return err
				}
			}
		}
		// Worktree commits (per-write auto-commit, den contribute) need an
		// author identity; when none resolves (no global git config), pin a
		// marmot identity in the shared cache repo config. marmot only ever
		// commits inside its own edit worktrees, never in user checkouts.
		if email, cfgErr := client.OutputIn(ctx, worktreePath, "config", "user.email"); cfgErr != nil || strings.TrimSpace(email) == "" {
			if _, err := client.Output(ctx, "--git-dir", bare, "config", "user.name", "marmot"); err != nil {
				return err
			}
			if _, err := client.Output(ctx, "--git-dir", bare, "config", "user.email", "marmot@localhost"); err != nil {
				return err
			}
		}
		return nil
	})
}

// jsonContributeEnvelope is the schema:1 success envelope for den contribute.
// Contribute is the terminal packaging verb of the den flow: when
// committed:true it carries the ready-to-run push_command (shell-quoted) and
// the checkout path, because a paired `warren propose` afterwards sees a
// clean tree and can supply neither.
type jsonContributeEnvelope struct {
	Schema      int            `json:"schema"`
	DenID       string         `json:"den_id"`
	Link        jsonLinkBody   `json:"link"`
	Branch      string         `json:"branch"`
	Commit      string         `json:"commit"`
	Committed   bool           `json:"committed"`
	Checkout    string         `json:"checkout"`
	PushCommand string         `json:"push_command"`
	Contributed jsonFlowCounts `json:"contributed"`
	Warnings    []string       `json:"warnings"`
}

// denContribute folds the den vault's knowledge into the linked warren
// project, staged as a git commit on branch marmot/edit/<den-id>/<project-id>
// in the registered warren checkout. Unlike live MCP writes, creates ARE
// allowed here — the PR is the review gate. The classification/apply engine
// lives in internal/den (exec-free); all git operations stay in this layer.
//
// Contribute may run repeatedly: it reuses the same edit branch, appending
// commits, and an unchanged den is a successful all-noop run with no git
// operations at all (committed:false). A dirty project scope is refused up
// front (checkout_dirty) so user work is never swept into the commit. The
// engine writes happen with the edit branch checked out, so repeat runs
// compare against previously contributed state; on any failure the recovery
// removes/restores exactly the engine-written files, returns to the previous
// branch, and deletes the edit branch if this run created it, so a retry is
// a real contribute. An author-side read-only project is refused up front as
// readonly_refused (warren.WriteEditableNodeFile would refuse each write anyway;
// the dedicated code makes the policy visible before any mutation).
func denContribute(args []string) int {
	// Flags may follow the den-id (stave: `den contribute <id> --json`).
	args = reorderInterspersedFlags(args,
		map[string]bool{},
		map[string]bool{"dry-run": true, "json": true},
	)
	fs := flag.NewFlagSet("den contribute", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "print planned ops without writing")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, "marmot den contribute <den-id> [<link>]")
	}
	rest := fs.Args()
	if len(rest) < 1 {
		msg := "usage: marmot den contribute <den-id> [<link>]"
		if *asJSON {
			return denJSONError("invalid_args", msg, "provide a den id")
		}
		fmt.Fprintln(os.Stderr, msg)
		return 1
	}
	if len(rest) > 2 {
		msg := fmt.Sprintf("unexpected extra arguments: %s", strings.Join(rest[2:], " "))
		if *asJSON {
			return denJSONError("invalid_args", msg, "usage: marmot den contribute <den-id> [<link>]")
		}
		fmt.Fprintf(os.Stderr, "den contribute: %s\n", msg)
		fmt.Fprintln(os.Stderr, "usage: marmot den contribute <den-id> [<link>]")
		return 1
	}
	denID := rest[0]
	linkRef := ""
	if len(rest) > 1 {
		linkRef = rest[1]
	}
	// Guard against accidental flag leftovers.
	if strings.HasPrefix(linkRef, "-") {
		linkRef = ""
	}

	info, err := den.Status(denID)
	if err != nil {
		if *asJSON {
			return denJSONError("den_not_found", err.Error(), "create one: marmot den create "+denID+" --lifetime task --project /path --json")
		}
		fmt.Fprintf(os.Stderr, "den contribute: %v\n", err)
		return 1
	}

	// Require an edit-mode link (target or explicit linkRef), keeping the
	// first match: it names the warren/project the contribute targets.
	var editLink *den.Link
	for i := range info.Links {
		l := info.Links[i]
		if l.Mode != "edit" {
			continue
		}
		if linkRef == "" || l.Target == linkRef || (l.Warren != "" && l.Project != "" && l.Warren+"/"+l.Project == linkRef) {
			editLink = &info.Links[i]
			break
		}
	}
	if editLink == nil {
		msg := fmt.Sprintf("den %q has no edit-mode link; contribute requires an edit-mode link (worktree branch). Add one with: marmot den link %s --edit <warren>/<project>", denID, denID)
		if linkRef != "" {
			msg = fmt.Sprintf("link %q is not an edit-mode link on den %q (or not found); contribute requires mode=edit", linkRef, denID)
		}
		if *asJSON {
			return denJSONError("edit_link_required", msg, "marmot den link "+denID+" --edit <warren>/<project>")
		}
		fmt.Fprintf(os.Stderr, "den contribute: %s\n", msg)
		return 1
	}

	// Resolve the link's warren/project (older links may carry only Target).
	warrenID, projectID := editLink.Warren, editLink.Project
	if warrenID == "" || projectID == "" {
		parts := strings.SplitN(editLink.Target, "/", 2)
		if len(parts) == 2 {
			warrenID, projectID = parts[0], parts[1]
		}
	}
	target := warrenID + "/" + projectID

	contributeFail := func(code, msg, hint string) int {
		if *asJSON {
			return denJSONError(code, msg, hint)
		}
		fmt.Fprintf(os.Stderr, "den contribute: %s\n", msg)
		if hint != "" {
			fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
		}
		return 1
	}

	// Git-ref safety: the branch name marmot/edit/<den-id>/<project-id>
	// embeds both ids verbatim, so hostile components (spaces, "..", '~',
	// trailing ".lock", …) are refused before ANY git operation.
	for _, comp := range []struct{ what, val string }{{"den id", denID}, {"project id", projectID}} {
		if !isGitRefComponentSafe(comp.val) {
			return contributeFail("invalid_branch_component",
				fmt.Sprintf("%s %q cannot form a safe git branch component (branch marmot/edit/<den-id>/<project-id>)", comp.what, comp.val),
				"allowed: [A-Za-z0-9._-], no leading '.' or '-', no '..', no trailing '.' or '.lock'")
		}
	}

	// Resolve the edit link to an active editable mount in the den vault.
	vaultDir := den.VaultPath(denID)
	if !dirExistsCLI(vaultDir) {
		return contributeFail("link_unresolved",
			fmt.Sprintf("den %q has no identity vault at %s; the edit link cannot be resolved", denID, vaultDir),
			"recreate the den without --no-vault and re-link: marmot den link "+denID+" --edit "+target)
	}
	mounts, err := warren.ActiveMounts(vaultDir)
	if err != nil {
		return contributeFail("link_unresolved",
			fmt.Sprintf("den vault warren state unreadable: %v", err),
			"inspect "+filepath.Join(vaultDir, warren.ManifestFileName))
	}
	var mount *warren.ProjectStatus
	for i := range mounts {
		if mounts[i].WarrenID == warrenID && mounts[i].ProjectID == projectID {
			mount = &mounts[i]
			break
		}
	}
	if mount == nil {
		// Distinguish a moved/deleted checkout from a never-mounted link.
		if state, _, serr := warren.LoadWorkspaceStateFromMarmot(vaultDir); serr == nil {
			if entry, ok := state.Warrens[warrenID]; ok && !dirExistsCLI(entry.Path) {
				return contributeFail("warren_unreachable",
					fmt.Sprintf("warren %q is UNREACHABLE at %s", warrenID, entry.Path),
					fmt.Sprintf("re-register the checkout: marmot warren register %s <checkout-path> --dir %s", warrenID, vaultDir))
			}
		}
		return contributeFail("link_unresolved",
			fmt.Sprintf("edit link %s has no active editable mount in den %q's vault", target, denID),
			"re-link it: marmot den link "+denID+" --edit "+target)
	}
	if !mount.Editable {
		return contributeFail("readonly_refused",
			fmt.Sprintf("project %q is not editable (the warren author marked it read-only, or the edit mount was revoked); contribute refuses to write", projectID),
			"edits must go through the warren repository itself")
	}

	// Cache-backed edit links are served from a dedicated edit worktree that
	// is permanently on its branch — contribute there needs no branch
	// checkout/restore dance and never touches a user checkout.
	if warren.IsCacheEditPath(mount.WarrenPath) {
		return denContributeWorktree(denID, target, vaultDir, *mount, *dryRun, *asJSON, contributeFail)
	}

	branch := fmt.Sprintf("marmot/edit/%s/%s", denID, projectID)
	ctx := context.Background()

	checkout := mount.WarrenPath
	if !dirExistsCLI(checkout) {
		return contributeFail("warren_unreachable",
			fmt.Sprintf("warren %q is UNREACHABLE at %s", warrenID, checkout),
			fmt.Sprintf("re-register the checkout: marmot warren register %s <checkout-path> --dir %s", warrenID, vaultDir))
	}
	_, gitErr := gitOutput(checkout, "rev-parse", "--is-inside-work-tree")
	isGit := gitErr == nil
	branchExists := false
	relProject := ""
	if isGit {
		if _, verifyErr := gitOutput(checkout, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); verifyErr == nil {
			branchExists = true
		}
		rel, relErr := filepath.Rel(checkout, mount.Path)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return contributeFail("contribute_failed",
				fmt.Sprintf("project vault %s is outside the warren checkout %s; contribute cannot stage it", mount.Path, checkout), "")
		}
		relProject = rel

		// PREFLIGHT (dirty-tree refusal): pre-existing uncommitted user work
		// under the project path would be swept into the contribute commit by
		// the pathspec-limited `git add` and then removed from the tree by
		// the checkout back — silent loss into a mislabeled commit. Refuse
		// before any engine write or branch operation, which also guarantees
		// the tree contribute stages contains ONLY engine-written files and
		// keeps the nothing-to-commit judgment sound. Same .marmot-data
		// excludes as the commit, so DB sidecar noise never counts. The rule
		// applies identically to --dry-run.
		statusArgs := append(append([]string{"status", "--porcelain", "--"}, proposeExcludePathspecs...), relProject)
		dirty, dirtyErr := gitOutput(checkout, statusArgs...)
		if dirtyErr != nil {
			return contributeFail("contribute_failed", dirtyErr.Error(), "")
		}
		if strings.TrimSpace(dirty) != "" {
			return contributeFail("checkout_dirty",
				fmt.Sprintf("warren checkout has uncommitted changes under %s; contribute refuses so it cannot sweep unrelated work into its commit:\n%s", relProject, strings.TrimSpace(dirty)),
				"commit or stash them first, or package them with: marmot warren propose")
		}
	}

	// Dry-run: resolution + the read-only classification pass, nothing
	// touched — no writes, no git mutations. Note the plan reads the
	// currently checked-out tree; commits already on the edit branch are not
	// visible (disclosed as a note op below).
	if *dryRun {
		plan, planErr := den.Contribute(ctx, vaultDir, *mount, true)
		if planErr != nil {
			return contributeFail("contribute_failed", planErr.Error(), "")
		}
		ops := make([]string, 0, len(plan.Ops)+2)
		for _, op := range plan.Ops {
			ops = append(ops, op.String())
		}
		if plan.Counts.Changes() > 0 {
			ops = append(ops, "commit on "+branch)
		}
		if branchExists {
			ops = append(ops, "note: edit branch "+branch+" exists; the actual run plans against it — results may differ")
		}
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}

	emitSuccess := func(commit string, committed bool, counts den.FlowCounts, warnings []string, prevBranch string) int {
		if warnings == nil {
			warnings = []string{}
		}
		// Contribute is the flow's terminal packaging verb: a committed run
		// must hand the caller everything publishing needs (a later `warren
		// propose` sees a clean tree and reports nothing_to_propose).
		checkoutOut, pushCommand := "", ""
		if committed {
			checkoutOut = checkout
			pushCommand = fmt.Sprintf("git -C %s push -u origin %s", shellQuoteArg(checkout), branch)
		}
		if *asJSON {
			return printDenJSON(jsonContributeEnvelope{
				Schema:      1,
				DenID:       denID,
				Link:        jsonLinkBody{Target: target, Mode: den.LinkModeEdit, Warren: warrenID, Project: projectID},
				Branch:      branch,
				Commit:      commit,
				Committed:   committed,
				Checkout:    checkoutOut,
				PushCommand: pushCommand,
				Contributed: jsonFlowCounts{
					Added:      counts.Added,
					Updated:    counts.Updated,
					Superseded: counts.Superseded,
					Noop:       counts.Noop,
				},
				Warnings: warnings,
			})
		}
		fmt.Printf("Contributed den %q to %s: +%d added ~%d updated ^%d superseded =%d noop\n",
			denID, target, counts.Added, counts.Updated, counts.Superseded, counts.Noop)
		if committed {
			fmt.Printf("Committed %s on branch %q (back on %q).\n", commit, branch, prevBranch)
			fmt.Printf("Publish it with:\n  git -C %s push -u origin %s\nthen open a pull request in the warren repository. marmot never pushes for you.\n", shellQuoteArg(mount.WarrenPath), branch)
		} else {
			fmt.Println("Nothing to commit — the warren project is already up to date.")
		}
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		return 0
	}

	// When the edit branch doesn't exist yet, a fresh branch would fork from
	// the current tree, so a read-only plan against it is accurate: zero
	// changes short-circuits to success with no branch/commit ops at all
	// (this is what makes a repeat contribute of an unchanged den exit 0
	// idempotently).
	if !branchExists {
		plan, planErr := den.Contribute(ctx, vaultDir, *mount, true)
		if planErr != nil {
			return contributeFail("contribute_failed", planErr.Error(), "")
		}
		if plan.Counts.Changes() == 0 {
			return emitSuccess("", false, plan.Counts, plan.Warnings, "")
		}
	}

	// There are changes to stage (or an existing edit branch to compare
	// against): from here contribute needs a real git checkout on a branch.
	if !isGit {
		return contributeFail("contribute_failed",
			fmt.Sprintf("warren %q at %s is not a git checkout; contribute stages a git commit and needs one", warrenID, checkout), "")
	}
	prevBranch, err := gitOutput(checkout, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return contributeFail("detached_head",
			fmt.Sprintf("warren checkout at %s is on a detached HEAD", checkout),
			"check out a branch first (contribute needs a branch to return to)")
	}

	// Check out the edit branch BEFORE the engine writes, so node files land
	// on the edit branch's working tree (the preflight guarantees the
	// project scope is clean, so the checkout cannot conflict there).
	branchCreated := false
	if branchExists {
		if _, err := gitOutput(checkout, "checkout", branch); err != nil {
			return contributeFail("contribute_failed", err.Error(),
				fmt.Sprintf("could not check out existing edit branch %q; resolve the checkout state in %s first", branch, checkout))
		}
	} else {
		if _, err := gitOutput(checkout, "checkout", "-b", branch); err != nil {
			return contributeFail("contribute_failed", err.Error(), "")
		}
		branchCreated = true
	}

	// From here every failure recovers to the pre-contribute state so a
	// retry is a REAL contribute, not a NOOP against leftovers: unstage and
	// remove/restore exactly the files the engine reported writing, return
	// to the previous branch, and delete the edit branch if THIS run created
	// it. Once the commit has landed, recovery keeps branch and files (only
	// the checkout-back failed; the knowledge is committed).
	var engineCreated, engineModified []string
	committedOK := false
	recoverFail := func(failErr error, step string) int {
		if !committedOK {
			restore := append(append([]string{}, engineCreated...), engineModified...)
			if len(restore) > 0 {
				_, _ = gitOutput(checkout, append([]string{"reset", "-q", "HEAD", "--"}, restore...)...)
			}
			if len(engineModified) > 0 {
				_, _ = gitOutput(checkout, append([]string{"checkout", "--"}, engineModified...)...)
			}
			if len(engineCreated) > 0 {
				_, _ = gitOutput(checkout, append([]string{"clean", "-qf", "--"}, engineCreated...)...)
			}
		}
		_, _ = gitOutput(checkout, "checkout", prevBranch)
		if branchCreated && !committedOK {
			_, _ = gitOutput(checkout, "branch", "-D", branch)
		}
		currentBranch, _ := gitOutput(checkout, "rev-parse", "--abbrev-ref", "HEAD")
		hint := fmt.Sprintf("recovered: engine-written files removed/restored, current branch %q; re-run after fixing the cause", currentBranch)
		if committedOK {
			hint = fmt.Sprintf("the contribute commit exists on branch %q; only the return to %q failed — current branch %q", branch, prevBranch, currentBranch)
		}
		return contributeFail("contribute_failed", step+": "+failErr.Error(), hint)
	}

	result, err := den.Contribute(ctx, vaultDir, *mount, false)
	if result != nil {
		// Partial results carry the files written before a mid-run failure.
		engineCreated, engineModified = result.CreatedFiles, result.ModifiedFiles
	}
	if err != nil {
		return recoverFail(err, "contribute engine")
	}
	if result.Counts.Changes() == 0 {
		if _, err := gitOutput(checkout, "checkout", prevBranch); err != nil {
			return recoverFail(err, "git checkout "+prevBranch)
		}
		if branchCreated {
			_, _ = gitOutput(checkout, "branch", "-D", branch)
		}
		return emitSuccess("", false, result.Counts, result.Warnings, prevBranch)
	}

	// Pathspec-limited add/commit scoped to the project inside the warren;
	// excludes MUST precede the positive pathspec (git add stages nothing
	// otherwise — see proposeExcludePathspecs).
	pathspec := append(append([]string{"--"}, proposeExcludePathspecs...), relProject)
	commitMsg := fmt.Sprintf("marmot contribute: den=%s project=%s +%d ~%d ^%d",
		denID, projectID, result.Counts.Added, result.Counts.Updated, result.Counts.Superseded)
	steps := [][]string{
		append([]string{"add"}, pathspec...),
		append([]string{"commit", "-m", commitMsg}, pathspec...),
	}
	for _, step := range steps {
		if _, err := gitOutput(checkout, step...); err != nil {
			return recoverFail(err, "git "+step[0])
		}
	}
	commitSHA, _ := gitOutput(checkout, "rev-parse", "HEAD")
	committedOK = true
	if _, err := gitOutput(checkout, "checkout", prevBranch); err != nil {
		return recoverFail(err, "git checkout "+prevBranch)
	}
	return emitSuccess(commitSHA, true, result.Counts, result.Warnings, prevBranch)
}

// denContributeWorktree is the contribute flow for edit links backed by a
// cache edit worktree ($MARMOT_HOME/warren-cache/edits/<warren>/<den>). The
// worktree is permanently checked out on marmot/edit/<den-id>/<warren-id>,
// so the legacy checkout dance disappears: no previous-branch capture, no
// branch checkout/restore, no branch deletion on recovery. The working tree
// IS the edit branch tip, which also makes the read-only plan (and dry-run)
// always accurate — commits from earlier contributes and per-write
// auto-commits are visible in the tree the plan reads. Failure recovery
// restores/removes exactly the engine-written files; the whole mutate span
// (engine writes, sweep, commit, recovery) runs under the per-warren cache
// lock like every other cache mutation. Node markdown left uncommitted by a
// failed auto-commit is swept into the contribute commit (see the preflight
// classification below); any other dirt still refuses.
func denContributeWorktree(denID, target, vaultDir string, mount warren.ProjectStatus, dryRun, asJSON bool, contributeFail func(code, msg, hint string) int) int {
	worktree, wtWarrenID, wtDenID, ok := warren.SplitCacheEditPath(mount.WarrenPath)
	if !ok {
		return contributeFail("contribute_failed",
			fmt.Sprintf("mount path %s is not a cache edit worktree", mount.WarrenPath), "")
	}
	// Branch-name safety for the worktree branch components (den id, warren
	// id) — same rule the legacy flow applies to den/project ids.
	for _, comp := range []struct{ what, val string }{{"den id", wtDenID}, {"warren id", wtWarrenID}} {
		if !isGitRefComponentSafe(comp.val) {
			return contributeFail("invalid_branch_component",
				fmt.Sprintf("%s %q cannot form a safe git branch component (branch marmot/edit/<den-id>/<warren-id>)", comp.what, comp.val),
				"allowed: [A-Za-z0-9._-], no leading '.' or '-', no '..', no trailing '.' or '.lock'")
		}
	}
	branch := warren.CacheEditBranch(wtDenID, wtWarrenID)
	if !dirExistsCLI(worktree) {
		return contributeFail("warren_unreachable",
			fmt.Sprintf("edit worktree missing at %s", worktree),
			"re-create it: marmot den link "+denID+" --edit "+target)
	}
	head, headErr := gitOutput(worktree, "symbolic-ref", "--short", "HEAD")
	if headErr != nil || head != branch {
		return contributeFail("contribute_failed",
			fmt.Sprintf("edit worktree %s is not on branch %q (HEAD=%q); it must stay permanently on its edit branch", worktree, branch, head),
			fmt.Sprintf("restore it with: git -C %s checkout %s", shellQuoteArg(worktree), branch))
	}
	relProject, relErr := filepath.Rel(worktree, mount.Path)
	if relErr != nil || strings.HasPrefix(relProject, "..") {
		return contributeFail("contribute_failed",
			fmt.Sprintf("project vault %s is outside the edit worktree %s; contribute cannot stage it", mount.Path, worktree), "")
	}

	// PREFLIGHT (dirty-tree classification): the worktree only ever holds
	// committed state — contribute commits, per-write auto-commits. Dirt
	// under the project scope is expected in exactly ONE shape: node markdown
	// left behind by a failed MCP auto-commit (autoCommitEditWrite degrades
	// to a warning promising "the next den contribute will commit it"). Those
	// *.md files are SWEEPABLE — the commit below includes them — because a
	// refusal on marmot's own leftovers would wedge contribute permanently
	// (nothing else ever cleans them). Any OTHER dirt is unknown state and
	// still refuses (checkout_dirty) rather than being mislabeled into a
	// marmot commit. Same .marmot-data excludes as the commit; the
	// classification applies identically to --dry-run. The legacy user-clone
	// contribute path keeps its strict refusal — dirt there can be the user's
	// own work.
	// --untracked-files=all: porcelain collapses untracked directories to one
	// "dir/" entry by default, which would misclassify a lone orphaned node
	// file in a fresh directory as foreign dirt.
	statusArgs := append(append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, proposeExcludePathspecs...), relProject)
	dirty, dirtyErr := gitOutput(worktree, statusArgs...)
	if dirtyErr != nil {
		return contributeFail("contribute_failed", dirtyErr.Error(), "")
	}
	sweepable, foreign := classifyWorktreeDirt(dirty)
	if len(foreign) > 0 {
		return contributeFail("checkout_dirty",
			fmt.Sprintf("edit worktree has uncommitted non-node changes under %s; contribute refuses so it cannot sweep unknown state into its commit:\n%s", relProject, strings.Join(foreign, "\n")),
			fmt.Sprintf("inspect it with: git -C %s status", shellQuoteArg(worktree)))
	}
	sweptCount := len(sweepable)

	ctx := context.Background()
	if dryRun {
		plan, planErr := den.Contribute(ctx, vaultDir, mount, true)
		if planErr != nil {
			return contributeFail("contribute_failed", planErr.Error(), "")
		}
		ops := make([]string, 0, len(plan.Ops)+2)
		for _, op := range plan.Ops {
			ops = append(ops, op.String())
		}
		if sweptCount > 0 {
			ops = append(ops, fmt.Sprintf("sweep %d uncommitted edit(s) from failed auto-commits into the commit", sweptCount))
		}
		if plan.Counts.Changes() > 0 || sweptCount > 0 {
			ops = append(ops, "commit on "+branch)
		}
		if asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}

	emitSuccess := func(commit string, committed bool, counts den.FlowCounts, warnings []string) int {
		if warnings == nil {
			warnings = []string{}
		}
		checkoutOut, pushCommand := "", ""
		if committed {
			checkoutOut = worktree
			pushCommand = fmt.Sprintf("git -C %s push -u origin %s", shellQuoteArg(worktree), branch)
		}
		if asJSON {
			return printDenJSON(jsonContributeEnvelope{
				Schema:      1,
				DenID:       denID,
				Link:        jsonLinkBody{Target: target, Mode: den.LinkModeEdit, Warren: mount.WarrenID, Project: mount.ProjectID},
				Branch:      branch,
				Commit:      commit,
				Committed:   committed,
				Checkout:    checkoutOut,
				PushCommand: pushCommand,
				Contributed: jsonFlowCounts{
					Added:      counts.Added,
					Updated:    counts.Updated,
					Superseded: counts.Superseded,
					Noop:       counts.Noop,
				},
				Warnings: warnings,
			})
		}
		fmt.Printf("Contributed den %q to %s: +%d added ~%d updated ^%d superseded =%d noop\n",
			denID, target, counts.Added, counts.Updated, counts.Superseded, counts.Noop)
		if committed {
			fmt.Printf("Committed %s on branch %q (edit worktree %s).\n", commit, branch, worktree)
			fmt.Printf("Publish it with:\n  git -C %s push -u origin %s\nthen open a pull request in the warren repository. marmot never pushes for you.\n", shellQuoteArg(worktree), branch)
		} else {
			fmt.Println("Nothing to commit — the warren project is already up to date.")
		}
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		return 0
	}

	// The plan reads the branch tip itself, so a zero-change plan with no
	// sweepable auto-commit leftovers is a sound idempotent success with no
	// git operations at all. Sweepable dirt forces the commit path even when
	// the engine plans zero changes — the sweep IS the point of that run.
	plan, planErr := den.Contribute(ctx, vaultDir, mount, true)
	if planErr != nil {
		return contributeFail("contribute_failed", planErr.Error(), "")
	}
	if plan.Counts.Changes() == 0 && sweptCount == 0 {
		return emitSuccess("", false, plan.Counts, plan.Warnings)
	}

	// The whole mutate span — engine writes, sweep, pathspec-limited
	// add/commit, and failure recovery — runs under the per-warren cache
	// lock, serialized with every other cache mutation of this warren
	// (per-write auto-commits from live MCP serves included). No lock
	// nesting: the contribute engine writes through
	// warren.WriteEditableNodeFile, which never commits and never takes the
	// cache lock (only WriteEditableNode's autoCommitEditWrite does).
	//
	// Failure recovery: unstage and remove/restore exactly the files the
	// engine reported writing — no branch operations (the worktree stays on
	// its branch by construction), so a retry is a real contribute.
	// Sweepable leftovers are deliberately left in place on failure: they
	// predate this run and the next contribute sweeps them again.
	var result *den.ContributeResult
	var engineCreated, engineModified []string
	recoverEngineFiles := func() {
		restore := append(append([]string{}, engineCreated...), engineModified...)
		if len(restore) > 0 {
			_, _ = gitOutput(worktree, append([]string{"reset", "-q", "HEAD", "--"}, restore...)...)
		}
		if len(engineModified) > 0 {
			_, _ = gitOutput(worktree, append([]string{"checkout", "--"}, engineModified...)...)
		}
		if len(engineCreated) > 0 {
			_, _ = gitOutput(worktree, append([]string{"clean", "-qf", "--"}, engineCreated...)...)
		}
	}
	// Pathspec: excludes MUST precede the positive pathspec (git add stages
	// nothing otherwise — see proposeExcludePathspecs).
	pathspec := append(append([]string{"--"}, proposeExcludePathspecs...), relProject)
	failStep := ""
	committed := false
	lockErr := gitx.WithCacheLock(home.WarrenCacheDir(), wtWarrenID, func() error {
		res, err := den.Contribute(ctx, vaultDir, mount, false)
		if res != nil {
			result = res
			engineCreated, engineModified = res.CreatedFiles, res.ModifiedFiles
		}
		if err != nil {
			recoverEngineFiles()
			failStep = "contribute engine"
			return err
		}
		if result.Counts.Changes() == 0 && sweptCount == 0 {
			return nil // idempotent noop: nothing to commit
		}
		commitMsg := fmt.Sprintf("marmot contribute: den=%s project=%s +%d ~%d ^%d",
			denID, mount.ProjectID, result.Counts.Added, result.Counts.Updated, result.Counts.Superseded)
		if _, addErr := gitOutput(worktree, append([]string{"add"}, pathspec...)...); addErr != nil {
			recoverEngineFiles()
			failStep = "git add"
			return addErr
		}
		if _, cErr := gitOutput(worktree, append([]string{"commit", "-m", commitMsg}, pathspec...)...); cErr != nil {
			recoverEngineFiles()
			failStep = "git commit on " + branch
			return cErr
		}
		committed = true
		return nil
	})
	if lockErr != nil {
		if failStep == "" {
			failStep = "cache lock"
		}
		return contributeFail("contribute_failed", failStep+": "+lockErr.Error(),
			"recovered: engine-written files removed/restored in the edit worktree; re-run after fixing the cause")
	}
	warnings := result.Warnings
	if committed && sweptCount > 0 {
		warnings = append(warnings, fmt.Sprintf("swept %d uncommitted edit(s) from failed auto-commits", sweptCount))
	}
	if !committed {
		return emitSuccess("", false, result.Counts, warnings)
	}
	commitSHA, _ := gitOutput(worktree, "rev-parse", "HEAD")
	return emitSuccess(commitSHA, true, result.Counts, warnings)
}

// classifyWorktreeDirt splits `git status --porcelain` output — already
// scoped to the edit worktree's project vault tree with .marmot-data excluded
// — into SWEEPABLE node markdown (*.md files, the only artifact a failed MCP
// auto-commit leaves behind: autoCommitEditWrite writes node files
// exclusively) and foreign dirt of any other shape. Renamed entries
// ("R  old -> new") and git-quoted paths (specials/escapes) are never
// auto-commit leftovers and classify as foreign. Foreign entries keep the
// full porcelain line for the refusal message; sweepable entries carry the
// bare path.
func classifyWorktreeDirt(porcelain string) (sweepable, foreign []string) {
	for _, line := range strings.Split(porcelain, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path := line
		if len(path) > 3 {
			path = path[3:] // strip the two-column "XY " porcelain status prefix
		}
		if strings.Contains(line, " -> ") || strings.HasPrefix(path, `"`) || !strings.HasSuffix(path, ".md") {
			foreign = append(foreign, line)
			continue
		}
		sweepable = append(sweepable, path)
	}
	return sweepable, foreign
}

// multiString is a flag.Value that collects repeated --project values.
type multiString []string

func (m *multiString) String() string { return strings.Join(*m, ",") }
func (m *multiString) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// parseRefSpec parses one `--ref name=<n>,url=<u>,path=<p>,ref=<r>` value
// (the §15.5 machine grammar). Keys are optional and order-free, but at
// least one of url/path must be present for the spec to be resolvable.
func parseRefSpec(raw string) (warren.RefSpec, error) {
	var spec warren.RefSpec
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return spec, fmt.Errorf("ref component %q is not key=value (keys: name,url,path,ref)", part)
		}
		switch k {
		case "name":
			spec.Name = v
		case "url":
			spec.URL = v
		case "path":
			spec.Path = v
		case "ref":
			spec.GitRef = v
		default:
			return spec, fmt.Errorf("unknown ref key %q (keys: name,url,path,ref)", k)
		}
	}
	if spec.URL == "" && spec.Path == "" {
		return spec, fmt.Errorf("ref %q needs url= or path= to resolve by", raw)
	}
	return spec, nil
}

// refLinkResult is one resolved --ref outcome: the link to add (nil =
// skipped), the envelope ref label, the resolved_via vocabulary value, the
// checkout vault dir to route-register for checkout-vault matches, and a
// skip warning for none.
type refLinkResult struct {
	link     *den.Link
	ref      string
	via      string
	vaultDir string
	warning  string
}

// resolveRefSpecs maps --ref specs onto den links via warren.ResolveReference
// (§15.5): warren-url → pinned link (mode=link, pinned to the warren's cache
// pin); checkout-vault → live link on the vault id (the checkout's vault dir
// is route-registered so federation can resolve it); none → skipped with a
// warning. Read-only: nothing is written here.
func resolveRefSpecs(specs []warren.RefSpec) []refLinkResult {
	out := make([]refLinkResult, 0, len(specs))
	for _, spec := range specs {
		label := spec.Name
		if label == "" {
			label = spec.URL
		}
		if label == "" {
			label = spec.Path
		}
		res := warren.ResolveReference(spec)
		switch res.Via {
		case warren.ResolvedViaWarrenURL:
			target := res.WarrenID + "/" + res.ProjectID
			out = append(out, refLinkResult{
				link: &den.Link{
					Target:    target,
					Mode:      den.LinkModeLink,
					Warren:    res.WarrenID,
					Project:   res.ProjectID,
					PinnedRef: warren.ReadCachePin(res.WarrenID),
				},
				ref: target,
				via: res.Via,
			})
		case warren.ResolvedViaCheckoutVault:
			out = append(out, refLinkResult{
				link:     &den.Link{Target: res.VaultID, Mode: den.LinkModeLive},
				ref:      label,
				via:      res.Via,
				vaultDir: checkoutVaultDir(spec.Path),
			})
		default:
			out = append(out, refLinkResult{
				ref:     label,
				via:     warren.ResolvedViaNone,
				warning: fmt.Sprintf("ref %s: no warren source_url or checkout vault_id match; skipped", label),
			})
		}
	}
	return out
}

// checkoutVaultDir maps a reference checkout path to its vault dir —
// <path>/.marmot by convention, or path itself when it directly holds
// _config.md (same probe warren.ResolveReference uses).
func checkoutVaultDir(path string) string {
	conventional := filepath.Join(path, ".marmot")
	if _, err := os.Stat(filepath.Join(conventional, "_config.md")); err == nil {
		return conventional
	}
	if _, err := os.Stat(filepath.Join(path, "_config.md")); err == nil {
		return path
	}
	return conventional
}

func denCreate(args []string) int {
	// Flags may follow the den-id positional (stave: `den create demo --json ...`).
	args = reorderInterspersedFlags(args,
		map[string]bool{"lifetime": true, "project": true, "embedding-provider": true, "embedding-model": true, "ref": true},
		map[string]bool{"no-pointer": true, "no-vault": true, "dry-run": true, "json": true},
	)
	fs := flag.NewFlagSet("den create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	lifetime := fs.String("lifetime", "durable", "den lifetime: task|durable")
	var projects multiString
	fs.Var(&projects, "project", "absolute project path (repeatable)")
	var refSpecsRaw multiString
	fs.Var(&refSpecsRaw, "ref", "reference repo spec name=<n>,url=<u>,path=<p>,ref=<r> (repeatable; resolved into links)")
	noPointer := fs.Bool("no-pointer", false, "do not write .marmot-vault into project paths")
	noVault := fs.Bool("no-vault", false, "links-only den (skip identity vault)")
	embProvider := fs.String("embedding-provider", "", "identity vault embedding provider: openai|mock (default mock)")
	embModel := fs.String("embedding-model", "", "identity vault embedding model (provider default when empty)")
	dryRun := fs.Bool("dry-run", false, "print planned ops without writing")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, "marmot den create <den-id> [flags]")
	}
	if fs.NArg() < 1 {
		msg := "den create: missing den-id"
		if *asJSON {
			return denJSONError("invalid_args", msg, "marmot den create <den-id> [--lifetime task|durable] [--project <abs>] [--no-pointer] [--json]")
		}
		fmt.Fprintln(os.Stderr, msg)
		fmt.Fprintln(os.Stderr, "usage: marmot den create <den-id> [--lifetime task|durable] [--project <abs>]... [--no-pointer] [--no-vault] [--dry-run] [--json]")
		return 1
	}
	denID := fs.Arg(0)

	// Parse --ref specs up front so a malformed spec is refused before any
	// persistence (resolution itself is read-only and runs after create).
	specs := make([]warren.RefSpec, 0, len(refSpecsRaw))
	for _, raw := range refSpecsRaw {
		spec, err := parseRefSpec(raw)
		if err != nil {
			if *asJSON {
				return denJSONError("invalid_args", err.Error(), "--ref name=<n>,url=<u>,path=<p>,ref=<r>")
			}
			fmt.Fprintf(os.Stderr, "den create: %v\n", err)
			return 1
		}
		specs = append(specs, spec)
	}

	// Default project to cwd when none given (except pure dry-run inspection is fine either way).
	projList := []string(projects)
	if len(projList) == 0 {
		if cwd, err := os.Getwd(); err == nil {
			projList = []string{cwd}
		}
	}

	res, err := den.Create(denID, den.CreateOptions{
		Lifetime:          *lifetime,
		Projects:          projList,
		NoVault:           *noVault,
		NoPointer:         *noPointer,
		DryRun:            *dryRun,
		EmbeddingProvider: *embProvider,
		EmbeddingModel:    *embModel,
	})
	if err != nil {
		if *asJSON {
			return denJSONError("den_create_failed", err.Error(), "marmot den create "+denID+" --lifetime task --project /path --json")
		}
		fmt.Fprintf(os.Stderr, "den create: %v\n", err)
		return 1
	}

	refResults := resolveRefSpecs(specs)

	if *dryRun {
		ops := append([]string{}, res.Ops...)
		for _, r := range refResults {
			if r.link == nil {
				ops = append(ops, "skip "+r.warning)
				continue
			}
			ops = append(ops, fmt.Sprintf("den manifest: append link %s mode=%s (resolved via %s)", r.link.Target, r.link.Mode, r.via))
			if r.vaultDir != "" {
				ops = append(ops, fmt.Sprintf("routes.Set %s -> %s (if missing)", r.link.Target, r.vaultDir))
			}
		}
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}

	// Apply resolved --ref links: pinned links are pure manifest entries; a
	// checkout-vault live link additionally route-registers the checkout's
	// vault dir (if unrouted) so federation can resolve the vault id.
	envLinks := make([]jsonCreateLink, 0, len(refResults))
	for _, r := range refResults {
		if r.link == nil {
			res.Warnings = append(res.Warnings, r.warning)
			envLinks = append(envLinks, jsonCreateLink{Ref: r.ref, Mode: nil, ResolvedVia: r.via})
			continue
		}
		if r.vaultDir != "" {
			vid, vdir := r.link.Target, r.vaultDir
			if rerr := routes.Update(func(rt *routes.RoutingTable) error {
				if _, ok := rt.Get(vid); !ok {
					rt.Set(vid, vdir)
				}
				return nil
			}); rerr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("routes.Set %s: %v", vid, rerr))
			}
		}
		if _, lerr := den.AddLink(res.DenID, *r.link); lerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("link %s: %v", r.link.Target, lerr))
			envLinks = append(envLinks, jsonCreateLink{Ref: r.ref, Mode: nil, ResolvedVia: r.via})
			continue
		}
		mode := r.link.Mode
		envLinks = append(envLinks, jsonCreateLink{Ref: r.ref, Mode: &mode, ResolvedVia: r.via})
	}

	if *asJSON {
		env := jsonCreateEnvelope{
			Schema:         1,
			DenID:          res.DenID,
			DenPath:        res.DenPath,
			VaultID:        res.VaultID,
			Routes:         map[string]string{},
			PointerWritten: res.PointerWritten,
			Links:          envLinks,
			Warnings:       res.Warnings,
		}
		if env.Warnings == nil {
			env.Warnings = []string{}
		}
		if len(res.Projects) > 0 {
			// Contract shape: routes.project_path is the primary registered path.
			env.Routes["project_path"] = res.Projects[0]
		}
		return printDenJSON(env)
	}

	fmt.Printf("Created den %q at %s\n", res.DenID, res.DenPath)
	if res.VaultID != "" {
		fmt.Printf("  vault_id: %s\n", res.VaultID)
	} else {
		fmt.Println("  vault: (none — links-only)")
	}
	for _, p := range res.Projects {
		fmt.Printf("  project: %s\n", p)
	}
	for _, l := range envLinks {
		if l.Mode == nil {
			fmt.Printf("  ref %s: unresolved (skipped)\n", l.Ref)
			continue
		}
		fmt.Printf("  link: %s mode=%s (resolved via %s)\n", l.Ref, *l.Mode, l.ResolvedVia)
	}
	if res.PointerWritten {
		fmt.Println("  pointer: written")
	} else if *noPointer {
		fmt.Println("  pointer: skipped (--no-pointer)")
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return 0
}

func denStatus(args []string) int {
	args = reorderInterspersedFlags(args, nil, map[string]bool{"json": true})
	fs := flag.NewFlagSet("den status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, "marmot den status [<den-id>] [--json]")
	}
	denID := ""
	if fs.NArg() >= 1 {
		denID = fs.Arg(0)
	}
	if denID == "" {
		// Resolve from cwd reverse route or pointer.
		cwd, err := os.Getwd()
		if err != nil {
			if *asJSON {
				return denJSONError("den_not_found", "cannot determine den from cwd", "pass den-id: marmot den status <den-id> --json")
			}
			fmt.Fprintf(os.Stderr, "den status: %v\n", err)
			return 1
		}
		if id, err := resolveDenFromProject(cwd); err == nil && id != "" {
			denID = id
		} else {
			msg := "den status: missing den-id and no reverse route/pointer for cwd"
			if *asJSON {
				return denJSONError("den_not_found", msg, "marmot den status <den-id> --json")
			}
			fmt.Fprintln(os.Stderr, msg)
			return 1
		}
	}

	st, err := den.Status(denID)
	if err != nil {
		if *asJSON {
			return denJSONError("den_not_found", fmt.Sprintf("den %q not found", denID),
				fmt.Sprintf("create one: marmot den create %s --lifetime task --project /path/to/project --json", denID))
		}
		fmt.Fprintf(os.Stderr, "den status: %v\n", err)
		return 1
	}

	// Real freshness per link (§9): git numbers for edit/pinned links, route
	// reachability for live links. Failures degrade to stderr warnings.
	ctx := context.Background()
	client := gitx.New()
	fresh := make([]linkFreshness, len(st.Links))
	for i, l := range st.Links {
		fresh[i] = denLinkFreshness(ctx, client, st.DenID, l)
		for _, w := range fresh[i].warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
	}

	if *asJSON {
		env := jsonStatusEnvelope{
			Schema:   1,
			DenID:    st.DenID,
			Lifetime: st.Lifetime,
			VaultID:  st.VaultID,
			Projects: st.Projects,
			Links:    make([]jsonStatusLink, 0, len(st.Links)),
		}
		if env.Projects == nil {
			env.Projects = []string{}
		}
		for i, l := range st.Links {
			ref := l.Target
			if l.Warren != "" && l.Project != "" {
				ref = l.Warren + "/" + l.Project
			}
			f := fresh[i]
			sl := jsonStatusLink{
				Ref:          ref,
				Mode:         l.Mode,
				PinnedCommit: nil,
				Ahead:        f.ahead,
				Behind:       f.behind,
				PendingEdits: f.pendingEdits,
				State:        f.state,
				SourceCommit: f.sourceCommit,
			}
			if f.pinned != "" {
				p := f.pinned
				sl.PinnedCommit = &p
			}
			env.Links = append(env.Links, sl)
		}
		return printDenJSON(env)
	}

	fmt.Printf("den: %s\n", st.DenID)
	fmt.Printf("  path: %s\n", st.DenPath)
	fmt.Printf("  lifetime: %s\n", st.Lifetime)
	if st.VaultID != "" {
		fmt.Printf("  vault_id: %s\n", st.VaultID)
	} else {
		fmt.Println("  vault_id: (none)")
	}
	if len(st.Projects) == 0 {
		fmt.Println("  projects: (none)")
	} else {
		fmt.Println("  projects:")
		for _, p := range st.Projects {
			fmt.Printf("    %s\n", p)
		}
	}
	if len(st.Links) == 0 {
		fmt.Println("  links: (none)")
	} else {
		fmt.Println("  links:")
		for i, l := range st.Links {
			f := fresh[i]
			line := fmt.Sprintf("    %s mode=%s", l.Target, l.Mode)
			switch l.Mode {
			case den.LinkModeEdit:
				line += fmt.Sprintf(" ahead=%d behind=%d pending=%d", f.ahead, f.behind, f.pendingEdits)
			case den.LinkModeLink:
				if f.pinned != "" {
					line += " pinned=" + f.pinned
				}
				line += fmt.Sprintf(" behind=%d", f.behind)
			}
			fmt.Printf("%s (%s)\n", line, denLinkStateLabel(f.state))
			if f.sourceCommit != "" {
				fmt.Printf("      vault snapshot from source commit %s\n", f.sourceCommit)
			}
		}
	}
	return 0
}

// denEditWorktree is one cache edit worktree backing a den's edit links
// (per-(warren, den) granularity: every edit link into one warren shares it).
type denEditWorktree struct {
	warrenID string
	path     string
	branch   string
}

// denEditWorktrees lists the den's cache edit worktrees that exist on disk,
// deduped per warren. Legacy registered-checkout edit links have no cache
// worktree and never appear here (their unpushed count stays 0 — there is no
// reliable upstream to count against in a user-managed checkout).
func denEditWorktrees(denID string, links []den.Link) []denEditWorktree {
	seen := map[string]bool{}
	var out []denEditWorktree
	for _, l := range links {
		if l.Mode != den.LinkModeEdit || l.Warren == "" || seen[l.Warren] {
			continue
		}
		seen[l.Warren] = true
		wt := warren.CacheEditWorktreePath(l.Warren, denID)
		if !dirExistsCLI(wt) {
			continue
		}
		out = append(out, denEditWorktree{warrenID: l.Warren, path: wt, branch: warren.CacheEditBranch(denID, l.Warren)})
	}
	return out
}

// denUnpushedEdits counts the den's unpublished edits: per worktree, commits
// on the edit branch not reachable from its upstream — origin/<edit-branch>
// when the branch was ever pushed, else origin/<default-branch> (registry),
// else the cache pin — plus 1 when the worktree carries uncommitted engine
// writes (a failed auto-commit/contribute; forced worktree removal would
// discard those silently). The final bool is `degraded`: true when any
// upstream-resolution, ahead/behind, or dirty-status check failed, so the count
// undercounts real unpushed work. Callers that gate a destructive step (den
// destroy) MUST treat a degraded count as unknown and fail closed unless
// --force; a warning-only caller may ignore it.
func denUnpushedEdits(ctx context.Context, client *gitx.Client, wts []denEditWorktree) (int, []string, bool) {
	unpushed := 0
	var warnings []string
	degraded := false
	for _, wt := range wts {
		upstream := editBranchUpstream(ctx, client, wt.warrenID, wt.branch)
		if upstream == "" {
			degraded = true
			warnings = append(warnings, fmt.Sprintf("cannot resolve an upstream for edit branch %s of warren %q; its unpushed edits could not be counted", wt.branch, wt.warrenID))
			continue
		}
		ahead, _, err := client.AheadBehind(ctx, wt.path, upstream, wt.branch)
		if err != nil {
			degraded = true
			warnings = append(warnings, fmt.Sprintf("counting unpushed edits on %s failed: %v", wt.branch, err))
			continue
		}
		unpushed += ahead
		dirtyOut, dErr := client.StatusPorcelain(ctx, wt.path, proposeExcludePathspecs...)
		if dErr != nil {
			degraded = true
			warnings = append(warnings, fmt.Sprintf("checking uncommitted edits on %s failed: %v", wt.branch, dErr))
			continue
		}
		if strings.TrimSpace(dirtyOut) != "" {
			unpushed++
		}
	}
	return unpushed, warnings, degraded
}

// editBranchUpstream resolves the upstream an edit branch's unpushed work is
// counted against: origin/<edit-branch> when the branch was ever pushed, else
// origin/<default-branch> from the global registry, else the cache pin
// commit. "" when none resolves.
func editBranchUpstream(ctx context.Context, client *gitx.Client, warrenID, branch string) string {
	bare := warren.CacheBarePath(warrenID)
	if _, err := client.Output(ctx, "--git-dir", bare, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch); err == nil {
		return "origin/" + branch
	}
	if reg, regErr := warrenreg.Load(); regErr == nil {
		if entry, ok := reg.Warrens[warrenID]; ok && entry.DefaultBranch != "" {
			return "origin/" + entry.DefaultBranch
		}
	}
	return warren.ReadCachePin(warrenID)
}

// denLinkStateLabel maps the machine state vocabulary (kept verbatim in JSON)
// to the human status line. Only 'stale' is reworded: a pinned link is not a
// pin-enforced serving guarantee today (§18.4), so 'stale' reads as "behind
// link baseline" — the shared checkout has advanced past the link-time
// baseline recorded for skew — rather than implying an unmet pin.
func denLinkStateLabel(state string) string {
	if state == "stale" {
		return "behind link baseline"
	}
	return state
}

// linkFreshness carries den status's per-link freshness numbers (§9).
type linkFreshness struct {
	pinned       string
	ahead        int
	behind       int
	pendingEdits int
	state        string // ok | unpushed | stale | unreachable
	sourceCommit string
	warnings     []string
}

// denLinkFreshness computes real freshness for one den link:
//
//   - edit (cache-backed): ahead/behind of the edit branch vs its upstream;
//     pending_edits = ahead + 1 when the worktree carries uncommitted engine
//     writes; state unpushed when pending. Legacy registered-checkout links
//     have no reliable upstream and stay at zeros/ok.
//   - link (pinned): pinned commit from the link's recorded pin (else the
//     current cache pin); behind = commits between the pin and
//     origin/<default-branch> in the bare cache; state stale when behind.
//     source_commit skew from warren manifest v3 provenance.
//   - live: state unreachable when neither a route nor a den identity vault
//     answers for the target.
//
// Every git/registry failure degrades to a warning, never a hard failure.
func denLinkFreshness(ctx context.Context, client *gitx.Client, denID string, l den.Link) linkFreshness {
	f := linkFreshness{state: "ok"}
	switch l.Mode {
	case den.LinkModeEdit:
		if l.Warren == "" {
			return f
		}
		wt := warren.CacheEditWorktreePath(l.Warren, denID)
		if !dirExistsCLI(wt) {
			return f
		}
		branch := warren.CacheEditBranch(denID, l.Warren)
		upstream := editBranchUpstream(ctx, client, l.Warren, branch)
		if upstream == "" {
			f.warnings = append(f.warnings, fmt.Sprintf("cannot resolve an upstream for edit branch %s of warren %q; its freshness was not computed", branch, l.Warren))
			return f
		}
		ahead, behind, err := client.AheadBehind(ctx, wt, upstream, branch)
		if err != nil {
			f.warnings = append(f.warnings, fmt.Sprintf("freshness of edit branch %s failed: %v", branch, err))
			return f
		}
		f.ahead, f.behind = ahead, behind
		f.pendingEdits = ahead
		if out, dErr := client.StatusPorcelain(ctx, wt, proposeExcludePathspecs...); dErr == nil && strings.TrimSpace(out) != "" {
			f.pendingEdits++
		}
		if f.pendingEdits > 0 {
			f.state = "unpushed"
		}
	case den.LinkModeLink:
		pin := l.PinnedRef
		if pin == "" && l.Warren != "" {
			pin = warren.ReadCachePin(l.Warren)
		}
		f.pinned = pin
		f.sourceCommit = den.PinnedLinkSourceCommit(l)
		if pin == "" || l.Warren == "" {
			return f
		}
		bare := warren.CacheBarePath(l.Warren)
		if !dirExistsCLI(bare) {
			return f
		}
		def := ""
		if reg, regErr := warrenreg.Load(); regErr == nil {
			if entry, ok := reg.Warrens[l.Warren]; ok {
				def = entry.DefaultBranch
			}
		}
		if def == "" {
			if b, bErr := client.RemoteDefaultBranch(ctx, bare); bErr == nil {
				def = b
			}
		}
		if def == "" {
			return f
		}
		out, err := client.Output(ctx, "--git-dir", bare, "rev-list", "--count", pin+"..origin/"+def)
		if err != nil {
			f.warnings = append(f.warnings, fmt.Sprintf("freshness of pinned link %s failed: %v", l.Target, err))
			return f
		}
		if n, cerr := strconv.Atoi(strings.TrimSpace(out)); cerr == nil {
			f.behind = n
		}
		if f.behind > 0 {
			f.state = "stale"
		}
	case den.LinkModeLive:
		if !den.LiveTargetReachable(l.Target) {
			f.state = "unreachable"
		}
	}
	return f
}

func denDestroy(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{"promote": true}, map[string]bool{"force": true, "dry-run": true, "json": true})
	fs := flag.NewFlagSet("den destroy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "destroy even with unpushed edit-worktree commits (edit branches always survive in the shared cache)")
	promoteTarget := fs.String("promote", "", "fold this den's vault knowledge into <target-den-id>'s identity vault before destroying")
	dryRun := fs.Bool("dry-run", false, "print planned ops without writing")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, "marmot den destroy <den-id> [--promote <target-den-id>] [--force] [--dry-run] [--json]")
	}
	if fs.NArg() < 1 {
		msg := "den destroy: missing den-id"
		if *asJSON {
			return denJSONError("invalid_args", msg, "marmot den destroy <den-id> [--json]")
		}
		fmt.Fprintln(os.Stderr, msg)
		return 1
	}
	denID := fs.Arg(0)

	destroyFail := func(code, msg, hint string) int {
		if *asJSON {
			return denJSONError(code, msg, hint)
		}
		fmt.Fprintf(os.Stderr, "den destroy: %s\n", msg)
		if hint != "" {
			fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
		}
		return 1
	}

	// Promote-target validation (§15.3 sibling): every refusal here fires
	// BEFORE anything is destroyed. The target must be a different, existing
	// den with an identity vault; the SOURCE den may legitimately lack one
	// (links-only) — that degrades to a zero-count promote with a warning.
	srcVault, tgtVault := den.VaultPath(denID), ""
	if *promoteTarget != "" {
		if *promoteTarget == denID {
			return destroyFail("invalid_args", fmt.Sprintf("den %q cannot promote into itself", denID), "")
		}
		if _, err := den.Status(*promoteTarget); err != nil {
			return destroyFail("promote_target_not_found",
				fmt.Sprintf("promote target den %q not found: %v — nothing destroyed", *promoteTarget, err),
				"marmot den list")
		}
		tgtVault = den.VaultPath(*promoteTarget)
		if _, err := os.Stat(filepath.Join(tgtVault, "_config.md")); err != nil {
			return destroyFail("promote_target_no_vault",
				fmt.Sprintf("promote target den %q has no identity vault at %s — nothing destroyed", *promoteTarget, tgtVault),
				"recreate it without --no-vault, or pick a target with a vault")
		}
	}

	if *dryRun {
		// Load to list ops without destroying.
		st, err := den.Status(denID)
		if err != nil {
			if *asJSON {
				return denJSONError("den_not_found", fmt.Sprintf("den %q not found", denID),
					fmt.Sprintf("create one: marmot den create %s --lifetime task --project /path --json", denID))
			}
			fmt.Fprintf(os.Stderr, "den destroy: %v\n", err)
			return 1
		}
		var ops []string
		if *promoteTarget != "" {
			if dirExistsCLI(srcVault) {
				plan, planErr := den.Promote(context.Background(), srcVault, tgtVault, true)
				if planErr != nil {
					return destroyFail("promote_failed", planErr.Error(), "nothing destroyed")
				}
				for _, op := range plan.Ops {
					ops = append(ops, "promote: "+op.String())
				}
			} else {
				ops = append(ops, fmt.Sprintf("note: den %q has no identity vault; nothing to promote", denID))
			}
		}
		ops = append(ops, "rm -rf "+st.DenPath)
		for _, p := range st.Projects {
			ops = append(ops, "routes.RemoveProject "+p)
		}
		for _, wt := range denEditWorktrees(denID, st.Links) {
			ops = append(ops, fmt.Sprintf("git worktree remove %s (branch %s kept in the shared cache)", wt.path, wt.branch))
		}
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}

	// Unpushed-edit refusal (cache edit worktrees only): destroying a den
	// whose edit branches carry commits its author never pushed would silently
	// bury knowledge staged for review. The commits DO survive in the bare
	// cache either way — the refusal is about un-published work, and --force
	// acknowledges it. A missing den falls through to den.Destroy for the
	// canonical not-found error.
	unpushed := 0
	var wts []denEditWorktree
	var destroyWarnings []string
	warn := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		destroyWarnings = append(destroyWarnings, msg)
		fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
	}
	ctx := context.Background()
	client := gitx.New()
	if st, stErr := den.Status(denID); stErr == nil {
		wts = denEditWorktrees(denID, st.Links)
		if len(wts) > 0 {
			var countWarnings []string
			var degraded bool
			unpushed, countWarnings, degraded = denUnpushedEdits(ctx, client, wts)
			for _, w := range countWarnings {
				warn("%s", w)
			}
			// Fail CLOSED when the count is unknown: a degraded git
			// environment (upstream/ahead-behind/status errors) undercounts
			// real unpushed work, so destroying without --force could discard
			// review-staged knowledge silently. --force acknowledges it.
			if degraded && !*force {
				msg := fmt.Sprintf("den %q: could not determine unpushed edit state (git upstream/status checks failed)", denID)
				hint := "resolve the git environment and retry, or re-run with --force to destroy anyway — edit branches always survive in the shared cache"
				if *asJSON {
					return denJSONError("unpushed_unknown", msg, hint)
				}
				fmt.Fprintf(os.Stderr, "den destroy: %s\n", msg)
				fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
				return 1
			}
			if unpushed > 0 && !*force {
				msg := fmt.Sprintf("den %q has %d unpushed edit(s) on its edit branch(es); they were never pushed for review", denID, unpushed)
				hint := "push them first (git -C <worktree> push -u origin <branch>, see den contribute's push_command) or re-run with --force — edit branches always survive in the shared cache"
				if *asJSON {
					return denJSONError("unpushed_edits", msg, hint)
				}
				fmt.Fprintf(os.Stderr, "den destroy: %s\n", msg)
				fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
				return 1
			}
		}
	}

	// Promote BEFORE any destructive step (worktree removal included): a
	// promote failure is a structured refusal with everything intact. The
	// engine is den.Promote — same classifier machinery as contribute, but
	// the target is a live local den vault, so node writes go through its
	// store AND its embeddings are updated with the target's embedder.
	var promoted *jsonFlowCounts
	if *promoteTarget != "" {
		if dirExistsCLI(srcVault) {
			promRes, promErr := den.Promote(ctx, srcVault, tgtVault, false)
			if promErr != nil {
				return destroyFail("promote_failed",
					fmt.Sprintf("promoting den %q into %q failed: %v — nothing destroyed", denID, *promoteTarget, promErr), "")
			}
			for _, w := range promRes.Warnings {
				warn("%s", w)
			}
			promoted = &jsonFlowCounts{
				Added:      promRes.Counts.Added,
				Updated:    promRes.Counts.Updated,
				Superseded: promRes.Counts.Superseded,
				Noop:       promRes.Counts.Noop,
			}
		} else {
			warn("den %q has no identity vault; nothing to promote", denID)
			promoted = &jsonFlowCounts{}
		}
	}

	// Remove the den's edit worktrees under the per-warren cache lock.
	// NEVER the branches: the knowledge stays in the bare repo.
	for _, wt := range wts {
		bare := warren.CacheBarePath(wt.warrenID)
		if err := gitx.WithCacheLock(home.WarrenCacheDir(), wt.warrenID, func() error {
			if err := client.WorktreeRemove(ctx, bare, wt.path, true); err != nil {
				return err
			}
			return client.WorktreePrune(ctx, bare)
		}); err != nil {
			warn("removing edit worktree %s failed: %v (branch %s is unaffected)", wt.path, err, wt.branch)
		}
	}

	res, err := den.Destroy(denID, *force)
	if err != nil {
		if *asJSON {
			code := "den_destroy_failed"
			if strings.Contains(err.Error(), "not found") {
				code = "den_not_found"
			}
			return denJSONError(code, err.Error(),
				fmt.Sprintf("create one: marmot den create %s --lifetime task --project /path/to/project --json", denID))
		}
		fmt.Fprintf(os.Stderr, "den destroy: %v\n", err)
		return 1
	}

	if *asJSON {
		if destroyWarnings == nil {
			destroyWarnings = []string{}
		}
		return printDenJSON(jsonDestroyEnvelope{
			Schema:        1,
			DenID:         res.DenID,
			Destroyed:     res.Destroyed,
			Kept:          res.Kept,
			UnpushedEdits: unpushed,
			Promoted:      promoted,
			Contributed:   nil,
			Warnings:      destroyWarnings,
		})
	}
	if promoted != nil {
		fmt.Printf("Promoted den %q into %q: +%d added ~%d updated ^%d superseded =%d noop\n",
			res.DenID, *promoteTarget, promoted.Added, promoted.Updated, promoted.Superseded, promoted.Noop)
	}
	fmt.Printf("Destroyed den %q\n", res.DenID)
	return 0
}

func denList(args []string) int {
	fs := flag.NewFlagSet("den list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, "marmot den list [--json]")
	}
	ids, err := den.List()
	if err != nil {
		if *asJSON {
			return denJSONError("den_list_failed", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "den list: %v\n", err)
		return 1
	}
	if *asJSON {
		if ids == nil {
			ids = []string{}
		}
		return printDenJSON(jsonListEnvelope{Schema: 1, Dens: ids})
	}
	if len(ids) == 0 {
		fmt.Println("No dens.")
		return 0
	}
	for _, id := range ids {
		fmt.Println(id)
	}
	return 0
}

// jsonAdoptEnvelope is the schema:1 success envelope for den adopt. It keeps
// the create-shaped fields adopt has always emitted and adds (additively)
// vault_moved / configs_rewritten — see testdata/contracts/den_adopt.v1.json.
type jsonAdoptEnvelope struct {
	Schema           int               `json:"schema"`
	DenID            string            `json:"den_id"`
	DenPath          string            `json:"den_path"`
	VaultID          string            `json:"vault_id"`
	Routes           map[string]string `json:"routes"`
	VaultMoved       bool              `json:"vault_moved"`
	PointerWritten   bool              `json:"pointer_written"`
	ConfigsRewritten []string          `json:"configs_rewritten"`
	Links            []jsonCreateLink  `json:"links"`
	Warnings         []string          `json:"warnings"`
}

// denAdopt migrates an in-repo .marmot vault into a den: the engine
// (den.Adopt) moves the vault, registers the route, and writes the pointer;
// this layer additionally rewrites project-local MCP configs that embed
// `serve --dir <old-vault>` to `serve --den <den-id>` (exec/config concerns
// stay out of internal packages). Refusals carry structured codes:
// not_a_vault, den_vault_exists, move_failed.
func denAdopt(args []string) int {
	fs := flag.NewFlagSet("den adopt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	from := fs.String("from", "", "project path containing in-repo .marmot (default: cwd)")
	id := fs.String("id", "", "den id (default: source vault_id, else basename of project)")
	noPointer := fs.Bool("no-pointer", false, "do not write the .marmot-vault pointer into the project root")
	noRewrite := fs.Bool("no-rewrite", false, "do not rewrite project-local MCP configs to serve --den")
	dryRun := fs.Bool("dry-run", false, "print planned ops without writing")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	const usage = "marmot den adopt [--from <project>] [--id <den-id>] [--no-pointer] [--no-rewrite] [--dry-run] [--json]"
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, usage)
	}
	adoptFail := func(code, msg, hint string) int {
		if *asJSON {
			return denJSONError(code, msg, hint)
		}
		fmt.Fprintf(os.Stderr, "den adopt: %s\n", msg)
		if hint != "" {
			fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
		}
		return 1
	}
	res, err := den.Adopt(den.AdoptOptions{
		From:      *from,
		DenID:     *id,
		DryRun:    *dryRun,
		NoPointer: *noPointer,
	})
	if err != nil {
		var refusal *den.AdoptRefusal
		if errors.As(err, &refusal) {
			return adoptFail(refusal.Code, refusal.Message, refusal.Hint)
		}
		return adoptFail("den_adopt_failed", err.Error(), usage)
	}

	// MCP config rewrites happen AFTER the engine work (real run) and are
	// planned against the pre-move vault path (the configs still reference
	// it). Warn+skip on unparseable files — never clobber.
	oldVaultAbs := den.SourceVaultPath(res.From)
	var plans []mcpRewrite
	var planWarnings []string
	if !*noRewrite {
		plans, planWarnings = planMCPRewrites(res.From, oldVaultAbs, res.DenID)
	}

	if *dryRun {
		ops := append([]string{}, res.Ops...)
		for _, p := range plans {
			ops = append(ops, p.Desc)
		}
		ops = append(ops, planWarnings...)
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}

	warnings := append([]string{}, res.Warnings...)
	warnings = append(warnings, planWarnings...)
	configsRewritten := []string{}
	for _, p := range plans {
		if err := p.apply(); err != nil {
			warnings = append(warnings, fmt.Sprintf("rewrite %s: %v", p.Path, err))
			continue
		}
		configsRewritten = append(configsRewritten, p.Path)
	}

	if *asJSON {
		return printDenJSON(jsonAdoptEnvelope{
			Schema:           1,
			DenID:            res.DenID,
			DenPath:          res.DenPath,
			VaultID:          res.DenID,
			Routes:           map[string]string{"project_path": res.From},
			VaultMoved:       res.VaultMoved,
			PointerWritten:   res.PointerWritten,
			ConfigsRewritten: configsRewritten,
			Links:            []jsonCreateLink{},
			Warnings:         warnings,
		})
	}
	fmt.Printf("Adopted den %q at %s (from %s)\n", res.DenID, res.DenPath, res.From)
	fmt.Printf("  vault: moved to %s\n", den.VaultPath(res.DenID))
	if res.PointerWritten {
		fmt.Println("  pointer: written")
	} else if *noPointer {
		fmt.Println("  pointer: skipped (--no-pointer)")
	}
	for _, p := range configsRewritten {
		fmt.Printf("  rewrote: %s (serve --den %s)\n", p, res.DenID)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return 0
}

// resolveDenFromProject tries reverse route then .marmot-vault pointer.
func resolveDenFromProject(projectPath string) (string, error) {
	// Lazy import pattern avoided — use den/routes directly.
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return "", err
	}
	// Pointer first is fine for status; routes is preferred for serve (P1a discovery).
	if id, err := den.ReadPointer(abs); err == nil && id != "" {
		return id, nil
	}
	// Reverse route via Load.
	return resolveProjectRoute(abs)
}
