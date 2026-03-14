package provider

import (
	"context"
	"fmt"
	"net/http"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const (
	defaultAnthropicBaseURL   = "https://api.anthropic.com"
	defaultAnthropicModel     = "claude-sonnet-4-20250514"
	defaultAnthropicMaxTokens = 4096
	anthropicAPIVersion       = "2023-06-01"
)

// AnthropicConfig holds configuration for the Anthropic provider.
type AnthropicConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// AnthropicProvider implements Provider using the Anthropic Messages API.
type AnthropicProvider struct {
	client anthropic.Client
	config AnthropicConfig
}

// NewAnthropicProvider creates a new Anthropic provider with the given config.
func NewAnthropicProvider(cfg AnthropicConfig) *AnthropicProvider {
	if cfg.Model == "" {
		cfg.Model = defaultAnthropicModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultAnthropicBaseURL
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultAnthropicMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	client := anthropic.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithHTTPClient(cfg.HTTPClient),
	)
	return &AnthropicProvider{client: client, config: cfg}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "direct",
		DisplayName: "Anthropic (Direct API)",
		Description: "Direct access to Anthropic's Claude models via API key.",
		DocsURL:     "https://platform.claude.com/docs/en/api/getting-started",
		ServerSafe:  true,
	}
}

func (p *AnthropicProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	params := toAnthropicParams(p.config.Model, p.config.MaxTokens, messages, tools)
	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return fromAnthropicMessage(msg), nil
}

func (p *AnthropicProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	params := toAnthropicParams(p.config.Model, p.config.MaxTokens, messages, tools)
	stream := p.client.Messages.NewStreaming(ctx, params)
	if stream.Err() != nil {
		return nil, fmt.Errorf("anthropic: %w", stream.Err())
	}
	ch := make(chan StreamEvent, 16)
	go streamAnthropicEvents(stream, ch)
	return ch, nil
}

// JSON types used by anthropic_foundry.go (hand-rolled HTTP, no SDK support).

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicContent struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
	CacheCtrl *cacheControl  `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicResponse struct {
	ID      string              `json:"id"`
	Type    string              `json:"type"`
	Content []anthropicRespItem `json:"content"`
	Usage   anthropicUsage      `json:"usage"`
	Error   *anthropicError     `json:"error,omitempty"`
}

type anthropicRespItem struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
