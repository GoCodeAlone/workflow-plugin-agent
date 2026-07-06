package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/iterator"
	googleoption "google.golang.org/api/option"
)

// ModelInfo describes an available model from a provider.
type ModelInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window,omitempty"`
}

type ModelListRequest struct {
	ProviderType string
	APIKey       string
	BaseURL      string
}

type ModelLister func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error)

// Constants used by model-listing functions, sourced from former provider files.
const (
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	anthropicAPIVersion     = "2023-06-01"
	defaultOpenAIBaseURL    = "https://api.openai.com"
	defaultOpenAIChatGPTURL = "https://chatgpt.com/backend-api/codex"
	defaultCopilotBaseURL   = "https://api.githubcopilot.com"
	copilotTokenExchangeURL = "https://api.github.com/copilot_internal/v2/token"
	copilotEditorVersion    = "ratchet/0.1.0"
	defaultCohereBaseURL    = "https://api.cohere.com"
)

// copilotTokenResponse is the response from the Copilot token exchange endpoint.
type copilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// ListModels fetches available models from the given provider type.
// Only requires an API key and optional base URL — no saved provider needed.
func ListModels(ctx context.Context, providerType, apiKey, baseURL string) ([]ModelInfo, error) {
	lister, ok := modelListers[providerType]
	if !ok {
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
	return lister(ctx, ModelListRequest{ProviderType: providerType, APIKey: apiKey, BaseURL: baseURL})
}

var modelListers = map[string]ModelLister{
	"anthropic": func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error) {
		return listAnthropicModels(ctx, req.APIKey, req.BaseURL)
	},
	"openai": func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error) {
		return listOpenAIModels(ctx, req.APIKey, req.BaseURL)
	},
	"openai_chatgpt": func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error) {
		return listOpenAIChatGPTModels(ctx, req.APIKey, req.BaseURL)
	},
	"openrouter": func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error) {
		baseURL := req.BaseURL
		if baseURL == "" {
			baseURL = "https://openrouter.ai/api/v1"
		}
		return listOpenAIModels(ctx, req.APIKey, baseURL)
	},
	"copilot": func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error) {
		return listCopilotModels(ctx, req.APIKey, req.BaseURL)
	},
	"copilot_models": func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error) {
		baseURL := req.BaseURL
		if baseURL == "" {
			baseURL = "https://models.github.ai/inference"
		}
		return listOpenAIModels(ctx, req.APIKey, baseURL)
	},
	"openai_azure": func(context.Context, ModelListRequest) ([]ModelInfo, error) {
		return azureOpenAIFallbackModels(), nil
	},
	"anthropic_bedrock": func(context.Context, ModelListRequest) ([]ModelInfo, error) {
		return bedrockFallbackModels(), nil
	},
	"anthropic_vertex": func(context.Context, ModelListRequest) ([]ModelInfo, error) {
		return vertexFallbackModels(), nil
	},
	"anthropic_foundry": func(context.Context, ModelListRequest) ([]ModelInfo, error) {
		return foundryFallbackModels(), nil
	},
	"gemini": func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error) {
		return listGeminiModels(ctx, req.APIKey)
	},
	"cohere": func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error) {
		return listCohereModels(ctx, req.APIKey, req.BaseURL)
	},
	"ollama": func(ctx context.Context, req ModelListRequest) ([]ModelInfo, error) {
		return listOllamaModels(ctx, req.BaseURL)
	},
	"llama_cpp": func(context.Context, ModelListRequest) ([]ModelInfo, error) {
		return []ModelInfo{{ID: "local", Name: "llama.cpp Local Model"}}, nil
	},
	"mock": func(context.Context, ModelListRequest) ([]ModelInfo, error) {
		return []ModelInfo{{ID: "mock-default", Name: "Mock Provider"}}, nil
	},
}

