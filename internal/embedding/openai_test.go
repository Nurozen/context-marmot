package embedding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewOpenAIEmbedder_ValidModels(t *testing.T) {
	models := map[string]int{
		"text-embedding-3-small": 1536,
		"text-embedding-3-large": 3072,
		"text-embedding-ada-002": 1536,
	}
	for model, expectedDim := range models {
		e, err := NewOpenAIEmbedder("test-key", model)
		if err != nil {
			t.Fatalf("NewOpenAIEmbedder(%q): %v", model, err)
		}
		if e.Dimension() != expectedDim {
			t.Errorf("model %q: Dimension() = %d, want %d", model, e.Dimension(), expectedDim)
		}
		if e.Model() != model {
			t.Errorf("model %q: Model() = %q", model, e.Model())
		}
	}
}

func TestNewOpenAIEmbedder_DefaultModel(t *testing.T) {
	e, err := NewOpenAIEmbedder("test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Model() != "text-embedding-3-small" {
		t.Errorf("default model = %q, want text-embedding-3-small", e.Model())
	}
	if e.Dimension() != 1536 {
		t.Errorf("default dimension = %d, want 1536", e.Dimension())
	}
}

func TestNewOpenAIEmbedder_InvalidModel(t *testing.T) {
	_, err := NewOpenAIEmbedder("test-key", "invalid-model")
	if err == nil {
		t.Fatal("expected error for invalid model")
	}
	if !strings.Contains(err.Error(), "unsupported model") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewOpenAIEmbedder_NoAPIKey(t *testing.T) {
	_, err := NewOpenAIEmbedder("", "text-embedding-3-small")
	if err == nil {
		t.Fatal("expected error for empty API key")
	}
	if !strings.Contains(err.Error(), "API key is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpenAIEmbed_RequestFormat(t *testing.T) {
	var receivedReq openaiRequest
	var receivedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&receivedReq); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", 400)
			return
		}

		resp := openaiResponse{
			Data: []openaiEmbedding{
				{Embedding: make([]float32, 1536), Index: 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e, err := NewOpenAIEmbedder("sk-test-key-123", "text-embedding-3-small")
	if err != nil {
		t.Fatal(err)
	}
	// Override endpoint for testing.
	e.client = srv.Client()
	origEndpoint := openaiEndpoint
	// We need to redirect to our test server. We'll use a custom transport.
	e.client.Transport = &rewriteTransport{base: http.DefaultTransport, newURL: srv.URL}

	_, err = e.Embed("hello world")
	if err != nil {
		t.Fatal(err)
	}

	if receivedAuth != "Bearer sk-test-key-123" {
		t.Errorf("auth header = %q, want %q", receivedAuth, "Bearer sk-test-key-123")
	}
	if receivedReq.Model != "text-embedding-3-small" {
		t.Errorf("model = %q, want text-embedding-3-small", receivedReq.Model)
	}

	// For single input, the API sends a string.
	inputStr, ok := receivedReq.Input.(string)
	if !ok {
		t.Errorf("expected single-string input, got %T", receivedReq.Input)
	} else if inputStr != "hello world" {
		t.Errorf("input = %q, want %q", inputStr, "hello world")
	}

	_ = origEndpoint
}

func TestOpenAIEmbed_401Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	_, err := e.Embed("hello")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("expected unauthorized error, got: %v", err)
	}
}

func TestOpenAIEmbed_429RetryThenSuccess(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		resp := openaiResponse{
			Data: []openaiEmbedding{
				{Embedding: make([]float32, 1536), Index: 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	// Override backoff for tests — we use a custom embedder with short backoff below.
	vec, err := e.Embed("hello")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if len(vec) != 1536 {
		t.Errorf("expected 1536 dims, got %d", len(vec))
	}
	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestOpenAIEmbed_429ExhaustedRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	_, err := e.Embed("hello")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "exhausted retries") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpenAIEmbedBatch_Chunking(t *testing.T) {
	var receivedBatchSizes []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openaiRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}

		// Determine the number of inputs.
		var inputs []string
		switch v := req.Input.(type) {
		case string:
			inputs = []string{v}
		case []interface{}:
			for _, item := range v {
				inputs = append(inputs, item.(string))
			}
		}
		receivedBatchSizes = append(receivedBatchSizes, len(inputs))

		resp := openaiResponse{
			Data: make([]openaiEmbedding, len(inputs)),
		}
		for i := range inputs {
			resp.Data[i] = openaiEmbedding{
				Embedding: make([]float32, 1536),
				Index:     i,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)

	// Create 150 texts — should be chunked into 100 + 50.
	texts := make([]string, 150)
	for i := range texts {
		texts[i] = "text"
	}

	results, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 150 {
		t.Errorf("expected 150 results, got %d", len(results))
	}
	if len(receivedBatchSizes) != 2 {
		t.Fatalf("expected 2 API calls, got %d", len(receivedBatchSizes))
	}
	if receivedBatchSizes[0] != 100 {
		t.Errorf("first chunk size = %d, want 100", receivedBatchSizes[0])
	}
	if receivedBatchSizes[1] != 50 {
		t.Errorf("second chunk size = %d, want 50", receivedBatchSizes[1])
	}
}

func TestOpenAIEmbed_NetworkError(t *testing.T) {
	// Use a server that immediately closes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Close the connection by hijacking it.
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer srv.Close()

	e := newTestOpenAIEmbedder(t, srv.URL)
	_, err := e.Embed("hello")
	if err == nil {
		t.Fatal("expected error for network failure")
	}
	if !strings.Contains(err.Error(), "exhausted retries") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestProviderFactory(t *testing.T) {
	// Mock provider.
	emb, err := NewEmbedder("mock", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if emb.Model() != "mock-v1" {
		t.Errorf("mock model = %q, want mock-v1", emb.Model())
	}
	if emb.Dimension() != 1536 {
		t.Errorf("mock dimension = %d, want 1536", emb.Dimension())
	}

	// Empty provider defaults to mock.
	emb2, err := NewEmbedder("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if emb2.Model() != "mock-v1" {
		t.Errorf("default model = %q, want mock-v1", emb2.Model())
	}

	// Mock with custom model name.
	emb3, err := NewEmbedder("mock", "custom-mock", "")
	if err != nil {
		t.Fatal(err)
	}
	if emb3.Model() != "custom-mock" {
		t.Errorf("custom mock model = %q, want custom-mock", emb3.Model())
	}

	// OpenAI without API key should error.
	_, err = NewEmbedder("openai", "", "")
	if err == nil {
		t.Fatal("expected error for openai without API key")
	}

	// OpenAI with API key should succeed.
	emb4, err := NewEmbedder("openai", "text-embedding-3-small", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if emb4.Model() != "text-embedding-3-small" {
		t.Errorf("openai model = %q", emb4.Model())
	}
	if emb4.Dimension() != 1536 {
		t.Errorf("openai dimension = %d", emb4.Dimension())
	}

	// Unknown provider.
	_, err = NewEmbedder("anthropic", "", "key")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// --- helpers ---

// rewriteTransport redirects all requests to the test server URL.
type rewriteTransport struct {
	base   http.RoundTripper
	newURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.newURL, "http://")
	return t.base.RoundTrip(req)
}

// newTestOpenAIEmbedder creates an OpenAIEmbedder pointing at a test server with near-zero backoff.
func newTestOpenAIEmbedder(t *testing.T, serverURL string) *OpenAIEmbedder {
	t.Helper()
	e, err := NewOpenAIEmbedder("sk-test", "text-embedding-3-small")
	if err != nil {
		t.Fatal(err)
	}
	e.client = &http.Client{
		Transport: &rewriteTransport{base: http.DefaultTransport, newURL: serverURL},
	}
	e.initialBackoff = 1 * time.Millisecond // fast retries in tests
	return e
}
