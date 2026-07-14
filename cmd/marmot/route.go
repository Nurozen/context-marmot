package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/routes"
)

func cmdRoute(args []string) int {
	if len(args) == 0 {
		return routeList()
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "add":
		return routeAdd(subArgs)
	case "rm", "remove":
		return routeRm(subArgs)
	case "resolve":
		return routeResolve(subArgs)
	case "set-project":
		return routeSetProject(subArgs)
	case "pointer":
		return routePointer(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "route: unknown subcommand %q\n", sub)
		fmt.Fprintln(os.Stderr, "usage: marmot route [add|rm|resolve|set-project|pointer]")
		return 1
	}
}

func routeList() int {
	rt, err := routes.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "route: %v\n", err)
		return 1
	}

	vaults := rt.List()
	projects := rt.ListProjects()

	if len(vaults) == 0 && len(projects) == 0 {
		fmt.Println("No vaults registered.")
		return 0
	}

	if len(vaults) > 0 {
		ids := make([]string, 0, len(vaults))
		for id := range vaults {
			ids = append(ids, id)
		}
		sort.Strings(ids)

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "VAULT_ID\tPATH")
		for _, id := range ids {
			fmt.Fprintf(w, "%s\t%s\n", id, vaults[id])
		}
		_ = w.Flush()
	}

	if len(projects) > 0 {
		paths := make([]string, 0, len(projects))
		for p := range projects {
			paths = append(paths, p)
		}
		sort.Strings(paths)

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PROJECT\tID")
		for _, p := range paths {
			fmt.Fprintf(w, "%s\t%s\n", p, projects[p])
		}
		_ = w.Flush()
	}
	return 0
}

func routeAdd(args []string) int {
	// Support both:
	//   marmot route add <vault-id> <path>
	//   marmot route add --project <abs-path> <den-or-vault-id>
	fs := flag.NewFlagSet("route add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	project := fs.String("project", "", "register reverse project path → den-or-vault id")
	asJSON := fs.Bool("json", false, "emit JSON result")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	rest := fs.Args()

	if *project != "" {
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: marmot route add --project <abs-path> <den-or-vault-id>")
			return 1
		}
		denOrVaultID := rest[0]
		key, err := routes.NormalizeProjectKey(*project)
		if err != nil {
			fmt.Fprintf(os.Stderr, "route add: %v\n", err)
			return 1
		}
		if err := routes.Update(func(rt *routes.RoutingTable) error {
			rt.SetProject(key, denOrVaultID)
			return nil
		}); err != nil {
			fmt.Fprintf(os.Stderr, "route add: %v\n", err)
			return 1
		}
		if *asJSON {
			return printDenJSON(map[string]any{
				"schema":       1,
				"project_path": key,
				"id":           denOrVaultID,
			})
		}
		fmt.Printf("Registered project %q -> %s\n", key, denOrVaultID)
		return 0
	}

	if len(rest) < 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot route add <vault-id> <path>")
		fmt.Fprintln(os.Stderr, "       marmot route add --project <abs-path> <den-or-vault-id>")
		return 1
	}

	vaultID := rest[0]
	p := rest[1]

	abs, err := filepath.Abs(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "route add: resolve path: %v\n", err)
		return 1
	}

	info, err := os.Stat(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "route add: path %q does not exist\n", abs)
		return 1
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "route add: path %q is not a directory\n", abs)
		return 1
	}

	// Update takes the routes flock, so a concurrent `route add` in another
	// process cannot be dropped by this read-modify-write cycle.
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		rt.Set(vaultID, abs)
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "route add: %v\n", err)
		return 1
	}

	fmt.Printf("Registered vault %q -> %s\n", vaultID, abs)
	return 0
}

