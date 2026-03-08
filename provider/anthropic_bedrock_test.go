package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		AccessKeyID:    "AKID",
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

func TestAnthropicBedrockProvider_URLConstruction(t *testing.T) {
	p := &anthropicBedrockProvider{
		config: AnthropicBedrockConfig{
			Region:  "us-west-2",
			Model:   "anthropic.claude-sonnet-4-20250514-v1:0",
			BaseURL: "https://bedrock-runtime.us-west-2.amazonaws.com",
		},
	}

	wantInvoke := "https://bedrock-runtime.us-west-2.amazonaws.com/model/anthropic.claude-sonnet-4-20250514-v1:0/invoke"
	if got := p.invokeURL(); got != wantInvoke {
		t.Errorf("invokeURL() = %q, want %q", got, wantInvoke)
	}

	wantStream := "https://bedrock-runtime.us-west-2.amazonaws.com/model/anthropic.claude-sonnet-4-20250514-v1:0/invoke-with-response-stream"
	if got := p.streamURL(); got != wantStream {
		t.Errorf("streamURL() = %q, want %q", got, wantStream)
	}
}

func TestSigv4Sign_AuthorizationHeader(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://bedrock-runtime.us-east-1.amazonaws.com/model/test/invoke", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	sigv4Sign(req, []byte("{}"), "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "", "us-east-1", "bedrock", now)

	auth := req.Header.Get("Authorization")
	if auth == "" {
		t.Fatal("expected Authorization header")
	}
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization header should start with AWS4-HMAC-SHA256, got %q", auth)
	}
	if !strings.Contains(auth, "Credential=AKIDEXAMPLE/20250615/us-east-1/bedrock/aws4_request") {
		t.Errorf("Authorization header missing expected credential scope, got %q", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=") {
		t.Errorf("Authorization header missing SignedHeaders, got %q", auth)
	}
	if !strings.Contains(auth, "Signature=") {
		t.Errorf("Authorization header missing Signature, got %q", auth)
	}

	// Verify X-Amz-Date is set
	if got := req.Header.Get("X-Amz-Date"); got != "20250615T120000Z" {
		t.Errorf("X-Amz-Date = %q, want %q", got, "20250615T120000Z")
	}
}

func TestSigv4Sign_WithSessionToken(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://bedrock-runtime.us-east-1.amazonaws.com/model/test/invoke", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	sigv4Sign(req, []byte("{}"), "AKID", "secret", "session-token-123", "us-east-1", "bedrock", now)

	if got := req.Header.Get("X-Amz-Security-Token"); got != "session-token-123" {
		t.Errorf("X-Amz-Security-Token = %q, want %q", got, "session-token-123")
	}

	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "x-amz-security-token") {
		t.Errorf("signed headers should include x-amz-security-token, got %q", auth)
	}
}

func TestAnthropicBedrockProvider_Chat(t *testing.T) {
	var gotBody anthropicRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(anthropicResponse{
			ID:   "msg_123",
			Type: "message",
			Content: []anthropicRespItem{
				{Type: "text", Text: "Hello from Bedrock!"},
			},
			Usage: anthropicUsage{InputTokens: 15, OutputTokens: 8},
		})
	}))
	defer srv.Close()

	p := &anthropicBedrockProvider{
		config: AnthropicBedrockConfig{
			Region:         "us-east-1",
			Model:          "anthropic.claude-sonnet-4-20250514-v1:0",
			MaxTokens:      1024,
			AccessKeyID:    "AKIDTEST",
			SecretAccessKey: "secret",
			HTTPClient:     srv.Client(),
			BaseURL:        srv.URL,
		},
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

	// Verify request body matches Anthropic Messages format
	if gotBody.Model != "anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Errorf("request model = %q", gotBody.Model)
	}
	if gotBody.System != "You are helpful." {
		t.Errorf("request system = %q", gotBody.System)
	}
	if len(gotBody.Messages) != 1 {
		t.Fatalf("request messages len = %d, want 1", len(gotBody.Messages))
	}
}

func TestAnthropicBedrockProvider_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Bedrock"}}`,
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

	p := &anthropicBedrockProvider{
		config: AnthropicBedrockConfig{
			Region:         "us-east-1",
			Model:          "anthropic.claude-sonnet-4-20250514-v1:0",
			MaxTokens:      1024,
			AccessKeyID:    "AKIDTEST",
			SecretAccessKey: "secret",
			HTTPClient:     srv.Client(),
			BaseURL:        srv.URL,
		},
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
