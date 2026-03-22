package tools_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/tools"
)

// --- mock tool implementations ---

type mockTool struct {
	name string
	def  provider.ToolDef
	fn   func(context.Context, map[string]any) (any, error)
}

func (m *mockTool) Name() string                 { return m.name }
func (m *mockTool) Definition() provider.ToolDef { return m.def }
func (m *mockTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	return m.fn(ctx, args)
}

func newEchoTool(name string) *mockTool {
	return &mockTool{
		name: name,
		def:  provider.ToolDef{Name: name, Description: "echoes input"},
		fn: func(_ context.Context, args map[string]any) (any, error) {
			return args, nil
		},
	}
}

func newErrorTool(name string, errMsg string) *mockTool {
	return &mockTool{
		name: name,
		def:  provider.ToolDef{Name: name, Description: "always errors"},
		fn: func(_ context.Context, _ map[string]any) (any, error) {
			return nil, errors.New(errMsg)
		},
	}
}

// --- Registry tests ---

// TestRegistry_RegisterAndGet verifies a tool can be registered and retrieved.
func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := tools.NewRegistry()
	tool := newEchoTool("echo")
	reg.Register(tool)

	got, ok := reg.Get("echo")
	if !ok {
		t.Fatal("expected to find 'echo' in registry")
	}
	if got.Name() != "echo" {
		t.Errorf("Name: want echo, got %q", got.Name())
	}
}

// TestRegistry_GetMissing returns false for unknown tool.
func TestRegistry_GetMissing(t *testing.T) {
	reg := tools.NewRegistry()
	_, ok := reg.Get("nonexistent")
	if ok {
		t.Error("expected false for nonexistent tool")
	}
}

// TestRegistry_Names returns all registered tool names.
func TestRegistry_Names(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(newEchoTool("tool_a"))
	reg.Register(newEchoTool("tool_b"))
	reg.Register(newEchoTool("tool_c"))

	names := reg.Names()
	sort.Strings(names)
	want := []string{"tool_a", "tool_b", "tool_c"}
	if len(names) != len(want) {
		t.Fatalf("Names: want %v, got %v", want, names)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("Names[%d]: want %q, got %q", i, want[i], n)
		}
	}
}

// TestRegistry_AllDefs returns definitions for all tools.
func TestRegistry_AllDefs(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(newEchoTool("t1"))
	reg.Register(newEchoTool("t2"))

	defs := reg.AllDefs()
	if len(defs) != 2 {
		t.Errorf("AllDefs: want 2, got %d", len(defs))
	}
}

