//go:build unix

package warren

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMaterializeSkipsFIFO: an irregular file (FIFO) in the source must be
// skipped — the old copyDir opened it and hung forever in io.Copy waiting
// for a writer that never comes.
func TestMaterializeSkipsFIFO(t *testing.T) {
	marmotDir := t.TempDir()
	warrenRoot, project, source := writeBurrowSource(t)

	fifo := filepath.Join(source, "pipe")
	if err := unix.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Materialize(marmotDir, "product-platform", project, warrenRoot, "")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Materialize: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Materialize hung on a FIFO in the source (io.Copy on irregular file)")
	}

	target := filepath.Join(marmotDir, ".marmot-data", "warrens", "product-platform", "projects", "api", ".marmot")
	if _, err := os.Lstat(filepath.Join(target, "pipe")); !os.IsNotExist(err) {
		t.Fatalf("FIFO copied into burrow cache (lstat err=%v)", err)
	}
	mustExist(t, filepath.Join(target, "service", "api.md"))
}
