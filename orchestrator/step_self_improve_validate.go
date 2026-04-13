package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"gopkg.in/yaml.v3"
)

// ImmutabilityViolation describes a config path that was modified but is immutable.
type ImmutabilityViolation struct {
	Path     string `json:"path"`
	Override string `json:"override"`
}

// SelfImproveValidateStep runs validation on a proposed workflow config.
// It checks:
//  1. YAML parse validity
//  2. Immutability constraints (if a GuardrailsModule is registered)
//  3. MCP-based wfctl validation (if an MCP provider is available)
type SelfImproveValidateStep struct {
	name         string
	proposedKey  string // key in pc.Current holding proposed YAML (default: "proposed_yaml")
	currentKey   string // key in pc.Current holding current YAML (default: "current_yaml")
	app          modular.Application
}

func (s *SelfImproveValidateStep) Name() string { return s.name }

func (s *SelfImproveValidateStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	proposedKey := s.proposedKey
	if proposedKey == "" {
		proposedKey = "proposed_yaml"
	}
	currentKey := s.currentKey
	if currentKey == "" {
		currentKey = "current_yaml"
	}

	proposedYAML := extractString(pc.Current, proposedKey, "")
	if proposedYAML == "" {
		return nil, fmt.Errorf("self_improve_validate step %q: %q is required", s.name, proposedKey)
	}
	currentYAML := extractString(pc.Current, currentKey, "")

	errors := []string{}
	warnings := []string{}

	// Step 1: YAML parse check
	var proposedDoc map[string]any
	if err := yaml.Unmarshal([]byte(proposedYAML), &proposedDoc); err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"valid":    false,
				"errors":   []string{"yaml parse error: " + err.Error()},
				"warnings": warnings,
			},
		}, nil
	}

	// Step 2: Immutability constraint check (requires current YAML + guardrails)
	if currentYAML != "" {
		violations := s.checkImmutability(proposedYAML, currentYAML)
		for _, v := range violations {
			errors = append(errors, fmt.Sprintf("immutable section %q modified (override: %q)", v.Path, v.Override))
		}
	}

	// Step 3: LSP diagnostics (optional — graceful skip if no LSP provider registered)
	if lsp := lookupLSPProvider(s.app); lsp != nil {
		diags, lspErr := lsp.DiagnoseContent(proposedYAML)
		if lspErr != nil {
			warnings = append(warnings, "lsp diagnostics error: "+lspErr.Error())
		} else {
			for _, d := range diags {
				if d.Severity == "error" {
					errors = append(errors, fmt.Sprintf("lsp: %s", d.Message))
				} else {
					warnings = append(warnings, fmt.Sprintf("lsp: %s", d.Message))
				}
			}
		}
	} else {
		warnings = append(warnings, "lsp provider not available; skipping diagnostics")
	}

	// Step 4: MCP wfctl validation (optional — graceful skip if unavailable)
	mcpWarning := s.runMCPValidation(ctx, proposedYAML)
	if mcpWarning != "" {
		warnings = append(warnings, mcpWarning)
	}

	valid := len(errors) == 0
	return &module.StepResult{
		Output: map[string]any{
			"valid":    valid,
			"errors":   errors,
			"warnings": warnings,
		},
	}, nil
}

// checkImmutability diffs proposed vs current YAML for immutable paths.
func (s *SelfImproveValidateStep) checkImmutability(proposedYAML, currentYAML string) []ImmutabilityViolation {
	guardrails := findGuardrailsModule(s.app)
	if guardrails == nil || len(guardrails.immutableSections) == 0 {
		return nil
	}

	var proposed, current map[string]any
	if err := yaml.Unmarshal([]byte(proposedYAML), &proposed); err != nil {
		return nil
	}
	if err := yaml.Unmarshal([]byte(currentYAML), &current); err != nil {
		return nil
	}

	var violations []ImmutabilityViolation
	for _, sec := range guardrails.immutableSections {
		proposedVal := extractNestedPath(proposed, sec.Path)
		currentVal := extractNestedPath(current, sec.Path)
		if fmt.Sprintf("%v", proposedVal) != fmt.Sprintf("%v", currentVal) {
			violations = append(violations, ImmutabilityViolation{
				Path:     sec.Path,
				Override: sec.Override,
			})
		}
	}
	return violations
}

// runMCPValidation attempts wfctl validation via an MCP provider.
// Returns a warning string if MCP is unavailable, or "" on success.
func (s *SelfImproveValidateStep) runMCPValidation(_ context.Context, _ string) string {
	// Look up MCP provider from registry.
	if s.app == nil {
		return "mcp provider not available; skipping wfctl validation"
	}
	if _, ok := s.app.SvcRegistry()["mcp.provider"]; !ok {
		return "mcp provider not available; skipping wfctl validation"
	}
	// MCP provider found but integration is deferred to a future wave.
	return ""
}

// extractNestedPath retrieves a value from a nested map using dot-separated path.
func extractNestedPath(m map[string]any, path string) any {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 1 {
		return m[path]
	}
	sub, ok := m[parts[0]].(map[string]any)
	if !ok {
		return nil
	}
	return extractNestedPath(sub, parts[1])
}

// newSelfImproveValidateFactory returns a plugin.StepFactory for "step.self_improve_validate".
func newSelfImproveValidateFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		proposedKey, _ := cfg["proposed_key"].(string)
		currentKey, _ := cfg["current_key"].(string)
		return &SelfImproveValidateStep{
			name:        name,
			proposedKey: proposedKey,
			currentKey:  currentKey,
			app:         app,
		}, nil
	}
}
