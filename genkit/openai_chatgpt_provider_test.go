package genkit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

func TestParseOpenAIChatGPTTokenBundleAcceptsCodexAuthJSON(t *testing.T) {
	idToken := unsignedJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_123",
			"chatgpt_user_id":    "user_123",
			"chatgpt_plan_type":  "plus",
		},
	})
	raw := fmt.Sprintf(`{
		"auth_mode": "chatgpt",
		"tokens": {
			"id_token": %q,
			"access_token": %q,
			"refresh_token": %q,
			"account_id": %q
		}
	}`, idToken, unsignedJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), "refresh-token", "acct_from_file")

	got, err := ParseOpenAIChatGPTTokenBundle(raw)
	if err != nil {
		t.Fatalf("ParseOpenAIChatGPTTokenBundle: %v", err)
	}
	if got.AccessToken == "" || got.RefreshToken != "refresh-token" {
		t.Fatalf("unexpected token bundle: %+v", got)
	}
	if got.AccountID != "acct_from_file" {
		t.Fatalf("AccountID = %q, want acct_from_file", got.AccountID)
	}
	if got.ChatGPTAccountID != "acct_123" {
		t.Fatalf("ChatGPTAccountID = %q, want acct_123", got.ChatGPTAccountID)
	}
	if got.PlanType != "plus" {
		t.Fatalf("PlanType = %q, want plus", got.PlanType)
	}
}

func TestOpenAIChatGPTProviderChatUsesBearerAndAccountHeaders(t *testing.T) {
	var sawAuth, sawAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		sawAccount = r.Header.Get("chatgpt-account-id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output_text": "hello from chatgpt",
			"usage": {"input_tokens": 7, "output_tokens": 3}
		}`))
	}))
	defer server.Close()

	p, err := newOpenAIChatGPTProviderWithClient(http.DefaultClient, tokenBundleJSON(t, "access-token", "refresh-token", "acct_123", time.Now().Add(time.Hour)), "gpt-5-codex", server.URL+"/backend-api/codex", server.URL+"/oauth/token", 512)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	resp, err := p.Chat(context.Background(), []provider.Message{{Role: provider.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hello from chatgpt" {
		t.Fatalf("Content = %q", resp.Content)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("Usage = %+v", resp.Usage)
	}
	if sawAuth != "Bearer access-token" {
		t.Fatalf("Authorization = %q", sawAuth)
	}
	if sawAccount != "acct_123" {
		t.Fatalf("chatgpt-account-id = %q", sawAccount)
	}
}

func TestOpenAIChatGPTProviderRefreshesExpiredAccessToken(t *testing.T) {
	var responseAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body: %v", err)
			}
			if body["grant_type"] != "refresh_token" || body["refresh_token"] != "refresh-old" {
				t.Fatalf("unexpected refresh body: %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"access_token": %q,
				"refresh_token": "refresh-new",
				"id_token": %q
			}`, unsignedJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), unsignedJWT(t, map[string]any{
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_refreshed"},
			}))))
		case "/backend-api/codex/responses":
			responseAuth = r.Header.Get("Authorization")
			if r.Header.Get("chatgpt-account-id") != "acct_refreshed" {
				t.Fatalf("chatgpt-account-id = %q", r.Header.Get("chatgpt-account-id"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output_text":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	p, err := newOpenAIChatGPTProviderWithClient(http.DefaultClient, tokenBundleJSON(t, unsignedJWT(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}), "refresh-old", "", time.Now().Add(-time.Hour)), "gpt-5-codex", server.URL+"/backend-api/codex", server.URL+"/oauth/token", 0)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	if _, err := p.Chat(context.Background(), []provider.Message{{Role: provider.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.HasPrefix(responseAuth, "Bearer ") || strings.Contains(responseAuth, "refresh-old") {
		t.Fatalf("Authorization = %q", responseAuth)
	}
}

func TestOpenAIChatGPTProviderStreamParsesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"))
	}))
	defer server.Close()

	p, err := newOpenAIChatGPTProviderWithClient(http.DefaultClient, tokenBundleJSON(t, "access-token", "refresh-token", "acct_123", time.Now().Add(time.Hour)), "gpt-5-codex", server.URL+"/backend-api/codex", server.URL+"/oauth/token", 0)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	ch, err := p.Stream(context.Background(), []provider.Message{{Role: provider.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var text string
	var done *provider.Usage
	for ev := range ch {
		switch ev.Type {
		case "text":
			text += ev.Text
		case "done":
			done = ev.Usage
		case "error":
			t.Fatalf("stream error: %s", ev.Error)
		}
	}
	if text != "hello" {
		t.Fatalf("stream text = %q", text)
	}
	if done == nil || done.InputTokens != 2 || done.OutputTokens != 1 {
		t.Fatalf("done usage = %+v", done)
	}
}

func tokenBundleJSON(t *testing.T, access, refresh, account string, expires time.Time) string {
	t.Helper()
	raw, err := json.Marshal(OpenAIChatGPTTokenBundle{
		AccessToken:  access,
		RefreshToken: refresh,
		AccountID:    account,
		ExpiresAt:    expires.Unix(),
	})
	if err != nil {
		t.Fatalf("marshal token bundle: %v", err)
	}
	return string(raw)
}

func unsignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}
