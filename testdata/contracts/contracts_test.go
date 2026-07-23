// Package contracts_test pins the OQ15 JSON envelope fixtures under testdata/contracts.
// Fixtures pin schema:1 shapes for dens/routes JSON verbs. Implementation
// tests exercise live CLI envelopes; these files remain the shared contract sketches.
package contracts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func TestContractFixturesExistAndSchema1(t *testing.T) {
	dir := fixtureDir(t)
	want := []string{
		"den_create.v1.json",
		"den_create_no_pointer.v1.json",
		"den_create_with_links.v1.json",
		"den_create_duplicate.v1.json",
		"den_create_project_collision.v1.json",
		"den_status.v1.json",
		"den_link.v1.json",
		"den_list.v1.json",
		"den_destroy.v1.json",
		"den_destroy_contributed.v1.json",
		"dry_run.v1.json",
		"error.v1.json",
		"route_set_project.v1.json",
		"warren_status_additive.v1.json",
		"warren_propose.v1.json",
		"warren_add.v1.json",
		"warren_sync.v1.json",
		"den_contribute.v1.json",
		"den_adopt.v1.json",
		"resolve.v1.json",
		"den_unlink.v1.json",
		"den_bridge_list.v1.json",
	}
	for _, name := range want {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing contract fixture %s: %v", name, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: invalid JSON: %v", name, err)
		}
		schema, ok := doc["schema"]
		if !ok {
			t.Fatalf("%s: missing schema field", name)
		}
		// encoding/json numbers are float64
		n, ok := schema.(float64)
		if !ok || int(n) != 1 {
			t.Fatalf("%s: schema = %v, want 1", name, schema)
		}
	}
}

func TestDenCreateLinksResolvedVia(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_create_with_links.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int `json:"schema"`
		Links  []struct {
			ResolvedVia string `json:"resolved_via"`
		} `json:"links"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"warren-url": true, "checkout-vault": true, "none": true}
	got := map[string]bool{}
	for _, l := range doc.Links {
		got[l.ResolvedVia] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("den_create_with_links.v1.json missing resolved_via %q", k)
		}
	}
}

// TestDenCreateLinkModes pins the resolved_via→mode mapping of den create
// --ref links: a warren-url match records a PINNED link (mode=link), a
// checkout-vault match records a LIVE link on the checkout's vault id
// (mode=live), and an unresolved ref carries mode null plus a skip warning.
func TestDenCreateLinkModes(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_create_with_links.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Links []struct {
			Ref         string  `json:"ref"`
			Mode        *string `json:"mode"`
			ResolvedVia string  `json:"resolved_via"`
		} `json:"links"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, l := range doc.Links {
		switch l.ResolvedVia {
		case "warren-url":
			if l.Mode == nil || *l.Mode != "link" {
				t.Fatalf("warren-url ref %q must record a pinned link (mode=link): %+v", l.Ref, l.Mode)
			}
		case "checkout-vault":
			if l.Mode == nil || *l.Mode != "live" {
				t.Fatalf("checkout-vault ref %q must record a live link (mode=live): %+v", l.Ref, l.Mode)
			}
		case "none":
			if l.Mode != nil {
				t.Fatalf("unresolved ref %q must have mode null", l.Ref)
			}
			found := false
			for _, w := range doc.Warnings {
				if strings.Contains(w, l.Ref) && strings.Contains(w, "skipped") {
					found = true
				}
			}
			if !found {
				t.Fatalf("unresolved ref %q must carry a skip warning: %v", l.Ref, doc.Warnings)
			}
		}
	}
}

