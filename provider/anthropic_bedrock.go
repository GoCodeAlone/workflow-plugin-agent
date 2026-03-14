package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const (
	defaultBedrockModel  = "anthropic.claude-sonnet-4-20250514-v1:0"
	defaultBedrockRegion = "us-east-1"
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
	// AccessKeyID is the AWS access key (required).
	AccessKeyID string
	// SecretAccessKey is the AWS secret key (required).
	SecretAccessKey string
	// SessionToken is the AWS session token for temporary credentials (optional).
	SessionToken string
	// Profile is the AWS config profile name (reserved for future use).
	Profile string
	// HTTPClient is the HTTP client to use (defaults to http.DefaultClient).
	HTTPClient *http.Client
	// BaseURL overrides the endpoint (for testing).
	BaseURL string
}

// anthropicBedrockProvider accesses Anthropic models via Amazon Bedrock.
type anthropicBedrockProvider struct {
	client anthropic.Client
	config AnthropicBedrockConfig
}

// NewAnthropicBedrockProvider creates a provider that accesses Claude via Amazon Bedrock.
//
// Docs: https://platform.claude.com/docs/en/build-with-claude/claude-on-amazon-bedrock
func NewAnthropicBedrockProvider(cfg AnthropicBedrockConfig) (*anthropicBedrockProvider, error) {
	if cfg.AccessKeyID == "" {
		return nil, fmt.Errorf("anthropic_bedrock: access key ID is required")
	}
	if cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("anthropic_bedrock: secret access key is required")
	}
	if cfg.Region == "" {
		cfg.Region = defaultBedrockRegion
	}
	if cfg.Model == "" {
		cfg.Model = defaultBedrockModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultAnthropicMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	awsCfg := aws.Config{
		Region:      cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
	}

	opts := []option.RequestOption{bedrock.WithConfig(awsCfg)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))

	client := anthropic.NewClient(opts...)
	return &anthropicBedrockProvider{client: client, config: cfg}, nil
}

func (p *anthropicBedrockProvider) Name() string { return "anthropic_bedrock" }

func (p *anthropicBedrockProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "bedrock",
		DisplayName: "Anthropic (Amazon Bedrock)",
		Description: "Access Claude models via Amazon Bedrock using AWS IAM SigV4 authentication. Supports instance roles, access keys, and shared credential profiles.",
		DocsURL:     "https://platform.claude.com/docs/en/build-with-claude/claude-on-amazon-bedrock",
		ServerSafe:  true,
	}
}

func (p *anthropicBedrockProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	params := toAnthropicParams(p.config.Model, p.config.MaxTokens, messages, tools)
	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic_bedrock: %w", err)
	}
	return fromAnthropicMessage(msg)
}

func (p *anthropicBedrockProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	params := toAnthropicParams(p.config.Model, p.config.MaxTokens, messages, tools)
	stream := p.client.Messages.NewStreaming(ctx, params)
	if stream.Err() != nil {
		return nil, fmt.Errorf("anthropic_bedrock: %w", stream.Err())
	}
	ch := make(chan StreamEvent, 16)
	go streamAnthropicEvents(stream, ch)
	return ch, nil
}
