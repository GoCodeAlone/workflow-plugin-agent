package provider

const defaultCopilotModelsBaseURL = "https://models.github.ai/inference"

// CopilotModelsConfig configures the GitHub Models provider.
// GitHub Models is a separate product from GitHub Copilot, available at models.github.ai.
// It uses fine-grained Personal Access Tokens with the models:read scope.
type CopilotModelsConfig struct {
	// Token is a GitHub fine-grained PAT with models:read permission.
	Token string
	// Model is the model identifier (e.g. "openai/gpt-4o", "anthropic/claude-sonnet-4").
	Model string
	// BaseURL overrides the default endpoint. Default: "https://models.github.ai/inference".
	BaseURL string
	// MaxTokens limits the response length.
	MaxTokens int
}

// CopilotModelsProvider uses GitHub Models (models.github.ai) for inference.
// It wraps OpenAIProvider since GitHub Models uses an OpenAI-compatible API.
type CopilotModelsProvider struct {
	*OpenAIProvider
}

func (p *CopilotModelsProvider) Name() string { return "copilot_models" }

func (p *CopilotModelsProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "github_models",
		DisplayName: "GitHub Models",
		Description: "Access AI models via GitHub's Models marketplace using a fine-grained PAT with models:read scope.",
		DocsURL:     "https://docs.github.com/en/rest/models/inference",
		ServerSafe:  true,
	}
}

// NewCopilotModelsProvider creates a provider that uses GitHub Models for inference.
// GitHub Models provides access to various AI models via a fine-grained PAT.
//
// Docs: https://docs.github.com/en/rest/models/inference
// Billing: https://docs.github.com/billing/managing-billing-for-your-products/about-billing-for-github-models
func NewCopilotModelsProvider(cfg CopilotModelsConfig) *CopilotModelsProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultCopilotModelsBaseURL
	}
	return &CopilotModelsProvider{
		OpenAIProvider: NewOpenAIProvider(OpenAIConfig{
			APIKey:    cfg.Token,
			Model:     cfg.Model,
			BaseURL:   baseURL,
			MaxTokens: cfg.MaxTokens,
		}),
	}
}
