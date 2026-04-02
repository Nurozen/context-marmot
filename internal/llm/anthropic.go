package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	anthropicDefaultModel = "claude-haiku-4-5-20251001"
	anthropicEndpoint     = "https://api.anthropic.com/v1/messages"
	anthropicVersion      = "2023-06-01"
	anthropicMaxCandidates = 3
	anthropicContextLimit  = 500
)

const anthropicSystemPrompt = `You are a knowledge graph classifier. Given an incoming node and similar existing nodes, classify the operation as:
- ADD: the incoming node is a genuinely new concept not covered by any candidate
- UPDATE: the incoming node enriches or corrects an existing node (same concept, more/better content)
- SUPERSEDE: the incoming node represents a significantly evolved version that replaces an existing one
- NOOP: the incoming content is essentially identical to an existing node (no change needed)

Respond with only valid JSON matching: {"action":"ADD|UPDATE|SUPERSEDE|NOOP","target_node_id":"<id or empty>","confidence":<0.0-1.0>,"reasoning":"<brief explanation>"}`

// AnthropicProvider classifies nodes using the Anthropic Messages API.
type AnthropicProvider struct {
	apiKey string
	model  string
	client *http.Client
}

// NewAnthropicProvider creates a new AnthropicProvider using the given API key.
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		model:  anthropicDefaultModel,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// Model returns the model name.
func (p *AnthropicProvider) Model() string {
	return p.model
}

// anthropicMessage is a single message in the Anthropic API request.
type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicRequest is the request body for the Anthropic Messages API.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

// anthropicContentBlock is a content block in the Anthropic API response.
type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// anthropicResponse is the response from the Anthropic Messages API.
type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Error   *anthropicError         `json:"error,omitempty"`
}

// anthropicError is an error from the Anthropic API.
type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// classifyJSON is the expected JSON shape returned by the LLM.
type classifyJSON struct {
	Action       string  `json:"action"`
	TargetNodeID string  `json:"target_node_id"`
	Confidence   float64 `json:"confidence"`
	Reasoning    string  `json:"reasoning"`
}

