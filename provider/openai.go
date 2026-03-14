package provider

import (
	"context"
	"fmt"
	"net/http"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

const (
	defaultOpenAIBaseURL   = "https://api.openai.com"
	defaultOpenAIModel     = "gpt-4o"
	defaultOpenAIMaxTokens = 4096
)

// OpenAIConfig holds configuration for the OpenAI provider.
type OpenAIConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// OpenAIProvider implements Provider using the OpenAI Chat Completions API.
type OpenAIProvider struct {
	client openaisdk.Client
	config OpenAIConfig
}

// NewOpenAIProvider creates a new OpenAI provider with the given config.
func NewOpenAIProvider(cfg OpenAIConfig) *OpenAIProvider {
	if cfg.Model == "" {
		cfg.Model = defaultOpenAIModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultOpenAIBaseURL
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultOpenAIMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	client := openaisdk.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithHTTPClient(cfg.HTTPClient),
	)
	return &OpenAIProvider{client: client, config: cfg}
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "direct",
		DisplayName: "OpenAI (Direct API)",
		Description: "Direct access to OpenAI models via API key.",
		DocsURL:     "https://platform.openai.com/docs/api-reference/introduction",
		ServerSafe:  true,
	}
}

func (p *OpenAIProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	params := openaisdk.ChatCompletionNewParams{
		Model:     shared.ChatModel(p.config.Model),
		Messages:  toOpenAIMessages(messages),
		MaxTokens: openaisdk.Int(int64(p.config.MaxTokens)),
	}
	if len(tools) > 0 {
		params.Tools = toOpenAITools(tools)
	}
	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	return fromOpenAIResponse(resp)
}

func (p *OpenAIProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	params := openaisdk.ChatCompletionNewParams{
		Model:     shared.ChatModel(p.config.Model),
		Messages:  toOpenAIMessages(messages),
		MaxTokens: openaisdk.Int(int64(p.config.MaxTokens)),
	}
	if len(tools) > 0 {
		params.Tools = toOpenAITools(tools)
	}
	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	ch := make(chan StreamEvent, 16)
	go streamOpenAIEvents(stream, ch)
	return ch, nil
}
