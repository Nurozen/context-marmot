package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/den"
)

func TestDenBridgeAddListRm(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("bridge-den", den.CreateOptions{Lifetime: den.LifetimeTask, NoVault: true, NoPointer: true}); err != nil {
		t.Fatalf("den.Create: %v", err)
	}

	// add with explicit relations.
	out, code := captureRun([]string{"den", "bridge", "add", "bridge-den", "@web", "@api", "--relation", "calls", "--relation", "reads", "--json"})
	if code != 0 {
		t.Fatalf("bridge add: code=%d out=%s", code, out)
	}
	var add struct {
		Schema int    `json:"schema"`
		DenID  string `json:"den_id"`
		Bridge struct {
			From      string   `json:"from"`
			To        string   `json:"to"`
			Relations []string `json:"relations"`
		} `json:"bridge"`
		Added    bool     `json:"added"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &add); err != nil {
		t.Fatalf("add envelope: %v out=%s", err, out)
	}
	if add.Schema != 1 || add.DenID != "bridge-den" || !add.Added {
		t.Fatalf("add envelope: %+v", add)
	}
	if add.Bridge.From != "web" || add.Bridge.To != "api" {
		t.Fatalf("bridge body: %+v", add.Bridge)
	}
	if len(add.Bridge.Relations) != 2 {
		t.Fatalf("relations: %+v", add.Bridge.Relations)
	}
	if add.Warnings == nil {
		t.Fatal("warnings must be present (empty array, not null)")
	}

	// Re-add merges (added=false).
	out, code = captureRun([]string{"den", "bridge", "add", "bridge-den", "web", "api", "--relation", "writes", "--json"})
	if code != 0 {
		t.Fatalf("bridge re-add: code=%d out=%s", code, out)
	}
	add.Added = true
	if err := json.Unmarshal([]byte(out), &add); err != nil {
		t.Fatalf("re-add envelope: %v out=%s", err, out)
	}
	if add.Added {
		t.Fatalf("merge into existing bridge must report added=false: %+v", add)
	}
	if len(add.Bridge.Relations) != 3 {
		t.Fatalf("merged relations: %+v", add.Bridge.Relations)
	}

	// list.
	out, code = captureRun([]string{"den", "bridge", "list", "bridge-den", "--json"})
	if code != 0 {
		t.Fatalf("bridge list: code=%d out=%s", code, out)
	}
	var list struct {
		Schema  int    `json:"schema"`
		DenID   string `json:"den_id"`
		Bridges []struct {
			From      string   `json:"from"`
			To        string   `json:"to"`
			Relations []string `json:"relations"`
		} `json:"bridges"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("list envelope: %v out=%s", err, out)
	}
	if list.Schema != 1 || len(list.Bridges) != 1 || list.Bridges[0].From != "web" {
		t.Fatalf("list envelope: %+v", list)
	}

	// rm.
	out, code = captureRun([]string{"den", "bridge", "rm", "bridge-den", "web", "api", "--json"})
	if code != 0 {
		t.Fatalf("bridge rm: code=%d out=%s", code, out)
	}
	var rm struct {
		Schema  int    `json:"schema"`
		DenID   string `json:"den_id"`
		Removed bool   `json:"removed"`
	}
	if err := json.Unmarshal([]byte(out), &rm); err != nil {
		t.Fatalf("rm envelope: %v out=%s", err, out)
	}
	if rm.Schema != 1 || !rm.Removed {
		t.Fatalf("rm envelope: %+v", rm)
	}

	// list is now empty (present array, not null).
	out, code = captureRun([]string{"den", "bridge", "list", "bridge-den", "--json"})
	if code != 0 {
		t.Fatalf("bridge list empty: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "\"bridges\": []") {
		t.Fatalf("empty list must render bridges as []: %s", out)
	}
}

func TestDenBridgeListDefaultRelations(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("def-den", den.CreateOptions{Lifetime: den.LifetimeTask, NoVault: true, NoPointer: true}); err != nil {
		t.Fatal(err)
	}
	// No --relation: the default set mirrors cmdBridge.
	out, code := captureRun([]string{"den", "bridge", "add", "def-den", "a", "b", "--json"})
	if code != 0 {
		t.Fatalf("add: code=%d out=%s", code, out)
	}
	for _, want := range []string{"calls", "reads", "writes", "references", "cross_project", "associated"} {
		if !strings.Contains(out, "\""+want+"\"") {
			t.Fatalf("default relations missing %q: %s", want, out)
		}
	}
}

func TestDenBridgeDenNotFound(t *testing.T) {
	hermeticDenCLI(t)
	out, code := captureRun([]string{"den", "bridge", "add", "no-such-den", "a", "b", "--json"})
	if code == 0 || !strings.Contains(out, "den_not_found") {
		t.Fatalf("add missing den: code=%d out=%s", code, out)
	}
	out, code = captureRun([]string{"den", "bridge", "list", "no-such-den", "--json"})
	if code == 0 || !strings.Contains(out, "den_not_found") {
		t.Fatalf("list missing den: code=%d out=%s", code, out)
	}
}

func TestDenBridgeRmNotFound(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("rm-den", den.CreateOptions{Lifetime: den.LifetimeTask, NoVault: true, NoPointer: true}); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "bridge", "rm", "rm-den", "a", "b", "--json"})
	if code == 0 || !strings.Contains(out, "bridge_not_found") {
		t.Fatalf("rm missing bridge: code=%d out=%s", code, out)
	}
}

