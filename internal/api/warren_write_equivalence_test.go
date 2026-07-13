package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// TestWarrenWriteEquivalenceMCPAndAPI (C8): the HTTP API and MCP @-write
// paths share warren.WriteEditableNode, so the same payload applied to two
// identical editable mounts produces byte-identical node files in the two
// checkouts. This pins the shared-helper refactor: any drift between the
// paths shows up as a byte diff.
func TestWarrenWriteEquivalenceMCPAndAPI(t *testing.T) {
	server, engine := newTestServer(t)
	workspaceRoot := filepath.Dir(engine.MarmotDir)

	rootA := setupAPIWarren(t, workspaceRoot, "warren-a", "proj-a", "vault-a")
	rootB := setupAPIWarren(t, workspaceRoot, "warren-b", "proj-b", "vault-b")
	for _, pair := range [][2]string{{"warren-a", "proj-a"}, {"warren-b", "proj-b"}} {
		if _, err := warrenpkg.SetEditable(workspaceRoot, pair[0], pair[1], true); err != nil {
			t.Fatalf("SetEditable %s: %v", pair[1], err)
		}
	}
	wireWarrenVaultRegistry(t, engine)

	const (
		summary     = "Equivalence-checked service API summary"
		contextBody = "Full context body for the equivalence check"
	)

	// API path → warren-a's checkout.
	body := `{"summary":"` + summary + `","context":"` + contextBody + `","tags":["shared","warren"]}`
	rec := doRequest(t, server.Handler(), "PUT", "/api/node/@vault-a/service/api", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("API warren write = %d: %s", rec.Code, rec.Body.String())
	}

	// MCP path → warren-b's checkout.
	res, err := engine.HandleContextWrite(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_write",
			Arguments: map[string]any{
				"id":      "@vault-b/service/api",
				"type":    "module",
				"summary": summary,
				"context": contextBody,
				"tags":    []string{"shared", "warren"},
			},
		},
	})
	if err != nil {
		t.Fatalf("MCP warren write: %v", err)
	}
	if res.IsError {
		t.Fatalf("MCP warren write rejected: %+v", res.Content)
	}

	fileA := filepath.Join(rootA, "projects", "proj-a", ".marmot", "service", "api.md")
	fileB := filepath.Join(rootB, "projects", "proj-b", ".marmot", "service", "api.md")
	bytesA, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatalf("read API-written node: %v", err)
	}
	bytesB, err := os.ReadFile(fileB)
	if err != nil {
		t.Fatalf("read MCP-written node: %v", err)
	}
	if string(bytesA) != string(bytesB) {
		t.Fatalf("MCP and API warren writes diverged:\nAPI:\n%s\nMCP:\n%s", bytesA, bytesB)
	}
}

