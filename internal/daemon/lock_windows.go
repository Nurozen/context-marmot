//go:build windows

package daemon

import "os"

// tryFlock is unsupported on Windows: flock(2) does not exist there and this
// repo's CI does not exercise AF_UNIX on Windows. `marmot serve` takes the
// standalone path on GOOS == "windows", so this stub is never reached in
// practice; it exists so the package compiles everywhere. A later Windows
// daemon can use LockFileEx behind the same API without touching callers.
func tryFlock(_ *os.File) error {
	return ErrUnsupported
}