func TestDenBridgeInvalidArgs(t *testing.T) {
	hermeticDenCLI(t)
	// Missing positionals.
	out, code := captureRun([]string{"den", "bridge", "add", "d", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("add missing positionals: code=%d out=%s", code, out)
	}
	out, code = captureRun([]string{"den", "bridge", "list", "--json"})
	if code == 0 || !strings.Contains(out, "invalid_args") {
		t.Fatalf("list missing den-id: code=%d out=%s", code, out)
	}
}

func TestDenBridgePlainText(t *testing.T) {
	hermeticDenCLI(t)
	if _, err := den.Create("plain-den", den.CreateOptions{Lifetime: den.LifetimeTask, NoVault: true, NoPointer: true}); err != nil {
		t.Fatal(err)
	}
	out, code := captureRun([]string{"den", "bridge", "add", "plain-den", "a", "b", "--relation", "calls"})
	if code != 0 || !strings.Contains(out, "Bridge added") {
		t.Fatalf("plain add: code=%d out=%s", code, out)
	}
	out, code = captureRun([]string{"den", "bridge", "list", "plain-den"})
	if code != 0 || !strings.Contains(out, "@a -> @b") {
		t.Fatalf("plain list: code=%d out=%s", code, out)
	}
}

// TestDenBridgeAddQualifiedNodeEndpoints: regression for the live-validation
// defect where `den bridge add alpha @alpha/<node-id> @beta/<node-id>` was
// refused with bridge_endpoint_unknown even though alpha IS the den's own
// identity vault — the vault-qualified endpoint was compared WHOLE
// ("alpha/<node-id>") against the known set of bare vault ids. Endpoints must
// be reduced to their vault segment: own-vault and linked-vault node refs are
// accepted (added:true, bridge stored vault-to-vault), a genuinely unknown
// vault still refuses, and the resulting bridge edge federates end-to-end —
// a query against alpha's vault reaches the beta node through the
// cross-vault edge.
func TestDenBridgeAddQualifiedNodeEndpoints(t *testing.T) {
	home := hermeticDenCLI(t)
	projects := t.TempDir()
	pa := filepath.Join(projects, "pa")
	pb := filepath.Join(projects, "pb")
	for _, p := range []string{pa, pb} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Mirror the repro exactly: create both dens (identity vaults included),
	// write a node in each vault, index each, link alpha -> beta (live).
	if out, code := captureRun([]string{"den", "create", "alpha", "--project", pa, "--json"}); code != 0 {
		t.Fatalf("den create alpha: code=%d out=%s", code, out)
	}
	if out, code := captureRun([]string{"den", "create", "beta", "--project", pb, "--json"}); code != 0 {
		t.Fatalf("den create beta: code=%d out=%s", code, out)
	}
	alphaVault := filepath.Join(home, "dens", "alpha", "vault")
	betaVault := filepath.Join(home, "dens", "beta", "vault")
	// alpha's node declares the cross-vault edge the bridge authorizes; the
	// node bodies share no query token, so the beta node can only enter a
	// query result by edge traversal, not lexical similarity.
	alphaNode := "---\nid: alpha-note\ntype: decision\ntitle: Alpha note\nedges:\n" +
		"  - target: \"@beta/beta-note\"\n    relation: references\n---\n# Alpha note\nzebra quantum flamingo payload\n"
	betaNode := "---\nid: beta-note\ntype: decision\ntitle: Beta note\n---\n# Beta note\nobsidian walrus cathedral payload\n"
	for _, n := range []struct{ vault, name, body string }{
		{alphaVault, "alpha-note.md", alphaNode},
		{betaVault, "beta-note.md", betaNode},
	} {
		dir := filepath.Join(n.vault, "default")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n.name), []byte(n.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{alphaVault, betaVault} {
		if out, code := captureRun([]string{"index", "--dir", v}); code != 0 {
			t.Fatalf("index %s: code=%d out=%s", v, code, out)
		}
	}
	if out, code := captureRun([]string{"den", "link", "alpha", "--link", "beta", "--json"}); code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}

	// Vault-qualified NODE endpoints — own vault + linked vault — are
	// accepted and reduced to the vault pair.
	out, code := captureRun([]string{"den", "bridge", "add", "alpha", "@alpha/alpha-note", "@beta/beta-note", "--json"})
	if code != 0 {
		t.Fatalf("bridge add with qualified node endpoints: code=%d out=%s", code, out)
	}
	var add struct {
		Bridge struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"bridge"`
		Added    bool     `json:"added"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &add); err != nil {
		t.Fatalf("add envelope: %v out=%s", err, out)
	}
	if !add.Added || add.Bridge.From != "alpha" || add.Bridge.To != "beta" || len(add.Warnings) != 0 {
		t.Fatalf("add envelope: %+v", add)
	}

	// bridge list shows the vault-level bridge.
	out, code = captureRun([]string{"den", "bridge", "list", "alpha", "--json"})
	if code != 0 {
		t.Fatalf("bridge list: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "\"from\": \"alpha\"") || !strings.Contains(out, "\"to\": \"beta\"") {
		t.Fatalf("bridge list missing alpha->beta: %s", out)
	}

	// A genuinely unknown vault (qualified form too) still refuses.
	out, code = captureRun([]string{"den", "bridge", "add", "alpha", "@alpha/alpha-note", "@ghost/spooky-note", "--json"})
	if code == 0 || !strings.Contains(out, "bridge_endpoint_unknown") {
		t.Fatalf("unknown endpoint must refuse: code=%d out=%s", code, out)
	}

	// End-to-end traversal: query alpha's vault; the beta node is reachable
	// only via the bridged cross-vault edge (no shared query tokens).
	out, code = captureRun([]string{"query", "--dir", alphaVault, "--query", "zebra quantum flamingo", "--depth", "2"})
	if code != 0 {
		t.Fatalf("query: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "@beta/beta-note") || !strings.Contains(out, "obsidian walrus cathedral") {
		t.Fatalf("beta node not reachable via bridge edge from alpha's vault:\n%s", out)
	}
}

// TestDenBridgeAddEndpointValidation (F18a): with fully-resolved links, an
// endpoint that is neither the den's own identity nor a linked vault refuses
// (bridge_endpoint_unknown); endpoints matching the den id or a resolved link
// pass; when link resolution is incomplete the unknown endpoint degrades to a
// warning instead of a refusal.
func TestDenBridgeAddEndpointValidation(t *testing.T) {
	hermeticDenCLI(t)
	// Durable den with an identity vault = a resolvable live-link target.
	if _, err := den.Create("libdep", den.CreateOptions{Lifetime: den.LifetimeDurable, NoPointer: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := den.Create("val-den", den.CreateOptions{Lifetime: den.LifetimeTask, NoVault: true, NoPointer: true}); err != nil {
		t.Fatal(err)
	}
	if out, code := captureRun([]string{"den", "link", "val-den", "--link", "libdep", "--json"}); code != 0 {
		t.Fatalf("den link: code=%d out=%s", code, out)
	}

	// Clearly-dangling endpoint: every link resolved, endpoint matches nothing.
	out, code := captureRun([]string{"den", "bridge", "add", "val-den", "@libdep", "@ghost-vault", "--json"})
	if code == 0 || !strings.Contains(out, "bridge_endpoint_unknown") {
		t.Fatalf("dangling endpoint: code=%d out=%s", code, out)
	}

	// Den's own id + resolved link vault: accepted, no warnings.
	out, code = captureRun([]string{"den", "bridge", "add", "val-den", "@val-den", "@libdep", "--json"})
	if code != 0 {
		t.Fatalf("valid endpoints: code=%d out=%s", code, out)
	}
	var add struct {
		Added    bool     `json:"added"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &add); err != nil {
		t.Fatalf("envelope: %v out=%s", err, out)
	}
	if !add.Added || len(add.Warnings) != 0 {
		t.Fatalf("valid endpoints envelope: %+v", add)
	}

	// Incomplete resolution (an edit link with no mount): unknown endpoint
	// warns instead of refusing.
	if _, err := den.AddLink("val-den", den.Link{Target: "wghost/p", Mode: den.LinkModeEdit, Warren: "wghost", Project: "p"}); err != nil {
		t.Fatal(err)
	}
	out, code = captureRun([]string{"den", "bridge", "add", "val-den", "@libdep", "@maybe-vault", "--json"})
	if code != 0 {
		t.Fatalf("best-effort add: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "link resolution incomplete") {
		t.Fatalf("expected incomplete-resolution warning: %s", out)
	}
}
