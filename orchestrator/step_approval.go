package orchestrator

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// ApprovalResolveStep handles POST /api/approvals/:id/approve and /api/approvals/:id/reject.
type ApprovalResolveStep struct {
	name   string
	action string // "approve" or "reject"
	app    modular.Application
	tmpl   *module.TemplateEngine
}

func (s *ApprovalResolveStep) Name() string { return s.name }

func (s *ApprovalResolveStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	// Lazy-lookup ApprovalManager
	var am *ApprovalManager
	if svc, ok := s.app.SvcRegistry()["ratchet-approval-manager"]; ok {
		am, _ = svc.(*ApprovalManager)
	}
	if am == nil {
		return nil, fmt.Errorf("approval_resolve step %q: approval manager not available", s.name)
	}

	// Resolve approval ID from path params or current data
	approvalID := extractString(pc.Current, "id", "")
	if approvalID == "" {
		// Try path_params
		if pp, ok := pc.Current["path_params"].(map[string]any); ok {
			approvalID, _ = pp["id"].(string)
		}
	}
	if approvalID == "" {
		return nil, fmt.Errorf("approval_resolve step %q: approval id is required", s.name)
	}

	// Resolve reviewer comment from body (optional)
	comment := extractString(pc.Current, "comment", "")
	if body, ok := pc.Current["body"].(map[string]any); ok {
		if c, ok := body["comment"].(string); ok && c != "" {
			comment = c
		}
	}

	// Resolve action: prefer config, fall back to pipeline data
	action := s.action
	if action == "" {
		action = extractString(pc.Current, "action", "")
	}

	switch action {
	case "approve":
		if err := am.Approve(ctx, approvalID, comment); err != nil {
			return nil, fmt.Errorf("approval_resolve step %q: %w", s.name, err)
		}
	case "reject":
		if err := am.Reject(ctx, approvalID, comment); err != nil {
			return nil, fmt.Errorf("approval_resolve step %q: %w", s.name, err)
		}
	default:
		return nil, fmt.Errorf("approval_resolve step %q: unknown action %q (want approve|reject)", s.name, action)
	}

	return &module.StepResult{
		Output: map[string]any{
			"id":      approvalID,
			"action":  action,
			"comment": comment,
			"success": true,
		},
	}, nil
}

// newApprovalResolveFactory returns a plugin.StepFactory for "step.approval_resolve".
func newApprovalResolveFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		action, _ := cfg["action"].(string)
		return &ApprovalResolveStep{
			name:   name,
			action: action,
			app:    app,
			tmpl:   module.NewTemplateEngine(),
		}, nil
	}
}
