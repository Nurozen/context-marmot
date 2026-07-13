package daemon

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/summary"
)

// newTestOwner acquires the lock for a fresh dataDir and returns it with an
// Owner using the given handler. Cleanup runs Close before Release, matching
// the required teardown order.
func newTestOwner(t *testing.T, handle func(net.Conn) error) (string, *Lock, *Owner) {
	t.Helper()
	dataDir := t.TempDir()
	lock, err := TryAcquire(dataDir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	own := NewOwner(dataDir, lock, handle)
	t.Cleanup(func() {
		_ = own.Close()
		_ = lock.Release()
	})
	return dataDir, lock, own
}

func TestOwnerListenPublishesInfoAndServes(t *testing.T) {
	dataDir, _, own := newTestOwner(t, func(c net.Conn) error {
		_, err := io.Copy(c, c) // echo until the client closes
		return err
	})

	if err := own.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Info is published only after the socket listens, with the actual path.
	info, err := ReadInfo(dataDir)
	if err != nil {
		t.Fatalf("ReadInfo after Listen: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("info.PID = %d, want %d", info.PID, os.Getpid())
	}
	if info.Socket != SocketPath(dataDir) {
		t.Errorf("info.Socket = %q, want %q", info.Socket, SocketPath(dataDir))
	}
	if info.Version == "" || info.StartedAt.IsZero() {
		t.Errorf("info missing version/started_at: %+v", info)
	}

	// Socket permissions were tightened post-listen.
	fi, err := os.Stat(info.Socket)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perm = %o, want 0600", perm)
	}

	// A full round-trip through the published socket works.
	conn, err := net.Dial("unix", info.Socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if got := string(buf); got != "ping\n" {
		t.Fatalf("echo = %q, want %q", got, "ping\n")
	}
	_ = conn.Close()
}

func TestOwnerSocketFallbackPublished(t *testing.T) {
	// A dataDir deep enough that the primary socket path exceeds the limit:
	// the owner must listen on the os.TempDir() fallback and publish it.
	base := t.TempDir()
	dataDir := filepath.Join(base, strings.Repeat("very-long-vault-segment/", 4), ".marmot-data")
	if len(filepath.Join(dataDir, socketFileName)) <= maxSocketPathLen {
		t.Skip("temp dir too short to force fallback") // defensive; never expected
	}

	lock, err := TryAcquire(dataDir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	own := NewOwner(dataDir, lock, func(c net.Conn) error {
		_, err := io.Copy(io.Discard, c)
		return err
	})
	defer func() {
		_ = own.Close()
		_ = lock.Release()
	}()

	if err := own.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	info, err := ReadInfo(dataDir)
	if err != nil {
		t.Fatalf("ReadInfo: %v", err)
	}
	if !strings.HasPrefix(info.Socket, os.TempDir()) {
		t.Fatalf("published socket %q not on the fallback path", info.Socket)
	}
	// Consumers never re-derive the path — but the derivation must agree.
	if info.Socket != SocketPath(dataDir) {
		t.Fatalf("published %q != derived %q", info.Socket, SocketPath(dataDir))
	}
	conn, err := net.Dial("unix", info.Socket)
	if err != nil {
		t.Fatalf("dial fallback socket: %v", err)
	}
	_ = conn.Close()
}

func TestOwnerRemovesStaleSocket(t *testing.T) {
	dataDir, _, own := newTestOwner(t, func(c net.Conn) error {
		_, err := io.Copy(io.Discard, c)
		return err
	})

	// Simulate a SIGKILLed previous owner: a leftover socket file. The fresh
	// owner holds the flock, so no live owner exists and removal is safe.
	stale := SocketPath(dataDir)
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := own.Listen(); err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	conn, err := net.Dial("unix", stale)
	if err != nil {
		t.Fatalf("dial after stale removal: %v", err)
	}
	_ = conn.Close()
}

func TestOwnerWaitIdleBlocksUntilSessionEnds(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	dataDir, _, own := newTestOwner(t, func(c net.Conn) error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err := own.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	conn, err := net.Dial("unix", SocketPath(dataDir))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}

	idle := make(chan struct{})
	go func() {
		own.WaitIdle(context.Background())
		close(idle)
	}()

	select {
	case <-idle:
		t.Fatal("WaitIdle returned while a session was active")
	case <-time.After(150 * time.Millisecond):
	}

	close(release) // handler returns, refcount drops to zero
	select {
	case <-idle:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle did not return after last session ended")
	}
}

func TestOwnerWaitIdleContextCancel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, 1)
	dataDir, _, own := newTestOwner(t, func(c net.Conn) error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err := own.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	conn, err := net.Dial("unix", SocketPath(dataDir))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	idle := make(chan struct{})
	go func() {
		own.WaitIdle(ctx)
		close(idle)
	}()
	cancel() // signal path: WaitIdle must return despite the live session
	select {
	case <-idle:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle did not return on context cancellation")
	}
}

// TestOwnerWaitIdleLateDialRace hammers the shutdown/attach race from plan
// 2.4: dialers race WaitIdle's transition from "sessions == 0 observed" to
// "accept stopped". Every dial must be served to completion (full response)
// or dropped with zero bytes / a dial error — never half-served.
func TestOwnerWaitIdleLateDialRace(t *testing.T) {
	const response = "hello\n"
	dataDir, _, own := newTestOwner(t, func(c net.Conn) error {
		_, err := c.Write([]byte(response))
		return err
	})
	if err := own.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	socket := SocketPath(dataDir)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	violations := make(chan string, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn, err := net.Dial("unix", socket)
				if err != nil {
					continue // dial refused: dropped before accept — fine
				}
				data, _ := io.ReadAll(conn) // error after 0 bytes = clean drop
				_ = conn.Close()
				if got := string(data); got != "" && got != response {
					select {
					case violations <- got:
					default:
					}
				}
			}
		}()
	}

	// Let some sessions flow, then run the teardown wait mid-storm.
	time.Sleep(50 * time.Millisecond)
	waitDone := make(chan struct{})
	go func() {
		own.WaitIdle(context.Background())
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("WaitIdle wedged during dial storm")
	}
	close(stop)
	wg.Wait()

	select {
	case got := <-violations:
		t.Fatalf("half-served connection observed: %q", got)
	default:
	}

	// After WaitIdle stopped the accept loop, new dials must fail: a late
	// proxy re-enters the election instead of being silently half-served.
	if conn, err := net.Dial("unix", socket); err == nil {
		_ = conn.Close()
		t.Fatal("dial succeeded after WaitIdle stopped accepting")
	}
}

