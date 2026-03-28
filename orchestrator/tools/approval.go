package tools

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// ApprovalCreator is the interface needed by RequestApprovalTool to create approvals.
// This avoids a circular import with the ratchetplugin package.
type ApprovalCreator interface {
	CreateApproval(ctx context.Context, agentID, taskID, action, reason, details string) (string, error)
}

// RequestApprovalTool requests human approval before proceeding with a sensitive action.
type RequestApprovalTool struct {
	Manager ApprovalCreator
}

func (t *RequestApprovalTool) Name() string { return "request_approval" }
func (t *RequestApprovalTool) Description() string {
	return "Request human approval before proceeding with a sensitive action"
}

func (t *RequestApprovalTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":  map[string]any{"type": "string", "description": "The action requiring approval"},
				"reason":  map[string]any{"type": "string", "description": "Why this action needs approval"},
				"details": map[string]any{"type": "string", "description": "Additional details about the action"},
			},
			"required": []string{"action", "reason"},
		},
	}
}

func (t *RequestApprovalTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}
	reason, _ := args["reason"].(string)
	if reason == "" {
		return nil, fmt.Errorf("reason is required")
	}
	details, _ := args["details"].(string)

	agentID, _ := AgentIDFromContext(ctx)
	taskID, _ := TaskIDFromContext(ctx)

	if t.Manager == nil {
		return nil, fmt.Errorf("approval manager not available")
	}

	id, err := t.Manager.CreateApproval(ctx, agentID, taskID, action, reason, details)
	if err != nil {
		return nil, fmt.Errorf("create approval: %w", err)
	}

	return map[string]any{
		"approval_id": id,
		"status":      "pending",
		"action":      action,
		"reason":      reason,
		"message":     "Approval request submitted. Waiting for human review.",
	}, nil
}
