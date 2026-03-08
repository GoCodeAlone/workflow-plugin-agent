package provider

const defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"

// OpenRouterConfig configures the OpenRouter provider.
type OpenRouterConfig struct {
	APIKey    string
	Model     string
	BaseURL   string
	MaxTokens int
}

// NewOpenRouterProvider creates a provider that uses OpenRouter's OpenAI-compatible API.
func NewOpenRouterProvider(cfg OpenRouterConfig) *OpenAIProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultOpenRouterBaseURL
	}
	return NewOpenAIProvider(OpenAIConfig{
		APIKey:    cfg.APIKey,
		Model:     cfg.Model,
		BaseURL:   baseURL,
		MaxTokens: cfg.MaxTokens,
	})
}
