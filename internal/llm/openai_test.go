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

func openaiTestProvider(t *testing.T, url, model string) *OpenAIProvider {
	t.Helper()
	p := NewOpenAIProviderWithModel("test-key", model)
	p.baseURL = url
	return p
}

// okOpenAIResponse encodes a successful Responses API reply with a single
// output_text block.
func okOpenAIResponse(text string) string {
	b, _ := json.Marshal(openaiResponsesResponse{
		Output: []openaiOutputItem{
			{Type: "message", Content: []openaiContentPart{{Type: "output_text", Text: text}}},
		},
	})
	return string(b)
}

func TestNewOpenAIProvider_Defaults(t *testing.T) {
	p := NewOpenAIProvider("key")
	if p.Model() != OpenAIDefaultModel {
		t.Errorf("Model() = %q, want %q", p.Model(), OpenAIDefaultModel)
	}
	if p.baseURL != openaiResponsesURL {
		t.Errorf("baseURL = %q, want %q", p.baseURL, openaiResponsesURL)
	}
}

func TestNewOpenAIProviderWithModel_EmptyFallsBack(t *testing.T) {
	if got := NewOpenAIProviderWithModel("k", "\t ").Model(); got != OpenAIDefaultModel {
		t.Errorf("empty model: got %q, want default", got)
	}
	if got := NewOpenAIProviderWithModel("k", "gpt-x").Model(); got != "gpt-x" {
		t.Errorf("got %q, want gpt-x", got)
	}
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		name string
		resp openaiResponsesResponse
		want string
	}{
		{
			name: "first message output_text",
			resp: openaiResponsesResponse{Output: []openaiOutputItem{
				{Type: "message", Content: []openaiContentPart{{Type: "output_text", Text: "hi"}}},
			}},
			want: "hi",
		},
		{
			name: "skips non-message and non-text parts",
			resp: openaiResponsesResponse{Output: []openaiOutputItem{
				{Type: "reasoning"},
				{Type: "message", Content: []openaiContentPart{
					{Type: "refusal"},
					{Type: "output_text", Text: "answer"},
				}},
			}},
			want: "answer",
		},
		{
			name: "no text",
			resp: openaiResponsesResponse{Output: []openaiOutputItem{{Type: "message"}}},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.resp.extractText(); got != tc.want {
				t.Errorf("extractText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasReasoningOnly(t *testing.T) {
	reasoningOnly := openaiResponsesResponse{Output: []openaiOutputItem{{Type: "reasoning"}}}
	if !reasoningOnly.hasReasoningOnly() {
		t.Error("expected hasReasoningOnly true for reasoning-only output")
	}
	withMessage := openaiResponsesResponse{Output: []openaiOutputItem{
		{Type: "reasoning"}, {Type: "message"},
	}}
	if withMessage.hasReasoningOnly() {
		t.Error("expected false when a message is present")
	}
	empty := openaiResponsesResponse{}
	if empty.hasReasoningOnly() {
		t.Error("expected false for empty output")
	}
}

func TestOpenAIClassify_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req openaiResponsesRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad body: %v", err)
		}
		if req.MaxTokens != 512 {
			t.Errorf("MaxTokens = %d, want 512", req.MaxTokens)
		}
		if len(req.Input) != 2 || req.Input[0].Role != "system" || req.Input[1].Role != "user" {
			t.Errorf("input = %+v", req.Input)
		}
		io.WriteString(w, okOpenAIResponse(`{"action":"ADD","confidence":0.6,"reasoning":"new"}`))
	}))
	defer srv.Close()

	p := openaiTestProvider(t, srv.URL, "")
	got, err := p.Classify(context.Background(), ClassifyRequest{
		Incoming: &node.Node{ID: "n1", Summary: "s"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Action != ActionADD || got.Confidence != 0.6 {
		t.Errorf("got %+v", got)
	}
}

func TestOpenAIClassify_ErrorReturnsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		b, _ := json.Marshal(openaiResponsesResponse{Error: &openaiAPIError{Message: "bad key", Type: "auth"}})
		w.Write(b)
	}))
	defer srv.Close()

	p := openaiTestProvider(t, srv.URL, "")
	got, err := p.Classify(context.Background(), ClassifyRequest{})
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("expected API error, got %v", err)
	}
	if got.Action != ActionADD || got.Confidence != 0.1 {
		t.Errorf("expected fallback, got %+v", got)
	}
}

