package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Ensure MockProvider implements Provider, Summarizer, and ChatProvider.
var (
	_ Provider     = (*MockProvider)(nil)
	_ Summarizer   = (*MockProvider)(nil)
	_ ChatProvider = (*MockProvider)(nil)
)

// MockProvider returns a configurable fixed result. Used in tests.
type MockProvider struct {
	Result        ClassifyResult
	Err           error
	SummaryResult string
	SummaryErr    error
	ChatResult    string
	ChatErr       error

	mu             sync.Mutex
	Calls          int
	ChatCalls      int
	summarizeCalls int64 // accessed atomically
}

// GetSummarizeCalls returns the number of Summarize calls (thread-safe).
func (m *MockProvider) GetSummarizeCalls() int {
	return int(atomic.LoadInt64(&m.summarizeCalls))
}

func (m *MockProvider) Classify(_ context.Context, _ ClassifyRequest) (ClassifyResult, error) {
	m.mu.Lock()
	m.Calls++
	m.mu.Unlock()
	return m.Result, m.Err
}

func (m *MockProvider) Model() string { return "mock" }

// Summarize generates a summary from the given request. If SummaryResult is set
// it is returned directly; otherwise a simple default is generated from node IDs.
func (m *MockProvider) Summarize(_ context.Context, req SummarizeRequest) (string, error) {
	atomic.AddInt64(&m.summarizeCalls, 1)
	if m.SummaryResult != "" {
		return m.SummaryResult, m.SummaryErr
	}
	// Default: generate a simple summary from node IDs.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Summary of namespace %s with %d nodes.\n\n", req.Namespace, len(req.Nodes))
	for _, n := range req.Nodes {
		if len(sb.String()) > 500 {
			break
		}
		fmt.Fprintf(&sb, "- [[%s]] (%s)\n", n.ID, n.Type)
	}
	return sb.String(), m.SummaryErr
}

// Chat returns a fixed chat result or a default echo. Thread-safe.
func (m *MockProvider) Chat(_ context.Context, req ChatRequest) (string, error) {
	m.mu.Lock()
	m.ChatCalls++
	m.mu.Unlock()
	if m.ChatResult != "" {
		return m.ChatResult, m.ChatErr
	}
	// Default: echo the last user message.
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return "I can help with that. You said: " + req.Messages[i].Content, m.ChatErr
		}
	}
	return "How can I help you curate your knowledge graph?", m.ChatErr
}
