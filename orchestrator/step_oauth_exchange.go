package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

const (
	anthropicOAuthTokenURL   = "https://console.anthropic.com/v1/oauth/token"
	anthropicCreateAPIKeyURL = "https://api.anthropic.com/api/oauth/claude_cli/create_api_key"
	anthropicOAuthClientID   = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	oauthHTTPTimeout         = 15 * time.Second
)

// OAuthExchangeStep handles OAuth authorization code exchange server-side,
// proxying to provider OAuth endpoints to avoid CORS issues in the browser.
type OAuthExchangeStep struct {
	name string
	app  modular.Application
}

func (s *OAuthExchangeStep) Name() string { return s.name }

func (s *OAuthExchangeStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	body := extractBody(pc)
	providerType := extractString(body, "provider", "")
	code := extractString(body, "code", "")
	codeVerifier := extractString(body, "code_verifier", "")
	redirectURI := extractString(body, "redirect_uri", "")

	switch providerType {
	case "anthropic":
		result, err := exchangeAnthropicOAuth(ctx, code, codeVerifier, redirectURI)
		if err != nil {
			return &module.StepResult{
				Output: map[string]any{
					"success": false,
					"error":   err.Error(),
				},
			}, nil
		}
		return &module.StepResult{Output: result}, nil
	default:
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "unsupported provider",
			},
		}, nil
	}
}

// exchangeAnthropicOAuth performs the two-step Anthropic OAuth exchange:
// 1. Exchange auth code for access token
// 2. Use access token to create a permanent API key
func exchangeAnthropicOAuth(ctx context.Context, code, codeVerifier, redirectURI string) (map[string]any, error) {
	client := &http.Client{Timeout: oauthHTTPTimeout}

	// Step 1: Exchange code for access token
	tokenReqBody, err := json.Marshal(map[string]any{
		"code":          code,
		"state":         codeVerifier,
		"grant_type":    "authorization_code",
		"client_id":     anthropicOAuthClientID,
		"redirect_uri":  redirectURI,
		"code_verifier": codeVerifier,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicOAuthTokenURL, bytes.NewReader(tokenReqBody))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	tokenReq.Header.Set("Content-Type", "application/json")

	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = tokenResp.Body.Close() }()

	tokenBody, err := io.ReadAll(tokenResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if tokenResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint error (status %d): %s", tokenResp.StatusCode, truncateOAuth(string(tokenBody), 200))
	}

	var tokenResult map[string]any
	if err := json.Unmarshal(tokenBody, &tokenResult); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	accessToken, _ := tokenResult["access_token"].(string)
	if accessToken == "" {
		return nil, fmt.Errorf("no access_token in response")
	}

	// Step 2: Create a permanent API key using the access token
	apiKeyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicCreateAPIKeyURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create api key request: %w", err)
	}
	apiKeyReq.Header.Set("Authorization", "Bearer "+accessToken)
	apiKeyReq.Header.Set("Content-Type", "application/json")

	apiKeyResp, err := client.Do(apiKeyReq)
	if err != nil {
		return nil, fmt.Errorf("api key request failed: %w", err)
	}
	defer func() { _ = apiKeyResp.Body.Close() }()

	apiKeyBody, err := io.ReadAll(apiKeyResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read api key response: %w", err)
	}

	if apiKeyResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api key endpoint error (status %d): %s", apiKeyResp.StatusCode, truncateOAuth(string(apiKeyBody), 200))
	}

	var apiKeyResult map[string]any
	if err := json.Unmarshal(apiKeyBody, &apiKeyResult); err != nil {
		return nil, fmt.Errorf("parse api key response: %w", err)
	}

	rawKey, _ := apiKeyResult["raw_key"].(string)
	if rawKey == "" {
		return nil, fmt.Errorf("no raw_key in api key response")
	}

	return map[string]any{
		"success": true,
		"api_key": rawKey,
	}, nil
}

func truncateOAuth(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func newOAuthExchangeFactory() plugin.StepFactory {
	return func(name string, _ map[string]any, app modular.Application) (any, error) {
		return &OAuthExchangeStep{
			name: name,
			app:  app,
		}, nil
	}
}
