package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/mcp"
)

// newTestEngine builds a real MCP engine on a hermetic temp vault with a
// mock embedder (pattern from internal/mcp/server_test.go testEngine).
func newTestEngine(t *testing.T) *mcp.Engine {
	t.Helper()
	eng, err := mcp.NewEngine(t.TempDir(), embedding.NewMockEmbedder("test-model"))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// testOwnerServer is an in-process stand-in for the elected owner: a unix
// listener serving each connection with a real mcp server over
// ListenStdio(ctx, conn, conn) — exactly what the owner's accept loop does.
type testOwnerServer struct {
	ln     net.Listener
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	conns []net.Conn
}

func startOwnerServer(t *testing.T, socketPath string, eng *mcp.Engine) *testOwnerServer {
	t.Helper()
	srv := mcp.NewServer(eng)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen %q: %v", socketPath, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &testOwnerServer{ln: ln, cancel: cancel}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.mu.Unlock()
			s.wg.Add(1)
			go func(c net.Conn) {
				defer s.wg.Done()
				defer func() { _ = c.Close() }()
				_ = srv.ListenStdio(ctx, c, c)
			}(c)
		}
	}()
	t.Cleanup(s.stop)
	return s
}

// stop kills the owner abruptly: sessions end, conns close, listener goes
// away (unlinking the socket file). Safe to call more than once.
func (s *testOwnerServer) stop() {
	s.cancel()
	_ = s.ln.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// rpcMsg is the minimal shape needed to assert on relayed JSON-RPC lines.
type rpcMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// proxyClient drives RunProxy in-process over io.Pipe pairs, playing the MCP
// client's role with raw newline-delimited JSON-RPC lines (pattern from
// internal/mcp/transport_test.go and the e2e harness).
type proxyClient struct {
	t          *testing.T
	stdin      *io.PipeWriter
	lines      chan string
	done       chan error
	transcript []string // every line the client received, in order
}

func startProxyClient(t *testing.T, socketPath string) *proxyClient {
	t.Helper()
	return startProxyClientFunc(t, func(stdin io.Reader, stdout io.Writer) error {
		return RunProxy(stdin, stdout, socketPath)
	})
}

// startProxyClientFunc is startProxyClient with a pluggable server side, so
// tests can drive the persistent-session election-loop shape (ClientSession +
// repeated RunProxySession, possibly ending in owner promotion) with the same
// client helpers.
func startProxyClientFunc(t *testing.T, run func(stdin io.Reader, stdout io.Writer) error) *proxyClient {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	pc := &proxyClient{
		t:     t,
		stdin: stdinW,
		lines: make(chan string, 64),
		done:  make(chan error, 1),
	}
	go func() {
		err := run(stdinR, stdoutW)
		_ = stdoutW.Close()
		pc.done <- err
	}()
	go func() {
		defer close(pc.lines)
		sc := newLineScanner(stdoutR)
		for sc.Scan() {
			pc.lines <- sc.Text()
		}
	}()
	t.Cleanup(func() {
		_ = stdinW.Close()
		_ = stdoutR.Close()
	})
	return pc
}

func (pc *proxyClient) send(line string) {
	pc.t.Helper()
	if _, err := io.WriteString(pc.stdin, line+"\n"); err != nil {
		pc.t.Fatalf("send: %v", err)
	}
}

// recv returns the next line from the proxy's stdout, recording it in the
// transcript, or fails the test after a generous deadline.
func (pc *proxyClient) recv() rpcMsg {
	pc.t.Helper()
	select {
	case line, ok := <-pc.lines:
		if !ok {
			pc.t.Fatal("proxy stdout closed while awaiting a line")
		}
		pc.transcript = append(pc.transcript, line)
		var msg rpcMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			pc.t.Fatalf("non-JSON line from proxy: %v\nraw: %s", err, line)
		}
		return msg
	case <-time.After(10 * time.Second):
		pc.t.Fatal("timeout waiting for proxy output")
	}
	return rpcMsg{}
}

