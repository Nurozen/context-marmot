package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/nurozen/context-marmot/internal/graph"
	"github.com/nurozen/context-marmot/internal/mcp"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/summary"
)

// Owner runs the elected process's unix-socket accept loop. Each accepted
// connection is a full MCP session served by handle on its own goroutine;
// a session refcount lets the owner linger headless until the last proxy
// disconnects (WaitIdle) before shutting down.
type Owner struct {
	dataDir string
	lock    *Lock
	handle  func(net.Conn) error

	mu            sync.Mutex
	cond          *sync.Cond
	sessions      int
	listener      net.Listener
	socketPath    string
	acceptStopped bool

	acceptDone chan struct{} // closed when the accept loop exits
}

// NewOwner returns an Owner that will listen on the vault's daemon socket
// and serve each accepted connection with handle. The lock must already be
// held; Listen publishes the socket path through it.
func NewOwner(dataDir string, lock *Lock, handle func(net.Conn) error) *Owner {
	o := &Owner{dataDir: dataDir, lock: lock, handle: handle}
	o.cond = sync.NewCond(&o.mu)
	return o
}

// Listen removes any stale socket file (safe: this process holds the flock,
// so no live owner exists), starts listening, tightens permissions to 0600,
// spawns the accept loop, and only then publishes daemon.info.json so
// readers never see an info file pointing at a dead socket.
func (o *Owner) Listen() error {
	path := SocketPath(o.dataDir)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket %q: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen %q: %w", path, err)
	}
	// Listener is created with the default umask; tighten as defense-in-depth
	// (the vault is user-private anyway).
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod socket %q: %w", path, err)
	}

	o.mu.Lock()
	o.listener = ln
	o.socketPath = path
	o.acceptDone = make(chan struct{})
	o.mu.Unlock()

	go o.acceptLoop(ln)

	info := Info{
		PID:       os.Getpid(),
		Socket:    path,
		Version:   Version,
		StartedAt: time.Now().UTC(),
	}
	if err := o.lock.WriteInfo(info); err != nil {
		_ = o.Close()
		return fmt.Errorf("publish daemon info: %w", err)
	}
	return nil
}

// acceptLoop serves each connection on its own goroutine, tracking the
// session refcount. The count is incremented on the accept goroutine itself,
// so once acceptDone is closed every accepted connection is already counted —
// the invariant WaitIdle's post-stop re-check relies on.
func (o *Owner) acceptLoop(ln net.Listener) {
	defer close(o.acceptDone)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		o.mu.Lock()
		o.sessions++
		o.mu.Unlock()
		go func(c net.Conn) {
			defer func() {
				_ = c.Close()
				o.mu.Lock()
				o.sessions--
				o.cond.Broadcast()
				o.mu.Unlock()
			}()
			if err := o.handle(c); err != nil && !errors.Is(err, io.EOF) {
				fmt.Fprintf(os.Stderr, "daemon: session ended with error: %v\n", err)
			}
		}(conn)
	}
}

// WaitIdle blocks until there are no active socket sessions (or ctx is
// cancelled — the signal path). When the count first hits zero it stops the
// accept loop and re-checks: a connection accepted in the window between the
// zero observation and the accept stop is already counted (see acceptLoop),
// so it is served to completion; anything dialing later fails the dial or is
// dropped by the kernel with no bytes exchanged — never silently half-served.
func (o *Owner) WaitIdle(ctx context.Context) {
	// Wake the cond waiter on cancellation. Taking the mutex before
	// broadcasting guarantees the waiter is parked (or will re-check
	// ctx.Err() before parking), so the wakeup cannot be lost.
	stop := context.AfterFunc(ctx, func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.cond.Broadcast()
	})
	defer stop()

	o.mu.Lock()
	for o.sessions > 0 && ctx.Err() == nil {
		o.cond.Wait()
	}
	done := o.acceptDone
	o.mu.Unlock()
	if ctx.Err() != nil {
		return
	}

	// Sessions hit zero: stop accepting so no new session can start, then
	// re-check for a connection that raced in before the listener closed.
	o.stopAccepting()
	if done != nil {
		<-done
	}
	o.mu.Lock()
	for o.sessions > 0 && ctx.Err() == nil {
		o.cond.Wait()
	}
	o.mu.Unlock()
}

// stopAccepting closes the listener exactly once. Closing a net-created
// *net.UnixListener also unlinks the socket file, so late dialers fail fast.
func (o *Owner) stopAccepting() {
	o.mu.Lock()
	ln := o.listener
	stopped := o.acceptStopped
	if ln != nil {
		// Only latch the flag once a listener exists: a Close before Listen
		// must not swallow the real stop later.
		o.acceptStopped = true
	}
	o.mu.Unlock()
	if ln != nil && !stopped {
		_ = ln.Close()
	}
}

