package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/nurozen/context-marmot/internal/warren"
)

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
	case "burrow":
		return warrenMount(subArgs, true)
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
	fmt.Fprintln(os.Stderr, "usage: marmot warren <init|project|bridge|doctor|format|register|list|mount|burrow|status|edit|refresh|propose> [flags]")
}

func warrenInit(args []string) int {
	fs := flag.NewFlagSet("warren init", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	rootCompat := fs.String("root", "", "Warren repository root")
	warrenID := fs.String("id", "", "Warren ID")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
	}
	if *warrenID == "" && fs.NArg() == 1 {
		*warrenID = fs.Arg(0)
	}
	if *warrenID == "" || fs.NArg() > 1 {
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
	fmt.Printf("Initialized Warren %q at %s\n", *warrenID, *root)
	return 0
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
	default:
		fmt.Fprintf(os.Stderr, "warren project: unknown subcommand %q\n", sub)
		warrenProjectUsage()
		return 1
	}
}

func warrenProjectUsage() {
	fmt.Fprintln(os.Stderr, "usage: marmot warren project <add|import|list|remove|rename> [flags]")
}

func warrenProjectAdd(args []string) int {
	args = reorderInterspersedFlags(args,
		map[string]bool{"warren-dir": true, "root": true, "path": true, "vault-id": true, "id": true, "aliases": true, "alias": true},
		map[string]bool{"generate-id": true},
	)
	fs := flag.NewFlagSet("warren project add", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	rootCompat := fs.String("root", "", "Warren repository root")
	path := fs.String("path", "", "project .marmot path inside the Warren")
	vaultID := fs.String("vault-id", "", "vault ID (default: project ID)")
	idCompat := fs.String("id", "", "project ID")
	aliasesCompat := fs.String("aliases", "", "comma-separated aliases")
	generateID := fs.Bool("generate-id", false, "generate the project ID from existing metadata or path")
	var aliases repeatedStringFlag
	fs.Var(&aliases, "alias", "project alias (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren project add <project-id> --path <project-.marmot> [--warren-dir .] [--vault-id <id>] [--alias <name>]...")
		return 1
	}
	aliases = append(aliases, splitCSV(*aliasesCompat)...)
	projectID := ""
	if fs.NArg() == 1 {
		projectID = fs.Arg(0)
	}
	if *idCompat != "" {
		projectID = *idCompat
		if *path == "" && fs.NArg() == 1 {
			*path = fs.Arg(0)
		}
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
		ProjectID: projectID,
		Path:      filepath.ToSlash(*path),
		Aliases:   aliases,
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
		map[string]bool{"warren-dir": true, "root": true, "path": true, "vault-id": true, "id": true, "aliases": true, "alias": true},
		map[string]bool{"generate-id": true, "include-heat": true, "no-obsidian": true},
	)
	fs := flag.NewFlagSet("warren project import", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	rootCompat := fs.String("root", "", "Warren repository root")
	path := fs.String("path", "", "destination .marmot path inside the Warren")
	vaultID := fs.String("vault-id", "", "vault ID (default: source vault_id or project ID)")
	idCompat := fs.String("id", "", "project ID")
	aliasesCompat := fs.String("aliases", "", "comma-separated aliases")
	generateID := fs.Bool("generate-id", false, "generate the project ID from existing metadata or source path")
	includeHeat := fs.Bool("include-heat", false, "include _heat/ files")
	noObsidian := fs.Bool("no-obsidian", false, "exclude .obsidian/ files")
	var aliases repeatedStringFlag
	fs.Var(&aliases, "alias", "project alias (repeatable)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
	}
	*root = resolveWarrenRoot(*root)
	aliases = append(aliases, splitCSV(*aliasesCompat)...)

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
	if *idCompat != "" {
		projectID = *idCompat
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
	opts := warren.ImportOptions{
		IncludeHeat: *includeHeat,
		NoObsidian:  *noObsidian,
		VaultID:     *vaultID,
	}
	if _, err := warren.ImportProject(*root, source, project, opts); err != nil {
		fmt.Fprintf(os.Stderr, "warren project import: %v\n", err)
		return 1
	}
	fmt.Printf("Imported project %q from %s -> %s\n", project.ProjectID, source, project.Path)
	return 0
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
	rootCompat := fs.String("root", "", "Warren repository root")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
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
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true, "root": true}, nil)
	fs := flag.NewFlagSet("warren project remove", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	rootCompat := fs.String("root", "", "Warren repository root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
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
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true, "root": true}, nil)
	fs := flag.NewFlagSet("warren project rename", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	rootCompat := fs.String("root", "", "Warren repository root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren project rename [--warren-dir .] <old-project-id> <new-project-id>")
		return 1
	}
	if _, err := warren.RenameProject(*root, fs.Arg(0), fs.Arg(1)); err != nil {
		fmt.Fprintf(os.Stderr, "warren project rename: %v\n", err)
		return 1
	}
	fmt.Printf("Renamed project %q -> %q\n", fs.Arg(0), fs.Arg(1))
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
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true, "root": true, "relations": true}, nil)
	fs := flag.NewFlagSet("warren bridge add", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	rootCompat := fs.String("root", "", "Warren repository root")
	relations := fs.String("relations", "references", "comma-separated allowed relations")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
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
	rootCompat := fs.String("root", "", "Warren repository root")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
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
	args = reorderInterspersedFlags(args, map[string]bool{"warren-dir": true, "root": true}, nil)
	fs := flag.NewFlagSet("warren bridge remove", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	rootCompat := fs.String("root", "", "Warren repository root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
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
	fs := flag.NewFlagSet("warren doctor", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	rootCompat := fs.String("root", "", "Warren repository root")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
	}
	*root = resolveWarrenRoot(*root)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren doctor [--warren-dir .] [--json]")
		return 1
	}
	report, err := warren.Doctor(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren doctor: %v\n", err)
		return 1
	}
	if *jsonOut {
		code := printJSON(report)
		if code != 0 {
			return code
		}
		if !report.OK() {
			return 1
		}
		return 0
	}
	if len(report.Issues) > 0 {
		for _, issue := range report.Issues {
			fmt.Fprintf(os.Stderr, "%s\t%s\t%s\n", issue.Severity, issue.Code, issue.Message)
		}
		if !report.OK() {
			return 1
		}
		return 0
	}
	fmt.Printf("Warren %q manifest looks healthy.\n", report.WarrenID)
	return 0
}

func warrenFormat(args []string) int {
	fs := flag.NewFlagSet("warren format", flag.ContinueOnError)
	root := fs.String("warren-dir", ".", "Warren repository root")
	rootCompat := fs.String("root", "", "Warren repository root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *rootCompat != "" {
		*root = *rootCompat
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

func reorderInterspersedFlags(args []string, valueFlags, boolFlags map[string]bool) []string {
	if len(args) == 0 {
		return args
	}
	var flags []string
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			flags = append(flags, arg)
			continue
		}
		if boolFlags[name] {
			flags = append(flags, arg)
			continue
		}
		if valueFlags[name] && i+1 < len(args) {
			flags = append(flags, arg, args[i+1])
			i++
			continue
		}
		flags = append(flags, arg)
	}
	return append(flags, positionals...)
}

func warrenRegister(args []string) int {
	fs := flag.NewFlagSet("warren register", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot warren register [--dir .marmot] <warren-id> <path>")
		return 1
	}
	marmotDir, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren register: %v\n", err)
		return 1
	}
	_ = marmotDir
	if _, err := warren.RegisterWorkspaceWarren(workspaceRoot, fs.Arg(0), fs.Arg(1)); err != nil {
		fmt.Fprintf(os.Stderr, "warren register: %v\n", err)
		return 1
	}
	fmt.Printf("Registered Warren %q -> %s\n", fs.Arg(0), fs.Arg(1))
	return 0
}

