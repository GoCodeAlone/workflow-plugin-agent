package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewAnthropicBedrockProvider_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     AnthropicBedrockConfig
		wantErr string
	}{
		{
			name:    "missing access key",
			cfg:     AnthropicBedrockConfig{SecretAccessKey: "secret"},
			wantErr: "access key ID is required",
		},
		{
			name:    "missing secret key",
			cfg:     AnthropicBedrockConfig{AccessKeyID: "AKID"},
			wantErr: "secret access key is required",
		},
		{
			name: "valid",
			cfg:  AnthropicBedrockConfig{AccessKeyID: "AKID", SecretAccessKey: "secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewAnthropicBedrockProvider(tt.cfg)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if got := err.Error(); !strings.Contains(got, tt.wantErr) {
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

func TestAnthropicBedrockProvider_Defaults(t *testing.T) {
	p, err := NewAnthropicBedrockProvider(AnthropicBedrockConfig{
		AccessKeyID:     "AKID",
		SecretAccessKey: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	if p.config.Model != defaultBedrockModel {
		t.Errorf("default model = %q, want %q", p.config.Model, defaultBedrockModel)
	}
	if p.config.Region != defaultBedrockRegion {
		t.Errorf("default region = %q, want %q", p.config.Region, defaultBedrockRegion)
	}
	if p.config.MaxTokens != defaultAnthropicMaxTokens {
		t.Errorf("default max tokens = %d, want %d", p.config.MaxTokens, defaultAnthropicMaxTokens)
	}
}

func TestAnthropicBedrockProvider_Name(t *testing.T) {
	p := &anthropicBedrockProvider{}
	if got := p.Name(); got != "anthropic_bedrock" {
		t.Errorf("Name() = %q, want %q", got, "anthropic_bedrock")
	}
}

func TestAnthropicBedrockProvider_AuthModeInfo(t *testing.T) {
	p := &anthropicBedrockProvider{}
	info := p.AuthModeInfo()
	if info.Mode != "bedrock" {
		t.Errorf("Mode = %q, want %q", info.Mode, "bedrock")
	}
	if info.DisplayName != "Anthropic (Amazon Bedrock)" {
		t.Errorf("DisplayName = %q", info.DisplayName)
	}
}

func TestAnthropicBedrockProvider_Chat(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":   "msg_123",
			"type": "message",
			"content": []any{
				map[string]any{"type": "text", "text": "Hello from Bedrock!"},
			},
			"usage": map[string]any{"input_tokens": 15, "output_tokens": 8},
		})
	}))
	defer srv.Close()

	p, err := NewAnthropicBedrockProvider(AnthropicBedrockConfig{
		Region:          "us-east-1",
		Model:           "anthropic.claude-sonnet-4-20250514-v1:0",
		MaxTokens:       1024,
		AccessKeyID:     "AKIDTEST",
		SecretAccessKey: "secret",
		HTTPClient:      srv.Client(),
		BaseURL:         srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Chat(t.Context(), []Message{
		{Role: RoleSystem, Content: "You are helpful."},
		{Role: RoleUser, Content: "Hi"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Content != "Hello from Bedrock!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello from Bedrock!")
	}
	if resp.Usage.InputTokens != 15 {
		t.Errorf("InputTokens = %d, want 15", resp.Usage.InputTokens)
	}

	// Verify SigV4 Authorization header is present
	if gotAuth == "" {
		t.Error("expected Authorization header")
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization should be SigV4, got %q", gotAuth)
	}

	// Bedrock middleware moves model to URL path
	if !strings.Contains(gotPath, "anthropic.claude-sonnet-4-20250514-v1:0") {
		t.Errorf("URL path should contain model, got %q", gotPath)
	}
}

func TestAnthropicBedrockProvider_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		events := []struct{ typ, data string }{
			{"message_start", `{"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":0}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Bedrock"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`},
			{"message_stop", `{"type":"message_stop"}`},
		}

		for _, e := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.typ, e.data)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	p, err := NewAnthropicBedrockProvider(AnthropicBedrockConfig{
		Region:          "us-east-1",
		Model:           "anthropic.claude-sonnet-4-20250514-v1:0",
		MaxTokens:       1024,
		AccessKeyID:     "AKIDTEST",
		SecretAccessKey: "secret",
		HTTPClient:      srv.Client(),
		BaseURL:         srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	ch, err := p.Stream(t.Context(), []Message{
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
	if got := strings.Join(texts, ""); got != "Hello Bedrock" {
		t.Errorf("streamed text = %q, want %q", got, "Hello Bedrock")
	}
}
