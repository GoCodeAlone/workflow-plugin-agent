package agent

import (
	"context"
	"fmt"

	"github.com/CrisisTextLine/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// ProviderTestStep tests connectivity to a configured AI provider.
type ProviderTestStep struct {
	name      string
	aliasExpr string
	app       modular.Application
	tmpl      *module.TemplateEngine
}

func (s *ProviderTestStep) Name() string { return s.name }

func (s *ProviderTestStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	var alias string
	if s.aliasExpr != "" {
		raw, err := s.tmpl.Resolve(s.aliasExpr, pc)
		if err != nil {
			return nil, fmt.Errorf("provider_test step %q: resolve alias: %w", s.name, err)
		}
		alias = fmt.Sprintf("%v", raw)
	}
	if alias == "" {
		alias = extractString(pc.Current, "alias", "")
	}
	if alias == "" {
		return nil, fmt.Errorf("provider_test step %q: alias is required", s.name)
	}

	// Lazy-lookup ProviderRegistry
	for _, regSvcName := range []string{"agent-provider-registry", "ratchet-provider-registry"} {
		svc, ok := s.app.SvcRegistry()[regSvcName]
		if !ok {
			continue
		}
		registry, ok := svc.(*ProviderRegistry)
		if !ok {
			continue
		}

		success, message, latency, err := registry.TestConnection(ctx, alias)
		if err != nil {
			return &module.StepResult{
				Output: map[string]any{
					"success":    false,
					"message":    message,
					"latency_ms": latency.Milliseconds(),
				},
			}, nil
		}

		return &module.StepResult{
			Output: map[string]any{
				"success":    success,
				"message":    message,
				"latency_ms": latency.Milliseconds(),
			},
		}, nil
	}

	return &module.StepResult{
		Output: map[string]any{
			"success":    false,
			"message":    "provider registry not available",
			"latency_ms": 0,
		},
	}, nil
}

// newProviderTestFactory returns a plugin.StepFactory for "step.provider_test".
func newProviderTestFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		aliasExpr, _ := cfg["alias"].(string)
		return &ProviderTestStep{
			name:      name,
			aliasExpr: aliasExpr,
			app:       app,
			tmpl:      module.NewTemplateEngine(),
		}, nil
	}
}
