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

const anthropicAPIVersion = "2023-06-01"

// AnthropicProvider implements the Provider interface for Anthropic's Claude API.
type AnthropicProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewAnthropicProvider creates a new Anthropic provider.
func NewAnthropicProvider(apiKey, model, baseURL string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Name returns the provider name.
func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

// GenerateSQL sends a prompt to the Anthropic API and returns the generated SQL.
func (p *AnthropicProvider) GenerateSQL(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	systemPrompt := BuildSystemPrompt(req.Schema)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	payload := anthropicRequest{
		Model:     p.model,
		System:    systemPrompt,
		MaxTokens: maxTokens,
		Messages: []anthropicMessage{
			{Role: "user", Content: req.Prompt},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return GenerateResponse{Error: "failed to marshal request"}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return GenerateResponse{Error: "failed to create request"}, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

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
		var errResp anthropicErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return GenerateResponse{Error: errResp.Error.Message}, fmt.Errorf("API error: %s", errResp.Error.Message)
		}
		return GenerateResponse{Error: fmt.Sprintf("API returned status %d", resp.StatusCode)}, fmt.Errorf("API error: status %d", resp.StatusCode)
	}

	var result anthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return GenerateResponse{Error: "failed to parse response"}, err
	}

	if len(result.Content) == 0 {
		return GenerateResponse{Error: "no response from model"}, fmt.Errorf("empty content array")
	}

	// Find the first text block
	var content string
	for _, block := range result.Content {
		if block.Type == "text" {
			content = block.Text
			break
		}
	}

	if content == "" {
		return GenerateResponse{Error: "no text in response"}, fmt.Errorf("no text content")
	}

	genResp := ParseResponse(content)
	genResp.Tokens = result.Usage.InputTokens + result.Usage.OutputTokens

	return genResp, nil
}

// Anthropic API request/response types

type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []anthropicContent `json:"content"`
	Usage   anthropicUsage     `json:"usage"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicErrorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
