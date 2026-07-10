package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

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
	case "rm":
		return routeRm(subArgs)
	case "resolve":
		return routeResolve(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "route: unknown subcommand %q\n", sub)
		fmt.Fprintln(os.Stderr, "usage: marmot route [add|rm|resolve]")
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
	if len(vaults) == 0 {
		fmt.Println("No vaults registered.")
		return 0
	}

	// Sort by vault ID for stable output.
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
	return 0
}

func routeAdd(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: marmot route add <vault-id> <path>")
		return 1
	}

	vaultID := args[0]
	p := args[1]

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
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: marmot route rm <vault-id>")
		return 1
	}

	vaultID := args[0]

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
