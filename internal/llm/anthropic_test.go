package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
)

// anthropicTestProvider returns a provider pointed at the given test server URL.
func anthropicTestProvider(t *testing.T, url, model string) *AnthropicProvider {
	t.Helper()
	p := NewAnthropicProviderWithModel("test-key", model)
	p.endpoint = url
	return p
}

// okAnthropicResponse encodes a successful Messages API response with a single
// text content block.
func okAnthropicResponse(text string) string {
	b, _ := json.Marshal(anthropicResponse{
		Content: []anthropicContentBlock{{Type: "text", Text: text}},
	})
	return string(b)
}

func TestNewAnthropicProvider_Defaults(t *testing.T) {
	p := NewAnthropicProvider("key")
	if p.Model() != AnthropicDefaultModel {
		t.Errorf("Model() = %q, want %q", p.Model(), AnthropicDefaultModel)
	}
	if p.endpoint != anthropicEndpoint {
		t.Errorf("endpoint = %q, want %q", p.endpoint, anthropicEndpoint)
	}
	if p.apiKey != "key" {
		t.Errorf("apiKey = %q, want %q", p.apiKey, "key")
	}
}

func TestNewAnthropicProviderWithModel_EmptyFallsBack(t *testing.T) {
	p := NewAnthropicProviderWithModel("key", "   ")
	if p.Model() != AnthropicDefaultModel {
		t.Errorf("Model() = %q, want default %q", p.Model(), AnthropicDefaultModel)
	}
	p2 := NewAnthropicProviderWithModel("key", "claude-custom")
	if p2.Model() != "claude-custom" {
		t.Errorf("Model() = %q, want %q", p2.Model(), "claude-custom")
	}
}

func TestAnthropicClassify_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request shape and headers.
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicVersion {
			t.Errorf("anthropic-version = %q, want %q", got, anthropicVersion)
		}
		if got := r.Header.Get("content-type"); got != "application/json" {
			t.Errorf("content-type = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		if req.System != anthropicSystemPrompt {
			t.Errorf("system prompt mismatch")
		}
		if req.MaxTokens != 512 {
			t.Errorf("MaxTokens = %d, want 512", req.MaxTokens)
		}
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
			t.Errorf("messages = %+v", req.Messages)
		}
		io.WriteString(w, okAnthropicResponse(`{"action":"UPDATE","target_node_id":"n1","confidence":0.8,"reasoning":"ok"}`))
	}))
	defer srv.Close()

	p := anthropicTestProvider(t, srv.URL, "")
	got, err := p.Classify(context.Background(), ClassifyRequest{
		Incoming:   &node.Node{ID: "n2", Type: "concept", Summary: "s", Context: "c"},
		Candidates: []CandidateNode{{Node: &node.Node{ID: "n1", Summary: "existing"}, Score: 0.9}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := ClassifyResult{Action: ActionUPDATE, TargetNodeID: "n1", Confidence: 0.8, Reasoning: "ok"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAnthropicClassify_APIErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		b, _ := json.Marshal(anthropicResponse{Error: &anthropicError{Type: "auth", Message: "bad key"}})
		w.Write(b)
	}))
	defer srv.Close()

	p := anthropicTestProvider(t, srv.URL, "")
	got, err := p.Classify(context.Background(), ClassifyRequest{})
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("expected API error, got %v", err)
	}
	if got.Action != ActionADD || got.Confidence != 0.1 {
		t.Errorf("expected fallback result, got %+v", got)
	}
}

func TestAnthropicClassify_UnexpectedStatusNoErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "gateway boom")
	}))
	defer srv.Close()

	p := anthropicTestProvider(t, srv.URL, "")
	_, err := p.Classify(context.Background(), ClassifyRequest{})
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
		t.Fatalf("expected unexpected status error, got %v", err)
	}
}

func TestAnthropicClassify_BadJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not json")
	}))
	defer srv.Close()

	p := anthropicTestProvider(t, srv.URL, "")
	_, err := p.Classify(context.Background(), ClassifyRequest{})
	if err == nil || !strings.Contains(err.Error(), "unmarshal response") {
		t.Fatalf("expected unmarshal error, got %v", err)
	}
}

