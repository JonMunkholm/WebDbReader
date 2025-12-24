// Package llm provides LLM provider integrations for natural language to SQL conversion.
package llm

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Provider defines the interface for LLM integrations.
type Provider interface {
	// GenerateSQL converts a natural language prompt to SQL using the given schema context.
	GenerateSQL(ctx context.Context, req GenerateRequest) (GenerateResponse, error)

	// Name returns the provider name for logging/debugging.
	Name() string
}

// GenerateRequest contains the input for SQL generation.
type GenerateRequest struct {
	Prompt    string // Natural language request from user
	Schema    string // Serialized database schema
	MaxTokens int    // Max tokens for response (0 = provider default)
}

// GenerateResponse contains the result of SQL generation.
type GenerateResponse struct {
	SQL     string // Generated SQL query (empty if missing info)
	Missing string // Explanation if request can't be fulfilled
	Error   string // Error message if generation failed
	Tokens  int    // Tokens used (for cost tracking)
}

// IsMissing returns true if the response indicates missing information.
func (r GenerateResponse) IsMissing() bool {
	return r.Missing != "" && r.SQL == ""
}

// IsError returns true if the response contains an error.
func (r GenerateResponse) IsError() bool {
	return r.Error != ""
}

// Config holds LLM provider configuration.
type Config struct {
	Provider string // "openai" or "anthropic"
	APIKey   string // API key for the provider
	Model    string // Model name (e.g., "gpt-4o", "claude-sonnet-4-20250514")
	BaseURL  string // Base URL (for OpenRouter, proxies, etc.)
}

// ConfigFromEnv reads LLM configuration from environment variables.
func ConfigFromEnv() Config {
	return Config{
		Provider: strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER"))),
		APIKey:   os.Getenv("LLM_API_KEY"),
		Model:    os.Getenv("LLM_MODEL"),
		BaseURL:  os.Getenv("LLM_BASE_URL"),
	}
}

// NewProvider creates an LLM provider based on configuration.
func NewProvider(cfg Config) (Provider, error) {
	if cfg.Provider == "" {
		cfg.Provider = "openai"
	}

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("LLM_API_KEY is required")
	}

	switch cfg.Provider {
	case "openai":
		if cfg.Model == "" {
			cfg.Model = "gpt-4o"
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.openai.com/v1"
		}
		return NewOpenAIProvider(cfg.APIKey, cfg.Model, cfg.BaseURL), nil

	case "anthropic":
		if cfg.Model == "" {
			cfg.Model = "claude-sonnet-4-20250514"
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.anthropic.com/v1"
		}
		return NewAnthropicProvider(cfg.APIKey, cfg.Model, cfg.BaseURL), nil

	default:
		return nil, fmt.Errorf("unknown LLM provider: %q (supported: openai, anthropic)", cfg.Provider)
	}
}

// NewProviderFromEnv creates an LLM provider from environment variables.
func NewProviderFromEnv() (Provider, error) {
	return NewProvider(ConfigFromEnv())
}

// ParseResponse extracts SQL or MISSING from raw LLM output.
func ParseResponse(raw string) GenerateResponse {
	trimmed := strings.TrimSpace(raw)

	// Check for MISSING prefix (case-insensitive)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "MISSING:") {
		missing := strings.TrimSpace(trimmed[8:])
		return GenerateResponse{Missing: missing}
	}

	// Clean up common LLM formatting quirks
	sql := trimmed
	sql = strings.TrimPrefix(sql, "```sql")
	sql = strings.TrimPrefix(sql, "```SQL")
	sql = strings.TrimPrefix(sql, "```")
	sql = strings.TrimSuffix(sql, "```")
	sql = strings.TrimSpace(sql)

	return GenerateResponse{SQL: sql}
}
