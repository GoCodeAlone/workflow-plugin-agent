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
	defaultOpenAIBaseURL   = "https://api.openai.com"
	defaultOpenAIModel     = "gpt-4o"
	defaultOpenAIMaxTokens = 4096
)

// OpenAIConfig holds configuration for the OpenAI provider.
type OpenAIConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// OpenAIProvider implements Provider using the OpenAI Chat Completions API.
type OpenAIProvider struct {
	config OpenAIConfig
}

// NewOpenAIProvider creates a new OpenAI provider with the given config.
func NewOpenAIProvider(cfg OpenAIConfig) *OpenAIProvider {
	if cfg.Model == "" {
		cfg.Model = defaultOpenAIModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultOpenAIBaseURL
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultOpenAIMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &OpenAIProvider{config: cfg}
}

func (p *OpenAIProvider) Name() string { return "openai" }

// openaiRequest is the request body for the Chat Completions API.
type openaiRequest struct {
	Model     string          `json:"model"`
	Messages  []openaiMessage `json:"messages"`
	Tools     []openaiTool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
}

type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiToolCallFunc `json:"function"`
}

type openaiToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type openaiTool struct {
	Type     string         `json:"type"`
	Function openaiToolFunc `json:"function"`
}

type openaiToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// openaiResponse is the response from the Chat Completions API.
type openaiResponse struct {
	ID      string         `json:"id"`
	Choices []openaiChoice `json:"choices"`
	Usage   openaiUsage    `json:"usage"`
	Error   *openaiError   `json:"error,omitempty"`
}

type openaiChoice struct {
	Message      openaiMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type openaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (p *OpenAIProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	reqBody := p.buildRequest(messages, tools, false)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai: API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp openaiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("openai: unmarshal response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("openai: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return p.parseResponse(&apiResp)
}

func (p *OpenAIProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	reqBody := p.buildRequest(messages, tools, true)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("openai: API error (status %d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 16)
	go p.readSSE(resp.Body, ch)
	return ch, nil
}

func (p *OpenAIProvider) buildRequest(messages []Message, tools []ToolDef, stream bool) *openaiRequest {
	req := &openaiRequest{
		Model:     p.config.Model,
		MaxTokens: p.config.MaxTokens,
		Stream:    stream,
	}

	// Convert messages — OpenAI includes system messages inline
	for _, msg := range messages {
		switch msg.Role {
		case RoleTool:
			req.Messages = append(req.Messages, openaiMessage{
				Role:       "tool",
				Content:    msg.Content,
				ToolCallID: msg.ToolCallID,
			})
		case RoleAssistant:
			om := openaiMessage{
				Role:    "assistant",
				Content: msg.Content,
			}
			for _, tc := range msg.ToolCalls {
				args := "{}"
				if tc.Arguments != nil {
					if b, err := json.Marshal(tc.Arguments); err == nil {
						args = string(b)
					}
				}
				om.ToolCalls = append(om.ToolCalls, openaiToolCall{
					ID:       tc.ID,
					Type:     "function",
					Function: openaiToolCallFunc{Name: tc.Name, Arguments: args},
				})
			}
			req.Messages = append(req.Messages, om)
		default:
			req.Messages = append(req.Messages, openaiMessage{
				Role:    string(msg.Role),
				Content: msg.Content,
			})
		}
	}

	// Convert tools
	for _, t := range tools {
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		req.Tools = append(req.Tools, openaiTool{
			Type: "function",
			Function: openaiToolFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			},
		})
	}

	return req
}

func (p *OpenAIProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
}

func (p *OpenAIProvider) parseResponse(apiResp *openaiResponse) (*Response, error) {
	resp := &Response{
		Usage: Usage{
			InputTokens:  apiResp.Usage.PromptTokens,
			OutputTokens: apiResp.Usage.CompletionTokens,
		},
	}

	if len(apiResp.Choices) == 0 {
		return resp, nil
	}

	msg := apiResp.Choices[0].Message
	resp.Content = msg.Content

	for _, tc := range msg.ToolCalls {
		var args map[string]any
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return nil, fmt.Errorf("openai: unmarshal tool call arguments for %q: %w", tc.Function.Name, err)
			}
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}

	return resp, nil
}

// openaiStreamDelta is the delta object in a streaming choice.
type openaiStreamDelta struct {
	Role      string                 `json:"role"`
	Content   *string                `json:"content"`
	ToolCalls []openaiStreamToolCall `json:"tool_calls"`
}

type openaiStreamToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function openaiStreamFunc `json:"function"`
}

type openaiStreamFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiStreamChoice struct {
	Index        int               `json:"index"`
	Delta        openaiStreamDelta `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

type openaiStreamChunk struct {
	ID      string               `json:"id"`
	Choices []openaiStreamChoice `json:"choices"`
	Usage   *openaiUsage         `json:"usage"`
}

// readSSE parses the SSE stream from the OpenAI API.
func (p *OpenAIProvider) readSSE(body io.ReadCloser, ch chan<- StreamEvent) {
	defer func() { _ = body.Close() }()
	defer close(ch)

	scanner := bufio.NewScanner(body)

	// Track tool calls being assembled by index
	type pendingToolCall struct {
		id      string
		name    string
		argsBuf strings.Builder
	}
	pending := make(map[int]*pendingToolCall)

	var usage *Usage

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			// Emit any accumulated tool calls; use range to handle sparse/non-sequential indices.
			for _, ptc := range pending {
				var args map[string]any
				if ptc.argsBuf.Len() > 0 {
					if err := json.Unmarshal([]byte(ptc.argsBuf.String()), &args); err != nil {
						ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("openai: unmarshal tool call arguments for %q: %v", ptc.name, err)}
						return
					}
				}
				ch <- StreamEvent{
					Type: "tool_call",
					Tool: &ToolCall{
						ID:        ptc.id,
						Name:      ptc.name,
						Arguments: args,
					},
				}
			}
			ch <- StreamEvent{Type: "done", Usage: usage}
			return
		}

		var chunk openaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Capture usage if present (some models send it in the final chunk)
		if chunk.Usage != nil {
			usage = &Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		// Text content
		if delta.Content != nil && *delta.Content != "" {
			ch <- StreamEvent{Type: "text", Text: *delta.Content}
		}

		// Tool call deltas
		for _, tc := range delta.ToolCalls {
			ptc, exists := pending[tc.Index]
			if !exists {
				ptc = &pendingToolCall{}
				pending[tc.Index] = ptc
			}
			if tc.ID != "" {
				ptc.id = tc.ID
			}
			if tc.Function.Name != "" {
				ptc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				ptc.argsBuf.WriteString(tc.Function.Arguments)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: err.Error()}
	}
}
