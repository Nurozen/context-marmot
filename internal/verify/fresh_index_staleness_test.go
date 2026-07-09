package verify_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nurozen/context-marmot/internal/indexer"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/verify"
)

// Regression test for manual-test issue 4: immediately after a fresh static
// index, verify/status reported hash_mismatch ("Stale: 3") on TypeScript
// module nodes and the go.mod node. The indexers hashed the whole file
// ([0,0]) but stored Lines [1, N]; verify recomputed the hash with the stored
// line range, and the two paths disagreed on trailing-newline bytes.
//
// This test indexes a small fixture with every default indexer (Go,
// TypeScript, generic) and asserts that recomputing each entity's source hash
// from its stored line range — exactly what verify and the staleness check do
// — reproduces the stored hash, i.e. a fresh index is never stale.
func TestFreshIndexEntities_NotStale(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n",
		"go.mod":  "module example.com/proj\n\ngo 1.26\n",
		"web/src/cart.ts": "// Cart rendering utilities\n" +
			"import { formatPrice } from './format';\n\n" +
			"export function renderCartSummary(prices: number[]): string {\n" +
			"  return prices.map((p) => formatPrice(p)).join('\\n');\n" +
			"}\n",
		"web/src/format.ts": "export function formatPrice(p: number): string {\n" +
			"  return `$${p.toFixed(2)}`;\n" +
			"}\n",
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	registry := indexer.NewDefaultRegistry()

	entityCount := 0
	for rel := range files {
		path := filepath.Join(dir, rel)
		idx, ok := registry.IndexerFor(filepath.Ext(rel))
		if !ok {
			t.Fatalf("no indexer for %s", rel)
		}

		result, err := idx.IndexFile(path, rel, "default")
		if err != nil {
			t.Fatalf("index %s: %v", rel, err)
		}

		for _, entity := range result.Entities {
			if entity.Source.Hash == "" {
				continue
			}
			entityCount++

			// This mirrors VerifyIntegrity's hash-mismatch check and
			// VerifyStaleness: recompute from the stored path + lines.
			current, err := verify.ComputeSourceHash(entity.Source.Path, entity.Source.Lines)
			if err != nil {
				t.Fatalf("recompute hash for %s (%s): %v", entity.ID, rel, err)
			}
			if current != entity.Source.Hash {
				t.Errorf("fresh-index entity %s (%s, lines %v) is immediately stale: stored=%s recomputed=%s",
					entity.ID, rel, entity.Source.Lines, entity.Source.Hash, current)
			}

			status, err := verify.VerifyStaleness(&node.Node{
				ID: entity.ID,
				Source: node.Source{
					Path:  entity.Source.Path,
					Lines: entity.Source.Lines,
					Hash:  entity.Source.Hash,
				},
			}, "")
			if err != nil {
				t.Fatalf("VerifyStaleness for %s: %v", entity.ID, err)
			}
			if status.IsStale {
				t.Errorf("VerifyStaleness reports fresh-index entity %s as stale", entity.ID)
			}
		}
	}

	if entityCount < 4 {
		t.Fatalf("expected at least 4 hashed entities across indexers, got %d", entityCount)
	}
}