// TestRegistry_Execute_NoPolicyEngine warns but still executes.
func TestRegistry_Execute_NoPolicyEngine(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(newEchoTool("echo"))

	result, err := reg.Execute(context.Background(), "echo", map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil {
		t.Error("expected non-nil result")
	}
}

// TestRegistry_Execute_ToolNotFound returns error for missing tool.
func TestRegistry_Execute_ToolNotFound(t *testing.T) {
	reg := tools.NewRegistry()
	_, err := reg.Execute(context.Background(), "ghost_tool", nil)
	if err == nil {
		t.Fatal("expected error for missing tool")
	}
	if !strings.Contains(err.Error(), "ghost_tool") {
		t.Errorf("error should mention tool name, got: %v", err)
	}
}

// TestRegistry_Execute_ToolError propagates tool execution errors.
func TestRegistry_Execute_ToolError(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(newErrorTool("bad_tool", "something went wrong"))

	_, err := reg.Execute(context.Background(), "bad_tool", nil)
	if err == nil {
		t.Fatal("expected error from failing tool")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("expected original error message, got: %v", err)
	}
}

// TestRegistry_Execute_PolicyDenied returns error when policy engine denies.
func TestRegistry_Execute_PolicyDenied(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(newEchoTool("restricted_tool"))
	reg.SetPolicyEngine(&denyAllPolicy{})

	_, err := reg.Execute(context.Background(), "restricted_tool", nil)
	if err == nil {
		t.Fatal("expected error when policy denies")
	}
	if !strings.Contains(err.Error(), "denied by policy") {
		t.Errorf("expected policy denial error, got: %v", err)
	}
}

// TestRegistry_Execute_PolicyAllowed executes when policy permits.
func TestRegistry_Execute_PolicyAllowed(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(newEchoTool("allowed_tool"))
	reg.SetPolicyEngine(&allowAllPolicy{})

	result, err := reg.Execute(context.Background(), "allowed_tool", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("Execute with allow policy: %v", err)
	}
	if result == nil {
		t.Error("expected non-nil result")
	}
}

// TestRegistry_RegisterMCP prefixes names with mcp_<server>__.
func TestRegistry_RegisterMCP(t *testing.T) {
	reg := tools.NewRegistry()
	reg.RegisterMCP("myserver", []tools.Tool{
		newEchoTool("search"),
		newEchoTool("fetch"),
	})

	_, ok := reg.Get("mcp_myserver__search")
	if !ok {
		t.Error("expected mcp_myserver__search to be registered")
	}
	_, ok = reg.Get("mcp_myserver__fetch")
	if !ok {
		t.Error("expected mcp_myserver__fetch to be registered")
	}
}

// TestRegistry_UnregisterMCP removes all tools for a server.
func TestRegistry_UnregisterMCP(t *testing.T) {
	reg := tools.NewRegistry()
	reg.RegisterMCP("srv", []tools.Tool{
		newEchoTool("tool1"),
		newEchoTool("tool2"),
	})
	reg.UnregisterMCP("srv")

	if _, ok := reg.Get("mcp_srv__tool1"); ok {
		t.Error("expected mcp_srv__tool1 to be removed after UnregisterMCP")
	}
	if _, ok := reg.Get("mcp_srv__tool2"); ok {
		t.Error("expected mcp_srv__tool2 to be removed after UnregisterMCP")
	}
}

// TestRegistry_OverwriteOnRegister verifies re-registering a name replaces the tool.
func TestRegistry_OverwriteOnRegister(t *testing.T) {
	reg := tools.NewRegistry()
	original := newEchoTool("my_tool")
	reg.Register(original)

	replacement := &mockTool{
		name: "my_tool",
		def:  provider.ToolDef{Name: "my_tool", Description: "replacement"},
		fn: func(_ context.Context, _ map[string]any) (any, error) {
			return "replaced", nil
		},
	}
	reg.Register(replacement)

	got, _ := reg.Get("my_tool")
	result, _ := got.Execute(context.Background(), nil)
	if result != "replaced" {
		t.Errorf("expected replacement tool to be used, got %v", result)
	}
}

// --- Context helpers tests ---

// TestContextHelpers_AgentID stores and retrieves agent ID from context.
func TestContextHelpers_AgentID(t *testing.T) {
	ctx := tools.WithAgentID(context.Background(), "agent-99")
	got := tools.AgentIDFromContext(ctx)
	if got != "agent-99" {
		t.Errorf("AgentIDFromContext: want agent-99, got %q", got)
	}
}

// TestContextHelpers_TaskID stores and retrieves task ID from context.
func TestContextHelpers_TaskID(t *testing.T) {
	ctx := tools.WithTaskID(context.Background(), "task-42")
	got := tools.TaskIDFromContext(ctx)
	if got != "task-42" {
		t.Errorf("TaskIDFromContext: want task-42, got %q", got)
	}
}

// TestContextHelpers_TeamID stores and retrieves team ID from context.
func TestContextHelpers_TeamID(t *testing.T) {
	ctx := tools.WithTeamID(context.Background(), "team-alpha")
	got := tools.TeamIDFromContext(ctx)
	if got != "team-alpha" {
		t.Errorf("TeamIDFromContext: want team-alpha, got %q", got)
	}
}

// TestContextHelpers_ProjectID stores and retrieves project ID from context.
func TestContextHelpers_ProjectID(t *testing.T) {
	ctx := tools.WithProjectID(context.Background(), "proj-x")
	got := tools.ProjectIDFromContext(ctx)
	if got != "proj-x" {
		t.Errorf("ProjectIDFromContext: want proj-x, got %q", got)
	}
}

// TestContextHelpers_EmptyOnMissingKey returns empty string when not set.
func TestContextHelpers_EmptyOnMissingKey(t *testing.T) {
	ctx := context.Background()
	if id := tools.AgentIDFromContext(ctx); id != "" {
		t.Errorf("expected empty string for missing agent ID, got %q", id)
	}
	if id := tools.TaskIDFromContext(ctx); id != "" {
		t.Errorf("expected empty string for missing task ID, got %q", id)
	}
}

// TestContextHelpers_ChainedValues multiple IDs in same context.
func TestContextHelpers_ChainedValues(t *testing.T) {
	ctx := tools.WithAgentID(context.Background(), "agent-1")
	ctx = tools.WithTaskID(ctx, "task-1")
	ctx = tools.WithTeamID(ctx, "team-1")
	ctx = tools.WithProjectID(ctx, "project-1")

	if tools.AgentIDFromContext(ctx) != "agent-1" {
		t.Error("AgentID lost after chaining context")
	}
	if tools.TaskIDFromContext(ctx) != "task-1" {
		t.Error("TaskID lost after chaining context")
	}
	if tools.TeamIDFromContext(ctx) != "team-1" {
		t.Error("TeamID lost after chaining context")
	}
	if tools.ProjectIDFromContext(ctx) != "project-1" {
		t.Error("ProjectID lost after chaining context")
	}
}

// --- mock policy engines ---

type denyAllPolicy struct{}

func (d *denyAllPolicy) IsAllowed(_ context.Context, _, _, _ string) (bool, string) {
	return false, "all tools denied by policy"
}

type allowAllPolicy struct{}

func (a *allowAllPolicy) IsAllowed(_ context.Context, _, _, _ string) (bool, string) {
	return true, ""
}
