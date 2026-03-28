package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	_ "modernc.org/sqlite"
)

func setupSubAgentDB(t *testing.T) *SubAgentManager {
	t.Helper()
	db := openTestDB(t)

	// Create agents and tasks tables with all required columns
	_, err := db.Exec(createAgentsTable)
	if err != nil {
		t.Fatalf("create agents table: %v", err)
	}
	_, err = db.Exec(createTasksTable)
	if err != nil {
		t.Fatalf("create tasks table: %v", err)
	}

	// Add ephemeral columns (idempotent)
	_, _ = db.Exec("ALTER TABLE agents ADD COLUMN is_ephemeral INTEGER NOT NULL DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE agents ADD COLUMN parent_agent_id TEXT NOT NULL DEFAULT ''")

	// Seed a parent agent
	_, err = db.Exec(`
		INSERT INTO agents (id, name, role, system_prompt, status, is_ephemeral, parent_agent_id)
		VALUES ('parent-1', 'Parent Agent', 'lead', 'You are a lead agent.', 'busy', 0, '')`)
	if err != nil {
		t.Fatalf("seed parent agent: %v", err)
	}

	return NewSubAgentManager(db, 0, 0)
}

func TestSubAgentManager_Spawn(t *testing.T) {
	mgr := setupSubAgentDB(t)
	ctx := context.Background()

	taskID, err := mgr.Spawn(ctx, "parent-1", "sub-agent-1", "Summarize the report", "You are a helper.")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if taskID == "" {
		t.Fatal("expected non-empty task ID")
	}

	// Verify task exists
	status, _, err := mgr.CheckTask(ctx, taskID)
	if err != nil {
		t.Fatalf("CheckTask: %v", err)
	}
	if status != "pending" {
		t.Errorf("expected status 'pending', got %q", status)
	}
}

func TestSubAgentManager_CountActive(t *testing.T) {
	mgr := setupSubAgentDB(t)
	ctx := context.Background()

	count, err := mgr.CountActive(ctx, "parent-1")
	if err != nil {
		t.Fatalf("CountActive: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 active, got %d", count)
	}

	// Spawn one
	_, err = mgr.Spawn(ctx, "parent-1", "sub-1", "Task 1", "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	count, err = mgr.CountActive(ctx, "parent-1")
	if err != nil {
		t.Fatalf("CountActive: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 active, got %d", count)
	}
}

func TestSubAgentManager_MaxLimitEnforced(t *testing.T) {
	mgr := setupSubAgentDB(t)
	mgr.maxPerParent = 2
	ctx := context.Background()

	// Spawn up to the limit
	for i := 0; i < 2; i++ {
		_, err := mgr.Spawn(ctx, "parent-1", "sub", "task", "")
		if err != nil {
			t.Fatalf("Spawn %d: %v", i, err)
		}
	}

	// One more should fail
	_, err := mgr.Spawn(ctx, "parent-1", "over-limit", "task", "")
	if err == nil {
		t.Fatal("expected error when exceeding max sub-agents")
	}
}

func TestSubAgentManager_EphemeralCannotSpawn(t *testing.T) {
	mgr := setupSubAgentDB(t)
	ctx := context.Background()

	// Spawn a sub-agent under parent-1
	_, err := mgr.Spawn(ctx, "parent-1", "ephemeral", "do something", "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Get the ephemeral agent's ID
	var ephemeralID string
	err = mgr.db.QueryRowContext(ctx,
		`SELECT id FROM agents WHERE parent_agent_id = 'parent-1' AND is_ephemeral = 1 LIMIT 1`,
	).Scan(&ephemeralID)
	if err != nil {
		t.Fatalf("find ephemeral agent: %v", err)
	}

	// Ephemeral agent should not be able to spawn sub-agents
	_, err = mgr.Spawn(ctx, ephemeralID, "nested", "nested task", "")
	if err == nil {
		t.Fatal("expected error: ephemeral agents cannot spawn sub-agents")
	}
}

func TestSubAgentManager_CheckTask_NotFound(t *testing.T) {
	mgr := setupSubAgentDB(t)
	ctx := context.Background()

	_, _, err := mgr.CheckTask(ctx, "nonexistent-task-id")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestSubAgentManager_CancelChildren(t *testing.T) {
	mgr := setupSubAgentDB(t)
	ctx := context.Background()

	taskID1, _ := mgr.Spawn(ctx, "parent-1", "sub-1", "task 1", "")
	taskID2, _ := mgr.Spawn(ctx, "parent-1", "sub-2", "task 2", "")

	if err := mgr.CancelChildren(ctx, "parent-1"); err != nil {
		t.Fatalf("CancelChildren: %v", err)
	}

	for _, tid := range []string{taskID1, taskID2} {
		status, _, err := mgr.CheckTask(ctx, tid)
		if err != nil {
			t.Fatalf("CheckTask %s: %v", tid, err)
		}
		if status != "cancelled" {
			t.Errorf("task %s: expected status 'cancelled', got %q", tid, status)
		}
	}
}

func TestSubAgentManager_WaitTasks_Immediate(t *testing.T) {
	mgr := setupSubAgentDB(t)
	ctx := context.Background()

	taskID, _ := mgr.Spawn(ctx, "parent-1", "fast-task", "quick work", "")

	// Mark task as completed immediately
	_, err := mgr.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'completed', result = 'done!' WHERE id = ?`, taskID)
	if err != nil {
		t.Fatalf("update task: %v", err)
	}

	results, err := mgr.WaitTasks(ctx, []string{taskID}, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitTasks: %v", err)
	}
	res, ok := results[taskID]
	if !ok {
		t.Fatal("expected result for task")
	}
	if res.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", res.Status)
	}
	if res.Result != "done!" {
		t.Errorf("expected result 'done!', got %q", res.Result)
	}
}

func TestSubAgentManager_WaitTasks_Timeout(t *testing.T) {
	mgr := setupSubAgentDB(t)
	ctx := context.Background()

	taskID, _ := mgr.Spawn(ctx, "parent-1", "slow-task", "slow work", "")

	// Very short timeout — task stays pending
	results, err := mgr.WaitTasks(ctx, []string{taskID}, 1*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}

	res, ok := results[taskID]
	if !ok {
		t.Fatal("expected result for timed-out task")
	}
	if res.Status != "timeout" {
		t.Errorf("expected status 'timeout', got %q", res.Status)
	}
}

func TestSubAgentManager_ImplementsSpawnerInterface(t *testing.T) {
	mgr := setupSubAgentDB(t)
	// Verify SubAgentManager implements tools.SubAgentSpawner
	var _ tools.SubAgentSpawner = mgr
}
