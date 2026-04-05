package genkit

import (
	"context"
	"testing"
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
