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
	defaultCohereBaseURL   = "https://api.cohere.com"
	defaultCohereModel     = "command-r-plus"
	defaultCohereMaxTokens = 4096
)

// CohereConfig holds configuration for the Cohere provider.
type CohereConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// CohereProvider implements Provider using the Cohere Chat API v2.
type CohereProvider struct {
	config CohereConfig
}

// NewCohereProvider creates a new Cohere provider with the given config.
func NewCohereProvider(cfg CohereConfig) *CohereProvider {
	if cfg.Model == "" {
		cfg.Model = defaultCohereModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultCohereBaseURL
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultCohereMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &CohereProvider{config: cfg}
}

func (p *CohereProvider) Name() string { return "cohere" }

func (p *CohereProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "direct",
		DisplayName: "Cohere (Direct API)",
		Description: "Direct access to Cohere's Command models via API key.",
		DocsURL:     "https://docs.cohere.com/reference/chat",
		ServerSafe:  true,
	}
}

// Cohere Chat API v2 request types

type cohereRequest struct {
	Model     string          `json:"model"`
	Messages  []cohereMessage `json:"messages"`
	Tools     []cohereTool    `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
}

type cohereMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []cohereToolCall `json:"tool_calls,omitempty"`
}

type cohereToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function cohereToolFunc `json:"function"`
}

type cohereToolFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type cohereTool struct {
	Type     string        `json:"type"`
	Function cohereToolDef `json:"function"`
}

type cohereToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Cohere Chat API v2 response types

type cohereResponse struct {
	ID           string        `json:"id"`
	Message      cohereRespMsg `json:"message"`
	FinishReason string        `json:"finish_reason"`
	Usage        cohereUsage   `json:"usage"`
}

type cohereRespMsg struct {
	Role      string           `json:"role"`
	Content   []cohereContent  `json:"content"`
	ToolCalls []cohereToolCall `json:"tool_calls,omitempty"`
}

type cohereContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cohereUsage struct {
	BilledUnits cohereBilledUnits `json:"billed_units"`
	Tokens      cohereTokens      `json:"tokens"`
}

type cohereBilledUnits struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type cohereTokens struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (p *CohereProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	reqBody := p.buildRequest(messages, tools, false)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("cohere: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/v2/chat", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cohere: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cohere: send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cohere: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cohere: API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp cohereResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("cohere: unmarshal response: %w", err)
	}

	return p.parseResponse(&apiResp), nil
}

func (p *CohereProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	reqBody := p.buildRequest(messages, tools, true)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("cohere: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/v2/chat", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cohere: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cohere: send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("cohere: API error (status %d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 16)
	go p.readSSE(resp.Body, ch)
	return ch, nil
}

func (p *CohereProvider) buildRequest(messages []Message, tools []ToolDef, stream bool) *cohereRequest {
	req := &cohereRequest{
		Model:     p.config.Model,
		MaxTokens: p.config.MaxTokens,
		Stream:    stream,
	}

	// Cohere v2 uses the same role names as OpenAI: system, user, assistant, tool
	for _, msg := range messages {
		switch msg.Role {
		case RoleTool:
			req.Messages = append(req.Messages, cohereMessage{
				Role:       "tool",
				Content:    msg.Content,
				ToolCallID: msg.ToolCallID,
			})
		case RoleAssistant:
			cm := cohereMessage{
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
				cm.ToolCalls = append(cm.ToolCalls, cohereToolCall{
					ID:       tc.ID,
					Type:     "function",
					Function: cohereToolFunc{Name: tc.Name, Arguments: args},
				})
			}
			req.Messages = append(req.Messages, cm)
		default:
			req.Messages = append(req.Messages, cohereMessage{
				Role:    string(msg.Role),
				Content: msg.Content,
			})
		}
	}

	// Convert tools to Cohere format (same as OpenAI)
	for _, t := range tools {
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		req.Tools = append(req.Tools, cohereTool{
			Type: "function",
			Function: cohereToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			},
		})
	}

	return req
}

func (p *CohereProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
}

func (p *CohereProvider) parseResponse(apiResp *cohereResponse) *Response {
	resp := &Response{
		Usage: Usage{
			InputTokens:  apiResp.Usage.Tokens.InputTokens,
			OutputTokens: apiResp.Usage.Tokens.OutputTokens,
		},
	}

	// Extract text content
	var textParts []string
	for _, c := range apiResp.Message.Content {
		if c.Type == "text" {
			textParts = append(textParts, c.Text)
		}
	}
	resp.Content = strings.Join(textParts, "")

	// Extract tool calls
	for _, tc := range apiResp.Message.ToolCalls {
		var args map[string]any
		if tc.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}

	return resp
}

// Cohere v2 streaming types

type cohereStreamEvent struct {
	Type     string             `json:"type"`
	Index    int                `json:"index"`
	Delta    *cohereStreamDelta `json:"delta,omitempty"`
	Response *cohereResponse    `json:"response,omitempty"`
}

type cohereStreamDelta struct {
	Message *cohereStreamDeltaMsg `json:"message,omitempty"`
}

type cohereStreamDeltaMsg struct {
	Content   *cohereStreamContent   `json:"content,omitempty"`
	ToolCalls *cohereStreamToolDelta `json:"tool_calls,omitempty"`
}

type cohereStreamContent struct {
	Text string `json:"text"`
}

type cohereStreamToolDelta struct {
	Function *cohereStreamFuncDelta `json:"function,omitempty"`
}

type cohereStreamFuncDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// readSSE parses the SSE stream from the Cohere Chat API v2.
func (p *CohereProvider) readSSE(body io.ReadCloser, ch chan<- StreamEvent) {
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
			break
		}

		var event cohereStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "content-delta":
			if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.Content != nil {
				ch <- StreamEvent{Type: "text", Text: event.Delta.Message.Content.Text}
			}

		case "tool-call-start":
			ptc := &pendingToolCall{}
			if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.ToolCalls != nil {
				if event.Delta.Message.ToolCalls.Function != nil {
					ptc.name = event.Delta.Message.ToolCalls.Function.Name
				}
			}
			pending[event.Index] = ptc

		case "tool-call-delta":
			ptc, exists := pending[event.Index]
			if !exists {
				ptc = &pendingToolCall{}
				pending[event.Index] = ptc
			}
			if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.ToolCalls != nil {
				if event.Delta.Message.ToolCalls.Function != nil {
					ptc.argsBuf.WriteString(event.Delta.Message.ToolCalls.Function.Arguments)
				}
			}

		case "tool-call-end":
			ptc, exists := pending[event.Index]
			if !exists {
				continue
			}
			var args map[string]any
			if ptc.argsBuf.Len() > 0 {
				_ = json.Unmarshal([]byte(ptc.argsBuf.String()), &args)
			}
			ch <- StreamEvent{
				Type: "tool_call",
				Tool: &ToolCall{
					ID:        ptc.id,
					Name:      ptc.name,
					Arguments: args,
				},
			}
			delete(pending, event.Index)

		case "message-end":
			if event.Response != nil {
				usage = &Usage{
					InputTokens:  event.Response.Usage.Tokens.InputTokens,
					OutputTokens: event.Response.Usage.Tokens.OutputTokens,
				}
			}
			ch <- StreamEvent{Type: "done", Usage: usage}
			return
		}
	}

	// Flush any pending tool calls
	for _, ptc := range pending {
		var args map[string]any
		if ptc.argsBuf.Len() > 0 {
			_ = json.Unmarshal([]byte(ptc.argsBuf.String()), &args)
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

	if err := scanner.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: err.Error()}
	}
}
