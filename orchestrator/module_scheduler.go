package orchestrator

import (
	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// newSchedulerFactory creates a module factory for ratchet.scheduler.
// This wraps workflow's built-in CronScheduler which implements the
// module.Scheduler interface needed by the schedule trigger.
func newSchedulerFactory() plugin.ModuleFactory {
	return func(name string, cfg map[string]any) modular.Module {
		cronExpr := "* * * * *"
		if c, ok := cfg["cronExpression"].(string); ok && c != "" {
			cronExpr = c
		}
		return module.NewCronScheduler(name, cronExpr)
	}
}
