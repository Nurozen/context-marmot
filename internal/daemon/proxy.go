package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"sync"
	"time"
)

const (
	// proxyMaxLine bounds a single JSON-RPC line through the relay. The
	// bufio.Scanner default of 64KB would truncate big context_write
	// payloads; 10MB gives ample headroom.
	proxyMaxLine = 10 * 1024 * 1024
	// proxyInitialBuf is the scanner's starting buffer; it grows on demand
	// up to proxyMaxLine.
	proxyInitialBuf = 64 * 1024
	// redialAttempts × redialBackoff is the window a proxy waits for a
	// replacement owner to win the flock and listen after the old owner
	// dies. Past the window the proxy returns ErrOwnerGone and the caller
	// re-enters the election itself.
	redialAttempts = 20
	redialBackoff  = 50 * time.Millisecond
	// drainTimeout bounds how long the proxy relays in-flight responses to
	// stdout after the client closes stdin.
	drainTimeout = 2 * time.Second
)

// session records the MCP handshake as it flows through the relay so it can
// be replayed verbatim against a replacement owner after a failover. Fields
// are written by the stdin→conn side and read by the conn→stdout pump, so
// all access goes through the mutex.
type session struct {
	mu           sync.Mutex
	initLine     []byte          // raw `initialize` request line, verbatim
	initID       json.RawMessage // its JSON-RPC id, for duplicate-response filtering
	initedLine   []byte          // raw `notifications/initialized` line, verbatim
	initRespSeen bool            // the client already received the initialize response
}

// record captures the handshake lines from the client→owner direction.
// Non-handshake lines (and non-JSON noise) pass through untouched.
func (s *session) record(line []byte) {
	var msg struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return
	}
	switch msg.Method {
	case "initialize":
		s.mu.Lock()
		s.initLine = append([]byte(nil), line...)
		s.initID = append(json.RawMessage(nil), msg.ID...)
		s.initRespSeen = false
		s.mu.Unlock()
	case "notifications/initialized":
		s.mu.Lock()
		s.initedLine = append([]byte(nil), line...)
		s.mu.Unlock()
	}
}

// markIfInitResponse notes when the initialize response reaches the client.
// Only a response the client has actually seen is a duplicate when replayed —
// if the owner died before answering, the replayed response is the first
// delivery and must not be suppressed.
func (s *session) markIfInitResponse(line []byte) {
	s.mu.Lock()
	id, seen := s.initID, s.initRespSeen
	s.mu.Unlock()
	if seen || id == nil || !isResponseTo(line, id) {
		return
	}
	s.mu.Lock()
	s.initRespSeen = true
	s.mu.Unlock()
}

// handshake returns a consistent snapshot of the recorded handshake.
func (s *session) handshake() (initLine []byte, initID json.RawMessage, initedLine []byte, respSeen bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initLine, s.initID, s.initedLine, s.initRespSeen
}

// pumpResult is the single value a conn→stdout pump delivers when it stops.
// A nil fatal means the connection side ended (owner EOF/reset or a read
// deadline) — survivable via reconnect; a non-nil fatal (stdout write
// failure, oversized line) ends the proxy.
type pumpResult struct {
	fatal error
}

// ClientSession owns the client half of a daemon-mode serve process for the
// process's whole lifetime. Exactly one goroutine ever reads the underlying
// stdin: proxy re-entries after an owner death and promotion to owner all
// consume the same scanned-line stream, so a defeated proxy can never leave
// a competing stdin reader behind to steal (and silently drop) a client
// line. It also carries the recorded MCP handshake across re-entries, so a
// proxy attaching to a brand-new owner replays initialize/initialized
// exactly like an in-flight reconnect does.
//
// Consumer-side state (carry/buf) is only ever touched by the single active
// consumer — the current RunProxySession call, then possibly the promoted
// owner — which the sequential election loop guarantees.
type ClientSession struct {
	ch      chan []byte // scanned stdin lines; closed on EOF or scan error
	scanErr error       // scanner error; valid only once ch is closed
	sess    session     // recorded MCP handshake, survives role switches

	carry    []byte // line a proxy read but never delivered to any owner
	hasCarry bool
	buf      []byte // partially-Read line (owner-promotion io.Reader path)
}

// NewClientSession starts the process's single stdin scanner goroutine and
// returns the session handle the election loop threads through every role it
// takes. The goroutine exits when stdin does; while the process is between
// consumers (re-electing), a scanned line simply waits in the channel — it
// is never dropped.
func NewClientSession(stdin io.Reader) *ClientSession {
	cs := &ClientSession{ch: make(chan []byte)}
	go func() {
		sc := newLineScanner(stdin)
		for sc.Scan() {
			cs.ch <- append([]byte(nil), sc.Bytes()...)
		}
		cs.scanErr = sc.Err() // nil on clean EOF; published by the close below
		close(cs.ch)
	}()
	return cs
}

