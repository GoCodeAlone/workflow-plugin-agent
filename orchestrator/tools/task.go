package tools

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/google/uuid"
)

// TaskCreateTool creates a sub-task in the database.
type TaskCreateTool struct {
	DB *sql.DB
}

func (t *TaskCreateTool) Name() string        { return "task_create" }
func (t *TaskCreateTool) Description() string { return "Create a new task" }
func (t *TaskCreateTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":       map[string]any{"type": "string", "description": "Task title"},
				"description": map[string]any{"type": "string", "description": "Task description"},
				"priority":    map[string]any{"type": "integer", "description": "Priority (1-10, default 1)"},
				"assigned_to": map[string]any{"type": "string", "description": "Agent ID to assign to"},
				"project_id":  map[string]any{"type": "string", "description": "Project ID"},
			},
			"required": []string{"title"},
		},
	}
}
func (t *TaskCreateTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	title, _ := args["title"].(string)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	description, _ := args["description"].(string)
	priority := 1
	if v, ok := args["priority"].(float64); ok && v > 0 {
		priority = int(v)
	}
	assignedTo, _ := args["assigned_to"].(string)
	projectID, _ := args["project_id"].(string)

	id := uuid.New().String()
	_, err := t.DB.ExecContext(ctx,
		`INSERT INTO tasks (id, title, description, priority, assigned_to, project_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		id, title, description, priority, assignedTo, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	return map[string]any{"id": id, "title": title, "status": "pending"}, nil
}

// TaskUpdateTool updates a task's status in the database.
type TaskUpdateTool struct {
	DB *sql.DB
}

func (t *TaskUpdateTool) Name() string        { return "task_update" }
func (t *TaskUpdateTool) Description() string { return "Update a task's status or result" }
func (t *TaskUpdateTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string", "description": "Task ID"},
				"status": map[string]any{"type": "string", "description": "New status (pending, in_progress, completed, failed)"},
				"result": map[string]any{"type": "string", "description": "Task result"},
			},
			"required": []string{"id"},
		},
	}
}
func (t *TaskUpdateTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	status, _ := args["status"].(string)
	result, _ := args["result"].(string)

	if status != "" {
		_, err := t.DB.ExecContext(ctx,
			`UPDATE tasks SET status = ?, updated_at = datetime('now') WHERE id = ?`,
			status, id,
		)
		if err != nil {
			return nil, fmt.Errorf("update task status: %w", err)
		}
	}
	if result != "" {
		_, err := t.DB.ExecContext(ctx,
			`UPDATE tasks SET result = ?, updated_at = datetime('now') WHERE id = ?`,
			result, id,
		)
		if err != nil {
			return nil, fmt.Errorf("update task result: %w", err)
		}
	}

	return map[string]any{"id": id, "updated": true}, nil
}