func TestAnthropicClassify_EmptyContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[]}`)
	}))
	defer srv.Close()

	p := anthropicTestProvider(t, srv.URL, "")
	_, err := p.Classify(context.Background(), ClassifyRequest{})
	if err == nil || !strings.Contains(err.Error(), "empty content") {
		t.Fatalf("expected empty content error, got %v", err)
	}
}

func TestAnthropicClassify_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // immediately closed -> connection refused

	p := anthropicTestProvider(t, srv.URL, "")
	_, err := p.Classify(context.Background(), ClassifyRequest{})
	if err == nil || !strings.Contains(err.Error(), "network error") {
		t.Fatalf("expected network error, got %v", err)
	}
}

func TestAnthropicChat_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		json.Unmarshal(body, &req)
		if req.System != "sys" {
			t.Errorf("system = %q, want sys", req.System)
		}
		if req.MaxTokens != 256 {
			t.Errorf("MaxTokens = %d, want 256", req.MaxTokens)
		}
		// The "system" role message must be filtered out of Messages.
		for _, m := range req.Messages {
			if m.Role == "system" {
				t.Errorf("system role leaked into messages")
			}
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "hi" {
			t.Errorf("messages = %+v", req.Messages)
		}
		io.WriteString(w, okAnthropicResponse("hello there"))
	}))
	defer srv.Close()

	p := anthropicTestProvider(t, srv.URL, "")
	got, err := p.Chat(context.Background(), ChatRequest{
		SystemPrompt: "sys",
		MaxTokens:    256,
		Messages: []ChatMessage{
			{Role: "system", Content: "ignored"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello there" {
		t.Errorf("got %q, want %q", got, "hello there")
	}
}

func TestAnthropicChat_DefaultMaxTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		json.Unmarshal(body, &req)
		if req.MaxTokens != 1024 {
			t.Errorf("MaxTokens = %d, want default 1024", req.MaxTokens)
		}
		io.WriteString(w, okAnthropicResponse("ok"))
	}))
	defer srv.Close()

	p := anthropicTestProvider(t, srv.URL, "")
	if _, err := p.Chat(context.Background(), ChatRequest{SystemPrompt: "s"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnthropicChat_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantSub string
	}{
		{
			name: "api error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
				b, _ := json.Marshal(anthropicResponse{Error: &anthropicError{Message: "rate limited"}})
				w.Write(b)
			},
			wantSub: "chat API error",
		},
		{
			name: "unexpected status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				io.WriteString(w, "boom")
			},
			wantSub: "chat unexpected status",
		},
		{
			name: "bad json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "{{{")
			},
			wantSub: "unmarshal chat response",
		},
		{
			name: "empty content",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"content":[]}`)
			},
			wantSub: "empty content in chat response",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			p := anthropicTestProvider(t, srv.URL, "")
			_, err := p.Chat(context.Background(), ChatRequest{SystemPrompt: "s"})
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestAnthropicChat_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	p := anthropicTestProvider(t, srv.URL, "")
	_, err := p.Chat(context.Background(), ChatRequest{SystemPrompt: "s"})
	if err == nil || !strings.Contains(err.Error(), "chat network error") {
		t.Fatalf("expected chat network error, got %v", err)
	}
}

func TestAnthropicSummarize_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		json.Unmarshal(body, &req)
		if req.System != summarizeSystemPrompt {
			t.Errorf("summarize system prompt mismatch")
		}
		if req.MaxTokens != 1024 {
			t.Errorf("MaxTokens = %d, want 1024", req.MaxTokens)
		}
		if !strings.Contains(req.Messages[0].Content, "Namespace: auth") {
			t.Errorf("user message missing namespace: %q", req.Messages[0].Content)
		}
		io.WriteString(w, okAnthropicResponse("# Summary"))
	}))
	defer srv.Close()

	p := anthropicTestProvider(t, srv.URL, "")
	got, err := p.Summarize(context.Background(), SummarizeRequest{
		Namespace: "auth",
		Nodes:     []NodeSummaryInput{{ID: "auth/login", Type: "concept", Summary: "login flow", Edges: []string{"auth/token"}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "# Summary" {
		t.Errorf("got %q, want %q", got, "# Summary")
	}
}

func TestAnthropicSummarize_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantSub string
	}{
		{
			name: "api error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				b, _ := json.Marshal(anthropicResponse{Error: &anthropicError{Message: "forbidden"}})
				w.Write(b)
			},
			wantSub: "summarize API error",
		},
		{
			name: "unexpected status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, "boom")
			},
			wantSub: "summarize unexpected status",
		},
		{
			name: "bad json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "nope")
			},
			wantSub: "unmarshal summarize response",
		},
		{
			name: "empty content",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"content":[]}`)
			},
			wantSub: "empty content in summarize response",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			p := anthropicTestProvider(t, srv.URL, "")
			_, err := p.Summarize(context.Background(), SummarizeRequest{Namespace: "ns"})
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestAnthropicSummarize_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	p := anthropicTestProvider(t, srv.URL, "")
	_, err := p.Summarize(context.Background(), SummarizeRequest{Namespace: "ns"})
	if err == nil || !strings.Contains(err.Error(), "summarize network error") {
		t.Fatalf("expected summarize network error, got %v", err)
	}
}
