package genkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	ollamaPlugin "github.com/firebase/genkit/go/plugins/ollama"
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
	p, err := NewOllamaProvider(context.Background(), "qwen3:8b", "", 4096)
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
	_, err := NewBedrockProvider(context.Background(), "us-east-1", "anthropic.claude-sonnet-4", "", "", "", "", 4096)
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
	p, err := NewOllamaProvider(context.Background(), "test", "http://localhost:11434", 4096)
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

	p, err := NewOllamaProvider(context.Background(), "gemma4:e2b", mockOllama.URL, 0)
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