func routeRm(args []string) int {
	// Support:
	//   marmot route rm <vault-id>
	//   marmot route rm --project <abs-path>
	//   marmot route remove --project <abs-path>
	fs := flag.NewFlagSet("route rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	project := fs.String("project", "", "remove reverse project path entry")
	asJSON := fs.Bool("json", false, "emit JSON result")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	rest := fs.Args()

	if *project != "" {
		key, err := routes.NormalizeProjectKey(*project)
		if err != nil {
			fmt.Fprintf(os.Stderr, "route rm: %v\n", err)
			return 1
		}
		var found bool
		if err := routes.Update(func(rt *routes.RoutingTable) error {
			found = rt.RemoveProject(key)
			if !found {
				return fmt.Errorf("project %q not found", key)
			}
			return nil
		}); err != nil {
			fmt.Fprintf(os.Stderr, "route rm: %v\n", err)
			return 1
		}
		if *asJSON {
			return printDenJSON(map[string]any{
				"schema":       1,
				"project_path": key,
				"removed":      true,
			})
		}
		fmt.Printf("Removed project %q\n", key)
		return 0
	}

	if len(rest) < 1 {
		fmt.Fprintln(os.Stderr, "usage: marmot route rm <vault-id>")
		fmt.Fprintln(os.Stderr, "       marmot route rm --project <abs-path>")
		return 1
	}

	vaultID := rest[0]

	// Update takes the routes flock, so a concurrent route mutation in
	// another process cannot be dropped by this read-modify-write cycle.
	if err := routes.Update(func(rt *routes.RoutingTable) error {
		if !rt.Remove(vaultID) {
			return fmt.Errorf("vault %q not found", vaultID)
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "route rm: %v\n", err)
		return 1
	}

	fmt.Printf("Removed vault %q\n", vaultID)
	return 0
}

func routeResolve(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: marmot route resolve <vault-id>")
		return 1
	}

	vaultID := args[0]

	rt, err := routes.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "route resolve: %v\n", err)
		return 1
	}

	p, ok := rt.Get(vaultID)
	if !ok {
		fmt.Fprintf(os.Stderr, "route resolve: vault %q not found\n", vaultID)
		return 1
	}

	fmt.Println(p)
	return 0
}

// routeSetProject implements D6 path move: --from old --to new.
// Updates routes.yml AND the owning den's _den.md projects list (when the
// id is a den) so destroy / status / ProjectRoot stay consistent after archive.
func routeSetProject(args []string) int {
	fs := flag.NewFlagSet("route set-project", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	from := fs.String("from", "", "old absolute project path")
	to := fs.String("to", "", "new absolute project path")
	asJSON := fs.Bool("json", false, "emit JSON result")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *from == "" || *to == "" {
		fmt.Fprintln(os.Stderr, "usage: marmot route set-project --from <old-abs> --to <new-abs>")
		return 1
	}
	fromKey, err := routes.NormalizeProjectKey(*from)
	if err != nil {
		fmt.Fprintf(os.Stderr, "route set-project: from: %v\n", err)
		return 1
	}
	toKey, err := routes.NormalizeProjectKey(*to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "route set-project: to: %v\n", err)
		return 1
	}

	denOrVaultID, err := den.RelocateProject(fromKey, toKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "route set-project: %v\n", err)
		return 1
	}

	if *asJSON {
		return printDenJSON(map[string]any{
			"schema": 1,
			"from":   fromKey,
			"to":     toKey,
			"id":     denOrVaultID,
		})
	}
	fmt.Printf("Moved project route %q -> %q (id %s)\n", fromKey, toKey, denOrVaultID)
	return 0
}

// routePointer repairs/writes/removes .marmot-vault pointers (OQ3).
func routePointer(args []string) int {
	fs := flag.NewFlagSet("route pointer", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	write := fs.Bool("write", false, "write pointer file")
	remove := fs.Bool("remove", false, "remove pointer file")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	rest := fs.Args()
	if *remove {
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: marmot route pointer --remove <project-path>")
			return 1
		}
		if err := den.RemovePointer(rest[0]); err != nil {
			fmt.Fprintf(os.Stderr, "route pointer: %v\n", err)
			return 1
		}
		fmt.Printf("Removed pointer at %s\n", filepath.Join(rest[0], den.PointerFileName))
		return 0
	}
	// Default / --write: need path + id
	if len(rest) < 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot route pointer [--write] <project-path> <den-or-vault-id>")
		fmt.Fprintln(os.Stderr, "       marmot route pointer --remove <project-path>")
		return 1
	}
	_ = write // write is default when id is provided
	if err := den.WritePointer(rest[0], rest[1]); err != nil {
		fmt.Fprintf(os.Stderr, "route pointer: %v\n", err)
		return 1
	}
	fmt.Printf("Wrote pointer %s -> %s\n", filepath.Join(rest[0], den.PointerFileName), rest[1])
	return 0
}

// resolveProjectRoute looks up a project path in the reverse routes table.
func resolveProjectRoute(projectPath string) (string, error) {
	rt, err := routes.Load()
	if err != nil {
		return "", err
	}
	id, ok := rt.GetProject(projectPath)
	if !ok {
		return "", fmt.Errorf("no reverse route for %s", projectPath)
	}
	return id, nil
}
