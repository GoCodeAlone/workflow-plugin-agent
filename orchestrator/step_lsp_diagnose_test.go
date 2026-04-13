package orchestrator

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow/module"
)

// mockLSPProvider implements LSPProvider for tests.
type mockLSPProvider struct {
	diags []LSPDiagnostic
	err   error
}

func (m *mockLSPProvider) DiagnoseContent(_ string) ([]LSPDiagnostic, error) {
	return m.diags, m.err
}

func TestLSPDiagnoseStep_NoContent(t *testing.T) {
	app := newMockApp()
	step := &LSPDiagnoseStep{name: "test-lsp", app: app}
	pc := &module.PipelineContext{Current: map[string]any{}}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	count, _ := result.Output["count"].(int)
	if count != 0 {
		t.Errorf("expected 0 diagnostics, got %d", count)
	}
	if result.Output["warning"] == nil {
		t.Error("expected warning when no content")
	}
}

func TestLSPDiagnoseStep_NoLSPProvider(t *testing.T) {
	app := newMockApp()
	step := &LSPDiagnoseStep{name: "test-lsp", app: app}
	pc := &module.PipelineContext{
		Current: map[string]any{"yaml": "modules:\n  - name: foo"},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output["warning"] == nil {
		t.Error("expected warning when lsp provider not available")
	}
}

func TestLSPDiagnoseStep_WithProvider(t *testing.T) {
	app := newMockApp()
	_ = app.RegisterService("ratchet-lsp", &mockLSPProvider{
		diags: []LSPDiagnostic{
			{Severity: "error", Message: "unexpected key", Range: "1:1"},
			{Severity: "warning", Message: "deprecated field", Range: "3:5"},
		},
	})

	step := &LSPDiagnoseStep{name: "test-lsp", app: app}
	pc := &module.PipelineContext{
		Current: map[string]any{"yaml": "bad: yaml: content"},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	count, _ := result.Output["count"].(int)
	if count != 2 {
		t.Errorf("expected 2 diagnostics, got %d", count)
	}
	hasErrors, _ := result.Output["has_errors"].(bool)
	if !hasErrors {
		t.Error("expected has_errors=true")
	}
}

func TestLSPDiagnoseStep_ContentKeyFallback(t *testing.T) {
	app := newMockApp()
	_ = app.RegisterService("ratchet-lsp", &mockLSPProvider{diags: nil})

	step := &LSPDiagnoseStep{name: "test-lsp", contentKey: "content", app: app}
	pc := &module.PipelineContext{
		Current: map[string]any{"content": "workflows:\n  - name: foo"},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	count, _ := result.Output["count"].(int)
	if count != 0 {
		t.Errorf("expected 0 diagnostics, got %d", count)
	}
}
