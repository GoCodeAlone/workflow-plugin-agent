package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	return &AnthropicProvider{config: cfg}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// anthropicRequest is the request body for the Messages API.
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
	Content any    `json:"content"` // string or []anthropicContent
}

type anthropicContent struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`  // for tool_result
	IsError   bool           `json:"is_error,omitempty"` // for tool_result
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

// anthropicResponse is the response from the Messages API.
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

func (p *AnthropicProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	reqBody, err := p.buildRequest(messages, tools, false)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic: API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("anthropic: unmarshal response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("anthropic: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return p.parseResponse(&apiResp), nil
}

func (p *AnthropicProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	reqBody, err := p.buildRequest(messages, tools, true)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("anthropic: API error (status %d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 16)
	go p.readSSE(resp.Body, ch)
	return ch, nil
}

func (p *AnthropicProvider) buildRequest(messages []Message, tools []ToolDef, stream bool) (*anthropicRequest, error) {
	req := &anthropicRequest{
		Model:     p.config.Model,
		MaxTokens: p.config.MaxTokens,
		Stream:    stream,
	}

	// Extract system message
	var apiMessages []anthropicMessage
	for _, msg := range messages {
		if msg.Role == RoleSystem {
			req.System = msg.Content
			continue
		}
		if msg.Role == RoleTool {
			// Tool results need structured content
			apiMessages = append(apiMessages, anthropicMessage{
				Role: "user",
				Content: []anthropicContent{
					{
						Type:      "tool_result",
						ToolUseID: msg.ToolCallID,
						Content:   msg.Content,
					},
				},
			})
			continue
		}
		apiMessages = append(apiMessages, anthropicMessage{
			Role:    string(msg.Role),
			Content: msg.Content,
		})
	}
	req.Messages = apiMessages

	// Convert tools
	for _, t := range tools {
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		req.Tools = append(req.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}

	return req, nil
}

func (p *AnthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.config.APIKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
}

func (p *AnthropicProvider) parseResponse(apiResp *anthropicResponse) *Response {
	resp := &Response{
		Usage: Usage{
			InputTokens:  apiResp.Usage.InputTokens,
			OutputTokens: apiResp.Usage.OutputTokens,
		},
	}

	var textParts []string
	for _, item := range apiResp.Content {
		switch item.Type {
		case "text":
			textParts = append(textParts, item.Text)
		case "tool_use":
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:        item.ID,
				Name:      item.Name,
				Arguments: item.Input,
			})
		}
	}
	resp.Content = strings.Join(textParts, "")

	return resp
}

// readSSE parses the SSE stream from the Anthropic API.
func (p *AnthropicProvider) readSSE(body io.ReadCloser, ch chan<- StreamEvent) {
	defer func() { _ = body.Close() }()
	defer close(ch)

	scanner := bufio.NewScanner(body)

	// Track current tool call being built
	var currentToolID, currentToolName string
	var toolInputBuf bytes.Buffer
	var usage *Usage

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
				Text string `json:"text"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Message *struct {
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			Usage *anthropicUsage `json:"usage"`
		}

		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				usage = &Usage{
					InputTokens:  event.Message.Usage.InputTokens,
					OutputTokens: event.Message.Usage.OutputTokens,
				}
			}

		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				currentToolID = event.ContentBlock.ID
				currentToolName = event.ContentBlock.Name
				toolInputBuf.Reset()
			}

		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				ch <- StreamEvent{Type: "text", Text: event.Delta.Text}
			case "input_json_delta":
				toolInputBuf.WriteString(event.Delta.PartialJSON)
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
			if event.Usage != nil && usage != nil {
				usage.OutputTokens = event.Usage.OutputTokens
			}

		case "message_stop":
			ch <- StreamEvent{Type: "done", Usage: usage}
			return

		case "error":
			ch <- StreamEvent{Type: "error", Error: data}
			return
		}
	}
}
