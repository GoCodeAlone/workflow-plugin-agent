package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const defaultAzureOpenAIAPIVersion = "2024-10-21"

// OpenAIAzureConfig configures the OpenAI provider for Azure OpenAI Service.
// Uses Azure API keys or Entra ID tokens. URLs follow the pattern:
// {resource}.openai.azure.com/openai/deployments/{deployment}/chat/completions?api-version={version}
type OpenAIAzureConfig struct {
	// Resource is the Azure OpenAI resource name.
	Resource string
	// DeploymentName is the model deployment name in Azure.
	DeploymentName string
	// APIVersion is the Azure API version (e.g. "2024-10-21").
	APIVersion string
	// MaxTokens limits the response length.
	MaxTokens int
	// APIKey is the Azure API key (use this OR Entra ID token, not both).
	APIKey string
	// EntraToken is a Microsoft Entra ID bearer token (optional, alternative to APIKey).
	EntraToken string
	// HTTPClient overrides the default HTTP client.
	HTTPClient *http.Client
}

// OpenAIAzureProvider accesses OpenAI models via Azure OpenAI Service.
type OpenAIAzureProvider struct {
	config OpenAIAzureConfig
	// endpoint is the fully constructed chat completions URL.
	endpoint string
}

func (p *OpenAIAzureProvider) Name() string { return "openai_azure" }

func (p *OpenAIAzureProvider) AuthModeInfo() AuthModeInfo {
	return AuthModeInfo{
		Mode:        "azure",
		DisplayName: "OpenAI (Azure OpenAI Service)",
		Description: "Access OpenAI models via Azure OpenAI Service using Azure API keys or Microsoft Entra ID tokens. Uses deployment-specific URLs.",
		DocsURL:     "https://learn.microsoft.com/en-us/azure/ai-services/openai/reference",
		ServerSafe:  true,
	}
}

// NewOpenAIAzureProvider creates a provider that accesses OpenAI models via Azure.
//
// Docs: https://learn.microsoft.com/en-us/azure/ai-services/openai/reference
func NewOpenAIAzureProvider(cfg OpenAIAzureConfig) (*OpenAIAzureProvider, error) {
	if cfg.Resource == "" {
		return nil, fmt.Errorf("openai_azure: Resource is required")
	}
	if cfg.DeploymentName == "" {
		return nil, fmt.Errorf("openai_azure: DeploymentName is required")
	}
	if cfg.APIKey == "" && cfg.EntraToken == "" {
		return nil, fmt.Errorf("openai_azure: APIKey or EntraToken is required")
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = defaultAzureOpenAIAPIVersion
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultOpenAIMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	endpoint := fmt.Sprintf("https://%s.openai.azure.com/openai/deployments/%s/chat/completions?api-version=%s",
		cfg.Resource, cfg.DeploymentName, cfg.APIVersion)

	return &OpenAIAzureProvider{config: cfg, endpoint: endpoint}, nil
}

func (p *OpenAIAzureProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	reqBody := p.buildRequest(messages, tools, false)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai_azure: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("openai_azure: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai_azure: send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai_azure: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai_azure: API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp openaiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("openai_azure: unmarshal response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("openai_azure: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return p.parseResponse(&apiResp)
}

func (p *OpenAIAzureProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	reqBody := p.buildRequest(messages, tools, true)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai_azure: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("openai_azure: create request: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai_azure: send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("openai_azure: API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Reuse the OpenAI SSE parser — create a temporary OpenAIProvider just for readSSE.
	oai := &OpenAIProvider{}
	ch := make(chan StreamEvent, 16)
	go oai.readSSE(resp.Body, ch)
	return ch, nil
}

func (p *OpenAIAzureProvider) buildRequest(messages []Message, tools []ToolDef, stream bool) *openaiRequest {
	req := &openaiRequest{
		Model:     p.config.DeploymentName,
		MaxTokens: p.config.MaxTokens,
		Stream:    stream,
	}

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

func (p *OpenAIAzureProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.config.APIKey != "" {
		req.Header.Set("api-key", p.config.APIKey)
	} else if p.config.EntraToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.EntraToken)
	}
}

func (p *OpenAIAzureProvider) parseResponse(apiResp *openaiResponse) (*Response, error) {
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
				return nil, fmt.Errorf("openai_azure: unmarshal tool call arguments for %q: %w", tc.Function.Name, err)
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
