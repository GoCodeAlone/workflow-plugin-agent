package provider

import (
	"context"
	"fmt"
)

// AnthropicFoundryConfig configures the Anthropic provider for Microsoft Azure AI Foundry.
// Uses Azure API keys or Entra ID (formerly Azure AD) tokens.
type AnthropicFoundryConfig struct {
	// Resource is the Azure AI Services resource name (forms the URL: {resource}.services.ai.azure.com).
	Resource string
	// Model is the model deployment name.
	Model string
	// MaxTokens limits the response length.
	MaxTokens int
	// APIKey is the Azure API key (use this OR Entra ID token, not both).
	APIKey string
	// EntraToken is a Microsoft Entra ID bearer token (optional, alternative to APIKey).
	EntraToken string
}

// anthropicFoundryProvider accesses Anthropic models via Azure AI Foundry.
type anthropicFoundryProvider struct {
	config AnthropicFoundryConfig
}

// NewAnthropicFoundryProvider creates a provider that accesses Claude via Azure AI Foundry.
//
// NOT YET IMPLEMENTED — scaffolded for future development.
//
// Docs: https://platform.claude.com/docs/en/build-with-claude/claude-in-microsoft-foundry
func NewAnthropicFoundryProvider(_ AnthropicFoundryConfig) (*anthropicFoundryProvider, error) {
	return nil, fmt.Errorf("anthropic_foundry provider not yet implemented: see https://platform.claude.com/docs/en/build-with-claude/claude-in-microsoft-foundry")
}

func (p *anthropicFoundryProvider) Name() string { return "anthropic_foundry" }

func (p *anthropicFoundryProvider) Chat(_ context.Context, _ []Message, _ []ToolDef) (*Response, error) {
	return nil, fmt.Errorf("anthropic_foundry provider not yet implemented")
}

func (p *anthropicFoundryProvider) Stream(_ context.Context, _ []Message, _ []ToolDef) (<-chan StreamEvent, error) {
	return nil, fmt.Errorf("anthropic_foundry provider not yet implemented")
}

func (p *anthropicFoundryProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "foundry",
		DisplayName: "Anthropic (Azure AI Foundry)",
		Description: "Access Claude models via Microsoft Azure AI Foundry using Azure API keys or Microsoft Entra ID tokens.",
		DocsURL:     "https://platform.claude.com/docs/en/build-with-claude/claude-in-microsoft-foundry",
		ServerSafe:  true,
	}
}
