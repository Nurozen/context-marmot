package warren

// Cache edit-worktree helpers (plan §5.4/§15.3): editable den links into a
// CACHE-BACKED warren (one added via `warren add`, registry + bare mirror)
// are served from a DEDICATED git worktree of the warren repo at
//
//	$MARMOT_HOME/warren-cache/edits/<warren-id>/<den-id>/
//
// permanently checked out on branch marmot/edit/<den-id>/<warren-id>, so
// agent writes never touch a user checkout or the shared read checkout (the
// §17 quarantine property). Granularity is deliberately per (warren, den) —
// NOT per project as the plan's branch sketch suggested — because a worktree
// checks out the whole warren repo: every edit link one den holds into one
// warren shares a single worktree/branch, which is also the right review
// granularity (one PR per den per warren).
//
// This file is the single, deliberate exception to the "internal/warren is
// exec-free" posture: the MCP/API editable write path (WriteEditableNode)
// auto-commits each node write when — and only when — the mount lives inside
// an edit worktree, and that commit must happen at write time to keep the
// worktree clean (contribute's dirty preflight treats uncommitted state as a
// previous failed run). All git execution goes through internal/gitx under
// the same per-warren cache lock every other cache mutation uses. Legacy
// registered-checkout mounts never reach any code in this file.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nurozen/context-marmot/internal/gitx"
	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/node"
)

// CacheEditsDir returns the root of all cache edit worktrees,
// $MARMOT_HOME/warren-cache/edits. Derived, never persisted (the warrenreg
// posture: the cache root can move without a migration).
func CacheEditsDir() string {
	return filepath.Join(home.WarrenCacheDir(), "edits")
}

// CacheEditWorktreePath returns the dedicated edit worktree for one
// (warren, den) pair: $MARMOT_HOME/warren-cache/edits/<warren-id>/<den-id>.
func CacheEditWorktreePath(warrenID, denID string) string {
	return filepath.Join(CacheEditsDir(), warrenID, denID)
}

// CacheEditBranch returns the branch an edit worktree is permanently checked
// out on: marmot/edit/<den-id>/<warren-id>. Both components must be
// git-ref-safe; callers validate before any git operation.
func CacheEditBranch(denID, warrenID string) string {
	return "marmot/edit/" + denID + "/" + warrenID
}

// SplitCacheEditPath reports whether path lies inside a cache edit worktree
// and, when it does, returns the worktree root plus the (warren, den) ids
// encoded in its first two path segments under CacheEditsDir. It is pure
// path arithmetic — no filesystem access — so it also classifies paths whose
// worktree has since been removed.
func SplitCacheEditPath(path string) (worktreeRoot, warrenID, denID string, ok bool) {
	if path == "" {
		return "", "", "", false
	}
	rel, err := filepath.Rel(CacheEditsDir(), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return filepath.Join(CacheEditsDir(), parts[0], parts[1]), parts[0], parts[1], true
}

// IsCacheEditPath reports whether path lies inside a cache edit worktree.
func IsCacheEditPath(path string) bool {
	_, _, _, ok := SplitCacheEditPath(path)
	return ok
}

// autoCommitEditWrite commits one just-written node file when the mount is a
// cache edit worktree (OQ7: auto-commit per MCP write). The commit is
// pathspec-limited to the node file alone — never `.marmot-data` sidecars —
// and runs under the per-warren cache lock. Every failure degrades to a
// warning string (the node file IS durably written; the next den contribute
// sweeps uncommitted engine state), and a write that changed nothing on disk
// commits nothing. Legacy checkout mounts return "" without any git work.
func autoCommitEditWrite(mount ProjectStatus, n *node.Node, verb string) (warning string) {
	root, warrenID, _, ok := SplitCacheEditPath(mount.Path)
	if !ok {
		return ""
	}
	nodePath, err := node.NewStore(mount.Path).SafeNodePath(n.ID)
	if err != nil {
		return fmt.Sprintf("edit worktree auto-commit skipped for node %s: %v", n.ID, err)
	}
	rel, err := filepath.Rel(root, nodePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Sprintf("edit worktree auto-commit skipped: node file %s is outside worktree %s", nodePath, root)
	}
	vaultID := mount.VaultID
	if vaultID == "" {
		vaultID = mount.ProjectID
	}
	msg := fmt.Sprintf("marmot edit: %s/%s (%s)", vaultID, n.ID, verb)
	ctx := context.Background()
	client := gitx.New()
	commitErr := gitx.WithCacheLock(home.WarrenCacheDir(), warrenID, func() error {
		dirty, _, statusErr := client.IsDirty(ctx, root, rel)
		if statusErr != nil {
			return statusErr
		}
		if !dirty {
			return nil // idempotent re-write: nothing changed, nothing to commit
		}
		if addErr := client.Add(ctx, root, rel); addErr != nil {
			return addErr
		}
		return client.Commit(ctx, root, msg, rel)
	})
	if commitErr != nil {
		return fmt.Sprintf("edit worktree auto-commit failed: %v (the node file is saved; the next den contribute will commit it)", commitErr)
	}
	return ""
}
