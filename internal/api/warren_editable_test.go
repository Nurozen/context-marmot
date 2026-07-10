package api

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// TestWarrenEditableWriteLandsInCheckoutDespiteStaleMaterializedFlag is the
// A4 server-side regression: with a stale workspace state carrying BOTH the
// editable and materialized flags (written by a pre-refusal binary or by
// hand), an editable node update must land in the project's checkout — the
// documented write target — not in the burrow cache that never syncs back.
func TestWarrenEditableWriteLandsInCheckoutDespiteStaleMaterializedFlag(t *testing.T) {
	server, engine := newTestServer(t)
	handler := server.Handler()
	workspaceRoot := filepath.Dir(engine.MarmotDir)
	warrenRoot := setupAPIWarren(t, workspaceRoot, "product-platform", "project-a", "project-a-vault")

	// Burrow the project so a materialized cache exists.
	project := warrenpkg.Project{ProjectID: "project-a", Path: "projects/project-a/.marmot"}
	cachePath, err := warrenpkg.Materialize(engine.MarmotDir, "product-platform", project, warrenRoot, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// Hand-craft the stale both-flags state, bypassing the refusal guards.
	state := &warrenpkg.WorkspaceState{Warrens: map[string]warrenpkg.WorkspaceWarren{
		"product-platform": {
			Path:             warrenRoot,
			ActiveProjects:   []string{"project-a"},
			EditableProjects: []string{"project-a"},
			Materialized:     true,
		},
	}}
	if err := warrenpkg.SaveWorkspaceState(workspaceRoot, state, ""); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}

	updatePath := "/api/node/" + url.PathEscape("@project-a-vault/service/api")
	rec := doRequest(t, handler, "PUT", updatePath, `{"summary":"edited via API"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected editable write 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// The write landed in the CHECKOUT.
	checkoutStore := node.NewStore(filepath.Join(warrenRoot, "projects", "project-a", ".marmot"))
	updated, err := checkoutStore.LoadNode(checkoutStore.NodePath("service/api"))
	if err != nil {
		t.Fatalf("load checkout node: %v", err)
	}
	if updated.Summary != "edited via API" {
		t.Fatalf("checkout summary = %q, want the edit (write went to the burrow cache instead)", updated.Summary)
	}

	// ...and NOT in the burrow cache.
	cacheStore := node.NewStore(cachePath)
	cached, err := cacheStore.LoadNode(cacheStore.NodePath("service/api"))
	if err != nil {
		t.Fatalf("load cached node: %v", err)
	}
	if cached.Summary == "edited via API" {
		t.Fatal("edit landed in the materialized cache")
	}
}