func warrenList(args []string) int {
	fs := flag.NewFlagSet("warren list", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	_, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren list: %v\n", err)
		return 1
	}
	state, _, err := warren.LoadWorkspaceState(workspaceRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren list: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(state)
	}
	if len(state.Warrens) == 0 {
		fmt.Println("No Warrens registered.")
		return 0
	}
	ids := make([]string, 0, len(state.Warrens))
	for id := range state.Warrens {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "WARREN_ID\tPATH\tACTIVE\tEDITABLE\tMATERIALIZED")
	for _, id := range ids {
		entry := state.Warrens[id]
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%t\n", id, entry.Path, len(entry.ActiveProjects), len(entry.EditableProjects), entry.Materialized)
	}
	_ = w.Flush()
	return 0
}

func warrenMount(args []string, isBurrow bool) int {
	name := "mount"
	if isBurrow {
		name = "burrow"
	}
	fs := flag.NewFlagSet("warren "+name, flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	materialize := fs.Bool("materialize", false, "copy mounted project vaults into the local Warren cache")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *warrenID == "" {
		fmt.Fprintf(os.Stderr, "warren %s: --warren is required\n", name)
		return 1
	}
	marmotDir, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren %s: %v\n", name, err)
		return 1
	}
	state, _, err := warren.LoadWorkspaceState(workspaceRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren %s: %v\n", name, err)
		return 1
	}
	entry, ok := state.Warrens[*warrenID]
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
		for _, project := range manifest.Projects {
			projects = append(projects, project.ProjectID)
		}
	}
	if len(projects) == 0 {
		fmt.Fprintf(os.Stderr, "warren %s: no projects to mount\n", name)
		return 1
	}
	if _, err := warren.Mount(workspaceRoot, *warrenID, projects, *materialize); err != nil {
		fmt.Fprintf(os.Stderr, "warren %s: %v\n", name, err)
		return 1
	}
	if *materialize {
		projectMap := make(map[string]warren.Project, len(manifest.Projects))
		for _, project := range manifest.Projects {
			projectMap[project.ProjectID] = project
		}
		for _, id := range projects {
			project, ok := projectMap[id]
			if !ok {
				fmt.Fprintf(os.Stderr, "warren %s: project %q is not registered in Warren %q\n", name, id, *warrenID)
				return 1
			}
			if _, err := warren.Materialize(marmotDir, *warrenID, project, entry.Path); err != nil {
				fmt.Fprintf(os.Stderr, "warren %s: materialize %s: %v\n", name, id, err)
				return 1
			}
		}
	}
	fmt.Printf("Mounted %d project(s) from Warren %q\n", len(projects), *warrenID)
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
	_, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren status: %v\n", err)
		return 1
	}
	id, err := resolveWarrenID(workspaceRoot, *warrenID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren status: %v\n", err)
		return 1
	}
	statuses, err := warren.Status(workspaceRoot, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren status: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(statuses)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSTATE\tEDITABLE\tAVAILABLE\tPATH")
	for _, status := range statuses {
		state := "dormant"
		if status.Active {
			state = "mounted"
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%t\t%s\n", status.ProjectID, state, status.Editable, status.Available, status.Path)
	}
	_ = w.Flush()
	return 0
}

func warrenEdit(args []string) int {
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
	_, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren edit: %v\n", err)
		return 1
	}
	if _, err := warren.SetEditable(workspaceRoot, *warrenID, fs.Arg(0), !*off); err != nil {
		fmt.Fprintf(os.Stderr, "warren edit: %v\n", err)
		return 1
	}
	if *off {
		fmt.Printf("Project %q in Warren %q is read-only\n", fs.Arg(0), *warrenID)
	} else {
		fmt.Printf("Project %q in Warren %q is editable in this workspace\n", fs.Arg(0), *warrenID)
	}
	return 0
}

