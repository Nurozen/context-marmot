package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	nsconfig "github.com/nurozen/context-marmot/internal/namespace"
)

func cmdNamespace(args []string) int {
	if len(args) == 0 {
		namespaceUsage()
		return 1
	}
	switch args[0] {
	case "create":
		return cmdNamespaceCreate(args[1:])
	case "list":
		return cmdNamespaceList(args[1:])
	case "update":
		return cmdNamespaceUpdate(args[1:])
	case "doctor":
		return cmdNamespaceDoctor(args[1:])
	case "remove", "rm":
		return cmdNamespaceRemove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "namespace: unknown subcommand %q\n", args[0])
		namespaceUsage()
		return 1
	}
}

func namespaceUsage() {
	fmt.Fprintln(os.Stderr, "usage: marmot namespace <create|list|update|doctor|remove> [flags]")
	fmt.Fprintln(os.Stderr, "  create <name> [--dir .marmot] [--root-path <path>]")
	fmt.Fprintln(os.Stderr, "  list [--dir .marmot] [--json]")
	fmt.Fprintln(os.Stderr, "  update <name> [--dir .marmot] [--root-path <path>]")
	fmt.Fprintln(os.Stderr, "  doctor [--dir .marmot]")
	fmt.Fprintln(os.Stderr, "  remove <name> [--dir .marmot] [--force]")
}

func cmdNamespaceCreate(args []string) int {
	fs := flag.NewFlagSet("namespace create", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	rootPath := fs.String("root-path", "", "source root path for this namespace")
	if err := fs.Parse(reorderInterspersedFlags(args, map[string]bool{"dir": true, "root-path": true}, nil)); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "namespace create: requires namespace name")
		return 1
	}
	name := fs.Arg(0)
	ns, created, err := nsconfig.EnsureNamespace(*dir, name, *rootPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "namespace create: %v\n", err)
		return 1
	}
	if created {
		fmt.Printf("Created namespace %q at %s\n", ns.Name, filepath.Join(*dir, ns.Name, "_namespace.md"))
	} else {
		fmt.Printf("Namespace %q already exists\n", ns.Name)
	}
	return 0
}

func cmdNamespaceList(args []string) int {
	fs := flag.NewFlagSet("namespace list", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(reorderInterspersedFlags(args, map[string]bool{"dir": true}, map[string]bool{"json": true})); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	items, err := nsconfig.Inventory(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "namespace list: %v\n", err)
		return 1
	}
	if *jsonOut {
		data, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "namespace list: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	for _, item := range items {
		status := "implicit"
		if item.HasManifest {
			status = "manifest"
		}
		if item.RootPath != "" {
			fmt.Printf("%s\t%d nodes\t%s\troot=%s\n", item.Name, item.NodeCount, status, item.RootPath)
		} else {
			fmt.Printf("%s\t%d nodes\t%s\n", item.Name, item.NodeCount, status)
		}
	}
	return 0
}

func cmdNamespaceUpdate(args []string) int {
	fs := flag.NewFlagSet("namespace update", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	rootPath := fs.String("root-path", "", "source root path for this namespace")
	if err := fs.Parse(reorderInterspersedFlags(args, map[string]bool{"dir": true, "root-path": true}, nil)); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "namespace update: requires namespace name")
		return 1
	}
	name := fs.Arg(0)
	if err := nsconfig.ValidateNamespaceName(name); err != nil {
		fmt.Fprintf(os.Stderr, "namespace update: %v\n", err)
		return 1
	}
	nsDir := filepath.Join(*dir, name)
	ns, err := nsconfig.LoadNamespace(nsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "namespace update: %v\n", err)
		return 1
	}
	ns.RootPath = *rootPath
	if err := nsconfig.SaveNamespace(nsDir, ns); err != nil {
		fmt.Fprintf(os.Stderr, "namespace update: %v\n", err)
		return 1
	}
	fmt.Printf("Updated namespace %q\n", ns.Name)
	return 0
}

func cmdNamespaceDoctor(args []string) int {
	fs := flag.NewFlagSet("namespace doctor", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	if err := fs.Parse(reorderInterspersedFlags(args, map[string]bool{"dir": true}, nil)); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	issues, err := nsconfig.Doctor(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "namespace doctor: %v\n", err)
		return 1
	}
	if len(issues) == 0 {
		fmt.Println("No namespace issues found.")
		return 0
	}
	fmt.Printf("Found %d namespace issue(s):\n", len(issues))
	for _, issue := range issues {
		if issue.Namespace != "" {
			fmt.Printf("  [%s] %s: %s\n", issue.Severity, issue.Namespace, issue.Message)
		} else {
			fmt.Printf("  [%s] %s\n", issue.Severity, issue.Message)
		}
	}
	return 1
}

func cmdNamespaceRemove(args []string) int {
	fs := flag.NewFlagSet("namespace remove", flag.ContinueOnError)
	dir := fs.String("dir", "", "marmot vault directory")
	force := fs.Bool("force", false, "remove manifest even when nodes still reference the namespace")
	if err := fs.Parse(reorderInterspersedFlags(args, map[string]bool{"dir": true}, map[string]bool{"force": true})); err != nil {
		return 1
	}
	if *dir == "" {
		*dir = discoverVault()
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "namespace remove: requires namespace name")
		return 1
	}
	name := fs.Arg(0)
	if name == "default" {
		fmt.Fprintln(os.Stderr, "namespace remove: refusing to remove default namespace manifest")
		return 1
	}
	if err := nsconfig.ValidateNamespaceName(name); err != nil {
		fmt.Fprintf(os.Stderr, "namespace remove: %v\n", err)
		return 1
	}
	items, err := nsconfig.Inventory(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "namespace remove: %v\n", err)
		return 1
	}
	for _, item := range items {
		if item.Name == name && item.NodeCount > 0 && !*force {
			fmt.Fprintf(os.Stderr, "namespace remove: namespace %q still has %d node(s); use --force to remove only the manifest\n", name, item.NodeCount)
			return 1
		}
	}
	manifest := filepath.Join(*dir, name, "_namespace.md")
	if err := os.Remove(manifest); err != nil {
		fmt.Fprintf(os.Stderr, "namespace remove: %v\n", err)
		return 1
	}
	fmt.Printf("Removed namespace manifest for %q\n", name)
	return 0
}