// TestDenStatusFreshness pins the §9 link-freshness shape: state draws from
// {ok, unpushed, stale, unreachable}; an edit link's pending_edits is at
// least its ahead count and forces state unpushed when positive; a pinned
// link that is behind is stale and may disclose source-commit skew
// (additive source_commit).
func TestDenStatusFreshness(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_status.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int `json:"schema"`
		Links  []struct {
			Ref          string  `json:"ref"`
			Mode         string  `json:"mode"`
			PinnedCommit *string `json:"pinned_commit"`
			Ahead        int     `json:"ahead"`
			Behind       int     `json:"behind"`
			PendingEdits int     `json:"pending_edits"`
			State        string  `json:"state"`
			SourceCommit string  `json:"source_commit"`
		} `json:"links"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	states := map[string]bool{"ok": true, "unpushed": true, "stale": true, "unreachable": true}
	sawUnpushed, sawStale := false, false
	for _, l := range doc.Links {
		if !states[l.State] {
			t.Fatalf("link %s state %q outside {ok, unpushed, stale, unreachable}", l.Ref, l.State)
		}
		switch l.Mode {
		case "edit":
			if l.PendingEdits < l.Ahead {
				t.Fatalf("edit link pending_edits (%d) must include ahead (%d)", l.PendingEdits, l.Ahead)
			}
			if l.PendingEdits > 0 {
				sawUnpushed = true
				if l.State != "unpushed" {
					t.Fatalf("edit link with pending edits must be unpushed, got %q", l.State)
				}
			}
		case "link":
			if l.PinnedCommit == nil || *l.PinnedCommit == "" {
				t.Fatalf("pinned link %s must carry pinned_commit", l.Ref)
			}
			if l.Behind > 0 {
				sawStale = true
				if l.State != "stale" {
					t.Fatalf("pinned link behind %d must be stale, got %q", l.Behind, l.State)
				}
				if l.SourceCommit == "" {
					t.Fatalf("fixture must pin the additive source_commit skew field on the stale pinned link")
				}
			}
		}
	}
	if !sawUnpushed || !sawStale {
		t.Fatalf("fixture must pin both the unpushed edit and stale pinned shapes (unpushed=%v stale=%v)", sawUnpushed, sawStale)
	}
}

// TestDenUnlinkEnvelope pins the den unlink success shape: removed is a
// non-empty array of full link bodies (target/mode/warren/project) and
// warnings is always present (empty array, not null).
func TestDenUnlinkEnvelope(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_unlink.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema  int    `json:"schema"`
		DenID   string `json:"den_id"`
		Removed []struct {
			Target  string `json:"target"`
			Mode    string `json:"mode"`
			Warren  string `json:"warren"`
			Project string `json:"project"`
		} `json:"removed"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.DenID == "" || len(doc.Removed) == 0 {
		t.Fatalf("unlink envelope must carry den_id and at least one removed link: %+v", doc)
	}
	for _, l := range doc.Removed {
		if l.Target == "" || l.Mode == "" {
			t.Fatalf("removed link must carry target and mode: %+v", l)
		}
	}
	if doc.Warnings == nil {
		t.Fatal("warnings must be present (empty array, not null)")
	}
}

// TestDenBridgeListEnvelope pins the den bridge list shape: bridges is an
// array (empty allowed, never null) of {from, to, relations} bodies.
func TestDenBridgeListEnvelope(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_bridge_list.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema  int    `json:"schema"`
		DenID   string `json:"den_id"`
		Bridges []struct {
			From      string   `json:"from"`
			To        string   `json:"to"`
			Relations []string `json:"relations"`
		} `json:"bridges"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.DenID == "" || doc.Bridges == nil || len(doc.Bridges) == 0 {
		t.Fatalf("bridge list fixture must carry den_id and at least one bridge: %+v", doc)
	}
	for _, b := range doc.Bridges {
		if b.From == "" || b.To == "" || len(b.Relations) == 0 {
			t.Fatalf("bridge body must carry from/to/relations: %+v", b)
		}
	}
}

func TestDenCreateNoPointerWrittenFalse(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_create_no_pointer.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema         int  `json:"schema"`
		PointerWritten bool `json:"pointer_written"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.PointerWritten {
		t.Fatal("stave attach fixture must have pointer_written: false")
	}
}

func TestErrorEnvelopeHasCodeAndMessage(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "error.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int `json:"schema"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Error == nil || doc.Error.Code == "" || doc.Error.Message == "" {
		t.Fatalf("error envelope incomplete: %+v", doc.Error)
	}
}

