package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// TranscriptEntry represents one entry in the transcript log.
type TranscriptEntry struct {
	ID         string              `json:"id"`
	AgentID    string              `json:"agent_id"`
	TaskID     string              `json:"task_id"`
	ProjectID  string              `json:"project_id"`
	Iteration  int                 `json:"iteration"`
	Role       provider.Role       `json:"role"`
	Content    string              `json:"content"`
	ToolCalls  []provider.ToolCall `json:"tool_calls"`
	ToolCallID string              `json:"tool_call_id"`
	Redacted   bool                `json:"redacted"`
	CreatedAt  string              `json:"created_at"`
}

// TranscriptRecorder records agent interactions to the database.
type TranscriptRecorder struct {
	db    *sql.DB
	guard *SecretGuard
}

func NewTranscriptRecorder(db *sql.DB, guard *SecretGuard) *TranscriptRecorder {
	return &TranscriptRecorder{db: db, guard: guard}
}

// Record saves a transcript entry to the database.
func (tr *TranscriptRecorder) Record(ctx context.Context, entry TranscriptEntry) error {
	redacted := 0
	content := entry.Content
	if tr.guard != nil {
		original := content
		content = tr.guard.Redact(content)
		if content != original {
			redacted = 1
		}
	}

	toolCallsJSON, _ := json.Marshal(entry.ToolCalls)
	if entry.ToolCalls == nil {
		toolCallsJSON = []byte("[]")
	}

	_, err := tr.db.ExecContext(ctx,
		`INSERT INTO transcripts (id, agent_id, task_id, project_id, iteration, role, content, tool_calls, tool_call_id, redacted)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.AgentID, entry.TaskID, entry.ProjectID,
		entry.Iteration, string(entry.Role), content, string(toolCallsJSON),
		entry.ToolCallID, redacted,
	)
	return err
}

// GetByTask returns all transcript entries for a given task.
func (tr *TranscriptRecorder) GetByTask(ctx context.Context, taskID string) ([]TranscriptEntry, error) {
	return tr.query(ctx, "SELECT id, agent_id, task_id, project_id, iteration, role, content, tool_calls, tool_call_id, redacted, created_at FROM transcripts WHERE task_id = ? ORDER BY created_at ASC", taskID)
}

// GetByAgent returns all transcript entries for a given agent.
func (tr *TranscriptRecorder) GetByAgent(ctx context.Context, agentID string) ([]TranscriptEntry, error) {
	return tr.query(ctx, "SELECT id, agent_id, task_id, project_id, iteration, role, content, tool_calls, tool_call_id, redacted, created_at FROM transcripts WHERE agent_id = ? ORDER BY created_at ASC", agentID)
}

func (tr *TranscriptRecorder) query(ctx context.Context, q string, args ...any) ([]TranscriptEntry, error) {
	rows, err := tr.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []TranscriptEntry
	for rows.Next() {
		var e TranscriptEntry
		var toolCallsJSON string
		var redacted int
		if err := rows.Scan(&e.ID, &e.AgentID, &e.TaskID, &e.ProjectID, &e.Iteration, &e.Role, &e.Content, &toolCallsJSON, &e.ToolCallID, &redacted, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(toolCallsJSON), &e.ToolCalls)
		e.Redacted = redacted == 1
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
