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
	// HumanRequestResolveStep is REQUIRED-STATEFUL on HumanRequestService.
	svcs := resolveServices(s.app)
	hrm := svcs.HumanRequest
	if IsNull(hrm) {
		return nil, fmt.Errorf("human_request_resolve step %q: %w", s.name, ErrServiceUnavailable)
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
		// auto-store the value in the secretService composite's provider.
		s.autoStoreSecret(ctx, svcs.SecretGuard, hrm, requestID, responseData)

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
// and if so, stores the provided value via the secretService composite's Provider
// and arms the Redactor so the just-stored token is redacted on the next pass (D3).
//
// The composite is TRULY-OPTIONAL: absence is a nil pointer (D5/D13 — concrete,
// not a Null default), so we nil-check Provider() rather than IsNull.
func (s *HumanRequestResolveStep) autoStoreSecret(ctx context.Context, guard *secretService, hrm HumanRequestService, requestID, responseData string) {
	if guard == nil || responseData == "" {
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

	// secretService is TRULY-OPTIONAL: skip auto-store when its provider is
	// unresolved (nil). D5 nil-check on the accessor path.
	if sp := guard.Provider(); sp == nil {
		return
	}
	if err := guard.Provider().Set(ctx, secretName, value); err != nil {
		fmt.Printf("ratchetplugin: failed to store secret %q: %v\n", secretName, err)
		return
	}
	// D3: arm the Redactor with the just-stored token so the next Redact masks
	// it. Dropping this call would silently leak the token in the next
	// redaction pass (the failure class the redactor exists to prevent).
	guard.Redactor().AddValue(secretName, value)
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
