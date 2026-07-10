package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	warrenpkg "github.com/nurozen/context-marmot/internal/warren"
)

// TestWarrenRuntimeBridgesWarnsOnUnreadableManifest (A6 #5): an unreadable
// warren manifest used to silently remove cross-vault bridge policy
// enforcement; the fail-open control flow stays, but the dropped enforcement
// must be announced on stderr.
func TestWarrenRuntimeBridgesWarnsOnUnreadableManifest(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenRoot := testWarrenRoot(t, "wp", "project-a")
	if _, err := warrenpkg.RegisterWorkspaceWarren(workspace, "wp", warrenRoot); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := warrenpkg.Mount(workspace, "wp", []string{"project-a"}, false); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	mounts, err := warrenpkg.ActiveMounts(marmotDir)
	if err != nil {
		t.Fatalf("ActiveMounts: %v", err)
	}
	// Truncate the warren manifest after the mounts were resolved.
	if err := os.WriteFile(filepath.Join(warrenRoot, "_warren.md"), []byte("---\nwarren_id: wp"), 0o644); err != nil {
		t.Fatalf("corrupt manifest: %v", err)
	}

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	var bridges []any
	var declared bool
	func() {
		defer func() { os.Stderr = old }()
		b, d := warrenRuntimeBridges(marmotDir, mounts)
		for _, bridge := range b {
			bridges = append(bridges, bridge)
		}
		declared = d
		_ = w.Close()
	}()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}

	// Fail-open control flow unchanged...
	if len(bridges) != 0 || declared {
		t.Fatalf("bridges = %v declared = %v, want fail-open empty", bridges, declared)
	}
	// ...but no longer silent.
	if !strings.Contains(string(out), "bridge manifest unreadable") || !strings.Contains(string(out), "NOT enforced") {
		t.Fatalf("expected bridge-policy warning on stderr, got %q", out)
	}
}
