package main

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/nurozen/context-marmot/internal/daemon"
	"github.com/nurozen/context-marmot/internal/heatmap"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/summary"
)

// ---------------------------------------------------------------------------
// Shared helpers for surface-coverage tests.
// ---------------------------------------------------------------------------

// withStdin temporarily replaces os.Stdin with a file containing content and
// restores it afterwards. Interactive commands that scan stdin see EOF (or the
// provided content) instead of blocking on a real terminal.
func withStdin(t *testing.T, content string, fn func()) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if _, err := f.WriteString(content); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
	}
	old := os.Stdin
	os.Stdin = f
	defer func() {
		os.Stdin = old
		_ = f.Close()
	}()
	fn()
}

// writeVaultWithID creates a minimal mock-embedder vault carrying a vault_id.
func writeVaultWithID(t *testing.T, dir, vaultID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	idLine := ""
	if vaultID != "" {
		idLine = "vault_id: " + vaultID + "\n"
	}
	content := "---\nversion: \"1\"\n" + idLine + "namespace: default\nembedding_provider: mock\nembedding_model: test-model\n---\n# Vault\n"
	if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// version
// ---------------------------------------------------------------------------

func TestVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		if code := run([]string{arg}); code != 0 {
			t.Fatalf("run(%q) exit code = %d, want 0", arg, code)
		}
	}
}

// ---------------------------------------------------------------------------
// init / configure / setup command wrappers (previously 0% cmd* funcs)
// ---------------------------------------------------------------------------

func TestCmdInitFullFlow(t *testing.T) {
	vault := filepath.Join(t.TempDir(), ".marmot")
	// Empty stdin -> configure uses defaults (mock), setup auto-generates.
	withStdin(t, "", func() {
		if code := run([]string{"init", "--dir", vault}); code != 0 {
			t.Fatalf("init exit code = %d, want 0", code)
		}
	})
	assertFileExists(t, filepath.Join(vault, "_config.md"))
}

func TestCmdInitFailsOnExisting(t *testing.T) {
	vault := filepath.Join(t.TempDir(), ".marmot")
	if err := runInit(vault); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	withStdin(t, "", func() {
		if code := run([]string{"init", "--dir", vault}); code != 1 {
			t.Fatalf("second init exit code = %d, want 1", code)
		}
	})
}

func TestCmdConfigureCommand(t *testing.T) {
	vault := newTestVault(t)
	withStdin(t, "\n\n", func() {
		if code := run([]string{"configure", "--dir", vault}); code != 0 {
			t.Fatalf("configure exit code = %d, want 0", code)
		}
	})
}

func TestCmdConfigureNonexistent(t *testing.T) {
	withStdin(t, "", func() {
		if code := run([]string{"configure", "--dir", filepath.Join(t.TempDir(), "nope")}); code != 1 {
			t.Fatalf("configure nonexistent exit code = %d, want 1", code)
		}
	})
}

func TestCmdSetupCommand(t *testing.T) {
	vault := newTestVault(t)
	if code := run([]string{"setup", "--dir", vault, "--claude"}); code != 0 {
		t.Fatalf("setup exit code = %d, want 0", code)
	}
	assertFileExists(t, filepath.Join(filepath.Dir(vault), ".mcp.json"))
}

func TestCmdSetupNonexistent(t *testing.T) {
	if code := run([]string{"setup", "--dir", filepath.Join(t.TempDir(), "nope")}); code != 1 {
		t.Fatalf("setup nonexistent exit code = %d, want 1", code)
	}
}

// ---------------------------------------------------------------------------
// serve (blocks on stdin JSON-RPC; feed EOF so ListenStdio returns)
// ---------------------------------------------------------------------------

// TestServeCommandEOF drives the default serve path: the daemon election is
// on by default, so a plain `marmot serve` wins the election, serves stdio
// until EOF, WaitIdle returns immediately (no proxies), and teardown leaves
// the vault clean — lock free, daemon.sock and daemon.info.json removed.
// Shutdown is exercised via stdin EOF only; no test in this package may send
// SIGINT/SIGTERM.
func TestServeCommandEOF(t *testing.T) {
	// Opt-out explicitly off: this pins the default (election) path
	// regardless of the developer's environment.
	t.Setenv("MARMOT_NO_DAEMON", "")
	vault := initTestVault(t)
	withStdin(t, "", func() {
		if code := run([]string{"serve", "--dir", vault}); code != 0 {
			t.Fatalf("serve exit code = %d, want 0", code)
		}
	})

	dataDir := filepath.Join(vault, ".marmot-data")
	if _, err := os.Stat(filepath.Join(dataDir, "daemon.info.json")); !os.IsNotExist(err) {
		t.Errorf("daemon.info.json still present after serve exit (stat err = %v)", err)
	}
	if _, err := os.Stat(daemon.SocketPath(dataDir)); !os.IsNotExist(err) {
		t.Errorf("daemon socket still present after serve exit (stat err = %v)", err)
	}
	// Lock free: a fresh acquire must succeed immediately.
	lock, err := daemon.TryAcquire(dataDir)
	if err != nil {
		t.Fatalf("daemon.lock not free after serve exit: %v", err)
	}
	_ = lock.Release()
}

// TestServeCommandEOFNoDaemon pins the standalone opt-out: with
// MARMOT_NO_DAEMON=1 (or the --no-daemon flag) serve never elects and must
// create ZERO daemon artifacts — no lock file, no info file, no socket.
func TestServeCommandEOFNoDaemon(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		args []string
	}{
		{name: "env", env: "1", args: []string{"serve", "--dir"}},
		{name: "flag", env: "", args: []string{"serve", "--no-daemon", "--dir"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MARMOT_NO_DAEMON", tc.env)
			vault := initTestVault(t)
			withStdin(t, "", func() {
				if code := run(append(tc.args, vault)); code != 0 {
					t.Fatalf("standalone serve exit code = %d, want 0", code)
				}
			})
			dataDir := filepath.Join(vault, ".marmot-data")
			for _, name := range []string{"daemon.lock", "daemon.info.json"} {
				if _, err := os.Stat(filepath.Join(dataDir, name)); !os.IsNotExist(err) {
					t.Errorf("standalone serve created %s (stat err = %v)", name, err)
				}
			}
			if _, err := os.Stat(daemon.SocketPath(dataDir)); !os.IsNotExist(err) {
				t.Errorf("standalone serve created a daemon socket (stat err = %v)", err)
			}
		})
	}
}

