package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCopilotModelsProvider_Name(t *testing.T) {
	p := NewCopilotModelsProvider(CopilotModelsConfig{Token: "ghp_test"})
	if got := p.Name(); got != "copilot_models" {
		t.Errorf("Name() = %q, want %q", got, "copilot_models")
	}
}

func TestCopilotModelsProvider_AuthModeInfo(t *testing.T) {
	p := NewCopilotModelsProvider(CopilotModelsConfig{Token: "ghp_test"})
	info := p.AuthModeInfo()

	if info.Mode != "github_models" {
		t.Errorf("AuthModeInfo().Mode = %q, want %q", info.Mode, "github_models")
	}
	if info.DisplayName != "GitHub Models" {
		t.Errorf("AuthModeInfo().DisplayName = %q, want %q", info.DisplayName, "GitHub Models")
	}
	if info.DocsURL != "https://docs.github.com/en/rest/models/inference" {
		t.Errorf("AuthModeInfo().DocsURL = %q, want GitHub Models docs URL", info.DocsURL)
	}
	if !info.ServerSafe {
		t.Error("AuthModeInfo().ServerSafe = false, want true")
	}
}

func TestCopilotModelsProvider_ImplementsProvider(t *testing.T) {
	var _ Provider = (*CopilotModelsProvider)(nil)
}

func TestCopilotModelsProvider_DefaultBaseURL(t *testing.T) {
	p := NewCopilotModelsProvider(CopilotModelsConfig{Token: "ghp_test"})
	if p.config.BaseURL != defaultCopilotModelsBaseURL {
		t.Errorf("default BaseURL = %q, want %q", p.config.BaseURL, defaultCopilotModelsBaseURL)
	}
}

func TestCopilotModelsProvider_CustomBaseURL(t *testing.T) {
	custom := "https://custom.models.github.ai/inference"
	p := NewCopilotModelsProvider(CopilotModelsConfig{Token: "ghp_test", BaseURL: custom})
	if p.config.BaseURL != custom {
		t.Errorf("BaseURL = %q, want %q", p.config.BaseURL, custom)
	}
}

func TestCopilotModelsProvider_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test123" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer ghp_test123")
		}

		resp := openaiResponse{
			ID: "chatcmpl-gh",
			Choices: []openaiChoice{
				{Message: openaiMessage{Role: "assistant", Content: "hello from github models"}, FinishReason: "stop"},
			},
			Usage: openaiUsage{PromptTokens: 8, CompletionTokens: 4},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewCopilotModelsProvider(CopilotModelsConfig{
		Token:   "ghp_test123",
		Model:   "openai/gpt-4o",
		BaseURL: srv.URL,
	})

	got, err := p.Chat(context.Background(), []Message{
		{Role: RoleUser, Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if got.Content != "hello from github models" {
		t.Errorf("Chat() content = %q, want %q", got.Content, "hello from github models")
	}
	if got.Usage.InputTokens != 8 || got.Usage.OutputTokens != 4 {
		t.Errorf("Chat() usage = %+v, want input=8 output=4", got.Usage)
	}
}

func TestCopilotModelsProvider_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		content := "gh-streamed"
		chunk := openaiStreamChunk{
			ID: "chatcmpl-gh-stream",
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

	p := NewCopilotModelsProvider(CopilotModelsConfig{
		Token:   "ghp_test123",
		Model:   "openai/gpt-4o",
		BaseURL: srv.URL,
	})

	ch, err := p.Stream(context.Background(), []Message{
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
	if texts[0] != "gh-streamed" {
		t.Errorf("first text event = %q, want %q", texts[0], "gh-streamed")
	}
}
