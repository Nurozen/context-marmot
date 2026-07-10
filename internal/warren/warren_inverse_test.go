package warren

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/flock"
	"github.com/nurozen/context-marmot/internal/node"
)

// registerAndMount is a small fixture: a warren with the named projects,
// registered in a fresh workspace.
func registerAndMount(t *testing.T, projects ...string) (workspace, warrenRoot string) {
	t.Helper()
	workspace = t.TempDir()
	warrenRoot = t.TempDir()
	writeWarrenFixture(t, warrenRoot, "product-platform", projects...)
	if _, err := RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return workspace, warrenRoot
}

func materializeFixtureProject(t *testing.T, workspace, warrenRoot, projectID string) {
	t.Helper()
	project := Project{ProjectID: projectID, Path: filepath.ToSlash(filepath.Join("projects", projectID, ".marmot"))}
	if _, err := Materialize(workspaceMarmotDir(workspace), "product-platform", project, warrenRoot, ""); err != nil {
		t.Fatalf("Materialize %s: %v", projectID, err)
	}
}

// TestUnmountRoundTrip: mount then unmount leaves the state file deep-equal
// to the pre-mount state — unmount is non-destructive.
func TestUnmountRoundTrip(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a", "project-b")
	statePath := filepath.Join(workspace, ".marmot", "_warren.md")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	if _, err := Mount(workspace, "product-platform", []string{"project-a", "project-b"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if _, err := Unmount(workspace, "product-platform", []string{"project-a", "project-b"}); err != nil {
		t.Fatalf("Unmount: %v", err)
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("mount+unmount round-trip changed state:\nbefore: %s\nafter: %s", before, after)
	}
}

// TestUnmountClearsEditable: unmounting an editable project drops both flags.
func TestUnmountClearsEditable(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a")
	if _, err := SetEditable(workspace, "product-platform", "project-a", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}
	state, err := Unmount(workspace, "product-platform", []string{"project-a"})
	if err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	entry := state.Warrens["product-platform"]
	if len(entry.ActiveProjects) != 0 || len(entry.EditableProjects) != 0 {
		t.Fatalf("unmount left flags behind: active=%v editable=%v", entry.ActiveProjects, entry.EditableProjects)
	}
}

// TestUnmountValidation: unknown project IDs are per-item errors naming the
// warren, and nothing is written on refusal.
func TestUnmountValidation(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a")
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	_, err := Unmount(workspace, "product-platform", []string{"project-a", "ghost"})
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) || !strings.Contains(err.Error(), "product-platform") {
		t.Fatalf("Unmount ghost err = %v, want per-item error naming warren", err)
	}
	state, _, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	if got := state.Warrens["product-platform"].ActiveProjects; len(got) != 1 || got[0] != "project-a" {
		t.Fatalf("refused unmount mutated state: %v", got)
	}
	if _, err := Unmount(workspace, "ghost-warren", []string{"project-a"}); err == nil {
		t.Fatal("expected unregistered-warren error")
	}
}

// TestUnmountWorksWithoutCheckout: validation runs against workspace state,
// not the manifest, so unmount is the escape hatch for unreachable warrens.
func TestUnmountWorksWithoutCheckout(t *testing.T) {
	workspace, warrenRoot := registerAndMount(t, "project-a")
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := os.RemoveAll(warrenRoot); err != nil {
		t.Fatalf("remove checkout: %v", err)
	}
	if _, err := Unmount(workspace, "product-platform", []string{"project-a"}); err != nil {
		t.Fatalf("Unmount with checkout gone: %v", err)
	}
}

// TestDropMaterializedPartialThenLast: dropping one of two caches keeps the
// warren-level Materialized flag; dropping the last clears it. Caches are
// removed whole (projects/<p>/ dir).
func TestDropMaterializedPartialThenLast(t *testing.T) {
	workspace, warrenRoot := registerAndMount(t, "project-a", "project-b")
	if _, err := Mount(workspace, "product-platform", []string{"project-a", "project-b"}, true); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	materializeFixtureProject(t, workspace, warrenRoot, "project-a")
	materializeFixtureProject(t, workspace, warrenRoot, "project-b")
	wsMarmot := workspaceMarmotDir(workspace)

	if got := MaterializedProjects(wsMarmot, "product-platform"); len(got) != 2 {
		t.Fatalf("MaterializedProjects = %v, want 2", got)
	}
	if err := DropMaterialized(wsMarmot, workspace, "product-platform", []string{"project-a"}); err != nil {
		t.Fatalf("Drop project-a: %v", err)
	}
	if dirExists(filepath.Dir(materializedProjectPath(wsMarmot, "product-platform", "project-a"))) {
		t.Fatal("project-a cache dir survived the drop")
	}
	state, _, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	if !entry.Materialized {
		t.Fatal("Materialized cleared while project-b cache remains")
	}
	if len(entry.ActiveProjects) != 2 {
		t.Fatalf("drop must not unmount; active=%v", entry.ActiveProjects)
	}

	if err := DropMaterialized(wsMarmot, workspace, "product-platform", []string{"project-b"}); err != nil {
		t.Fatalf("Drop project-b: %v", err)
	}
	state, _, err = LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	if state.Warrens["product-platform"].Materialized {
		t.Fatal("Materialized flag survived dropping the last cache")
	}

	// Dropping a project without a cache is a per-item error.
	if err := DropMaterialized(wsMarmot, workspace, "product-platform", []string{"project-a"}); err == nil {
		t.Fatal("expected no-cache error")
	}
}

// TestUnregisterRefusalsAndForce: unregister names the blocking projects and
// exact commands; --force removes the whole cache tree and the entry.
func TestUnregisterRefusalsAndForce(t *testing.T) {
	workspace, warrenRoot := registerAndMount(t, "project-a")
	wsMarmot := workspaceMarmotDir(workspace)
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, true); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	materializeFixtureProject(t, workspace, warrenRoot, "project-a")

	err := Unregister(wsMarmot, workspace, "product-platform", false)
	if err == nil || !strings.Contains(err.Error(), "project-a") || !strings.Contains(err.Error(), "warren unmount") {
		t.Fatalf("Unregister with mounts err = %v, want refusal naming project and unmount command", err)
	}
	if _, err := Unmount(workspace, "product-platform", []string{"project-a"}); err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	err = Unregister(wsMarmot, workspace, "product-platform", false)
	if err == nil || !strings.Contains(err.Error(), "burrow --drop") {
		t.Fatalf("Unregister with caches err = %v, want refusal naming burrow --drop", err)
	}

	if err := Unregister(wsMarmot, workspace, "product-platform", true); err != nil {
		t.Fatalf("Unregister --force: %v", err)
	}
	if dirExists(filepath.Join(wsMarmot, ".marmot-data", "warrens", "product-platform")) {
		t.Fatal("--force left the warren cache tree behind")
	}
	state, _, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	if _, ok := state.Warrens["product-platform"]; ok {
		t.Fatal("entry survived unregister --force")
	}
	if err := Unregister(wsMarmot, workspace, "product-platform", false); err == nil {
		t.Fatal("expected unregistered-warren error")
	}
}