// startFakeOwner makes this test process a dial-able "live owner" of vault:
// it takes the daemon lock, listens on the vault's socket (answering every
// received line with reply, when non-empty), and publishes daemon.info.json.
// Cleanup releases everything.
func startFakeOwner(t *testing.T, vault, reply string) daemon.Info {
	t.Helper()
	dataDir := filepath.Join(vault, ".marmot-data")
	lock, err := daemon.TryAcquire(dataDir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	sock := daemon.SocketPath(dataDir)
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %q: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					if reply != "" {
						_, _ = c.Write([]byte(reply + "\n"))
					}
				}
			}(conn)
		}
	}()

	info := daemon.Info{PID: os.Getpid(), Socket: sock, Version: "test", StartedAt: time.Now().UTC()}
	if err := lock.WriteInfo(info); err != nil {
		t.Fatalf("WriteInfo: %v", err)
	}
	return info
}

// TestServeSecondIsProxy runs a default `serve` while the daemon lock is
// already held and a dial-able owner socket is published: the serve must
// become a proxy and relay the client's line to the owner and the owner's
// answer back to stdout. A full in-process two-serve flow is not feasible
// here — both serves would race over the one global os.Stdin/os.Stdout pair —
// so the owner side is faked in-process and the real owner+proxy pairing is
// covered end-to-end by the e2e suite (TestConcurrentServesDaemon).
func TestServeSecondIsProxy(t *testing.T) {
	t.Setenv("MARMOT_NO_DAEMON", "")
	vault := initTestVault(t)
	const reply = `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"fake-owner-ack"}]}}`
	startFakeOwner(t, vault, reply)

	// Sanity: the published owner info is what the election loop will read.
	if _, err := daemon.ReadInfo(filepath.Join(vault, ".marmot-data")); err != nil {
		t.Fatalf("daemon.info.json unreadable: %v", err)
	}

	var out string
	var code int
	withStdin(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n", func() {
		out, code = captureRun([]string{"serve", "--dir", vault})
	})
	if code != 0 {
		t.Fatalf("proxy serve exit code = %d, want 0 (stdout: %q)", code, out)
	}
	if !strings.Contains(out, "fake-owner-ack") {
		t.Fatalf("proxy did not relay the owner's answer; stdout = %q", out)
	}
}

// TestServeStaleOwnerInfoBounded pins the plan-2.11 wedge bound for the
// stale-info case: the flock is held AND daemon.info.json is readable but
// points at a socket nothing listens on (a wedged owner, a reaped tmp
// socket, or a SIGKILLed predecessor's leftovers plus a wedged successor).
// The election loop must give up with a clear error within the wedge window
// instead of spinning silently forever at 50ms intervals.
func TestServeStaleOwnerInfoBounded(t *testing.T) {
	t.Setenv("MARMOT_NO_DAEMON", "")
	vault := initTestVault(t)
	dataDir := filepath.Join(vault, ".marmot-data")

	// Hold the flock without ever listening: a wedged owner.
	lock, err := daemon.TryAcquire(dataDir)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	// Publish info pointing at a socket that refuses every dial.
	sock := daemon.SocketPath(dataDir)
	_ = os.Remove(sock)
	if err := lock.WriteInfo(daemon.Info{PID: os.Getpid(), Socket: sock, Version: "test", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("WriteInfo: %v", err)
	}

	oldWait := ownerWedgeWait
	ownerWedgeWait = 500 * time.Millisecond
	t.Cleanup(func() { ownerWedgeWait = oldWait })

	start := time.Now()
	var code int
	withStdin(t, "", func() {
		code = run([]string{"serve", "--dir", vault})
	})
	if code != 1 {
		t.Fatalf("serve with wedged owner exit code = %d, want 1", code)
	}
	// Generous ceiling: the point is "bounded", not a precise duration.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("serve took %v to give up on a wedged owner; wedge bound not applied", elapsed)
	}
}

// TestWatchRefusedWhenOwnerAlive: `watch` duplicates the owner's watcher role
// and must be refused while an owner is dial-able.
func TestWatchRefusedWhenOwnerAlive(t *testing.T) {
	vault := initTestVault(t)
	startFakeOwner(t, vault, "")

	err := watchLoop(context.Background(), vault)
	if err == nil {
		t.Fatal("watchLoop succeeded with a live owner; want refusal")
	}
	if !strings.Contains(err.Error(), "watch is redundant") {
		t.Fatalf("watchLoop error = %v, want mention of 'watch is redundant'", err)
	}
}

// TestIndexForceRefusedWhenOwnerAlive: `index --force` deletes embeddings.db
// out from under the owner's open WAL connection and must be refused. A plain
// index (no --force) stays allowed — WAL makes its writes safe.
func TestIndexForceRefusedWhenOwnerAlive(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")
	startFakeOwner(t, vault, "")

	if code := run([]string{"index", "--force", "--dir", vault}); code != 1 {
		t.Fatalf("index --force with live owner exit code = %d, want 1", code)
	}
	if code := run([]string{"index", "--dir", vault}); code != 0 {
		t.Fatalf("plain index with live owner exit code = %d, want 0", code)
	}
}

func TestServeCommandNoVault(t *testing.T) {
	if code := run([]string{"serve", "--dir", filepath.Join(t.TempDir(), "nope")}); code != 1 {
		t.Fatalf("serve nonexistent exit code = %d, want 1", code)
	}
}

// ---------------------------------------------------------------------------
// den create/status/destroy/list (P1b)
// ---------------------------------------------------------------------------

