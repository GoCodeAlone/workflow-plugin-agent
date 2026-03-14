package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// setupCopilotServers creates two test servers: one for token exchange and one for chat.
// Returns (tokenSrv, chatSrv, cleanup).
func setupCopilotServers(t *testing.T, tokenHandler, chatHandler http.HandlerFunc) (*httptest.Server, *httptest.Server) {
	t.Helper()
	tokenSrv := httptest.NewServer(tokenHandler)
	chatSrv := httptest.NewServer(chatHandler)
	t.Cleanup(func() {
		tokenSrv.Close()
		chatSrv.Close()
	})
	return tokenSrv, chatSrv
}

func validTokenHandler(expiresAt int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(copilotTokenResponse{
			Token:     "copilot-bearer-token",
			ExpiresAt: expiresAt,
		})
	}
}

func validChatHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer copilot-bearer-token" {
			t.Errorf("chat Authorization = %q, want Bearer copilot-bearer-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-cop","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello from copilot"},"finish_reason":"stop","logprobs":null}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8},"created":1704067200}`)
	}
}

func TestCopilotProvider_Name(t *testing.T) {
	p := NewCopilotProvider(CopilotConfig{Token: "ghp_test"})
	if got := p.Name(); got != "copilot" {
		t.Errorf("Name() = %q, want %q", got, "copilot")
	}
}

func TestCopilotProvider_AuthModeInfo(t *testing.T) {
	p := NewCopilotProvider(CopilotConfig{Token: "ghp_test"})
	info := p.AuthModeInfo()
	if info.Mode != "personal" {
		t.Errorf("AuthModeInfo().Mode = %q, want %q", info.Mode, "personal")
	}
	if info.ServerSafe {
		t.Error("AuthModeInfo().ServerSafe = true, want false")
	}
}

func TestCopilotProvider_ImplementsProvider(t *testing.T) {
	var _ Provider = (*CopilotProvider)(nil)
}

func TestCopilotProvider_Chat_HappyPath(t *testing.T) {
	expiresAt := time.Now().Add(30 * time.Minute).Unix()
	tokenSrv, chatSrv := setupCopilotServers(t, validTokenHandler(expiresAt), validChatHandler(t))

	p := NewCopilotProvider(CopilotConfig{
		Token:   "ghp_oauth_token",
		Model:   "gpt-4o",
		BaseURL: chatSrv.URL,
	})
	// Override the token exchange URL by patching the provider's internal call via an http.Client
	// that redirects token exchange requests to our test server.
	p.config.HTTPClient = redirectingClient(tokenSrv.URL)

	got, err := p.Chat(t.Context(), []Message{
		{Role: RoleUser, Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if got.Content != "hello from copilot" {
		t.Errorf("Chat() content = %q, want %q", got.Content, "hello from copilot")
	}
	if got.Usage.InputTokens != 5 || got.Usage.OutputTokens != 3 {
		t.Errorf("Chat() usage = %+v, want input=5 output=3", got.Usage)
	}
}

func TestCopilotProvider_TokenExchange_Error(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer tokenSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("chat server should not be called when token exchange fails")
	}))
	defer chatSrv.Close()

	p := NewCopilotProvider(CopilotConfig{
		Token:      "bad_token",
		Model:      "gpt-4o",
		BaseURL:    chatSrv.URL,
		HTTPClient: redirectingClient(tokenSrv.URL),
	})

	_, err := p.Chat(t.Context(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error when token exchange returns 401")
	}
}

func TestCopilotProvider_TokenCacheHit(t *testing.T) {
	var tokenCallCount atomic.Int32
	expiresAt := time.Now().Add(30 * time.Minute).Unix()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(copilotTokenResponse{
			Token:     "cached-bearer-token",
			ExpiresAt: expiresAt,
		})
	}))
	defer tokenSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop","logprobs":null}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"created":1704067200}`)
	}))
	defer chatSrv.Close()

	p := NewCopilotProvider(CopilotConfig{
		Token:      "ghp_test",
		Model:      "gpt-4o",
		BaseURL:    chatSrv.URL,
		HTTPClient: redirectingClient(tokenSrv.URL),
	})

	// Two sequential calls — token exchange should only happen once.
	for range 2 {
		if _, err := p.Chat(t.Context(), []Message{{Role: RoleUser, Content: "hi"}}, nil); err != nil {
			t.Fatalf("Chat() error: %v", err)
		}
	}

	if n := tokenCallCount.Load(); n != 1 {
		t.Errorf("token exchange called %d times, want 1 (cache should be hit on second call)", n)
	}
}

