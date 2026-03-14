package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

const (
	defaultAnthropicBaseURL   = "https://api.anthropic.com"
	defaultAnthropicModel     = "claude-sonnet-4-20250514"
	defaultAnthropicMaxTokens = 4096
	anthropicAPIVersion       = "2023-06-01"
)

// AnthropicConfig holds configuration for the Anthropic provider.
type AnthropicConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// AnthropicProvider implements Provider using the Anthropic Messages API.
type AnthropicProvider struct {
	client anthropic.Client
	config AnthropicConfig
}

// NewAnthropicProvider creates a new Anthropic provider with the given config.
func NewAnthropicProvider(cfg AnthropicConfig) *AnthropicProvider {
	if cfg.Model == "" {
		cfg.Model = defaultAnthropicModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultAnthropicBaseURL
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultAnthropicMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	client := anthropic.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithHTTPClient(cfg.HTTPClient),
	)
	return &AnthropicProvider{client: client, config: cfg}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "direct",
		DisplayName: "Anthropic (Direct API)",
		Description: "Direct access to Anthropic's Claude models via API key.",
		DocsURL:     "https://platform.claude.com/docs/en/api/getting-started",
		ServerSafe:  true,
	}
}

func (p *AnthropicProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	params := toAnthropicParams(p.config.Model, p.config.MaxTokens, messages, tools)
	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return fromAnthropicMessage(msg), nil
}

func (p *AnthropicProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	params := toAnthropicParams(p.config.Model, p.config.MaxTokens, messages, tools)
	stream := p.client.Messages.NewStreaming(ctx, params)
	if stream.Err() != nil {
		return nil, fmt.Errorf("anthropic: %w", stream.Err())
	}
	ch := make(chan StreamEvent, 16)
	go streamAnthropicEvents(stream, ch)
	return ch, nil
}

// toAnthropicParams converts provider types to SDK MessageNewParams.
func toAnthropicParams(model string, maxTokens int, messages []Message, tools []ToolDef) anthropic.MessageNewParams {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(maxTokens),
	}
	for _, msg := range messages {
		if msg.Role == RoleSystem {
			params.System = append(params.System, anthropic.TextBlockParam{Text: msg.Content})
			continue
		}
		if msg.Role == RoleTool {
			params.Messages = append(params.Messages,
				anthropic.NewUserMessage(anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false)),
			)
			continue
		}
		if msg.Role == RoleAssistant {
			params.Messages = append(params.Messages,
				anthropic.NewAssistantMessage(anthropic.NewTextBlock(msg.Content)),
			)
		} else {
			params.Messages = append(params.Messages,
				anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)),
			)
		}
	}
	for _, t := range tools {
		extras := make(map[string]any)
		for k, v := range t.Parameters {
			if k != "type" {
				extras[k] = v
			}
		}
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: anthropic.ToolInputSchemaParam{ExtraFields: extras},
			},
		})
	}
	return params
}

// fromAnthropicMessage converts an SDK Message to a provider Response.
func fromAnthropicMessage(msg *anthropic.Message) *Response {
	resp := &Response{
		Usage: Usage{
			InputTokens:  int(msg.Usage.InputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
		},
	}
	var textParts []string
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			var args map[string]any
			if len(block.Input) > 0 {
				_ = json.Unmarshal(block.Input, &args)
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		}
	}
	resp.Content = strings.Join(textParts, "")
	return resp
}

// streamAnthropicEvents reads SDK stream events and sends them to ch.
func streamAnthropicEvents(stream *ssestream.Stream[anthropic.MessageStreamEventUnion], ch chan<- StreamEvent) {
	defer close(ch)
	defer stream.Close()

	var usage *Usage
	var currentToolID, currentToolName string
	var toolInputBuf bytes.Buffer

	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "message_start":
			e := event.AsMessageStart()
			usage = &Usage{
				InputTokens:  int(e.Message.Usage.InputTokens),
				OutputTokens: int(e.Message.Usage.OutputTokens),
			}
		case "content_block_start":
			e := event.AsContentBlockStart()
			if e.ContentBlock.Type == "tool_use" {
				currentToolID = e.ContentBlock.ID
				currentToolName = e.ContentBlock.Name
				toolInputBuf.Reset()
			}
		case "content_block_delta":
			e := event.AsContentBlockDelta()
			switch e.Delta.Type {
			case "text_delta":
				ch <- StreamEvent{Type: "text", Text: e.Delta.Text}
			case "input_json_delta":
				toolInputBuf.WriteString(e.Delta.PartialJSON)
			}
		case "content_block_stop":
			if currentToolID != "" {
				var args map[string]any
				if toolInputBuf.Len() > 0 {
					_ = json.Unmarshal(toolInputBuf.Bytes(), &args)
				}
				ch <- StreamEvent{
					Type: "tool_call",
					Tool: &ToolCall{
						ID:        currentToolID,
						Name:      currentToolName,
						Arguments: args,
					},
				}
				currentToolID = ""
				currentToolName = ""
				toolInputBuf.Reset()
			}
		case "message_delta":
			e := event.AsMessageDelta()
			if usage != nil {
				usage.OutputTokens = int(e.Usage.OutputTokens)
			}
		case "message_stop":
			ch <- StreamEvent{Type: "done", Usage: usage}
			return
		}
	}

	if err := stream.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: err.Error()}
	}
}

// The following types are kept for backward compatibility during the SDK migration.
// They are used by bedrock/vertex test helpers and will be removed in the shared-helpers refactor.

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicContent struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
	CacheCtrl *cacheControl  `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicResponse struct {
	ID      string              `json:"id"`
	Type    string              `json:"type"`
	Content []anthropicRespItem `json:"content"`
	Usage   anthropicUsage      `json:"usage"`
	Error   *anthropicError     `json:"error,omitempty"`
}

type anthropicRespItem struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
