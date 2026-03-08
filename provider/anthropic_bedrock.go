package provider

import (
	"context"
	"fmt"
)

// AnthropicBedrockConfig configures the Anthropic provider for Amazon Bedrock.
// Uses AWS IAM SigV4 authentication against the Bedrock Runtime API.
type AnthropicBedrockConfig struct {
	// Region is the AWS region (e.g. "us-east-1").
	Region string
	// Model is the Bedrock model ID (e.g. "anthropic.claude-sonnet-4-20250514-v1:0").
	Model string
	// MaxTokens limits the response length.
	MaxTokens int
	// AccessKeyID is the AWS access key (optional if using instance/role credentials).
	AccessKeyID string
	// SecretAccessKey is the AWS secret key (optional if using instance/role credentials).
	SecretAccessKey string
	// SessionToken is the AWS session token for temporary credentials (optional).
	SessionToken string
	// Profile is the AWS config profile name (optional, for shared credentials).
	Profile string
}

// anthropicBedrockProvider accesses Anthropic models via Amazon Bedrock.
type anthropicBedrockProvider struct {
	config AnthropicBedrockConfig
}

// NewAnthropicBedrockProvider creates a provider that accesses Claude via Amazon Bedrock.
//
// NOT YET IMPLEMENTED — scaffolded for future development.
//
// Docs: https://platform.claude.com/docs/en/build-with-claude/claude-on-amazon-bedrock
func NewAnthropicBedrockProvider(_ AnthropicBedrockConfig) (*anthropicBedrockProvider, error) {
	return nil, fmt.Errorf("anthropic_bedrock provider not yet implemented: see https://platform.claude.com/docs/en/build-with-claude/claude-on-amazon-bedrock")
}

func (p *anthropicBedrockProvider) Name() string { return "anthropic_bedrock" }

func (p *anthropicBedrockProvider) Chat(_ context.Context, _ []Message, _ []ToolDef) (*Response, error) {
	return nil, fmt.Errorf("anthropic_bedrock provider not yet implemented")
}

func (p *anthropicBedrockProvider) Stream(_ context.Context, _ []Message, _ []ToolDef) (<-chan StreamEvent, error) {
	return nil, fmt.Errorf("anthropic_bedrock provider not yet implemented")
}

func (p *anthropicBedrockProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "bedrock",
		DisplayName: "Anthropic (Amazon Bedrock)",
		Description: "Access Claude models via Amazon Bedrock using AWS IAM SigV4 authentication. Supports instance roles, access keys, and shared credential profiles.",
		DocsURL:     "https://platform.claude.com/docs/en/build-with-claude/claude-on-amazon-bedrock",
		ServerSafe:  true,
	}
}
