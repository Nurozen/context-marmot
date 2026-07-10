package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/warren"
)

// TestContextWriteEditableWarrenMount (C8): an @vault-id/... context_write
// against an active *editable* mount updates the node in the project's own
// checkout, refreshes the registry, and the change is queryable afterwards.
func TestContextWriteEditableWarrenMount(t *testing.T) {
	eng := warrenEngine(t)
	warrenRoot := warrenFixture(t, eng, "wp", "proj-a", "proj-a-vault")
	workspaceRoot := filepath.Dir(eng.MarmotDir)
	if _, err := warren.SetEditable(workspaceRoot, "wp", "proj-a", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}
	if err := eng.ReloadWarrenState(); err != nil {
		t.Fatalf("ReloadWarrenState: %v", err)
	}

	res, err := eng.HandleContextWrite(context.Background(), makeCallToolRequest("context_write", map[string]any{
		"id":      "@proj-a-vault/service/api",
		"type":    "module",
		"summary": "Rewritten through MCP editable warren write",
		"tags":    []string{"warren-write"},
	}))
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("editable @-write rejected: %s", resultText(t, res))
	}
	payload := resultText(t, res)
	if !strings.Contains(payload, `"status":"updated"`) && !strings.Contains(payload, `"status": "updated"`) {
		t.Fatalf("payload = %s, want updated status", payload)
	}
	if !strings.Contains(payload, "wp/proj-a") {
		t.Fatalf("payload = %s, want provenance naming the mount", payload)
	}

	// The write landed under the CHECKOUT (never a cache), and is durable.
	checkout := filepath.Join(warrenRoot, "projects", "proj-a", ".marmot")
	store := node.NewStore(checkout)
	diskNode, err := store.LoadNode(store.NodePath("service/api"))
	if err != nil {
		t.Fatalf("load node from checkout: %v", err)
	}
	if diskNode.Summary != "Rewritten through MCP editable warren write" {
		t.Fatalf("checkout summary = %q, not updated", diskNode.Summary)
	}
	if len(diskNode.Tags) != 1 || diskNode.Tags[0] != "warren-write" {
		t.Fatalf("checkout tags = %v", diskNode.Tags)
	}

	// Queryable after the write's built-in refresh.
	qres, err := eng.HandleContextQuery(context.Background(), makeCallToolRequest("context_query", map[string]any{
		"query": "Rewritten through MCP editable warren write",
	}))
	if err != nil {
		t.Fatalf("HandleContextQuery: %v", err)
	}
	if text := resultText(t, qres); !strings.Contains(text, "@proj-a-vault/service/api") {
		t.Fatalf("query after editable write missed the node:\n%s", text)
	}

	// No local shadow node was created.
	if eng.GetGraph().NodeCount() != 0 {
		t.Fatalf("local graph gained %d node(s) from an @-write", eng.GetGraph().NodeCount())
	}
}

// TestContextWriteWarrenMatrix: read-only mounts and unmounted vaults are
// rejected with actionable text; a missing node in an editable mount is a
// not-found rejection (MCP @-writes update existing nodes only, mirroring
// the API path).
func TestContextWriteWarrenMatrix(t *testing.T) {
	eng := warrenEngine(t)
	warrenFixture(t, eng, "wp", "proj-a", "proj-a-vault")
	workspaceRoot := filepath.Dir(eng.MarmotDir)

	// Mounted but read-only.
	res, err := eng.HandleContextWrite(context.Background(), makeCallToolRequest("context_write", map[string]any{
		"id":      "@proj-a-vault/service/api",
		"type":    "module",
		"summary": "Should be rejected while read-only",
	}))
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "marmot warren edit") {
		t.Fatalf("read-only @-write = %s, want rejection naming 'marmot warren edit'", resultText(t, res))
	}

	// Unmounted vault.
	res, err = eng.HandleContextWrite(context.Background(), makeCallToolRequest("context_write", map[string]any{
		"id":      "@ghost-vault/service/api",
		"type":    "module",
		"summary": "Should be rejected while unmounted",
	}))
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "not an editable warren mount") {
		t.Fatalf("unmounted @-write = %s, want editable-mount rejection", resultText(t, res))
	}

	// Editable, but the node does not exist: creation is not supported.
	if _, err := warren.SetEditable(workspaceRoot, "wp", "proj-a", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}
	res, err = eng.HandleContextWrite(context.Background(), makeCallToolRequest("context_write", map[string]any{
		"id":      "@proj-a-vault/brand/new",
		"type":    "module",
		"summary": "Creating remote nodes is out of scope",
	}))
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "not found") {
		t.Fatalf("create-via-@-write = %s, want not-found rejection", resultText(t, res))
	}
}
