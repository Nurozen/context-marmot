package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

func TestBuildEngineEnforcesWarrenBridgesForActiveMounts(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	writeTestConfig(t, marmotDir, "vault-a")

	warrenRoot := t.TempDir()
	saveWarrenProject(t, warrenRoot, "product-platform", "project-a", "vault-a")
	saveWarrenProject(t, warrenRoot, "product-platform", "project-b", "vault-b")
	saveWarrenProject(t, warrenRoot, "product-platform", "project-c", "vault-c")
	if err := warrenpkg.SaveManifest(warrenRoot, &warrenpkg.Manifest{
		WarrenID: "product-platform",
		Projects: []warrenpkg.Project{
			{ProjectID: "project-a", Path: "projects/project-a/.marmot"},
			{ProjectID: "project-b", Path: "projects/project-b/.marmot"},
			{ProjectID: "project-c", Path: "projects/project-c/.marmot"},
		},
		Bridges: []warrenpkg.Bridge{
			{Source: "project-a", Target: "project-b", Relations: []string{"calls"}},
			{Source: "project-a", Target: "project-c", Relations: []string{"calls"}},
		},
	}, ""); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if _, err := warrenpkg.RegisterWorkspaceWarren(workspace, "product-platform", warrenRoot); err != nil {
		t.Fatalf("RegisterWorkspaceWarren: %v", err)
	}
	if _, err := warrenpkg.Mount(workspace, "product-platform", []string{"project-a", "project-b"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	result, err := buildEngine(marmotDir)
	if err != nil {
		t.Fatalf("buildEngine: %v", err)
	}
	defer result.Cleanup()

	if result.Engine.NSManager == nil {
		t.Fatal("expected Warren bridge declarations to attach namespace manager")
	}
	if err := result.Engine.NSManager.ValidateCrossVaultEdge("vault-a", "vault-b", "calls"); err != nil {
		t.Fatalf("expected active Warren bridge to allow calls: %v", err)
	}
	if err := result.Engine.NSManager.ValidateCrossVaultEdge("vault-a", "vault-b", "imports"); err == nil {
		t.Fatal("expected disallowed relation to be rejected")
	}
	if err := result.Engine.NSManager.ValidateCrossVaultEdge("vault-a", "vault-c", "calls"); err == nil {
		t.Fatal("expected dormant Warren bridge endpoint to be unavailable")
	}

	knownVaults := make(map[string]bool)
	for _, id := range result.Engine.VaultRegistry.KnownVaultIDs() {
		knownVaults[id] = true
	}
	if !knownVaults["vault-b"] {
		t.Fatalf("expected active bridge target vault-b in registry, got %+v", knownVaults)
	}
	if knownVaults["vault-c"] {
		t.Fatalf("did not expect dormant vault-c in registry, got %+v", knownVaults)
	}

	ctx := context.Background()
	allowed, err := result.Engine.HandleContextWrite(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_write",
			Arguments: map[string]any{
				"id":      "local/allowed",
				"type":    "module",
				"summary": "Allowed Warren bridge relation",
				"edges": []map[string]any{
					{"target": "@vault-b/service/api", "relation": "calls"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("allowed context_write: %v", err)
	}
	if allowed.IsError {
		t.Fatalf("expected allowed Warren bridge write, got %s", toolResultText(allowed))
	}

	disallowed, err := result.Engine.HandleContextWrite(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_write",
			Arguments: map[string]any{
				"id":      "local/disallowed",
				"type":    "module",
				"summary": "Disallowed Warren bridge relation",
				"edges": []map[string]any{
					{"target": "@vault-b/service/api", "relation": "imports"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("disallowed context_write: %v", err)
	}
	if !disallowed.IsError || !strings.Contains(toolResultText(disallowed), "not allowed") {
		t.Fatalf("expected disallowed relation error, got %s", toolResultText(disallowed))
	}

	dormant, err := result.Engine.HandleContextWrite(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_write",
			Arguments: map[string]any{
				"id":      "local/dormant",
				"type":    "module",
				"summary": "Dormant Warren bridge endpoint",
				"edges": []map[string]any{
					{"target": "@vault-c/service/api", "relation": "calls"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("dormant context_write: %v", err)
	}
	if !dormant.IsError || !strings.Contains(toolResultText(dormant), "no cross-vault bridge") {
		t.Fatalf("expected dormant endpoint bridge error, got %s", toolResultText(dormant))
	}
}

func writeTestConfig(t *testing.T, marmotDir, vaultID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(marmotDir, ".marmot-data"), 0o755); err != nil {
		t.Fatalf("mkdir marmot dir: %v", err)
	}
	content := "---\nversion: \"1\"\nvault_id: " + vaultID + "\nnamespace: default\nembedding_provider: mock\nembedding_model: test-model\n---\n"
	if err := os.WriteFile(filepath.Join(marmotDir, "_config.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func saveWarrenProject(t *testing.T, root, warrenID, projectID, vaultID string) {
	t.Helper()
	marmotDir := filepath.Join(root, "projects", projectID, ".marmot")
	if err := warrenpkg.SaveProjectMetadata(marmotDir, &warrenpkg.ProjectMetadata{
		ProjectID: projectID,
		WarrenID:  warrenID,
		VaultID:   vaultID,
	}, ""); err != nil {
		t.Fatalf("SaveProjectMetadata %s: %v", projectID, err)
	}
	writeTestConfig(t, marmotDir, vaultID)
}
