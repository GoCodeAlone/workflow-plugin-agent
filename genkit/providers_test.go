package genkit

import (
	"context"
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
	// gemma4 is NOT in the Genkit Ollama plugin's hardcoded toolSupportedModels list.
	// NewOllamaProvider must explicitly define the model with Tools:true so that
	// Chat() with tools does not fail with "model does not support tool use".
	p, err := NewOllamaProvider(context.Background(), "gemma4:e2b", "http://127.0.0.1:1", 0)
	if err != nil {
		t.Fatalf("unexpected creation error: %v", err)
	}
	gp, ok := p.(*genkitProvider)
	if !ok {
		t.Fatalf("expected *genkitProvider, got %T", p)
	}

	// Verify model is pre-registered in the Genkit instance.
	// ollamaPlugin.IsDefinedModel calls LookupModel which triggers dynamic resolution;
	// after explicit DefineModel this should always be true.
	if !ollamaPlugin.IsDefinedModel(gp.g, "gemma4:e2b") {
		t.Error("model 'gemma4:e2b' not defined in Genkit instance")
	}

	// Calling Chat with tools must fail with a connection error — NOT a
	// "does not support tool use" validation error.  The latter would mean the
	// model was registered with Tools:false (e.g. via DefineModel with nil opts,
	// which would check toolSupportedModels and exclude gemma4).
	tools := []provider.ToolDef{{
		Name:        "file_read",
		Description: "Read a file",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}}
	_, err = gp.Chat(context.Background(),
		[]provider.Message{{Role: provider.RoleUser, Content: "test"}},
		tools,
	)
	// Expect an error (no server), but NOT a "does not support tool use" validation error.
	if err == nil {
		t.Fatal("expected error (no server running)")
	}
	if strings.Contains(err.Error(), "does not support tool use") {
		t.Errorf("model should support tools but got: %v", err)
	}
}
