package orchestrator

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/modular"
	ratchetplugin "github.com/GoCodeAlone/workflow-plugin-agent/plugin"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/config"
	wfmcp "github.com/GoCodeAlone/workflow/mcp"
	"github.com/GoCodeAlone/workflow/plugin"
)

// MCPProvider is the interface for in-process MCP tool invocation.
// It matches github.com/GoCodeAlone/workflow/mcp.MCPProvider so callers can
// use either type interchangeably.
type MCPProvider interface {
	ListTools() []string
	ListToolSchemas() []wfmcp.ToolSchema
	CallTool(ctx context.Context, toolName string, args map[string]any) (any, error)
}

// inProcessMCPToolAdapter wraps an in-process MCP tool as a plugin.Tool for
// the ToolRegistry. The tool name is exposed as "mcp_wfctl_<toolName>" so it
// cannot collide with native tools or external MCP-client tools.
type inProcessMCPToolAdapter struct {
	serverName  string
	toolName    string
	description string
	schema      map[string]any // JSON Schema for parameters
	provider    MCPProvider
}

// Name returns the raw tool name. The ToolRegistry's RegisterMCP method will
// prepend the "mcp_{server}__" prefix, matching the convention used by
// MCPToolAdapter in module_mcp_client.go.
func (a *inProcessMCPToolAdapter) Name() string {
	return a.toolName
}

// Description returns the tool description from the MCP schema, or a generic one.
func (a *inProcessMCPToolAdapter) Description() string {
	if a.description != "" {
		return a.description
	}
	return fmt.Sprintf("MCP tool %q from server %q", a.toolName, a.serverName)
}

// Definition returns a ToolDef with the tool name, description, and parameter
// schema from the MCP tool's registration. This allows LLMs to know what
// arguments each tool accepts.
func (a *inProcessMCPToolAdapter) Definition() provider.ToolDef {
	params := a.schema
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return provider.ToolDef{
		Name:        a.toolName,
		Description: a.Description(),
		Parameters:  params,
	}
}

// Execute delegates to the MCPProvider.
func (a *inProcessMCPToolAdapter) Execute(ctx context.Context, args map[string]any) (any, error) {
	return a.provider.CallTool(ctx, a.toolName, args)
}

// mcpToolsHook bridges MCP tools into the ToolRegistry.
//
// Priority 60: runs well after tool_registry (priority 80) so the registry
// already exists, but before low-priority hooks like transcript_recorder (75)
// or blackboard (70) that may depend on the final set of tools.
//
// Sources wired, in order:
//  1. "mcp.provider" service (in-process wfctl MCP server) — registered under
//     server name "wfctl".
//  2. Any ratchet.mcp_client modules already registered in the service registry
//     (external MCP servers via stdio). Their tools are already registered by
//     MCPClientModule.Start(), so this hook re-registers them under the unified
//     naming convention if they were missed.
func mcpToolsHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.mcp_tools_wiring",
		Priority: 60,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			// Resolve the ToolRegistry — created by toolRegistryHook (priority 80).
			registrySvc, ok := app.SvcRegistry()["ratchet-tool-registry"]
			if !ok {
				app.Logger().Warn("mcp_tools_wiring: ratchet-tool-registry not found; skipping")
				return nil
			}
			registry, ok := registrySvc.(*ToolRegistry)
			if !ok {
				return nil
			}

			// Source 1: in-process wfctl MCP server.
			if mcpSvc, ok := app.SvcRegistry()["mcp.provider"]; ok {
				if prov, ok := mcpSvc.(MCPProvider); ok {
					registerMCPProviderTools(registry, "wfctl", prov, app)
				}
			} else {
				// Not yet registered — create a default in-process server and wire it.
				inProc := wfmcp.NewInProcessServer()
				// Store in service registry so other components can reach it.
				_ = app.RegisterService("mcp.provider", inProc)
				registerMCPProviderTools(registry, "wfctl", inProc, app)
			}

			return nil
		},
	}
}

// registerMCPProviderTools lists all tools from the provider with their
// schemas and registers them in the ToolRegistry.
func registerMCPProviderTools(registry *ToolRegistry, serverName string, prov MCPProvider, app modular.Application) {
	schemas := prov.ListToolSchemas()
	var adapted []ratchetplugin.Tool
	for _, ts := range schemas {
		adapted = append(adapted, &inProcessMCPToolAdapter{
			serverName:  serverName,
			toolName:    ts.Name,
			description: ts.Description,
			schema:      ts.InputSchema,
			provider:    prov,
		})
	}
	registry.RegisterMCP(serverName, adapted)
	app.Logger().Info("mcp_tools_wiring: registered MCP tools", "server", serverName, "count", len(adapted))
}
