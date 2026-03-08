package provider

import (
	"context"
	"fmt"
)

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
}

// openaiAzureProvider accesses OpenAI models via Azure OpenAI Service.
type openaiAzureProvider struct {
	config OpenAIAzureConfig
}

// NewOpenAIAzureProvider creates a provider that accesses OpenAI models via Azure.
//
// NOT YET IMPLEMENTED — scaffolded for future development.
//
// Docs: https://learn.microsoft.com/en-us/azure/ai-services/openai/reference
func NewOpenAIAzureProvider(_ OpenAIAzureConfig) (*openaiAzureProvider, error) {
	return nil, fmt.Errorf("openai_azure provider not yet implemented: see https://learn.microsoft.com/en-us/azure/ai-services/openai/reference")
}

func (p *openaiAzureProvider) Name() string { return "openai_azure" }

func (p *openaiAzureProvider) Chat(_ context.Context, _ []Message, _ []ToolDef) (*Response, error) {
	return nil, fmt.Errorf("openai_azure provider not yet implemented")
}

func (p *openaiAzureProvider) Stream(_ context.Context, _ []Message, _ []ToolDef) (<-chan StreamEvent, error) {
	return nil, fmt.Errorf("openai_azure provider not yet implemented")
}

func (p *openaiAzureProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "azure",
		DisplayName: "OpenAI (Azure OpenAI Service)",
		Description: "Access OpenAI models via Azure OpenAI Service using Azure API keys or Microsoft Entra ID tokens. Uses deployment-specific URLs.",
		DocsURL:     "https://learn.microsoft.com/en-us/azure/ai-services/openai/reference",
		ServerSafe:  true,
	}
}
