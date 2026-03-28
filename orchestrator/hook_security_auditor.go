package orchestrator

import (
	"database/sql"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/config"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// securityAuditorHook creates a SecurityAuditor and registers it in the service registry.
func securityAuditorHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.security_auditor",
		Priority: 70,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			auditor := NewSecurityAuditor(db, app)
			_ = app.RegisterService("ratchet-security-auditor", auditor)
			return nil
		},
	}
}
