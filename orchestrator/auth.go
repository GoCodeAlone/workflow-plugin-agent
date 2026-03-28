package orchestrator

import (
	"os"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/config"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

const defaultDevToken = "ratchet-dev-token-change-me-in-production"

// authTokenHook registers an auth token with the auth middleware.
// Set RATCHET_AUTH_TOKEN to override the default dev token.
// A warning is logged if the default insecure token is in use.
func authTokenHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.auth_token",
		Priority: 90,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			token := os.Getenv("RATCHET_AUTH_TOKEN")
			if token == "" {
				token = defaultDevToken
				app.Logger().Warn("using default dev auth token — set RATCHET_AUTH_TOKEN for production")
			}

			// Find all auth middlewares and register our token
			for _, svc := range app.SvcRegistry() {
				am, ok := svc.(*module.AuthMiddleware)
				if !ok {
					continue
				}
				am.AddProvider(map[string]map[string]any{
					token: {
						"sub":  "admin",
						"role": "admin",
					},
				})
			}
			return nil
		},
	}
}
