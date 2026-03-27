package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newLlamaCppTestServer starts an httptest server returning OpenAI-compatible responses.
// If thinking is non-empty, the content wraps it in <think> tags.
func newLlamaCppTestServer(t *testing.T, thinking, content string) *httptest.Server {
	t.Helper()
	rawContent := content
	if thinking != "" {
		rawContent = fmt.Sprintf("<think>%s</think>%s", thinking, content)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"id":      "chatcmpl-test",
				"object":  "chat.completion",
				"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": rawContent}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
}

// newLlamaCppStreamServer starts an httptest server returning SSE streaming responses.
func newLlamaCppStreamServer(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, chunk := range chunks {
				delta := map[string]any{
					"id":      "chatcmpl-test",
					"object":  "chat.completion.chunk",
					"choices": []any{map[string]any{"delta": map[string]any{"content": chunk}, "finish_reason": nil}},
				}
				b, _ := json.Marshal(delta)
				fmt.Fprintf(w, "data: %s\n\n", b)
			}
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		http.NotFound(w, r)
	}))
}

func TestLlamaCppProvider_Defaults(t *testing.T) {
	p := NewLlamaCppProvider(LlamaCppConfig{BaseURL: "http://localhost:8081/v1"})
	if p.config.GPULayers != defaultLlamaCppGPULayers {
		t.Errorf("GPULayers: want %d, got %d", defaultLlamaCppGPULayers, p.config.GPULayers)
	}
	if p.config.ContextSize != defaultLlamaCppContextSize {
		t.Errorf("ContextSize: want %d, got %d", defaultLlamaCppContextSize, p.config.ContextSize)
	}
	if p.config.MaxTokens != defaultLlamaCppMaxTokens {
		t.Errorf("MaxTokens: want %d, got %d", defaultLlamaCppMaxTokens, p.config.MaxTokens)
	}
	if p.config.Port != defaultLlamaCppPort {
		t.Errorf("Port: want %d, got %d", defaultLlamaCppPort, p.config.Port)
	}
	if p.Name() != "llama_cpp" {
		t.Errorf("Name: want %q, got %q", "llama_cpp", p.Name())
	}
}

func TestLlamaCppProvider_Chat_NoThinking(t *testing.T) {
	srv := newLlamaCppTestServer(t, "", "The sky is blue.")
	defer srv.Close()

	p := NewLlamaCppProvider(LlamaCppConfig{
		BaseURL:    srv.URL + "/v1",
		HTTPClient: srv.Client(),
	})

	resp, err := p.Chat(context.Background(), []Message{{Role: RoleUser, Content: "What color is the sky?"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "The sky is blue." {
		t.Errorf("Content: want %q, got %q", "The sky is blue.", resp.Content)
	}
	if resp.Thinking != "" {
		t.Errorf("Thinking: want empty, got %q", resp.Thinking)
	}
}

func TestLlamaCppProvider_Chat_WithThinking(t *testing.T) {
	srv := newLlamaCppTestServer(t, "Let me reason step by step.", "The answer is 42.")
	defer srv.Close()

	p := NewLlamaCppProvider(LlamaCppConfig{
		BaseURL:    srv.URL + "/v1",
		HTTPClient: srv.Client(),
	})

	resp, err := p.Chat(context.Background(), []Message{{Role: RoleUser, Content: "What is the answer?"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Thinking != "Let me reason step by step." {
		t.Errorf("Thinking: want %q, got %q", "Let me reason step by step.", resp.Thinking)
	}
	if resp.Content != "The answer is 42." {
		t.Errorf("Content: want %q, got %q", "The answer is 42.", resp.Content)
	}
}

func TestLlamaCppProvider_Stream_WithThinking(t *testing.T) {
	// Stream chunks that spell out <think>reasoning</think>answer
	chunks := []string{"<think>", "reasoning", "</think>", "answer"}
	srv := newLlamaCppStreamServer(t, chunks)
	defer srv.Close()

	p := NewLlamaCppProvider(LlamaCppConfig{
		BaseURL:    srv.URL + "/v1",
		HTTPClient: srv.Client(),
	})

	ch, err := p.Stream(context.Background(), []Message{{Role: RoleUser, Content: "question"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var thinkingText, contentText string
	for event := range ch {
		switch event.Type {
		case "thinking":
			thinkingText += event.Thinking
		case "text":
			contentText += event.Text
		case "error":
			t.Fatalf("stream error: %s", event.Error)
		}
	}
	if thinkingText != "reasoning" {
		t.Errorf("thinking: want %q, got %q", "reasoning", thinkingText)
	}
	if contentText != "answer" {
		t.Errorf("content: want %q, got %q", "answer", contentText)
	}
}

func TestLlamaCppProvider_Close_NoProcess(t *testing.T) {
	p := NewLlamaCppProvider(LlamaCppConfig{BaseURL: "http://localhost:8081/v1"})
	if err := p.Close(); err != nil {
		t.Errorf("Close with no process: want nil, got %v", err)
	}
}

func TestLlamaCppProvider_EnsureServer_ExternalMode(t *testing.T) {
	p := NewLlamaCppProvider(LlamaCppConfig{BaseURL: "http://localhost:8081/v1"})
	// External mode: EnsureServer should be a no-op.
	if err := p.EnsureServer(context.Background()); err != nil {
		t.Errorf("EnsureServer external mode: want nil, got %v", err)
	}
}
