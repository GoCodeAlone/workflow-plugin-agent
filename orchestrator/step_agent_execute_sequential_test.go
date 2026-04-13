package orchestrator

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/module"
)

// countingTool records how many times it was called.
type countingTool struct {
	name  string
	calls atomic.Int32
}

func (c *countingTool) Name() string                                              { return c.name }
func (c *countingTool) Description() string                                       { return "counting tool" }
func (c *countingTool) Definition() provider.ToolDef                              { return provider.ToolDef{Name: c.name} }
func (c *countingTool) Execute(_ context.Context, _ map[string]any) (any, error) {
	c.calls.Add(1)
	return "ok", nil
}

func TestSequentialMode_ErrorOnMultipleToolCalls(t *testing.T) {
	h := NewTestHarness(t)
	env := SetupE2EAgent(t, h.Provider)

	toolA := &countingTool{name: "tool_a"}
	toolB := &countingTool{name: "tool_b"}
	toolC := &countingTool{name: "tool_c"}
	env.ToolRegistry.Register(toolA)
	env.ToolRegistry.Register(toolB)
	env.ToolRegistry.Register(toolC)

	pe := NewToolPolicyEngine(env.DB)
	if err := pe.InitTable(); err != nil {
		t.Fatalf("InitTable: %v", err)
	}
	pe.DefaultPolicy = PolicyAllow
	env.ToolRegistry.SetPolicyEngine(pe)

	// Configure step in sequential mode.
	env.Step.parallelToolCalls = false
	env.Step.maxIterations = 5

	tc1 := provider.ToolCall{ID: "1", Name: "tool_a", Arguments: map[string]any{}}
	tc2 := provider.ToolCall{ID: "2", Name: "tool_b", Arguments: map[string]any{}}
	tc3 := provider.ToolCall{ID: "3", Name: "tool_c", Arguments: map[string]any{}}

	var secondInteraction Interaction
	done := make(chan struct{})
	go func() {
		defer close(done)
		// First call: return all 3 tool calls — no tools should execute, error injected.
		h.ExpectInteraction(5 * time.Second)
		h.RespondToolCall("calling tools", tc1, tc2, tc3)

		// Second call: LLM receives the error and corrects to a single tool call.
		secondInteraction = h.ExpectInteraction(5 * time.Second)
		h.RespondToolCall("calling single tool", tc1)

		// Third call: LLM returns final answer after seeing tool_a result.
		h.ExpectInteraction(5 * time.Second)
		h.RespondText("done")
	}()

	pc := NewPipelineContext("agent-1", "task-1", "run all tools")
	_, err := env.Step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	<-done

	// No tools should have executed on the first turn (all 3 rejected via error).
	// tool_a executes once on the second turn after LLM corrects itself.
	if toolA.calls.Load() != 1 {
		t.Errorf("tool_a: expected 1 call, got %d", toolA.calls.Load())
	}
	if toolB.calls.Load() != 0 {
		t.Errorf("tool_b: expected 0 calls, got %d", toolB.calls.Load())
	}
	if toolC.calls.Load() != 0 {
		t.Errorf("tool_c: expected 0 calls, got %d", toolC.calls.Load())
	}

	// Verify the sequential-execution error was injected into the conversation.
	found := false
	for _, msg := range secondInteraction.Messages {
		if msg.Role == provider.RoleUser && strings.Contains(msg.Content, "sequential execution") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected sequential execution error message in second LLM interaction")
	}
}

func TestParallelToolCalls_AllExecutePerTurn(t *testing.T) {
	h := NewTestHarness(t)
	env := SetupE2EAgent(t, h.Provider)

	toolA := &countingTool{name: "tool_a"}
	toolB := &countingTool{name: "tool_b"}
	toolC := &countingTool{name: "tool_c"}
	env.ToolRegistry.Register(toolA)
	env.ToolRegistry.Register(toolB)
	env.ToolRegistry.Register(toolC)

	pe := NewToolPolicyEngine(env.DB)
	if err := pe.InitTable(); err != nil {
		t.Fatalf("InitTable: %v", err)
	}
	pe.DefaultPolicy = PolicyAllow
	env.ToolRegistry.SetPolicyEngine(pe)

	// parallel_tool_calls defaults to true.
	env.Step.parallelToolCalls = true
	env.Step.maxIterations = 5

	tc1 := provider.ToolCall{ID: "1", Name: "tool_a", Arguments: map[string]any{}}
	tc2 := provider.ToolCall{ID: "2", Name: "tool_b", Arguments: map[string]any{}}
	tc3 := provider.ToolCall{ID: "3", Name: "tool_c", Arguments: map[string]any{}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ExpectInteraction(5 * time.Second)
		h.RespondToolCall("calling tools", tc1, tc2, tc3)

		h.ExpectInteraction(5 * time.Second)
		h.RespondText("done")
	}()

	pc := &module.PipelineContext{
		Current: map[string]any{
			"system_prompt": "test",
			"task":          "run tools",
			"agent_name":    "agent-1",
			"agent_id":      "agent-1",
			"task_id":       "task-1",
		},
	}
	_, err := env.Step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	<-done

	if toolA.calls.Load() != 1 {
		t.Errorf("tool_a: expected 1 call, got %d", toolA.calls.Load())
	}
	if toolB.calls.Load() != 1 {
		t.Errorf("tool_b: expected 1 call in parallel mode, got %d", toolB.calls.Load())
	}
	if toolC.calls.Load() != 1 {
		t.Errorf("tool_c: expected 1 call in parallel mode, got %d", toolC.calls.Load())
	}
}

func TestFactory_ParallelToolCalls_DefaultTrue(t *testing.T) {
	factory := newAgentExecuteStepFactory()
	raw, err := factory("test", map[string]any{}, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	step := raw.(*AgentExecuteStep)
	if !step.parallelToolCalls {
		t.Error("expected parallelToolCalls=true by default")
	}
}

func TestFactory_ParallelToolCalls_SetFalse(t *testing.T) {
	factory := newAgentExecuteStepFactory()
	raw, err := factory("test", map[string]any{"parallel_tool_calls": false}, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	step := raw.(*AgentExecuteStep)
	if step.parallelToolCalls {
		t.Error("expected parallelToolCalls=false when configured")
	}
}
