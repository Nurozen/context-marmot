package llm

import "context"

// ChatMessage is a single message in a multi-turn chat conversation.
type ChatMessage struct {
	Role    string `json:"role"`    // "system", "user", "assistant"
	Content string `json:"content"`
}

// ChatRequest is the input for a simple (non-streaming) chat completion.
type ChatRequest struct {
	SystemPrompt string
	Messages     []ChatMessage
	MaxTokens    int // 0 = provider default
}

// ChatProvider extends Provider with a simple multi-turn chat method.
// Implementations that only support Classify can leave Chat unimplemented.
type ChatProvider interface {
	Provider
	Chat(ctx context.Context, req ChatRequest) (string, error)
}
