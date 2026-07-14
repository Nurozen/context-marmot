package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nurozen/context-marmot/internal/den"
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
}

type jsonDestroyEnvelope struct {
	Schema        int             `json:"schema"`
	DenID         string          `json:"den_id"`
	Destroyed     bool            `json:"destroyed"`
	Kept          bool            `json:"kept"`
	UnpushedEdits int             `json:"unpushed_edits"`
	Promoted      *jsonFlowCounts `json:"promoted"`
	Contributed   *jsonFlowCounts `json:"contributed"`
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

func denUsage() {
	fmt.Fprintln(os.Stderr, "usage: marmot den <command> [flags]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  create <den-id>  [--lifetime task|durable] [--project <abs>]... [--no-pointer] [--no-vault] [--dry-run] [--json]")
	fmt.Fprintln(os.Stderr, "  status  [<den-id>] [--json]")
	fmt.Fprintln(os.Stderr, "  destroy <den-id> [--force] [--dry-run] [--json]")
	fmt.Fprintln(os.Stderr, "  list    [--json]")
	fmt.Fprintln(os.Stderr, "  adopt   [--from <project>] [--id <den-id>] [--dry-run] [--json]")
	fmt.Fprintln(os.Stderr, "  contribute <den-id> [<link>] [--dry-run] [--json]  # P4; requires edit-mode link")
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
	case "contribute":
		return denContribute(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "den: unknown subcommand %q\n", sub)
		denUsage()
		return 1
	}
}

// denContribute is the P4 flow-back verb skeleton. Full CRUD-classifier staging
// lands with edit-mode links (P3/P4). Until then we refuse clearly when no
// edit-mode link exists so stave memory propose surfaces a mode-naming error.
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
		if *asJSON {
			return denJSONError("invalid_args", err.Error(), "marmot den contribute <den-id> [<link>]")
		}
		return 1
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

	// Require an edit-mode link (target or explicit linkRef).
	hasEdit := false
	for _, l := range info.Links {
		if l.Mode != "edit" {
			continue
		}
		if linkRef == "" || l.Target == linkRef || (l.Warren != "" && l.Project != "" && l.Warren+"/"+l.Project == linkRef) {
			hasEdit = true
			break
		}
	}
	if !hasEdit {
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

	// Full classifier staging is P4 — refuse with clear not-implemented for now
	// once an edit link exists, so callers don't get silent no-ops.
	if *dryRun {
		ops := []string{fmt.Sprintf("contribute den=%s link=%s (classifier staging — not yet implemented)", denID, linkRef)}
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}
	msg := "den contribute classifier staging not yet implemented (P4); edit-mode link is present"
	if *asJSON {
		return denJSONError("not_implemented", msg, "await marmot P4 contribute")
	}
	fmt.Fprintf(os.Stderr, "den contribute: %s\n", msg)
	return 1
}

// multiString is a flag.Value that collects repeated --project values.
type multiString []string

