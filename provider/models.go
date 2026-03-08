package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// ModelInfo describes an available model from a provider.
type ModelInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window,omitempty"`
}

// ListModels fetches available models from the given provider type.
// Only requires an API key and optional base URL — no saved provider needed.
func ListModels(ctx context.Context, providerType, apiKey, baseURL string) ([]ModelInfo, error) {
	switch providerType {
	case "anthropic":
		return listAnthropicModels(ctx, apiKey, baseURL)
	case "openai":
		return listOpenAIModels(ctx, apiKey, baseURL)
	case "openrouter":
		if baseURL == "" {
			baseURL = "https://openrouter.ai/api/v1"
		}
		return listOpenAIModels(ctx, apiKey, baseURL)
	case "copilot":
		return listCopilotModels(ctx, apiKey, baseURL)
	case "cohere":
		return listCohereModels(ctx, apiKey, baseURL)
	case "mock":
		return []ModelInfo{
			{ID: "mock-default", Name: "Mock Provider"},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

// listAnthropicModels calls the Anthropic /v1/models endpoint.
func listAnthropicModels(ctx context.Context, apiKey, baseURL string) ([]ModelInfo, error) {
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
