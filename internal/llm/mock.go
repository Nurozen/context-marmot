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

	// ChatResults, if non-nil, is consumed in order — the first Chat call
	// pops index 0, the next pops index 1, and so on. After the queue is
	// drained, Chat falls back to ChatResult or the default echo. This lets
	// tests script multi-turn conversations (e.g. code-mode phase 1 then
	// phase 2).
	ChatResults []string

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
	// Consume the queue first if non-empty.
	if len(m.ChatResults) > 0 {
		next := m.ChatResults[0]
		m.ChatResults = m.ChatResults[1:]
		m.mu.Unlock()
		return next, m.ChatErr
	}
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
