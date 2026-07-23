package main

// marmot resolve — thin diagnostic verb over warren.ResolveReference
// (§15.5): show how a reference repo (URL and/or local checkout path) would
// resolve into a den link, without creating anything. Shares the exact
// resolver den create/link `--ref` uses, so its answer can never disagree
// with theirs.

import (
	"flag"
	"fmt"
	"os"

	"github.com/nurozen/context-marmot/internal/warren"
)

// jsonResolveEnvelope is the schema:1 stdout contract for
// `marmot resolve --json` (testdata/contracts/resolve.v1.json). Additive-only
// evolution.
type jsonResolveEnvelope struct {
	Schema      int    `json:"schema"`
	ResolvedVia string `json:"resolved_via"`
	Warren      string `json:"warren"`
	Project     string `json:"project"`
	VaultID     string `json:"vault_id"`
	Detail      string `json:"detail"`
}

func cmdResolve(args []string) int {
	args = reorderInterspersedFlags(args,
		map[string]bool{"url": true, "path": true, "name": true, "ref": true},
		map[string]bool{"json": true},
	)
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	url := fs.String("url", "", "reference repo remote URL (canonical-URL matched against warren project source_url)")
	path := fs.String("path", "", "reference repo checkout path (probed for an in-checkout .marmot vault)")
	name := fs.String("name", "", "reference name (diagnostic label only)")
	gitRef := fs.String("ref", "", "git ref of the reference repo (recorded, not yet used for resolution)")
	jsonOut := fs.Bool("json", false, "print a schema:1 JSON envelope on stdout")
	const usage = "usage: marmot resolve --url <url> [--path <checkout>] [--name <n>] [--ref <git-ref>] [--json]"
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, usage)
	}
	if fs.NArg() != 0 {
		return resolveFail(*jsonOut, "unexpected positional arguments", usage)
	}
	if *url == "" && *path == "" {
		return resolveFail(*jsonOut, "at least one of --url or --path is required", usage)
	}
	res := warren.ResolveReference(warren.RefSpec{
		Name:   *name,
		URL:    *url,
		Path:   *path,
		GitRef: *gitRef,
	})
	if *jsonOut {
		return printDenJSON(jsonResolveEnvelope{
			Schema:      1,
			ResolvedVia: res.Via,
			Warren:      res.WarrenID,
			Project:     res.ProjectID,
			VaultID:     res.VaultID,
			Detail:      res.Detail,
		})
	}
	label := *name
	if label == "" {
		label = *url
	}
	if label == "" {
		label = *path
	}
	switch res.Via {
	case warren.ResolvedViaWarrenURL:
		fmt.Printf("%s -> warren-url: %s/%s (vault %s)\n", label, res.WarrenID, res.ProjectID, res.VaultID)
	case warren.ResolvedViaCheckoutVault:
		fmt.Printf("%s -> checkout-vault: %s\n", label, res.VaultID)
	default:
		fmt.Printf("%s -> none\n", label)
	}
	fmt.Printf("  %s\n", res.Detail)
	return 0
}

// resolveFail routes an argument refusal to the right surface: a schema:1
// invalid_args envelope on stdout with --json, plain stderr otherwise.
func resolveFail(jsonOut bool, message, hint string) int {
	if jsonOut {
		return denJSONError("invalid_args", message, hint)
	}
	fmt.Fprintf(os.Stderr, "resolve: %s\n", message)
	fmt.Fprintln(os.Stderr, hint)
	return 1
}
