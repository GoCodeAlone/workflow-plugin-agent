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

const defaultFoundryModel = "claude-sonnet-4-20250514"

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
	// HTTPClient is the HTTP client to use (defaults to http.DefaultClient).
	HTTPClient *http.Client
}

// anthropicFoundryProvider accesses Anthropic models via Azure AI Foundry.
type anthropicFoundryProvider struct {
	config AnthropicFoundryConfig
	url    string
}

// NewAnthropicFoundryProvider creates a provider that accesses Claude via Azure AI Foundry.
//
// Docs: https://platform.claude.com/docs/en/build-with-claude/claude-in-microsoft-foundry
func NewAnthropicFoundryProvider(cfg AnthropicFoundryConfig) (*anthropicFoundryProvider, error) {
	if cfg.Resource == "" {
		return nil, fmt.Errorf("anthropic_foundry: resource name is required")
	}
	if cfg.APIKey == "" && cfg.EntraToken == "" {
		return nil, fmt.Errorf("anthropic_foundry: either APIKey or EntraToken is required")
	}
	if cfg.Model == "" {
		cfg.Model = defaultFoundryModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultAnthropicMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &anthropicFoundryProvider{
		config: cfg,
		url:    fmt.Sprintf("https://%s.services.ai.azure.com/anthropic/v1/messages", cfg.Resource),
	}, nil
}

func (p *anthropicFoundryProvider) Name() string { return "anthropic_foundry" }

func (p *anthropicFoundryProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "foundry",
		DisplayName: "Anthropic (Azure AI Foundry)",
		Description: "Access Claude models via Microsoft Azure AI Foundry using Azure API keys or Microsoft Entra ID tokens.",
		DocsURL:     "https://platform.claude.com/docs/en/build-with-claude/claude-in-microsoft-foundry",
		ServerSafe:  true,
	}
}

func (p *anthropicFoundryProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	reqBody := p.buildRequest(messages, tools, false)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic_foundry: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic_foundry: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic_foundry: send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic_foundry: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic_foundry: API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("anthropic_foundry: unmarshal response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("anthropic_foundry: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return foundryParseResponse(&apiResp), nil
}

func (p *anthropicFoundryProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	reqBody := p.buildRequest(messages, tools, true)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic_foundry: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic_foundry: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic_foundry: send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("anthropic_foundry: API error (status %d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 16)
	go foundryReadSSE(resp.Body, ch)
	return ch, nil
}

func (p *anthropicFoundryProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	if p.config.EntraToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.EntraToken)
	} else {
		req.Header.Set("api-key", p.config.APIKey)
	}
}

func (p *anthropicFoundryProvider) buildRequest(messages []Message, tools []ToolDef, stream bool) *anthropicRequest {
	req := &anthropicRequest{
		Model:     p.config.Model,
		MaxTokens: p.config.MaxTokens,
		Stream:    stream,
	}

	var apiMessages []anthropicMessage
	for _, msg := range messages {
		if msg.Role == RoleSystem {
			req.System = msg.Content
			continue
		}
		if msg.Role == RoleTool {
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

	return req
}

func foundryParseResponse(apiResp *anthropicResponse) *Response {
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

func foundryReadSSE(body io.ReadCloser, ch chan<- StreamEvent) {
	defer func() { _ = body.Close() }()
	defer close(ch)

	scanner := bufio.NewScanner(body)

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
					if err := json.Unmarshal(toolInputBuf.Bytes(), &args); err != nil {
						ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("tool %s: malformed arguments: %v", currentToolName, err)}
					}
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

	if err := scanner.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("foundry stream read: %v", err)}
	}
}