// TestClearStaleMaterialized: clears the warren-level flag only when the
// per-project cache ground truth is empty (the stranded-flag repair for a
// mount whose materialization failed before any cache existed).
func TestClearStaleMaterialized(t *testing.T) {
	workspace, warrenRoot := registerAndMount(t, "project-a")
	wsMarmot := workspaceMarmotDir(workspace)
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, true); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// With a real cache the flag must survive.
	materializeFixtureProject(t, workspace, warrenRoot, "project-a")
	if err := ClearStaleMaterialized(wsMarmot, workspace, "product-platform"); err != nil {
		t.Fatalf("ClearStaleMaterialized (cache present): %v", err)
	}
	state, _, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	if !state.Warrens["product-platform"].Materialized {
		t.Fatal("flag cleared while a burrow cache exists")
	}

	// Zero caches on disk (the stranded state): the flag is cleared.
	if err := os.RemoveAll(filepath.Join(wsMarmot, ".marmot-data", "warrens", "product-platform", "projects")); err != nil {
		t.Fatal(err)
	}
	if err := ClearStaleMaterialized(wsMarmot, workspace, "product-platform"); err != nil {
		t.Fatalf("ClearStaleMaterialized (no caches): %v", err)
	}
	state, _, err = LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	if state.Warrens["product-platform"].Materialized {
		t.Fatal("stranded Materialized flag survived ClearStaleMaterialized")
	}

	// Unknown warren is a no-op, not an error.
	if err := ClearStaleMaterialized(wsMarmot, workspace, "nope"); err != nil {
		t.Fatalf("ClearStaleMaterialized (unknown warren): %v", err)
	}
}

