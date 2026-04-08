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
	openaiResponsesURL    = "https://api.openai.com/v1/responses"
)

// OpenAIProvider classifies/summarises nodes using the OpenAI Responses API.
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

// ── Responses API types ─────────────────────────────────────

// openaiInputMessage is a single message in the Responses API input array.
type openaiInputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openaiResponsesRequest is the request body for the OpenAI Responses API.
type openaiResponsesRequest struct {
	Model     string               `json:"model"`
	MaxTokens int                  `json:"max_output_tokens,omitempty"`
	Input     []openaiInputMessage `json:"input"`
}

// openaiContentPart is a single part in an output message's content array.
type openaiContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// openaiOutputItem is a single item in the Responses API output array.
type openaiOutputItem struct {
	Type    string              `json:"type"`
	Content []openaiContentPart `json:"content,omitempty"`
}

// openaiResponsesResponse is the response from the OpenAI Responses API.
type openaiResponsesResponse struct {
	Output []openaiOutputItem `json:"output,omitempty"`
	Error  *openaiAPIError    `json:"error,omitempty"`
}

// openaiAPIError is an error from the OpenAI API.
type openaiAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// extractText walks the Responses API output and returns the first text block.
func (r *openaiResponsesResponse) extractText() string {
	for _, item := range r.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" && part.Text != "" {
				return part.Text
			}
		}
	}
	return ""
}

// ── Public methods ──────────────────────────────────────────

// Classify sends a classification request to the OpenAI Responses API.
func (p *OpenAIProvider) Classify(ctx context.Context, req ClassifyRequest) (ClassifyResult, error) {
	userMsg := buildUserMessage(req)

	apiReq := openaiResponsesRequest{
		Model:     p.model,
		MaxTokens: 512,
		Input: []openaiInputMessage{
			{Role: "system", Content: anthropicSystemPrompt},
			{Role: "user", Content: userMsg},
		},
	}

	text, err := p.doRequest(ctx, apiReq, "classify")
	if err != nil {
		return fallbackResult(), err
	}

	return parseClassifyJSON(text), nil
}

// Summarize generates a namespace summary using the OpenAI Responses API.
func (p *OpenAIProvider) Summarize(ctx context.Context, req SummarizeRequest) (string, error) {
	userMsg := buildSummarizeUserMessage(req)

	apiReq := openaiResponsesRequest{
		Model:     p.model,
		MaxTokens: 1024,
		Input: []openaiInputMessage{
			{Role: "system", Content: summarizeSystemPrompt},
			{Role: "user", Content: userMsg},
		},
	}

	return p.doRequest(ctx, apiReq, "summarize")
}

// ── HTTP helper ─────────────────────────────────────────────

func (p *OpenAIProvider) doRequest(ctx context.Context, apiReq openaiResponsesRequest, label string) (string, error) {
	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return "", fmt.Errorf("openai: marshal %s request: %w", label, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiResponsesURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("openai: create %s request: %w", label, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("openai: %s network error: %w", label, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("openai: read %s response: %w", label, err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiResp openaiResponsesResponse
		if err := json.Unmarshal(respBody, &apiResp); err == nil && apiResp.Error != nil {
			return "", fmt.Errorf("openai: %s API error (%d): %s", label, resp.StatusCode, apiResp.Error.Message)
		}
		return "", fmt.Errorf("openai: %s unexpected status %d: %s", label, resp.StatusCode, string(respBody))
	}

	var apiResp openaiResponsesResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("openai: unmarshal %s response: %w", label, err)
	}

	text := apiResp.extractText()
	if text == "" {
		return "", fmt.Errorf("openai: empty output in %s response", label)
	}

	return text, nil
}
