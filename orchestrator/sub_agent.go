package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	"github.com/google/uuid"
)

// SubAgentManager manages ephemeral sub-agents spawned by parent agents.
// It implements the tools.SubAgentSpawner interface.
type SubAgentManager struct {
	db           *sql.DB
	maxPerParent int // default 5
	maxDepth     int // default 1 (no recursive spawning)
}

// NewSubAgentManager creates a new SubAgentManager.
// maxPerParent is the maximum number of concurrent sub-agents per parent (default 5 when <= 0).
// maxDepth is the maximum spawn depth — ephemeral agents at this depth cannot spawn further (default 1 when <= 0).
func NewSubAgentManager(db *sql.DB, maxPerParent, maxDepth int) *SubAgentManager {
	if maxPerParent <= 0 {
		maxPerParent = 5
	}
	if maxDepth <= 0 {
		maxDepth = 1
	}
	return &SubAgentManager{
		db:           db,
		maxPerParent: maxPerParent,
		maxDepth:     maxDepth,
	}
}

// CountActive returns the number of active (non-completed, non-failed) sub-agents for the given parent.
func (sm *SubAgentManager) CountActive(ctx context.Context, parentAgentID string) (int, error) {
	var count int
	err := sm.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents
		 WHERE parent_agent_id = ? AND is_ephemeral = 1
		 AND status NOT IN ('completed', 'failed', 'idle')`,
		parentAgentID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active sub-agents: %w", err)
	}
	return count, nil
}

// Spawn creates an ephemeral sub-agent and assigns it a task.
// Returns the task ID for tracking.
func (sm *SubAgentManager) Spawn(ctx context.Context, parentAgentID string, name string, taskDesc string, systemPrompt string) (taskID string, err error) {
	// Enforce max sub-agents per parent
	count, err := sm.CountActive(ctx, parentAgentID)
	if err != nil {
		return "", fmt.Errorf("spawn sub-agent: %w", err)
	}
	if count >= sm.maxPerParent {
		return "", fmt.Errorf("spawn sub-agent: parent %q already has %d active sub-agents (max %d)", parentAgentID, count, sm.maxPerParent)
	}

	// Check depth: ephemeral agents cannot spawn their own sub-agents
	var parentIsEphemeral int
	row := sm.db.QueryRowContext(ctx, `SELECT is_ephemeral FROM agents WHERE id = ?`, parentAgentID)
	if scanErr := row.Scan(&parentIsEphemeral); scanErr == nil && parentIsEphemeral == 1 {
		return "", fmt.Errorf("spawn sub-agent: ephemeral agents cannot spawn sub-agents (max depth %d)", sm.maxDepth)
	}

	// Create ephemeral agent record
	agentID := uuid.New().String()
	_, err = sm.db.ExecContext(ctx, `
		INSERT INTO agents (id, name, role, system_prompt, provider, model, status, team_id, is_lead, is_ephemeral, parent_agent_id, created_at, updated_at)
		VALUES (?, ?, 'sub-agent', ?, '', '', 'busy', '', 0, 1, ?, datetime('now'), datetime('now'))`,
		agentID, name, systemPrompt, parentAgentID,
	)
	if err != nil {
		return "", fmt.Errorf("spawn sub-agent: create agent record: %w", err)
	}

	// Create task assigned to the new sub-agent
	taskID = uuid.New().String()
	_, err = sm.db.ExecContext(ctx, `
		INSERT INTO tasks (id, title, description, status, priority, assigned_to, parent_id, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', 5, ?, ?, datetime('now'), datetime('now'))`,
		taskID, name, taskDesc, agentID, parentAgentID,
	)
	if err != nil {
		return "", fmt.Errorf("spawn sub-agent: create task: %w", err)
	}

	return taskID, nil
}

// CheckTask returns the current status and result of a task.
func (sm *SubAgentManager) CheckTask(ctx context.Context, taskID string) (status string, result string, err error) {
	var taskErr string
	scanErr := sm.db.QueryRowContext(ctx,
		`SELECT status, result, error FROM tasks WHERE id = ?`,
		taskID,
	).Scan(&status, &result, &taskErr)
	if scanErr != nil {
		if scanErr == sql.ErrNoRows {
			return "", "", fmt.Errorf("task %q not found", taskID)
		}
		return "", "", fmt.Errorf("check task: %w", scanErr)
	}
	if taskErr != "" && result == "" {
		result = taskErr
	}
	return status, result, nil
}

// WaitTasks polls all given task IDs until they complete or timeout expires.
// Returns a map of taskID -> tools.SubTaskResult.
func (sm *SubAgentManager) WaitTasks(ctx context.Context, taskIDs []string, timeout time.Duration) (map[string]tools.SubTaskResult, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	deadline := time.Now().Add(timeout)
	results := make(map[string]tools.SubTaskResult, len(taskIDs))
	pending := make(map[string]struct{}, len(taskIDs))
	for _, id := range taskIDs {
		pending[id] = struct{}{}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for len(pending) > 0 {
		if time.Now().After(deadline) {
			// Mark remaining as timed out
			for id := range pending {
				results[id] = tools.SubTaskResult{
					TaskID: id,
					Status: "timeout",
					Error:  "task did not complete within timeout",
				}
			}
			return results, fmt.Errorf("wait tasks: timeout after %v", timeout)
		}

		select {
		case <-ctx.Done():
			return results, ctx.Err()
		case <-ticker.C:
		}

		for taskID := range pending {
			status, result, err := sm.CheckTask(ctx, taskID)
			if err != nil {
				results[taskID] = tools.SubTaskResult{
					TaskID: taskID,
					Status: "error",
					Error:  err.Error(),
				}
				delete(pending, taskID)
				continue
			}

			switch status {
			case "completed", "failed", "cancelled":
				results[taskID] = tools.SubTaskResult{
					TaskID: taskID,
					Status: status,
					Result: result,
				}
				delete(pending, taskID)
			}
		}
	}

	return results, nil
}

// CancelChildren cancels all active sub-agent tasks for the given parent agent.
func (sm *SubAgentManager) CancelChildren(ctx context.Context, parentAgentID string) error {
	// Mark pending/in_progress tasks from sub-agents of this parent as cancelled
	_, err := sm.db.ExecContext(ctx, `
		UPDATE tasks
		SET status = 'cancelled', updated_at = datetime('now')
		WHERE assigned_to IN (
			SELECT id FROM agents WHERE parent_agent_id = ? AND is_ephemeral = 1
		)
		AND status IN ('pending', 'in_progress')`,
		parentAgentID,
	)
	if err != nil {
		return fmt.Errorf("cancel children: update tasks: %w", err)
	}

	// Mark the ephemeral agents as idle
	_, err = sm.db.ExecContext(ctx, `
		UPDATE agents
		SET status = 'idle', updated_at = datetime('now')
		WHERE parent_agent_id = ? AND is_ephemeral = 1`,
		parentAgentID,
	)
	if err != nil {
		return fmt.Errorf("cancel children: update agents: %w", err)
	}

	return nil
}
