package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/curator"
	"github.com/nurozen/context-marmot/internal/llm"
)

func TestHandleChat_SlashCommand(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// /verify should execute without an LLM provider.
	body := `{"message": "/verify", "session_id": "test-1"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Message.Role != "assistant" {
		t.Errorf("expected role assistant, got %q", resp.Message.Role)
	}
	if resp.Message.Content == "" {
		t.Error("expected non-empty message content")
	}
}

func TestHandleChat_SlashTag(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	body := `{"message": "/tag payment", "session_id": "test-2", "selected_nodes": ["auth/login"]}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Message.Content == "" {
		t.Error("expected non-empty message content from /tag")
	}
}

func TestHandleChat_NoLLMProvider(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	// Natural language message without LLM configured should return helpful message.
	body := `{"message": "What are the most connected nodes?", "session_id": "test-3"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Message.Role != "assistant" {
		t.Errorf("expected role assistant, got %q", resp.Message.Role)
	}
	// Should mention slash commands.
	if resp.Message.Content == "" {
		t.Error("expected non-empty message content")
	}
}

func TestHandleChat_WithLLMProvider(t *testing.T) {
	server, _ := newTestServer(t)

	mock := &llm.MockProvider{
		ChatResult: "The auth/login node has the most connections with 3 edges.",
	}
	server.WithLLMChat(mock)

	handler := server.Handler()

	body := `{"message": "What is the most connected node?", "session_id": "test-4"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Message.Role != "assistant" {
		t.Errorf("expected role assistant, got %q", resp.Message.Role)
	}
	if resp.Message.Content != mock.ChatResult {
		t.Errorf("expected mock result %q, got %q", mock.ChatResult, resp.Message.Content)
	}
	if mock.ChatCalls != 1 {
		t.Errorf("expected 1 Chat call, got %d", mock.ChatCalls)
	}
}

func TestHandleChat_WithSelectedNodes(t *testing.T) {
	server, _ := newTestServer(t)

	mock := &llm.MockProvider{}
	server.WithLLMChat(mock)

	handler := server.Handler()

	body := `{"message": "Tell me about this node", "session_id": "test-5", "selected_nodes": ["auth/login"]}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Message.Role != "assistant" {
		t.Errorf("expected role assistant, got %q", resp.Message.Role)
	}
	if resp.Message.Content == "" {
		t.Error("expected non-empty response from mock LLM")
	}
}

func TestHandleChat_MissingSessionID(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	body := `{"message": "hello"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChat_EmptyMessage(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	body := `{"message": "", "session_id": "test-6"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChat_InvalidJSON(t *testing.T) {
	server, _ := newTestServer(t)
	handler := server.Handler()

	rec := doRequest(t, handler, "POST", "/api/chat", "not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Code-mode tests
// ---------------------------------------------------------------------------

func TestHandleChat_CodeMode_Roundtrip(t *testing.T) {
	server, _ := newTestServer(t)

	// Phase 1: model emits code that calls client.getStats().
	// Phase 2: model produces a NL summary referencing the result.
	mock := &llm.MockProvider{
		ChatResults: []string{
			"I'll check the graph stats.\n\n```js\nreturn client.getStats();\n```",
			"The graph has 4 nodes across 1 namespace (default), with 3 outgoing edges. The auth/login node is the most connected.",
		},
	}
	server.WithLLMChat(mock)
	handler := server.Handler()

	body := `{"message": "How big is the graph?", "session_id": "code-1"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CodeRun == nil {
		t.Fatalf("expected CodeRun to be populated, got nil")
	}
	if !strings.Contains(resp.CodeRun.Code, "client.getStats") {
		t.Errorf("expected code to mention getStats, got %q", resp.CodeRun.Code)
	}
	if resp.CodeRun.Error != "" {
		t.Errorf("expected no error, got %q", resp.CodeRun.Error)
	}
	if resp.CodeRun.Result == nil {
		t.Errorf("expected non-nil Result")
	}
	// Final message should be the phase-2 summary, not the phase-1 code.
	if !strings.Contains(resp.Message.Content, "4 nodes") {
		t.Errorf("expected final message to be phase-2 summary, got %q", resp.Message.Content)
	}
	if mock.ChatCalls != 2 {
		t.Errorf("expected 2 LLM calls (phase 1 + phase 2), got %d", mock.ChatCalls)
	}
}

func TestHandleChat_CodeMode_NoCode(t *testing.T) {
	server, _ := newTestServer(t)

	// Model decides the question doesn't need graph access — direct answer.
	mock := &llm.MockProvider{
		ChatResults: []string{
			"To tag a node, use the `/tag <name>` slash command after selecting nodes.",
		},
	}
	server.WithLLMChat(mock)
	handler := server.Handler()

	body := `{"message": "How do I tag a node?", "session_id": "code-2"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CodeRun != nil {
		t.Errorf("expected no CodeRun for direct answer, got %+v", resp.CodeRun)
	}
	if !strings.Contains(resp.Message.Content, "/tag") {
		t.Errorf("expected message to mention /tag, got %q", resp.Message.Content)
	}
	// Only one LLM call — phase 2 was skipped.
	if mock.ChatCalls != 1 {
		t.Errorf("expected 1 LLM call (no code path), got %d", mock.ChatCalls)
	}
}

func TestHandleChat_CodeMode_BrokenCode(t *testing.T) {
	server, _ := newTestServer(t)

	mock := &llm.MockProvider{
		ChatResults: []string{
			// Phase 1 — broken JS.
			"```js\nthis is not valid javascript syntax!\n```",
			// Recovery retry — also broken so the executor stays in error state.
			"```js\nalso still broken !!\n```",
			// Phase 2 — synthesizes an apology referencing the malformed run.
			"I tried to inspect the graph but my code was malformed. Try asking again.",
		},
	}
	server.WithLLMChat(mock)
	handler := server.Handler()

	body := `{"message": "Show me orphan nodes", "session_id": "code-3"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CodeRun == nil {
		t.Fatalf("expected CodeRun even on parse failure")
	}
	if resp.CodeRun.Error == "" {
		t.Errorf("expected non-empty Error on broken code")
	}
	// Phase 2 should still produce a message (model gets to apologize).
	if !strings.Contains(resp.Message.Content, "malformed") {
		t.Errorf("expected phase-2 apology, got %q", resp.Message.Content)
	}
	// 3 LLM calls: phase 1 + recovery retry + phase 2.
	if mock.ChatCalls != 3 {
		t.Errorf("expected 3 LLM calls (phase 1 + retry + phase 2), got %d", mock.ChatCalls)
	}
}

// TestHandleChat_CodeMode_RetryRecovery verifies that when phase-1 code
// throws (e.g. "node not found"), the chat handler automatically gives the
// LLM another shot. The retry's successful code result is what feeds phase 2.
func TestHandleChat_CodeMode_RetryRecovery(t *testing.T) {
	server, _ := newTestServer(t)

	mock := &llm.MockProvider{
		ChatResults: []string{
			// Phase 1 — wrong ID, will throw "node not found".
			"```js\nreturn client.getNode(\"login\");\n```",
			// Recovery — uses search this time.
			"```js\nreturn client.listByType(\"function\").map(n => n.id);\n```",
			// Phase 2 — synthesizes answer from retry result.
			"Found the login handler at auth/login.",
		},
	}
	server.WithLLMChat(mock)
	handler := server.Handler()

	body := `{"message": "tell me about login", "session_id": "code-retry"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CodeRun == nil {
		t.Fatalf("expected CodeRun in retry path")
	}
	// The CodeRun should reflect the *retry's* code, not the failed first.
	if !strings.Contains(resp.CodeRun.Code, "listByType") {
		t.Errorf("expected retry code in CodeRun.Code, got %q", resp.CodeRun.Code)
	}
	if resp.CodeRun.Error != "" {
		t.Errorf("expected no error after successful retry, got %q", resp.CodeRun.Error)
	}
	// 3 LLM calls: phase 1 (failed) + retry (succeeded) + phase 2.
	if mock.ChatCalls != 3 {
		t.Errorf("expected 3 LLM calls, got %d", mock.ChatCalls)
	}
}

func TestHandleChat_CodeMode_QueryGraph(t *testing.T) {
	// Verifies the client.search method actually returns nodes from the seeded graph.
	server, _ := newTestServer(t)

	mock := &llm.MockProvider{
		ChatResults: []string{
			"```js\nreturn client.listByType(\"function\").map(n => n.id);\n```",
			"There are two function nodes: auth/login and auth/token.",
		},
	}
	server.WithLLMChat(mock)
	handler := server.Handler()

	body := `{"message": "List all function nodes", "session_id": "code-4"}`
	rec := doRequest(t, handler, "POST", "/api/chat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp curator.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CodeRun == nil || resp.CodeRun.Error != "" {
		t.Fatalf("expected successful execution, got %+v", resp.CodeRun)
	}
	// Result should be a slice of strings containing the function node IDs.
	got, ok := resp.CodeRun.Result.([]any)
	if !ok {
		t.Fatalf("expected []any result, got %T (%v)", resp.CodeRun.Result, resp.CodeRun.Result)
	}
	have := map[string]bool{}
	for _, v := range got {
		if s, ok := v.(string); ok {
			have[s] = true
		}
	}
	if !have["auth/login"] || !have["auth/token"] {
		t.Errorf("expected auth/login and auth/token in result, got %v", got)
	}
}
