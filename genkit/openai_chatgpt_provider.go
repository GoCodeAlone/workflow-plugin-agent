package genkit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

const (
	defaultOpenAIChatGPTModel      = "gpt-5-codex"
	defaultOpenAIChatGPTBaseURL    = "https://chatgpt.com/backend-api/codex"
	defaultOpenAIChatGPTRefreshURL = "https://auth.openai.com/oauth/token"
	openAIChatGPTClientID          = "app_EMoamEEZ73f0CkXaXp7hrann"
)

// OpenAIChatGPTTokenBundle contains ChatGPT subscription credentials compatible
// with the Codex CLI auth.json token shape.
type OpenAIChatGPTTokenBundle struct {
	AccessToken             string `json:"access_token"`
	RefreshToken            string `json:"refresh_token"`
	IDToken                 string `json:"id_token,omitempty"`
	AccountID               string `json:"account_id,omitempty"`
	ChatGPTAccountID        string `json:"chatgpt_account_id,omitempty"`
	ChatGPTAccountIsFedRAMP bool   `json:"chatgpt_account_is_fedramp,omitempty"`
	ChatGPTUserID           string `json:"chatgpt_user_id,omitempty"`
	PlanType                string `json:"chatgpt_plan_type,omitempty"`
	ExpiresAt               int64  `json:"expires_at,omitempty"`
}

type openAIChatGPTProvider struct {
	client     *http.Client
	model      string
	baseURL    string
	refreshURL string
	maxTokens  int

	mu     sync.Mutex
	tokens OpenAIChatGPTTokenBundle
}

