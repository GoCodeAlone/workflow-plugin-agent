package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// ModelPullStep ensures a model is available before agent execution.
// For "ollama" source it pulls from an Ollama server; for "huggingface"
// it downloads from HuggingFace Hub.
type ModelPullStep struct {
	name      string
	source    string // "ollama" or "huggingface"
	model     string // model name / HF repo (e.g. "org/repo")
	file      string // HuggingFace file name within the repo
	outputDir string // HuggingFace local destination; defaults to ~/.cache/workflow/models
	ollamaBase string // Ollama server base URL; defaults to http://localhost:11434
}

func (s *ModelPullStep) Name() string { return s.name }

func (s *ModelPullStep) Execute(ctx context.Context, _ *module.PipelineContext) (*module.StepResult, error) {
	switch s.source {
	case "ollama":
		return s.pullOllama(ctx)
	case "huggingface":
		return s.pullHuggingFace(ctx)
	default:
		return &module.StepResult{
			Output: map[string]any{
				"status":    "error",
				"model_path": "",
				"size_bytes": 0,
				"error":     fmt.Sprintf("unknown source %q: must be \"ollama\" or \"huggingface\"", s.source),
			},
		}, nil
	}
}

func (s *ModelPullStep) pullOllama(ctx context.Context) (*module.StepResult, error) {
	client := provider.NewOllamaClient(s.ollamaBase)

	// Check if model is already available.
	models, err := client.ListModels(ctx)
	if err == nil {
		for _, m := range models {
			if m.ID == s.model || m.Name == s.model {
				return &module.StepResult{
					Output: map[string]any{
						"status":     "ready",
						"model_path": s.model,
						"size_bytes": 0,
					},
				}, nil
			}
		}
	}

	// Pull (download) the model.
	if pullErr := client.Pull(ctx, s.model, nil); pullErr != nil {
		return &module.StepResult{
			Output: map[string]any{
				"status":     "error",
				"model_path": "",
				"size_bytes": 0,
				"error":      pullErr.Error(),
			},
		}, nil
	}

	return &module.StepResult{
		Output: map[string]any{
			"status":     "downloaded",
			"model_path": s.model,
			"size_bytes": 0,
		},
	}, nil
}

func (s *ModelPullStep) pullHuggingFace(ctx context.Context) (*module.StepResult, error) {
	path, err := provider.DownloadHuggingFaceFile(ctx, s.model, s.file, s.outputDir, nil)
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"status":     "error",
				"model_path": "",
				"size_bytes": 0,
				"error":      err.Error(),
			},
		}, nil
	}

	// Determine whether the file was already present by checking mtime vs request timing.
	// DownloadHuggingFaceFile returns early (skip) when the file already exists — we detect
	// this by statting and checking size, which is simpler than an extra timestamp check.
	fi, statErr := os.Stat(path)
	var sizeBytes int64
	if statErr == nil {
		sizeBytes = fi.Size()
	}

	// DownloadHuggingFaceFile skips downloading when the file exists; we call it "ready".
	// Without introspecting the function internals, we conservatively report "downloaded".
	// A future optimisation could pass a flag via context or return value.
	return &module.StepResult{
		Output: map[string]any{
			"status":     "downloaded",
			"model_path": path,
			"size_bytes": sizeBytes,
		},
	}, nil
}

// newModelPullStepFactory returns a plugin.StepFactory for "step.model_pull".
func newModelPullStepFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, _ modular.Application) (any, error) {
		source, _ := cfg["provider"].(string) // "ollama" or "huggingface"
		if source == "" {
			source, _ = cfg["source"].(string)
		}
		model, _ := cfg["model"].(string)
		file, _ := cfg["file"].(string)
		outputDir, _ := cfg["output_dir"].(string)
		ollamaBase, _ := cfg["base_url"].(string)

		return &ModelPullStep{
			name:       name,
			source:     source,
			model:      model,
			file:       file,
			outputDir:  outputDir,
			ollamaBase: ollamaBase,
		}, nil
	}
}
