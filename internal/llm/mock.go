package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Ensure MockProvider implements both Provider and Summarizer.
var (
	_ Provider   = (*MockProvider)(nil)
	_ Summarizer = (*MockProvider)(nil)
)

// MockProvider returns a configurable fixed result. Used in tests.
type MockProvider struct {
	Result        ClassifyResult
	Err           error
	SummaryResult string
	SummaryErr    error

	mu             sync.Mutex
	Calls          int
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