func TestDenCommands(t *testing.T) {
	homeRoot := t.TempDir()
	t.Setenv("MARMOT_HOME", homeRoot)
	routesFile := filepath.Join(homeRoot, "routes.yml")
	routes.SetOverridePath(routesFile)
	defer routes.SetOverridePath("")

	// Capability probe: den --help exits 0.
	if code := run([]string{"den", "--help"}); code != 0 {
		t.Fatalf("den --help exit code = %d, want 0", code)
	}
	if code := run([]string{"den"}); code != 0 {
		t.Fatalf("den (no args) exit code = %d, want 0", code)
	}

	proj := t.TempDir()

	// create --no-pointer --json
	out, code := captureRun([]string{
		"den", "create", "demo-space",
		"--lifetime", "task",
		"--project", proj,
		"--no-pointer",
		"--json",
	})
	if code != 0 {
		t.Fatalf("den create exit code = %d out=%s", code, out)
	}
	if !strings.Contains(out, `"schema": 1`) && !strings.Contains(out, `"schema":1`) {
		t.Fatalf("den create --json missing schema: %s", out)
	}
	if !strings.Contains(out, `"pointer_written": false`) && !strings.Contains(out, `"pointer_written":false`) {
		t.Fatalf("den create --no-pointer must set pointer_written false: %s", out)
	}
	if !strings.Contains(out, `"den_id": "demo-space"`) && !strings.Contains(out, `"den_id":"demo-space"`) {
		t.Fatalf("den create missing den_id: %s", out)
	}

	// status --json
	out, code = captureRun([]string{"den", "status", "demo-space", "--json"})
	if code != 0 {
		t.Fatalf("den status exit code = %d out=%s", code, out)
	}
	if !strings.Contains(out, `"lifetime"`) {
		t.Fatalf("den status missing lifetime: %s", out)
	}

	// list
	out, code = captureRun([]string{"den", "list"})
	if code != 0 || !strings.Contains(out, "demo-space") {
		t.Fatalf("den list = %q code=%d", out, code)
	}

	// destroy --json
	out, code = captureRun([]string{"den", "destroy", "demo-space", "--json"})
	if code != 0 {
		t.Fatalf("den destroy exit code = %d out=%s", code, out)
	}
	if !strings.Contains(out, `"destroyed": true`) && !strings.Contains(out, `"destroyed":true`) {
		t.Fatalf("den destroy missing destroyed: %s", out)
	}

	// status missing → structured error
	out, code = captureRun([]string{"den", "status", "missing-id", "--json"})
	if code == 0 {
		t.Fatalf("den status missing should fail, out=%s", out)
	}
	if !strings.Contains(out, `"code"`) || !strings.Contains(out, "den_not_found") {
		t.Fatalf("den status error envelope: %s", out)
	}

	// create with pointer
	proj2 := t.TempDir()
	out, code = captureRun([]string{
		"den", "create", "with-ptr",
		"--lifetime", "durable",
		"--project", proj2,
		"--json",
	})
	if code != 0 {
		t.Fatalf("den create with pointer exit = %d out=%s", code, out)
	}
	if !strings.Contains(out, `"pointer_written": true`) && !strings.Contains(out, `"pointer_written":true`) {
		t.Fatalf("expected pointer_written true: %s", out)
	}

	// dry-run
	out, code = captureRun([]string{
		"den", "create", "dry-den",
		"--project", t.TempDir(),
		"--dry-run", "--json",
	})
	if code != 0 {
		t.Fatalf("den create dry-run exit = %d out=%s", code, out)
	}
	if !strings.Contains(out, `"dry_run": true`) && !strings.Contains(out, `"dry_run":true`) {
		t.Fatalf("dry-run envelope: %s", out)
	}

	// route project verbs
	if code := run([]string{"route", "add", "--project", proj2, "with-ptr"}); code != 0 {
		t.Fatalf("route add --project exit = %d", code)
	}
	archived := filepath.Join(t.TempDir(), ".archive", "moved")
	if err := os.MkdirAll(archived, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"route", "set-project", "--from", proj2, "--to", archived}); code != 0 {
		t.Fatalf("route set-project exit = %d", code)
	}
	if code := run([]string{"route", "rm", "--project", archived}); code != 0 {
		t.Fatalf("route rm --project exit = %d", code)
	}
}

// ---------------------------------------------------------------------------
// route (entire file was 0%; isolate storage via SetOverridePath)
// ---------------------------------------------------------------------------

func TestRouteCommands(t *testing.T) {
	routesFile := filepath.Join(t.TempDir(), "routes.yml")
	routes.SetOverridePath(routesFile)
	defer routes.SetOverridePath("")

	// Empty list.
	if out, code := captureRun([]string{"route"}); code != 0 || !strings.Contains(out, "No vaults registered") {
		t.Fatalf("route list empty = %q code=%d", out, code)
	}

	target := t.TempDir()
	if code := run([]string{"route", "add", "vault-x", target}); code != 0 {
		t.Fatalf("route add exit code = %d", code)
	}
	if out, code := captureRun([]string{"route"}); code != 0 || !strings.Contains(out, "vault-x") {
		t.Fatalf("route list = %q code=%d", out, code)
	}
	if out, code := captureRun([]string{"route", "resolve", "vault-x"}); code != 0 || !strings.Contains(out, target) {
		t.Fatalf("route resolve = %q code=%d", out, code)
	}
	if code := run([]string{"route", "rm", "vault-x"}); code != 0 {
		t.Fatalf("route rm exit code = %d", code)
	}
	if code := run([]string{"route", "resolve", "vault-x"}); code != 1 {
		t.Fatalf("route resolve removed exit code = %d, want 1", code)
	}
}

func TestRouteErrorPaths(t *testing.T) {
	routesFile := filepath.Join(t.TempDir(), "routes.yml")
	routes.SetOverridePath(routesFile)
	defer routes.SetOverridePath("")

	cases := [][]string{
		{"route", "add"}, // missing args
		{"route", "add", "v", filepath.Join(t.TempDir(), "no")}, // path does not exist
		{"route", "rm"},                 // missing arg
		{"route", "rm", "missing"},      // not registered
		{"route", "resolve"},            // missing arg
		{"route", "resolve", "missing"}, // not registered
		{"route", "bogus"},              // unknown subcommand
	}
	for _, args := range cases {
		if code := run(args); code != 1 {
			t.Fatalf("run(%v) exit code = %d, want 1", args, code)
		}
	}

	// route add with a path that is a file (not a directory).
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"route", "add", "v", file}); code != 1 {
		t.Fatalf("route add file exit code = %d, want 1", code)
	}
}

// ---------------------------------------------------------------------------
// sdk
// ---------------------------------------------------------------------------

func TestSDKCommand(t *testing.T) {
	if out, code := captureRun([]string{"sdk"}); code != 0 || len(out) == 0 {
		t.Fatalf("sdk stdout empty, code=%d", code)
	}
	out := filepath.Join(t.TempDir(), "sdk.ts")
	if code := run([]string{"sdk", "--out", out, "--base-url", "http://example.test"}); code != 0 {
		t.Fatalf("sdk --out exit code = %d", code)
	}
	assertFileExists(t, out)

	if code := run([]string{"sdk", "--out", filepath.Join(t.TempDir(), "missing-dir", "sdk.ts")}); code != 1 {
		t.Fatalf("sdk --out bad path exit code = %d, want 1", code)
	}
}

// ---------------------------------------------------------------------------
// ui
// ---------------------------------------------------------------------------

func TestUICommandNoVault(t *testing.T) {
	if code := run([]string{"ui", "--dir", filepath.Join(t.TempDir(), "nope"), "--no-open"}); code != 1 {
		t.Fatalf("ui nonexistent exit code = %d, want 1", code)
	}
}