// takeCarry pops the carried-over line, if any.
func (cs *ClientSession) takeCarry() ([]byte, bool) {
	line, ok := cs.carry, cs.hasCarry
	cs.carry, cs.hasCarry = nil, false
	return line, ok
}

// setCarry saves a client line that was read but never delivered to a live
// owner, so the next consumer delivers it instead of dropping it.
func (cs *ClientSession) setCarry(line []byte) {
	cs.carry, cs.hasCarry = line, true
}

// Read serves the client's bytes to the promoted owner's stdio MCP server,
// reassembling scanned lines (trailing newline restored) so the newline-
// delimited JSON-RPC framing crosses intact. A carried-over line — one a
// defeated proxy read but never delivered — is served first.
func (cs *ClientSession) Read(p []byte) (int, error) {
	if len(cs.buf) == 0 {
		line, ok := cs.takeCarry()
		if !ok {
			line, ok = <-cs.ch
			if !ok {
				if cs.scanErr != nil {
					return 0, cs.scanErr
				}
				return 0, io.EOF
			}
		}
		cs.buf = append(line, '\n')
	}
	n := copy(p, cs.buf)
	cs.buf = cs.buf[n:]
	return n, nil
}

// RunProxy relays a newline-delimited JSON-RPC MCP session between the
// client (stdin/stdout) and the vault owner's unix socket. It wraps
// RunProxySession for one-shot use; the election loop in cmd/marmot builds
// one ClientSession per process and calls RunProxySession directly so stdin
// has exactly one reader across re-entries and owner promotion.
//
// It takes io.Reader/io.Writer rather than os.Stdin/os.Stdout so tests can
// drive a real client ↔ proxy ↔ owner chain in-process over io.Pipe.
func RunProxy(stdin io.Reader, stdout io.Writer, socketPath string) error {
	return RunProxySession(NewClientSession(stdin), stdout, socketPath)
}

// RunProxySession relays cs's MCP session to the owner's unix socket. It is
// a byte-preserving line relay, not a protocol translator: each line crosses
// unchanged. The session records the initialize handshake so that when the
// owner dies mid-session the proxy can reconnect to the replacement owner —
// or, on a later RunProxySession call, attach to a brand-new one — and
// replay the handshake, suppressing the duplicate initialize response;
// failover stays invisible to the MCP client.
//
// Returns: nil on client EOF (after half-closing the socket and draining
// in-flight responses for drainTimeout); ErrNoOwner if the initial dial
// fails (never attached — the election loop bounds its retries on this);
// ErrOwnerGone if an attached owner died and no replacement appeared within
// the redial window (re-elect); ErrNoResume if the owner died and
// MARMOT_PROXY_NO_RESUME=1 disables resumption (exit nonzero, the MCP client
// restarts serve); any other error is a fatal relay failure.
func RunProxySession(cs *ClientSession, stdout io.Writer, socketPath string) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return ErrNoOwner // stale info.json / owner mid-death: never attached
	}
	defer func() { _ = conn.Close() }()
	fmt.Fprintf(os.Stderr, "ContextMarmot MCP proxy → %s\n", socketPath)

	noResume := os.Getenv("MARMOT_PROXY_NO_RESUME") == "1"
	sess := &cs.sess
	pending, hasPending := cs.takeCarry()
	var pump <-chan pumpResult

	// attach replays the recorded handshake on conn — a no-op for a fresh
	// client, the failover replay for a reconnect or a re-entered proxy
	// facing a brand-new owner (verbatim: same-binary owners return
	// identical capabilities, so the client's view stays consistent) — then
	// re-delivers the pending line and starts the conn→stdout pump.
	attach := func() error {
		initLine, initID, initedLine, respSeen := sess.handshake()
		var suppress json.RawMessage
		if initLine != nil {
			if _, err := writeLine(conn, initLine); err != nil {
				return err
			}
			if respSeen {
				suppress = initID // client already has this response: drop the duplicate
			}
			if hasPending && bytes.Equal(pending, initLine) {
				hasPending = false // the replay itself was the first delivery
			}
			if initedLine != nil {
				if _, err := writeLine(conn, initedLine); err != nil {
					return err
				}
				if hasPending && bytes.Equal(pending, initedLine) {
					hasPending = false
				}
			}
		}
		if hasPending {
			if _, err := writeLine(conn, pending); err != nil {
				return err
			}
			hasPending = false
		}
		pump = startPump(conn, stdout, sess, suppress)
		return nil
	}

	// reconnect handles an owner death mid-session: consume the old pump's
	// result if it is still unconsumed, dial the replacement, and re-attach.
	reconnect := func(pumpAlive bool) error {
		_ = conn.Close()
		if pumpAlive {
			if res := <-pump; res.fatal != nil {
				return res.fatal
			}
		}
		if noResume {
			return ErrNoResume // degraded mode: exit nonzero, client restarts serve
		}
		nc, err := redialOwner(socketPath)
		if err != nil {
			return ErrOwnerGone
		}
		conn = nc
		if err := attach(); err != nil {
			_ = conn.Close()
			return ErrOwnerGone
		}
		return nil
	}

	// fail saves an undelivered client line back into the session before
	// returning, so the next consumer (a later proxy, or the owner this
	// process is about to become) delivers it instead of dropping it.
	fail := func(err error) error {
		if hasPending {
			cs.setCarry(pending)
			hasPending = false
		}
		return err
	}

	if err := attach(); err != nil {
		// The owner died under the initial handshake replay. The dial itself
		// succeeded, so this is a normal failover, not a never-attached dial
		// failure — take the reconnect path (no pump was started yet).
		if rerr := reconnect(false); rerr != nil {
			return fail(rerr)
		}
	}

	for {
		select {
		case line, ok := <-cs.ch:
			if !ok {
				// Stdin EOF: the client is done. Half-close so the owner can
				// finish in-flight responses, drain them to stdout for up to
				// drainTimeout, then return clean.
				if err := cs.scanErr; err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				if uc, ok := conn.(*net.UnixConn); ok {
					_ = uc.CloseWrite()
				}
				_ = conn.SetReadDeadline(time.Now().Add(drainTimeout))
				if res := <-pump; res.fatal != nil {
					return res.fatal
				}
				return nil
			}
			sess.record(line)
			if _, err := writeLine(conn, line); err != nil {
				// Owner died under the write; the line never arrived, so
				// carry it across the reconnect.
				pending, hasPending = line, true
				if err := reconnect(true); err != nil {
					return fail(err)
				}
			}
		case res := <-pump:
			if res.fatal != nil {
				return res.fatal
			}
			// Owner closed (or reset) the connection mid-session.
			if err := reconnect(false); err != nil {
				return fail(err)
			}
		}
	}
}