// TestUnregisterRevalidatesUnderStateLock: unregister's no---force
// preconditions must be evaluated on the flocked state, not an earlier
// unlocked snapshot — a concurrent `warren mount` that lands while
// unregister waits for the state lock must flip the outcome to a refusal
// (before the fix, unregister proceeded on its stale precondition check and
// silently dropped the other process's just-created mount and caches).
func TestUnregisterRevalidatesUnderStateLock(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a")
	wsMarmot := workspaceMarmotDir(workspace)
	statePath, err := workspaceStatePath(workspace)
	if err != nil {
		t.Fatalf("workspaceStatePath: %v", err)
	}

	// Hold the workspace state flock (each flock acquisition opens its own
	// fd, so BSD flock serializes goroutines within one process too).
	held := make(chan struct{})
	release := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- flock.WithLock(statePath+".lock", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	// Unregister (no force) must wait for the lock instead of pre-checking.
	unregDone := make(chan error, 1)
	go func() { unregDone <- Unregister(wsMarmot, workspace, "product-platform", false) }()
	select {
	case err := <-unregDone:
		t.Fatalf("Unregister completed while the state lock was held (err=%v)", err)
	case <-time.After(150 * time.Millisecond):
		// Still blocked — good.
	}

	// The "concurrent mount": while unregister waits, a project becomes
	// active in the state file.
	state, body, err := LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	entry := state.Warrens["product-platform"]
	entry.ActiveProjects = []string{"project-a"}
	state.Warrens["product-platform"] = entry
	if err := SaveWorkspaceState(workspace, state, body); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}
	close(release)
	if err := <-lockDone; err != nil {
		t.Fatalf("lock holder: %v", err)
	}

	err = <-unregDone
	if err == nil || !strings.Contains(err.Error(), "mounted project") {
		t.Fatalf("Unregister err = %v, want refusal naming the concurrently mounted project", err)
	}
	state, _, err = LoadWorkspaceState(workspace)
	if err != nil {
		t.Fatalf("LoadWorkspaceState after refusal: %v", err)
	}
	if got := state.Warrens["product-platform"].ActiveProjects; len(got) != 1 || got[0] != "project-a" {
		t.Fatalf("concurrent mount lost: ActiveProjects = %v", got)
	}
}

// TestSetEditableRefusalNamesBurrowDrop: the materialized refusal points at
// the inverse verb now that it exists (C1 swapped A4's manual escape text).
func TestSetEditableRefusalNamesBurrowDrop(t *testing.T) {
	workspace, warrenRoot := registerAndMount(t, "project-a")
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, true); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	materializeFixtureProject(t, workspace, warrenRoot, "project-a")
	_, err := SetEditable(workspace, "product-platform", "project-a", true)
	if err == nil || !strings.Contains(err.Error(), "burrow --drop") {
		t.Fatalf("SetEditable err = %v, want refusal naming 'burrow --drop'", err)
	}
}