func TestDenDestroyContributedField(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_destroy_contributed.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Contributed *struct {
			Added int `json:"added"`
		} `json:"contributed"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Contributed == nil {
		t.Fatal("expected contributed object")
	}
	if doc.Contributed.Added != 2 {
		t.Fatalf("contributed.added = %d, want 2", doc.Contributed.Added)
	}
}

// TestDenDestroyWarningsField pins the additive den destroy warnings[] field
// (schema stays 1): both destroy fixtures carry warnings as an array (never
// null), and the populated fixture discloses the same non-fatal notices
// destroy also prints to stderr.
func TestDenDestroyWarningsField(t *testing.T) {
	for _, name := range []string{"den_destroy.v1.json", "den_destroy_contributed.v1.json"} {
		path := filepath.Join(fixtureDir(t), name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var doc struct {
			Schema   int      `json:"schema"`
			Warnings []string `json:"warnings"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if doc.Schema != 1 {
			t.Fatalf("%s: schema = %d, want 1", name, doc.Schema)
		}
		if doc.Warnings == nil {
			t.Fatalf("%s: warnings must be present (array, not null)", name)
		}
	}
}

func TestProjectCollisionErrorCode(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_create_project_collision.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int `json:"schema"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Error == nil || doc.Error.Code != "den_create_failed" {
		t.Fatalf("collision envelope: %+v", doc.Error)
	}
	if !contains(doc.Error.Message, "already registered") {
		t.Fatalf("message = %q", doc.Error.Message)
	}
}

// TestDenLinkWorktreeAdditive pins the additive cache-backed den link fields:
// an edit link into a cache-backed warren carries the dedicated edit worktree
// path and its branch marmot/edit/<den-id>/<warren-id> (per-(warren,den)
// granularity — every edit link one den holds into one warren shares them).
// Legacy registered-checkout links omit both fields; the base link shape is
// unchanged either way.
func TestDenLinkWorktreeAdditive(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_link.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int    `json:"schema"`
		DenID  string `json:"den_id"`
		Link   *struct {
			Target  string `json:"target"`
			Mode    string `json:"mode"`
			Warren  string `json:"warren"`
			Project string `json:"project"`
		} `json:"link"`
		Worktree string   `json:"worktree"`
		Branch   string   `json:"branch"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Link == nil || doc.Link.Mode != "edit" || doc.Link.Warren == "" || doc.Link.Project == "" {
		t.Fatalf("link body: %+v", doc.Link)
	}
	if doc.Worktree == "" || doc.Branch == "" {
		t.Fatalf("cache-backed link fixture must pin worktree and branch: %+v", doc)
	}
	wantBranch := "marmot/edit/" + doc.DenID + "/" + doc.Link.Warren
	if doc.Branch != wantBranch {
		t.Fatalf("branch %q, want %q (marmot/edit/<den-id>/<warren-id>)", doc.Branch, wantBranch)
	}
	if !contains(doc.Worktree, doc.Link.Warren) || !contains(doc.Worktree, doc.DenID) {
		t.Fatalf("worktree %q must be the edits/<warren>/<den> location", doc.Worktree)
	}
	if doc.Warnings == nil {
		t.Fatal("warnings must be present (empty array, not null)")
	}
}

func TestDenListEnvelope(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_list.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int      `json:"schema"`
		Dens   []string `json:"dens"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Dens) < 1 {
		t.Fatal("dens list empty")
	}
}

// TestDenContributeEnvelope pins the den contribute success shape: the
// contributed counts object is always present, and a committed contribute
// carries a non-empty branch and commit plus the publishing handoff —
// checkout path and ready-to-run push_command (contribute is the flow's
// terminal packaging verb; a paired `warren propose` sees a clean tree and
// cannot supply them).
func TestDenContributeEnvelope(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_contribute.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema int    `json:"schema"`
		DenID  string `json:"den_id"`
		Link   *struct {
			Target  string `json:"target"`
			Mode    string `json:"mode"`
			Warren  string `json:"warren"`
			Project string `json:"project"`
		} `json:"link"`
		Branch      string `json:"branch"`
		Commit      string `json:"commit"`
		Committed   bool   `json:"committed"`
		Checkout    string `json:"checkout"`
		PushCommand string `json:"push_command"`
		Contributed *struct {
			Added      int `json:"added"`
			Updated    int `json:"updated"`
			Superseded int `json:"superseded"`
			Noop       int `json:"noop"`
		} `json:"contributed"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Contributed == nil {
		t.Fatal("contributed counts object must be present")
	}
	if doc.Link == nil || doc.Link.Mode != "edit" {
		t.Fatalf("contribute link must pin mode=edit: %+v", doc.Link)
	}
	if doc.Committed && (doc.Branch == "" || doc.Commit == "") {
		t.Fatalf("committed contribute must carry branch and commit: %+v", doc)
	}
	if doc.Committed && (doc.Checkout == "" || doc.PushCommand == "") {
		t.Fatalf("committed contribute must carry checkout and push_command: %+v", doc)
	}
	if doc.Committed && !strings.Contains(doc.PushCommand, doc.Branch) {
		t.Fatalf("push_command %q must push the contribute branch %q", doc.PushCommand, doc.Branch)
	}
	if doc.Warnings == nil {
		t.Fatal("warnings must be present (even empty)")
	}
}

