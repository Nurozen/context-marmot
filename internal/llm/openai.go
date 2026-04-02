package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Ensure OpenAIProvider implements Summarizer.
var _ Summarizer = (*OpenAIProvider)(nil)

const (
	openaiLLMDefaultModel = "gpt-5.1-codex-mini"
	openaiChatEndpoint    = "https://api.openai.com/v1/chat/completions"
)

// OpenAIProvider classifies nodes using the OpenAI Chat Completions API.
type OpenAIProvider struct {
	apiKey string
	model  string
	client *http.Client
}

// NewOpenAIProvider creates a new OpenAIProvider using the given API key.
func NewOpenAIProvider(apiKey string) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey: apiKey,
		model:  openaiLLMDefaultModel,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// Model returns the model name.
func (p *OpenAIProvider) Model() string {
	return p.model
}

// openaiChatMessage is a single message in the OpenAI chat request.
type openaiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openaiChatRequest is the request body for the OpenAI Chat Completions API.
type openaiChatRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	Messages  []openaiChatMessage `json:"messages"`
}

// openaiChatChoice is a single choice in the OpenAI chat response.
type openaiChatChoice struct {
	Message openaiChatMessage `json:"message"`
}

// openaiChatResponse is the response from the OpenAI Chat Completions API.
type openaiChatResponse struct {
	Choices []openaiChatChoice `json:"choices"`
	Error   *openaiChatError   `json:"error,omitempty"`
}

// openaiChatError is an error from the OpenAI API.
type openaiChatError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Classify sends a classification request to the OpenAI Chat Completions API.
func (p *OpenAIProvider) Classify(ctx context.Context, req ClassifyRequest) (ClassifyResult, error) {
	userMsg := buildUserMessage(req)

	apiReq := openaiChatRequest{
		Model:     p.model,
		MaxTokens: 512,
		Messages: []openaiChatMessage{
			{Role: "system", Content: anthropicSystemPrompt},
			{Role: "user", Content: userMsg},
		},
	}

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return fallbackResult(), fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiChatEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return fallbackResult(), fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fallbackResult(), fmt.Errorf("openai: network error: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fallbackResult(), fmt.Errorf("openai: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiResp openaiChatResponse
		if err := json.Unmarshal(respBody, &apiResp); err == nil && apiResp.Error != nil {
			return fallbackResult(), fmt.Errorf("openai: API error (%d): %s", resp.StatusCode, apiResp.Error.Message)
		}
		return fallbackResult(), fmt.Errorf("openai: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp openaiChatResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fallbackResult(), fmt.Errorf("openai: unmarshal response: %w", err)
	}

	if len(apiResp.Choices) == 0 {
		return fallbackResult(), fmt.Errorf("openai: empty choices in response")
	}

	return parseClassifyJSON(apiResp.Choices[0].Message.Content), nil
}

// Summarize generates a namespace summary using the OpenAI Chat Completions API.
func (p *OpenAIProvider) Summarize(ctx context.Context, req SummarizeRequest) (string, error) {
	userMsg := buildSummarizeUserMessage(req)

	apiReq := openaiChatRequest{
		Model:     p.model,
		MaxTokens: 1024,
		Messages: []openaiChatMessage{
			{Role: "system", Content: summarizeSystemPrompt},
			{Role: "user", Content: userMsg},
		},
	}

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return "", fmt.Errorf("openai: marshal summarize request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiChatEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("openai: create summarize request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("openai: summarize network error: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("openai: read summarize response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiResp openaiChatResponse
		if err := json.Unmarshal(respBody, &apiResp); err == nil && apiResp.Error != nil {
			return "", fmt.Errorf("openai: summarize API error (%d): %s", resp.StatusCode, apiResp.Error.Message)
		}
		return "", fmt.Errorf("openai: summarize unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp openaiChatResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("openai: unmarshal summarize response: %w", err)
	}

	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("openai: empty choices in summarize response")
	}

	return apiResp.Choices[0].Message.Content, nil
}