// ParseOpenAIChatGPTTokenBundle parses either a direct token bundle or Codex
// CLI auth.json, whose token fields are nested under "tokens".
func ParseOpenAIChatGPTTokenBundle(raw string) (*OpenAIChatGPTTokenBundle, error) {
	var wrapper struct {
		OpenAIChatGPTTokenBundle
		Tokens *OpenAIChatGPTTokenBundle `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return nil, fmt.Errorf("openai_chatgpt: parse token bundle: %w", err)
	}

	bundle := wrapper.OpenAIChatGPTTokenBundle
	if wrapper.Tokens != nil {
		bundle = *wrapper.Tokens
	}
	bundle.applyJWTClaims()

	if bundle.AccessToken == "" && bundle.RefreshToken == "" {
		return nil, errors.New("openai_chatgpt: token bundle requires access_token or refresh_token")
	}
	return &bundle, nil
}

// NewOpenAIChatGPTProvider creates a provider that uses a ChatGPT subscription
// token bundle against OpenAI's Codex Responses endpoint.
func NewOpenAIChatGPTProvider(ctx context.Context, tokenJSON, model, baseURL string, maxTokens int) (provider.Provider, error) {
	_ = ctx
	if baseURL != "" {
		if err := provider.ValidateBaseURL(baseURL); err != nil {
			return nil, fmt.Errorf("openai_chatgpt: %w", err)
		}
	}
	return newOpenAIChatGPTProviderWithClient(http.DefaultClient, tokenJSON, model, baseURL, defaultOpenAIChatGPTRefreshURL, maxTokens)
}

func newOpenAIChatGPTProviderWithClient(client *http.Client, tokenJSON, model, baseURL, refreshURL string, maxTokens int) (provider.Provider, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if model == "" {
		model = defaultOpenAIChatGPTModel
	}
	if baseURL == "" {
		baseURL = defaultOpenAIChatGPTBaseURL
	}
	if refreshURL == "" {
		refreshURL = defaultOpenAIChatGPTRefreshURL
	}
	tokens, err := ParseOpenAIChatGPTTokenBundle(tokenJSON)
	if err != nil {
		return nil, err
	}
	return &openAIChatGPTProvider{
		client:     client,
		model:      model,
		baseURL:    strings.TrimRight(baseURL, "/"),
		refreshURL: refreshURL,
		maxTokens:  maxTokens,
		tokens:     *tokens,
	}, nil
}

func (p *openAIChatGPTProvider) Name() string { return "openai_chatgpt" }

func (p *openAIChatGPTProvider) AuthModeInfo() provider.AuthModeInfo {
	return provider.AuthModeInfo{
		Mode:        "chatgpt",
		DisplayName: "OpenAI ChatGPT subscription",
		Description: "Uses ChatGPT account credentials for OpenAI Codex models. For local CLI/IDE use only.",
		DocsURL:     "https://developers.openai.com/codex/auth",
		ServerSafe:  false,
	}
}

func (p *openAIChatGPTProvider) Chat(ctx context.Context, messages []provider.Message, tools []provider.ToolDef) (*provider.Response, error) {
	body, err := p.responsesRequestBody(messages, tools, false)
	if err != nil {
		return nil, err
	}
	resp, err := p.doResponsesRequest(ctx, body, "application/json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseStatusError("openai_chatgpt: responses request", resp)
	}

	var parsed openAIChatGPTResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("openai_chatgpt: decode response: %w", err)
	}
	return parsed.toProviderResponse(), nil
}

func (p *openAIChatGPTProvider) Stream(ctx context.Context, messages []provider.Message, tools []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	body, err := p.responsesRequestBody(messages, tools, true)
	if err != nil {
		return nil, err
	}
	resp, err := p.doResponsesRequest(ctx, body, "text/event-stream")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, responseStatusError("openai_chatgpt: responses stream", resp)
	}

	ch := make(chan provider.StreamEvent, 64)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" || data == "[DONE]" {
				continue
			}
			if evs, err := parseOpenAIChatGPTSSE(data); err != nil {
				ch <- provider.StreamEvent{Type: "error", Error: err.Error()}
				return
			} else {
				for _, ev := range evs {
					ch <- ev
				}
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- provider.StreamEvent{Type: "error", Error: fmt.Sprintf("openai_chatgpt: read stream: %v", err)}
			return
		}
	}()
	return ch, nil
}

func (p *openAIChatGPTProvider) doResponsesRequest(ctx context.Context, body []byte, accept string) (*http.Response, error) {
	accessToken, accountID, fedramp, err := p.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai_chatgpt: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	if fedramp {
		req.Header.Set("x-openai-fedramp", "true")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai_chatgpt: responses request failed: %w", err)
	}
	return resp, nil
}

func (p *openAIChatGPTProvider) ensureAccessToken(ctx context.Context) (string, string, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tokens.AccessToken != "" && !p.tokens.accessTokenExpiredSoon(time.Now()) {
		return p.tokens.AccessToken, p.tokens.effectiveAccountID(), p.tokens.ChatGPTAccountIsFedRAMP, nil
	}
	if p.tokens.RefreshToken == "" {
		return "", "", false, errors.New("openai_chatgpt: access token expired and refresh_token is missing")
	}

	payload, err := json.Marshal(map[string]string{
		"client_id":     openAIChatGPTClientID,
		"grant_type":    "refresh_token",
		"refresh_token": p.tokens.RefreshToken,
	})
	if err != nil {
		return "", "", false, fmt.Errorf("openai_chatgpt: encode refresh request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.refreshURL, bytes.NewReader(payload))
	if err != nil {
		return "", "", false, fmt.Errorf("openai_chatgpt: create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", "", false, fmt.Errorf("openai_chatgpt: refresh failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", false, responseStatusError("openai_chatgpt: refresh token", resp)
	}

	var refreshed OpenAIChatGPTTokenBundle
	if err := json.NewDecoder(resp.Body).Decode(&refreshed); err != nil {
		return "", "", false, fmt.Errorf("openai_chatgpt: decode refresh response: %w", err)
	}
	if refreshed.AccessToken == "" {
		return "", "", false, errors.New("openai_chatgpt: refresh response missing access_token")
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = p.tokens.RefreshToken
	}
	refreshed.applyJWTClaims()
	p.tokens = refreshed
	return p.tokens.AccessToken, p.tokens.effectiveAccountID(), p.tokens.ChatGPTAccountIsFedRAMP, nil
}

func (p *openAIChatGPTProvider) responsesRequestBody(messages []provider.Message, tools []provider.ToolDef, stream bool) ([]byte, error) {
	body := map[string]any{
		"model":  p.model,
		"input":  toOpenAIChatGPTInput(messages),
		"stream": stream,
	}
	if p.maxTokens > 0 {
		body["max_output_tokens"] = p.maxTokens
	}
	if len(tools) > 0 {
		body["tools"] = toOpenAIChatGPTTools(tools)
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai_chatgpt: encode request: %w", err)
	}
	return out, nil
}

func toOpenAIChatGPTInput(messages []provider.Message) []map[string]any {
	input := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		role := string(msg.Role)
		switch msg.Role {
		case provider.RoleSystem, provider.RoleUser:
			input = append(input, map[string]any{
				"role": role,
				"content": []map[string]string{{
					"type": "input_text",
					"text": msg.Content,
				}},
			})
		case provider.RoleAssistant:
			item := map[string]any{
				"role": "assistant",
				"content": []map[string]string{{
					"type": "output_text",
					"text": msg.Content,
				}},
			}
			if len(msg.ToolCalls) > 0 {
				item["tool_calls"] = msg.ToolCalls
			}
			input = append(input, item)
		case provider.RoleTool:
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
				"output":  msg.Content,
			})
		default:
			input = append(input, map[string]any{
				"role": role,
				"content": []map[string]string{{
					"type": "input_text",
					"text": msg.Content,
				}},
			})
		}
	}
	return input
}

func toOpenAIChatGPTTools(tools []provider.ToolDef) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		})
	}
	return out
}

type openAIChatGPTResponse struct {
	OutputText string `json:"output_text"`
	Output     []struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage openAIChatGPTUsage `json:"usage"`
}

type openAIChatGPTUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (r openAIChatGPTResponse) toProviderResponse() *provider.Response {
	resp := &provider.Response{
		Content: r.OutputText,
		Usage: provider.Usage{
			InputTokens:  r.Usage.InputTokens,
			OutputTokens: r.Usage.OutputTokens,
		},
	}
	var text strings.Builder
	for _, item := range r.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Text != "" {
					text.WriteString(c.Text)
				}
			}
		case "function_call":
			resp.ToolCalls = append(resp.ToolCalls, provider.ToolCall{
				ID:        item.ID,
				Name:      item.Name,
				Arguments: normalizeToolArguments(item.Arguments),
			})
		}
	}
	if resp.Content == "" {
		resp.Content = text.String()
	}
	return resp
}

func normalizeToolArguments(v any) map[string]any {
	switch typed := v.(type) {
	case map[string]any:
		return typed
	case string:
		var parsed map[string]any
		if json.Unmarshal([]byte(typed), &parsed) == nil {
			return parsed
		}
	}
	return map[string]any{}
}

func parseOpenAIChatGPTSSE(data string) ([]provider.StreamEvent, error) {
	var raw struct {
		Type     string                 `json:"type"`
		Delta    string                 `json:"delta"`
		Text     string                 `json:"text"`
		Response *openAIChatGPTResponse `json:"response"`
		Item     *struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Arguments any    `json:"arguments"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("openai_chatgpt: parse stream event: %w", err)
	}
	switch raw.Type {
	case "response.output_text.delta":
		delta := raw.Delta
		if delta == "" {
			delta = raw.Text
		}
		if delta == "" {
			return nil, nil
		}
		return []provider.StreamEvent{{Type: "text", Text: delta}}, nil
	case "response.completed":
		ev := provider.StreamEvent{Type: "done"}
		if raw.Response != nil {
			ev.Usage = &provider.Usage{
				InputTokens:  raw.Response.Usage.InputTokens,
				OutputTokens: raw.Response.Usage.OutputTokens,
			}
		}
		return []provider.StreamEvent{ev}, nil
	case "response.output_item.done":
		if raw.Item != nil && raw.Item.Type == "function_call" {
			return []provider.StreamEvent{{
				Type: "tool_call",
				Tool: &provider.ToolCall{
					ID:        raw.Item.ID,
					Name:      raw.Item.Name,
					Arguments: normalizeToolArguments(raw.Item.Arguments),
				},
			}}, nil
		}
	}
	return nil, nil
}

