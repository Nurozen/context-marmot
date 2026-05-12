package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

type cancellingEmbedder struct{}

func (c cancellingEmbedder) Embed(string) ([]float32, error) {
	panic("Embed should not be called when EmbedContext is available")
}

func (c cancellingEmbedder) EmbedContext(ctx context.Context, _ string) ([]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c cancellingEmbedder) EmbedBatch([]string) ([][]float32, error) {
	panic("EmbedBatch should not be called")
}

func (c cancellingEmbedder) Model() string { return "cancel-test" }

func (c cancellingEmbedder) Dimension() int { return 1536 }

func TestHandleContextQueryHonorsCancellationDuringEmbedding(t *testing.T) {
	engine, err := NewEngine(t.TempDir(), cancellingEmbedder{})
	if err != nil {
		t.Fatalf("create engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := engine.HandleContextQuery(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "context_query",
			Arguments: map[string]any{
				"query": "mcp-engine",
			},
		},
	})
	if err != nil {
		t.Fatalf("HandleContextQuery returned error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected tool error for cancelled query, got %+v", result)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected error content")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok || !strings.Contains(strings.ToLower(text.Text), "canceled") {
		t.Fatalf("expected cancellation text, got %#v", result.Content[0])
	}
}
