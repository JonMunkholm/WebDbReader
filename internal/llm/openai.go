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

// OpenAIProvider implements the Provider interface for OpenAI-compatible APIs.
// This works with OpenAI, OpenRouter, Together.ai, Groq, and other compatible services.
type OpenAIProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewOpenAIProvider creates a new OpenAI-compatible provider.
func NewOpenAIProvider(apiKey, model, baseURL string) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Name returns the provider name.
func (p *OpenAIProvider) Name() string {
	return "openai"
}

// GenerateSQL sends a prompt to the OpenAI API and returns the generated SQL.
func (p *OpenAIProvider) GenerateSQL(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	systemPrompt := BuildSystemPrompt(req.Schema)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	payload := openAIRequest{
		Model: p.model,
		Messages: []openAIMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: req.Prompt},
		},
		MaxCompletionTokens: maxTokens,
		Temperature:         0, // Deterministic for SQL generation
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return GenerateResponse{Error: "failed to marshal request"}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return GenerateResponse{Error: "failed to create request"}, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return GenerateResponse{Error: "request failed"}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return GenerateResponse{Error: "failed to read response"}, err
	}

	if resp.StatusCode != http.StatusOK {
		var errResp openAIErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return GenerateResponse{Error: errResp.Error.Message}, fmt.Errorf("API error: %s", errResp.Error.Message)
		}
		return GenerateResponse{Error: fmt.Sprintf("API returned status %d", resp.StatusCode)}, fmt.Errorf("API error: status %d", resp.StatusCode)
	}

	var result openAIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return GenerateResponse{Error: "failed to parse response"}, err
	}

	if len(result.Choices) == 0 {
		return GenerateResponse{Error: "no response from model"}, fmt.Errorf("empty choices array")
	}

	content := result.Choices[0].Message.Content
	genResp := ParseResponse(content)
	genResp.Tokens = result.Usage.TotalTokens

	return genResp, nil
}

// OpenAI API request/response types

type openAIRequest struct {
	Model               string          `json:"model"`
	Messages            []openAIMessage `json:"messages"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         float64         `json:"temperature"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
}

type openAIChoice struct {
	Message openAIMessage `json:"message"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}
