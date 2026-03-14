package provider

import (
	"context"
	"fmt"
	"net/http"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

const defaultAzureOpenAIAPIVersion = "2024-10-21"

// OpenAIAzureConfig configures the OpenAI provider for Azure OpenAI Service.
// Uses Azure API keys or Entra ID tokens. URLs follow the pattern:
// {resource}.openai.azure.com/openai/deployments/{deployment}/chat/completions?api-version={version}
type OpenAIAzureConfig struct {
	// Resource is the Azure OpenAI resource name.
	Resource string
	// DeploymentName is the model deployment name in Azure.
	DeploymentName string
	// APIVersion is the Azure API version (e.g. "2024-10-21").
	APIVersion string
	// MaxTokens limits the response length.
	MaxTokens int
	// APIKey is the Azure API key (use this OR Entra ID token, not both).
	APIKey string
	// EntraToken is a Microsoft Entra ID bearer token (optional, alternative to APIKey).
	EntraToken string
	// HTTPClient overrides the default HTTP client.
	HTTPClient *http.Client
	// BaseURL overrides the computed Azure endpoint (used in tests).
	BaseURL string
}

// OpenAIAzureProvider accesses OpenAI models via Azure OpenAI Service.
type OpenAIAzureProvider struct {
	client openaisdk.Client
	config OpenAIAzureConfig
}

func (p *OpenAIAzureProvider) Name() string { return "openai_azure" }

func (p *OpenAIAzureProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "azure",
		DisplayName: "OpenAI (Azure OpenAI Service)",
		Description: "Access OpenAI models via Azure OpenAI Service using Azure API keys or Microsoft Entra ID tokens. Uses deployment-specific URLs.",
		DocsURL:     "https://learn.microsoft.com/en-us/azure/ai-services/openai/reference",
		ServerSafe:  true,
	}
}

// NewOpenAIAzureProvider creates a provider that accesses OpenAI models via Azure.
//
// Docs: https://learn.microsoft.com/en-us/azure/ai-services/openai/reference
func NewOpenAIAzureProvider(cfg OpenAIAzureConfig) (*OpenAIAzureProvider, error) {
	if cfg.Resource == "" && cfg.BaseURL == "" {
		return nil, fmt.Errorf("openai_azure: Resource is required")
	}
	if cfg.DeploymentName == "" && cfg.BaseURL == "" {
		return nil, fmt.Errorf("openai_azure: DeploymentName is required")
	}
	if cfg.APIKey == "" && cfg.EntraToken == "" {
		return nil, fmt.Errorf("openai_azure: APIKey or EntraToken is required")
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = defaultAzureOpenAIAPIVersion
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultOpenAIMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://%s.openai.azure.com/openai/deployments/%s",
			cfg.Resource, cfg.DeploymentName)
	}

	opts := []option.RequestOption{
		// Use a placeholder API key to prevent OPENAI_API_KEY env var from being used,
		// then remove the resulting Authorization header for Azure auth.
		option.WithAPIKey("azure-placeholder"),
		option.WithHeaderDel("authorization"),
		option.WithBaseURL(baseURL),
		option.WithQuery("api-version", cfg.APIVersion),
		option.WithHTTPClient(cfg.HTTPClient),
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithHeader("api-key", cfg.APIKey))
	} else if cfg.EntraToken != "" {
		opts = append(opts, option.WithHeader("Authorization", "Bearer "+cfg.EntraToken))
	}

	client := openaisdk.NewClient(opts...)
	return &OpenAIAzureProvider{client: client, config: cfg}, nil
}

func (p *OpenAIAzureProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	params := openaisdk.ChatCompletionNewParams{
		Model:     shared.ChatModel(p.config.DeploymentName),
		Messages:  toOpenAIMessages(messages),
		MaxTokens: openaisdk.Int(int64(p.config.MaxTokens)),
	}
	if len(tools) > 0 {
		params.Tools = toOpenAITools(tools)
	}
	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai_azure: %w", err)
	}
	return fromOpenAIResponse(resp)
}

func (p *OpenAIAzureProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	params := openaisdk.ChatCompletionNewParams{
		Model:     shared.ChatModel(p.config.DeploymentName),
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
