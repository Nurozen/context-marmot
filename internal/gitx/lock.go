package gitx

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nurozen/context-marmot/internal/flock"
)

// WithCacheLock serializes mutating operations against one warren's bare
// mirror. Concurrent `worktree add`/`fetch` against a single bare repo
// corrupts .git/worktrees metadata, so every cache mutation for warrenID
// must run inside this guard. The lock is a blocking, cross-process flock on
// <cacheRoot>/<warrenID>.git.lock (kernel-released on process exit; no stale
// lock cleanup needed). Critical sections are expected to be short; there is
// no timeout.
func WithCacheLock(cacheRoot, warrenID string, fn func() error) error {
	if warrenID == "" || warrenID != filepath.Base(warrenID) || strings.HasPrefix(warrenID, ".") {
		return fmt.Errorf("invalid warren id for cache lock: %q", warrenID)
	}
	return flock.WithLock(filepath.Join(cacheRoot, warrenID+".git.lock"), fn)
}
