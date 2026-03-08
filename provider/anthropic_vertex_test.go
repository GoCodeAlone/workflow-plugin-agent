package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

// mockTokenSource returns a fixed token for testing.
type mockTokenSource struct {
	token string
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: m.token}, nil
}

func TestNewAnthropicVertexProvider_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     AnthropicVertexConfig
		wantErr string
	}{
		{
			name:    "missing project ID",
			cfg:     AnthropicVertexConfig{TokenSource: &mockTokenSource{token: "tok"}},
			wantErr: "project ID is required",
		},
		{
			name: "valid with token source",
			cfg: AnthropicVertexConfig{
				ProjectID:   "my-project",
				TokenSource: &mockTokenSource{token: "tok"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewAnthropicVertexProvider(tt.cfg)
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

func TestAnthropicVertexProvider_Defaults(t *testing.T) {
	p, err := NewAnthropicVertexProvider(AnthropicVertexConfig{
		ProjectID:   "my-project",
		TokenSource: &mockTokenSource{token: "tok"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if p.config.Model != defaultVertexModel {
		t.Errorf("default model = %q, want %q", p.config.Model, defaultVertexModel)
	}
	if p.config.Region != defaultVertexRegion {
		t.Errorf("default region = %q, want %q", p.config.Region, defaultVertexRegion)
	}
	if p.config.MaxTokens != defaultAnthropicMaxTokens {
		t.Errorf("default max tokens = %d, want %d", p.config.MaxTokens, defaultAnthropicMaxTokens)
	}
}

func TestAnthropicVertexProvider_Name(t *testing.T) {
	p := &anthropicVertexProvider{}
	if got := p.Name(); got != "anthropic_vertex" {
		t.Errorf("Name() = %q, want %q", got, "anthropic_vertex")
	}
}

func TestAnthropicVertexProvider_AuthModeInfo(t *testing.T) {
	p := &anthropicVertexProvider{}
	info := p.AuthModeInfo()
	if info.Mode != "vertex" {
		t.Errorf("Mode = %q, want %q", info.Mode, "vertex")
	}
	if info.DisplayName != "Anthropic (Google Vertex AI)" {
		t.Errorf("DisplayName = %q", info.DisplayName)
	}
}

func TestAnthropicVertexProvider_URLConstruction(t *testing.T) {
	p := &anthropicVertexProvider{
		config: AnthropicVertexConfig{
			ProjectID: "my-project",
			Region:    "us-east5",
			Model:     "claude-sonnet-4@20250514",
		},
	}

	wantChat := "https://us-east5-aiplatform.googleapis.com/v1/projects/my-project/locations/us-east5/publishers/anthropic/models/claude-sonnet-4@20250514:rawPredict"
	if got := p.vertexURL(false); got != wantChat {
		t.Errorf("vertexURL(false) = %q, want %q", got, wantChat)
	}

	wantStream := "https://us-east5-aiplatform.googleapis.com/v1/projects/my-project/locations/us-east5/publishers/anthropic/models/claude-sonnet-4@20250514:streamRawPredict"
	if got := p.vertexURL(true); got != wantStream {
		t.Errorf("vertexURL(true) = %q, want %q", got, wantStream)
	}
}

func TestAnthropicVertexProvider_BearerAuth(t *testing.T) {
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

	p := &anthropicVertexProvider{
		config: AnthropicVertexConfig{
			ProjectID:  "proj",
			Region:     "us-east5",
			Model:      "claude-sonnet-4@20250514",
			MaxTokens:  1024,
			HTTPClient: srv.Client(),
		},
		tokenSource: &mockTokenSource{token: "my-gcp-token"},
	}
	transport := &urlRewriteTransport{
		target:    srv.URL,
		transport: srv.Client().Transport,
	}
	p.config.HTTPClient = &http.Client{Transport: transport}

	_, err := p.Chat(t.Context(), []Message{
		{Role: RoleUser, Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := gotHeaders.Get("Authorization"); got != "Bearer my-gcp-token" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer my-gcp-token")
	}
	if got := gotHeaders.Get("anthropic-version"); got != anthropicAPIVersion {
		t.Errorf("anthropic-version header = %q, want %q", got, anthropicAPIVersion)
	}
	// Vertex should NOT set x-api-key
	if got := gotHeaders.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key should be empty, got %q", got)
	}
}

// urlRewriteTransport rewrites all request URLs to target for testing.
type urlRewriteTransport struct {
	target    string
	transport http.RoundTripper
}

func (t *urlRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = t.target[len("http://"):]
	if t.transport != nil {
		return t.transport.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestAnthropicVertexProvider_Chat(t *testing.T) {
	var gotBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(anthropicResponse{
			ID:   "msg_123",
			Type: "message",
			Content: []anthropicRespItem{
				{Type: "text", Text: "Hello from Vertex!"},
			},
			Usage: anthropicUsage{InputTokens: 15, OutputTokens: 8},
		})
	}))
	defer srv.Close()

	p := &anthropicVertexProvider{
		config: AnthropicVertexConfig{
			ProjectID:  "proj",
			Region:     "us-east5",
			Model:      "claude-sonnet-4@20250514",
			MaxTokens:  1024,
			HTTPClient: &http.Client{Transport: &urlRewriteTransport{target: srv.URL}},
		},
		tokenSource: &mockTokenSource{token: "tok"},
	}

	resp, err := p.Chat(t.Context(), []Message{
		{Role: RoleSystem, Content: "You are helpful."},
		{Role: RoleUser, Content: "Hi"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Content != "Hello from Vertex!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello from Vertex!")
	}
	if resp.Usage.InputTokens != 15 {
		t.Errorf("InputTokens = %d, want 15", resp.Usage.InputTokens)
	}

	// Verify request body matches Anthropic Messages format
	if gotBody.Model != "claude-sonnet-4@20250514" {
		t.Errorf("request model = %q", gotBody.Model)
	}
	if gotBody.System != "You are helpful." {
		t.Errorf("request system = %q", gotBody.System)
	}
}

func TestAnthropicVertexProvider_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Vertex"}}`,
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

	p := &anthropicVertexProvider{
		config: AnthropicVertexConfig{
			ProjectID:  "proj",
			Region:     "us-east5",
			Model:      "claude-sonnet-4@20250514",
			MaxTokens:  1024,
			HTTPClient: &http.Client{Transport: &urlRewriteTransport{target: srv.URL}},
		},
		tokenSource: &mockTokenSource{token: "tok"},
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
	if got := strings.Join(texts, ""); got != "Hello Vertex" {
		t.Errorf("streamed text = %q, want %q", got, "Hello Vertex")
	}
}
