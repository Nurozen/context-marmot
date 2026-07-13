//go:build unix

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryFlock takes an exclusive, non-blocking BSD flock on f. It returns
// ErrHeld if another process (or another open file description) already
// holds the lock. The lock travels with the fd and is released by the
// kernel when the fd closes — including on SIGKILL.
func tryFlock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrHeld
	}
	return err
}
