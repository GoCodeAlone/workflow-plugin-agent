package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenRouterProvider_Name(t *testing.T) {
	p := NewOpenRouterProvider(OpenRouterConfig{APIKey: "test-key"})
	if got := p.Name(); got != "openrouter" {
		t.Errorf("Name() = %q, want %q", got, "openrouter")
	}
}

func TestOpenRouterProvider_AuthModeInfo(t *testing.T) {
	p := NewOpenRouterProvider(OpenRouterConfig{APIKey: "test-key"})
	info := p.AuthModeInfo()

	if info.Mode != "openrouter" {
		t.Errorf("AuthModeInfo().Mode = %q, want %q", info.Mode, "openrouter")
	}
	if info.DisplayName != "OpenRouter" {
		t.Errorf("AuthModeInfo().DisplayName = %q, want %q", info.DisplayName, "OpenRouter")
	}
	if info.DocsURL != "https://openrouter.ai/docs/api/reference/authentication" {
		t.Errorf("AuthModeInfo().DocsURL = %q, want openrouter docs URL", info.DocsURL)
	}
	if !info.ServerSafe {
		t.Error("AuthModeInfo().ServerSafe = false, want true")
	}
}

func TestOpenRouterProvider_ImplementsProvider(t *testing.T) {
	var _ Provider = (*OpenRouterProvider)(nil)
}

func TestOpenRouterProvider_DefaultBaseURL(t *testing.T) {
	p := NewOpenRouterProvider(OpenRouterConfig{APIKey: "test-key"})
	if p.config.BaseURL != defaultOpenRouterBaseURL {
		t.Errorf("default BaseURL = %q, want %q", p.config.BaseURL, defaultOpenRouterBaseURL)
	}
}

func TestOpenRouterProvider_CustomBaseURL(t *testing.T) {
	custom := "https://custom.openrouter.ai/api/v1"
	p := NewOpenRouterProvider(OpenRouterConfig{APIKey: "test-key", BaseURL: custom})
	if p.config.BaseURL != custom {
		t.Errorf("BaseURL = %q, want %q", p.config.BaseURL, custom)
	}
}

func TestOpenRouterProvider_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer test-key")
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-test","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello from openrouter"},"finish_reason":"stop","logprobs":null}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},"created":1704067200}`)
	}))
	defer srv.Close()

	p := NewOpenRouterProvider(OpenRouterConfig{
		APIKey:  "test-key",
		Model:   "meta-llama/llama-3-70b",
		BaseURL: srv.URL,
	})

	got, err := p.Chat(t.Context(), []Message{
		{Role: RoleUser, Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if got.Content != "hello from openrouter" {
		t.Errorf("Chat() content = %q, want %q", got.Content, "hello from openrouter")
	}
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 5 {
		t.Errorf("Chat() usage = %+v, want input=10 output=5", got.Usage)
	}
}

func TestOpenRouterProvider_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		data, _ := json.Marshal(map[string]any{
			"id":      "chatcmpl-stream",
			"object":  "chat.completion.chunk",
			"model":   "gpt-4o",
			"created": 1704067200,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{"role": "assistant", "content": "streamed"}, "finish_reason": nil},
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", string(data))
		flusher.Flush()

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewOpenRouterProvider(OpenRouterConfig{
		APIKey:  "test-key",
		Model:   "meta-llama/llama-3-70b",
		BaseURL: srv.URL,
	})

	ch, err := p.Stream(t.Context(), []Message{
		{Role: RoleUser, Content: "hi"},
	}, nil)
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
	if texts[0] != "streamed" {
		t.Errorf("first text event = %q, want %q", texts[0], "streamed")
	}
}
