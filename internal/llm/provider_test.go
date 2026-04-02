package llm

import (
	"context"
	"errors"
	"testing"
)

func TestMockProvider_CountsCalls(t *testing.T) {
	m := &MockProvider{}
	ctx := context.Background()

	_, _ = m.Classify(ctx, ClassifyRequest{})
	_, _ = m.Classify(ctx, ClassifyRequest{})

	if m.Calls != 2 {
		t.Errorf("expected Calls == 2, got %d", m.Calls)
	}
}

func TestMockProvider_ReturnsConfiguredResult(t *testing.T) {
	want := ClassifyResult{
		Action:       ActionSUPERSEDE,
		TargetNodeID: "node-123",
		Confidence:   0.95,
		Reasoning:    "test",
	}
	m := &MockProvider{Result: want}

	got, err := m.Classify(context.Background(), ClassifyRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

func TestMockProvider_ReturnsError(t *testing.T) {
	sentinel := errors.New("classify failed")
	m := &MockProvider{Err: sentinel}

	_, err := m.Classify(context.Background(), ClassifyRequest{})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}