// TestUIInvalidPort forces runUIPipeline's ListenAndServe to fail immediately by
// using an out-of-range port, exercising the full engine wiring and startup path
// without blocking. Note: this leaves a signal-wait goroutine alive, so no test
// in this package may send SIGINT/SIGTERM to the process.
func TestUIInvalidPort(t *testing.T) {
	vault := initTestVault(t)
	// Port 70000 is out of the valid 0-65535 range; net.Listen rejects it,
	// so ListenAndServe returns an error immediately instead of blocking.
	if code := run([]string{"ui", "--dir", vault, "--no-open", "--port", "70000"}); code != 1 {
		t.Fatalf("ui invalid-port exit code = %d, want 1", code)
	}
}

// ---------------------------------------------------------------------------
// bridge — cross-vault mode and additional error branches
// ---------------------------------------------------------------------------

func TestCrossVaultBridgeCommand(t *testing.T) {
	local := filepath.Join(t.TempDir(), ".marmot")
	remote := filepath.Join(t.TempDir(), ".marmot")
	writeVaultWithID(t, local, "local-vault")
	writeVaultWithID(t, remote, "remote-vault")

	// Two positional paths.
	if code := run([]string{"bridge", local, remote}); code != 0 {
		t.Fatalf("cross-vault bridge exit code = %d, want 0", code)
	}

	// verify --bridges should now exercise the cross-vault bridge check path.
	writeTestNode(t, local, "svc/a", "default")
	if code := run([]string{"verify", "--bridges", "--dir", local}); code != 0 {
		t.Fatalf("verify --bridges exit code = %d, want 0", code)
	}
}

func TestCrossVaultBridgeSinglePathArg(t *testing.T) {
	local := filepath.Join(t.TempDir(), ".marmot")
	remote := filepath.Join(t.TempDir(), ".marmot")
	writeVaultWithID(t, local, "local-vault")
	writeVaultWithID(t, remote, "remote-vault")

	// Single path arg -> local from --dir, remote from positional.
	if code := run([]string{"bridge", "--dir", local, remote}); code != 0 {
		t.Fatalf("single-path cross-vault bridge exit code = %d, want 0", code)
	}
	// Flags may also follow the positional path.
	if code := run([]string{"bridge", remote, "--dir", local}); code != 0 {
		t.Fatalf("single-path cross-vault bridge trailing --dir exit code = %d, want 0", code)
	}
}

func TestCrossVaultBridgeMissingVault(t *testing.T) {
	if code := run([]string{"bridge", filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")}); code != 1 {
		t.Fatalf("cross-vault bridge missing dirs exit code = %d, want 1", code)
	}
}

func TestBridgeArgErrors(t *testing.T) {
	vault := initTestVault(t)
	cases := [][]string{
		{"bridge", "--dir", vault},                 // 0 positional args
		{"bridge", "a", "b", "c", "--dir", vault},  // too many args
		{"bridge", "--dir", vault, "_bad", "beta"}, // invalid namespace name
	}
	for _, args := range cases {
		if code := run(args); code != 1 {
			t.Fatalf("run(%v) exit code = %d, want 1", args, code)
		}
	}
}

// ---------------------------------------------------------------------------
// warren list / refresh / propose (previously 0%)
// ---------------------------------------------------------------------------

func TestWarrenListRefreshPropose(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenRoot := testWarrenRoot(t, "product-platform", "project-a")

	// Read-only verbs are lazy (C5): before any mutating verb creates the
	// workspace, list errors and must NOT fabricate a .marmot dir.
	if _, code := captureRun([]string{"warren", "list", "--dir", marmotDir}); code != 1 {
		t.Fatalf("warren list without a workspace exit code = %d, want 1", code)
	}
	if _, err := os.Stat(marmotDir); !os.IsNotExist(err) {
		t.Fatalf("warren list must not create the workspace, stat err = %v", err)
	}

	if code := run([]string{"warren", "register", "--dir", marmotDir, "product-platform", warrenRoot}); code != 0 {
		t.Fatalf("register exit code = %d", code)
	}

	// Empty list renders once the workspace exists (registered warrens are
	// listed below; unregister round-trip is covered elsewhere).
	if out, code := captureRun([]string{"warren", "list", "--dir", marmotDir}); code != 0 || !strings.Contains(out, "product-platform") {
		t.Fatalf("warren list after register = %q code=%d", out, code)
	}

	if out, code := captureRun([]string{"warren", "list", "--dir", marmotDir}); code != 0 || !strings.Contains(out, "product-platform") {
		t.Fatalf("warren list = %q code=%d", out, code)
	}
	if code := run([]string{"warren", "list", "--dir", marmotDir, "--json"}); code != 0 {
		t.Fatalf("warren list --json exit code = %d", code)
	}
	if code := run([]string{"warren", "refresh", "--dir", marmotDir, "--warren", "product-platform"}); code != 0 {
		t.Fatalf("warren refresh exit code = %d", code)
	}
	// D3: propose is real git mechanics now, so the non-git warren fixture is
	// refused (both with an explicit --warren and via auto-resolve).
	if code := run([]string{"warren", "propose", "--dir", marmotDir, "--warren", "product-platform"}); code != 1 {
		t.Fatalf("warren propose on non-git warren exit code = %d, want 1", code)
	}
	// refresh/propose resolve a single registered Warren without --warren.
	if code := run([]string{"warren", "refresh", "--dir", marmotDir}); code != 0 {
		t.Fatalf("warren refresh (auto-resolve) exit code = %d", code)
	}
	if code := run([]string{"warren", "propose", "--dir", marmotDir}); code != 1 {
		t.Fatalf("warren propose (auto-resolve, non-git warren) exit code = %d, want 1", code)
	}
}

func TestWarrenRefreshProposeNoWarren(t *testing.T) {
	marmotDir := filepath.Join(t.TempDir(), ".marmot")
	if code := run([]string{"warren", "refresh", "--dir", marmotDir}); code != 1 {
		t.Fatalf("warren refresh no-warren exit code = %d, want 1", code)
	}
	if code := run([]string{"warren", "propose", "--dir", marmotDir}); code != 1 {
		t.Fatalf("warren propose no-warren exit code = %d, want 1", code)
	}
}

// ---------------------------------------------------------------------------
// warren project add --generate-id (covers generatedProjectID)
// ---------------------------------------------------------------------------

func TestWarrenProjectAddGenerateID(t *testing.T) {
	root := t.TempDir()
	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "product-platform"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	// --generate-id with a path whose parent dir name becomes the project ID.
	if code := run([]string{"warren", "project", "add", "--generate-id", "--warren-dir", root, "--path", "projects/billing/.marmot"}); code != 0 {
		t.Fatalf("project add --generate-id exit code = %d", code)
	}
}

func TestWarrenProjectImportGenerateIDFromPath(t *testing.T) {
	root := t.TempDir()
	// Source vault with no project metadata -> ID derived from path base.
	source := writeCLIImportSourceVault(t, filepath.Join(t.TempDir(), "reporting", ".marmot"), "src-vault")
	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "product-platform"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	if code := run([]string{"warren", "project", "import", "--generate-id", source, "--warren-dir", root}); code != 0 {
		t.Fatalf("project import --generate-id exit code = %d", code)
	}
}

// ---------------------------------------------------------------------------
// usage / unknown-subcommand paths (0% usage funcs)
// ---------------------------------------------------------------------------

func TestSubcommandUsageAndUnknownPaths(t *testing.T) {
	cases := [][]string{
		{"namespace"},                  // namespaceUsage
		{"namespace", "bogus"},         // unknown namespace subcommand
		{"warren"},                     // warrenUsage
		{"warren", "bogus"},            // unknown warren subcommand
		{"warren", "project"},          // warrenProjectUsage
		{"warren", "project", "bogus"}, // unknown project subcommand
		{"warren", "bridge"},           // warrenBridgeUsage
		{"warren", "bridge", "bogus"},  // unknown bridge subcommand
		{"warren", "init"},             // missing --id
	}
	for _, args := range cases {
		if code := run(args); code != 1 {
			t.Fatalf("run(%v) exit code = %d, want 1", args, code)
		}
	}
}

// ---------------------------------------------------------------------------
// query flag branches
// ---------------------------------------------------------------------------

func TestQueryWithExplicitBudgetAndDepth(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")
	if code := run([]string{"query", "--dir", vault, "--query", "hello", "--depth", "3", "--budget", "2048"}); code != 0 {
		t.Fatalf("query with flags exit code = %d, want 0", code)
	}
}

// ---------------------------------------------------------------------------
// watch (context-driven loop; no real signals so the UI signal goroutine is
// never triggered)
// ---------------------------------------------------------------------------

func TestWatchLoopCancels(t *testing.T) {
	vault := initTestVault(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watchLoop(ctx, vault) }()

	// Give the watcher a moment to start, then cancel.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watchLoop returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchLoop did not return after cancel")
	}
}

