package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// MemoryExtractStep extracts memories from completed task transcripts and saves them.
type MemoryExtractStep struct {
	name string
	app  modular.Application
}

func (s *MemoryExtractStep) Name() string { return s.name }

func (s *MemoryExtractStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	if s.app == nil {
		return &module.StepResult{Output: map[string]any{"extracted": false, "reason": "no app context"}}, nil
	}

	// Get agent_id and task_id from pipeline context
	agentID := ""
	taskID := ""
	if v, ok := pc.Current["agent_id"].(string); ok {
		agentID = v
	}
	if v, ok := pc.Current["task_id"].(string); ok {
		taskID = v
	}
	// Also check step outputs from find-pending-task
	for _, out := range pc.StepOutputs {
		if row, ok := out["row"].(map[string]any); ok {
			if v, ok := row["agent_id"].(string); ok && agentID == "" {
				agentID = v
			}
			if v, ok := row["id"].(string); ok && taskID == "" {
				taskID = v
			}
		}
	}

	if agentID == "" {
		return &module.StepResult{Output: map[string]any{"extracted": false, "reason": "no agent_id"}}, nil
	}

	// Resolve services via the orchestrator service bundle. memory_extract is
	// REQUIRED-STATEFUL on both the MemoryStoreService (ExtractAndSave) and the
	// DB (it must read the transcripts table). Either being absent yields a
	// graceful extracted=false outcome — matching the prior keyed-lookup
	// behavior — but the cause strings are preserved for diagnostics.
	svcs := resolveServices(s.app)
	if IsNull(svcs.Memory) {
		return &module.StepResult{Output: map[string]any{"extracted": false, "reason": "memory store not available"}}, nil
	}
	ms := svcs.Memory

	if svcs.DB == nil {
		return &module.StepResult{Output: map[string]any{"extracted": false, "reason": "no database"}}, nil
	}
	db := svcs.DB.DB()

	// Query recent transcripts for the agent+task
	query := `SELECT role, content FROM transcripts WHERE agent_id = ? AND task_id = ? ORDER BY created_at ASC`
	rows, err := db.QueryContext(ctx, query, agentID, taskID)
	if err != nil {
		return &module.StepResult{Output: map[string]any{"extracted": false, "reason": fmt.Sprintf("query error: %v", err)}}, nil
	}
	defer func() { _ = rows.Close() }()

	var sb strings.Builder
	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err != nil {
			continue
		}
		if role == "assistant" {
			sb.WriteString(content)
			sb.WriteString("\n")
		}
		// Cap at 10KB
		if sb.Len() > 10240 {
			break
		}
	}

	transcript := sb.String()
	if transcript == "" {
		return &module.StepResult{Output: map[string]any{"extracted": false, "reason": "no transcript content"}}, nil
	}

	// Extract and save memories
	if err := ms.ExtractAndSave(ctx, agentID, transcript, nil); err != nil {
		return &module.StepResult{Output: map[string]any{"extracted": false, "reason": fmt.Sprintf("extract error: %v", err)}}, nil
	}

	return &module.StepResult{Output: map[string]any{
		"extracted": true,
		"agent_id":  agentID,
		"task_id":   taskID,
	}}, nil
}

func newMemoryExtractFactory() plugin.StepFactory {
	return func(name string, _ map[string]any, app modular.Application) (any, error) {
		return &MemoryExtractStep{name: name, app: app}, nil
	}
}
