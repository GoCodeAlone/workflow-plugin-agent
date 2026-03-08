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

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	defaultVertexModel  = "claude-sonnet-4@20250514"
	defaultVertexRegion = "us-east5"
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
	// TokenSource provides OAuth2 tokens (optional, for testing or custom auth).
	// If set, CredentialsJSON and ADC are ignored.
	TokenSource oauth2.TokenSource
	// HTTPClient is the HTTP client to use (defaults to http.DefaultClient).
	HTTPClient *http.Client
}

// anthropicVertexProvider accesses Anthropic models via Google Vertex AI.
type anthropicVertexProvider struct {
	config      AnthropicVertexConfig
	tokenSource oauth2.TokenSource
}

// NewAnthropicVertexProvider creates a provider that accesses Claude via Google Vertex AI.
//
// Docs: https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai
func NewAnthropicVertexProvider(cfg AnthropicVertexConfig) (*anthropicVertexProvider, error) {
	if cfg.ProjectID == "" {
		return nil, fmt.Errorf("anthropic_vertex: project ID is required")
	}
	if cfg.Region == "" {
		cfg.Region = defaultVertexRegion
	}
	if cfg.Model == "" {
		cfg.Model = defaultVertexModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultAnthropicMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	var ts oauth2.TokenSource
	if cfg.TokenSource != nil {
		ts = cfg.TokenSource
	} else if cfg.CredentialsJSON != "" {
		creds, err := google.CredentialsFromJSON(context.Background(), []byte(cfg.CredentialsJSON), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("anthropic_vertex: parse credentials JSON: %w", err)
		}
		ts = creds.TokenSource
	} else {
		creds, err := google.FindDefaultCredentials(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("anthropic_vertex: find default credentials: %w", err)
		}
		ts = creds.TokenSource
	}

	return &anthropicVertexProvider{
		config:      cfg,
		tokenSource: ts,
	}, nil
}

func (p *anthropicVertexProvider) Name() string { return "anthropic_vertex" }

func (p *anthropicVertexProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "vertex",
		DisplayName: "Anthropic (Google Vertex AI)",
		Description: "Access Claude models via Google Cloud Vertex AI using Application Default Credentials (ADC) or service account JSON.",
		DocsURL:     "https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai",
		ServerSafe:  true,
	}
}

func (p *anthropicVertexProvider) vertexURL(stream bool) string {
	suffix := ":rawPredict"
	if stream {
		suffix = ":streamRawPredict"
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s%s",
		p.config.Region, p.config.ProjectID, p.config.Region, p.config.Model, suffix)
}

func (p *anthropicVertexProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	reqBody := vertexBuildRequest(p.config.Model, p.config.MaxTokens, messages, tools, false)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic_vertex: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.vertexURL(false), bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic_vertex: create request: %w", err)
	}
	if err := p.setHeaders(req); err != nil {
		return nil, err
	}

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic_vertex: send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic_vertex: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic_vertex: API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("anthropic_vertex: unmarshal response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("anthropic_vertex: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return vertexParseResponse(&apiResp), nil
}

func (p *anthropicVertexProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	reqBody := vertexBuildRequest(p.config.Model, p.config.MaxTokens, messages, tools, true)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic_vertex: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.vertexURL(true), bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic_vertex: create request: %w", err)
	}
	if err := p.setHeaders(req); err != nil {
		return nil, err
	}

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic_vertex: send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("anthropic_vertex: API error (status %d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 16)
	go vertexReadSSE(resp.Body, ch)
	return ch, nil
}

func (p *anthropicVertexProvider) setHeaders(req *http.Request) error {
	token, err := p.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("anthropic_vertex: get token: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	return nil
}

func vertexBuildRequest(model string, maxTokens int, messages []Message, tools []ToolDef, stream bool) *anthropicRequest {
	req := &anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
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

func vertexParseResponse(apiResp *anthropicResponse) *Response {
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

func vertexReadSSE(body io.ReadCloser, ch chan<- StreamEvent) {
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
