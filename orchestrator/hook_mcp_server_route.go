package orchestrator

import (
	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/config"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// mcpServerRouteHook registers the MCP server's HTTP handler on the router.
func mcpServerRouteHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.mcp_server_route_registration",
		Priority: 97, // Run before static file server registration
		Hook: func(app modular.Application, cfg *config.WorkflowConfig) error {
			// Find the MCP server instance
			var mcpServer *MCPServerModule
			for _, svc := range app.SvcRegistry() {
				if srv, ok := svc.(*MCPServerModule); ok {
					mcpServer = srv
					break
				}
			}
			if mcpServer == nil {
				return nil // MCP server not configured, skip
			}

			// Find the router instance using the HTTPRouter interface
			var router module.HTTPRouter
			for _, svc := range app.SvcRegistry() {
				if r, ok := svc.(module.HTTPRouter); ok {
					router = r
					break
				}
			}
			if router == nil {
				app.Logger().Warn("router not found in service registry, MCP server route registration skipped")
				return nil
			}

			// Register the MCP server as a POST route
			router.AddRoute("POST", mcpServer.Path(), module.NewHTTPHandlerAdapter(mcpServer))
			app.Logger().Info("MCP server route registered", "path", mcpServer.Path())
			return nil
		},
	}
}