// Classify sends a classification request to the Anthropic API.
func (p *AnthropicProvider) Classify(ctx context.Context, req ClassifyRequest) (ClassifyResult, error) {
	userMsg := buildUserMessage(req)

	apiReq := anthropicRequest{
		Model:     p.model,
		MaxTokens: 512,
		System:    anthropicSystemPrompt,
		Messages: []anthropicMessage{
			{Role: "user", Content: userMsg},
		},
	}

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return fallbackResult(), fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return fallbackResult(), fmt.Errorf("anthropic: create request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fallbackResult(), fmt.Errorf("anthropic: network error: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fallbackResult(), fmt.Errorf("anthropic: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiResp anthropicResponse
		if err := json.Unmarshal(respBody, &apiResp); err == nil && apiResp.Error != nil {
			return fallbackResult(), fmt.Errorf("anthropic: API error (%d): %s", resp.StatusCode, apiResp.Error.Message)
		}
		return fallbackResult(), fmt.Errorf("anthropic: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fallbackResult(), fmt.Errorf("anthropic: unmarshal response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return fallbackResult(), fmt.Errorf("anthropic: empty content in response")
	}

	return parseClassifyJSON(apiResp.Content[0].Text), nil
}

// Ensure AnthropicProvider implements Summarizer.
var _ Summarizer = (*AnthropicProvider)(nil)

// Summarize generates a namespace summary using the Anthropic Messages API.
func (p *AnthropicProvider) Summarize(ctx context.Context, req SummarizeRequest) (string, error) {
	userMsg := buildSummarizeUserMessage(req)

	apiReq := anthropicRequest{
		Model:     p.model,
		MaxTokens: 1024,
		System:    summarizeSystemPrompt,
		Messages: []anthropicMessage{
			{Role: "user", Content: userMsg},
		},
	}

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return "", fmt.Errorf("anthropic: marshal summarize request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("anthropic: create summarize request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("anthropic: summarize network error: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("anthropic: read summarize response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiResp anthropicResponse
		if err := json.Unmarshal(respBody, &apiResp); err == nil && apiResp.Error != nil {
			return "", fmt.Errorf("anthropic: summarize API error (%d): %s", resp.StatusCode, apiResp.Error.Message)
		}
		return "", fmt.Errorf("anthropic: summarize unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("anthropic: unmarshal summarize response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("anthropic: empty content in summarize response")
	}

	return apiResp.Content[0].Text, nil
}

const summarizeSystemPrompt = `You are a knowledge graph summarizer. Given a list of nodes in a namespace, synthesize a concise summary.

Rules:
- Use [[wikilinks]] to reference key nodes (e.g. [[auth/login]])
- Organize the summary by major themes or subsystems
- Focus on relationships between nodes and their purpose, not implementation details
- Keep the output under 2000 characters
- Use clear, technical prose`

const summarizeMaxNodes = 50
const summarizeSummaryLimit = 200
const summarizeMaxEdges = 5

// buildSummarizeUserMessage constructs the user prompt for summary generation.
func buildSummarizeUserMessage(req SummarizeRequest) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Namespace: %s\n\n", req.Namespace)
	sb.WriteString("Nodes:\n")

	nodes := req.Nodes
	if len(nodes) > summarizeMaxNodes {
		nodes = nodes[:summarizeMaxNodes]
	}

	for _, n := range nodes {
		summary := n.Summary
		if len(summary) > summarizeSummaryLimit {
			summary = summary[:summarizeSummaryLimit]
		}
		edges := n.Edges
		if len(edges) > summarizeMaxEdges {
			edges = edges[:summarizeMaxEdges]
		}
		fmt.Fprintf(&sb, "- ID: %s | Type: %s | Summary: %s", n.ID, n.Type, summary)
		if len(edges) > 0 {
			fmt.Fprintf(&sb, " | Edges: %s", strings.Join(edges, ", "))
		}
		sb.WriteByte('\n')
	}

	sb.WriteString("\nSynthesize a concise namespace summary from these nodes.")
	return sb.String()
}

// buildUserMessage constructs the user prompt from the classify request.
func buildUserMessage(req ClassifyRequest) string {
	var sb strings.Builder

	sb.WriteString("Incoming node:\n")
	if req.Incoming != nil {
		fmt.Fprintf(&sb, "  ID: %s\n", req.Incoming.ID)
		fmt.Fprintf(&sb, "  Type: %s\n", req.Incoming.Type)
		fmt.Fprintf(&sb, "  Summary: %s\n", req.Incoming.Summary)
		ctx := req.Incoming.Context
		if len(ctx) > anthropicContextLimit {
			ctx = ctx[:anthropicContextLimit]
		}
		fmt.Fprintf(&sb, "  Context: %s\n", ctx)
	}

	candidates := req.Candidates
	if len(candidates) > anthropicMaxCandidates {
		candidates = candidates[:anthropicMaxCandidates]
	}

	if len(candidates) == 0 {
		sb.WriteString("\nNo similar existing nodes found.\n")
	} else {
		sb.WriteString("\nSimilar existing nodes:\n")
		for i, c := range candidates {
			fmt.Fprintf(&sb, "  [%d] ID: %s\n", i+1, c.Node.ID)
			fmt.Fprintf(&sb, "      Summary: %s\n", c.Node.Summary)
			fmt.Fprintf(&sb, "      Similarity score: %.4f\n", c.Score)
		}
	}

	sb.WriteString("\nClassify the operation and respond with only valid JSON.")
	return sb.String()
}

// parseClassifyJSON parses the LLM text output into a ClassifyResult.
// On failure it returns a safe ADD fallback with low confidence.
func parseClassifyJSON(text string) ClassifyResult {
	// Strip any markdown code fences the model may have added.
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "{"); idx > 0 {
		text = text[idx:]
	}
	if idx := strings.LastIndex(text, "}"); idx >= 0 && idx < len(text)-1 {
		text = text[:idx+1]
	}

	var parsed classifyJSON
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return fallbackResult()
	}

	action := Action(strings.ToUpper(parsed.Action))
	switch action {
	case ActionADD, ActionUPDATE, ActionSUPERSEDE, ActionNOOP:
		// valid
	default:
		return fallbackResult()
	}

	return ClassifyResult{
		Action:       action,
		TargetNodeID: parsed.TargetNodeID,
		Confidence:   parsed.Confidence,
		Reasoning:    parsed.Reasoning,
	}
}

// fallbackResult returns a safe ADD result with low confidence.
func fallbackResult() ClassifyResult {
	return ClassifyResult{
		Action:     ActionADD,
		Confidence: 0.1,
		Reasoning:  "fallback: could not parse LLM response",
	}
}
