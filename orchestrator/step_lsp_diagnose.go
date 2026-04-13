package orchestrator

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// LSPDiagnoseStep wraps LSP diagnostics as a pipeline step.
// It takes YAML content from current data (key: "yaml" or "content") and
// returns diagnostics. When no LSP provider is available, it returns an
// empty diagnostics list with a warning.
type LSPDiagnoseStep struct {
	name        string
	contentKey  string // config key to read YAML from (default: "yaml")
	app         modular.Application
}

func (s *LSPDiagnoseStep) Name() string { return s.name }

func (s *LSPDiagnoseStep) Execute(_ context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	key := s.contentKey
	if key == "" {
		key = "yaml"
	}

	// Resolve YAML content from current data
	yamlContent := extractString(pc.Current, key, "")
	if yamlContent == "" {
		yamlContent = extractString(pc.Current, "content", "")
	}
	if yamlContent == "" {
		return &module.StepResult{
			Output: map[string]any{
				"diagnostics": []any{},
				"count":       0,
				"warning":     "no yaml content provided",
			},
		}, nil
	}

	// Look up LSP provider from service registry.
	// The LSP provider is optional — if not wired, return empty diagnostics.
	lspProvider := s.findLSPProvider()
	if lspProvider == nil {
		return &module.StepResult{
			Output: map[string]any{
				"diagnostics": []any{},
				"count":       0,
				"warning":     "lsp provider not available; skipping diagnostics",
			},
		}, nil
	}

	diags, err := lspProvider.DiagnoseContent(yamlContent)
	if err != nil {
		return nil, fmt.Errorf("lsp_diagnose step %q: %w", s.name, err)
	}

	diagsOut := make([]map[string]any, 0, len(diags))
	for _, d := range diags {
		diagsOut = append(diagsOut, map[string]any{
			"severity": d.Severity,
			"message":  d.Message,
			"range":    d.Range,
		})
	}

	hasErrors := false
	for _, d := range diags {
		if d.Severity == "error" {
			hasErrors = true
			break
		}
	}

	return &module.StepResult{
		Output: map[string]any{
			"diagnostics": diagsOut,
			"count":       len(diagsOut),
			"has_errors":  hasErrors,
		},
	}, nil
}

// LSPDiagnostic is a single diagnostic returned by the LSP provider.
type LSPDiagnostic struct {
	Severity string `json:"severity"` // "error", "warning", "info"
	Message  string `json:"message"`
	Range    string `json:"range"`
}

// LSPProvider is the interface for in-process LSP diagnostics.
type LSPProvider interface {
	DiagnoseContent(yaml string) ([]LSPDiagnostic, error)
}

// findLSPProvider looks up any registered LSPProvider from the service registry.
func (s *LSPDiagnoseStep) findLSPProvider() LSPProvider {
	return lookupLSPProvider(s.app)
}

// lookupLSPProvider is the package-level helper used by multiple step types.
func lookupLSPProvider(app modular.Application) LSPProvider {
	if app == nil {
		return nil
	}
	for _, svc := range app.SvcRegistry() {
		if lsp, ok := svc.(LSPProvider); ok {
			return lsp
		}
	}
	return nil
}

// newLSPDiagnoseFactory returns a plugin.StepFactory for "step.lsp_diagnose".
func newLSPDiagnoseFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		contentKey, _ := cfg["content_key"].(string)
		return &LSPDiagnoseStep{
			name:       name,
			contentKey: contentKey,
			app:        app,
		}, nil
	}
}
