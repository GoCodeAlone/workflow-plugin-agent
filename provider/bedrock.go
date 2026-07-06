package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

type bedrockConverseAPI interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// BedrockConfig configures a provider that uses Amazon Bedrock Runtime Converse.
type BedrockConfig struct {
	Name            string
	Region          string
	Model           string
	MaxTokens       int
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	HTTPClient      *http.Client
	BaseURL         string
	RuntimeAPI      bedrockConverseAPI
}

type bedrockProvider struct {
	name      string
	model     string
	maxTokens int
	api       bedrockConverseAPI
}

func NewBedrockProvider(cfg BedrockConfig) (*bedrockProvider, error) {
	if cfg.AccessKeyID == "" {
		return nil, fmt.Errorf("bedrock: access key ID is required")
	}
	if cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("bedrock: secret access key is required")
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
	if cfg.Name == "" {
		cfg.Name = "bedrock"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	api := cfg.RuntimeAPI
	if api == nil {
		awsCfg := aws.Config{
			Region:      cfg.Region,
			Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
			HTTPClient:  cfg.HTTPClient,
		}
		opts := []func(*bedrockruntime.Options){}
		if cfg.BaseURL != "" {
			opts = append(opts, func(o *bedrockruntime.Options) {
				o.BaseEndpoint = aws.String(strings.TrimRight(cfg.BaseURL, "/"))
			})
		}
		api = bedrockruntime.NewFromConfig(awsCfg, opts...)
	}

	return &bedrockProvider{
		name:      cfg.Name,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		api:       api,
	}, nil
}

func (p *bedrockProvider) Name() string { return p.name }

func (p *bedrockProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "bedrock",
		DisplayName: "Amazon Bedrock",
		Description: "Access Bedrock Converse-compatible models using AWS IAM SigV4 authentication.",
		DocsURL:     "https://docs.aws.amazon.com/bedrock/latest/userguide/conversation-inference.html",
		ServerSafe:  true,
	}
}

func (p *bedrockProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(p.model),
		Messages: bedrockMessages(messages),
		System:   bedrockSystem(messages),
	}
	if p.maxTokens > 0 {
		input.InferenceConfig = &brtypes.InferenceConfiguration{MaxTokens: aws.Int32(int32(p.maxTokens))}
	}
	if len(tools) > 0 {
		input.ToolConfig = bedrockToolConfig(tools)
	}

	out, err := p.api.Converse(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("bedrock: converse: %w", err)
	}
	return responseFromBedrock(out)
}

func (p *bedrockProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 8)
	go func() {
		defer close(ch)
		resp, err := p.Chat(ctx, messages, tools)
		if err != nil {
			ch <- StreamEvent{Type: "error", Error: err.Error()}
			return
		}
		if resp.Content != "" {
			ch <- StreamEvent{Type: "text", Text: resp.Content}
		}
		for i := range resp.ToolCalls {
			tc := resp.ToolCalls[i]
			ch <- StreamEvent{Type: "tool_call", Tool: &tc}
		}
		ch <- StreamEvent{Type: "done", Usage: &resp.Usage}
	}()
	return ch, nil
}

func bedrockSystem(messages []Message) []brtypes.SystemContentBlock {
	var out []brtypes.SystemContentBlock
	for _, msg := range messages {
		if msg.Role == RoleSystem && msg.Content != "" {
			out = append(out, &brtypes.SystemContentBlockMemberText{Value: msg.Content})
		}
	}
	return out
}

func bedrockMessages(messages []Message) []brtypes.Message {
	var out []brtypes.Message
	for _, msg := range messages {
		if msg.Role == RoleSystem {
			continue
		}
		role := brtypes.ConversationRoleUser
		if msg.Role == RoleAssistant {
			role = brtypes.ConversationRoleAssistant
		}
		blocks := bedrockContentBlocks(msg)
		if len(blocks) == 0 {
			blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: msg.Content})
		}
		out = append(out, brtypes.Message{Role: role, Content: blocks})
	}
	return out
}

func bedrockContentBlocks(msg Message) []brtypes.ContentBlock {
	if msg.ToolCallID != "" {
		return []brtypes.ContentBlock{&brtypes.ContentBlockMemberToolResult{Value: brtypes.ToolResultBlock{
			ToolUseId: aws.String(msg.ToolCallID),
			Content: []brtypes.ToolResultContentBlock{
				&brtypes.ToolResultContentBlockMemberText{Value: msg.Content},
			},
		}}}
	}

	var blocks []brtypes.ContentBlock
	if msg.Content != "" {
		blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		id := tc.ID
		if id == "" {
			id = tc.Name
		}
		blocks = append(blocks, &brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
			ToolUseId: aws.String(id),
			Name:      aws.String(tc.Name),
			Input:     brdoc.NewLazyDocument(tc.Arguments),
		}})
	}
	return blocks
}

func bedrockToolConfig(tools []ToolDef) *brtypes.ToolConfiguration {
	cfg := &brtypes.ToolConfiguration{Tools: make([]brtypes.Tool, 0, len(tools))}
	for _, t := range tools {
		cfg.Tools = append(cfg.Tools, &brtypes.ToolMemberToolSpec{Value: brtypes.ToolSpecification{
			Name:        aws.String(t.Name),
			Description: aws.String(t.Description),
			InputSchema: &brtypes.ToolInputSchemaMemberJson{Value: brdoc.NewLazyDocument(t.Parameters)},
		}})
	}
	return cfg
}

func responseFromBedrock(out *bedrockruntime.ConverseOutput) (*Response, error) {
	resp := &Response{}
	if out == nil {
		return resp, nil
	}
	if out.Usage != nil {
		resp.Usage = Usage{
			InputTokens:  int(aws.ToInt32(out.Usage.InputTokens)),
			OutputTokens: int(aws.ToInt32(out.Usage.OutputTokens)),
		}
	}
	msgOut, ok := out.Output.(*brtypes.ConverseOutputMemberMessage)
	if !ok || msgOut == nil {
		return resp, nil
	}
	for _, block := range msgOut.Value.Content {
		switch b := block.(type) {
		case *brtypes.ContentBlockMemberText:
			resp.Content += b.Value
		case *brtypes.ContentBlockMemberToolUse:
			args := map[string]any{}
			if b.Value.Input != nil {
				if err := b.Value.Input.UnmarshalSmithyDocument(&args); err != nil {
					return nil, fmt.Errorf("bedrock: unmarshal tool call arguments for %q: %w", aws.ToString(b.Value.Name), err)
				}
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:        aws.ToString(b.Value.ToolUseId),
				Name:      aws.ToString(b.Value.Name),
				Arguments: args,
			})
		}
	}
	return resp, nil
}
