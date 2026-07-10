// Package flock provides a blocking, cross-process exclusive file lock for
// guarding read-modify-write cycles on shared state files (_warren.md,
// routes.yml). The lock is a kernel BSD flock(2) tied to an open file
// description: it is released the instant the holding process exits —
// including on SIGKILL — so there is no stale-lock cleanup and no PID
// liveness probing.
//
// Unlike internal/daemon's TryAcquire (a non-blocking single-owner election
// on a fixed daemon.lock), WithLock BLOCKS until the lock is free: RMW
// writers should wait for each other, not fail. Critical sections are
// expected to be short (milliseconds), so no timeout machinery is provided.
//
// On Windows this degrades to running fn unlocked: individual writes stay
// atomic via tmp+rename, so Windows keeps last-writer-wins semantics instead
// of gaining a hard failure. On NFS and other network filesystems BSD flock
// semantics vary by server; the same exposure already exists for the daemon
// lock and is accepted.
package flock

import (
	"fmt"
	"os"
	"path/filepath"
)

// WithLock opens (creating if needed, 0o600) lockPath, takes an exclusive
// blocking flock on it, runs fn, and releases the lock by closing the fd.
// The lock file is left in place; its parent directory is created if absent.
func WithLock(lockPath string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("create lock dir for %s: %w", lockPath, err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()
	if err := lockBlocking(f); err != nil {
		return fmt.Errorf("flock %s: %w", lockPath, err)
	}
	return fn()
}
