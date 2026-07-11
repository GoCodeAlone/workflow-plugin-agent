package genkit

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	ollamaPlugin "github.com/firebase/genkit/go/plugins/ollama"
	"github.com/openai/openai-go"
)

func TestNewAnthropicProvider_MissingKey(t *testing.T) {
	_, err := NewAnthropicProvider(context.Background(), "", "claude-sonnet-4-6", "", 4096)
	if err == nil {
		t.Error("expected error for missing API key")
	}
}

func TestNewOpenAIProvider_MissingKey(t *testing.T) {
	_, err := NewOpenAIProvider(context.Background(), "", "gpt-4o", "", 4096)
	if err == nil {
		t.Error("expected error for missing API key")
	}
}

func TestNewGoogleAIProvider_MissingKey(t *testing.T) {
	_, err := NewGoogleAIProvider(context.Background(), "", "gemini-2.0-flash", 4096)
	if err == nil {
		t.Error("expected error for missing API key")
	}
}

func TestNewOllamaProvider_DefaultAddress(t *testing.T) {
	// Ollama doesn't require an API key; verify factory instantiation works
	// with default address (no real server needed for creation)
	p, err := NewOllamaProvider(context.Background(), "qwen3:8b", "", 4096, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.Name() != "ollama" {
		t.Errorf("expected name 'ollama', got %q", p.Name())
	}
}

func TestNewOpenAICompatibleProvider_NoKey(t *testing.T) {
	// Local providers may not need a key
	p, err := NewOpenAICompatibleProvider(context.Background(), "local", "", "model", "http://localhost:8080", 4096)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestOpenAICompatibleProvider_MaxTokensUsesNativeConfig(t *testing.T) {
	var requestBody struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		MaxTokens           *int `json:"max_tokens"`
		MaxCompletionTokens *int `json:"max_completion_tokens"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected request target", http.StatusNotFound)
			t.Errorf("request target = %s %s", r.Method, r.URL.Path)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			http.Error(w, "unexpected authorization", http.StatusUnauthorized)
			t.Errorf("authorization header = %q", got)
			return
		}
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "application/json" {
			http.Error(w, "unexpected content type", http.StatusUnsupportedMediaType)
			t.Errorf("content type = %q, parse error: %v", contentType, err)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			t.Errorf("decode request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	p, err := NewOpenAICompatibleProvider(t.Context(), "test", "key", "fixture-model", server.URL+"/v1", 321)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	response, err := p.Chat(t.Context(), []provider.Message{{Role: provider.RoleUser, Content: "ping"}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if response.Content != "ok" {
		t.Fatalf("response content = %q, want ok", response.Content)
	}
	if requestBody.Model != "fixture-model" {
		t.Fatalf("model = %q, want fixture-model", requestBody.Model)
	}
	if len(requestBody.Messages) != 1 {
		t.Fatalf("messages = %#v, want one message", requestBody.Messages)
	}
	message := requestBody.Messages[0]
	if message.Role != "user" || len(message.Content) != 1 || message.Content[0].Type != "text" || message.Content[0].Text != "ping" {
		t.Fatalf("message = %#v, want one user text part containing ping", message)
	}
	if requestBody.MaxTokens == nil || *requestBody.MaxTokens != 321 {
		t.Fatalf("max_tokens = %v, want 321", requestBody.MaxTokens)
	}
	if requestBody.MaxCompletionTokens != nil {
		t.Fatal("generic compatible request unexpectedly contains max_completion_tokens")
	}
}

func TestOpenAIAdapterConstructorsUseNativeTokenLimitConfig(t *testing.T) {
	tests := []struct {
		name                string
		new                 func() (provider.Provider, error)
		wantMaxTokens       bool
		wantCompletionLimit bool
	}{
		{name: "openai", new: func() (provider.Provider, error) {
			return NewOpenAIProvider(t.Context(), "key", "gpt-4.1", "", 321)
		}, wantCompletionLimit: true},
		{name: "openai reasoning", new: func() (provider.Provider, error) {
			return NewOpenAIProvider(t.Context(), "key", "o3", "", 321)
		}, wantCompletionLimit: true},
		{name: "compatible", new: func() (provider.Provider, error) {
			return NewOpenAICompatibleProvider(t.Context(), "test", "key", "model", "http://localhost", 321)
		}, wantMaxTokens: true},
		{name: "azure", new: func() (provider.Provider, error) {
			return NewAzureOpenAIProvider(t.Context(), "resource", "deployment", "2024-10-21", "key", "", 321)
		}, wantCompletionLimit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, err := test.new()
			if err != nil {
				t.Fatalf("create provider: %v", err)
			}
			gp, ok := p.(*genkitProvider)
			if !ok {
				t.Fatalf("provider type = %T, want *genkitProvider", p)
			}
			config, ok := gp.customConfig.(*openai.ChatCompletionNewParams)
			if !ok {
				t.Fatalf("custom config type = %T, want *openai.ChatCompletionNewParams", gp.customConfig)
			}
			if config.MaxTokens.Valid() != test.wantMaxTokens {
				t.Fatalf("max tokens valid = %t, want %t", config.MaxTokens.Valid(), test.wantMaxTokens)
			}
			if config.MaxTokens.Valid() && config.MaxTokens.Value != 321 {
				t.Fatalf("max tokens = %d, want 321", config.MaxTokens.Value)
			}
			if config.MaxCompletionTokens.Valid() != test.wantCompletionLimit {
				t.Fatalf("max completion tokens valid = %t, want %t", config.MaxCompletionTokens.Valid(), test.wantCompletionLimit)
			}
			if config.MaxCompletionTokens.Valid() && config.MaxCompletionTokens.Value != 321 {
				t.Fatalf("max completion tokens = %d, want 321", config.MaxCompletionTokens.Value)
			}
		})
	}
}

func TestNewAzureOpenAIProvider_MissingResource(t *testing.T) {
	_, err := NewAzureOpenAIProvider(context.Background(), "", "gpt-4o", "2024-10-21", "key", "", 4096)
	if err == nil {
		t.Error("expected error for missing resource")
	}
}

func TestNewAzureOpenAIProvider_MissingCredentials(t *testing.T) {
	_, err := NewAzureOpenAIProvider(context.Background(), "myresource", "gpt-4o", "2024-10-21", "", "", 4096)
	if err == nil {
		t.Error("expected error for missing credentials")
	}
}

func TestNewBedrockProvider_MissingKey(t *testing.T) {
	_, err := NewBedrockProvider(context.Background(), "bedrock", "us-east-1", "anthropic.claude-sonnet-4", "", "", "", "", 4096)
	if err == nil {
		t.Error("expected error for missing secret key")
	}
}

func TestNewVertexAIProvider_MissingProject(t *testing.T) {
	_, err := NewVertexAIProvider(context.Background(), "", "us-central1", "gemini-2.0-flash", "", 4096)
	if err == nil {
		t.Error("expected error for missing project ID")
	}
}

func TestProviderImplementsInterface(t *testing.T) {
	// Ensure all returned providers implement provider.Provider
	p, err := NewOllamaProvider(context.Background(), "test", "http://localhost:11434", 4096, 0)
	if err != nil {
		t.Skip("factory failed, skipping interface check")
	}
	_ = p // already provider.Provider; compile verifies interface
}

func TestNewOllamaProvider_GemmaToolSupport(t *testing.T) {
	// gemma4 is NOT in the Genkit Ollama plugin's static toolSupportedModels list,
	// but the GoCodeAlone fork dynamically detects capabilities via /api/show.
	// This test uses a mock Ollama server that reports gemma4 as tool-capable.
	mockOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{
				"capabilities": []string{"completion", "tools", "thinking"},
			})
		case "/api/chat":
			// Return a tool call response to prove tools are enabled.
			json.NewEncoder(w).Encode(map[string]any{
				"model": "gemma4:e2b",
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{{
						"function": map[string]any{
							"name":      "file_read",
							"arguments": map[string]any{"path": "/tmp/test"},
						},
					}},
				},
				"done": true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockOllama.Close()

	p, err := NewOllamaProvider(context.Background(), "gemma4:e2b", mockOllama.URL, 0, 0)
	if err != nil {
		t.Fatalf("unexpected creation error: %v", err)
	}
	gp, ok := p.(*genkitProvider)
	if !ok {
		t.Fatalf("expected *genkitProvider, got %T", p)
	}

	// Verify model is registered in the Genkit instance.
	if !ollamaPlugin.IsDefinedModel(gp.g, "gemma4:e2b") {
		t.Error("model 'gemma4:e2b' not defined in Genkit instance")
	}

	// Chat with tools should succeed (not fail with "does not support tool use").
	tools := []provider.ToolDef{{
		Name:        "file_read",
		Description: "Read a file",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}}
	resp, err := gp.Chat(context.Background(),
		[]provider.Message{{Role: provider.RoleUser, Content: "read /tmp/test"}},
		tools,
	)
	if err != nil {
		if strings.Contains(err.Error(), "does not support tool use") {
			t.Fatalf("model should support tools (dynamic detection via /api/show) but got: %v", err)
		}
		t.Fatalf("Chat failed: %v", err)
	}
	if len(resp.ToolCalls) == 0 {
		t.Error("expected tool calls in response")
	}
}
