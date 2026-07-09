// Package daemon implements single-owner election for `marmot serve`: at most
// one process per vault owns the engine, summary scheduler, and graph watcher,
// while every other serve process relays its stdio MCP session to the owner
// over a unix socket. Election is a kernel flock(2) on daemon.lock — the lock
// is released the instant the holder dies (including SIGKILL), so "lock held"
// is equivalent to "owner alive" with no liveness probing or stale-lock GC.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var (
	// ErrHeld is returned by TryAcquire when another live process owns the vault.
	ErrHeld = errors.New("daemon lock held by another process")
	// ErrOwnerGone is returned by RunProxy when the owner dies or the socket
	// is unreachable; the caller should re-enter the election loop.
	ErrOwnerGone = errors.New("daemon owner gone")
	// ErrNoOwner is returned by RunProxy/RunProxySession when the initial
	// dial fails — no owner was ever reached. It wraps ErrOwnerGone so
	// callers that don't care still match; the election loop uses the
	// distinction to bound its retries when a flock-holder never becomes
	// dialable (a wedged or stale owner must yield a clear error, not a
	// silent spin — plan 2.11).
	ErrNoOwner = fmt.Errorf("no dialable owner: %w", ErrOwnerGone)
	// ErrNoResume is returned when the owner dies mid-session and
	// MARMOT_PROXY_NO_RESUME=1 disables handshake replay: the proxy must
	// exit nonzero so the MCP client restarts `marmot serve` itself
	// (degraded mode, plan 2.6), never silently re-elect and serve the
	// already-initialized client over a fresh, un-initialized session.
	// Deliberately does NOT match ErrOwnerGone.
	ErrNoResume = errors.New("owner died and MARMOT_PROXY_NO_RESUME=1 disables session resumption")
	// ErrUnsupported is returned on platforms without flock support (Windows).
	ErrUnsupported = errors.New("daemon mode not supported on this platform")
)

const (
	lockFileName = "daemon.lock"
	infoFileName = "daemon.info.json"
)

// Version is stamped into daemon.info.json. The main package overrides it
// with the build version at startup; "dev" matches cmd/marmot's default.
var Version = "dev"

// Info describes a running owner. It is published to daemon.info.json after
// the socket is listening so proxies and CLI commands can find the socket and
// answer "is an owner running?". The actual socket path is always published
// here, so consumers never re-derive it (the >96-byte fallback is invisible).
type Info struct {
	PID       int       `json:"pid"`
	Socket    string    `json:"socket"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}

// Lock represents ownership of a vault's daemon.lock. The file descriptor
// stays open for the owner's lifetime; the flock travels with the fd and is
// dropped by the kernel when the fd closes (or the process dies).
type Lock struct {
	dataDir string
	file    *os.File
}

// TryAcquire attempts to become the vault owner by taking an exclusive,
// non-blocking flock on <dataDir>/daemon.lock. It returns (lock, nil) if this
// process is now the owner, (nil, ErrHeld) if another live process holds the
// vault, or any other error. The PID written into the lock file is diagnostic
// only — it is never trusted for liveness.
func TryAcquire(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %q: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, lockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	if err := tryFlock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrHeld) || errors.Is(err, ErrUnsupported) {
			return nil, err
		}
		return nil, fmt.Errorf("flock %q: %w", path, err)
	}
	// Diagnostic-only PID content; failures here don't affect ownership.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	}
	return &Lock{dataDir: dataDir, file: f}, nil
}

// WriteInfo publishes info to <dataDir>/daemon.info.json atomically
// (tmp+rename). It must be called only after the socket is listening.
func (l *Lock) WriteInfo(info Info) error {
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal daemon info: %w", err)
	}
	tmp, err := os.CreateTemp(l.dataDir, infoFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp info file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp info file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp info file: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(l.dataDir, infoFileName)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish daemon info: %w", err)
	}
	return nil
}

// Release removes daemon.info.json and closes the lock fd, dropping the flock.
// The socket file itself is removed by Owner.Close, which must run first so a
// late proxy either fails the dial or gets a clean drop, never a half-serve.
// Release is idempotent.
func (l *Lock) Release() error {
	if l.file == nil {
		return nil
	}
	if err := os.Remove(filepath.Join(l.dataDir, infoFileName)); err != nil && !os.IsNotExist(err) {
		// Keep going: closing the fd is what actually frees the vault.
		fmt.Fprintf(os.Stderr, "daemon: remove info file: %v\n", err)
	}
	err := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("close lock file: %w", err)
	}
	return nil
}

// ReadInfo reads the owner's published daemon.info.json. Proxies and other
// CLI commands use it to find the socket; a successful read does not prove
// the owner is alive (that requires a successful dial).
func ReadInfo(dataDir string) (Info, error) {
	path := filepath.Join(dataDir, infoFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return Info{}, fmt.Errorf("read %q: %w", path, err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("parse %q: %w", path, err)
	}
	return info, nil
}