func TestWatchLoopNoVault(t *testing.T) {
	if err := watchLoop(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for nonexistent vault")
	}
}

// ---------------------------------------------------------------------------
// openBrowser (neutralise side effects by clearing PATH so the launcher binary
// cannot be found and Start() fails harmlessly)
// ---------------------------------------------------------------------------

func TestOpenBrowserNoSideEffects(t *testing.T) {
	t.Setenv("PATH", "")
	openBrowser("http://localhost:0/")
}

// ---------------------------------------------------------------------------
// buildEngine classifier branches (provider configured but no API key present)
// ---------------------------------------------------------------------------

func TestBuildEngineClassifierNoKey(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "")
			t.Setenv("ANTHROPIC_API_KEY", "")
			dir := filepath.Join(t.TempDir(), ".marmot")
			if err := os.MkdirAll(filepath.Join(dir, ".marmot-data"), 0o755); err != nil {
				t.Fatal(err)
			}
			content := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\nembedding_model: test-model\nclassifier_provider: " + provider + "\n---\n"
			if err := os.WriteFile(filepath.Join(dir, "_config.md"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			hermeticEngine(t, dir)
		})
	}
}

// ---------------------------------------------------------------------------
// index / static-index error branches
// ---------------------------------------------------------------------------

func TestIndexErrorPaths(t *testing.T) {
	// index on a nonexistent vault.
	if code := run([]string{"index", "--dir", filepath.Join(t.TempDir(), "nope")}); code != 1 {
		t.Fatalf("index nonexistent exit code = %d, want 1", code)
	}
	// static index (positional source) on a nonexistent vault.
	if code := run([]string{"index", "src", "--dir", filepath.Join(t.TempDir(), "nope")}); code != 1 {
		t.Fatalf("static index nonexistent exit code = %d, want 1", code)
	}
	// index --force on a valid empty vault (no nodes).
	vault := initTestVault(t)
	if code := run([]string{"index", "--force", "--dir", vault}); code != 0 {
		t.Fatalf("index --force exit code = %d, want 0", code)
	}
}

