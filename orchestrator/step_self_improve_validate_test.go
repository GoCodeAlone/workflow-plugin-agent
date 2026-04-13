package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow/module"
)

func TestSelfImproveValidateStep_ValidYAML(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveValidateStep{name: "test-validate", app: app}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"proposed_yaml": "modules:\n  - name: foo\n    type: ratchet.sse_hub\n",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	valid, _ := result.Output["valid"].(bool)
	if !valid {
		t.Errorf("expected valid=true, got errors: %v", result.Output["errors"])
	}
}

func TestSelfImproveValidateStep_InvalidYAML(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveValidateStep{name: "test-validate", app: app}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"proposed_yaml": "{\ninvalid: [yaml: content",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	valid, _ := result.Output["valid"].(bool)
	if valid {
		t.Error("expected valid=false for invalid YAML")
	}
	errs, _ := result.Output["errors"].([]string)
	if len(errs) == 0 {
		t.Error("expected error messages for invalid YAML")
	}
}

func TestSelfImproveValidateStep_MissingProposed(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveValidateStep{name: "test-validate", app: app}

	pc := &module.PipelineContext{Current: map[string]any{}}
	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Error("expected error when proposed_yaml is missing")
	}
}

func TestSelfImproveValidateStep_ImmutabilityViolation(t *testing.T) {
	gm := NewGuardrailsModule("test-guardrails", GuardrailsDefaults{})
	gm.immutableSections = []ImmutableSection{
		{Path: "security.tls", Override: "challenge_token"},
	}
	app := newMockApp()
	_ = app.RegisterService("test-guardrails", gm)

	step := &SelfImproveValidateStep{name: "test-validate", app: app}
	pc := &module.PipelineContext{
		Current: map[string]any{
			"current_yaml":  "security:\n  tls: true\n",
			"proposed_yaml": "security:\n  tls: false\n",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	valid, _ := result.Output["valid"].(bool)
	if valid {
		t.Error("expected valid=false when immutable section is modified")
	}
	errs, _ := result.Output["errors"].([]string)
	if len(errs) == 0 {
		t.Error("expected immutability violation error")
	}
}

func TestSelfImproveValidateStep_EmptyProposedYAML(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveValidateStep{name: "test-validate", app: app}

	cases := []struct {
		name  string
		value string
	}{
		{"empty string", ""},
		{"whitespace only", "   \n\t  "},
		{"no value template", "<no value>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := &module.PipelineContext{
				Current: map[string]any{
					"proposed_yaml": tc.value,
				},
			}
			_, err := step.Execute(context.Background(), pc)
			if err == nil {
				t.Errorf("expected error for proposed_yaml=%q, got nil", tc.value)
			}
			if err != nil && !strings.Contains(err.Error(), "proposed_yaml is empty or not set") {
				t.Errorf("unexpected error message: %v", err)
			}
		})
	}
}

func TestSelfImproveValidateStep_MCPUnavailable(t *testing.T) {
	app := newMockApp() // no mcp.provider
	step := &SelfImproveValidateStep{name: "test-validate", app: app}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"proposed_yaml": "modules: []\n",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	warnings, _ := result.Output["warnings"].([]string)
	hasLSPWarn := false
	for _, w := range warnings {
		if strings.Contains(w, "lsp provider not available") {
			hasLSPWarn = true
			break
		}
	}
	if !hasLSPWarn {
		t.Errorf("expected lsp-unavailable warning, got warnings: %v", warnings)
	}
}