// TestDenAdoptEnvelope pins the den adopt success shape: the create-shaped
// head fields stay (den_id/den_path/vault_id/routes/pointer_written/links/
// warnings) and the adopt-specific additions are present — vault_moved:true
// (the in-repo .marmot moved into the den) and configs_rewritten as an array
// (may be empty, never null) of project-local MCP configs rewritten to
// `serve --den <id>`.
func TestDenAdoptEnvelope(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "den_adopt.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema           int               `json:"schema"`
		DenID            string            `json:"den_id"`
		DenPath          string            `json:"den_path"`
		VaultID          string            `json:"vault_id"`
		Routes           map[string]string `json:"routes"`
		VaultMoved       *bool             `json:"vault_moved"`
		PointerWritten   bool              `json:"pointer_written"`
		ConfigsRewritten []string          `json:"configs_rewritten"`
		Warnings         []string          `json:"warnings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.DenID == "" || doc.DenPath == "" || doc.VaultID != doc.DenID {
		t.Fatalf("adopt head fields: %+v", doc)
	}
	if doc.Routes["project_path"] == "" {
		t.Fatalf("routes.project_path must be pinned: %+v", doc.Routes)
	}
	if doc.VaultMoved == nil || !*doc.VaultMoved {
		t.Fatal("adopt fixture must pin vault_moved: true")
	}
	if doc.ConfigsRewritten == nil {
		t.Fatal("configs_rewritten must be present (array, not null)")
	}
	if doc.Warnings == nil {
		t.Fatal("warnings must be present (empty array, not null)")
	}
}

// TestWarrenProposePushCommand pins the stave-facing propose success shape:
// a committed proposal always carries a non-empty push_command (stave relays
// it verbatim) and a branch, and is not nothing_to_propose.
func TestWarrenProposePushCommand(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "warren_propose.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema           int      `json:"schema"`
		Branch           string   `json:"branch"`
		Committed        bool     `json:"committed"`
		NothingToPropose bool     `json:"nothing_to_propose"`
		PushCommand      string   `json:"push_command"`
		Warnings         []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Committed || doc.NothingToPropose {
		t.Fatalf("fixture must pin the committed success shape: %+v", doc)
	}
	if doc.PushCommand == "" || doc.Branch == "" {
		t.Fatalf("committed proposal must carry push_command and branch: %+v", doc)
	}
	if !contains(doc.PushCommand, doc.Branch) {
		t.Fatalf("push_command %q must reference branch %q", doc.PushCommand, doc.Branch)
	}
	if doc.Warnings == nil {
		t.Fatal("warnings must be present (empty array, not null)")
	}
}

// TestWarrenAddEnvelope pins the stave-facing warren add success shape: the
// cache identity fields (warren_id, url, derived cache/checkout paths) and
// the provenance pin are always present, and warnings is an array even when
// empty. default_branch may legitimately be "" (soft resolution), so only
// its presence-as-string is pinned, not its content.
func TestWarrenAddEnvelope(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "warren_add.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema        int      `json:"schema"`
		WarrenID      string   `json:"warren_id"`
		URL           string   `json:"url"`
		CachePath     string   `json:"cache_path"`
		CheckoutPath  string   `json:"checkout_path"`
		DefaultBranch string   `json:"default_branch"`
		PinnedCommit  string   `json:"pinned_commit"`
		Warnings      []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.WarrenID == "" || doc.URL == "" {
		t.Fatalf("add envelope must carry warren_id and url: %+v", doc)
	}
	if doc.CachePath == "" || doc.CheckoutPath == "" {
		t.Fatalf("add envelope must carry cache_path and checkout_path: %+v", doc)
	}
	if !contains(doc.CachePath, doc.WarrenID+".git") {
		t.Fatalf("cache_path %q must be the derived <warren-cache>/<id>.git", doc.CachePath)
	}
	if !contains(doc.CheckoutPath, "checkouts") || !contains(doc.CheckoutPath, doc.WarrenID) {
		t.Fatalf("checkout_path %q must be the shared checkouts/<id> location", doc.CheckoutPath)
	}
	if doc.PinnedCommit == "" {
		t.Fatal("a successful add always pins the shared checkout")
	}
	if doc.Warnings == nil {
		t.Fatal("warnings must be present (empty array, not null)")
	}
}

// TestWarrenSyncEnvelope pins the stave-facing warren sync shape (stave's
// memory sync consumes it): a warrens array whose entries carry stable
// {id, fetched, previous_commit, pinned_commit, updated} fields, an
// updated entry re-pins to a new commit, and per-warren failures ride along
// as error strings without removing the entry.
func TestWarrenSyncEnvelope(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "warren_sync.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema  int `json:"schema"`
		Warrens []struct {
			ID             string `json:"id"`
			Fetched        bool   `json:"fetched"`
			PreviousCommit string `json:"previous_commit"`
			PinnedCommit   string `json:"pinned_commit"`
			Updated        bool   `json:"updated"`
			Error          string `json:"error"`
		} `json:"warrens"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Warrens) == 0 {
		t.Fatal("warrens array must be present and non-empty in the fixture")
	}
	sawUpdated, sawError := false, false
	for _, entry := range doc.Warrens {
		if entry.ID == "" {
			t.Fatalf("every sync entry carries its warren id: %+v", entry)
		}
		if entry.Updated {
			sawUpdated = true
			if !entry.Fetched || entry.PinnedCommit == "" || entry.PinnedCommit == entry.PreviousCommit {
				t.Fatalf("updated entry must re-pin to a new fetched commit: %+v", entry)
			}
		}
		if entry.Error != "" {
			sawError = true
			if entry.Updated {
				t.Fatalf("a failed entry can never be updated: %+v", entry)
			}
		}
	}
	if !sawUpdated {
		t.Fatal("fixture must pin the updated:true shape")
	}
	if !sawError {
		t.Fatal("fixture must pin the per-warren error shape (failures do not abort the loop)")
	}
	if doc.Warnings == nil {
		t.Fatal("warnings must be present (empty array, not null)")
	}
}

// TestResolveEnvelope pins the `marmot resolve --json` diagnostic shape:
// resolved_via draws from the fixed {warren-url, checkout-vault, none}
// vocabulary (the same one den create links[].resolved_via uses), a
// warren-url resolution always names the matched warren and project, and
// detail is always a non-empty human-readable explanation.
func TestResolveEnvelope(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "resolve.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema      int    `json:"schema"`
		ResolvedVia string `json:"resolved_via"`
		Warren      string `json:"warren"`
		Project     string `json:"project"`
		VaultID     string `json:"vault_id"`
		Detail      string `json:"detail"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	vocab := map[string]bool{"warren-url": true, "checkout-vault": true, "none": true}
	if !vocab[doc.ResolvedVia] {
		t.Fatalf("resolved_via %q outside {warren-url, checkout-vault, none}", doc.ResolvedVia)
	}
	if doc.ResolvedVia == "warren-url" && (doc.Warren == "" || doc.Project == "") {
		t.Fatalf("warren-url resolution must name warren and project: %+v", doc)
	}
	if doc.Detail == "" {
		t.Fatal("detail must be present and non-empty")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && (func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})()))
}