// drainTranscript records any lines that arrive within d — used to catch a
// duplicate initialize response that would otherwise sneak in after the last
// asserted reply.
func (pc *proxyClient) drainTranscript(d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case line, ok := <-pc.lines:
			if !ok {
				return
			}
			pc.transcript = append(pc.transcript, line)
		case <-deadline:
			return
		}
	}
}

// countResponsesTo counts transcript lines that are responses (no method)
// with the given numeric id.
func (pc *proxyClient) countResponsesTo(id int) int {
	n := 0
	for _, line := range pc.transcript {
		if isResponseTo([]byte(line), json.RawMessage(fmt.Sprintf("%d", id))) {
			n++
		}
	}
	return n
}

const initLine = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"proxy-test","version":"0"}}}`

// handshake performs the MCP initialize exchange through the proxy.
func (pc *proxyClient) handshake() {
	pc.t.Helper()
	pc.send(initLine)
	resp := pc.recv()
	if resp.Error != nil {
		pc.t.Fatalf("initialize error: %s", resp.Error)
	}
	if string(resp.ID) != "0" {
		pc.t.Fatalf("initialize response id = %s, want 0", resp.ID)
	}
	pc.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
}

// call issues a tools/call and asserts the reply matches the request id with
// no JSON-RPC error.
func (pc *proxyClient) call(id int, tool, argsJSON string) rpcMsg {
	pc.t.Helper()
	pc.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, tool, argsJSON))
	resp := pc.recv()
	if resp.Error != nil {
		pc.t.Fatalf("tools/call %s error: %s", tool, resp.Error)
	}
	if string(resp.ID) != fmt.Sprintf("%d", id) {
		pc.t.Fatalf("tools/call response id = %s, want %d (unexpected interleaved line?)", resp.ID, id)
	}
	return resp
}

// waitDone waits for RunProxy to return and hands back its error.
func (pc *proxyClient) waitDone(timeout time.Duration) error {
	pc.t.Helper()
	select {
	case err := <-pc.done:
		return err
	case <-time.After(timeout):
		pc.t.Fatal("RunProxy did not return in time")
		return nil
	}
}

func TestProxyRoundTrip(t *testing.T) {
	eng := newTestEngine(t)
	sock := SocketPath(t.TempDir())
	startOwnerServer(t, sock, eng)

	pc := startProxyClient(t, sock)
	pc.handshake()

	resp := pc.call(1, "context_write",
		`{"id":"proxy/roundtrip","type":"concept","summary":"written through the relay","context":"package proxy"}`)
	if !strings.Contains(string(resp.Result), "proxy/roundtrip") {
		t.Errorf("context_write result missing node id:\n%s", resp.Result)
	}

	// Stdin EOF ends the session cleanly.
	_ = pc.stdin.Close()
	if err := pc.waitDone(5 * time.Second); err != nil {
		t.Fatalf("RunProxy = %v, want nil on stdin EOF", err)
	}
}

func TestProxyReconnectReplaysHandshake(t *testing.T) {
	eng := newTestEngine(t)
	sock := SocketPath(t.TempDir())
	srv1 := startOwnerServer(t, sock, eng)

	pc := startProxyClient(t, sock)
	pc.handshake()
	pc.call(1, "context_write",
		`{"id":"proxy/before-drop","type":"concept","summary":"written before the failover","context":"a"}`)

	// Kill the owner mid-session and bring up a replacement on the same
	// socket path (a new elected serve process in production).
	srv1.stop()
	startOwnerServer(t, sock, eng)

	// The next call must succeed without the client re-initializing: the
	// proxy reconnects and replays the recorded handshake itself.
	resp := pc.call(2, "context_write",
		`{"id":"proxy/after-drop","type":"concept","summary":"written after the failover","context":"b"}`)
	if !strings.Contains(string(resp.Result), "proxy/after-drop") {
		t.Errorf("post-failover write result missing node id:\n%s", resp.Result)
	}

	// Exactly one initialize response reached the client: the replayed
	// duplicate (matched by id) was suppressed. Drain briefly to catch a
	// late-arriving duplicate before counting.
	pc.drainTranscript(200 * time.Millisecond)
	if n := pc.countResponsesTo(0); n != 1 {
		t.Fatalf("client saw %d initialize responses, want exactly 1\ntranscript:\n%s",
			n, strings.Join(pc.transcript, "\n"))
	}
}

func TestProxyStdinEOFReturnsNil(t *testing.T) {
	eng := newTestEngine(t)
	sock := SocketPath(t.TempDir())
	startOwnerServer(t, sock, eng)

	pc := startProxyClient(t, sock)
	pc.handshake()

	_ = pc.stdin.Close()
	if err := pc.waitDone(5 * time.Second); err != nil {
		t.Fatalf("RunProxy = %v, want nil on stdin EOF", err)
	}
}

func TestProxyLargeLineSurvivesRelay(t *testing.T) {
	eng := newTestEngine(t)
	sock := SocketPath(t.TempDir())
	startOwnerServer(t, sock, eng)

	pc := startProxyClient(t, sock)
	pc.handshake()

	// A ~1MB tool-call line: far past bufio.Scanner's 64KB default, proving
	// the relay's buffer sizing in the stdin→conn direction.
	big := strings.Repeat("x", 1<<20)
	resp := pc.call(1, "context_write", fmt.Sprintf(
		`{"id":"proxy/big","type":"concept","summary":"large payload","context":%q}`, big))
	if !strings.Contains(string(resp.Result), "proxy/big") {
		t.Errorf("large write result missing node id:\n%s", resp.Result)
	}
}

func TestProxyNoResumeDegradedMode(t *testing.T) {
	t.Setenv("MARMOT_PROXY_NO_RESUME", "1")

	eng := newTestEngine(t)
	sock := SocketPath(t.TempDir())
	srv := startOwnerServer(t, sock, eng)

	pc := startProxyClient(t, sock)
	pc.handshake()

	// Owner death with resume disabled: no replay, ErrNoResume so the caller
	// exits nonzero and the MCP client restarts serve. It must NOT match
	// ErrOwnerGone, or the election loop would silently re-elect and serve
	// the already-initialized client over a fresh session.
	srv.stop()
	err := pc.waitDone(5 * time.Second)
	if !errors.Is(err, ErrNoResume) {
		t.Fatalf("RunProxy = %v, want ErrNoResume", err)
	}
	if errors.Is(err, ErrOwnerGone) {
		t.Fatalf("ErrNoResume must not match ErrOwnerGone (would re-elect): %v", err)
	}
}

func TestProxyDialFailureReturnsOwnerGone(t *testing.T) {
	sock := SocketPath(t.TempDir()) // nothing listening
	r, w := io.Pipe()
	defer func() { _ = r.Close(); _ = w.Close() }()
	err := RunProxy(r, io.Discard, sock)
	if !errors.Is(err, ErrOwnerGone) {
		t.Fatalf("RunProxy = %v, want ErrOwnerGone", err)
	}
	// The never-attached case is distinguishable so the election loop can
	// bound its retries against a stale info.json / wedged owner.
	if !errors.Is(err, ErrNoOwner) {
		t.Fatalf("RunProxy = %v, want ErrNoOwner on initial dial failure", err)
	}
}

func TestProxyOwnerGoneAfterRedialWindow(t *testing.T) {
	eng := newTestEngine(t)
	sock := SocketPath(t.TempDir())
	srv := startOwnerServer(t, sock, eng)

	pc := startProxyClient(t, sock)
	pc.handshake()

	// Owner dies and no replacement ever appears: after the redial window
	// the proxy hands control back to the election loop.
	srv.stop()
	err := pc.waitDone(10 * time.Second)
	if !errors.Is(err, ErrOwnerGone) {
		t.Fatalf("RunProxy = %v, want ErrOwnerGone", err)
	}
	// We did attach to a live owner, so this is progress, not a
	// never-attached dial failure — the loop's wedge bound must reset.
	if errors.Is(err, ErrNoOwner) {
		t.Fatalf("RunProxy = %v; post-attach owner death must not be ErrNoOwner", err)
	}
}

// TestClientSessionReelectionReplaysHandshake drives the production election-
// loop shape: one ClientSession, repeated RunProxySession calls. The owner
// dies with no replacement inside the redial window (RunProxySession returns
// to the "loop"), the client sends its next call while NO owner exists, and
// only then does a brand-new owner appear. The line must wait in the session
// (never be stolen or dropped), the re-entered proxy must replay the
// handshake to the new owner, and the client must see exactly one initialize
// response.
func TestClientSessionReelectionReplaysHandshake(t *testing.T) {
	eng := newTestEngine(t)
	sock := SocketPath(t.TempDir())
	srv1 := startOwnerServer(t, sock, eng)

	pc := startProxyClientFunc(t, func(stdin io.Reader, stdout io.Writer) error {
		cs := NewClientSession(stdin)
		for {
			err := RunProxySession(cs, stdout, sock)
			if errors.Is(err, ErrOwnerGone) {
				// Production re-enters the election here (TryAcquire); this
				// process always loses and proxies again.
				time.Sleep(20 * time.Millisecond)
				continue
			}
			return err
		}
	})
	pc.handshake()
	pc.call(1, "context_write",
		`{"id":"proxy/session-before","type":"concept","summary":"written before the ownerless gap","context":"a"}`)

	// Kill the owner and outwait the redial window so RunProxySession
	// actually returns ErrOwnerGone and the loop re-enters.
	srv1.stop()
	time.Sleep(redialAttempts*redialBackoff + 300*time.Millisecond)

	// Send the next call into the ownerless gap, then bring up the new owner.
	pc.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"context_write","arguments":{"id":"proxy/session-after","type":"concept","summary":"written across re-election","context":"b"}}}`)
	startOwnerServer(t, sock, eng)

	resp := pc.recv()
	if resp.Error != nil {
		t.Fatalf("post-reelection call error: %s", resp.Error)
	}
	if string(resp.ID) != "2" {
		t.Fatalf("post-reelection response id = %s, want 2", resp.ID)
	}
	if !strings.Contains(string(resp.Result), "proxy/session-after") {
		t.Errorf("post-reelection write result missing node id:\n%s", resp.Result)
	}

	// Exactly one initialize response: the re-entry replay's duplicate was
	// suppressed.
	pc.drainTranscript(200 * time.Millisecond)
	if n := pc.countResponsesTo(0); n != 1 {
		t.Fatalf("client saw %d initialize responses, want exactly 1\ntranscript:\n%s",
			n, strings.Join(pc.transcript, "\n"))
	}
}

