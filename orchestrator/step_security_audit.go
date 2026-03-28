package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// SecurityAuditStep runs a full security audit and returns the report as JSON.
type SecurityAuditStep struct {
	name string
	app  modular.Application
}

func (s *SecurityAuditStep) Name() string { return s.name }

func (s *SecurityAuditStep) Execute(ctx context.Context, _ *module.PipelineContext) (*module.StepResult, error) {
	// Resolve DB from service registry.
	var db *sql.DB
	if svc, ok := s.app.SvcRegistry()["ratchet-db"]; ok {
		if dbp, ok := svc.(module.DBProvider); ok {
			db = dbp.DB()
		}
	}

	auditor := NewSecurityAuditor(db, s.app)
	report := auditor.RunAll(ctx)

	// Marshal findings to a JSON-friendly structure.
	type findingJSON struct {
		Check       string `json:"check"`
		Severity    string `json:"severity"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Remediation string `json:"remediation"`
	}

	findings := make([]findingJSON, 0, len(report.Findings))
	for _, f := range report.Findings {
		findings = append(findings, findingJSON{
			Check:       f.Check,
			Severity:    string(f.Severity),
			Title:       f.Title,
			Description: f.Description,
			Remediation: f.Remediation,
		})
	}

	summary := map[string]int{}
	for severity, count := range report.Summary {
		summary[string(severity)] = count
	}

	output := map[string]any{
		"timestamp": report.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		"score":     report.Score,
		"summary":   summary,
		"findings":  findings,
	}

	// Also provide a JSON-encoded string for easy downstream consumption.
	reportJSON, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("security_audit step %q: marshal report: %w", s.name, err)
	}
	output["report_json"] = string(reportJSON)

	return &module.StepResult{Output: output}, nil
}

// newSecurityAuditFactory returns a plugin.StepFactory for "step.security_audit".
func newSecurityAuditFactory() plugin.StepFactory {
	return func(name string, _ map[string]any, app modular.Application) (any, error) {
		return &SecurityAuditStep{
			name: name,
			app:  app,
		}, nil
	}
}
