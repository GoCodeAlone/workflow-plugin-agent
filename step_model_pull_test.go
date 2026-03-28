package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/module"
)

// newOllamaMockServer returns a test server implementing minimal Ollama API endpoints.
// If hasModel is true, the tags endpoint returns the model (simulating already-available).
// Otherwise tags returns empty and pull returns success (simulating a fresh download).
func newOllamaMockServer(t *testing.T, modelName string, hasModel bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pull":
			w.Header().Set("Content-Type", "application/x-ndjson")
			resp := map[string]any{"status": "success"}
			b, _ := json.Marshal(resp)
			fmt.Fprintf(w, "%s\n", b)
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			var models []map[string]any
			if hasModel {
				models = []map[string]any{
					{"name": modelName, "model": modelName, "size": 1234567},
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestModelPullStep_Ollama_Downloaded(t *testing.T) {
	// Model not in tags → should be pulled → "downloaded"
	srv := newOllamaMockServer(t, "qwen3.5:7b", false)
	defer srv.Close()

	step := &ModelPullStep{
		name:       "test-pull",
		source:     "ollama",
		model:      "qwen3.5:7b",
		ollamaBase: srv.URL,
	}

	result, err := step.Execute(context.Background(), &module.PipelineContext{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output["status"] != "downloaded" {
		t.Errorf("status: want %q, got %q", "downloaded", result.Output["status"])
	}
	if result.Output["model_path"] != "qwen3.5:7b" {
		t.Errorf("model_path: want %q, got %q", "qwen3.5:7b", result.Output["model_path"])
	}
}

func TestModelPullStep_Ollama_AlreadyReady(t *testing.T) {
	// Model already in tags → "ready", no pull needed
	srv := newOllamaMockServer(t, "qwen3.5:7b", true)
	defer srv.Close()

	step := &ModelPullStep{
		name:       "test-pull-ready",
		source:     "ollama",
		model:      "qwen3.5:7b",
		ollamaBase: srv.URL,
	}

	result, err := step.Execute(context.Background(), &module.PipelineContext{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output["status"] != "ready" {
		t.Errorf("status: want %q, got %q", "ready", result.Output["status"])
	}
}

func TestModelPullStep_HuggingFace(t *testing.T) {
	content := []byte("GGUF model content here")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	origURL := provider.HuggingFaceBaseURL()
	provider.SetHuggingFaceBaseURL(srv.URL)
	defer provider.SetHuggingFaceBaseURL(origURL)

	outDir := t.TempDir()
	step := &ModelPullStep{
		name:      "test-hf-pull",
		source:    "huggingface",
		model:     "org/mymodel",
		file:      "model.gguf",
		outputDir: outDir,
	}

	result, err := step.Execute(context.Background(), &module.PipelineContext{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output["status"] != "downloaded" {
		t.Errorf("status: want %q, got %q", "downloaded", result.Output["status"])
	}
	if result.Output["model_path"] == "" {
		t.Error("expected non-empty model_path")
	}
	if result.Output["size_bytes"].(int64) != int64(len(content)) {
		t.Errorf("size_bytes: want %d, got %v", len(content), result.Output["size_bytes"])
	}
}

func TestModelPullStep_UnknownSource(t *testing.T) {
	step := &ModelPullStep{
		name:   "test-unknown",
		source: "unknown-provider",
		model:  "some-model",
	}

	result, err := step.Execute(context.Background(), &module.PipelineContext{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output["status"] != "error" {
		t.Errorf("status: want %q, got %q", "error", result.Output["status"])
	}
}

func TestNewModelPullStepFactory_Config(t *testing.T) {
	factory := newModelPullStepFactory()
	raw, err := factory("pull-step", map[string]any{
		"provider":   "ollama",
		"model":      "llama3:8b",
		"base_url":   "http://localhost:11434",
	}, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	step, ok := raw.(*ModelPullStep)
	if !ok {
		t.Fatalf("expected *ModelPullStep, got %T", raw)
	}
	if step.source != "ollama" {
		t.Errorf("source: want %q, got %q", "ollama", step.source)
	}
	if step.model != "llama3:8b" {
		t.Errorf("model: want %q, got %q", "llama3:8b", step.model)
	}
	if step.ollamaBase != "http://localhost:11434" {
		t.Errorf("ollamaBase: want %q, got %q", "http://localhost:11434", step.ollamaBase)
	}
}
