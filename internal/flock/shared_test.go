package flock

import (
	"path/filepath"
	"testing"
	"time"
)

// TestSharedAllowsConcurrentReaders: any number of shared holders coexist.
func TestSharedAllowsConcurrentReaders(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "vault.read.lock")
	rel1, err := Shared(lockPath)
	if err != nil {
		t.Fatalf("Shared #1: %v", err)
	}
	defer rel1()
	rel2, err := Shared(lockPath)
	if err != nil {
		t.Fatalf("Shared #2 (concurrent): %v", err)
	}
	rel2()
}

// TestTryExclusiveBlockedByShared: the shared/exclusive matrix that guards
// `index --force` against live cross-vault readers. Each call opens its own
// fd, so BSD flock semantics apply within one process too.
func TestTryExclusiveBlockedByShared(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "vault.read.lock")

	release, err := Shared(lockPath)
	if err != nil {
		t.Fatalf("Shared: %v", err)
	}

	if _, ok, err := TryExclusive(lockPath); err != nil {
		t.Fatalf("TryExclusive under shared: %v", err)
	} else if ok {
		t.Fatal("TryExclusive must fail while a shared reader holds the lock")
	}

	// After the reader releases, the exclusive lock succeeds…
	release()
	exRelease, ok, err := TryExclusive(lockPath)
	if err != nil {
		t.Fatalf("TryExclusive after release: %v", err)
	}
	if !ok {
		t.Fatal("TryExclusive must succeed once the shared reader released")
	}

	// …and a second exclusive attempt fails while the first holds it.
	if _, ok, err := TryExclusive(lockPath); err != nil {
		t.Fatalf("TryExclusive under exclusive: %v", err)
	} else if ok {
		t.Fatal("TryExclusive must fail while another exclusive holder exists")
	}

	// Release is idempotent.
	exRelease()
	exRelease()
	if _, ok, err := TryExclusive(lockPath); err != nil || !ok {
		t.Fatalf("TryExclusive after exclusive release: ok=%v err=%v", ok, err)
	}
}

// TestSharedBlockedByExclusive: a reader attempting to attach while the
// exclusive (index --force) holder works waits instead of failing; it
// acquires as soon as the writer releases.
func TestSharedBlockedByExclusive(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "vault.read.lock")
	exRelease, ok, err := TryExclusive(lockPath)
	if err != nil || !ok {
		t.Fatalf("TryExclusive: ok=%v err=%v", ok, err)
	}

	acquired := make(chan struct{})
	go func() {
		rel, err := Shared(lockPath)
		if err == nil {
			rel()
		}
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("Shared acquired while an exclusive holder existed")
	case <-time.After(100 * time.Millisecond):
	}

	exRelease()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("Shared never acquired after the exclusive holder released")
	}
}
