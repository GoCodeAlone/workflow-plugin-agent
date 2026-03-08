package provider

import (
	"context"
	"fmt"
)

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

// copilotModelsProvider uses GitHub Models (models.github.ai) for inference.
type copilotModelsProvider struct {
	config CopilotModelsConfig
}

// NewCopilotModelsProvider creates a provider that uses GitHub Models for inference.
// GitHub Models provides access to various AI models via a fine-grained PAT.
//
// NOT YET IMPLEMENTED — scaffolded for future development.
//
// Docs: https://docs.github.com/en/rest/models/inference
// Billing: https://docs.github.com/billing/managing-billing-for-your-products/about-billing-for-github-models
func NewCopilotModelsProvider(_ CopilotModelsConfig) (*copilotModelsProvider, error) {
	return nil, fmt.Errorf("copilot_models provider not yet implemented: see https://docs.github.com/en/rest/models/inference")
}

func (p *copilotModelsProvider) Name() string { return "copilot_models" }

func (p *copilotModelsProvider) Chat(_ context.Context, _ []Message, _ []ToolDef) (*Response, error) {
	return nil, fmt.Errorf("copilot_models provider not yet implemented")
}

func (p *copilotModelsProvider) Stream(_ context.Context, _ []Message, _ []ToolDef) (<-chan StreamEvent, error) {
	return nil, fmt.Errorf("copilot_models provider not yet implemented")
}

func (p *copilotModelsProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "github_models",
		DisplayName: "GitHub Models",
		Description: "Access AI models via GitHub's Models marketplace using a fine-grained PAT with models:read scope.",
		DocsURL:     "https://docs.github.com/en/rest/models/inference",
		ServerSafe:  true,
	}
}
