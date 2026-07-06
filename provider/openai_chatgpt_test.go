package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestAllAuthModesIncludesOpenAIChatGPT(t *testing.T) {
	for _, mode := range AllAuthModes() {
		if mode.Mode != "chatgpt" {
			continue
		}
		if mode.ServerSafe {
			t.Fatal("chatgpt auth mode must not be marked server-safe")
		}
		if mode.DocsURL == "" {
			t.Fatal("chatgpt auth mode should link official docs")
		}
		return
	}
	t.Fatal("missing chatgpt auth mode")
}

func TestListModelsOpenAIChatGPTFetchesLiveCodexCatalog(t *testing.T) {
	var sawURL, sawAuth, sawAccount string
	origClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawURL = r.URL.String()
		sawAuth = r.Header.Get("Authorization")
		sawAccount = r.Header.Get("ChatGPT-Account-ID")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(bytes.NewBufferString(`{
			"models": [
				{"slug": "gpt-5.5", "display_name": "GPT-5.5", "visibility": "list"},
				{"slug": "hidden-review", "display_name": "Hidden Review", "visibility": "hide"},
				{"id": "gpt-5.4-mini", "display_name": "GPT-5.4-Mini", "show_in_picker": true},
				{"slug": "not-for-picker", "display_name": "Not For Picker", "show_in_picker": false}
			]
		}`)),
		}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = origClient })

	tokenBundle := openAIChatGPTTokenBundleJSON(t, "access-token", "refresh-token", "acct_123")
	models, err := ListModels(t.Context(), "openai_chatgpt", tokenBundle, "")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if sawURL != "https://chatgpt.com/backend-api/codex/models?client_version=1.0.0" {
		t.Fatalf("URL = %q", sawURL)
	}
	if sawAuth != "Bearer access-token" {
		t.Fatalf("Authorization = %q", sawAuth)
	}
	if sawAccount != "acct_123" {
		t.Fatalf("ChatGPT-Account-ID = %q", sawAccount)
	}
	if len(models) != 2 || models[0].ID != "gpt-5.5" || models[1].ID != "gpt-5.4-mini" {
		t.Fatalf("models = %+v", models)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestListModelsOpenAIChatGPTRejectsNonHTTPSBaseURL(t *testing.T) {
	_, err := ListModels(t.Context(), "openai_chatgpt", openAIChatGPTTokenBundleJSON(t, "access-token", "refresh-token", "acct_123"), "http://example.com")
	if err == nil {
		t.Fatal("expected non-HTTPS base URL error")
	}
}

func TestListModelsOpenAIChatGPTRequiresAccessToken(t *testing.T) {
	_, err := ListModels(t.Context(), "openai_chatgpt", `{"refresh_token":"refresh-token"}`, "")
	if err == nil {
		t.Fatal("expected missing access token error")
	}
}

func TestListModelsOpenAIChatGPTAcceptsNestedCodexAuthJSON(t *testing.T) {
	var sawAuth string
	origClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"models":[{"slug":"gpt-5.5"}]}`)),
		}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = origClient })

	models, err := ListModels(t.Context(), "openai_chatgpt", `{"tokens":{"access_token":"nested-token","refresh_token":"refresh-token"}}`, "")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if sawAuth != "Bearer nested-token" {
		t.Fatalf("Authorization = %q", sawAuth)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.5" {
		t.Fatalf("models = %+v", models)
	}
}

func openAIChatGPTTokenBundleJSON(t *testing.T, access, refresh, account string) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
		ExpiresAt    int64  `json:"expires_at"`
	}{
		AccessToken:  access,
		RefreshToken: refresh,
		AccountID:    account,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("marshal token bundle: %v", err)
	}
	return string(raw)
}
