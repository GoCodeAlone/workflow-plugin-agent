package agent

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// ProviderModelsStep fetches available models from a provider's API.
type ProviderModelsStep struct {
	name string
	app  modular.Application
}

func (s *ProviderModelsStep) Name() string { return s.name }

func (s *ProviderModelsStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	body := extractBody(pc)
	providerType := extractString(body, "type", "")
	apiKey := extractString(body, "api_key", "")
	baseURL := extractString(body, "base_url", "")

	if providerType == "" {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "provider type is required",
				"models":  []any{},
			},
		}, nil
	}

	models, err := provider.ListModels(ctx, providerType, apiKey, baseURL)
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   fmt.Sprintf("failed to fetch models: %v", err),
				"models":  []any{},
			},
		}, nil
	}

	modelList := make([]any, len(models))
	for i, m := range models {
		modelList[i] = map[string]any{
			"id":   m.ID,
			"name": m.Name,
		}
		if m.ContextWindow > 0 {
			modelList[i].(map[string]any)["context_window"] = m.ContextWindow
		}
	}

	return &module.StepResult{
		Output: map[string]any{
			"success": true,
			"models":  modelList,
		},
	}, nil
}

func newProviderModelsFactory() plugin.StepFactory {
	return func(name string, _ map[string]any, app modular.Application) (any, error) {
		return &ProviderModelsStep{
			name: name,
			app:  app,
		}, nil
	}
}

// extractBody extracts the request body from the pipeline context.
func extractBody(pc *module.PipelineContext) map[string]any {
	if body, ok := pc.Current["body"].(map[string]any); ok {
		return body
	}
	return pc.Current
}