// startPump copies newline-delimited lines from conn to stdout on its own
// goroutine. suppressID, when non-nil, drops the first response line whose id
// matches — the duplicate initialize response produced by a handshake replay.
// The result channel is buffered and receives exactly one value when the
// pump stops.
func startPump(conn net.Conn, stdout io.Writer, sess *session, suppressID json.RawMessage) <-chan pumpResult {
	ch := make(chan pumpResult, 1)
	go func() {
		sc := newLineScanner(conn)
		for sc.Scan() {
			line := sc.Bytes()
			if suppressID != nil && isResponseTo(line, suppressID) {
				suppressID = nil // exactly one duplicate is dropped
				continue
			}
			sess.markIfInitResponse(line)
			if _, err := writeLine(stdout, line); err != nil {
				ch <- pumpResult{fatal: fmt.Errorf("write stdout: %w", err)}
				return
			}
		}
		if err := sc.Err(); err != nil && errors.Is(err, bufio.ErrTooLong) {
			ch <- pumpResult{fatal: fmt.Errorf("owner sent a line exceeding %d bytes: %w", proxyMaxLine, err)}
			return
		}
		// EOF, reset, or read deadline: the connection side is done.
		ch <- pumpResult{}
	}()
	return ch
}

// redialOwner retries the dial briefly: after an owner death the replacement
// (elected by another serve process) needs a moment to win the flock and
// start listening on the same path.
func redialOwner(socketPath string) (net.Conn, error) {
	var lastErr error
	for i := 0; i < redialAttempts; i++ {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(redialBackoff)
	}
	return nil, lastErr
}

// newLineScanner returns a line scanner sized for large JSON-RPC payloads.
func newLineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, proxyInitialBuf), proxyMaxLine)
	return sc
}

// writeLine writes line plus the trailing newline as a single Write call,
// preserving the relay's one-JSON-object-per-line framing byte-for-byte.
func writeLine(w io.Writer, line []byte) (int, error) {
	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	return w.Write(buf)
}

// isResponseTo reports whether line is a JSON-RPC response (no method) whose
// id equals id. Ids are compared by JSON value, not raw bytes, so formatting
// differences between client and owner cannot defeat the match.
func isResponseTo(line []byte, id json.RawMessage) bool {
	var msg struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return false
	}
	if msg.Method != "" || len(msg.ID) == 0 {
		return false // request or notification, not a response
	}
	var a, b any
	if json.Unmarshal(msg.ID, &a) != nil || json.Unmarshal(id, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}