func (b *OpenAIChatGPTTokenBundle) accessTokenExpiredSoon(now time.Time) bool {
	exp := b.ExpiresAt
	if exp == 0 {
		exp = jwtExp(b.AccessToken)
		b.ExpiresAt = exp
	}
	if exp == 0 {
		return false
	}
	return time.Unix(exp, 0).Before(now.Add(5 * time.Minute))
}

func (b *OpenAIChatGPTTokenBundle) effectiveAccountID() string {
	if b.ChatGPTAccountID != "" {
		return b.ChatGPTAccountID
	}
	return b.AccountID
}

func (b *OpenAIChatGPTTokenBundle) applyJWTClaims() {
	if b.ExpiresAt == 0 {
		b.ExpiresAt = jwtExp(b.AccessToken)
	}
	var claims map[string]any
	if !decodeJWTPayload(b.IDToken, &claims) {
		return
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	if auth == nil {
		return
	}
	if v, _ := auth["chatgpt_account_id"].(string); v != "" && b.ChatGPTAccountID == "" {
		b.ChatGPTAccountID = v
	}
	if v, _ := auth["chatgpt_user_id"].(string); v != "" && b.ChatGPTUserID == "" {
		b.ChatGPTUserID = v
	}
	if v, _ := auth["user_id"].(string); v != "" && b.ChatGPTUserID == "" {
		b.ChatGPTUserID = v
	}
	if v, _ := auth["chatgpt_plan_type"].(string); v != "" && b.PlanType == "" {
		b.PlanType = v
	}
	if v, ok := auth["chatgpt_account_is_fedramp"].(bool); ok {
		b.ChatGPTAccountIsFedRAMP = v
	}
}

func jwtExp(token string) int64 {
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if !decodeJWTPayload(token, &claims) {
		return 0
	}
	return claims.Exp
}

func decodeJWTPayload(token string, out any) bool {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return false
		}
	}
	return json.Unmarshal(payload, out) == nil
}

func responseStatusError(prefix string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Errorf("%s failed with status %d", prefix, resp.StatusCode)
	}
	return fmt.Errorf("%s failed with status %d: %s", prefix, resp.StatusCode, msg)
}
