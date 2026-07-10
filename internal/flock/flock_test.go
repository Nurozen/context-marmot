package flock

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWithLockRunsFn(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.md.lock")
	ran := false
	if err := WithLock(lockPath, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !ran {
		t.Fatal("fn did not run")
	}
	// The lock file is left in place and a second acquisition succeeds.
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file missing after release: %v", err)
	}
	if err := WithLock(lockPath, func() error { return nil }); err != nil {
		t.Fatalf("WithLock (second): %v", err)
	}
}

func TestWithLockCreatesParentDir(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "missing", ".marmot", "_warren.md.lock")
	if err := WithLock(lockPath, func() error { return nil }); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}

func TestWithLockPropagatesFnError(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "x.lock")
	wantErr := fmt.Errorf("boom")
	if err := WithLock(lockPath, func() error { return wantErr }); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("WithLock err = %v, want boom", err)
	}
}

// TestWithLockMutualExclusionInProcess: each WithLock opens its own fd, so
// BSD flock (per open-file-description) serializes goroutines within one
// process too.
func TestWithLockMutualExclusionInProcess(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "counter.lock")
	const n = 16
	inSection := 0
	var mu sync.Mutex // protects only the assertion bookkeeping
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			err := WithLock(lockPath, func() error {
				mu.Lock()
				inSection++
				if inSection != 1 {
					t.Errorf("critical section held by %d goroutines", inSection)
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				inSection--
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("WithLock: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestWithLockBlocksAcrossProcessesAndSurvivesSIGKILL spawns this test binary
// as a helper that takes the lock and parks. WithLock in the parent must
// block while the holder is alive, and complete promptly once the holder is
// SIGKILLed (kernel releases flock on process death — no stale-lock GC).
func TestWithLockBlocksAcrossProcessesAndSurvivesSIGKILL(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "shared.lock")

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperFlockHolder$", "-test.v")
	cmd.Env = append(os.Environ(),
		"MARMOT_FLOCK_HELPER=1",
		"MARMOT_FLOCK_TEST_PATH="+lockPath,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Wait for the helper to report it holds the lock.
	acquired := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "HELPER_ACQUIRED") {
				acquired <- nil
				return
			}
		}
		acquired <- fmt.Errorf("helper exited without acquiring: %v", sc.Err())
	}()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for helper to acquire lock")
	}

	// WithLock must block while the helper holds the flock.
	done := make(chan error, 1)
	go func() {
		done <- WithLock(lockPath, func() error { return nil })
	}()
	select {
	case err := <-done:
		t.Fatalf("WithLock completed while helper held the lock: %v", err)
	case <-time.After(500 * time.Millisecond):
		// Still blocked — expected.
	}

	// SIGKILL the helper: no cleanup code runs, the kernel frees the lock.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WithLock after helper killed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WithLock still blocked after helper was SIGKILLed")
	}
}

// TestHelperFlockHolder is not a real test: it is the subprocess body for the
// cross-process test above. It takes the lock, announces it on stdout, and
// parks until the parent kills it.
func TestHelperFlockHolder(t *testing.T) {
	if os.Getenv("MARMOT_FLOCK_HELPER") != "1" {
		t.Skip("helper process only")
	}
	lockPath := os.Getenv("MARMOT_FLOCK_TEST_PATH")
	err := WithLock(lockPath, func() error {
		fmt.Println("HELPER_ACQUIRED")
		time.Sleep(time.Minute) // parked; parent SIGKILLs us
		return nil
	})
	if err != nil {
		fmt.Printf("HELPER_ERROR: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
