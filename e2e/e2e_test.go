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
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
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

// mcpSession drives a `marmot serve` process over newline-delimited JSON-RPC.
type mcpSession struct {
	t     *testing.T
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
}

func startMCP(t *testing.T, proj string) *mcpSession {
	t.Helper()
	cmd := exec.Command(binPath, "serve", "--dir", ".marmot")
	cmd.Dir = proj
	cmd.Env = hermeticEnv(proj)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
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

	s := &mcpSession{t: t, cmd: cmd, stdin: stdin, lines: lines}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	s.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}`)
	if resp := s.recv(0); resp.Error != nil {
		t.Fatalf("initialize error: %s", resp.Error)
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return s
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
	timeout := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				s.t.Fatalf("server closed stream waiting for id %d", id)
			}
			var resp rpcResponse
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				continue // skip notifications/log lines
			}
			if n, ok := resp.ID.(float64); ok && int(n) == id {
				return resp
			}
		case <-timeout:
			s.t.Fatalf("timeout waiting for response id %d", id)
		}
	}
}

// callTool invokes an MCP tool and returns the text content of the result.
func (s *mcpSession) callTool(id int, tool string, args map[string]any) string {
	s.t.Helper()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		s.t.Fatal(err)
	}
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, tool, argsJSON))
	resp := s.recv(id)
	if resp.Error != nil {
		s.t.Fatalf("tool %s rpc error: %s", tool, resp.Error)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		s.t.Fatalf("tool %s: bad result: %v", tool, err)
	}
	if result.IsError {
		s.t.Fatalf("tool %s returned error: %+v", tool, result.Content)
	}
	if len(result.Content) == 0 {
		s.t.Fatalf("tool %s returned no content", tool)
	}
	return result.Content[0].Text
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

func TestUIServer(t *testing.T) {
	proj := seedProject(t)
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

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
	var ready bool
	for i := 0; i < 150; i++ {
		resp, err := client.Get(base + "/api/version")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatal("ui server did not become ready within 30s")
	}

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
}
