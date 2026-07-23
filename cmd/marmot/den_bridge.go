package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
)

// denBridgeDefaultRelations mirrors cmdBridge's default relation set so a den
// bridge created without --relation authorizes the same cross-vault edges a
// vault-internal cross-vault bridge would.
const denBridgeDefaultRelations = "calls,reads,writes,references,cross_project,associated"

// jsonDenBridgeBody is the shared bridge body for the add/list envelopes.
type jsonDenBridgeBody struct {
	From      string   `json:"from"`
	To        string   `json:"to"`
	Relations []string `json:"relations"`
}

type jsonDenBridgeAddEnvelope struct {
	Schema   int               `json:"schema"`
	DenID    string            `json:"den_id"`
	Bridge   jsonDenBridgeBody `json:"bridge"`
	Added    bool              `json:"added"`
	Warnings []string          `json:"warnings"`
}

type jsonDenBridgeListEnvelope struct {
	Schema  int                 `json:"schema"`
	DenID   string              `json:"den_id"`
	Bridges []jsonDenBridgeBody `json:"bridges"`
}

type jsonDenBridgeRmEnvelope struct {
	Schema  int    `json:"schema"`
	DenID   string `json:"den_id"`
	Removed bool   `json:"removed"`
}

// denBridge dispatches the den-scoped bridge verbs (plan §7):
//
//	marmot den bridge add  <den-id> <from> <to> [--relation r]... [--json]
//	marmot den bridge list <den-id> [--json]
//	marmot den bridge rm   <den-id> <from> <to> [--json]
//
// Den bridges are consumer-owned cross-vault edge declarations stored under
// $MARMOT_HOME/dens/<id>/_bridges/ — same manifest shape as vault-internal
// cross-vault bridges (_bridges/@a--@b.md), loaded ADDITIONALLY when serving
// the den's vault.
func denBridge(args []string) int {
	if len(args) == 0 {
		denBridgeUsage()
		return 1
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "add":
		return denBridgeAdd(rest)
	case "list":
		return denBridgeList(rest)
	case "rm", "remove":
		return denBridgeRemove(rest)
	case "--help", "-h", "help":
		denBridgeUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "den bridge: unknown subcommand %q\n", sub)
		denBridgeUsage()
		return 1
	}
}

func denBridgeUsage() {
	fmt.Fprintln(os.Stderr, "usage: marmot den bridge <command> [flags]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  add  <den-id> <from> <to> [--relation r]... [--json]")
	fmt.Fprintln(os.Stderr, "  list <den-id> [--json]")
	fmt.Fprintln(os.Stderr, "  rm   <den-id> <from> <to> [--json]")
}

// denBridgeDenNotFound emits the shared den_not_found refusal for a den that
// does not exist (or whose manifest is unreadable).
func denBridgeDenNotFound(denID string, err error, asJSON bool) int {
	if asJSON {
		return denJSONError("den_not_found", err.Error(),
			"create one: marmot den create "+denID+" --json")
	}
	fmt.Fprintf(os.Stderr, "den bridge: %v\n", err)
	return 1
}

