package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTryAcquireSecondCallerHeld(t *testing.T) {
	dataDir := t.TempDir()

	lock, err := TryAcquire(dataDir)
	if err != nil {
		t.Fatalf("first TryAcquire: %v", err)
	}
	defer lock.Release()

	// flock is per open-file-description, so a second acquire in the same
	// process must still see ErrHeld.
	second, err := TryAcquire(dataDir)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("second TryAcquire: got (%v, %v), want ErrHeld", second, err)
	}
}

func TestReleaseThenReacquire(t *testing.T) {
	dataDir := t.TempDir()

	lock, err := TryAcquire(dataDir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Release is idempotent.
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	again, err := TryAcquire(dataDir)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	defer again.Release()
}

func TestWriteReadReleaseInfo(t *testing.T) {
	dataDir := t.TempDir()

	lock, err := TryAcquire(dataDir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}

	if _, err := ReadInfo(dataDir); err == nil {
		t.Fatal("ReadInfo before WriteInfo: want error, got nil")
	}

	want := Info{PID: os.Getpid(), Socket: "/tmp/x.sock", Version: "test", StartedAt: time.Now().UTC().Truncate(time.Second)}
	if err := lock.WriteInfo(want); err != nil {
		t.Fatalf("WriteInfo: %v", err)
	}
	got, err := ReadInfo(dataDir)
	if err != nil {
		t.Fatalf("ReadInfo: %v", err)
	}
	if got.PID != want.PID || got.Socket != want.Socket || got.Version != want.Version || !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("ReadInfo = %+v, want %+v", got, want)
	}

	// No leftover tmp files from the tmp+rename dance.
	matches, _ := filepath.Glob(filepath.Join(dataDir, infoFileName+".tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp info files: %v", matches)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, infoFileName)); !os.IsNotExist(err) {
		t.Fatalf("info file should be removed on Release, stat err = %v", err)
	}
}

func TestReadInfoMalformed(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, infoFileName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInfo(dataDir); err == nil {
		t.Fatal("ReadInfo on malformed file: want error, got nil")
	}
}

// TestFlockReleasedOnKill proves the property that justified choosing flock:
// the kernel releases the lock the instant the holder dies, even on SIGKILL,
// so there is no stale-lock GC and no PID liveness probing. It spawns this
// test binary as a helper subprocess that acquires the lock and parks.
func TestFlockReleasedOnKill(t *testing.T) {
	dataDir := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperLockHolder$", "-test.v")
	cmd.Env = append(os.Environ(),
		"MARMOT_DAEMON_LOCK_HELPER=1",
		"MARMOT_DAEMON_TEST_DATADIR="+dataDir,
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

	// While the helper is alive, the lock is held.
	if _, err := TryAcquire(dataDir); !errors.Is(err, ErrHeld) {
		t.Fatalf("TryAcquire with live helper: err = %v, want ErrHeld", err)
	}

	// SIGKILL the helper — no cleanup code runs in it.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait() // reap; expected to be an exit error from the kill

	// The flock must be free immediately after the process is gone.
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := TryAcquire(dataDir)
		if err == nil {
			defer lock.Release()
			return
		}
		if !errors.Is(err, ErrHeld) {
			t.Fatalf("TryAcquire after kill: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("lock still held after helper was SIGKILLed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHelperLockHolder is not a real test: it is the subprocess body for
// TestFlockReleasedOnKill (go test exec-helper pattern). It acquires the
// lock, announces it on stdout, and parks until the parent kills it.
func TestHelperLockHolder(t *testing.T) {
	if os.Getenv("MARMOT_DAEMON_LOCK_HELPER") != "1" {
		t.Skip("helper process only")
	}
	dataDir := os.Getenv("MARMOT_DAEMON_TEST_DATADIR")
	lock, err := TryAcquire(dataDir)
	if err != nil {
		fmt.Printf("HELPER_ERROR: %v\n", err)
		os.Exit(1)
	}
	_ = lock // held until the process dies
	fmt.Println("HELPER_ACQUIRED")
	// Park; the parent SIGKILLs us. Exit eventually as a safety net.
	time.Sleep(time.Minute)
	os.Exit(0)
}
