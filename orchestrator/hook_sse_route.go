package orchestrator

import (
	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/config"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// sseRouteRegistrationHook registers the SSE hub's HTTP handler on the router
// with high priority to ensure it takes precedence over the static file server's catch-all route.
func sseRouteRegistrationHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.sse_route_registration",
		Priority: 98, // Run before static file server registration
		Hook: func(app modular.Application, cfg *config.WorkflowConfig) error {
			// Find the SSE hub instance
			var sseHub *SSEHub
			for _, svc := range app.SvcRegistry() {
				if hub, ok := svc.(*SSEHub); ok {
					sseHub = hub
					break
				}
			}
			if sseHub == nil {
				return nil // SSE hub not configured, skip
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
				app.Logger().Warn("router not found in service registry, SSE route registration skipped")
				return nil
			}

			// Register the SSE hub as a GET route, wrapping it in an HTTPHandlerAdapter
			// so it satisfies the module.HTTPHandler interface expected by AddRoute.
			router.AddRoute("GET", sseHub.Path(), module.NewHTTPHandlerAdapter(sseHub))
			app.Logger().Info("SSE route registered", "path", sseHub.Path())
			return nil
		},
	}
}