// TestWarrenWriteReadOnlyPolicyEquivalence (D4, extending C8's matrix): once
// the warren author marks a project readonly — even AFTER the workspace
// granted editability, so any cached mount state is stale — both the HTTP
// API and MCP write paths reject the write. Depending on where each path
// resolves its mount, the refusal comes from enforcement point 3 (statuses
// recomputed live report Editable=false → "not an editable warren mount")
// or from the shared WriteEditableNode manifest re-read backstop
// ("read-only"); either way no write lands.
func TestWarrenWriteReadOnlyPolicyEquivalence(t *testing.T) {
	server, engine := newTestServer(t)
	workspaceRoot := filepath.Dir(engine.MarmotDir)

	rootA := setupAPIWarren(t, workspaceRoot, "warren-a", "proj-a", "vault-a")
	rootB := setupAPIWarren(t, workspaceRoot, "warren-b", "proj-b", "vault-b")
	for _, pair := range [][2]string{{"warren-a", "proj-a"}, {"warren-b", "proj-b"}} {
		if _, err := warrenpkg.SetEditable(workspaceRoot, pair[0], pair[1], true); err != nil {
			t.Fatalf("SetEditable %s: %v", pair[1], err)
		}
	}
	wireWarrenVaultRegistry(t, engine)
	// Author-side policy flip lands after the editable grant.
	for _, fix := range [][2]string{{rootA, "proj-a"}, {rootB, "proj-b"}} {
		if _, err := warrenpkg.SetProjectReadOnly(fix[0], fix[1], true); err != nil {
			t.Fatalf("SetProjectReadOnly %s: %v", fix[1], err)
		}
	}

	rec := doRequest(t, server.Handler(), "PUT", "/api/node/@vault-a/service/api", `{"summary":"blocked write"}`)
	if rec.Code == http.StatusOK || !isReadOnlyRejection(rec.Body.String()) {
		t.Fatalf("API write to readonly project = %d %q, want read-only rejection", rec.Code, rec.Body.String())
	}

	res, err := engine.HandleContextWrite(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_write",
			Arguments: map[string]any{
				"id":      "@vault-b/service/api",
				"type":    "module",
				"summary": "blocked write",
			},
		},
	})
	if err != nil {
		t.Fatalf("MCP warren write: %v", err)
	}
	if !res.IsError || len(res.Content) == 0 {
		t.Fatalf("MCP write to readonly project must be rejected: %+v", res)
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok || !isReadOnlyRejection(text.Text) {
		t.Fatalf("MCP rejection = %+v, want read-only/not-editable text", res.Content[0])
	}

	// Neither surface may have modified the seeded node in either checkout.
	for _, fix := range [][2]string{{rootA, "proj-a"}, {rootB, "proj-b"}} {
		nodePath := filepath.Join(fix[0], "projects", fix[1], ".marmot", "service", "api.md")
		data, readErr := os.ReadFile(nodePath)
		if readErr != nil {
			t.Fatalf("read node in %s: %v", fix[1], readErr)
		}
		if strings.Contains(string(data), "blocked write") {
			t.Fatalf("write to readonly project %s landed anyway:\n%s", fix[1], data)
		}
	}
}

// TestWarrenAPIWriteSerialization: the HTTP API warren-node update must take
// the same per-mount NamespaceLock("@"+vaultID) as the MCP @-write path — C8
// guarantees payload equivalence, but without shared locking two concurrent
// writers' load-modify-save cycles interleave and one writer's fields are
// silently dropped. Two goroutines update disjoint fields (summary vs tags)
// of the same mounted node; serialized, every iteration ends with both
// writers' latest values in the checkout file.
func TestWarrenAPIWriteSerialization(t *testing.T) {
	server, engine := newTestServer(t)
	workspaceRoot := filepath.Dir(engine.MarmotDir)

	rootA := setupAPIWarren(t, workspaceRoot, "warren-a", "proj-a", "vault-a")
	if _, err := warrenpkg.SetEditable(workspaceRoot, "warren-a", "proj-a", true); err != nil {
		t.Fatalf("SetEditable: %v", err)
	}
	wireWarrenVaultRegistry(t, engine)

	nodePath := filepath.Join(rootA, "projects", "proj-a", ".marmot", "service", "api.md")
	handler := server.Handler()
	for i := 0; i < 20; i++ {
		summary := fmt.Sprintf("serialized summary revision %d", i)
		tag := fmt.Sprintf("serialized-tag-%d", i)
		var wg sync.WaitGroup
		var codes [2]int
		wg.Add(2)
		go func() {
			defer wg.Done()
			codes[0] = doRequest(t, handler, "PUT", "/api/node/@vault-a/service/api", `{"summary":"`+summary+`"}`).Code
		}()
		go func() {
			defer wg.Done()
			codes[1] = doRequest(t, handler, "PUT", "/api/node/@vault-a/service/api", `{"tags":["`+tag+`"]}`).Code
		}()
		wg.Wait()
		if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
			t.Fatalf("iteration %d: concurrent warren updates = %d, %d, want 200s", i, codes[0], codes[1])
		}
		data, err := os.ReadFile(nodePath)
		if err != nil {
			t.Fatalf("iteration %d: read node: %v", i, err)
		}
		if !strings.Contains(string(data), summary) || !strings.Contains(string(data), tag) {
			t.Fatalf("iteration %d: a concurrent writer's fields were dropped (want %q and %q):\n%s", i, summary, tag, data)
		}
	}
}

// isReadOnlyRejection accepts either layer's phrasing of the D4 policy
// refusal (see the test comment above).
func isReadOnlyRejection(msg string) bool {
	return strings.Contains(msg, "read-only") || strings.Contains(msg, "not an editable warren mount")
}
