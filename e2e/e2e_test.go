//go:build e2e

// Package e2e exercises the marmot binary end to end: CLI flows, the MCP
// server over stdio JSON-RPC, and the embedded web UI over HTTP. Run with:
//
//	make e2e
//
// Tests copy the static fixture in e2e/fixture/ into a temp directory, so
// the fixture is never mutated and no real vault or API key is touched.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
	// MARMOT_E2E_BIN points the harness at a prebuilt marmot binary instead
	// of building the working tree. Used for red-first baseline verification
	// (running the contention tests against a pre-fix commit's binary).
	if pre := os.Getenv("MARMOT_E2E_BIN"); pre != "" {
		binPath = pre
		os.Exit(m.Run())
	}

	tmp, err := os.MkdirTemp("", "marmot-e2e-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: temp dir: %v\n", err)
		os.Exit(1)
	}
	binPath = filepath.Join(tmp, "marmot")

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: repo root: %v\n", err)
		os.Exit(1)
	}
	build := exec.Command("go", "build", "-o", binPath, "./cmd/marmot")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build marmot: %v\n%s", err, out)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// seedProject copies the static fixture into a temp project directory with a
// .marmot vault and a src/ tree, then indexes it with the mock embedder.
func seedProject(t *testing.T) string {
	t.Helper()
	proj := t.TempDir()
	copyDir(t, "fixture/vault", filepath.Join(proj, ".marmot"))
	copyDir(t, "fixture/src", filepath.Join(proj, "src"))
	// The embedding store expects its data directory to exist (marmot init
	// normally creates it).
	if err := os.MkdirAll(filepath.Join(proj, ".marmot", ".marmot-data"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(proj, "index", "--dir", ".marmot")
	if err != nil {
		t.Fatalf("seed index: %v\n%s", err, out)
	}
	return proj
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
}

// hermeticEnv returns the process environment with HOME pointed at the
// project dir so spawned marmot processes never read the developer's real
// ~/.marmot state (e.g. routes.yml vault registrations).
func hermeticEnv(dir string) []string {
	return append(os.Environ(), "HOME="+dir)
}

func runCLI(dir string, args ...string) (string, error) {
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = hermeticEnv(dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// --- CLI surface flow ---

func TestCLIFlow(t *testing.T) {
	proj := seedProject(t)

	t.Run("version", func(t *testing.T) {
		out, err := runCLI(proj, "version")
		if err != nil {
			t.Fatalf("version: %v\n%s", err, out)
		}
		if !strings.Contains(out, "marmot") {
			t.Errorf("version output missing binary name: %q", out)
		}
	})

	t.Run("status", func(t *testing.T) {
		out, err := runCLI(proj, "status", "--dir", ".marmot")
		if err != nil {
			t.Fatalf("status: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Nodes: 4 total") {
			t.Errorf("expected 4 nodes in status, got:\n%s", out)
		}
		if !strings.Contains(out, "Embeddings: 4") {
			t.Errorf("expected 4 embeddings in status, got:\n%s", out)
		}
	})

	t.Run("query", func(t *testing.T) {
		out, err := runCLI(proj, "query", "--dir", ".marmot", "--query", "user login credentials session token")
		if err != nil {
			t.Fatalf("query: %v\n%s", err, out)
		}
		if !strings.Contains(out, "auth/login") {
			t.Errorf("query result missing auth/login:\n%s", out)
		}
	})

	t.Run("verify", func(t *testing.T) {
		out, err := runCLI(proj, "verify", "--dir", ".marmot")
		if err != nil {
			t.Fatalf("verify: %v\n%s", err, out)
		}
		if !strings.Contains(out, "No issues found") {
			t.Errorf("expected clean verify, got:\n%s", out)
		}
	})

	t.Run("sdk", func(t *testing.T) {
		out, err := runCLI(proj, "sdk", "--out", "marmot-sdk.ts")
		if err != nil {
			t.Fatalf("sdk: %v\n%s", err, out)
		}
		data, err := os.ReadFile(filepath.Join(proj, "marmot-sdk.ts"))
		if err != nil {
			t.Fatalf("sdk output not written: %v", err)
		}
		if !strings.Contains(string(data), "context_query") {
			t.Error("generated SDK missing context_query tool")
		}
	})

	// Last: mutates the fixture source file, so no vault-dependent subtests
	// may follow.
	t.Run("verify_detects_staleness", func(t *testing.T) {
		srcFile := filepath.Join(proj, "src", "auth.go")
		if err := os.WriteFile(srcFile, []byte("package auth\n\nfunc Login() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, _ := runCLI(proj, "verify", "--dir", ".marmot")
		if !strings.Contains(out, "auth/login") {
			t.Errorf("expected staleness finding for auth/login, got:\n%s", out)
		}
	})
}

// --- MCP server over stdio JSON-RPC ---

type rpcResponse struct {
	ID     any             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// syncBuffer is a goroutine-safe buffer for capturing a child process's
// stderr while the test inspects it concurrently.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// mcpSession drives a `marmot serve` process over newline-delimited JSON-RPC.
type mcpSession struct {
	t         *testing.T
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	lines     chan string
	stderr    *syncBuffer
	closeOnce sync.Once
	closeErr  error
}

func startMCP(t *testing.T, proj string) *mcpSession {
	t.Helper()
	return startMCPEnv(t, proj, hermeticEnv(proj))
}

// startMCPDaemon spawns `marmot serve` with the WS2 single-owner daemon
// enabled (dark-launch gate: MARMOT_DAEMON=1 in the child env). The first
// such serve per vault wins the flock and owns the engine; later ones relay
// their stdio session to the owner over the vault's unix socket.
func startMCPDaemon(t *testing.T, proj string) *mcpSession {
	t.Helper()
	return startMCPEnv(t, proj, append(hermeticEnv(proj), "MARMOT_DAEMON=1"))
}

func startMCPEnv(t *testing.T, proj string, env []string) *mcpSession {
	t.Helper()
	cmd := exec.Command(binPath, "serve", "--dir", ".marmot")
	cmd.Dir = proj
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)

	// Pump stdout on a goroutine so recv can time out instead of blocking
	// forever on a wedged server.
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	s := &mcpSession{t: t, cmd: cmd, stdin: stdin, lines: lines, stderr: stderr}
	t.Cleanup(func() { _ = s.closeAndWait(5 * time.Second) })

	s.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}`)
	if resp := s.recv(0); resp.Error != nil {
		t.Fatalf("initialize error: %s", resp.Error)
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return s
}

// closeAndWait closes stdin (the MCP client's EOF) and waits for the serve
// process to exit within budget, killing it on overrun. Safe to call more
// than once; later calls return the first call's result. Registered as the
// session's t.Cleanup, so tests that call it explicitly can assert on the
// shutdown budget without double-Waiting the process.
func (s *mcpSession) closeAndWait(budget time.Duration) error {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()
		done := make(chan error, 1)
		go func() { done <- s.cmd.Wait() }()
		select {
		case err := <-done:
			s.closeErr = err
		case <-time.After(budget):
			_ = s.cmd.Process.Kill()
			<-done
			s.closeErr = fmt.Errorf("serve did not exit within %v of stdin EOF (killed)", budget)
		}
	})
	return s.closeErr
}

func (s *mcpSession) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		s.t.Fatalf("send: %v", err)
	}
}

// recv reads responses until it sees the given id, or times out.
func (s *mcpSession) recv(id int) rpcResponse {
	s.t.Helper()
	resp, err := s.recvErr(id, 30*time.Second)
	if err != nil {
		s.t.Fatal(err)
	}
	return resp
}

// recvErr reads responses until it sees the given id or the timeout elapses.
// Unlike recv it never fails the test, so it is safe off the test goroutine.
func (s *mcpSession) recvErr(id int, timeout time.Duration) (rpcResponse, error) {
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				return rpcResponse{}, fmt.Errorf("server closed stream waiting for id %d", id)
			}
			var resp rpcResponse
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				continue // skip notifications/log lines
			}
			if n, ok := resp.ID.(float64); ok && int(n) == id {
				return resp, nil
			}
		case <-deadline:
			return rpcResponse{}, fmt.Errorf("timeout after %v waiting for response id %d", timeout, id)
		}
	}
}

// callTool invokes an MCP tool and returns the text content of the result.
func (s *mcpSession) callTool(id int, tool string, args map[string]any) string {
	s.t.Helper()
	text, err := s.callToolErr(id, tool, args, 30*time.Second)
	if err != nil {
		s.t.Fatal(err)
	}
	return text
}

// callToolErr invokes an MCP tool with a per-call deadline and returns an
// error instead of failing the test, so concurrent load loops running in
// goroutines can collect failures (t.Fatal is only legal on the test
// goroutine).
func (s *mcpSession) callToolErr(id int, tool string, args map[string]any, timeout time.Duration) (string, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("tool %s: marshal args: %w", tool, err)
	}
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, tool, argsJSON)
	if _, err := io.WriteString(s.stdin, req+"\n"); err != nil {
		return "", fmt.Errorf("tool %s: send: %w", tool, err)
	}
	resp, err := s.recvErr(id, timeout)
	if err != nil {
		return "", fmt.Errorf("tool %s: %w", tool, err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("tool %s rpc error: %s", tool, resp.Error)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("tool %s: bad result: %w", tool, err)
	}
	if result.IsError {
		return "", fmt.Errorf("tool %s returned error: %+v", tool, result.Content)
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("tool %s returned no content", tool)
	}
	return result.Content[0].Text, nil
}

func TestMCPServer(t *testing.T) {
	proj := seedProject(t)
	s := startMCP(t, proj)

	// tools/list must expose the full tool surface.
	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	listResp := s.recv(1)
	for _, tool := range []string{"context_query", "context_write", "context_tag", "context_verify", "context_delete", "context_namespace"} {
		if !strings.Contains(string(listResp.Result), tool) {
			t.Errorf("tools/list missing %s", tool)
		}
	}

	// Write a node linked to an existing one.
	writeOut := s.callTool(2, "context_write", map[string]any{
		"id":      "auth/logout",
		"type":    "function",
		"summary": "Logout revokes the active session token for the current user.",
		"edges":   []map[string]any{{"target": "auth/login", "relation": "references"}},
	})
	if !strings.Contains(writeOut, `"status":"created"`) {
		t.Fatalf("write: expected created, got %s", writeOut)
	}

	// Query should surface the new node.
	queryOut := s.callTool(3, "context_query", map[string]any{
		"query":  "revoke session token logout",
		"budget": 4000,
	})
	if !strings.Contains(queryOut, "auth/logout") {
		t.Errorf("query missing auth/logout:\n%s", queryOut)
	}

	// Scoped verify of the new node must be clean even though its edge
	// targets a node outside the scoped set (regression for the
	// scoped-verify dangling-edge bug).
	verifyOut := s.callTool(4, "context_verify", map[string]any{
		"node_ids": []string{"auth/logout"},
		"check":    "all",
	})
	if !strings.Contains(verifyOut, `"total":0`) {
		t.Errorf("scoped verify expected 0 issues, got %s", verifyOut)
	}

	// Tag it via semantic search.
	tagOut := s.callTool(5, "context_tag", map[string]any{
		"query": "logout revoke session",
		"tag":   "e2e",
		"limit": 1,
	})
	if !strings.Contains(tagOut, `"count":1`) {
		t.Errorf("tag expected count 1, got %s", tagOut)
	}

	// Soft-delete and confirm exclusion from queries.
	delOut := s.callTool(6, "context_delete", map[string]any{"id": "auth/logout"})
	if !strings.Contains(delOut, `"status":"superseded"`) {
		t.Errorf("delete expected superseded, got %s", delOut)
	}
	requery := s.callTool(7, "context_query", map[string]any{
		"query":  "revoke session token logout",
		"budget": 4000,
	})
	if strings.Contains(requery, `id="auth/logout"`) {
		t.Errorf("superseded node still returned by query:\n%s", requery)
	}
}

// --- Multi-process contention (canonical freeze regressions) ---

// lockErr is the SQLite failure both contention tests must never observe in
// tool responses or process stderr.
const lockErr = "database is locked"

// TestConcurrentServes pins reproduced failure mode 2 (a reader's SHARED lock
// parks a writer's COMMIT on the PENDING lock; from then on all reads in all
// processes wedge until the parked process dies): serve A runs a tight
// context_query loop (long SearchActive scans holding SHARED locks) while
// serve B runs a context_write burst (COMMITs) against the same vault,
// sustained for ~10s. Every call must complete within a 5s per-call deadline,
// nothing may report "database is locked", and both processes must exit
// within the post-EOF shutdown budget.
//
// Under WS1 (the default, daemon off) each process keeps its own in-memory
// graph, so only tool-call success is asserted here. The daemon-mode variant
// TestConcurrentServesDaemon tightens this to cross-process read-your-writes
// through the shared owner engine.
func TestConcurrentServes(t *testing.T) {
	proj := seedProject(t)
	a := startMCP(t, proj)
	b := startMCP(t, proj)

	const (
		sustain = 10 * time.Second
		perCall = 5 * time.Second
	)
	deadline := time.Now().Add(sustain)

	type loadResult struct {
		calls int
		errs  []string
	}
	// run drives one call per iteration until the sustain deadline, stopping
	// on the first failure (a wedged server would only repeat the timeout).
	run := func(call func(id, i int) (string, error), baseID int) chan loadResult {
		out := make(chan loadResult, 1)
		go func() {
			var r loadResult
			for i := 0; time.Now().Before(deadline); i++ {
				r.calls++
				text, err := call(baseID+i, i)
				if err == nil && strings.Contains(text, lockErr) {
					err = fmt.Errorf("response contains %q: %s", lockErr, text)
				}
				if err != nil {
					r.errs = append(r.errs, err.Error())
					break
				}
			}
			out <- r
		}()
		return out
	}

	queries := run(func(id, _ int) (string, error) {
		return a.callToolErr(id, "context_query", map[string]any{
			"query":  "user login credentials session token",
			"budget": 4000,
		}, perCall)
	}, 1000)
	writes := run(func(id, i int) (string, error) {
		return b.callToolErr(id, "context_write", map[string]any{
			"id":      fmt.Sprintf("e2e/burst-%d", i),
			"type":    "concept",
			"summary": fmt.Sprintf("Concurrent write burst node %d exercising cross-process SQLite commits under sustained reads.", i),
		}, perCall)
	}, 1_000_000)

	qr, wr := <-queries, <-writes
	t.Logf("sustained %v: %d queries (A), %d writes (B)", sustain, qr.calls, wr.calls)
	for _, e := range qr.errs {
		t.Errorf("serve A query failed: %s", e)
	}
	for _, e := range wr.errs {
		t.Errorf("serve B write failed: %s", e)
	}
	if qr.calls < 2 || wr.calls < 2 {
		t.Errorf("load loops barely ran (queries=%d writes=%d); contention window too small", qr.calls, wr.calls)
	}

	// Both processes must exit within the post-EOF budget: a COMMIT parked on
	// the PENDING lock keeps the process alive well past it.
	if err := a.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("serve A shutdown: %v", err)
	}
	if err := b.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("serve B shutdown: %v", err)
	}
	if es := a.stderr.String(); strings.Contains(es, lockErr) {
		t.Errorf("serve A stderr contains %q:\n%s", lockErr, es)
	}
	if es := b.stderr.String(); strings.Contains(es, lockErr) {
		t.Errorf("serve B stderr contains %q:\n%s", lockErr, es)
	}
}

// TestIndexDuringServe pins reproduced failure mode 1 (instant "database is
// locked" on a concurrent write from a second process): serve A issues
// context_write calls in a loop while `marmot index --dir .marmot` re-embeds
// a pre-seeded batch of nodes into the same embeddings.db. The index run must
// exit 0 AND report no swallowed upsert errors — the index pipeline prints
// "warning: upsert ..." and keeps going (cmd/marmot/pipeline.go, mirroring
// internal/indexer/runner.go's silent RunResult.Errors), so the exit code
// alone proves nothing. All serve writes must succeed and a post-index
// context_query must still return results.
func TestIndexDuringServe(t *testing.T) {
	proj := seedProject(t)

	// Seed node files that have no embeddings yet so the index run performs a
	// sustained upsert burst; the fixture's four nodes alone would finish
	// before any contention window opens.
	vault := filepath.Join(proj, ".marmot")
	const bulkNodes = 300
	for i := 0; i < bulkNodes; i++ {
		id := fmt.Sprintf("bulk/node-%03d", i)
		body := fmt.Sprintf("---\nid: %s\ntype: concept\nnamespace: default\nstatus: active\n---\n\nBulk fixture node %d giving marmot index a sustained upsert workload.\n", id, i)
		path := filepath.Join(vault, "bulk", fmt.Sprintf("node-%03d.md", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s := startMCP(t, proj)

	type indexResult struct {
		out string
		err error
	}
	indexDone := make(chan indexResult, 1)
	go func() {
		out, err := runCLI(proj, "index", "--dir", ".marmot")
		indexDone <- indexResult{out: out, err: err}
	}()

	// Write through serve until the index run completes.
	var writeErrs []string
	writesOK := 0
	var res indexResult
writeLoop:
	for i := 0; ; i++ {
		select {
		case res = <-indexDone:
			break writeLoop
		default:
		}
		_, err := s.callToolErr(100+i, "context_write", map[string]any{
			"id":      fmt.Sprintf("e2e/during-index-%d", i),
			"type":    "concept",
			"summary": fmt.Sprintf("Write %d issued while marmot index runs against the same vault.", i),
		}, 5*time.Second)
		if err != nil {
			writeErrs = append(writeErrs, err.Error())
			// The write side failed; still collect the index result (bounded)
			// so its output can be reported.
			select {
			case res = <-indexDone:
			case <-time.After(60 * time.Second):
				t.Fatal("index run still not finished 60s after a serve write failure")
			}
			break writeLoop
		}
		writesOK++
	}
	t.Logf("index-during-serve: %d serve writes completed; index output: %s", writesOK, strings.TrimSpace(res.out))

	if res.err != nil {
		t.Errorf("index during serve failed: %v\n%s", res.err, res.out)
	}
	// Exit code 0 is not enough: upsert failures are swallowed as warnings.
	for _, needle := range []string{"warning:", lockErr} {
		if strings.Contains(res.out, needle) {
			t.Errorf("index output contains %q (swallowed error):\n%s", needle, res.out)
		}
	}
	if res.err == nil && !strings.Contains(res.out, "Indexed ") {
		t.Errorf("index run did no work (want an \"Indexed N/M\" line):\n%s", res.out)
	}
	for _, e := range writeErrs {
		t.Errorf("serve write during index failed: %s", e)
	}

	// The vault must still answer queries after the index run.
	queryOut, err := s.callToolErr(1_000_000, "context_query", map[string]any{
		"query":  "user login credentials session token",
		"budget": 4000,
	}, 10*time.Second)
	if err != nil {
		t.Errorf("post-index query: %v", err)
	} else if !strings.Contains(queryOut, "auth/login") {
		t.Errorf("post-index query missing auth/login:\n%s", queryOut)
	}

	if err := s.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("serve shutdown: %v", err)
	}
	if es := s.stderr.String(); strings.Contains(es, lockErr) {
		t.Errorf("serve stderr contains %q:\n%s", lockErr, es)
	}
}

// --- Single-owner daemon (WS2 dark launch, MARMOT_DAEMON=1) ---
//
// All daemon tests set MARMOT_DAEMON=1 in the child env (startMCPDaemon); the
// default serve path stays byte-for-byte standalone and is covered by the
// tests above. Per the plan's CI-flake guidance these tests assert on end
// state (responses received, lock free, socket/info removed), never on
// durations beyond the harness's existing 5s post-EOF shutdown budget.

// daemonInfo mirrors the fields of daemon.info.json the tests need. The file
// is published by the owner after its socket is listening and removed on
// graceful shutdown.
type daemonInfo struct {
	PID    int    `json:"pid"`
	Socket string `json:"socket"`
}

// readDaemonInfo polls <vault>/.marmot-data/daemon.info.json until it parses
// (the owner publishes it after winning the flock and listening) and returns
// it. Fails the test if none appears within 10s.
func readDaemonInfo(t *testing.T, proj string) daemonInfo {
	t.Helper()
	path := filepath.Join(proj, ".marmot", ".marmot-data", "daemon.info.json")
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var info daemonInfo
			if err := json.Unmarshal(data, &info); err == nil && info.PID != 0 && info.Socket != "" {
				return info
			}
			lastErr = fmt.Errorf("parse %s: %v (content %q)", path, err, data)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon.info.json never became readable: %v", lastErr)
	return daemonInfo{}
}

// splitOwnerProxy identifies which of two daemon-mode serve sessions holds
// the vault (pid published in daemon.info.json) and which relays to it.
func splitOwnerProxy(t *testing.T, proj string, a, b *mcpSession) (owner, proxy *mcpSession, info daemonInfo) {
	t.Helper()
	info = readDaemonInfo(t, proj)
	switch info.PID {
	case a.cmd.Process.Pid:
		return a, b, info
	case b.cmd.Process.Pid:
		return b, a, info
	default:
		t.Fatalf("daemon.info.json pid %d matches neither serve (%d, %d)", info.PID, a.cmd.Process.Pid, b.cmd.Process.Pid)
		return nil, nil, info
	}
}

// assertVaultReleased asserts the daemon end state after a full shutdown: no
// daemon.info.json, no socket file, and no flock held on daemon.lock (the
// test takes and releases the flock itself to prove it is free).
func assertVaultReleased(t *testing.T, proj, socket string) {
	t.Helper()
	dataDir := filepath.Join(proj, ".marmot", ".marmot-data")
	if _, err := os.Stat(filepath.Join(dataDir, "daemon.info.json")); !os.IsNotExist(err) {
		t.Errorf("daemon.info.json still present after shutdown (stat err: %v)", err)
	}
	if socket != "" {
		if _, err := os.Stat(socket); !os.IsNotExist(err) {
			t.Errorf("daemon socket %s still present after shutdown (stat err: %v)", socket, err)
		}
	}
	lockPath := filepath.Join(dataDir, "daemon.lock")
	f, err := os.Open(lockPath)
	if os.IsNotExist(err) {
		return // no lock file at all: trivially free
	}
	if err != nil {
		t.Fatalf("open daemon.lock: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("daemon.lock is still flocked after shutdown: %v", err)
	} else {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
}

// TestConcurrentServesDaemon is the daemon-mode tightening of
// TestConcurrentServes: with MARMOT_DAEMON=1 both serve processes execute on
// the single owner engine, so a node written via serve A must be visible to a
// context_query on serve B immediately (cross-process read-your-writes), and
// vice versa — no reindex, no watcher debounce, no per-process graph copies.
func TestConcurrentServesDaemon(t *testing.T) {
	proj := seedProject(t)
	a := startMCPDaemon(t, proj)
	b := startMCPDaemon(t, proj)
	owner, proxy, _ := splitOwnerProxy(t, proj, a, b)

	// Write via A → read via B.
	writeOut := a.callTool(10, "context_write", map[string]any{
		"id":      "e2e/daemon-rw-a",
		"type":    "concept",
		"summary": "Cross process read your writes marker node written through serve A of the daemon pair.",
	})
	if !strings.Contains(writeOut, `"status":"created"`) {
		t.Fatalf("write via A: expected created, got %s", writeOut)
	}
	queryOut := b.callTool(11, "context_query", map[string]any{
		"query":  "cross process read your writes marker serve A",
		"budget": 4000,
	})
	if !strings.Contains(queryOut, "e2e/daemon-rw-a") {
		t.Errorf("query via B does not see node written via A:\n%s", queryOut)
	}

	// And the reverse direction (covers both proxy→owner and owner-session
	// writes regardless of which process won the election).
	writeOut = b.callTool(12, "context_write", map[string]any{
		"id":      "e2e/daemon-rw-b",
		"type":    "concept",
		"summary": "Reverse direction marker node written through serve B of the daemon pair.",
	})
	if !strings.Contains(writeOut, `"status":"created"`) {
		t.Fatalf("write via B: expected created, got %s", writeOut)
	}
	queryOut = a.callTool(13, "context_query", map[string]any{
		"query":  "reverse direction marker node serve B",
		"budget": 4000,
	})
	if !strings.Contains(queryOut, "e2e/daemon-rw-b") {
		t.Errorf("query via A does not see node written via B:\n%s", queryOut)
	}

	// Orderly teardown: the proxy detaches first so the owner does not linger
	// past the harness's shutdown budget.
	if err := proxy.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("proxy shutdown: %v", err)
	}
	if err := owner.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("owner shutdown: %v", err)
	}
}

// waitDaemonOwner polls daemon.info.json until it names pid as the owner —
// used after a SIGKILL failover, where the dead owner's stale info file
// lingers (no cleanup ran) until the re-elected survivor republishes over it.
func waitDaemonOwner(t *testing.T, proj string, pid int) daemonInfo {
	t.Helper()
	path := filepath.Join(proj, ".marmot", ".marmot-data", "daemon.info.json")
	deadline := time.Now().Add(15 * time.Second)
	var last daemonInfo
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			var info daemonInfo
			if err := json.Unmarshal(data, &info); err == nil {
				last = info
				if info.PID == pid {
					return info
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon.info.json never named pid %d as owner (last seen: %+v)", pid, last)
	return daemonInfo{}
}

// TestOwnerFailover pins re-election: two daemon serves share one vault; the
// owner (pid from daemon.info.json) is SIGKILLed — the kernel drops its flock
// instantly and no cleanup runs — and the surviving serve must take over
// ownership and answer the next tool call. A third serve then joins the new
// owner and sees the post-failover write.
//
// The test waits for the survivor to republish daemon.info.json before
// sending the post-failover write, so the request cannot race into the dying
// owner's socket (the one loss mode the plan allows — an in-flight request at
// the moment of death is a normal client-side timeout). With that sequenced
// out, a SINGLE attempt must succeed: the process-wide ClientSession
// guarantees the survivor's defeated proxy leaves no stdin reader behind that
// could swallow the line across its promotion to owner.
func TestOwnerFailover(t *testing.T) {
	proj := seedProject(t)
	a := startMCPDaemon(t, proj)
	b := startMCPDaemon(t, proj)
	owner, survivor, _ := splitOwnerProxy(t, proj, a, b)

	if err := owner.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL owner: %v", err)
	}

	// The survivor must take over: daemon.info.json republished with its pid
	// (the survivor's proxy outwaits its ~1s redial window, re-enters the
	// election, wins the now-free flock, and listens).
	newInfo := waitDaemonOwner(t, proj, survivor.cmd.Process.Pid)

	// One attempt, no retry: nothing may swallow this line.
	writeOut, writeErr := survivor.callToolErr(2000, "context_write", map[string]any{
		"id":      "e2e/failover",
		"type":    "concept",
		"summary": "Failover marker node written by the surviving serve after the owner was killed.",
	}, 10*time.Second)
	if writeErr != nil {
		t.Fatalf("survivor write after owner SIGKILL: %v\nsurvivor stderr:\n%s", writeErr, survivor.stderr.String())
	}
	if !strings.Contains(writeOut, `"status":"created"`) {
		t.Fatalf("survivor write: expected created, got %s", writeOut)
	}

	// A third serve joins the new owner (its initialize handshake succeeding
	// proves the join) and sees the post-failover write through the shared
	// engine.
	c := startMCPDaemon(t, proj)
	queryOut := c.callTool(2100, "context_query", map[string]any{
		"query":  "failover marker surviving serve owner killed",
		"budget": 4000,
	})
	if !strings.Contains(queryOut, "e2e/failover") {
		t.Errorf("third serve does not see the post-failover write:\n%s", queryOut)
	}

	// Orderly teardown: proxy before owner (t.Cleanup would also run in this
	// order, but failing loudly here beats a silent kill in cleanup).
	if err := c.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("third serve shutdown: %v", err)
	}
	if err := survivor.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("survivor shutdown: %v", err)
	}
	assertVaultReleased(t, proj, newInfo.Socket)
}

// TestDaemonShutdownBudget pins the owner's linger-then-exit lifecycle: when
// the owner's own MCP client disconnects while a proxy is still attached, the
// owner lingers headless and keeps serving the proxy; when the last proxy
// disconnects, the owner fully exits within the harness's 5s budget and
// leaves the vault clean — no daemon.lock flock, no daemon.sock, no
// daemon.info.json.
func TestDaemonShutdownBudget(t *testing.T) {
	proj := seedProject(t)
	a := startMCPDaemon(t, proj)
	b := startMCPDaemon(t, proj)
	owner, proxy, info := splitOwnerProxy(t, proj, a, b)

	// Owner's client disconnects with the proxy attached → the owner must
	// linger: the proxy's session keeps getting answers.
	if err := owner.stdin.Close(); err != nil {
		t.Fatalf("close owner stdin: %v", err)
	}
	out, err := proxy.callToolErr(3000, "context_query", map[string]any{
		"query":  "user login credentials session token",
		"budget": 4000,
	}, 15*time.Second)
	if err != nil {
		t.Fatalf("proxy call after owner stdin EOF (owner should linger): %v", err)
	}
	if !strings.Contains(out, "auth/login") {
		t.Errorf("lingering owner returned wrong query result:\n%s", out)
	}

	// Proxy disconnects → last session ends → the owner exits within budget.
	if err := proxy.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("proxy shutdown: %v", err)
	}
	if err := owner.closeAndWait(5 * time.Second); err != nil {
		t.Errorf("owner did not exit after the last proxy detached: %v", err)
	}

	assertVaultReleased(t, proj, info.Socket)
}

// TestDaemonNoResumeProxyExits pins the degraded mode of plan 2.6/2.11: with
// MARMOT_PROXY_NO_RESUME=1 a proxy must NOT re-enter the election after its
// owner dies mid-session — no handshake replay means any resurrected session
// would be silently un-initialized — it exits nonzero on its own so the MCP
// client restarts `marmot serve` itself.
func TestDaemonNoResumeProxyExits(t *testing.T) {
	proj := seedProject(t)
	owner := startMCPDaemon(t, proj)
	// The first serve alone must be the owner before the proxy joins, so the
	// no-resume env var is guaranteed to land on the proxy side.
	if info := readDaemonInfo(t, proj); info.PID != owner.cmd.Process.Pid {
		t.Fatalf("first serve (pid %d) did not become owner (info pid %d)", owner.cmd.Process.Pid, info.PID)
	}
	proxy := startMCPEnv(t, proj, append(hermeticEnv(proj), "MARMOT_DAEMON=1", "MARMOT_PROXY_NO_RESUME=1"))

	if err := owner.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL owner: %v", err)
	}

	// The proxy exits by itself — stdin stays open — and nonzero.
	done := make(chan error, 1)
	go func() { done <- proxy.cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("no-resume proxy exit = %v, want a nonzero exit\nproxy stderr:\n%s", err, proxy.stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no-resume proxy did not exit after owner death (re-entered the election instead?)")
	}
}

// --- Embedded web UI over HTTP ---

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// startUI starts `marmot ui` (default flags, so the default loopback bind is
// what e2e exercises) for the project at proj, waits until the server answers
// on 127.0.0.1, and returns the base URL plus the bound port. The process is
// stopped via t.Cleanup.
func startUI(t *testing.T, proj string) (base string, port int) {
	t.Helper()
	port = freePort(t)
	base = fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := exec.Command(binPath, "ui", "--dir", ".marmot", "--port", fmt.Sprint(port), "--no-open")
	cmd.Dir = proj
	cmd.Env = hermeticEnv(proj)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ui: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGINT)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	// Wait for the server to come up. Generous timeout: e2e may run on a
	// heavily loaded machine (parallel CI jobs).
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 150; i++ {
		resp, err := client.Get(base + "/api/version")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base, port
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("ui server did not become ready within 30s")
	return "", 0
}

func TestUIServer(t *testing.T) {
	// Warren fixture (U5): the workspace registers a warren (nothing mounted)
	// so the warren_endpoints subtest can drive the management endpoints —
	// mount over POST, status flip, graph render, unmount, doctor — against a
	// real `marmot ui` process.
	warrenRoot, proj := seedWarren(t)
	if out, err := runCLI(proj, "warren", "register", "--dir", ".marmot", warrenID, warrenRoot); err != nil {
		t.Fatalf("warren register: %v\n%s", err, out)
	}
	base, _ := startUI(t, proj)
	client := &http.Client{Timeout: 2 * time.Second}

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return resp.StatusCode, string(body)
	}

	t.Run("index_html", func(t *testing.T) {
		code, body := get("/")
		if code != http.StatusOK {
			t.Fatalf("GET /: status %d", code)
		}
		if !strings.Contains(body, "ContextMarmot") || !strings.Contains(body, `id="app"`) {
			t.Errorf("index.html missing app shell")
		}

		// The bundled JS referenced by index.html must be served.
		m := regexp.MustCompile(`/assets/[^"]+\.js`).FindString(body)
		if m == "" {
			t.Fatal("no bundled JS reference in index.html")
		}
		jsCode, jsBody := get(m)
		if jsCode != http.StatusOK || len(jsBody) == 0 {
			t.Errorf("bundle %s: status %d, %d bytes", m, jsCode, len(jsBody))
		}
	})

	t.Run("graph_api", func(t *testing.T) {
		code, body := get("/api/graph/default")
		if code != http.StatusOK {
			t.Fatalf("graph: status %d", code)
		}
		for _, id := range []string{"auth", "auth/login", "auth/validate", "db/users"} {
			if !strings.Contains(body, `"`+id+`"`) {
				t.Errorf("graph missing node %s", id)
			}
		}
	})

	t.Run("namespaces_api", func(t *testing.T) {
		code, body := get("/api/namespaces")
		if code != http.StatusOK || !strings.Contains(body, "default") {
			t.Errorf("namespaces: status %d body %s", code, body)
		}
	})

	t.Run("search_api", func(t *testing.T) {
		code, body := get("/api/search?q=" + strings.ReplaceAll("user login credentials", " ", "+"))
		if code != http.StatusOK {
			t.Fatalf("search: status %d", code)
		}
		if !strings.Contains(body, "auth/login") {
			t.Errorf("search missing auth/login: %s", body)
		}
	})

	t.Run("node_api", func(t *testing.T) {
		code, body := get("/api/node/default/auth/login")
		if code != http.StatusOK || !strings.Contains(body, "session token") {
			t.Errorf("node: status %d body %s", code, body)
		}
	})

	// U5a: mount via POST → status shows active → graph serves the mounted
	// project's nodes → unmount → doctor endpoint shape.
	t.Run("warren_endpoints", func(t *testing.T) {
		post := func(path, body string) (int, string) {
			t.Helper()
			resp, err := client.Post(base+path, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatalf("POST %s: %v", path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			return resp.StatusCode, string(respBody)
		}
		projActive := func() bool {
			t.Helper()
			code, body := get("/api/warren/" + warrenID + "/status")
			if code != http.StatusOK {
				t.Fatalf("warren status = %d: %s", code, body)
			}
			var resp struct {
				Projects []struct {
					ProjectID string `json:"project_id"`
					Active    bool   `json:"active"`
					Available bool   `json:"available"`
				} `json:"projects"`
			}
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatalf("decode warren status: %v (%s)", err, body)
			}
			for _, p := range resp.Projects {
				if p.ProjectID == projA {
					if p.Active && !p.Available {
						t.Fatalf("mounted project unavailable: %+v", p)
					}
					return p.Active
				}
			}
			t.Fatalf("project %q missing from warren status: %s", projA, body)
			return false
		}

		if projActive() {
			t.Fatal("fixture must start with nothing mounted")
		}

		code, body := post("/api/warren/"+warrenID+"/mount", `{"projects":["`+projA+`"]}`)
		if code != http.StatusOK || !strings.Contains(body, `"action":"mounted"`) || !strings.Contains(body, `"status":"reloaded"`) {
			t.Fatalf("mount = %d: %s", code, body)
		}
		if !projActive() {
			t.Fatal("mounted project not active in status")
		}

		code, body = get("/api/warren/" + warrenID + "/graph")
		if code != http.StatusOK || !strings.Contains(body, "@"+projAVault+"/"+hotwalID) {
			t.Fatalf("warren graph = %d, missing @%s/%s: %s", code, projAVault, hotwalID, body)
		}

		code, body = post("/api/warren/"+warrenID+"/unmount", `{"projects":["`+projA+`"]}`)
		if code != http.StatusOK || !strings.Contains(body, `"action":"unmounted"`) {
			t.Fatalf("unmount = %d: %s", code, body)
		}
		if projActive() {
			t.Fatal("unmounted project still active in status")
		}

		code, body = get("/api/doctor/workspace")
		if code != http.StatusOK {
			t.Fatalf("doctor workspace = %d: %s", code, body)
		}
		var report struct {
			Issues []struct {
				Severity string `json:"severity"`
				Code     string `json:"code"`
				Message  string `json:"message"`
			} `json:"issues"`
		}
		if err := json.Unmarshal([]byte(body), &report); err != nil {
			t.Fatalf("decode doctor report: %v (%s)", err, body)
		}
		for _, issue := range report.Issues {
			if issue.Severity == "" || issue.Code == "" || issue.Message == "" {
				t.Errorf("doctor issue missing fields: %+v", issue)
			}
			if issue.Severity == "error" {
				t.Errorf("healthy workspace reported doctor error: %+v", issue)
			}
		}
	})
}

// TestUIBindsLoopbackByDefault (U5a prerequisite): the UI's API carries
// workspace-state mutation (POST /api/warren/{id}/mount|unmount), so
// `marmot ui` must bind 127.0.0.1 unless --host explicitly opts out —
// no other host on the network may reach the server by default.
func TestUIBindsLoopbackByDefault(t *testing.T) {
	proj := seedProject(t)
	_, port := startUI(t, proj) // startUI proves loopback answers

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("interface addrs: %v", err)
	}
	checked := 0
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		checked++
		hostPort := net.JoinHostPort(ipnet.IP.String(), strconv.Itoa(port))
		conn, dialErr := net.DialTimeout("tcp", hostPort, 2*time.Second)
		if dialErr == nil {
			_ = conn.Close()
			t.Errorf("UI accepted a connection on non-loopback %s — must bind loopback by default", hostPort)
		}
	}
	if checked == 0 {
		t.Log("no non-loopback IPv4 interface present; only loopback reachability was verified")
	}
}