// Close stops accepting and removes the socket file. It MUST run before
// lock.Release() so a late proxy either fails the dial or gets an immediate
// drop and re-enters the election against a still-ordered teardown. Close is
// idempotent and safe to call even if Listen was never called; it does not
// wait for in-flight sessions (that is WaitIdle's job).
func (o *Owner) Close() error {
	o.stopAccepting()
	o.mu.Lock()
	done := o.acceptDone
	path := o.socketPath
	o.mu.Unlock()
	if done != nil {
		<-done
	}
	if path != "" {
		// The listener's Close already unlinks; this covers error paths.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove socket %q: %w", path, err)
		}
	}
	return nil
}

// BoundedStop runs s.Stop() but proceeds after timeout: Stop can block up to
// the 2-minute LLM regeneration timeout, while the e2e harness kills serve 5s
// after stdin close. An abandoned regeneration writes _summary.md atomically
// (tmp+rename), so the worst case is a wasted LLM call, never a torn file.
// Heatmap save, engine close, and lock release must not wait on it.
func BoundedStop(s *summary.Scheduler, timeout time.Duration) {
	if s == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		fmt.Fprintf(os.Stderr, "daemon: summary scheduler stop exceeded %s; proceeding\n", timeout)
	}
}

// StartGraphWatcher watches the vault directory for node .md changes and
// reloads the engine's in-memory graph, debounced to batch rapid writes into
// a single reload. This is what keeps the owner's graph fresh while other
// processes (marmot index, ui) write node files directly. Unlike the api
// watcher it was modeled on, it also watches directories created after start
// (new namespaces) by re-adding them on fsnotify.Create. The returned
// function stops the watcher.
func StartGraphWatcher(dir string, eng *mcp.Engine) (stop func(), err error) {
	return StartGraphWatcherNotify(dir, eng, nil)
}

// StartGraphWatcherNotify is StartGraphWatcher with an optional hook invoked
// after each successful graph reload. The api server delegates here and uses
// the hook to push SSE version bumps to connected UI clients; onReload may be
// nil.
func StartGraphWatcherNotify(dir string, eng *mcp.Engine, onReload func()) (stop func(), err error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	// Watch the vault dir and all current subdirectories (where node .md
	// files live), skipping hidden (.obsidian, .marmot-data) and system
	// (_bridges, _heat) dirs.
	if err := fw.Add(dir); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("watch %q: %w", dir, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || skipDirName(e.Name()) {
			continue
		}
		_ = fw.Add(filepath.Join(dir, e.Name())) // best-effort
	}

	stopCh := make(chan struct{})
	go func() {
		const debounce = 1 * time.Second
		var timer *time.Timer
		pending := false
		schedule := func() {
			if !pending {
				pending = true
				timer = time.NewTimer(debounce)
			}
		}

		for {
			select {
			case <-stopCh:
				if timer != nil {
					timer.Stop()
				}
				_ = fw.Close()
				return
			case event, ok := <-fw.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
					continue
				}
				// A directory created after start (e.g. a new namespace)
				// must be watched too, and may already contain files
				// written before the watch attached — so reload as well.
				if event.Op&fsnotify.Create != 0 {
					if fi, statErr := os.Stat(event.Name); statErr == nil && fi.IsDir() {
						if !skipDirName(filepath.Base(event.Name)) {
							_ = fw.Add(event.Name) // best-effort
							schedule()
						}
						continue
					}
				}
				// Only react to .md files.
				if !strings.HasSuffix(event.Name, ".md") {
					continue
				}
				// Ignore underscore-prefixed files (_config.md, _summary.md, etc.)
				if strings.HasPrefix(filepath.Base(event.Name), "_") {
					continue
				}
				schedule()
			case _, ok := <-fw.Errors:
				if !ok {
					return
				}
			case <-func() <-chan time.Time {
				if timer != nil {
					return timer.C
				}
				return nil
			}():
				pending = false
				if reloadGraph(dir, eng) && onReload != nil {
					onReload()
				}
			}
		}
	}()

	return func() { close(stopCh) }, nil
}

// skipDirName reports whether a vault subdirectory should not be watched:
// hidden dirs (.obsidian, .marmot-data) and system dirs (_bridges, _heat).
func skipDirName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// reloadGraph re-reads all nodes from disk and atomically replaces the
// engine's in-memory graph. It reports whether the reload succeeded.
func reloadGraph(dir string, eng *mcp.Engine) bool {
	newGraph, err := graph.LoadGraph(node.NewStore(dir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: graph reload failed: %v\n", err)
		return false
	}
	eng.SetGraph(newGraph)
	fmt.Fprintf(os.Stderr, "daemon: graph reloaded (%d nodes)\n", len(newGraph.AllNodes()))
	return true
}
