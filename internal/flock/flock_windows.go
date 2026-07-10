//go:build !unix

package flock

import "os"

// lockBlocking degrades to a no-op on platforms without BSD flock: fn runs
// unlocked. Individual writes stay atomic via tmp+rename, so these platforms
// keep today's last-writer-wins semantics instead of gaining a hard failure.
func lockBlocking(_ *os.File) error {
	return nil
}

// lockShared degrades to a no-op on platforms without BSD flock.
func lockShared(_ *os.File) error {
	return nil
}

// tryLockExclusive always succeeds on platforms without BSD flock, keeping
// today's unguarded `index --force` semantics there (documented gap).
func tryLockExclusive(_ *os.File) (bool, error) {
	return true, nil
}
