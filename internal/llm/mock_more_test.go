package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestMockProvider_Model(t *testing.T) {
	if got := (&MockProvider{}).Model(); got != "mock" {
		t.Errorf("Model() = %q, want mock", got)
	}
}

func TestMockProvider_SummarizeDefault(t *testing.T) {
	m := &MockProvider{}
	out, err := m.Summarize(context.Background(), SummarizeRequest{
		Namespace: "auth",
		Nodes:     []NodeSummaryInput{{ID: "auth/login", Type: "concept"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Summary of namespace auth with 1 nodes") {
		t.Errorf("default summary missing header: %q", out)
	}
	if !strings.Contains(out, "[[auth/login]]") {
		t.Errorf("default summary missing node link: %q", out)
	}
	if m.GetSummarizeCalls() != 1 {
		t.Errorf("GetSummarizeCalls() = %d, want 1", m.GetSummarizeCalls())
	}
}

func TestMockProvider_SummarizeConfigured(t *testing.T) {
	sentinel := errors.New("boom")
	m := &MockProvider{SummaryResult: "fixed", SummaryErr: sentinel}
	out, err := m.Summarize(context.Background(), SummarizeRequest{})
	if out != "fixed" || !errors.Is(err, sentinel) {
		t.Errorf("got (%q, %v)", out, err)
	}
}

func TestMockProvider_SummarizeStopsAtLimit(t *testing.T) {
	// Feed enough long-ID nodes that the 500-char builder cap trips and breaks
	// the loop early.
	nodes := make([]NodeSummaryInput, 200)
	for i := range nodes {
		nodes[i] = NodeSummaryInput{ID: strings.Repeat("x", 40), Type: "t"}
	}
	out, _ := (&MockProvider{}).Summarize(context.Background(), SummarizeRequest{Namespace: "ns", Nodes: nodes})
	if strings.Count(out, "[[") >= len(nodes) {
		t.Errorf("expected early break under length cap, rendered all %d nodes", len(nodes))
	}
}

func TestMockProvider_ChatQueueThenFallback(t *testing.T) {
	m := &MockProvider{ChatResults: []string{"first", "second"}}
	for _, want := range []string{"first", "second"} {
		got, err := m.Chat(context.Background(), ChatRequest{})
		if err != nil || got != want {
			t.Fatalf("got (%q, %v), want %q", got, err, want)
		}
	}
	// Queue drained -> falls back to default echo of last user message.
	got, _ := m.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "ping"}}})
	if !strings.Contains(got, "You said: ping") {
		t.Errorf("expected echo fallback, got %q", got)
	}
	if m.ChatCalls != 3 {
		t.Errorf("ChatCalls = %d, want 3", m.ChatCalls)
	}
}

func TestMockProvider_ChatFixedResult(t *testing.T) {
	m := &MockProvider{ChatResult: "canned"}
	got, err := m.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}})
	if err != nil || got != "canned" {
		t.Errorf("got (%q, %v), want canned", got, err)
	}
}

func TestMockProvider_ChatNoUserMessage(t *testing.T) {
	m := &MockProvider{}
	got, _ := m.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "assistant", Content: "x"}}})
	if !strings.Contains(got, "How can I help") {
		t.Errorf("expected default greeting, got %q", got)
	}
}