// TestClientSessionOwnerPromotionNoLineLost pins the proxy→owner role switch:
// the owner dies, the client's next line races the dying proxy (it may be
// read and left undelivered), the redial window expires, and the process
// "wins the election" — serving the SAME ClientSession with an in-process
// MCP server, exactly as runServeOwner does. The raced line must be answered
// by the promoted owner, not swallowed by a leftover proxy stdin reader.
func TestClientSessionOwnerPromotionNoLineLost(t *testing.T) {
	eng := newTestEngine(t)
	sock := SocketPath(t.TempDir())
	srv1 := startOwnerServer(t, sock, eng)

	pc := startProxyClientFunc(t, func(stdin io.Reader, stdout io.Writer) error {
		cs := NewClientSession(stdin)
		if err := RunProxySession(cs, stdout, sock); !errors.Is(err, ErrOwnerGone) {
			return fmt.Errorf("proxy phase = %v, want ErrOwnerGone", err)
		}
		// Promotion: same process, same session, engine served directly.
		return mcp.NewServer(eng).ListenStdio(context.Background(), cs, stdout)
	})
	pc.handshake()
	pc.call(1, "context_write",
		`{"id":"proxy/promote-before","type":"concept","summary":"written before promotion","context":"a"}`)

	// Owner dies; the very next client line races the dying proxy.
	srv1.stop()
	pc.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"context_query","arguments":{"query":"written before promotion","budget":2000}}}`)

	// The promoted owner must answer that exact line (recv outlasts the
	// ~1s redial window).
	resp := pc.recv()
	if resp.Error != nil {
		t.Fatalf("post-promotion call error: %s", resp.Error)
	}
	if string(resp.ID) != "2" {
		t.Fatalf("post-promotion response id = %s, want 2 (line lost across promotion?)", resp.ID)
	}
}
