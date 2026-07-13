package mcp

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/routes"
)

// TestContextQueryWarnsOncePerBrokenVault (A6 #6): a routed vault whose
// embedding store cannot be opened is excluded from context_query
// best-effort — with a stderr warning exactly once per vault per process,
// not once per query.
func TestContextQueryWarnsOncePerBrokenVault(t *testing.T) {
	eng := newClassifyTestEngine(t)

	rt := &routes.RoutingTable{Vaults: map[string]routes.VaultEntry{}}
	rt.Set("broken-vault", t.TempDir()) // no embeddings.db, no _config.md
	eng.VaultRegistry = namespace.NewVaultRegistry("local", eng.MarmotDir, nil, rt)
	t.Cleanup(eng.VaultRegistry.Close)
	eng.LocalVaultID = "local"

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	func() {
		defer func() { os.Stderr = old }()
		for i := 0; i < 3; i++ {
			req := makeCallToolRequest("context_query", map[string]any{"query": "anything"})
			if _, err := eng.HandleContextQuery(context.Background(), req); err != nil {
				t.Errorf("HandleContextQuery %d: %v", i, err)
			}
		}
		_ = w.Close()
	}()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	if got := strings.Count(string(out), `warren vault "broken-vault" embedding store unavailable`); got != 1 {
		t.Fatalf("warning fired %d times across 3 queries, want exactly 1; stderr: %q", got, out)
	}
}
