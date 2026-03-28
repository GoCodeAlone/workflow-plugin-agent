package provider

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

const (
	defaultLlamaCppPort        = 8081
	defaultLlamaCppGPULayers   = -1
	defaultLlamaCppContextSize = 8192
	defaultLlamaCppMaxTokens   = 4096
)

// LlamaCppConfig holds configuration for the LlamaCpp provider.
// Set BaseURL for external mode (any OpenAI-compatible server).
// Set ModelPath for managed mode (provider starts llama-server).
type LlamaCppConfig struct {
	BaseURL     string // external mode: OpenAI-compatible server URL
	ModelPath   string // managed mode: path to .gguf model file
	ModelName   string // model name sent to server (external mode); defaults to "local"
	BinaryPath  string // override llama-server binary location
	GPULayers   int    // -ngl flag; 0 → default -1 (all layers)
	ContextSize int    // -c flag; default 8192
	Threads     int    // -t flag; default runtime.NumCPU()
	Port        int    // server port; default 8081
	MaxTokens   int    // default 4096
	HTTPClient  *http.Client
}

// LlamaCppProvider implements Provider using an OpenAI-compatible llama-server.
type LlamaCppProvider struct {
	client openaisdk.Client
	config LlamaCppConfig
	cmd    *exec.Cmd // non-nil in managed mode after server start
}

// NewLlamaCppProvider creates a LlamaCppProvider with the given config.
// In external mode (BaseURL set), it points at the given URL.
// In managed mode (ModelPath set), call ensureServer before use.
func NewLlamaCppProvider(cfg LlamaCppConfig) *LlamaCppProvider {
	if cfg.GPULayers == 0 {
		cfg.GPULayers = defaultLlamaCppGPULayers
	}
	if cfg.ContextSize <= 0 {
		cfg.ContextSize = defaultLlamaCppContextSize
	}
	if cfg.Threads <= 0 {
		cfg.Threads = runtime.NumCPU()
	}
	if cfg.Port <= 0 {
		cfg.Port = defaultLlamaCppPort
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultLlamaCppMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%d/v1", cfg.Port)
	}

	client := openaisdk.NewClient(
		option.WithAPIKey("no-key"),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(cfg.HTTPClient),
	)
	return &LlamaCppProvider{client: client, config: cfg}
}

func (p *LlamaCppProvider) Name() string { return "llama_cpp" }

// modelName returns the model identifier to send to the server.
func (p *LlamaCppProvider) modelName() string {
	if p.config.ModelName != "" {
		return p.config.ModelName
	}
	return "local"
}

func (p *LlamaCppProvider) AuthModeInfo() AuthModeInfo {
	return LocalAuthMode("llama_cpp", "llama.cpp (Local)")
}

// Chat sends a non-streaming request and applies ParseThinking to the response.
func (p *LlamaCppProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	params := openaisdk.ChatCompletionNewParams{
		Messages:  toOpenAIMessages(messages),
		MaxTokens: openaisdk.Int(int64(p.config.MaxTokens)),
	}
	if len(tools) > 0 {
		params.Tools = toOpenAITools(tools)
	}
	params.Model = shared.ChatModel(p.modelName())

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("llama_cpp: %w", err)
	}
	result, err := fromOpenAIResponse(resp)
	if err != nil {
		return nil, err
	}
	result.Thinking, result.Content = ParseThinking(result.Content)
	return result, nil
}

// Stream sends a streaming request, applying ThinkingStreamParser to text events.
func (p *LlamaCppProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	params := openaisdk.ChatCompletionNewParams{
		Messages:  toOpenAIMessages(messages),
		MaxTokens: openaisdk.Int(int64(p.config.MaxTokens)),
	}
	if len(tools) > 0 {
		params.Tools = toOpenAITools(tools)
	}
	params.Model = shared.ChatModel(p.modelName())

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	rawCh := make(chan StreamEvent, 16)
	go streamOpenAIEvents(stream, rawCh)

	outCh := make(chan StreamEvent, 16)
	go func() {
		defer close(outCh)
		parser := &ThinkingStreamParser{}
		for event := range rawCh {
			if event.Type == "text" {
				for _, e := range parser.Feed(event.Text) {
					outCh <- e
				}
			} else {
				outCh <- event
			}
		}
	}()
	return outCh, nil
}

// EnsureServer starts the managed llama-server if ModelPath is configured.
// No-op in external mode. Must be called before Chat/Stream in managed mode.
func (p *LlamaCppProvider) EnsureServer(ctx context.Context) error {
	if p.config.ModelPath == "" {
		return nil // external mode, nothing to start
	}
	return p.ensureServer(ctx)
}

func (p *LlamaCppProvider) ensureServer(ctx context.Context) error {
	binPath := p.config.BinaryPath
	if binPath == "" {
		if path, err := exec.LookPath("llama-server"); err == nil {
			binPath = path
		} else {
			var dlErr error
			binPath, dlErr = EnsureLlamaServer(ctx)
			if dlErr != nil {
				return fmt.Errorf("llama_cpp: find llama-server: %w", dlErr)
			}
		}
	}

	args := []string{
		"--model", p.config.ModelPath,
		"--port", fmt.Sprintf("%d", p.config.Port),
		"-ngl", fmt.Sprintf("%d", p.config.GPULayers),
		"-c", fmt.Sprintf("%d", p.config.ContextSize),
		"-t", fmt.Sprintf("%d", p.config.Threads),
	}

	// #nosec G204 -- BinaryPath/binPath comes from config or PATH lookup, not user input.
	cmd := exec.Command(binPath, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("llama_cpp: start llama-server: %w", err)
	}
	p.cmd = cmd

	healthURL := fmt.Sprintf("http://localhost:%d/health", p.config.Port)
	deadline := time.Now().Add(2 * time.Minute)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				_ = p.cmd.Process.Kill()
				_ = p.cmd.Wait()
				return fmt.Errorf("llama_cpp: llama-server did not become healthy within timeout")
			}
			hReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
			resp, err := p.config.HTTPClient.Do(hReq)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

// Close kills the managed llama-server process if one was started
// and waits for it to exit to avoid zombie processes.
func (p *LlamaCppProvider) Close() error {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait() // reap the child process
	}
	return nil
}
