package llm

import "context"

// MockProvider returns a configurable fixed result. Used in tests.
type MockProvider struct {
	Result ClassifyResult
	Err    error
	Calls  int
}

func (m *MockProvider) Classify(_ context.Context, _ ClassifyRequest) (ClassifyResult, error) {
	m.Calls++
	return m.Result, m.Err
}

func (m *MockProvider) Model() string { return "mock" }
