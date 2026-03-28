package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// HumanRequestResolveStep handles POST /api/requests/:id/resolve and /api/requests/:id/cancel.
type HumanRequestResolveStep struct {
	name   string
	action string // "resolve" or "cancel"
	app    modular.Application
	tmpl   *module.TemplateEngine
}

func (s *HumanRequestResolveStep) Name() string { return s.name }

func (s *HumanRequestResolveStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	var hrm *HumanRequestManager
	if svc, ok := s.app.SvcRegistry()["ratchet-human-request-manager"]; ok {
		hrm, _ = svc.(*HumanRequestManager)
	}
	if hrm == nil {
		return nil, fmt.Errorf("human_request_resolve step %q: human request manager not available", s.name)
	}

	// Resolve request ID from path params or current data
	requestID := extractString(pc.Current, "id", "")
	if requestID == "" {
		if pp, ok := pc.Current["path_params"].(map[string]any); ok {
			requestID, _ = pp["id"].(string)
		}
	}
	if requestID == "" {
		return nil, fmt.Errorf("human_request_resolve step %q: request id is required", s.name)
	}

	action := s.action
	if action == "" {
		action = extractString(pc.Current, "action", "")
	}

	switch action {
	case "resolve":
		// Extract response_data and comment from body
		responseData := ""
		comment := ""
		resolvedBy := "human"

		if body, ok := pc.Current["body"].(map[string]any); ok {
			if rd, ok := body["response_data"]; ok {
				if rdBytes, err := json.Marshal(rd); err == nil {
					responseData = string(rdBytes)
				}
			}
			if c, ok := body["comment"].(string); ok {
				comment = c
			}
			if rb, ok := body["resolved_by"].(string); ok && rb != "" {
				resolvedBy = rb
			}
		}

		if err := hrm.Resolve(ctx, requestID, responseData, comment, resolvedBy); err != nil {
			return nil, fmt.Errorf("human_request_resolve step %q: %w", s.name, err)
		}

		// If the request was for a token and metadata specifies a secret_name,
		// auto-store the value in the SecretGuard.
		s.autoStoreSecret(ctx, hrm, requestID, responseData)

		return &module.StepResult{
			Output: map[string]any{
				"id":      requestID,
				"action":  "resolve",
				"success": true,
			},
		}, nil

	case "cancel":
		comment := ""
		if body, ok := pc.Current["body"].(map[string]any); ok {
			if c, ok := body["comment"].(string); ok {
				comment = c
			}
		}

		if err := hrm.Cancel(ctx, requestID, comment); err != nil {
			return nil, fmt.Errorf("human_request_resolve step %q: %w", s.name, err)
		}

		return &module.StepResult{
			Output: map[string]any{
				"id":      requestID,
				"action":  "cancel",
				"success": true,
			},
		}, nil

	default:
		return nil, fmt.Errorf("human_request_resolve step %q: unknown action %q (want resolve|cancel)", s.name, action)
	}
}

// autoStoreSecret checks if a resolved token request has a secret_name in its metadata,
// and if so, stores the provided value in the SecretGuard.
func (s *HumanRequestResolveStep) autoStoreSecret(ctx context.Context, hrm *HumanRequestManager, requestID, responseData string) {
	if responseData == "" {
		return
	}

	req, err := hrm.Get(ctx, requestID)
	if err != nil || req == nil {
		return
	}
	if req.RequestType != RequestTypeToken {
		return
	}

	var meta map[string]any
	if jsonErr := json.Unmarshal([]byte(req.Metadata), &meta); jsonErr != nil {
		return
	}
	secretName, ok := meta["secret_name"].(string)
	if !ok || secretName == "" {
		return
	}

	var respData map[string]any
	if jsonErr := json.Unmarshal([]byte(responseData), &respData); jsonErr != nil {
		return
	}
	value, ok := respData["value"].(string)
	if !ok || value == "" {
		return
	}

	if svc, ok := s.app.SvcRegistry()["ratchet-secret-guard"]; ok {
		if guard, ok := svc.(*SecretGuard); ok {
			if sp := guard.Provider(); sp != nil {
				if err := sp.Set(ctx, secretName, value); err != nil {
					fmt.Printf("ratchetplugin: failed to store secret %q: %v\n", secretName, err)
					return
				}
				guard.AddKnownSecret(secretName, value)
			}
		}
	}
}

// newHumanRequestResolveFactory returns a plugin.StepFactory for "step.human_request_resolve".
func newHumanRequestResolveFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		action, _ := cfg["action"].(string)
		return &HumanRequestResolveStep{
			name:   name,
			action: action,
			app:    app,
			tmpl:   module.NewTemplateEngine(),
		}, nil
	}
}
