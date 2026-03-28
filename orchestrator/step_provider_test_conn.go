package orchestrator

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
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

	// Lazy-lookup ProviderRegistry (registered by wiring hook after step factories)
	svc, ok := s.app.SvcRegistry()["ratchet-provider-registry"]
	if !ok {
		return &module.StepResult{
			Output: map[string]any{
				"success":    false,
				"message":    "provider registry not available",
				"latency_ms": 0,
			},
		}, nil
	}

	registry, ok := svc.(*ProviderRegistry)
	if !ok {
		return &module.StepResult{
			Output: map[string]any{
				"success":    false,
				"message":    "provider registry does not support connection testing",
				"latency_ms": 0,
			},
		}, nil
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