// TestIndexForceRemovesWALSidecars verifies that index --force removes stale
// WAL sidecar files alongside the embeddings DB, so an old -wal is never
// replayed into the freshly created database.
func TestIndexForceRemovesWALSidecars(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")

	dataDir := filepath.Join(vault, ".marmot-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "embeddings.db")
	// Pre-create stale sidecars (garbage content from a hypothetical killed process).
	for _, sidecar := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(sidecar, []byte("stale sidecar"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if code := run([]string{"index", "--force", "--dir", vault}); code != 0 {
		t.Fatalf("index --force exit code = %d, want 0", code)
	}

	// The stale sidecars must be gone: --force removed them before indexing,
	// and the clean close checkpoints/removes the fresh ones.
	for _, sidecar := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if data, err := os.ReadFile(sidecar); err == nil && string(data) == "stale sidecar" {
			t.Errorf("stale sidecar %s survived index --force", sidecar)
		}
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("expected fresh embeddings.db after index --force: %v", err)
	}
}

func TestStaticIndexNonexistentSource(t *testing.T) {
	vault := initTestVault(t)
	if code := run([]string{"index", filepath.Join(t.TempDir(), "no-src"), "--dir", vault}); code != 1 {
		t.Fatalf("static index bad source exit code = %d, want 1", code)
	}
}

// ---------------------------------------------------------------------------
// query / summarize / reembed additional branches
// ---------------------------------------------------------------------------

func TestQueryNonexistentVault(t *testing.T) {
	if code := run([]string{"query", "--dir", filepath.Join(t.TempDir(), "nope"), "--query", "x"}); code != 1 {
		t.Fatalf("query nonexistent exit code = %d, want 1", code)
	}
}

func TestSummarizeWithNamespaceFlag(t *testing.T) {
	vault := initTestVault(t)
	// No LLM configured -> exit 1, but exercises the --namespace flag path.
	if code := run([]string{"summarize", "--namespace", "default", "--dir", vault}); code != 1 {
		t.Fatalf("summarize --namespace exit code = %d, want 1", code)
	}
}

func TestReembedWithNamespaceFlag(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")
	// --namespace warns but still rebuilds.
	if code := run([]string{"reembed", "--namespace", "default", "--dir", vault}); code != 0 {
		t.Fatalf("reembed --namespace exit code = %d, want 0", code)
	}
}

// ---------------------------------------------------------------------------
// verify combinations
// ---------------------------------------------------------------------------

func TestVerifyNamespaceNoMatch(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")
	// Filtering to a namespace with no nodes prints the "no nodes" branch.
	if code := run([]string{"verify", "--namespace", "missing", "--dir", vault}); code != 0 {
		t.Fatalf("verify --namespace missing exit code = %d, want 0", code)
	}
}

func TestVerifyBridgesNoVaultID(t *testing.T) {
	// A vault with a cross-vault bridge but no vault_id yields a warning.
	local := filepath.Join(t.TempDir(), ".marmot")
	remote := filepath.Join(t.TempDir(), ".marmot")
	writeVaultWithID(t, local, "local-vault")
	writeVaultWithID(t, remote, "remote-vault")
	if code := run([]string{"bridge", local, remote}); code != 0 {
		t.Fatalf("cross-vault bridge exit code = %d", code)
	}
	writeTestNode(t, local, "svc/a", "default")

	// Rewrite local config to drop vault_id so the bridge-config warning fires.
	noID := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\nembedding_model: test-model\n---\n"
	if err := os.WriteFile(filepath.Join(local, "_config.md"), []byte(noID), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", "--bridges", "--dir", local}); code != 0 {
		t.Fatalf("verify --bridges no-id exit code = %d, want 0", code)
	}
}

// ---------------------------------------------------------------------------
// namespace error / print branches
// ---------------------------------------------------------------------------

func TestNamespaceErrorPaths(t *testing.T) {
	vault := initTestVault(t)
	cases := [][]string{
		{"namespace", "create", "--dir", vault},            // missing name
		{"namespace", "update", "--dir", vault},            // missing name
		{"namespace", "update", "--dir", vault, "_bad"},    // invalid name
		{"namespace", "update", "--dir", vault, "ghost"},   // nonexistent manifest
		{"namespace", "remove", "--dir", vault},            // missing name
		{"namespace", "remove", "--dir", vault, "default"}, // refuses default
		{"namespace", "remove", "--dir", vault, "_bad"},    // invalid name
		{"namespace", "remove", "--dir", vault, "ghost"},   // manifest not present
	}
	for _, args := range cases {
		if code := run(args); code != 1 {
			t.Fatalf("run(%v) exit code = %d, want 1", args, code)
		}
	}
}

func TestNamespaceListPlainWithRootPath(t *testing.T) {
	vault := initTestVault(t)
	if code := run([]string{"namespace", "create", "team-x", "--dir", vault, "--root-path", "../team-x"}); code != 0 {
		t.Fatalf("namespace create exit code = %d", code)
	}
	writeTestNode(t, vault, "team-x/overview", "team-x")
	// Plain (non-JSON) listing exercises the root-path print branch.
	if out, code := captureRun([]string{"namespace", "list", "--dir", vault}); code != 0 || !strings.Contains(out, "root=") {
		t.Fatalf("namespace list plain = %q code=%d", out, code)
	}
}

// ---------------------------------------------------------------------------
// warren error / print branches
// ---------------------------------------------------------------------------

func TestWarrenProjectErrorPaths(t *testing.T) {
	root := t.TempDir()
	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "wp"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	cases := [][]string{
		{"warren", "project", "add", "--warren-dir", root},                    // missing project id
		{"warren", "project", "add", "a", "b", "--warren-dir", root},          // too many args
		{"warren", "project", "remove", "--warren-dir", root},                 // missing arg
		{"warren", "project", "remove", "ghost", "--warren-dir", root},        // nonexistent
		{"warren", "project", "rename", "--warren-dir", root},                 // wrong arg count
		{"warren", "project", "rename", "a", "b", "c", "--warren-dir", root},  // too many
		{"warren", "project", "rename", "ghost", "new", "--warren-dir", root}, // nonexistent
		{"warren", "project", "import", "--warren-dir", root},                 // missing args
		{"warren", "project", "list", "extra", "--warren-dir", root},          // unexpected positional
	}
	for _, args := range cases {
		if code := run(args); code != 1 {
			t.Fatalf("run(%v) exit code = %d, want 1", args, code)
		}
	}
}

func TestWarrenBridgeErrorPaths(t *testing.T) {
	root := t.TempDir()
	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "wp"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	cases := [][]string{
		{"warren", "bridge", "add", "--warren-dir", root},              // wrong arg count
		{"warren", "bridge", "add", "a", "b", "--warren-dir", root},    // projects not registered
		{"warren", "bridge", "list", "extra", "--warren-dir", root},    // unexpected positional
		{"warren", "bridge", "remove", "--warren-dir", root},           // wrong arg count
		{"warren", "bridge", "remove", "a", "b", "--warren-dir", root}, // nonexistent
	}
	for _, args := range cases {
		if code := run(args); code != 1 {
			t.Fatalf("run(%v) exit code = %d, want 1", args, code)
		}
	}
}

func TestWarrenDoctorNonJSONReportsIssues(t *testing.T) {
	// A Warren whose projects lack embedding DBs reports (warning) issues; the
	// non-JSON branch prints them and still exits 0.
	root := testWarrenRoot(t, "wp", "project-a", "project-b")
	if code := run([]string{"warren", "doctor", "--warren-dir", root}); code != 0 {
		t.Fatalf("warren doctor exit code = %d, want 0", code)
	}
}

func TestWarrenFormatAndRegisterErrors(t *testing.T) {
	cases := [][]string{
		{"warren", "format", "--warren-dir", filepath.Join(t.TempDir(), "no-manifest")}, // no manifest
		{"warren", "format", "extra", "--warren-dir", t.TempDir()},                      // unexpected positional
		{"warren", "register", "--dir", filepath.Join(t.TempDir(), ".marmot")},          // wrong arg count
		{"warren", "init", "--warren-dir", t.TempDir(), "id-a", "id-b"},                 // too many args
	}
	for _, args := range cases {
		if code := run(args); code != 1 {
			t.Fatalf("run(%v) exit code = %d, want 1", args, code)
		}
	}
}

// TestWarrenLegacyFlagSpellingsRejected: the U2-era compat spellings
// (--root, --aliases, and --id outside warren init) are gone; each now
// errors as an unknown flag, and canonical spellings keep working silently.
func TestWarrenLegacyFlagSpellingsRejected(t *testing.T) {
	root := t.TempDir()
	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "wp"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	cases := []struct {
		args    []string
		removed string
	}{
		{[]string{"warren", "init", "--root", t.TempDir(), "--id", "wq"}, "-root"},
		{[]string{"warren", "project", "add", "billing", "--root", root}, "-root"},
		{[]string{"warren", "project", "add", "billing", "--warren-dir", root, "--aliases", "pay,bill"}, "-aliases"},
		{[]string{"warren", "project", "add", "--id", "ledger", "--warren-dir", root}, "-id"},
		{[]string{"warren", "project", "import", "--id", "imported", "src", "--warren-dir", root}, "-id"},
		{[]string{"warren", "burrow", "--warren", "wp", "--materialize", "billing"}, "-materialize"},
	}
	for _, tc := range cases {
		_, stderr, code := captureRunBoth(t, tc.args)
		if code != 1 {
			t.Errorf("run(%v) exit code = %d, want 1 (unknown flag)", tc.args, code)
		}
		if !strings.Contains(stderr, "flag provided but not defined: "+tc.removed) {
			t.Errorf("run(%v) stderr = %q, want unknown-flag error for %s", tc.args, stderr, tc.removed)
		}
	}
	// A refused legacy invocation must not have registered anything.
	out, code := captureRun([]string{"warren", "project", "list", "--warren-dir", root, "--json"})
	if code != 0 || strings.Contains(out, "billing") || strings.Contains(out, "ledger") {
		t.Fatalf("refused legacy invocation still registered a project: %s", out)
	}

	// Canonical spellings keep working silently.
	_, stderr, code := captureRunBoth(t, []string{"warren", "project", "add", "reports", "--warren-dir", root, "--alias", "rep"})
	if code != 0 {
		t.Fatalf("canonical project add exit code = %d stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "deprecated") || strings.Contains(stderr, "not defined") {
		t.Fatalf("canonical spellings errored or warned: %q", stderr)
	}
}

// TestWarrenUsageShowsOnlyCanonicalSpellings (U2): usage lines must never
// advertise a deprecated spelling (--root, --aliases, or --id outside
// warren init, where --id is the canonical form).
func TestWarrenUsageShowsOnlyCanonicalSpellings(t *testing.T) {
	data, err := os.ReadFile("warren.go")
	if err != nil {
		t.Fatalf("read warren.go: %v", err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "usage: marmot warren") {
			continue
		}
		if strings.Contains(line, "--root") || strings.Contains(line, "--aliases") {
			t.Errorf("warren.go:%d usage line advertises a deprecated spelling: %s", i+1, strings.TrimSpace(line))
		}
		if strings.Contains(line, "--id") && !strings.Contains(line, "warren init") {
			t.Errorf("warren.go:%d usage line advertises --id outside warren init: %s", i+1, strings.TrimSpace(line))
		}
	}
}

func TestWarrenInitPositionalID(t *testing.T) {
	// Positional id form; interspersed flags are reordered, so the id may
	// come before or after the flags.
	if code := run([]string{"warren", "init", "--warren-dir", t.TempDir(), "myrepo"}); code != 0 {
		t.Fatalf("warren init positional exit code = %d, want 0", code)
	}
	if code := run([]string{"warren", "init", "myrepo", "--warren-dir", t.TempDir()}); code != 0 {
		t.Fatalf("warren init positional-first exit code = %d, want 0", code)
	}
	// A stray positional alongside --id is an error, not silently ignored.
	if code := run([]string{"warren", "init", "--warren-dir", t.TempDir(), "--id", "myrepo", "stray"}); code != 1 {
		t.Fatalf("warren init --id with stray positional exit code = %d, want 1", code)
	}
}

func TestWarrenInterspersedFlagsAfterPositionals(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	// Two projects: project-b gets burrowed (positional-before-flags
	// coverage), project-a stays a plain mount so it can become editable —
	// editable + materialized is refused since the A4 correctness fix.
	warrenRoot := testWarrenRoot(t, "wp", "project-a", "project-b")
	if code := run([]string{"warren", "register", "wp", warrenRoot, "--dir", marmotDir}); code != 0 {
		t.Fatalf("warren register positional-first exit code = %d, want 0", code)
	}
	if code := run([]string{"warren", "burrow", "project-b", "--dir", marmotDir, "--warren", "wp"}); code != 0 {
		t.Fatalf("warren burrow positional-first exit code = %d, want 0", code)
	}
	if code := run([]string{"warren", "edit", "project-a", "--warren", "wp", "--dir", marmotDir}); code != 0 {
		t.Fatalf("warren edit positional-first exit code = %d, want 0", code)
	}
	if code := run([]string{"warren", "edit", "project-a", "--off", "--warren", "wp", "--dir", marmotDir}); code != 0 {
		t.Fatalf("warren edit --off positional-first exit code = %d, want 0", code)
	}
	// Editable on the burrowed project must now refuse.
	if code := run([]string{"warren", "edit", "project-b", "--warren", "wp", "--dir", marmotDir}); code != 1 {
		t.Fatalf("warren edit on burrowed project exit code = %d, want 1 (editable+materialized refusal)", code)
	}
}

func TestWarrenMountAndEditErrors(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	cases := [][]string{
		{"warren", "mount", "--dir", marmotDir},                             // missing --warren
		{"warren", "mount", "--dir", marmotDir, "--warren", "ghost"},        // unregistered warren
		{"warren", "edit", "--dir", marmotDir},                              // missing warren + project
		{"warren", "edit", "--dir", marmotDir, "--warren", "ghost", "proj"}, // unregistered warren
		{"warren", "status", "--dir", marmotDir, "--warren", "ghost"},       // unregistered warren
	}
	for _, args := range cases {
		if code := run(args); code != 1 {
			t.Fatalf("run(%v) exit code = %d, want 1", args, code)
		}
	}
}

func TestWarrenStatusJSON(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenRoot := testWarrenRoot(t, "wp", "project-a")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatalf("register exit code = %d", code)
	}
	if code := run([]string{"warren", "status", "--dir", marmotDir, "--warren", "wp", "--json"}); code != 0 {
		t.Fatalf("warren status --json exit code = %d, want 0", code)
	}
}

// ---------------------------------------------------------------------------
// index pipeline: second run finds everything up to date
// ---------------------------------------------------------------------------

func TestIndexUpToDateSecondRun(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "node_a", "default")
	writeTestNode(t, vault, "node_b", "default")
	if code := run([]string{"index", "--dir", vault}); code != 0 {
		t.Fatalf("first index exit code = %d", code)
	}
	// Second index without --force: all nodes unchanged.
	if out, code := captureRun([]string{"index", "--dir", vault}); code != 0 || !strings.Contains(out, "up to date") {
		t.Fatalf("second index = %q code=%d", out, code)
	}
}

// ---------------------------------------------------------------------------
// static index with a classifier configured but no API key
// ---------------------------------------------------------------------------

func TestStaticIndexClassifierNoKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	root := t.TempDir()
	vault := filepath.Join(root, ".marmot")
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(vault, ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "---\nversion: \"1\"\nnamespace: default\nembedding_provider: mock\nembedding_model: test-model\nclassifier_provider: openai\n---\n"
	if err := os.WriteFile(filepath.Join(vault, "_config.md"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc Hello() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"index", src, "--dir", vault}); code != 0 {
		t.Fatalf("static index exit code = %d, want 0", code)
	}
	// Bool flags after the positional source path are reordered too.
	if code := run([]string{"index", src, "--incremental", "--dir", vault}); code != 0 {
		t.Fatalf("static index trailing --incremental exit code = %d, want 0", code)
	}
}

// ---------------------------------------------------------------------------
// verify with staleness and bridges together
// ---------------------------------------------------------------------------

func TestVerifyStalenessAndBridges(t *testing.T) {
	vault := filepath.Join(t.TempDir(), ".marmot")
	writeVaultWithID(t, vault, "local-vault")
	writeTestNode(t, vault, "node_a", "default")
	if code := run([]string{"verify", "--staleness", "--bridges", "--dir", vault}); code != 0 {
		t.Fatalf("verify --staleness --bridges exit code = %d, want 0", code)
	}
}

// ---------------------------------------------------------------------------
// warren mount with no project args mounts every project in the manifest
// ---------------------------------------------------------------------------

func TestWarrenMountAllProjects(t *testing.T) {
	workspace := t.TempDir()
	marmotDir := filepath.Join(workspace, ".marmot")
	warrenRoot := testWarrenRoot(t, "wp", "project-a", "project-b")
	if code := run([]string{"warren", "register", "--dir", marmotDir, "wp", warrenRoot}); code != 0 {
		t.Fatalf("register exit code = %d", code)
	}
	// Bare zero-arg mount refuses (C3): nothing becomes queryable by accident.
	if code := run([]string{"warren", "mount", "--dir", marmotDir, "--warren", "wp"}); code != 1 {
		t.Fatalf("warren bare mount exit code = %d, want 1", code)
	}
	// Explicit --all expands to every manifest project.
	if code := run([]string{"warren", "mount", "--dir", marmotDir, "--warren", "wp", "--all"}); code != 0 {
		t.Fatalf("warren mount --all exit code = %d, want 0", code)
	}
}

// ---------------------------------------------------------------------------
// generatedProjectID resolves an existing project's metadata ID
// ---------------------------------------------------------------------------

func TestWarrenProjectAddGenerateIDFromMetadata(t *testing.T) {
	root := t.TempDir()
	if code := run([]string{"warren", "init", "--warren-dir", root, "--id", "wp"}); code != 0 {
		t.Fatalf("init exit code = %d", code)
	}
	// Pre-seed metadata at the target path so generatedProjectID reads its ID.
	marmotDir := filepath.Join(root, "projects", "seeded", ".marmot")
	if err := os.MkdirAll(marmotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marmotDir, "_warren.md"),
		[]byte("---\nproject_id: seeded-svc\nwarren_id: wp\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"warren", "project", "add", "--generate-id", "--warren-dir", root, "--path", "projects/seeded/.marmot"}); code != 0 {
		t.Fatalf("project add --generate-id (metadata) exit code = %d", code)
	}
}

// ---------------------------------------------------------------------------
// status / buildEngine with namespaces, heat map and a generated summary
// present (covers the richer reporting and wiring branches)
// ---------------------------------------------------------------------------

func TestStatusAndQueryWithNamespaceHeatSummary(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "svc/a", "default")
	writeTestNode(t, vault, "svc/b", "default")
	if code := run([]string{"namespace", "create", "svc", "--dir", vault}); code != 0 {
		t.Fatalf("namespace create exit code = %d", code)
	}

	// Seed a heat map with a co-access pair.
	hm := heatmap.New("default")
	hm.RecordCoAccess([]string{"svc/a", "svc/b"}, 0.5)
	if err := heatmap.Save(vault, hm); err != nil {
		t.Fatalf("heatmap.Save: %v", err)
	}

	// Seed a generated summary for the default namespace.
	if err := summary.WriteSummary(vault, "default", &summary.SummaryResult{
		Namespace:   "default",
		Content:     "Overview of the default namespace.",
		NodeCount:   2,
		GeneratedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("summary.WriteSummary: %v", err)
	}

	if out, code := captureRun([]string{"status", "--dir", vault}); code != 0 || !strings.Contains(out, "Heat map:") || !strings.Contains(out, "Summary: generated") {
		t.Fatalf("status output missing heat/summary: %q code=%d", out, code)
	}

	// A query wires the engine with the namespace manager and heat map.
	if code := run([]string{"query", "--dir", vault, "--query", "svc"}); code != 0 {
		t.Fatalf("query exit code = %d, want 0", code)
	}
}

// ---------------------------------------------------------------------------
// summarize core with an injected mock summarizer (offline; no LLM keys)
// ---------------------------------------------------------------------------

func TestSummarizeWithProvider(t *testing.T) {
	vault := initTestVault(t)
	writeTestNode(t, vault, "svc/a", "default")
	writeTestNode(t, vault, "svc/b", "default")

	mock := &llm.MockProvider{SummaryResult: "A concise summary of svc."}
	if err := summarizeWithProvider(vault, "default", mock); err != nil {
		t.Fatalf("summarizeWithProvider: %v", err)
	}
	if mock.GetSummarizeCalls() != 1 {
		t.Fatalf("expected 1 summarize call, got %d", mock.GetSummarizeCalls())
	}
	// The summary file should now exist and status should report it.
	if _, err := summary.ReadSummary(vault, "default"); err != nil {
		t.Fatalf("ReadSummary after generate: %v", err)
	}
}

func TestSummarizeWithProviderNoNodes(t *testing.T) {
	vault := initTestVault(t)
	mock := &llm.MockProvider{}
	if err := summarizeWithProvider(vault, "default", mock); err != nil {
		t.Fatalf("summarizeWithProvider (no nodes): %v", err)
	}
}

// ---------------------------------------------------------------------------
// small helper unit tests
// ---------------------------------------------------------------------------

func TestToolResultTextEdgeCases(t *testing.T) {
	if got := toolResultText(nil); got != "" {
		t.Fatalf("toolResultText(nil) = %q, want empty", got)
	}
	if got := toolResultText(&mcp.CallToolResult{}); got != "" {
		t.Fatalf("toolResultText(empty) = %q, want empty", got)
	}
}

func TestTruncateHashForDisplay(t *testing.T) {
	if got := truncateHashForDisplay("short"); got != "short" {
		t.Fatalf("truncateHashForDisplay(short) = %q", got)
	}
	if got := truncateHashForDisplay("0123456789abcdef"); got != "01234567" {
		t.Fatalf("truncateHashForDisplay(long) = %q, want 01234567", got)
	}
}

// TestRuntimeBridgeKeyOrdering and TestEmptyNamespaceManager moved to
// internal/mcp/warren_reload_test.go with their subjects (B2 extraction).