func warrenRefresh(args []string) int {
	fs := flag.NewFlagSet("warren refresh", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	_, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: %v\n", err)
		return 1
	}
	id, err := resolveWarrenID(workspaceRoot, *warrenID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren refresh: %v\n", err)
		return 1
	}
	fmt.Printf("Warren %q is refreshed from git-managed files; run git pull in its checkout when needed.\n", id)
	return 0
}

func warrenPropose(args []string) int {
	fs := flag.NewFlagSet("warren propose", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory (default: auto-discover or .marmot)")
	warrenID := fs.String("warren", "", "Warren ID")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	_, workspaceRoot, err := ensureWorkspace(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren propose: %v\n", err)
		return 1
	}
	id, err := resolveWarrenID(workspaceRoot, *warrenID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren propose: %v\n", err)
		return 1
	}
	fmt.Printf("Warren %q uses git for proposals; commit changes in its checkout and open a PR.\n", id)
	return 0
}

func ensureWorkspace(dirFlag string) (marmotDir, workspaceRoot string, err error) {
	if dirFlag == "" {
		dirFlag = discoverVault()
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
	}
	return marmotDir, workspaceRoot, nil
}

func resolveWarrenID(workspaceRoot, requested string) (string, error) {
	state, _, err := warren.LoadWorkspaceState(workspaceRoot)
	if err != nil {
		return "", err
	}
	if requested != "" {
		if _, ok := state.Warrens[requested]; !ok {
			return "", fmt.Errorf("Warren %q is not registered", requested)
		}
		return requested, nil
	}
	if len(state.Warrens) == 1 {
		for id := range state.Warrens {
			return id, nil
		}
	}
	if len(state.Warrens) == 0 {
		return "", fmt.Errorf("no Warrens registered")
	}
	return "", fmt.Errorf("--warren is required when multiple Warrens are registered")
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