// listAnthropicModels calls the Anthropic /v1/models endpoint.
func listAnthropicModels(ctx context.Context, apiKey, baseURL string) ([]ModelInfo, error) {
	if err := ValidateBaseURL(baseURL); err != nil {
		return nil, err
	}
	if baseURL == "" {
		baseURL = defaultAnthropicBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Type        string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	var models []ModelInfo
	for _, m := range result.Data {
		if m.Type != "" && m.Type != "model" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		models = append(models, ModelInfo{
			ID:   m.ID,
			Name: name,
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return models, nil
}

// listOpenAIModels calls the OpenAI /v1/models endpoint.
func listOpenAIModels(ctx context.Context, apiKey, baseURL string) ([]ModelInfo, error) {
	if err := ValidateBaseURL(baseURL); err != nil {
		return nil, err
	}
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// Filter to chat-capable models
	chatPrefixes := []string{"gpt-4", "gpt-3.5", "o1", "o3", "chatgpt"}
	var models []ModelInfo
	for _, m := range result.Data {
		isChatModel := false
		lower := strings.ToLower(m.ID)
		for _, prefix := range chatPrefixes {
			if strings.HasPrefix(lower, prefix) {
				isChatModel = true
				break
			}
		}
		if !isChatModel {
			continue
		}
		models = append(models, ModelInfo{
			ID:   m.ID,
			Name: m.ID,
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return models, nil
}

func listOpenAIChatGPTModels(ctx context.Context, tokenJSON, baseURL string) ([]ModelInfo, error) {
	if err := ValidateBaseURL(baseURL); err != nil {
		return nil, err
	}
	if baseURL == "" {
		baseURL = defaultOpenAIChatGPTURL
	}
	token, accountID, err := parseOpenAIChatGPTModelToken(tokenJSON)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models?client_version=1.0.0", nil)
	if err != nil {
		return nil, fmt.Errorf("openai_chatgpt: create models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai_chatgpt: models request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai_chatgpt: read models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai_chatgpt: models API error (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result struct {
		Models []struct {
			Slug             string `json:"slug"`
			ID               string `json:"id"`
			DisplayName      string `json:"display_name"`
			Name             string `json:"name"`
			Visibility       string `json:"visibility"`
			ShowInPicker     *bool  `json:"show_in_picker"`
			ContextWindow    int    `json:"context_window"`
			MaxContextWindow int    `json:"max_context_window"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("openai_chatgpt: parse models response: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Models))
	for _, m := range result.Models {
		if m.Visibility != "" && strings.ToLower(m.Visibility) != "list" {
			continue
		}
		if m.ShowInPicker != nil && !*m.ShowInPicker {
			continue
		}
		id := strings.TrimSpace(m.Slug)
		if id == "" {
			id = strings.TrimSpace(m.ID)
		}
		if id == "" {
			continue
		}
		name := strings.TrimSpace(m.DisplayName)
		if name == "" {
			name = strings.TrimSpace(m.Name)
		}
		if name == "" {
			name = id
		}
		contextWindow := m.MaxContextWindow
		if contextWindow == 0 {
			contextWindow = m.ContextWindow
		}
		models = append(models, ModelInfo{ID: id, Name: name, ContextWindow: contextWindow})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("openai_chatgpt: models response did not include selectable models")
	}
	// ChatGPT returns the same picker order used by Codex/OpenClaw. Preserve it
	// so the default selection follows the account-visible catalog ordering.
	return models, nil
}

func parseOpenAIChatGPTModelToken(raw string) (accessToken, accountID string, err error) {
	var wrapper struct {
		AccessToken      string `json:"access_token"`
		IDToken          string `json:"id_token"`
		AccountID        string `json:"account_id"`
		ChatGPTAccountID string `json:"chatgpt_account_id"`
		Tokens           *struct {
			AccessToken      string `json:"access_token"`
			IDToken          string `json:"id_token"`
			AccountID        string `json:"account_id"`
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return "", "", fmt.Errorf("openai_chatgpt: parse token bundle: %w", err)
	}
	if wrapper.Tokens != nil {
		wrapper.AccessToken = wrapper.Tokens.AccessToken
		wrapper.IDToken = wrapper.Tokens.IDToken
		wrapper.AccountID = wrapper.Tokens.AccountID
		wrapper.ChatGPTAccountID = wrapper.Tokens.ChatGPTAccountID
	}
	accessToken = strings.TrimSpace(wrapper.AccessToken)
	if accessToken == "" {
		return "", "", fmt.Errorf("openai_chatgpt: token bundle requires access_token for model discovery")
	}
	accountID = strings.TrimSpace(wrapper.ChatGPTAccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(wrapper.AccountID)
	}
	if claimAccountID := openAIChatGPTAccountIDFromIDToken(wrapper.IDToken); claimAccountID != "" {
		accountID = claimAccountID
	}
	return accessToken, accountID, nil
}

func openAIChatGPTAccountIDFromIDToken(idToken string) string {
	var claims map[string]any
	if !decodeModelJWTPayload(idToken, &claims) {
		return ""
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	if auth == nil {
		return ""
	}
	accountID, _ := auth["chatgpt_account_id"].(string)
	return strings.TrimSpace(accountID)
}

func decodeModelJWTPayload(token string, out any) bool {
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

// exchangeCopilotToken exchanges a GitHub OAuth token for a Copilot bearer token.
func exchangeCopilotToken(ctx context.Context, oauthToken, tokenURL string) (string, error) {
	if tokenURL == "" {
		tokenURL = copilotTokenExchangeURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("copilot: create token exchange request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+oauthToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("copilot: token exchange request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("copilot: read token exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("copilot: token exchange failed (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}

	var tokenResp copilotTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("copilot: parse token exchange response: %w", err)
	}

	return tokenResp.Token, nil
}

// listCopilotModels calls the Copilot /models endpoint to retrieve available models.
// Falls back to a curated list if the API call fails.
func listCopilotModels(ctx context.Context, apiKey, baseURL string) ([]ModelInfo, error) {
	if baseURL == "" {
		baseURL = defaultCopilotBaseURL
	}

	// Exchange OAuth token for Copilot bearer token.
	bearerToken, err := exchangeCopilotToken(ctx, apiKey, "")
	if err != nil {
		return copilotFallbackModels(), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return copilotFallbackModels(), nil
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("Editor-Version", "vscode/1.100.0")
	req.Header.Set("Editor-Plugin-Version", copilotEditorVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotFallbackModels(), nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return copilotFallbackModels(), nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return copilotFallbackModels(), nil
	}

	var result struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return copilotFallbackModels(), nil
	}

	var models []ModelInfo
	for _, m := range result.Data {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, ModelInfo{
			ID:   m.ID,
			Name: name,
		})
	}

	if len(models) == 0 {
		return copilotFallbackModels(), nil
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return models, nil
}

func copilotFallbackModels() []ModelInfo {
	return []ModelInfo{
		{ID: "claude-sonnet-4", Name: "Claude Sonnet 4"},
		{ID: "gpt-4.1", Name: "GPT-4.1"},
		{ID: "gpt-4o", Name: "GPT-4o"},
		{ID: "gpt-4o-mini", Name: "GPT-4o Mini"},
		{ID: "o3-mini", Name: "o3-mini"},
	}
}

// listCohereModels calls the Cohere /v1/models endpoint.
func listCohereModels(ctx context.Context, apiKey, baseURL string) ([]ModelInfo, error) {
	if err := ValidateBaseURL(baseURL); err != nil {
		return cohereFallbackModels(), nil
	}
	if baseURL == "" {
		baseURL = defaultCohereBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return cohereFallbackModels(), nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return cohereFallbackModels(), nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return cohereFallbackModels(), nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return cohereFallbackModels(), nil
	}

	var result struct {
		Models []struct {
			Name      string   `json:"name"`
			Endpoints []string `json:"endpoints"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return cohereFallbackModels(), nil
	}

	var models []ModelInfo
	for _, m := range result.Models {
		// Filter to models that support the chat endpoint
		hasChat := false
		for _, ep := range m.Endpoints {
			if ep == "chat" {
				hasChat = true
				break
			}
		}
		if !hasChat {
			continue
		}
		models = append(models, ModelInfo{
			ID:   m.Name,
			Name: m.Name,
		})
	}

	if len(models) == 0 {
		return cohereFallbackModels(), nil
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return models, nil
}

func cohereFallbackModels() []ModelInfo {
	return []ModelInfo{
		{ID: "command-a-03-2025", Name: "Command A (March 2025)"},
		{ID: "command-r", Name: "Command R"},
		{ID: "command-r-plus", Name: "Command R+"},
	}
}

func azureOpenAIFallbackModels() []ModelInfo {
	return []ModelInfo{
		{ID: "gpt-4o", Name: "GPT-4o"},
		{ID: "gpt-4o-mini", Name: "GPT-4o Mini"},
		{ID: "gpt-4.1", Name: "GPT-4.1"},
		{ID: "gpt-4.1-mini", Name: "GPT-4.1 Mini"},
		{ID: "o3-mini", Name: "o3-mini"},
	}
}

func bedrockFallbackModels() []ModelInfo {
	return []ModelInfo{
		{ID: "anthropic.claude-opus-4-20250514-v1:0", Name: "Claude Opus 4"},
		{ID: "anthropic.claude-sonnet-4-20250514-v1:0", Name: "Claude Sonnet 4"},
		{ID: "anthropic.claude-haiku-4-20250514-v1:0", Name: "Claude Haiku 4"},
		{ID: "anthropic.claude-3-5-sonnet-20241022-v2:0", Name: "Claude 3.5 Sonnet v2"},
		{ID: "anthropic.claude-3-5-haiku-20241022-v1:0", Name: "Claude 3.5 Haiku"},
	}
}

func vertexFallbackModels() []ModelInfo {
	return []ModelInfo{
		{ID: "claude-opus-4@20250514", Name: "Claude Opus 4"},
		{ID: "claude-sonnet-4@20250514", Name: "Claude Sonnet 4"},
		{ID: "claude-haiku-4@20250514", Name: "Claude Haiku 4"},
		{ID: "claude-3-5-sonnet-v2@20241022", Name: "Claude 3.5 Sonnet v2"},
		{ID: "claude-3-5-haiku@20241022", Name: "Claude 3.5 Haiku"},
	}
}

func foundryFallbackModels() []ModelInfo {
	return []ModelInfo{
		{ID: "claude-opus-4-20250514", Name: "Claude Opus 4"},
		{ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4"},
		{ID: "claude-haiku-4-20250514", Name: "Claude Haiku 4"},
		{ID: "claude-3-5-sonnet-20241022-v2", Name: "Claude 3.5 Sonnet v2"},
		{ID: "claude-3-5-haiku-20241022", Name: "Claude 3.5 Haiku"},
	}
}

// listGeminiModels lists available Gemini models using the genai SDK.
func listGeminiModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	client, err := genai.NewClient(ctx, googleoption.WithAPIKey(apiKey))
	if err != nil {
		return geminiFallbackModels(), nil
	}
	defer client.Close()

	iter := client.ListModels(ctx)
	var models []ModelInfo
	for {
		m, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return geminiFallbackModels(), nil
		}
		models = append(models, ModelInfo{
			ID:   m.Name,
			Name: m.DisplayName,
		})
	}
	if len(models) == 0 {
		return geminiFallbackModels(), nil
	}
	return models, nil
}

func geminiFallbackModels() []ModelInfo {
	return []ModelInfo{
		{ID: "gemini-2.5-pro-preview-03-25", Name: "Gemini 2.5 Pro Preview"},
		{ID: "gemini-2.0-flash", Name: "Gemini 2.0 Flash"},
		{ID: "gemini-2.0-flash-lite", Name: "Gemini 2.0 Flash-Lite"},
		{ID: "gemini-1.5-pro", Name: "Gemini 1.5 Pro"},
		{ID: "gemini-1.5-flash", Name: "Gemini 1.5 Flash"},
	}
}

// listOllamaModels lists models available on a local Ollama server.
func listOllamaModels(ctx context.Context, baseURL string) ([]ModelInfo, error) {
	c := NewOllamaClient(baseURL)
	return c.ListModels(ctx)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
