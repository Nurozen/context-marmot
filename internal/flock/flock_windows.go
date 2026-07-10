//go:build !unix

package flock

import "os"

// lockBlocking degrades to a no-op on platforms without BSD flock: fn runs
// unlocked. Individual writes stay atomic via tmp+rename, so these platforms
// keep today's last-writer-wins semantics instead of gaining a hard failure.
func lockBlocking(_ *os.File) error {
	return nil
}