func denBridgeAdd(args []string) int {
	// Flags may follow the positionals (stave: `den bridge add d a b --json`).
	args = reorderInterspersedFlags(args,
		map[string]bool{"relation": true},
		map[string]bool{"json": true},
	)
	fs := flag.NewFlagSet("den bridge add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var relations multiString
	fs.Var(&relations, "relation", "allowed cross-vault relation (repeatable)")
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	const usage = "usage: marmot den bridge add <den-id> <from> <to> [--relation r]... [--json]"
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, usage)
	}
	invalidArgs := func(msg string) int {
		if *asJSON {
			return denJSONError("invalid_args", msg, usage)
		}
		fmt.Fprintf(os.Stderr, "den bridge add: %s\n", msg)
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	if fs.NArg() < 3 {
		return invalidArgs("expected <den-id> <from> <to>")
	}
	if fs.NArg() > 3 {
		return invalidArgs(fmt.Sprintf("unexpected extra arguments: %s", strings.Join(fs.Args()[3:], " ")))
	}
	denID, from, to := fs.Arg(0), fs.Arg(1), fs.Arg(2)

	info, err := den.Status(denID)
	if err != nil {
		return denBridgeDenNotFound(denID, err, *asJSON)
	}

	// Endpoint validation (best-effort): a den bridge authorizes cross-vault
	// edges between vaults this den can actually see, so each endpoint must
	// be the den's own identity or a vault one of its links resolves to.
	// Endpoints may be bare vault ids ("@beta", "beta") or vault-qualified
	// node references ("@beta/<node-id>", the form cross-vault edges use) —
	// a den bridge authorizes the VAULT pair, so only the vault segment is
	// validated (and stored). A clearly-dangling endpoint — unknown while
	// EVERY link resolved — is refused (bridge_endpoint_unknown); when any
	// link failed to resolve (cache missing, mount gone) the check degrades
	// to a warning instead of blocking a legitimate bridge.
	warnings := []string{}
	fromVault := denBridgeEndpointVault(from)
	toVault := denBridgeEndpointVault(to)
	known, complete := denBridgeKnownEndpoints(info)
	for _, id := range []string{fromVault, toVault} {
		if id == "" || known[id] {
			continue
		}
		msg := fmt.Sprintf("bridge endpoint %q is neither den %q's own identity nor a vault any of its links resolves to", id, denID)
		if complete {
			hint := "link the vault first (marmot den link " + denID + " ...) or check the id: marmot den status " + denID
			if *asJSON {
				return denJSONError("bridge_endpoint_unknown", msg, hint)
			}
			fmt.Fprintf(os.Stderr, "den bridge add: %s\n", msg)
			fmt.Fprintf(os.Stderr, "hint: %s\n", hint)
			return 1
		}
		warnings = append(warnings, msg+" (link resolution incomplete; adding anyway)")
	}

	rels := []string(relations)
	if len(rels) == 0 {
		rels = strings.Split(denBridgeDefaultRelations, ",")
	}

	bridge, added, err := den.AddBridge(denID, fromVault, toVault, rels)
	if err != nil {
		if *asJSON {
			return denJSONError("bridge_failed", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "den bridge add: %v\n", err)
		return 1
	}

	if *asJSON {
		return printDenJSON(jsonDenBridgeAddEnvelope{
			Schema:   1,
			DenID:    denID,
			Bridge:   bridgeBody(bridge),
			Added:    added,
			Warnings: warnings,
		})
	}
	verb := "updated"
	if added {
		verb = "added"
	}
	fmt.Printf("Bridge %s: @%s -> @%s (%s)\n", verb, bridge.SourceVaultID, bridge.TargetVaultID,
		strings.Join(bridge.AllowedRelations, ","))
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return 0
}

// denBridgeEndpointVault reduces a bridge endpoint to its vault id: a leading
// '@' is stripped and, for vault-qualified node references (@vault/<node-id>),
// everything from the first '/' on is dropped — den bridges are vault-pair
// policies, the node portion only identifies the edge that motivated them.
func denBridgeEndpointVault(ep string) string {
	v := strings.TrimPrefix(strings.TrimSpace(ep), "@")
	if i := strings.IndexByte(v, '/'); i >= 0 {
		v = v[:i]
	}
	return v
}

// denBridgeKnownEndpoints resolves the vault ids a den's bridges may
// legitimately name: the den's own identity (the identity vault's vault_id
// and the den id itself — links-only dens use the den id as their local
// endpoint, matching the MCP engine's den-identity fallback) plus the
// resolved vault id of every den link. Best-effort: any link that cannot be
// resolved right now (cache missing, mount gone, route absent) makes the
// knowledge incomplete (complete=false) — the caller warns about unknown
// endpoints instead of refusing.
func denBridgeKnownEndpoints(info *den.StatusInfo) (known map[string]bool, complete bool) {
	known = map[string]bool{info.DenID: true}
	// A link-less den cannot prove an endpoint dangling — bridges may be
	// declared before their links (setup-order freedom) — so unknown
	// endpoints only warn there.
	complete = len(info.Links) > 0
	vaultDir := den.VaultPath(info.DenID)
	if vid := warren.LocalVaultID(vaultDir); vid != "" {
		known[vid] = true
	}
	var mounts []warren.ProjectStatus
	if dirExistsCLI(vaultDir) {
		if m, err := warren.ActiveMounts(vaultDir); err == nil {
			mounts = m
		} else {
			complete = false
		}
	}
	rt, rtErr := routes.Load()
	if rtErr != nil || rt == nil {
		rt = routes.EmptyTable()
	}
	for _, l := range info.Links {
		switch l.Mode {
		case den.LinkModeEdit:
			found := false
			for _, mnt := range mounts {
				if mnt.WarrenID == l.Warren && mnt.ProjectID == l.Project && mnt.VaultID != "" {
					known[mnt.VaultID] = true
					found = true
				}
			}
			if !found {
				complete = false
			}
		case den.LinkModeLive:
			if path, ok := rt.Get(l.Target); ok {
				known[l.Target] = true
				if vid := warren.LocalVaultID(path); vid != "" {
					known[vid] = true
				}
				continue
			}
			if vid := warren.LocalVaultID(den.VaultPath(l.Target)); vid != "" {
				known[vid] = true
				known[l.Target] = true
				continue
			}
			complete = false
		case den.LinkModeLink:
			entry, ok := warren.CacheWorkspaceWarren(l.Warren)
			if !ok {
				complete = false
				continue
			}
			manifest, _, merr := warren.LoadManifest(entry.Path)
			if merr != nil {
				complete = false
				continue
			}
			resolved := false
			for i := range manifest.Projects {
				p := &manifest.Projects[i]
				if p.ProjectID != l.Project && !sliceContains(p.Aliases, l.Project) {
					continue
				}
				dir := filepath.Join(entry.Path, filepath.FromSlash(p.Path))
				if meta, _, mdErr := warren.LoadProjectMetadata(dir); mdErr == nil && meta.VaultID != "" {
					known[meta.VaultID] = true
					resolved = true
				} else if vid := warren.LocalVaultID(dir); vid != "" {
					known[vid] = true
					resolved = true
				}
				break
			}
			if !resolved {
				complete = false
			}
		default:
			complete = false
		}
	}
	return known, complete
}

func denBridgeList(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{}, map[string]bool{"json": true})
	fs := flag.NewFlagSet("den bridge list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	const usage = "usage: marmot den bridge list <den-id> [--json]"
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, usage)
	}
	invalidArgs := func(msg string) int {
		if *asJSON {
			return denJSONError("invalid_args", msg, usage)
		}
		fmt.Fprintf(os.Stderr, "den bridge list: %s\n", msg)
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

	if _, err := den.Status(denID); err != nil {
		return denBridgeDenNotFound(denID, err, *asJSON)
	}

	bridges, err := den.ListBridges(denID)
	if err != nil {
		if *asJSON {
			return denJSONError("bridge_failed", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "den bridge list: %v\n", err)
		return 1
	}

	if *asJSON {
		items := make([]jsonDenBridgeBody, 0, len(bridges))
		for _, b := range bridges {
			items = append(items, bridgeBody(b))
		}
		return printDenJSON(jsonDenBridgeListEnvelope{
			Schema:  1,
			DenID:   denID,
			Bridges: items,
		})
	}
	if len(bridges) == 0 {
		fmt.Printf("No bridges for den %q\n", denID)
		return 0
	}
	for _, b := range bridges {
		fmt.Printf("@%s -> @%s (%s)\n", b.SourceVaultID, b.TargetVaultID,
			strings.Join(b.AllowedRelations, ","))
	}
	return 0
}

func denBridgeRemove(args []string) int {
	args = reorderInterspersedFlags(args, map[string]bool{}, map[string]bool{"json": true})
	fs := flag.NewFlagSet("den bridge rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit schema:1 JSON envelope on stdout")
	const usage = "usage: marmot den bridge rm <den-id> <from> <to> [--json]"
	if err := fs.Parse(args); err != nil {
		return denParseFail(args, err, usage)
	}
	invalidArgs := func(msg string) int {
		if *asJSON {
			return denJSONError("invalid_args", msg, usage)
		}
		fmt.Fprintf(os.Stderr, "den bridge rm: %s\n", msg)
		fmt.Fprintln(os.Stderr, usage)
		return 1
	}
	if fs.NArg() < 3 {
		return invalidArgs("expected <den-id> <from> <to>")
	}
	if fs.NArg() > 3 {
		return invalidArgs(fmt.Sprintf("unexpected extra arguments: %s", strings.Join(fs.Args()[3:], " ")))
	}
	denID, from, to := fs.Arg(0), fs.Arg(1), fs.Arg(2)

	if _, err := den.Status(denID); err != nil {
		return denBridgeDenNotFound(denID, err, *asJSON)
	}

	removed, err := den.RemoveBridge(denID, from, to)
	if err != nil {
		if *asJSON {
			return denJSONError("bridge_failed", err.Error(), "")
		}
		fmt.Fprintf(os.Stderr, "den bridge rm: %v\n", err)
		return 1
	}
	if !removed {
		msg := fmt.Sprintf("no bridge between %q and %q on den %q", from, to, denID)
		if *asJSON {
			return denJSONError("bridge_not_found", msg, "list them: marmot den bridge list "+denID+" --json")
		}
		fmt.Fprintf(os.Stderr, "den bridge rm: %s\n", msg)
		return 1
	}

	if *asJSON {
		return printDenJSON(jsonDenBridgeRmEnvelope{
			Schema:  1,
			DenID:   denID,
			Removed: true,
		})
	}
	fmt.Printf("Removed bridge %s -> %s from den %q\n", from, to, denID)
	return 0
}

// bridgeBody converts a namespace.Bridge into the JSON body, guaranteeing a
// non-nil relations array (marshals as [] not null).
func bridgeBody(b *namespace.Bridge) jsonDenBridgeBody {
	rels := b.AllowedRelations
	if rels == nil {
		rels = []string{}
	}
	return jsonDenBridgeBody{From: b.SourceVaultID, To: b.TargetVaultID, Relations: rels}
}
