package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ollamaChatNDJSON builds a minimal Ollama chat NDJSON response line.
func ollamaChatNDJSON(content string, done bool) string {
	resp := map[string]any{
		"model":      "test",
		"created_at": time.Now().Format(time.RFC3339),
		"message": map[string]any{
			"role":    "assistant",
			"content": content,
		},
		"done":               done,
		"prompt_eval_count":  3,
		"eval_count":         7,
	}
	b, _ := json.Marshal(resp)
	return string(b) + "\n"
}

// ollamaMockServer sets up a minimal Ollama HTTP server for unit tests.
func ollamaMockServer(t *testing.T, chatContent string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// HEAD / — heartbeat
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
		}
	})

	// POST /api/chat — chat (streaming NDJSON)
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(ollamaChatNDJSON(chatContent, true)))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	// GET /api/tags — list models
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "llama3.2:latest", "model": "llama3.2:latest", "size": int64(4000000000)},
			},
		})
	})

	return httptest.NewServer(mux)
}

func TestOllamaProvider_ImplementsProvider(t *testing.T) {
	var _ Provider = (*OllamaProvider)(nil)
}

func TestOllamaProvider_Name(t *testing.T) {
	p := NewOllamaProvider(OllamaConfig{Model: "qwen3.5:7b"})
	if got := p.Name(); got != "ollama" {
		t.Errorf("Name()=%q, want %q", got, "ollama")
	}
}

func TestOllamaProvider_AuthModeInfo(t *testing.T) {
	p := NewOllamaProvider(OllamaConfig{})
	info := p.AuthModeInfo()
	if info.Mode != "ollama" {
		t.Errorf("Mode=%q, want %q", info.Mode, "ollama")
	}
	if !info.ServerSafe {
		t.Error("ServerSafe should be true")
	}
	if info.Warning != "" {
		t.Errorf("Warning should be empty, got %q", info.Warning)
	}
}

func TestOllamaProvider_DefaultBaseURL(t *testing.T) {
	p := NewOllamaProvider(OllamaConfig{})
	if p.config.BaseURL != defaultOllamaBaseURL {
		t.Errorf("BaseURL=%q, want %q", p.config.BaseURL, defaultOllamaBaseURL)
	}
}

func TestOllamaProvider_Health(t *testing.T) {
	srv := ollamaMockServer(t, "")
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "test"})
	if err := p.Health(context.Background()); err != nil {
		t.Errorf("Health() error: %v", err)
	}
}

func TestOllamaProvider_Chat(t *testing.T) {
	srv := ollamaMockServer(t, "hello world")
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "test"})
	resp, err := p.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "hello world" {
		t.Errorf("Content=%q, want %q", resp.Content, "hello world")
	}
	if resp.Usage.InputTokens != 3 {
		t.Errorf("InputTokens=%d, want 3", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 7 {
		t.Errorf("OutputTokens=%d, want 7", resp.Usage.OutputTokens)
	}
}

func TestOllamaProvider_ChatWithThinkTags(t *testing.T) {
	srv := ollamaMockServer(t, "<think>reasoning</think>answer")
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "test"})
	resp, err := p.Chat(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Thinking != "reasoning" {
		t.Errorf("Thinking=%q, want %q", resp.Thinking, "reasoning")
	}
	if resp.Content != "answer" {
		t.Errorf("Content=%q, want %q", resp.Content, "answer")
	}
}

func TestOllamaProvider_Stream(t *testing.T) {
	srv := ollamaMockServer(t, "stream result")
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "test"})
	ch, err := p.Stream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var textParts []string
	var sawDone bool
	for ev := range ch {
		switch ev.Type {
		case "text":
			textParts = append(textParts, ev.Text)
		case "done":
			sawDone = true
		case "error":
			t.Fatalf("stream error: %s", ev.Error)
		}
	}
	if !sawDone {
		t.Error("expected done event")
	}
	full := strings.Join(textParts, "")
	if full != "stream result" {
		t.Errorf("text=%q, want %q", full, "stream result")
	}
}

func TestOllamaProvider_StreamThinkTags(t *testing.T) {
	srv := ollamaMockServer(t, "<think>thinking</think>content")
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "test"})
	ch, err := p.Stream(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, nil)
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var thinkParts, textParts []string
	for ev := range ch {
		switch ev.Type {
		case "thinking":
			thinkParts = append(thinkParts, ev.Thinking)
		case "text":
			textParts = append(textParts, ev.Text)
		case "error":
			t.Fatalf("stream error: %s", ev.Error)
		}
	}
	if strings.Join(thinkParts, "") != "thinking" {
		t.Errorf("thinking=%q, want %q", strings.Join(thinkParts, ""), "thinking")
	}
	if strings.Join(textParts, "") != "content" {
		t.Errorf("text=%q, want %q", strings.Join(textParts, ""), "content")
	}
}

func TestOllamaProvider_ListModels(t *testing.T) {
	srv := ollamaMockServer(t, "")
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Model: "test"})
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len=%d, want 1", len(models))
	}
	if models[0].ID != "llama3.2:latest" {
		t.Errorf("ID=%q, want %q", models[0].ID, "llama3.2:latest")
	}
}
