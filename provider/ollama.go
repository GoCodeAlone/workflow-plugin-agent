package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	ollamaapi "github.com/ollama/ollama/api"
)

const defaultOllamaBaseURL = "http://localhost:11434"

// OllamaConfig holds configuration for the Ollama provider.
type OllamaConfig struct {
	Model      string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// OllamaProvider implements Provider using a local Ollama server.
type OllamaProvider struct {
	client *ollamaapi.Client
	config OllamaConfig
}

// NewOllamaProvider creates a new OllamaProvider with the given config.
func NewOllamaProvider(cfg OllamaConfig) *OllamaProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultOllamaBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		// Fall back to default URL if configured one is invalid.
		base, _ = url.Parse(defaultOllamaBaseURL)
		cfg.BaseURL = defaultOllamaBaseURL
	}
	return &OllamaProvider{
		client: ollamaapi.NewClient(base, cfg.HTTPClient),
		config: cfg,
	}
}

func (p *OllamaProvider) Name() string { return "ollama" }

func (p *OllamaProvider) AuthModeInfo() AuthModeInfo {
	return LocalAuthMode("ollama", "Ollama (Local)")
}

// Chat sends a non-streaming request and returns the complete response.
func (p *OllamaProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	streamFalse := false
	req := &ollamaapi.ChatRequest{
		Model:    p.config.Model,
		Messages: toOllamaMessages(messages),
		Stream:   &streamFalse,
		Options:  map[string]any{},
	}
	if len(tools) > 0 {
		req.Tools = toOllamaTools(tools)
	}
	if p.config.MaxTokens > 0 {
		req.Options["num_predict"] = p.config.MaxTokens
	}

	var final ollamaapi.ChatResponse
	if err := p.client.Chat(ctx, req, func(resp ollamaapi.ChatResponse) error {
		final = resp
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	return fromOllamaResponse(final), nil
}

// Stream sends a streaming request and emits events on the returned channel.
// Thinking tokens extracted via ThinkingStreamParser are emitted as "thinking" events.
func (p *OllamaProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	req := &ollamaapi.ChatRequest{
		Model:    p.config.Model,
		Messages: toOllamaMessages(messages),
		Options:  map[string]any{},
	}
	if len(tools) > 0 {
		req.Tools = toOllamaTools(tools)
	}
	if p.config.MaxTokens > 0 {
		req.Options["num_predict"] = p.config.MaxTokens
	}

	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		var parser ThinkingStreamParser
		var usage *Usage

		err := p.client.Chat(ctx, req, func(resp ollamaapi.ChatResponse) error {
			text, toolCalls, done := fromOllamaStreamChunk(resp)
			if text != "" {
				for _, ev := range parser.Feed(text) {
					ch <- ev
				}
			}
			for i := range toolCalls {
				ch <- StreamEvent{Type: "tool_call", Tool: &toolCalls[i]}
			}
			if done {
				usage = &Usage{
					InputTokens:  resp.PromptEvalCount,
					OutputTokens: resp.EvalCount,
				}
			}
			return nil
		})
		if err != nil {
			ch <- StreamEvent{Type: "error", Error: err.Error()}
			return
		}
		ch <- StreamEvent{Type: "done", Usage: usage}
	}()
	return ch, nil
}

// Pull downloads a model via the Ollama server.
// progressFn is called with percent completion (0–100); may be nil.
func (p *OllamaProvider) Pull(ctx context.Context, model string, progressFn func(pct float64)) error {
	req := &ollamaapi.PullRequest{Model: model}
	return p.client.Pull(ctx, req, func(resp ollamaapi.ProgressResponse) error {
		if progressFn != nil && resp.Total > 0 {
			progressFn(float64(resp.Completed) / float64(resp.Total) * 100)
		}
		return nil
	})
}

// ListModels returns the models available on the Ollama server.
func (p *OllamaProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := p.client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("ollama: list models: %w", err)
	}
	models := make([]ModelInfo, 0, len(resp.Models))
	for _, m := range resp.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		models = append(models, ModelInfo{ID: name, Name: name})
	}
	return models, nil
}

// Health checks whether the Ollama server is reachable.
func (p *OllamaProvider) Health(ctx context.Context) error {
	if err := p.client.Heartbeat(ctx); err != nil {
		return fmt.Errorf("ollama: health check: %w", err)
	}
	return nil
}
