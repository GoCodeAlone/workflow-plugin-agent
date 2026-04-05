package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	ollamaapi "github.com/ollama/ollama/api"
)

const defaultOllamaClientURL = "http://localhost:11434"

// OllamaClient provides utility operations against a local Ollama server:
// pulling models and listing available models. For chat/stream, use the
// Genkit-backed provider returned by genkit.NewOllamaProvider.
type OllamaClient struct {
	client *ollamaapi.Client
}

// NewOllamaClient creates an OllamaClient pointing at the given server address.
// If serverAddress is empty, http://localhost:11434 is used.
func NewOllamaClient(serverAddress string) *OllamaClient {
	if serverAddress == "" {
		serverAddress = defaultOllamaClientURL
	}
	base, err := url.Parse(serverAddress)
	if err != nil {
		base, _ = url.Parse(defaultOllamaClientURL)
	}
	return &OllamaClient{
		client: ollamaapi.NewClient(base, http.DefaultClient),
	}
}

// Pull downloads a model via the Ollama server.
// progressFn is called with percent completion (0–100); may be nil.
func (c *OllamaClient) Pull(ctx context.Context, model string, progressFn func(pct float64)) error {
	req := &ollamaapi.PullRequest{Model: model}
	return c.client.Pull(ctx, req, func(resp ollamaapi.ProgressResponse) error {
		if progressFn != nil && resp.Total > 0 {
			progressFn(float64(resp.Completed) / float64(resp.Total) * 100)
		}
		return nil
	})
}

// ListModels returns the models available on the Ollama server.
func (c *OllamaClient) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := c.client.List(ctx)
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
func (c *OllamaClient) Health(ctx context.Context) error {
	if err := c.client.Heartbeat(ctx); err != nil {
		return fmt.Errorf("ollama: health check: %w", err)
	}
	return nil
}
