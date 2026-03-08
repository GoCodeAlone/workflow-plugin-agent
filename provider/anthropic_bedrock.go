package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
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
	if cfg.BaseURL == "" {
		cfg.BaseURL = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", cfg.Region)
	}
	return &anthropicBedrockProvider{config: cfg}, nil
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

func (p *anthropicBedrockProvider) invokeURL() string {
	return fmt.Sprintf("%s/model/%s/invoke", p.config.BaseURL, p.config.Model)
}

func (p *anthropicBedrockProvider) streamURL() string {
	return fmt.Sprintf("%s/model/%s/invoke-with-response-stream", p.config.BaseURL, p.config.Model)
}

func (p *anthropicBedrockProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	reqBody := bedrockBuildRequest(p.config.Model, p.config.MaxTokens, messages, tools, false)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic_bedrock: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.invokeURL(), bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic_bedrock: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	sigv4Sign(req, data, p.config.AccessKeyID, p.config.SecretAccessKey, p.config.SessionToken, p.config.Region, "bedrock", time.Now().UTC())

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic_bedrock: send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic_bedrock: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic_bedrock: API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("anthropic_bedrock: unmarshal response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("anthropic_bedrock: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return bedrockParseResponse(&apiResp), nil
}

func (p *anthropicBedrockProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	reqBody := bedrockBuildRequest(p.config.Model, p.config.MaxTokens, messages, tools, true)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic_bedrock: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.streamURL(), bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic_bedrock: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	sigv4Sign(req, data, p.config.AccessKeyID, p.config.SecretAccessKey, p.config.SessionToken, p.config.Region, "bedrock", time.Now().UTC())

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic_bedrock: send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("anthropic_bedrock: API error (status %d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 16)
	go bedrockReadSSE(resp.Body, ch)
	return ch, nil
}

func bedrockBuildRequest(model string, maxTokens int, messages []Message, tools []ToolDef, stream bool) *anthropicRequest {
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

func bedrockParseResponse(apiResp *anthropicResponse) *Response {
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

func bedrockReadSSE(body io.ReadCloser, ch chan<- StreamEvent) {
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

// sigv4Sign signs an HTTP request using AWS Signature Version 4.
// It modifies the request in-place by adding X-Amz-Date, X-Amz-Security-Token (if applicable),
// and Authorization headers.
func sigv4Sign(req *http.Request, payload []byte, accessKey, secretKey, sessionToken, region, service string, now time.Time) {
	datestamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	req.Header.Set("X-Amz-Date", amzDate)
	if sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", sessionToken)
	}

	// Hash payload
	payloadHash := sha256Hex(payload)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	// Canonical headers — must be sorted by lowercase header name
	signedHeaderNames := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	if sessionToken != "" {
		signedHeaderNames = append(signedHeaderNames, "x-amz-security-token")
	}
	sort.Strings(signedHeaderNames)

	var canonicalHeaders strings.Builder
	for _, name := range signedHeaderNames {
		var val string
		if name == "host" {
			val = req.Host
			if val == "" {
				val = req.URL.Host
			}
		} else {
			val = req.Header.Get(name)
		}
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(val))
		canonicalHeaders.WriteByte('\n')
	}

	signedHeaders := strings.Join(signedHeaderNames, ";")

	// Canonical request
	canonicalPath := req.URL.Path
	if canonicalPath == "" {
		canonicalPath = "/"
	}
	canonicalQuery := req.URL.Query().Encode() // already sorted by Go

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalPath,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign
	scope := datestamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))

	// Derive signing key
	signingKey := hmacSHA256([]byte("AWS4"+secretKey), []byte(datestamp))
	signingKey = hmacSHA256(signingKey, []byte(region))
	signingKey = hmacSHA256(signingKey, []byte(service))
	signingKey = hmacSHA256(signingKey, []byte("aws4_request"))

	// Calculate signature
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// Set Authorization header
	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
