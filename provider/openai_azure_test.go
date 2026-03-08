package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIAzureProvider_Name(t *testing.T) {
	p, err := NewOpenAIAzureProvider(OpenAIAzureConfig{
		Resource: "myresource", DeploymentName: "gpt-4o", APIKey: "key123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Name(); got != "openai_azure" {
		t.Errorf("Name() = %q, want %q", got, "openai_azure")
	}
}

func TestOpenAIAzureProvider_AuthModeInfo(t *testing.T) {
	p, err := NewOpenAIAzureProvider(OpenAIAzureConfig{
		Resource: "myresource", DeploymentName: "gpt-4o", APIKey: "key123",
	})
	if err != nil {
		t.Fatal(err)
	}
	info := p.AuthModeInfo()
	if info.Mode != "azure" {
		t.Errorf("AuthModeInfo().Mode = %q, want %q", info.Mode, "azure")
	}
	if info.DisplayName != "OpenAI (Azure OpenAI Service)" {
		t.Errorf("AuthModeInfo().DisplayName = %q, want %q", info.DisplayName, "OpenAI (Azure OpenAI Service)")
	}
	if !info.ServerSafe {
		t.Error("AuthModeInfo().ServerSafe = false, want true")
	}
}

func TestOpenAIAzureProvider_ImplementsProvider(t *testing.T) {
	var _ Provider = (*OpenAIAzureProvider)(nil)
}

func TestOpenAIAzureProvider_ValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  OpenAIAzureConfig
		want string
	}{
		{"missing resource", OpenAIAzureConfig{DeploymentName: "d", APIKey: "k"}, "Resource is required"},
		{"missing deployment", OpenAIAzureConfig{Resource: "r", APIKey: "k"}, "DeploymentName is required"},
		{"missing auth", OpenAIAzureConfig{Resource: "r", DeploymentName: "d"}, "APIKey or EntraToken is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewOpenAIAzureProvider(tt.cfg)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); !strings.Contains(got, tt.want) {
				t.Errorf("error = %q, want to contain %q", got, tt.want)
			}
		})
	}
}

func TestOpenAIAzureProvider_DefaultAPIVersion(t *testing.T) {
	p, err := NewOpenAIAzureProvider(OpenAIAzureConfig{
		Resource: "res", DeploymentName: "dep", APIKey: "key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.config.APIVersion != defaultAzureOpenAIAPIVersion {
		t.Errorf("APIVersion = %q, want %q", p.config.APIVersion, defaultAzureOpenAIAPIVersion)
	}
}

func TestOpenAIAzureProvider_URLConstruction(t *testing.T) {
	p, err := NewOpenAIAzureProvider(OpenAIAzureConfig{
		Resource: "myres", DeploymentName: "gpt-4o", APIKey: "key", APIVersion: "2024-10-21",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://myres.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21"
	if p.endpoint != want {
		t.Errorf("endpoint = %q, want %q", p.endpoint, want)
	}
}

func TestOpenAIAzureProvider_APIKeyAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("api-key"); got != "azure-key-123" {
			t.Errorf("api-key header = %q, want %q", got, "azure-key-123")
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization header should be empty with API key auth, got %q", got)
		}

		resp := openaiResponse{
			ID: "chatcmpl-azure",
			Choices: []openaiChoice{
				{Message: openaiMessage{Role: "assistant", Content: "hello from azure"}, FinishReason: "stop"},
			},
			Usage: openaiUsage{PromptTokens: 5, CompletionTokens: 3},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// Override endpoint to point at test server
	p := &OpenAIAzureProvider{
		config: OpenAIAzureConfig{
			Resource:       "test",
			DeploymentName: "gpt-4o",
			APIKey:         "azure-key-123",
			APIVersion:     "2024-10-21",
			MaxTokens:      4096,
			HTTPClient:     http.DefaultClient,
		},
		endpoint: srv.URL,
	}

	got, err := p.Chat(t.Context(), []Message{
		{Role: RoleUser, Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if got.Content != "hello from azure" {
		t.Errorf("Chat() content = %q, want %q", got.Content, "hello from azure")
	}
}

func TestOpenAIAzureProvider_EntraTokenAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer entra-token-xyz" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer entra-token-xyz")
		}
		if got := r.Header.Get("api-key"); got != "" {
			t.Errorf("api-key header should be empty with Entra auth, got %q", got)
		}

		resp := openaiResponse{
			ID: "chatcmpl-entra",
			Choices: []openaiChoice{
				{Message: openaiMessage{Role: "assistant", Content: "hello from entra"}, FinishReason: "stop"},
			},
			Usage: openaiUsage{PromptTokens: 5, CompletionTokens: 3},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := &OpenAIAzureProvider{
		config: OpenAIAzureConfig{
			Resource:       "test",
			DeploymentName: "gpt-4o",
			EntraToken:     "entra-token-xyz",
			APIVersion:     "2024-10-21",
			MaxTokens:      4096,
			HTTPClient:     http.DefaultClient,
		},
		endpoint: srv.URL,
	}

	got, err := p.Chat(t.Context(), []Message{
		{Role: RoleUser, Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if got.Content != "hello from entra" {
		t.Errorf("Chat() content = %q, want %q", got.Content, "hello from entra")
	}
}

func TestOpenAIAzureProvider_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		content := "azure-streamed"
		chunk := openaiStreamChunk{
			ID: "chatcmpl-azure-stream",
			Choices: []openaiStreamChoice{
				{Index: 0, Delta: openaiStreamDelta{Content: &content}},
			},
		}
		data, _ := json.Marshal(chunk)
		w.Write([]byte("data: " + string(data) + "\n\n"))
		flusher.Flush()

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	p := &OpenAIAzureProvider{
		config: OpenAIAzureConfig{
			Resource:       "test",
			DeploymentName: "gpt-4o",
			APIKey:         "azure-key",
			APIVersion:     "2024-10-21",
			MaxTokens:      4096,
			HTTPClient:     http.DefaultClient,
		},
		endpoint: srv.URL,
	}

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
	if texts[0] != "azure-streamed" {
		t.Errorf("first text event = %q, want %q", texts[0], "azure-streamed")
	}
}