func TestOpenAISummarize_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req openaiResponsesRequest
		json.Unmarshal(body, &req)
		if req.MaxTokens != 1024 {
			t.Errorf("MaxTokens = %d, want 1024", req.MaxTokens)
		}
		if req.Input[0].Content != summarizeSystemPrompt {
			t.Errorf("system prompt mismatch")
		}
		io.WriteString(w, okOpenAIResponse("summary text"))
	}))
	defer srv.Close()

	p := openaiTestProvider(t, srv.URL, "")
	got, err := p.Summarize(context.Background(), SummarizeRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "summary text" {
		t.Errorf("got %q", got)
	}
}

func TestOpenAIChat_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req openaiResponsesRequest
		json.Unmarshal(body, &req)
		if req.MaxTokens != 300 {
			t.Errorf("MaxTokens = %d, want 300", req.MaxTokens)
		}
		// System prompt prepended, and the system-role message dropped.
		if req.Input[0].Role != "system" || req.Input[0].Content != "sys" {
			t.Errorf("input[0] = %+v", req.Input[0])
		}
		for _, m := range req.Input[1:] {
			if m.Role == "system" {
				t.Errorf("system role leaked into input")
			}
		}
		io.WriteString(w, okOpenAIResponse("chat reply"))
	}))
	defer srv.Close()

	p := openaiTestProvider(t, srv.URL, "")
	got, err := p.Chat(context.Background(), ChatRequest{
		SystemPrompt: "sys",
		MaxTokens:    300,
		Messages: []ChatMessage{
			{Role: "system", Content: "ignored"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "chat reply" {
		t.Errorf("got %q", got)
	}
}

func TestOpenAIChat_DefaultMaxTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req openaiResponsesRequest
		json.Unmarshal(body, &req)
		if req.MaxTokens != 1024 {
			t.Errorf("MaxTokens = %d, want default 1024", req.MaxTokens)
		}
		io.WriteString(w, okOpenAIResponse("ok"))
	}))
	defer srv.Close()

	p := openaiTestProvider(t, srv.URL, "")
	if _, err := p.Chat(context.Background(), ChatRequest{SystemPrompt: "s"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAIDoRequest_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		model   string
		wantSub string
	}{
		{
			name: "api error json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				b, _ := json.Marshal(openaiResponsesResponse{Error: &openaiAPIError{Message: "invalid model"}})
				w.Write(b)
			},
			wantSub: "API error (400): invalid model",
		},
		{
			name: "unexpected status non-error body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, "down")
			},
			wantSub: "unexpected status 503",
		},
		{
			name: "unmarshal failure",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "not json")
			},
			wantSub: "unmarshal chat response",
		},
		{
			name: "empty output",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"output":[]}`)
			},
			wantSub: "empty output",
		},
		{
			name:  "reasoning only",
			model: "o5-reasoner",
			handler: func(w http.ResponseWriter, r *http.Request) {
				b, _ := json.Marshal(openaiResponsesResponse{Output: []openaiOutputItem{{Type: "reasoning"}}})
				w.Write(b)
			},
			wantSub: "used the entire max_output_tokens budget on reasoning",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			p := openaiTestProvider(t, srv.URL, tc.model)
			// Exercise via Chat so the "chat" label appears in unmarshal errors.
			_, err := p.Chat(context.Background(), ChatRequest{SystemPrompt: "s"})
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestOpenAIDoRequest_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	p := openaiTestProvider(t, srv.URL, "")
	_, err := p.Summarize(context.Background(), SummarizeRequest{Namespace: "ns"})
	if err == nil || !strings.Contains(err.Error(), "summarize network error") {
		t.Fatalf("expected summarize network error, got %v", err)
	}
}

func TestOpenAIDoRequest_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okOpenAIResponse("ok"))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := openaiTestProvider(t, srv.URL, "")
	_, err := p.Chat(ctx, ChatRequest{SystemPrompt: "s"})
	if err == nil || !strings.Contains(err.Error(), "network error") {
		t.Fatalf("expected network error from cancelled context, got %v", err)
	}
}
