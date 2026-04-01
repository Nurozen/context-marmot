package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// pipeWriteCloser wraps an *io.PipeWriter to satisfy io.WriteCloser.
type pipeWriteCloser struct{ *io.PipeWriter }

func (p pipeWriteCloser) Close() error { return p.PipeWriter.Close() }

// pipeReadCloser wraps an *io.PipeReader to satisfy io.ReadCloser.
type pipeReadCloser struct{ *io.PipeReader }

func (p pipeReadCloser) Close() error { return p.PipeReader.Close() }

// setupTransport creates an Engine + MCP server backed by a temp dir and
// connects a real MCP SDK client to it via in-memory pipes (stdio transport).
// The returned client is already initialized and ready to call tools.
func setupTransport(t *testing.T) *client.Client {
	t.Helper()

	eng := testEngine(t)
	srv := NewServer(eng)

	// Two io.Pipe pairs form a bidirectional channel:
	//   client writes -> server reads  (serverStdinR / serverStdinW)
	//   server writes -> client reads  (clientStdinR / clientStdinW)
	serverStdinR, serverStdinW := io.Pipe()
	clientStdinR, clientStdinW := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())

	// Start the stdio server in the background.
	serverDone := make(chan error, 1)
	go func() {
		stdio := server.NewStdioServer(srv.mcpServer)
		stdio.SetErrorLogger(log.New(io.Discard, "", 0))
		serverDone <- stdio.Listen(ctx, serverStdinR, clientStdinW)
	}()

	// The client transport: reads from clientStdinR, writes to serverStdinW.
	// NewIO signature: (input io.Reader, output io.WriteCloser, logging io.ReadCloser)
	// We pass a dummy stderr pipe since we don't need logging.
	stderrR, stderrW := io.Pipe()
	go func() {
		<-ctx.Done()
		stderrW.Close()
	}()

	ioTransport := transport.NewIO(clientStdinR, pipeWriteCloser{serverStdinW}, pipeReadCloser{stderrR})
	c := client.NewClient(ioTransport)

	if err := c.Start(ctx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}

	// Initialize the MCP session.
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "transport-test",
		Version: "0.0.1",
	}
	_, err := c.Initialize(ctx, initReq)
	if err != nil {
		t.Fatalf("client.Initialize: %v", err)
	}

	t.Cleanup(func() {
		_ = c.Close()
		cancel()
		// Wait for server to exit.
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
		}
	})

	return c
}

// ---------------------------------------------------------------------------
// Test 1: tools/list — verify all 3 tools are returned with correct names
// ---------------------------------------------------------------------------

func TestTransport_ToolsList(t *testing.T) {
	c := setupTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if len(result.Tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(result.Tools))
	}

	expected := map[string]bool{
		"context_query":  false,
		"context_write":  false,
		"context_verify": false,
		"context_delete": false,
	}
	for _, tool := range result.Tools {
		if _, ok := expected[tool.Name]; !ok {
			t.Errorf("unexpected tool: %s", tool.Name)
			continue
		}
		expected[tool.Name] = true

		// Every tool must have a non-empty input schema.
		schemaBytes, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Errorf("tool %s: marshal schema: %v", tool.Name, err)
			continue
		}
		if len(schemaBytes) < 2 { // at minimum "{}"
			t.Errorf("tool %s: empty input schema", tool.Name)
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing tool: %s", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 2: context_write via transport — send JSON-RPC tools/call
// ---------------------------------------------------------------------------

func TestTransport_ContextWrite(t *testing.T) {
	c := setupTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := mcp.CallToolRequest{}
	req.Params.Name = "context_write"
	req.Params.Arguments = map[string]any{
		"id":      "transport/test-node",
		"type":    "concept",
		"summary": "A node written through the stdio transport",
		"context": "package main // transport test",
	}

	result, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool(context_write): %v", err)
	}
	if result.IsError {
		t.Fatalf("context_write returned tool error: %+v", result.Content)
	}

	// The result should contain a JSON WriteResult with node_id and status.
	if len(result.Content) == 0 {
		t.Fatal("context_write returned empty content")
	}
	tc, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}

	var wr WriteResult
	if err := json.Unmarshal([]byte(tc.Text), &wr); err != nil {
		t.Fatalf("unmarshal WriteResult: %v\nraw: %s", err, tc.Text)
	}
	if wr.NodeID != "transport/test-node" {
		t.Errorf("expected node_id=transport/test-node, got %s", wr.NodeID)
	}
	if wr.Status != "created" {
		t.Errorf("expected status=created, got %s", wr.Status)
	}
	if wr.Hash == "" {
		t.Error("expected non-empty hash")
	}
}

// ---------------------------------------------------------------------------
// Test 3: context_query via transport — write then query
// ---------------------------------------------------------------------------

