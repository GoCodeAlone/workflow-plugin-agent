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
	defaultCopilotBaseURL   = "https://api.githubcopilot.com"
	defaultCopilotModel     = "gpt-4o"
	defaultCopilotMaxTokens = 4096
	copilotIntegrationID    = "ratchet"
)

// CopilotConfig holds configuration for the GitHub Copilot provider.
type CopilotConfig struct {
	Token      string
	Model      string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// CopilotProvider implements Provider using the GitHub Copilot Chat API.
// The API follows the OpenAI Chat Completions format.
type CopilotProvider struct {
	config CopilotConfig
}

// NewCopilotProvider creates a new Copilot provider with the given config.
func NewCopilotProvider(cfg CopilotConfig) *CopilotProvider {
	if cfg.Model == "" {
		cfg.Model = defaultCopilotModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultCopilotBaseURL
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultCopilotMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &CopilotProvider{config: cfg}
}

func (p *CopilotProvider) Name() string { return "copilot" }

// copilotRequest is the request body for the Copilot Chat Completions API.
type copilotRequest struct {
	Model     string           `json:"model"`
	Messages  []copilotMessage `json:"messages"`
	Tools     []copilotTool    `json:"tools,omitempty"`
	MaxTokens int              `json:"max_tokens,omitempty"`
	Stream    bool             `json:"stream,omitempty"`
}

type copilotMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type copilotTool struct {
	Type     string              `json:"type"`
	Function copilotToolFunction `json:"function"`
}

type copilotToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// copilotResponse is the response from the Copilot Chat Completions API.
type copilotResponse struct {
	ID      string           `json:"id"`
	Choices []copilotChoice  `json:"choices"`
	Usage   copilotUsage     `json:"usage"`
	Error   *copilotAPIError `json:"error,omitempty"`
}

type copilotChoice struct {
	Index        int            `json:"index"`
	Message      copilotResMsg  `json:"message"`
	Delta        *copilotResMsg `json:"delta,omitempty"`
	FinishReason *string        `json:"finish_reason,omitempty"`
}

type copilotResMsg struct {
	Role      string               `json:"role"`
	Content   *string              `json:"content"`
	ToolCalls []copilotResToolCall `json:"tool_calls,omitempty"`
}

// copilotFunctionCall is the function call structure used in both request and response tool calls.
type copilotFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type copilotResToolCall struct {
	Index    int                 `json:"index"`
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function copilotFunctionCall `json:"function"`
}

// copilotReqToolCall is the serialized form of a tool call in a request message.
type copilotReqToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function copilotFunctionCall `json:"function"`
}

type copilotUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type copilotAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (p *CopilotProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	reqBody := p.buildRequest(messages, tools, false)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("copilot: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("copilot: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("copilot: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot: API error (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}

	var apiResp copilotResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("copilot: unmarshal response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("copilot: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return p.parseResponse(&apiResp)
}

func (p *CopilotProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	reqBody := p.buildRequest(messages, tools, true)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("copilot: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("copilot: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("copilot: API error (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}

	ch := make(chan StreamEvent, 16)
	go p.readSSE(resp.Body, ch)
	return ch, nil
}

func (p *CopilotProvider) buildRequest(messages []Message, tools []ToolDef, stream bool) *copilotRequest {
	req := &copilotRequest{
		Model:     p.config.Model,
		MaxTokens: p.config.MaxTokens,
		Stream:    stream,
	}

	for _, msg := range messages {
		cm := copilotMessage{
			Role:    string(msg.Role),
			Content: msg.Content,
		}
		if msg.Role == RoleTool {
			cm.ToolCallID = msg.ToolCallID
		}
		if msg.Role == RoleAssistant && len(msg.ToolCalls) > 0 {
			var tcs []copilotReqToolCall
			for _, tc := range msg.ToolCalls {
				args := "{}"
				if tc.Arguments != nil {
					if b, err := json.Marshal(tc.Arguments); err == nil {
						args = string(b)
					}
				}
				tcs = append(tcs, copilotReqToolCall{
					ID:       tc.ID,
					Type:     "function",
					Function: copilotFunctionCall{Name: tc.Name, Arguments: args},
				})
			}
			if raw, err := json.Marshal(tcs); err == nil {
				cm.ToolCalls = json.RawMessage(raw)
			}
		}
		req.Messages = append(req.Messages, cm)
	}

	for _, t := range tools {
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		req.Tools = append(req.Tools, copilotTool{
			Type: "function",
			Function: copilotToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			},
		})
	}

	return req
}

func (p *CopilotProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.config.Token)
	req.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
}

func (p *CopilotProvider) parseResponse(apiResp *copilotResponse) (*Response, error) {
	resp := &Response{
		Usage: Usage{
			InputTokens:  apiResp.Usage.PromptTokens,
			OutputTokens: apiResp.Usage.CompletionTokens,
		},
	}

	if len(apiResp.Choices) > 0 {
		choice := apiResp.Choices[0]
		if choice.Message.Content != nil {
			resp.Content = *choice.Message.Content
		}
		for _, tc := range choice.Message.ToolCalls {
			var args map[string]any
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					return nil, fmt.Errorf("copilot: unmarshal tool call arguments for %q: %w", tc.Function.Name, err)
				}
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: args,
			})
		}
	}

	return resp, nil
}

// readSSE parses the SSE stream from the Copilot Chat Completions API.
func (p *CopilotProvider) readSSE(body io.ReadCloser, ch chan<- StreamEvent) {
	defer func() { _ = body.Close() }()
	defer close(ch)

	scanner := bufio.NewScanner(body)
	// Increase buffer size to handle large tool call argument payloads.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	// Track tool calls being assembled across deltas
	type toolCallState struct {
		id   string
		name string
		args strings.Builder
	}
	toolCalls := make(map[int]*toolCallState)
	var usage *Usage

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			ch <- StreamEvent{Type: "done", Usage: usage}
			return
		}

		var chunk copilotResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Track usage
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			usage = &Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta
		if delta == nil {
			continue
		}

		// Text content
		if delta.Content != nil && *delta.Content != "" {
			ch <- StreamEvent{Type: "text", Text: *delta.Content}
		}

		// Tool call deltas
		for _, tc := range delta.ToolCalls {
			idx := tc.Index
			state, ok := toolCalls[idx]
			if !ok {
				state = &toolCallState{}
				toolCalls[idx] = state
			}
			if tc.ID != "" {
				// New tool call starting — flush any previous one
				if state.id != "" {
					var args map[string]any
					if state.args.Len() > 0 {
						if err := json.Unmarshal([]byte(state.args.String()), &args); err != nil {
							ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("copilot: invalid tool call arguments JSON for tool %q (id %q): %v", state.name, state.id, err)}
							return
						}
					}
					ch <- StreamEvent{
						Type: "tool_call",
						Tool: &ToolCall{
							ID:        state.id,
							Name:      state.name,
							Arguments: args,
						},
					}
				}
				state.id = tc.ID
				state.name = tc.Function.Name
				state.args.Reset()
			}
			if tc.Function.Arguments != "" {
				state.args.WriteString(tc.Function.Arguments)
			}
		}

		// Check for finish
		if choice.FinishReason != nil {
			// Flush any pending tool calls
			for _, state := range toolCalls {
				if state.id != "" {
					var args map[string]any
					if state.args.Len() > 0 {
						if err := json.Unmarshal([]byte(state.args.String()), &args); err != nil {
							ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("copilot: invalid tool call arguments JSON for tool %q (id %q): %v", state.name, state.id, err)}
							return
						}
					}
					ch <- StreamEvent{
						Type: "tool_call",
						Tool: &ToolCall{
							ID:        state.id,
							Name:      state.name,
							Arguments: args,
						},
					}
					state.id = ""
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("stream read error: %s", err.Error())}
	}

	// If we exit the loop without [DONE], send a done event
	ch <- StreamEvent{Type: "done", Usage: usage}
}
