//go:build unix

package flock

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockBlocking takes an exclusive, BLOCKING BSD flock on f. The lock travels
// with the open file description and is released by the kernel when the fd
// closes — including on SIGKILL.
func lockBlocking(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}
