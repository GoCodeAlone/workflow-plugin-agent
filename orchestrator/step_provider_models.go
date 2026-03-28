package orchestrator

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/module"
)

// ProviderModelsStep fetches available models from a provider's API.
// Requires provider type and API key in the request body.
type ProviderModelsStep struct {
	name string
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

	// Convert to []any for JSON serialization
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
