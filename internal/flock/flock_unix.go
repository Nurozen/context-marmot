//go:build unix

package flock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockBlocking takes an exclusive, BLOCKING BSD flock on f. The lock travels
// with the open file description and is released by the kernel when the fd
// closes — including on SIGKILL.
func lockBlocking(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

// lockShared takes a shared, BLOCKING BSD flock on f (many readers, no
// exclusive holder).
func lockShared(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_SH)
}

// tryLockShared attempts a non-blocking shared BSD flock on f.
// Returns (false, nil) when another process holds the lock exclusively.
func tryLockShared(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}

// tryLockExclusive attempts a non-blocking exclusive BSD flock on f.
// Returns (false, nil) when another process holds the lock.
func tryLockExclusive(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}