func TestTransport_ContextQuery(t *testing.T) {
	c := setupTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First, write a node so there's something to query.
	writeReq := mcp.CallToolRequest{}
	writeReq.Params.Name = "context_write"
	writeReq.Params.Arguments = map[string]any{
		"id":      "query/target",
		"type":    "function",
		"summary": "Handles transport-layer query testing",
		"context": "func queryTarget() {}",
	}
	writeRes, err := c.CallTool(ctx, writeReq)
	if err != nil {
		t.Fatalf("CallTool(context_write): %v", err)
	}
	if writeRes.IsError {
		t.Fatalf("write returned error: %+v", writeRes.Content)
	}

	// Now query for it.
	queryReq := mcp.CallToolRequest{}
	queryReq.Params.Name = "context_query"
	queryReq.Params.Arguments = map[string]any{
		"query":  "transport-layer query testing",
		"depth":  2,
		"budget": 4096,
	}
	queryRes, err := c.CallTool(ctx, queryReq)
	if err != nil {
		t.Fatalf("CallTool(context_query): %v", err)
	}
	if queryRes.IsError {
		t.Fatalf("query returned error: %+v", queryRes.Content)
	}

	if len(queryRes.Content) == 0 {
		t.Fatal("context_query returned empty content")
	}
	tc, ok := queryRes.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", queryRes.Content[0])
	}

	xml := tc.Text
	if xml == "" {
		t.Fatal("context_query returned empty XML")
	}
	// The XML result should reference our node.
	if !strings.Contains(xml, "query/target") {
		t.Errorf("expected XML to contain query/target, got:\n%s", xml)
	}
	// It should be wrapped in a context_result element.
	if !strings.Contains(xml, "<context_result") {
		t.Errorf("expected XML to contain <context_result, got:\n%s", xml)
	}
}

// ---------------------------------------------------------------------------
// Test 4: context_verify via transport — verify response format
// ---------------------------------------------------------------------------

func TestTransport_ContextVerify(t *testing.T) {
	c := setupTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Verify on an empty graph should return zero issues.
	verifyReq := mcp.CallToolRequest{}
	verifyReq.Params.Name = "context_verify"
	verifyReq.Params.Arguments = map[string]any{
		"check": "all",
	}

	result, err := c.CallTool(ctx, verifyReq)
	if err != nil {
		t.Fatalf("CallTool(context_verify): %v", err)
	}
	if result.IsError {
		t.Fatalf("context_verify returned tool error: %+v", result.Content)
	}

	if len(result.Content) == 0 {
		t.Fatal("context_verify returned empty content")
	}
	tc, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}

	var vr VerifyResult
	if err := json.Unmarshal([]byte(tc.Text), &vr); err != nil {
		t.Fatalf("unmarshal VerifyResult: %v\nraw: %s", err, tc.Text)
	}
	if vr.Issues == nil {
		t.Error("issues field should be non-nil (empty slice)")
	}
	if vr.Total != 0 {
		t.Errorf("expected 0 issues on empty graph, got %d", vr.Total)
	}
}

// ---------------------------------------------------------------------------
// Test 5: context_verify with dangling edges via transport
// ---------------------------------------------------------------------------

func TestTransport_ContextVerifyDanglingEdge(t *testing.T) {
	c := setupTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Write a node with a dangling edge.
	writeReq := mcp.CallToolRequest{}
	writeReq.Params.Name = "context_write"
	writeReq.Params.Arguments = map[string]any{
		"id":      "verify/source",
		"type":    "function",
		"summary": "Node with dangling edge for verify test",
		"edges": []map[string]any{
			{"target": "verify/nonexistent", "relation": "calls"},
		},
	}
	writeRes, err := c.CallTool(ctx, writeReq)
	if err != nil {
		t.Fatalf("CallTool(context_write): %v", err)
	}
	if writeRes.IsError {
		t.Fatalf("write returned error: %+v", writeRes.Content)
	}

	// Verify integrity — should detect the dangling edge.
	verifyReq := mcp.CallToolRequest{}
	verifyReq.Params.Name = "context_verify"
	verifyReq.Params.Arguments = map[string]any{
		"check": "integrity",
	}

	result, err := c.CallTool(ctx, verifyReq)
	if err != nil {
		t.Fatalf("CallTool(context_verify): %v", err)
	}
	if result.IsError {
		t.Fatalf("context_verify returned tool error: %+v", result.Content)
	}

	tc, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}

	var vr VerifyResult
	if err := json.Unmarshal([]byte(tc.Text), &vr); err != nil {
		t.Fatalf("unmarshal VerifyResult: %v\nraw: %s", err, tc.Text)
	}
	if vr.Total == 0 {
		t.Error("expected at least 1 issue (dangling edge)")
	}

	found := false
	for _, issue := range vr.Issues {
		if issue.Type == "dangling_edge" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected dangling_edge issue, got: %+v", vr.Issues)
	}
}
