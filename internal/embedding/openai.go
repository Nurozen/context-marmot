package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// openaiModelDims maps supported OpenAI embedding models to their output dimensions.
var openaiModelDims = map[string]int{
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
	"text-embedding-ada-002": 1536,
}

const (
	openaiDefaultModel = "text-embedding-3-small"
	openaiEndpoint     = "https://api.openai.com/v1/embeddings"
	openaiMaxBatch     = 100 // chunk size for batch requests
	openaiMaxRetries   = 3
)

// OpenAIEmbedder generates embeddings using the OpenAI API.
type OpenAIEmbedder struct {
	apiKey         string
	model          string
	dimension      int
	client         *http.Client
	initialBackoff time.Duration // for testing; defaults to 1s
}

// NewOpenAIEmbedder creates a new OpenAI embedder.
// If model is empty, it defaults to text-embedding-3-small.
func NewOpenAIEmbedder(apiKey string, model string) (*OpenAIEmbedder, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("openai: API key is required")
	}
	if model == "" {
		model = openaiDefaultModel
	}
	dim, ok := openaiModelDims[model]
	if !ok {
		return nil, fmt.Errorf("openai: unsupported model %q (supported: text-embedding-3-small, text-embedding-3-large, text-embedding-ada-002)", model)
	}
	return &OpenAIEmbedder{
		apiKey:         apiKey,
		model:          model,
		dimension:      dim,
		client:         &http.Client{Timeout: 60 * time.Second},
		initialBackoff: 1 * time.Second,
	}, nil
}

// Model returns the model name.
func (o *OpenAIEmbedder) Model() string {
	return o.model
}

// Dimension returns the embedding dimension for the configured model.
func (o *OpenAIEmbedder) Dimension() int {
	return o.dimension
}

// openaiRequest is the request body for the OpenAI embeddings API.
type openaiRequest struct {
	Input interface{} `json:"input"` // string or []string
	Model string      `json:"model"`
}

// openaiResponse is the response from the OpenAI embeddings API.
type openaiResponse struct {
	Data  []openaiEmbedding `json:"data"`
	Error *openaiError      `json:"error,omitempty"`
}

type openaiEmbedding struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type openaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Embed generates an embedding for a single text.
func (o *OpenAIEmbedder) Embed(text string) ([]float32, error) {
	results, err := o.callAPI([]string{text})
	if err != nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, fmt.Errorf("openai: expected 1 embedding, got %d", len(results))
	}
	return results[0], nil
}

// EmbedBatch generates embeddings for multiple texts, chunking at 100 per request.
func (o *OpenAIEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(texts))

	for start := 0; start < len(texts); start += openaiMaxBatch {
		end := start + openaiMaxBatch
		if end > len(texts) {
			end = len(texts)
		}
		chunk := texts[start:end]

		chunkResults, err := o.callAPI(chunk)
		if err != nil {
			return nil, fmt.Errorf("openai: batch chunk [%d:%d]: %w", start, end, err)
		}
		if len(chunkResults) != len(chunk) {
			return nil, fmt.Errorf("openai: expected %d embeddings, got %d", len(chunk), len(chunkResults))
		}
		copy(results[start:end], chunkResults)
	}

	return results, nil
}

// callAPI sends a request to the OpenAI embeddings endpoint with retry on 429.
func (o *OpenAIEmbedder) callAPI(inputs []string) ([][]float32, error) {
	var input interface{}
	if len(inputs) == 1 {
		input = inputs[0]
	} else {
		input = inputs
	}

	reqBody := openaiRequest{
		Input: input,
		Model: o.model,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	var lastErr error
	backoff := o.initialBackoff

	for attempt := 0; attempt < openaiMaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(backoff)
			backoff *= 2
		}

		req, err := http.NewRequest("POST", openaiEndpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("openai: create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := o.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("openai: network error: %w", err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("openai: read response: %w", err)
			continue
		}

		switch resp.StatusCode {
		case http.StatusOK:
			var apiResp openaiResponse
			if err := json.Unmarshal(respBody, &apiResp); err != nil {
				return nil, fmt.Errorf("openai: unmarshal response: %w", err)
			}

			// Sort by index to ensure correct ordering.
			sort.Slice(apiResp.Data, func(i, j int) bool {
				return apiResp.Data[i].Index < apiResp.Data[j].Index
			})

			embeddings := make([][]float32, len(apiResp.Data))
			for i, d := range apiResp.Data {
				embeddings[i] = d.Embedding
			}
			return embeddings, nil

		case http.StatusTooManyRequests:
			lastErr = fmt.Errorf("openai: rate limited (429)")
			continue // retry

		case http.StatusUnauthorized:
			return nil, fmt.Errorf("openai: unauthorized (401): invalid API key")

		case http.StatusBadRequest:
			var apiResp openaiResponse
			if err := json.Unmarshal(respBody, &apiResp); err == nil && apiResp.Error != nil {
				return nil, fmt.Errorf("openai: bad request (400): %s", apiResp.Error.Message)
			}
			return nil, fmt.Errorf("openai: bad request (400): %s", string(respBody))

		default:
			return nil, fmt.Errorf("openai: unexpected status %d: %s", resp.StatusCode, string(respBody))
		}
	}

	return nil, fmt.Errorf("openai: exhausted retries: %w", lastErr)
}