func TestOwnerCloseIdempotentAndBeforeListen(t *testing.T) {
	_, _, own := newTestOwner(t, func(c net.Conn) error { return nil })
	// Close before Listen must not panic or block.
	if err := own.Close(); err != nil {
		t.Fatalf("Close before Listen: %v", err)
	}
	if err := own.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := own.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := own.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// WaitIdle after Close returns immediately (zero sessions, accept stopped).
	done := make(chan struct{})
	go func() {
		own.WaitIdle(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle after Close did not return")
	}
}

func TestBoundedStop(t *testing.T) {
	// Nil scheduler: no-op.
	BoundedStop(nil, time.Second)

	// A started scheduler stops well inside the bound.
	sched := summary.NewScheduler(nil, summary.DefaultSchedulerConfig(), t.TempDir(), "test",
		func() ([]*node.Node, error) { return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)
	start := time.Now()
	BoundedStop(sched, 5*time.Second)
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("BoundedStop took %v, expected fast stop", elapsed)
	}
	// Stopping an already-stopped scheduler is a no-op inside the bound too.
	BoundedStop(sched, time.Second)
}

func TestStartGraphWatcherReloadsAndWatchesNewDirs(t *testing.T) {
	dir := t.TempDir()
	store := node.NewStore(dir)
	eng := &mcp.Engine{}

	stop, err := StartGraphWatcher(dir, eng)
	if err != nil {
		t.Fatalf("StartGraphWatcher: %v", err)
	}
	defer stop()

	waitForNode := func(id string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if g := eng.GetGraph(); g != nil {
				for _, n := range g.AllNodes() {
					if n.ID == id {
						return
					}
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("node %q never appeared in the reloaded graph", id)
	}

	// A node written at the vault root triggers a debounced reload.
	if err := store.SaveNode(&node.Node{
		ID: "root-node", Type: "function", Namespace: "test", Status: "active", Summary: "Root node.",
	}); err != nil {
		t.Fatal(err)
	}
	waitForNode("root-node")

	// A node in a directory created AFTER the watcher started — the fix over
	// the api watcher, which never saw dirs created post-start.
	if err := store.SaveNode(&node.Node{
		ID: "newns/first", Type: "function", Namespace: "newns", Status: "active", Summary: "First in new dir.",
	}); err != nil {
		t.Fatal(err)
	}
	waitForNode("newns/first")

	// And the new directory is genuinely watched: a later write inside it
	// (no new dir-create event to piggyback on) is also picked up.
	time.Sleep(1500 * time.Millisecond) // let the previous debounce window close
	if err := store.SaveNode(&node.Node{
		ID: "newns/second", Type: "function", Namespace: "newns", Status: "active", Summary: "Second in new dir.",
	}); err != nil {
		t.Fatal(err)
	}
	waitForNode("newns/second")
}
