package provider

import (
	"context"
	"fmt"
)

// AnthropicVertexConfig configures the Anthropic provider for Google Vertex AI.
// Uses GCP Application Default Credentials (ADC) or explicit OAuth2 tokens.
type AnthropicVertexConfig struct {
	// ProjectID is the GCP project ID.
	ProjectID string
	// Region is the GCP region (e.g. "us-east5", "europe-west1").
	Region string
	// Model is the Vertex model ID (e.g. "claude-sonnet-4@20250514").
	Model string
	// MaxTokens limits the response length.
	MaxTokens int
	// CredentialsJSON is the GCP service account JSON (optional if using ADC).
	CredentialsJSON string
}

// anthropicVertexProvider accesses Anthropic models via Google Vertex AI.
type anthropicVertexProvider struct {
	config AnthropicVertexConfig
}

// NewAnthropicVertexProvider creates a provider that accesses Claude via Google Vertex AI.
//
// NOT YET IMPLEMENTED — scaffolded for future development.
//
// Docs: https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai
func NewAnthropicVertexProvider(_ AnthropicVertexConfig) (*anthropicVertexProvider, error) {
	return nil, fmt.Errorf("anthropic_vertex provider not yet implemented: see https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai")
}

func (p *anthropicVertexProvider) Name() string { return "anthropic_vertex" }

func (p *anthropicVertexProvider) Chat(_ context.Context, _ []Message, _ []ToolDef) (*Response, error) {
	return nil, fmt.Errorf("anthropic_vertex provider not yet implemented")
}

func (p *anthropicVertexProvider) Stream(_ context.Context, _ []Message, _ []ToolDef) (<-chan StreamEvent, error) {
	return nil, fmt.Errorf("anthropic_vertex provider not yet implemented")
}

func (p *anthropicVertexProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "vertex",
		DisplayName: "Anthropic (Google Vertex AI)",
		Description: "Access Claude models via Google Cloud Vertex AI using Application Default Credentials (ADC) or service account JSON.",
		DocsURL:     "https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai",
		ServerSafe:  true,
	}
}
