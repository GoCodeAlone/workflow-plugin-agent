package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// HumanRequestCreator is the interface needed by RequestHumanTool to create requests.
type HumanRequestCreator interface {
	CreateRequest(ctx context.Context, agentID, taskID, projectID, reqType, title, desc, urgency, metadata string) (string, error)
}

// HumanRequestChecker is the interface needed by CheckHumanRequestTool to check request status.
type HumanRequestChecker interface {
	GetRequest(ctx context.Context, id string) (map[string]any, error)
}

// RequestHumanTool allows agents to request something from the human operator.
type RequestHumanTool struct {
	Manager HumanRequestCreator
}

func (t *RequestHumanTool) Name() string { return "request_human" }
func (t *RequestHumanTool) Description() string {
	return "Request something from the human operator (tokens, tool installation, access, information). Creates a pending request that the human will see and respond to."
}

func (t *RequestHumanTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_type": map[string]any{
					"type":        "string",
					"enum":        []string{"token", "binary", "access", "info", "custom"},
					"description": "Category of request: 'token' for API keys/PATs, 'binary' for CLI tools, 'access' for service access, 'info' for clarification, 'custom' for anything else",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Short summary of what you need, e.g. 'Need GitHub PAT for repo X'",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Detailed explanation of what you need and why",
				},
				"urgency": map[string]any{
					"type":        "string",
					"enum":        []string{"low", "normal", "high", "critical"},
					"description": "How urgently this is needed (default: normal)",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Extra context hints, e.g. {\"secret_name\": \"GITHUB_TOKEN\"} to auto-store the provided value",
				},
				"blocking": map[string]any{
					"type":        "boolean",
					"description": "If true, the agent will pause and wait for the human to respond before continuing. Default false.",
				},
			},
			"required": []string{"request_type", "title"},
		},
	}
}

func isValidRequestType(reqType string) bool {
	switch reqType {
	case "token", "binary", "access", "info", "custom":
		return true
	default:
		return false
	}
}

func isValidUrgency(urgency string) bool {
	switch urgency {
	case "low", "normal", "high", "critical":
		return true
	default:
		return false
	}
}

func (t *RequestHumanTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	reqType, _ := args["request_type"].(string)
	if reqType == "" {
		return nil, fmt.Errorf("request_type is required")
	}
	if !isValidRequestType(reqType) {
		return nil, fmt.Errorf("invalid request_type: %s", reqType)
	}

	title, _ := args["title"].(string)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}

	description, _ := args["description"].(string)
	urgency, _ := args["urgency"].(string)
	if urgency == "" {
		urgency = "normal"
	}
	if !isValidUrgency(urgency) {
		return nil, fmt.Errorf("invalid urgency: %s", urgency)
	}

	// Serialize metadata to JSON string
	metadataStr := "{}"
	if meta, ok := args["metadata"]; ok && meta != nil {
		if metaBytes, err := json.Marshal(meta); err == nil {
			metadataStr = string(metaBytes)
		}
	}

	agentID, _ := AgentIDFromContext(ctx)
	taskID, _ := TaskIDFromContext(ctx)
	projectID, _ := ProjectIDFromContext(ctx)

	if t.Manager == nil {
		return nil, fmt.Errorf("human request manager not available")
	}

	id, err := t.Manager.CreateRequest(ctx, agentID, taskID, projectID, reqType, title, description, urgency, metadataStr)
	if err != nil {
		return nil, fmt.Errorf("create human request: %w", err)
	}

	blocking, _ := args["blocking"].(bool)

	return map[string]any{
		"request_id":   id,
		"status":       "pending",
		"request_type": reqType,
		"title":        title,
		"blocking":     blocking,
		"message":      "Request submitted. The human operator will be notified.",
	}, nil
}

// CheckHumanRequestTool checks the status of a previously created human request.
type CheckHumanRequestTool struct {
	Manager HumanRequestChecker
}

func (t *CheckHumanRequestTool) Name() string { return "check_human_request" }
func (t *CheckHumanRequestTool) Description() string {
	return "Check the status of a previously created human request to see if the human has responded"
}

func (t *CheckHumanRequestTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": map[string]any{
					"type":        "string",
					"description": "The ID of the request to check",
				},
			},
			"required": []string{"request_id"},
		},
	}
}

func (t *CheckHumanRequestTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	requestID, _ := args["request_id"].(string)
	if requestID == "" {
		return nil, fmt.Errorf("request_id is required")
	}

	if t.Manager == nil {
		return nil, fmt.Errorf("human request manager not available")
	}

	req, err := t.Manager.GetRequest(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("get human request: %w", err)
	}

	// Redact sensitive data for token-type requests so secrets are not exposed to the agent/LLM.
	if reqType, ok := req["request_type"].(string); ok && reqType == "token" {
		delete(req, "response_data")
	}

	return req, nil
}
