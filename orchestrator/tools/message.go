package tools

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/google/uuid"
)

// MessageSendTool sends a message to another agent.
type MessageSendTool struct {
	DB *sql.DB
}

func (t *MessageSendTool) Name() string        { return "message_send" }
func (t *MessageSendTool) Description() string { return "Send a message to another agent" }
func (t *MessageSendTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to":      map[string]any{"type": "string", "description": "Recipient agent ID"},
				"subject": map[string]any{"type": "string", "description": "Message subject"},
				"content": map[string]any{"type": "string", "description": "Message content"},
			},
			"required": []string{"to", "content"},
		},
	}
}
func (t *MessageSendTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	to, _ := args["to"].(string)
	content, _ := args["content"].(string)
	subject, _ := args["subject"].(string)
	if to == "" || content == "" {
		return nil, fmt.Errorf("to and content are required")
	}

	// from_agent from args, falling back to the executing agent's ID from context
	from, _ := args["from"].(string)
	if from == "" {
		from, _ = AgentIDFromContext(ctx)
	}

	id := uuid.New().String()
	_, err := t.DB.ExecContext(ctx,
		`INSERT INTO messages (id, type, from_agent, to_agent, subject, content, created_at)
		 VALUES (?, 'direct', ?, ?, ?, ?, datetime('now'))`,
		id, from, to, subject, content,
	)
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}
	return map[string]any{"id": id, "sent": true}, nil
}
