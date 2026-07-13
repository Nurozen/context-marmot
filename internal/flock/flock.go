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
	"sync"
)

// WithLock opens (creating if needed, 0o600) lockPath, takes an exclusive
// blocking flock on it, runs fn, and releases the lock by closing the fd.
// The lock file is left in place; its parent directory is created if absent.
func WithLock(lockPath string, fn func() error) error {
	f, err := openLockFile(lockPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := lockBlocking(f); err != nil {
		return fmt.Errorf("flock %s: %w", lockPath, err)
	}
	return fn()
}

// Shared opens (creating if needed) lockPath and takes a shared, BLOCKING
// flock on it (LOCK_SH). Any number of processes may hold the shared lock
// simultaneously; it excludes TryExclusive holders. The returned release
// func drops the lock by closing the fd (idempotent; kernel-released on
// process exit regardless). Used by cross-vault readers (VaultRegistry) to
// advertise an open embeddings DB so `index --force` refuses to delete it
// out from under them. On Windows this degrades to a no-op lock.
func Shared(lockPath string) (release func(), err error) {
	f, err := openLockFile(lockPath)
	if err != nil {
		return nil, err
	}
	if err := lockShared(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock (shared) %s: %w", lockPath, err)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = f.Close() }) }, nil
}

// TryShared attempts a NON-BLOCKING shared flock (LOCK_SH|LOCK_NB) on
// lockPath. ok is false when another process holds the lock exclusively
// (an `index --force` mid-rebuild); release is non-nil only when ok. Used
// where the caller must never block — VaultRegistry.ResolveEmbeddingStore
// runs under the registry's own mutex, and a blocking Shared there would
// wedge every registry operation for the duration of a foreign reindex. On
// Windows this degrades to a no-op lock that always succeeds.
func TryShared(lockPath string) (release func(), ok bool, err error) {
	f, err := openLockFile(lockPath)
	if err != nil {
		return nil, false, err
	}
	ok, err = tryLockShared(f)
	if err != nil {
		_ = f.Close()
		return nil, false, fmt.Errorf("flock (shared, non-blocking) %s: %w", lockPath, err)
	}
	if !ok {
		_ = f.Close()
		return nil, false, nil
	}
	var once sync.Once
	return func() { once.Do(func() { _ = f.Close() }) }, true, nil
}

// TryExclusive attempts a NON-BLOCKING exclusive flock (LOCK_EX|LOCK_NB) on
// lockPath. ok is false when any other process holds the lock (shared or
// exclusive); release is non-nil only when ok. On Windows this degrades to
// always succeeding (today's unguarded semantics).
func TryExclusive(lockPath string) (release func(), ok bool, err error) {
	f, err := openLockFile(lockPath)
	if err != nil {
		return nil, false, err
	}
	ok, err = tryLockExclusive(f)
	if err != nil {
		_ = f.Close()
		return nil, false, fmt.Errorf("flock (exclusive, non-blocking) %s: %w", lockPath, err)
	}
	if !ok {
		_ = f.Close()
		return nil, false, nil
	}
	var once sync.Once
	return func() { once.Do(func() { _ = f.Close() }) }, true, nil
}

// openLockFile creates the lock file's parent directory if absent and opens
// (creating if needed, 0o600) the lock file itself.
func openLockFile(lockPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir for %s: %w", lockPath, err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	return f, nil
}
