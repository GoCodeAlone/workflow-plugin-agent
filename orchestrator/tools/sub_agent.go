package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// SubAgentSpawner is the interface the sub-agent tools use to spawn/check/wait tasks.
// It is satisfied by *ratchetplugin.SubAgentManager.
type SubAgentSpawner interface {
	Spawn(ctx context.Context, parentAgentID string, name string, taskDesc string, systemPrompt string) (taskID string, err error)
	CheckTask(ctx context.Context, taskID string) (status string, result string, err error)
	WaitTasks(ctx context.Context, taskIDs []string, timeout time.Duration) (map[string]SubTaskResult, error)
}

// SubTaskResult mirrors the type in ratchetplugin to avoid an import cycle.
type SubTaskResult struct {
	TaskID string
	Status string
	Result string
	Error  string
}

// AgentSpawnTool spawns an ephemeral sub-agent to handle a delegated task.
type AgentSpawnTool struct {
	Manager SubAgentSpawner
}

func (t *AgentSpawnTool) Name() string { return "agent_spawn" }
func (t *AgentSpawnTool) Description() string {
	return "Spawn an ephemeral sub-agent to handle a delegated task in parallel"
}
func (t *AgentSpawnTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "A short name for the sub-agent and its task",
				},
				"task_description": map[string]any{
					"type":        "string",
					"description": "The task to delegate to the sub-agent",
				},
				"system_prompt": map[string]any{
					"type":        "string",
					"description": "Optional system prompt for the sub-agent personality",
				},
			},
			"required": []string{"name", "task_description"},
		},
	}
}

func (t *AgentSpawnTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	taskDesc, _ := args["task_description"].(string)
	systemPrompt, _ := args["system_prompt"].(string)

	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if taskDesc == "" {
		return nil, fmt.Errorf("task_description is required")
	}
	if systemPrompt == "" {
		systemPrompt = "You are a helpful AI sub-agent. Complete the assigned task and report your findings."
	}

	parentAgentID, ok := AgentIDFromContext(ctx)
	if !ok || parentAgentID == "" {
		return nil, fmt.Errorf("agent_spawn: no parent agent ID in context")
	}

	taskID, err := t.Manager.Spawn(ctx, parentAgentID, name, taskDesc, systemPrompt)
	if err != nil {
		return nil, fmt.Errorf("agent_spawn: %w", err)
	}

	return map[string]any{
		"task_id": taskID,
		"name":    name,
		"status":  "spawned",
	}, nil
}

// AgentCheckTool checks the status of a previously spawned sub-agent task.
type AgentCheckTool struct {
	Manager SubAgentSpawner
}

func (t *AgentCheckTool) Name() string        { return "agent_check" }
func (t *AgentCheckTool) Description() string { return "Check the status of a spawned sub-agent task" }
func (t *AgentCheckTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The task ID returned by agent_spawn",
				},
			},
			"required": []string{"task_id"},
		},
	}
}

func (t *AgentCheckTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}

	status, result, err := t.Manager.CheckTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("agent_check: %w", err)
	}

	return map[string]any{
		"task_id": taskID,
		"status":  status,
		"result":  result,
	}, nil
}

// AgentWaitTool waits for one or more spawned sub-agent tasks to complete.
type AgentWaitTool struct {
	Manager SubAgentSpawner
}

func (t *AgentWaitTool) Name() string { return "agent_wait" }
func (t *AgentWaitTool) Description() string {
	return "Wait for one or more spawned sub-agent tasks to complete"
}
func (t *AgentWaitTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "List of task IDs to wait for",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Maximum seconds to wait (default 300)",
				},
			},
			"required": []string{"task_ids"},
		},
	}
}

func (t *AgentWaitTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	rawIDs, _ := args["task_ids"].([]any)
	if len(rawIDs) == 0 {
		return nil, fmt.Errorf("task_ids must be a non-empty array")
	}

	taskIDs := make([]string, 0, len(rawIDs))
	for _, v := range rawIDs {
		if s, ok := v.(string); ok && s != "" {
			taskIDs = append(taskIDs, s)
		}
	}
	if len(taskIDs) == 0 {
		return nil, fmt.Errorf("task_ids must contain at least one valid string ID")
	}

	timeout := 300 * time.Second
	if v, ok := args["timeout_seconds"].(float64); ok && v > 0 {
		timeout = time.Duration(v) * time.Second
	}

	results, err := t.Manager.WaitTasks(ctx, taskIDs, timeout)

	// Convert results to a serialisable form regardless of error
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		out = append(out, map[string]any{
			"task_id": r.TaskID,
			"status":  r.Status,
			"result":  r.Result,
			"error":   r.Error,
		})
	}

	if err != nil {
		return map[string]any{
			"results": out,
			"error":   err.Error(),
		}, nil
	}

	return map[string]any{"results": out}, nil
}
