package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	wfmcp "github.com/GoCodeAlone/workflow/mcp"
)

// mockMCPProvider is a test double for MCPProvider.
type mockMCPProvider struct {
	tools   []string
	results map[string]any
	errs    map[string]error
}

func (m *mockMCPProvider) ListTools() []string { return m.tools }

func (m *mockMCPProvider) ListToolSchemas() []wfmcp.ToolSchema {
	schemas := make([]wfmcp.ToolSchema, len(m.tools))
	for i, name := range m.tools {
		schemas[i] = wfmcp.ToolSchema{
			Name:        name,
			Description: "Mock tool: " + name,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"yaml_content": map[string]any{"type": "string"},
			}},
		}
	}
	return schemas
}

func (m *mockMCPProvider) CallTool(_ context.Context, toolName string, _ map[string]any) (any, error) {
	if err, ok := m.errs[toolName]; ok {
		return nil, err
	}
	if res, ok := m.results[toolName]; ok {
		return res, nil
	}
	return nil, nil
}

func TestInProcessMCPToolAdapter_Name(t *testing.T) {
	prov := &mockMCPProvider{}
	adapter := &inProcessMCPToolAdapter{
		serverName: "wfctl",
		toolName:   "validate_config",
		provider:   prov,
	}

	// Name() returns the raw tool name; RegisterMCP adds the "mcp_{server}__" prefix.
	got := adapter.Name()
	want := "validate_config"
	if got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestInProcessMCPToolAdapter_Name_CustomServer(t *testing.T) {
	prov := &mockMCPProvider{}
	adapter := &inProcessMCPToolAdapter{
		serverName: "myserver",
		toolName:   "do_thing",
		provider:   prov,
	}

	// Name returns raw tool name — server prefix is added by RegisterMCP.
	got := adapter.Name()
	if got != "do_thing" {
		t.Errorf("Name() = %q, want %q", got, "do_thing")
	}
}

func TestInProcessMCPToolAdapter_Execute_Success(t *testing.T) {
	prov := &mockMCPProvider{
		tools:   []string{"list_step_types"},
		results: map[string]any{"list_step_types": "step.agent_execute"},
	}
	adapter := &inProcessMCPToolAdapter{
		serverName: "wfctl",
		toolName:   "list_step_types",
		provider:   prov,
	}

	result, err := adapter.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result != "step.agent_execute" {
		t.Errorf("Execute() result = %v, want %q", result, "step.agent_execute")
	}
}

func TestInProcessMCPToolAdapter_Execute_Error(t *testing.T) {
	prov := &mockMCPProvider{
		tools: []string{"bad_tool"},
		errs:  map[string]error{"bad_tool": errors.New("tool failure")},
	}
	adapter := &inProcessMCPToolAdapter{
		serverName: "wfctl",
		toolName:   "bad_tool",
		provider:   prov,
	}

	_, err := adapter.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tool failure") {
		t.Errorf("Execute() error = %v, want to contain 'tool failure'", err)
	}
}

func TestMCPToolsHook_RegistersTools(t *testing.T) {
	app := newMockApp()

	// Pre-populate the tool registry (normally done by toolRegistryHook).
	registry := NewToolRegistry()
	_ = app.RegisterService("ratchet-tool-registry", registry)

	// Register a mock MCP provider.
	prov := &mockMCPProvider{
		tools: []string{"validate_config", "list_module_types"},
	}
	_ = app.RegisterService("mcp.provider", prov)

	hook := mcpToolsHook()
	if err := hook.Hook(app, nil); err != nil {
		t.Fatalf("hook.Hook() error = %v", err)
	}

	// RegisterMCP stores tools as "mcp_{server}__{toolName}" (double underscore).
	for _, toolName := range []string{"validate_config", "list_module_types"} {
		name := "mcp_wfctl__" + toolName
		if _, ok := registry.Get(name); !ok {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
}

func TestMCPToolsHook_NoRegistryGracefulSkip(t *testing.T) {
	app := newMockApp()
	// No ratchet-tool-registry in the service registry.

	hook := mcpToolsHook()
	if err := hook.Hook(app, nil); err != nil {
		t.Fatalf("hook.Hook() should not error when registry is absent, got: %v", err)
	}
}

func TestMCPToolsHook_NoProviderCreatesDefault(t *testing.T) {
	app := newMockApp()

	// Pre-populate tool registry but no mcp.provider.
	registry := NewToolRegistry()
	_ = app.RegisterService("ratchet-tool-registry", registry)

	hook := mcpToolsHook()
	// Should not error even without an explicit provider — it creates one.
	if err := hook.Hook(app, nil); err != nil {
		t.Fatalf("hook.Hook() error = %v", err)
	}

	// The in-process server should now be registered as mcp.provider.
	if _, ok := app.SvcRegistry()["mcp.provider"]; !ok {
		t.Error("expected mcp.provider to be registered after hook runs without one")
	}
}
