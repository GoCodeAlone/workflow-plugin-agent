package provider

const defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"

// OpenRouterConfig configures the OpenRouter provider.
type OpenRouterConfig struct {
	APIKey    string
	Model     string
	BaseURL   string
	MaxTokens int
}

// OpenRouterProvider wraps OpenAIProvider with OpenRouter-specific identity and auth info.
type OpenRouterProvider struct {
	*OpenAIProvider
}

func (p *OpenRouterProvider) Name() string { return "openrouter" }

func (p *OpenRouterProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "openrouter",
		DisplayName: "OpenRouter",
		Description: "Access multiple AI models via OpenRouter's unified API.",
		DocsURL:     "https://openrouter.ai/docs/api/reference/authentication",
		ServerSafe:  true,
	}
}

// NewOpenRouterProvider creates a provider that uses OpenRouter's OpenAI-compatible API.
func NewOpenRouterProvider(cfg OpenRouterConfig) *OpenRouterProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultOpenRouterBaseURL
	}
	return &OpenRouterProvider{
		OpenAIProvider: NewOpenAIProvider(OpenAIConfig{
			APIKey:    cfg.APIKey,
			Model:     cfg.Model,
			BaseURL:   baseURL,
			MaxTokens: cfg.MaxTokens,
		}),
	}
}
