package main

import (
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/routes"
)

// TestIndexForceRefusedWhileVaultMountedElsewhere (B4): a vault whose
// embeddings DB is held open read-only by another marmot process's
// VaultRegistry (warren mount) must refuse `index --force` — deleting the DB
// under the reader's open WAL connection would leave it on an unlinked file.
// BSD flock is per open-file-description, so an in-process registry stands
// in for the second process.
func TestIndexForceRefusedWhileVaultMountedElsewhere(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")
	if err := runIndexPipeline(vault, false); err != nil {
		t.Fatalf("seed index: %v", err)
	}

	// "Another workspace" resolves this vault's store through a registry,
	// taking the shared vault.read.lock.
	rt := routes.EmptyTable()
	rt.Set("shared-vault", vault)
	registry := namespace.NewVaultRegistry("other-local", t.TempDir(), nil, rt)
	if _, err := registry.ResolveEmbeddingStore("shared-vault"); err != nil {
		t.Fatalf("ResolveEmbeddingStore: %v", err)
	}

	err := runIndexPipeline(vault, true)
	if err == nil {
		t.Fatal("index --force must refuse while a cross-vault reader holds the DB open")
	}
	if !strings.Contains(err.Error(), "open read-only by another marmot process") {
		t.Fatalf("unexpected refusal error: %v", err)
	}

	// Once the reader closes, --force proceeds.
	registry.Close()
	if err := runIndexPipeline(vault, true); err != nil {
		t.Fatalf("index --force after reader closed: %v", err)
	}
}
