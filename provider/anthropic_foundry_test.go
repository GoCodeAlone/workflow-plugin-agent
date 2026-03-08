package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewAnthropicFoundryProvider_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     AnthropicFoundryConfig
		wantErr string
	}{
		{
			name:    "missing resource",
			cfg:     AnthropicFoundryConfig{APIKey: "key"},
			wantErr: "resource name is required",
		},
		{
			name:    "missing auth",
			cfg:     AnthropicFoundryConfig{Resource: "myresource"},
			wantErr: "either APIKey or EntraToken is required",
		},
		{
			name: "valid with api key",
			cfg:  AnthropicFoundryConfig{Resource: "myresource", APIKey: "key"},
		},
		{
			name: "valid with entra token",
			cfg:  AnthropicFoundryConfig{Resource: "myresource", EntraToken: "token"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewAnthropicFoundryProvider(tt.cfg)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if got := err.Error(); !contains(got, tt.wantErr) {
					t.Fatalf("error %q does not contain %q", got, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p == nil {
				t.Fatal("expected non-nil provider")
			}
		})
	}
}

func TestAnthropicFoundryProvider_Defaults(t *testing.T) {
	p, err := NewAnthropicFoundryProvider(AnthropicFoundryConfig{
		Resource: "myresource",
		APIKey:   "key",
	})
	if err != nil {
		t.Fatal(err)
	}

	if p.config.Model != defaultFoundryModel {
		t.Errorf("default model = %q, want %q", p.config.Model, defaultFoundryModel)
	}
	if p.config.MaxTokens != defaultAnthropicMaxTokens {
		t.Errorf("default max tokens = %d, want %d", p.config.MaxTokens, defaultAnthropicMaxTokens)
	}
}

func TestAnthropicFoundryProvider_Name(t *testing.T) {
	p := &anthropicFoundryProvider{}
	if got := p.Name(); got != "anthropic_foundry" {
		t.Errorf("Name() = %q, want %q", got, "anthropic_foundry")
	}
}

func TestAnthropicFoundryProvider_AuthModeInfo(t *testing.T) {
	p := &anthropicFoundryProvider{}
	info := p.AuthModeInfo()
	if info.Mode != "foundry" {
		t.Errorf("Mode = %q, want %q", info.Mode, "foundry")
	}
	if info.DisplayName != "Anthropic (Azure AI Foundry)" {
		t.Errorf("DisplayName = %q", info.DisplayName)
	}
}

func TestAnthropicFoundryProvider_URLConstruction(t *testing.T) {
	p, err := NewAnthropicFoundryProvider(AnthropicFoundryConfig{
		Resource: "my-ai-resource",
		APIKey:   "key",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "https://my-ai-resource.services.ai.azure.com/anthropic/v1/messages"
	if p.url != want {
		t.Errorf("url = %q, want %q", p.url, want)
	}
}

func TestAnthropicFoundryProvider_APIKeyAuth(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		json.NewEncoder(w).Encode(anthropicResponse{
			ID:   "msg_123",
			Type: "message",
			Content: []anthropicRespItem{
				{Type: "text", Text: "hello"},
			},
			Usage: anthropicUsage{InputTokens: 10, OutputTokens: 5},
		})
	}))
	defer srv.Close()

	p := &anthropicFoundryProvider{
		config: AnthropicFoundryConfig{
			Resource:   "test",
			Model:      "claude-sonnet-4-20250514",
			MaxTokens:  1024,
			APIKey:     "my-azure-key",
			HTTPClient: srv.Client(),
		},
		url: srv.URL,
	}

	_, err := p.Chat(context.Background(), []Message{
		{Role: RoleUser, Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := gotHeaders.Get("api-key"); got != "my-azure-key" {
		t.Errorf("api-key header = %q, want %q", got, "my-azure-key")
	}
	if got := gotHeaders.Get("anthropic-version"); got != anthropicAPIVersion {
		t.Errorf("anthropic-version header = %q, want %q", got, anthropicAPIVersion)
	}
	if got := gotHeaders.Get("Authorization"); got != "" {
		t.Errorf("Authorization header should be empty with API key auth, got %q", got)
	}
}

func TestAnthropicFoundryProvider_EntraIDAuth(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		json.NewEncoder(w).Encode(anthropicResponse{
			ID:      "msg_123",
			Type:    "message",
			Content: []anthropicRespItem{{Type: "text", Text: "hello"}},
			Usage:   anthropicUsage{InputTokens: 10, OutputTokens: 5},
		})
	}))
	defer srv.Close()

	p := &anthropicFoundryProvider{
		config: AnthropicFoundryConfig{
			Resource:   "test",
			Model:      "claude-sonnet-4-20250514",
			MaxTokens:  1024,
			EntraToken: "my-entra-token",
			HTTPClient: srv.Client(),
		},
		url: srv.URL,
	}

	_, err := p.Chat(context.Background(), []Message{
		{Role: RoleUser, Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := gotHeaders.Get("Authorization"); got != "Bearer my-entra-token" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer my-entra-token")
	}
	if got := gotHeaders.Get("api-key"); got != "" {
		t.Errorf("api-key header should be empty with Entra auth, got %q", got)
	}
}

func TestAnthropicFoundryProvider_Chat(t *testing.T) {
	var gotBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(anthropicResponse{
			ID:   "msg_123",
			Type: "message",
			Content: []anthropicRespItem{
				{Type: "text", Text: "Hello from Foundry!"},
			},
			Usage: anthropicUsage{InputTokens: 15, OutputTokens: 8},
		})
	}))
	defer srv.Close()

	p := &anthropicFoundryProvider{
		config: AnthropicFoundryConfig{
			Resource:   "test",
			Model:      "claude-sonnet-4-20250514",
			MaxTokens:  1024,
			APIKey:     "key",
			HTTPClient: srv.Client(),
		},
		url: srv.URL,
	}

	resp, err := p.Chat(context.Background(), []Message{
		{Role: RoleSystem, Content: "You are helpful."},
		{Role: RoleUser, Content: "Hi"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Content != "Hello from Foundry!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello from Foundry!")
	}
	if resp.Usage.InputTokens != 15 {
		t.Errorf("InputTokens = %d, want 15", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 8 {
		t.Errorf("OutputTokens = %d, want 8", resp.Usage.OutputTokens)
	}

	// Verify request body matches Anthropic Messages format
	if gotBody.Model != "claude-sonnet-4-20250514" {
		t.Errorf("request model = %q", gotBody.Model)
	}
	if gotBody.System != "You are helpful." {
		t.Errorf("request system = %q", gotBody.System)
	}
	if len(gotBody.Messages) != 1 {
		t.Fatalf("request messages len = %d, want 1", len(gotBody.Messages))
	}
}

func TestAnthropicFoundryProvider_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`,
			`{"type":"message_stop"}`,
		}

		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	p := &anthropicFoundryProvider{
		config: AnthropicFoundryConfig{
			Resource:   "test",
			Model:      "claude-sonnet-4-20250514",
			MaxTokens:  1024,
			APIKey:     "key",
			HTTPClient: srv.Client(),
		},
		url: srv.URL,
	}

	ch, err := p.Stream(context.Background(), []Message{
		{Role: RoleUser, Content: "Hi"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var texts []string
	var done bool
	for ev := range ch {
		switch ev.Type {
		case "text":
			texts = append(texts, ev.Text)
		case "done":
			done = true
			if ev.Usage == nil {
				t.Error("expected usage in done event")
			} else if ev.Usage.OutputTokens != 10 {
				t.Errorf("OutputTokens = %d, want 10", ev.Usage.OutputTokens)
			}
		}
	}

	if !done {
		t.Error("expected done event")
	}
	if got := join(texts); got != "Hello world" {
		t.Errorf("streamed text = %q, want %q", got, "Hello world")
	}
}

// helpers

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func join(parts []string) string {
	result := ""
	for _, p := range parts {
		result += p
	}
	return result
}
