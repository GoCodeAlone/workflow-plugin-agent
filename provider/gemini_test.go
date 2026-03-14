package provider

import (
	"testing"
)

func TestGeminiProvider_Name(t *testing.T) {
	p, err := NewGeminiProvider(GeminiConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Name(); got != "gemini" {
		t.Errorf("Name() = %q, want %q", got, "gemini")
	}
}

func TestGeminiProvider_AuthModeInfo(t *testing.T) {
	p, err := NewGeminiProvider(GeminiConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	info := p.AuthModeInfo()

	if info.Mode != "gemini" {
		t.Errorf("AuthModeInfo().Mode = %q, want %q", info.Mode, "gemini")
	}
	if info.DisplayName != "Google Gemini" {
		t.Errorf("AuthModeInfo().DisplayName = %q, want %q", info.DisplayName, "Google Gemini")
	}
	if info.DocsURL != "https://ai.google.dev/gemini-api/docs/api-key" {
		t.Errorf("AuthModeInfo().DocsURL = %q, want Gemini docs URL", info.DocsURL)
	}
	if !info.ServerSafe {
		t.Error("AuthModeInfo().ServerSafe = false, want true")
	}
}

func TestGeminiProvider_ImplementsProvider(t *testing.T) {
	var _ Provider = (*GeminiProvider)(nil)
}

func TestGeminiProvider_RequiresAPIKey(t *testing.T) {
	_, err := NewGeminiProvider(GeminiConfig{})
	if err == nil {
		t.Fatal("expected error when no API key provided")
	}
}

func TestGeminiProvider_DefaultModel(t *testing.T) {
	p, err := NewGeminiProvider(GeminiConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if p.config.Model != defaultGeminiModel {
		t.Errorf("default Model = %q, want %q", p.config.Model, defaultGeminiModel)
	}
}

func TestGeminiProvider_DefaultMaxTokens(t *testing.T) {
	p, err := NewGeminiProvider(GeminiConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if p.config.MaxTokens != defaultGeminiMaxTokens {
		t.Errorf("default MaxTokens = %d, want %d", p.config.MaxTokens, defaultGeminiMaxTokens)
	}
}

func TestToGeminiSchema_NilParams(t *testing.T) {
	s := toGeminiSchema(nil)
	if s != nil {
		t.Errorf("toGeminiSchema(nil) = %v, want nil", s)
	}
}

func TestToGeminiSchema_WithProperties(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "A name"},
			"age":  map[string]any{"type": "integer"},
		},
		"required": []any{"name"},
	}
	s := toGeminiSchema(params)
	if s == nil {
		t.Fatal("toGeminiSchema returned nil for valid params")
	}
	if len(s.Properties) != 2 {
		t.Errorf("Properties count = %d, want 2", len(s.Properties))
	}
	if len(s.Required) != 1 || s.Required[0] != "name" {
		t.Errorf("Required = %v, want [name]", s.Required)
	}
}

func TestGeminiFallbackModels(t *testing.T) {
	models := geminiFallbackModels()
	if len(models) == 0 {
		t.Error("geminiFallbackModels() returned empty slice")
	}
	for _, m := range models {
		if m.ID == "" {
			t.Error("fallback model has empty ID")
		}
		if m.Name == "" {
			t.Error("fallback model has empty Name")
		}
	}
}