func (m *multiString) String() string { return strings.Join(*m, ",") }
func (m *multiString) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func denCreate(args []string) int {
	// Flags may follow the den-id positional (stave: `den create demo --json ...`).
	args = reorderInterspersedFlags(args,
		map[string]bool{"lifetime": true, "project": true},
		map[string]bool{"no-pointer": true, "no-vault": true, "dry-run": true, "json": true},
	)
	fs := flag.NewFlagSet("den create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	lifetime := fs.String("lifetime", "durable", "den lifetime: task|durable")
	var projects multiString
	fs.Var(&projects, "project", "absolute project path (repeatable)")
	noPointer := fs.Bool("no-pointer", false, "do not write .marmot-vault into project paths")
	noVault := fs.Bool("no-vault", false, "links-only den (skip identity vault)")
	dryRun := fs.Bool("dry-run", false, "print planned ops without writing")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		if *asJSON {
			return denJSONError("invalid_args", err.Error(), "marmot den create <den-id> [flags]")
		}
		return 1
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

	// Default project to cwd when none given (except pure dry-run inspection is fine either way).
	projList := []string(projects)
	if len(projList) == 0 {
		if cwd, err := os.Getwd(); err == nil {
			projList = []string{cwd}
		}
	}

	res, err := den.Create(denID, den.CreateOptions{
		Lifetime: *lifetime,
		Projects:  projList,
		NoVault:   *noVault,
		NoPointer: *noPointer,
		DryRun:    *dryRun,
	})
	if err != nil {
		if *asJSON {
			return denJSONError("den_create_failed", err.Error(), "marmot den create "+denID+" --lifetime task --project /path --json")
		}
		fmt.Fprintf(os.Stderr, "den create: %v\n", err)
		return 1
	}

	if *dryRun {
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: res.Ops})
		}
		for _, op := range res.Ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}

	if *asJSON {
		env := jsonCreateEnvelope{
			Schema:         1,
			DenID:          res.DenID,
			DenPath:        res.DenPath,
			VaultID:        res.VaultID,
			Routes:         map[string]string{},
			PointerWritten: res.PointerWritten,
			Links:          []jsonCreateLink{},
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
		return 1
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
		for _, l := range st.Links {
			ref := l.Target
			if l.Warren != "" && l.Project != "" {
				ref = l.Warren + "/" + l.Project
			}
			sl := jsonStatusLink{
				Ref:          ref,
				Mode:         l.Mode,
				PinnedCommit: nil,
				Ahead:        0,
				Behind:       0,
				PendingEdits: 0,
				State:        "ok",
			}
			if l.PinnedRef != "" {
				p := l.PinnedRef
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
		for _, l := range st.Links {
			fmt.Printf("    %s mode=%s\n", l.Target, l.Mode)
		}
	}
	return 0
}

func denDestroy(args []string) int {
	args = reorderInterspersedFlags(args, nil, map[string]bool{"force": true, "dry-run": true, "json": true})
	fs := flag.NewFlagSet("den destroy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "force destroy (reserved; unpushed-edit refusal is P4)")
	dryRun := fs.Bool("dry-run", false, "print planned ops without writing")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		return 1
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
		ops := []string{"rm -rf " + st.DenPath}
		for _, p := range st.Projects {
			ops = append(ops, "routes.RemoveProject "+p)
		}
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: ops})
		}
		for _, op := range ops {
			fmt.Println("dry-run:", op)
		}
		return 0
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
		return printDenJSON(jsonDestroyEnvelope{
			Schema:        1,
			DenID:         res.DenID,
			Destroyed:     res.Destroyed,
			Kept:          res.Kept,
			UnpushedEdits: 0,
			Promoted:      nil,
			Contributed:   nil,
		})
	}
	fmt.Printf("Destroyed den %q\n", res.DenID)
	return 0
}

func denList(args []string) int {
	fs := flag.NewFlagSet("den list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		return 1
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

func denAdopt(args []string) int {
	fs := flag.NewFlagSet("den adopt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	from := fs.String("from", "", "project path containing in-repo .marmot (default: cwd)")
	id := fs.String("id", "", "den id (default: basename of project)")
	dryRun := fs.Bool("dry-run", false, "print planned ops without writing")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	res, err := den.Adopt(den.AdoptOptions{
		From:   *from,
		DenID:  *id,
		DryRun: *dryRun,
	})
	if err != nil {
		if *asJSON {
			return denJSONError("den_adopt_failed", err.Error(), "marmot den adopt --from <project> [--id <den-id>]")
		}
		fmt.Fprintf(os.Stderr, "den adopt: %v\n", err)
		return 1
	}
	if *dryRun {
		if *asJSON {
			return printDenJSON(jsonDryRunEnvelope{Schema: 1, DryRun: true, Ops: res.Ops})
		}
		for _, op := range res.Ops {
			fmt.Println("dry-run:", op)
		}
		return 0
	}
	if *asJSON {
		// Reuse create-shaped envelope for adopt success.
		env := jsonCreateEnvelope{
			Schema:         1,
			DenID:          res.DenID,
			DenPath:        res.DenPath,
			VaultID:        res.DenID,
			Routes:         map[string]string{"project_path": res.From},
			PointerWritten: true,
			Links:          []jsonCreateLink{},
			Warnings:       []string{res.Note},
		}
		return printDenJSON(env)
	}
	fmt.Printf("Adopted den %q at %s (from %s)\n", res.DenID, res.DenPath, res.From)
	if res.Note != "" {
		fmt.Fprintf(os.Stderr, "note: %s\n", res.Note)
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