// TestStatusDegradesWhenCheckoutGone (C6): a known entry with an unreadable
// manifest yields rows from workspace state (Registered=false,
// Available=false) plus a warning, instead of an opaque error.
func TestStatusDegradesWhenCheckoutGone(t *testing.T) {
	workspace, warrenRoot := registerAndMount(t, "project-a")
	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if _, err := SetEditable(workspace, "product-platform", "project-a", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}
	if err := os.RemoveAll(warrenRoot); err != nil {
		t.Fatalf("remove checkout: %v", err)
	}

	var warned bytes.Buffer
	oldWarn := warnWriter
	warnWriter = &warned
	defer func() { warnWriter = oldWarn }()

	statuses, err := Status(workspace, "product-platform")
	if err != nil {
		t.Fatalf("Status must degrade, got error: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %+v, want 1 degraded row", statuses)
	}
	row := statuses[0]
	if row.Registered || row.Available || !row.Active || !row.Editable {
		t.Fatalf("degraded row = %+v, want Active+Editable, not Registered/Available", row)
	}
	if !strings.Contains(warned.String(), "unreadable") {
		t.Fatalf("expected degradation warning, got %q", warned.String())
	}
}

// TestMountRefusesVaultIDCollision (C7): a second warren project claiming an
// already-mounted vault ID is refused; the first mount stays intact.
func TestMountRefusesVaultIDCollision(t *testing.T) {
	workspace := t.TempDir()
	warrenA := t.TempDir()
	warrenB := t.TempDir()
	writeWarrenFixture(t, warrenA, "warren-a", "project-a")
	// warren-b's project gets the SAME vault ID as warren-a's project.
	manifest := &Manifest{WarrenID: "warren-b"}
	marmotDir := filepath.Join(warrenB, "projects", "project-x", ".marmot")
	if err := SaveProjectMetadata(marmotDir, &ProjectMetadata{
		ProjectID: "project-x",
		WarrenID:  "warren-b",
		VaultID:   "project-a-vault",
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata: %v", err)
	}
	manifest.Projects = append(manifest.Projects, Project{
		ProjectID: "project-x",
		Path:      "projects/project-x/.marmot",
	})
	if err := SaveManifest(warrenB, manifest, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if _, err := RegisterWorkspaceWarren(workspace, "warren-a", warrenA); err != nil {
		t.Fatalf("Register warren-a: %v", err)
	}
	if _, err := RegisterWorkspaceWarren(workspace, "warren-b", warrenB); err != nil {
		t.Fatalf("Register warren-b: %v", err)
	}
	if _, err := Mount(workspace, "warren-a", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount warren-a: %v", err)
	}

	_, err := Mount(workspace, "warren-b", []string{"project-x"}, false)
	if err == nil || !strings.Contains(err.Error(), "collides with warren-a/project-a") {
		t.Fatalf("Mount collision err = %v, want refusal naming warren-a/project-a", err)
	}
	state, _, loadErr := LoadWorkspaceState(workspace)
	if loadErr != nil {
		t.Fatalf("LoadWorkspaceState: %v", loadErr)
	}
	if got := state.Warrens["warren-b"].ActiveProjects; len(got) != 0 {
		t.Fatalf("refused mount still activated projects: %v", got)
	}
	if got := state.Warrens["warren-a"].ActiveProjects; len(got) != 1 {
		t.Fatalf("first mount damaged by refusal: %v", got)
	}

	// SetEditable's auto-mount takes the same refusal.
	_, err = SetEditable(workspace, "warren-b", "project-x", true)
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("SetEditable auto-mount collision err = %v", err)
	}

	// Re-mounting the same project is not a collision with itself.
	if _, err := Mount(workspace, "warren-a", []string{"project-a"}, false); err != nil {
		t.Fatalf("idempotent re-mount refused: %v", err)
	}
}

// TestMountSelfAliasesLiveVault: mounting the warren copy of *this* project
// (same vault ID as the local vault) succeeds as a self-alias: the mount
// activates the warren's bridges against the LIVE vault, claims no route,
// and can never be editable. (This replaces the pre-alias deliberate
// deviation that warned and let the mount shadow the live vault.)
func TestMountSelfAliasesLiveVault(t *testing.T) {
	workspace, _ := registerAndMount(t, "project-a")
	writeSelfVaultConfig(t, workspace, "project-a-vault")

	var warned bytes.Buffer
	oldWarn := warnWriter
	warnWriter = &warned
	defer func() { warnWriter = oldWarn }()

	if _, err := Mount(workspace, "product-platform", []string{"project-a"}, false); err != nil {
		t.Fatalf("self-alias mount must succeed: %v", err)
	}
	if !strings.Contains(warned.String(), "mounting as an alias of the live local vault") {
		t.Fatalf("expected self-alias note, got %q", warned.String())
	}

	mounts, err := ActiveMounts(workspaceMarmotDir(workspace))
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	if len(mounts) != 1 || !mounts[0].SelfAlias {
		t.Fatalf("mounts = %+v, want one SelfAlias mount", mounts)
	}

	// A subsequent status carries no editable flag.
	statuses, err := Status(workspace, "product-platform")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, status := range statuses {
		if status.Editable {
			t.Fatalf("self-alias status carries editable flag: %+v", status)
		}
		if !status.SelfAlias {
			t.Fatalf("status missing SelfAlias: %+v", status)
		}
	}
}

// writeSelfVaultConfig gives the workspace's live vault a vault_id so warren
// projects carrying the same ID become self-aliases.
func writeSelfVaultConfig(t *testing.T, workspace, vaultID string) {
	t.Helper()
	marmotDir := workspaceMarmotDir(workspace)
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWriteEditableNode: the shared MCP/API write-back helper refuses
// read-only mounts, persists the node, upserts the embedding, and degrades
// an embedding failure to a warning without rolling the node write back.
func TestWriteEditableNode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := &node.Node{
		ID:        "service/api",
		Type:      "module",
		Namespace: "default",
		Status:    node.StatusActive,
		Summary:   "Service API",
	}

	if _, err := WriteEditableNode(ProjectStatus{ProjectID: "p", Path: dir, Editable: false}, n, nil, "", ""); err == nil {
		t.Fatal("expected read-only refusal")
	}

	mount := ProjectStatus{ProjectID: "p", Path: dir, Editable: true}
	vec := []float32{0.1, 0.2, 0.3}
	warning, err := WriteEditableNode(mount, n, vec, "hash", "test-model")
	if err != nil || warning != "" {
		t.Fatalf("WriteEditableNode = (%q, %v), want clean success", warning, err)
	}
	store := node.NewStore(dir)
	if _, err := store.LoadNode(store.NodePath("service/api")); err != nil {
		t.Fatalf("node not persisted: %v", err)
	}
	emb, err := embedding.NewStoreReadOnly(filepath.Join(dir, ".marmot-data", "embeddings.db"))
	if err != nil {
		t.Fatalf("open embeddings: %v", err)
	}
	defer func() { _ = emb.Close() }()
	if count := emb.Count(); count != 1 {
		t.Fatalf("embedding rows = %d, want 1", count)
	}

	// Embedding failure degrades to a warning; the node write stays durable.
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".marmot-data"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	warning, err = WriteEditableNode(ProjectStatus{ProjectID: "p", Path: broken, Editable: true}, n, vec, "hash", "test-model")
	if err != nil {
		t.Fatalf("embedding failure must not fail the write: %v", err)
	}
	if !strings.Contains(warning, "embedding not updated") {
		t.Fatalf("warning = %q, want embedding degradation", warning)
	}
	brokenStore := node.NewStore(broken)
	if _, err := brokenStore.LoadNode(brokenStore.NodePath("service/api")); err != nil {
		t.Fatalf("node write rolled back on embedding failure: %v", err)
	}
}
