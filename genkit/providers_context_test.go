package genkit

import (
	"context"
	"testing"
)

func TestOllamaProvider_ContextWindow(t *testing.T) {
	p, err := NewOllamaProvider(context.Background(), "qwen3:8b", "", 0, 8192)
	if err != nil {
		t.Fatalf("NewOllamaProvider: %v", err)
	}
	gp, ok := p.(*genkitProvider)
	if !ok {
		t.Fatalf("expected *genkitProvider, got %T", p)
	}
	if gp.ContextWindow() != 8192 {
		t.Errorf("ContextWindow() = %d, want 8192", gp.ContextWindow())
	}
}

func TestOllamaProvider_ContextWindow_Zero(t *testing.T) {
	p, err := NewOllamaProvider(context.Background(), "qwen3:8b", "", 0, 0)
	if err != nil {
		t.Fatalf("NewOllamaProvider: %v", err)
	}
	gp, ok := p.(*genkitProvider)
	if !ok {
		t.Fatalf("expected *genkitProvider, got %T", p)
	}
	if gp.ContextWindow() != 0 {
		t.Errorf("ContextWindow() = %d, want 0 (use model default)", gp.ContextWindow())
	}
}

func TestOllamaProvider_NumCtxInConfig(t *testing.T) {
	p, err := NewOllamaProvider(context.Background(), "qwen3:8b", "", 512, 4096)
	if err != nil {
		t.Fatalf("NewOllamaProvider: %v", err)
	}
	gp, ok := p.(*genkitProvider)
	if !ok {
		t.Fatalf("expected *genkitProvider, got %T", p)
	}
	// Verify both max tokens and context window are stored
	if gp.maxTokens != 512 {
		t.Errorf("maxTokens = %d, want 512", gp.maxTokens)
	}
	if gp.contextWindow != 4096 {
		t.Errorf("contextWindow = %d, want 4096", gp.contextWindow)
	}
}
