package api

import (
	"encoding/json"
	"net/http"
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
