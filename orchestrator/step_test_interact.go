package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// TestInteractStep is a pipeline step that bridges HTTP requests to an HTTPSource.
// It supports three operations: list_pending, get_interaction, and respond.
type TestInteractStep struct {
	name      string
	operation string
	app       modular.Application
}

func (s *TestInteractStep) Name() string { return s.name }

func (s *TestInteractStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	// Look up the HTTPSource from the service registry
	svc, ok := s.app.SvcRegistry()["ratchet-test-http-source"]
	if !ok {
		return &module.StepResult{
			Output: map[string]any{
				"error":   "test provider not configured in HTTP mode",
				"success": false,
			},
		}, nil
	}
	httpSource, ok := svc.(*HTTPSource)
	if !ok {
		return &module.StepResult{
			Output: map[string]any{
				"error":   "ratchet-test-http-source is not an *HTTPSource",
				"success": false,
			},
		}, nil
	}

	switch s.operation {
	case "list_pending":
		return s.listPending(httpSource)
	case "get_interaction":
		return s.getInteraction(httpSource, pc)
	case "respond":
		return s.respond(httpSource, pc)
	default:
		return nil, fmt.Errorf("test_interact step %q: unknown operation %q", s.name, s.operation)
	}
}

func (s *TestInteractStep) listPending(source *HTTPSource) (*module.StepResult, error) {
	summaries := source.ListPending()
	return &module.StepResult{
		Output: map[string]any{
			"interactions": summaries,
			"count":        len(summaries),
			"success":      true,
		},
	}, nil
}

func (s *TestInteractStep) getInteraction(source *HTTPSource, pc *module.PipelineContext) (*module.StepResult, error) {
	id := extractInteractionID(pc)
	if id == "" {
		return &module.StepResult{
			Output: map[string]any{
				"error":   "interaction id is required",
				"success": false,
			},
		}, nil
	}

	interaction, err := source.GetInteraction(id)
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"error":   err.Error(),
				"success": false,
			},
		}, nil
	}

	return &module.StepResult{
		Output: map[string]any{
			"interaction": interaction,
			"success":     true,
		},
	}, nil
}

func (s *TestInteractStep) respond(source *HTTPSource, pc *module.PipelineContext) (*module.StepResult, error) {
	id := extractInteractionID(pc)
	if id == "" {
		return &module.StepResult{
			Output: map[string]any{
				"error":   "interaction id is required",
				"success": false,
			},
		}, nil
	}

	// Parse response body
	var resp InteractionResponse
	if body, ok := pc.Current["body"].(map[string]any); ok {
		if c, ok := body["content"].(string); ok {
			resp.Content = c
		}
		if e, ok := body["error"].(string); ok {
			resp.Error = e
		}
		if tcs, ok := body["tool_calls"]; ok {
			// Re-marshal and unmarshal to handle tool_calls properly
			tcBytes, _ := json.Marshal(tcs)
			_ = json.Unmarshal(tcBytes, &resp.ToolCalls)
		}
	} else if req, ok := pc.Current["request"].(*http.Request); ok {
		// Fall back to reading the request body directly
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			_ = json.Unmarshal(bodyBytes, &resp)
		}
	}

	if err := source.Respond(id, resp); err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"error":   err.Error(),
				"success": false,
			},
		}, nil
	}

	return &module.StepResult{
		Output: map[string]any{
			"id":      id,
			"success": true,
		},
	}, nil
}

// extractInteractionID extracts the interaction ID from path params or current data.
func extractInteractionID(pc *module.PipelineContext) string {
	// Check path_params first (from HTTP router)
	if pp, ok := pc.Current["path_params"].(map[string]any); ok {
		if id, ok := pp["id"].(string); ok && id != "" {
			return id
		}
	}
	// Fall back to direct "id" field
	return extractString(pc.Current, "id", "")
}

// newTestInteractFactory returns a plugin.StepFactory for "step.test_interact".
func newTestInteractFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		operation, _ := cfg["operation"].(string)
		if operation == "" {
			return nil, fmt.Errorf("test_interact step %q: operation is required", name)
		}
		return &TestInteractStep{
			name:      name,
			operation: operation,
			app:       app,
		}, nil
	}
}