func TestCopilotProvider_TokenRefresh_AfterExpiry(t *testing.T) {
	var tokenCallCount atomic.Int32
	// Already-expired token.
	pastExpiry := time.Now().Add(-5 * time.Minute).Unix()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Return a fresh token each time.
		json.NewEncoder(w).Encode(copilotTokenResponse{
			Token:     fmt.Sprintf("token-%d", tokenCallCount.Load()),
			ExpiresAt: time.Now().Add(30 * time.Minute).Unix(),
		})
	}))
	defer tokenSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop","logprobs":null}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"created":1704067200}`)
	}))
	defer chatSrv.Close()

	p := NewCopilotProvider(CopilotConfig{
		Token:      "ghp_test",
		Model:      "gpt-4o",
		BaseURL:    chatSrv.URL,
		HTTPClient: redirectingClient(tokenSrv.URL),
	})

	// Manually set an expired bearer token.
	p.bearerToken = "expired-token"
	p.expiresAt = time.Unix(pastExpiry, 0)

	if _, err := p.Chat(t.Context(), []Message{{Role: RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	if n := tokenCallCount.Load(); n != 1 {
		t.Errorf("token exchange called %d times, want 1 (should refresh expired token)", n)
	}
}

func TestCopilotProvider_Stream(t *testing.T) {
	expiresAt := time.Now().Add(30 * time.Minute).Unix()

	tokenSrv := httptest.NewServer(validTokenHandler(expiresAt))
	defer tokenSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		data, _ := json.Marshal(map[string]any{
			"id":      "chatcmpl-cop-stream",
			"object":  "chat.completion.chunk",
			"model":   "gpt-4o",
			"created": 1704067200,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{"role": "assistant", "content": "copilot-streamed"}, "finish_reason": nil},
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", string(data))
		flusher.Flush()

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer chatSrv.Close()

	p := NewCopilotProvider(CopilotConfig{
		Token:      "ghp_test",
		Model:      "gpt-4o",
		BaseURL:    chatSrv.URL,
		HTTPClient: redirectingClient(tokenSrv.URL),
	})

	ch, err := p.Stream(t.Context(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var texts []string
	for ev := range ch {
		switch ev.Type {
		case "text":
			texts = append(texts, ev.Text)
		case "error":
			t.Fatalf("stream error: %s", ev.Error)
		}
	}
	if len(texts) == 0 {
		t.Fatal("expected at least one text event")
	}
	if texts[0] != "copilot-streamed" {
		t.Errorf("first text event = %q, want %q", texts[0], "copilot-streamed")
	}
}

// redirectingClient returns an http.Client whose transport redirects all requests
// with the copilotTokenExchangeURL path to tokenBaseURL, leaving others unchanged.
func redirectingClient(tokenBaseURL string) *http.Client {
	return &http.Client{
		Transport: &tokenRedirectTransport{
			tokenBaseURL: tokenBaseURL,
			inner:        http.DefaultTransport,
		},
	}
}

type tokenRedirectTransport struct {
	tokenBaseURL string
	inner        http.RoundTripper
}

func (t *tokenRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Redirect token exchange requests to our test server.
	if req.URL.String() == copilotTokenExchangeURL {
		redirected := req.Clone(req.Context())
		redirected.URL.Scheme = "http"
		redirected.URL.Host = req.URL.Host
		// Parse tokenBaseURL and override host.
		srv := t.tokenBaseURL
		// Strip scheme.
		if len(srv) > 7 && srv[:7] == "http://" {
			srv = srv[7:]
		}
		redirected.URL.Host = srv
		redirected.URL.Path = req.URL.Path
		redirected.Host = srv
		return t.inner.RoundTrip(redirected)
	}
	return t.inner.RoundTrip(req)
}
