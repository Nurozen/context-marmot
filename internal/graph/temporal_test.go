package graph

import (
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
)

// ---------------------------------------------------------------------------
// AllActiveNodes filtering
// ---------------------------------------------------------------------------

func TestAllActiveNodes_ExcludesSuperseded(t *testing.T) {
	g := NewGraph()

	if err := g.AddNode(makeNode("a")); err != nil {
		t.Fatalf("AddNode a: %v", err)
	}
	if err := g.AddNode(makeNode("b")); err != nil {
		t.Fatalf("AddNode b: %v", err)
	}

	c := makeNode("c")
	c.Status = node.StatusSuperseded
	if err := g.AddNode(c); err != nil {
		t.Fatalf("AddNode c: %v", err)
	}

	allNodes := g.AllNodes()
	if len(allNodes) != 3 {
		t.Errorf("AllNodes() = %d, want 3", len(allNodes))
	}

	activeNodes := g.AllActiveNodes()
	if len(activeNodes) != 2 {
		t.Errorf("AllActiveNodes() = %d, want 2", len(activeNodes))
	}

	for _, n := range activeNodes {
		if n.ID == "c" {
			t.Error("AllActiveNodes() contains superseded node c")
		}
	}
}

// ---------------------------------------------------------------------------
// AddNode with superseded status never enters active index
// ---------------------------------------------------------------------------

func TestAddNode_SupersededNotInActiveIndex(t *testing.T) {
	g := NewGraph()

	n := makeNode("x")
	n.Status = node.StatusSuperseded
	if err := g.AddNode(n); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	if got := len(g.AllActiveNodes()); got != 0 {
		t.Errorf("AllActiveNodes() = %d, want 0", got)
	}
	if got := len(g.AllNodes()); got != 1 {
		t.Errorf("AllNodes() = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// UpsertNode status transition active -> superseded -> active
// ---------------------------------------------------------------------------

func TestUpsertNode_StatusTransition(t *testing.T) {
	g := NewGraph()

	// Add as active.
	if err := g.AddNode(makeNode("a")); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if got := len(g.AllActiveNodes()); got != 1 {
		t.Fatalf("step 1: AllActiveNodes() = %d, want 1", got)
	}

	// Upsert with superseded status.
	superseded := makeNode("a")
	superseded.Status = node.StatusSuperseded
	if err := g.UpsertNode(superseded); err != nil {
		t.Fatalf("UpsertNode (supersede): %v", err)
	}
	if got := len(g.AllActiveNodes()); got != 0 {
		t.Errorf("step 2: AllActiveNodes() = %d, want 0", got)
	}
	if got := len(g.AllNodes()); got != 1 {
		t.Errorf("step 2: AllNodes() = %d, want 1", got)
	}

	// Upsert back to active.
	reactivated := makeNode("a")
	reactivated.Status = node.StatusActive
	if err := g.UpsertNode(reactivated); err != nil {
		t.Fatalf("UpsertNode (reactivate): %v", err)
	}
	if got := len(g.AllActiveNodes()); got != 1 {
		t.Errorf("step 3: AllActiveNodes() = %d, want 1", got)
	}
	active := g.AllActiveNodes()
	if active[0].ID != "a" {
		t.Errorf("step 3: active node ID = %q, want %q", active[0].ID, "a")
	}
}

// ---------------------------------------------------------------------------
// SupersedeNode basic behaviour
// ---------------------------------------------------------------------------

func TestSupersedeNode_Basic(t *testing.T) {
	g := NewGraph()

	if err := g.AddNode(makeNode("old")); err != nil {
		t.Fatalf("AddNode old: %v", err)
	}

	newNode := makeNode("new")
	if err := g.SupersedeNode("old", newNode); err != nil {
		t.Fatalf("SupersedeNode: %v", err)
	}

	// "old" should still be in AllNodes() with superseded status and SupersededBy set.
	oldNode, ok := g.GetNode("old")
	if !ok {
		t.Fatal("old node not found in AllNodes() after SupersedeNode")
	}
	if oldNode.Status != node.StatusSuperseded {
		t.Errorf("old.Status = %q, want %q", oldNode.Status, node.StatusSuperseded)
	}
	if oldNode.SupersededBy != "new" {
		t.Errorf("old.SupersededBy = %q, want %q", oldNode.SupersededBy, "new")
	}

	// "new" should be in AllActiveNodes().
	found := false
	for _, n := range g.AllActiveNodes() {
		if n.ID == "new" {
			found = true
		}
	}
	if !found {
		t.Error("new node not found in AllActiveNodes() after SupersedeNode")
	}

	// "old" must NOT be in AllActiveNodes().
	for _, n := range g.AllActiveNodes() {
		if n.ID == "old" {
			t.Error("old node still present in AllActiveNodes() after SupersedeNode")
		}
	}
}

// ---------------------------------------------------------------------------
// SupersedeNode with unknown old ID returns error
// ---------------------------------------------------------------------------

func TestSupersedeNode_OldNotFound(t *testing.T) {
	g := NewGraph()

	dummy := makeNode("replacement")
	err := g.SupersedeNode("nonexistent", dummy)
	if err == nil {
		t.Fatal("expected error from SupersedeNode with nonexistent oldID, got nil")
	}
}

// ---------------------------------------------------------------------------
// RemoveNode clears both nodes map and active index
// ---------------------------------------------------------------------------

func TestRemoveNode_ClearsActiveIndex(t *testing.T) {
	g := NewGraph()

	if err := g.AddNode(makeNode("a")); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	if err := g.RemoveNode("a"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}

	if got := len(g.AllActiveNodes()); got != 0 {
		t.Errorf("AllActiveNodes() = %d, want 0 after RemoveNode", got)
	}
	if got := len(g.AllNodes()); got != 0 {
		t.Errorf("AllNodes() = %d, want 0 after RemoveNode", got)
	}
}
