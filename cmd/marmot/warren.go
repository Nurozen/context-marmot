package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/nurozen/context-marmot/internal/warren"
)

func cmdWarren(args []string) int {
	if len(args) == 0 {
		warrenUsage()
		return 1
	}
	sub := args[0]
	subArgs := args[1:]
	switch sub {
	case "register":
		return warrenRegister(subArgs)
	case "list":
		return warrenList(subArgs)
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
	fmt.Fprintln(os.Stderr, "usage: marmot warren <register|list|mount|burrow|status|edit|refresh|propose> [flags]")
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
