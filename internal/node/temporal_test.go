package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// IsActive tests
// ---------------------------------------------------------------------------

func TestIsActive(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"", true},
		{StatusActive, true},
		{StatusSuperseded, false},
		{StatusArchived, false},
	}

	for _, tt := range tests {
		n := &Node{Status: tt.status}
		got := n.IsActive()
		if got != tt.want {
			t.Errorf("IsActive() with status=%q: got %v, want %v", tt.status, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Temporal fields parsing
// ---------------------------------------------------------------------------

const temporalMarkdown = `---
id: auth/login
type: function
namespace: test
status: superseded
valid_from: "2026-01-01T00:00:00Z"
valid_until: "2026-04-01T00:00:00Z"
superseded_by: auth/login_v2
---

Login handler that has been superseded.
`

func TestTemporalFieldsParsing(t *testing.T) {
	n, err := ParseNode([]byte(temporalMarkdown), "temporal.md")
	if err != nil {
		t.Fatalf("ParseNode: %v", err)
	}

	if n.Status != StatusSuperseded {
		t.Errorf("Status = %q, want %q", n.Status, StatusSuperseded)
	}
	if n.ValidFrom != "2026-01-01T00:00:00Z" {
		t.Errorf("ValidFrom = %q, want %q", n.ValidFrom, "2026-01-01T00:00:00Z")
	}
	if n.ValidUntil != "2026-04-01T00:00:00Z" {
		t.Errorf("ValidUntil = %q, want %q", n.ValidUntil, "2026-04-01T00:00:00Z")
	}
	if n.SupersededBy != "auth/login_v2" {
		t.Errorf("SupersededBy = %q, want %q", n.SupersededBy, "auth/login_v2")
	}
}

// ---------------------------------------------------------------------------
// Temporal fields roundtrip
// ---------------------------------------------------------------------------

func TestTemporalFieldsRoundtrip(t *testing.T) {
	original := &Node{
		ID:           "auth/login",
		Type:         "function",
		Namespace:    "test",
		Status:       StatusSuperseded,
		ValidFrom:    "2026-01-01T00:00:00Z",
		ValidUntil:   "2026-04-01T00:00:00Z",
		SupersededBy: "auth/login_v2",
		Summary:      "A superseded login handler.",
	}

	data, err := RenderNode(original)
	if err != nil {
		t.Fatalf("RenderNode: %v", err)
	}

	parsed, err := ParseNode(data, "roundtrip.md")
	if err != nil {
		t.Fatalf("ParseNode after render: %v", err)
	}

	if parsed.Status != original.Status {
		t.Errorf("Status: got %q, want %q", parsed.Status, original.Status)
	}
	if parsed.ValidFrom != original.ValidFrom {
		t.Errorf("ValidFrom: got %q, want %q", parsed.ValidFrom, original.ValidFrom)
	}
	if parsed.ValidUntil != original.ValidUntil {
		t.Errorf("ValidUntil: got %q, want %q", parsed.ValidUntil, original.ValidUntil)
	}
	if parsed.SupersededBy != original.SupersededBy {
		t.Errorf("SupersededBy: got %q, want %q", parsed.SupersededBy, original.SupersededBy)
	}
}

// ---------------------------------------------------------------------------
// SoftDeleteNode with replacement
// ---------------------------------------------------------------------------

func TestSoftDeleteNode(t *testing.T) {
	dir, err := os.MkdirTemp("", "marmot-soft-delete-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	store := NewStore(dir)

	active := &Node{
		ID:        "auth/login",
		Type:      "function",
		Namespace: "test",
		Status:    StatusActive,
		ValidFrom: "2025-01-01T00:00:00Z",
		Summary:   "Active login handler.",
	}
	if err := store.SaveNode(active); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}

	if err := store.SoftDeleteNode("auth/login", "auth/new"); err != nil {
		t.Fatalf("SoftDeleteNode: %v", err)
	}

	reloaded, err := store.LoadNode(filepath.Join(dir, "auth/login.md"))
	if err != nil {
		t.Fatalf("LoadNode: %v", err)
	}

	if reloaded.Status != StatusSuperseded {
		t.Errorf("Status = %q, want %q", reloaded.Status, StatusSuperseded)
	}
	if reloaded.ValidUntil == "" {
		t.Error("ValidUntil should be set after soft-delete")
	} else {
		if _, parseErr := time.Parse(time.RFC3339, reloaded.ValidUntil); parseErr != nil {
			t.Errorf("ValidUntil %q is not valid RFC3339: %v", reloaded.ValidUntil, parseErr)
		}
	}
	if reloaded.SupersededBy != "auth/new" {
		t.Errorf("SupersededBy = %q, want %q", reloaded.SupersededBy, "auth/new")
	}
	// ValidFrom should be preserved.
	if reloaded.ValidFrom != active.ValidFrom {
		t.Errorf("ValidFrom = %q, want %q (original preserved)", reloaded.ValidFrom, active.ValidFrom)
	}
}

// ---------------------------------------------------------------------------
// SoftDeleteNode without replacement
// ---------------------------------------------------------------------------

func TestSoftDeleteNode_NoReplacement(t *testing.T) {
	dir, err := os.MkdirTemp("", "marmot-soft-delete-norep-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	store := NewStore(dir)

	active := &Node{
		ID:        "auth/login",
		Type:      "function",
		Namespace: "test",
		Status:    StatusActive,
		Summary:   "Active login handler.",
	}
	if err := store.SaveNode(active); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}

	if err := store.SoftDeleteNode("auth/login", ""); err != nil {
		t.Fatalf("SoftDeleteNode: %v", err)
	}

	reloaded, err := store.LoadNode(filepath.Join(dir, "auth/login.md"))
	if err != nil {
		t.Fatalf("LoadNode: %v", err)
	}

	if reloaded.Status != StatusSuperseded {
		t.Errorf("Status = %q, want %q", reloaded.Status, StatusSuperseded)
	}
	if reloaded.ValidUntil == "" {
		t.Error("ValidUntil should be set after soft-delete")
	} else {
		if _, parseErr := time.Parse(time.RFC3339, reloaded.ValidUntil); parseErr != nil {
			t.Errorf("ValidUntil %q is not valid RFC3339: %v", reloaded.ValidUntil, parseErr)
		}
	}
	if reloaded.SupersededBy != "" {
		t.Errorf("SupersededBy = %q, want empty (no replacement given)", reloaded.SupersededBy)
	}
}

// ---------------------------------------------------------------------------
// ListActiveNodes
// ---------------------------------------------------------------------------

func TestListActiveNodes(t *testing.T) {
	dir, err := os.MkdirTemp("", "marmot-list-active-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	store := NewStore(dir)

	nodes := []*Node{
		{ID: "auth/login", Type: "function", Namespace: "test", Status: StatusActive, Summary: "Active login."},
		{ID: "auth/logout", Type: "function", Namespace: "test", Status: StatusActive, Summary: "Active logout."},
		{ID: "auth/legacy", Type: "function", Namespace: "test", Status: StatusSuperseded, Summary: "Superseded legacy."},
	}
	for _, n := range nodes {
		if err := store.SaveNode(n); err != nil {
			t.Fatalf("SaveNode(%s): %v", n.ID, err)
		}
	}

	// ListActiveNodes should return only the 2 active nodes.
	active, err := store.ListActiveNodes()
	if err != nil {
		t.Fatalf("ListActiveNodes: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("ListActiveNodes returned %d, want 2", len(active))
	}
	for _, m := range active {
		if strings.Contains(m.ID, "legacy") {
			t.Errorf("superseded node %q should not appear in ListActiveNodes", m.ID)
		}
		if m.Status != StatusActive {
			t.Errorf("ListActiveNodes returned node with status %q, want %q", m.Status, StatusActive)
		}
	}

	// ListNodes should return all 3.
	all, err := store.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListNodes returned %d, want 3", len(all))
	}
}
