package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// SecurityScanTool runs a platform security audit and returns structured findings.
type SecurityScanTool struct {
	// RunAudit is a callback injected at registration time that calls SecurityAuditor.RunAll().
	// This avoids importing the full auditor into the tools package.
	RunAudit func(ctx context.Context) (map[string]any, error)
}

func (t *SecurityScanTool) Name() string { return "security_scan" }
func (t *SecurityScanTool) Description() string {
	return "Run a platform security audit (12-point assessment) and return findings"
}
func (t *SecurityScanTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "security_scan",
		Description: "Run a comprehensive security audit on the Ratchet platform. Returns findings categorized by severity (critical, high, medium, low, info) with a security score (0-100).",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *SecurityScanTool) Execute(ctx context.Context, _ map[string]any) (any, error) {
	if t.RunAudit == nil {
		return map[string]any{"error": "security audit not configured", "score": 0, "findings": []any{}}, nil
	}
	return t.RunAudit(ctx)
}

// VulnCheckTool runs govulncheck on a Go module to find known vulnerabilities.
type VulnCheckTool struct{}

func (t *VulnCheckTool) Name() string { return "vuln_check" }
func (t *VulnCheckTool) Description() string {
	return "Check Go module dependencies for known vulnerabilities using govulncheck"
}
func (t *VulnCheckTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "vuln_check",
		Description: "Run govulncheck on a Go module to find known CVEs in dependencies. Returns vulnerability list with severity and fix versions.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"module_path": map[string]any{
					"type":        "string",
					"description": "Path to the Go module directory (must contain go.mod)",
				},
			},
			"required": []string{"module_path"},
		},
	}
}

func (t *VulnCheckTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	modulePath, ok := args["module_path"].(string)
	if !ok || modulePath == "" {
		return nil, fmt.Errorf("vuln_check: 'module_path' is required")
	}

	vulnPath, err := exec.LookPath("govulncheck")
	if err != nil {
		return map[string]any{
			"error":           "govulncheck not installed (go install golang.org/x/vuln/cmd/govulncheck@latest)",
			"vulnerabilities": []any{},
			"count":           0,
		}, nil
	}

	execCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, vulnPath, "-json", "./...")
	cmd.Dir = modulePath
	out, _ := cmd.CombinedOutput()

	return t.parseVulnOutput(out)
}

func (t *VulnCheckTool) parseVulnOutput(out []byte) (any, error) {
	vulns := []map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			break
		}
		if finding, ok := entry["finding"].(map[string]any); ok {
			osv, _ := finding["osv"].(string)
			vulns = append(vulns, map[string]any{
				"id":      osv,
				"finding": finding,
			})
		}
		if osv, ok := entry["osv"].(map[string]any); ok {
			id, _ := osv["id"].(string)
			summary, _ := osv["summary"].(string)
			vulns = append(vulns, map[string]any{
				"id":      id,
				"summary": summary,
				"details": osv,
			})
		}
	}

	if len(vulns) == 0 && len(out) > 0 {
		return map[string]any{
			"vulnerabilities": []any{},
			"count":           0,
			"raw":             string(out),
		}, nil
	}

	return map[string]any{
		"vulnerabilities": vulns,
		"count":           len(vulns),
	}, nil
}

// ---- ComplianceReportTool ---------------------------------------------------

// complianceMapping maps security audit check names to one or more framework
// controls. Each check can appear in multiple frameworks simultaneously.
var complianceMapping = map[string][]struct{ framework, id, title string }{
	"auth_enabled": {
		{"cis", "CIS-5.1", "Authentication Required"},
		{"owasp", "OWASP-A07", "Identification and Authentication Failures"},
		{"soc2", "SOC2-CC6.1", "Logical Access Security"},
	},
	"cors_configured": {
		{"owasp", "OWASP-A05", "Security Misconfiguration"},
		{"soc2", "SOC2-CC6.6", "System Boundary Protection"},
	},
	"rate_limiting": {
		{"owasp", "OWASP-A04", "Insecure Design"},
		{"soc2", "SOC2-CC6.1", "Resource Access Control"},
	},
	"tls_enabled": {
		{"cis", "CIS-4.1", "Transport Encryption"},
		{"owasp", "OWASP-A02", "Cryptographic Failures"},
		{"soc2", "SOC2-CC6.7", "Data Encryption in Transit"},
	},
	"secrets_protected": {
		{"cis", "CIS-13.1", "Sensitive Data Protection"},
		{"owasp", "OWASP-A02", "Cryptographic Failures"},
		{"soc2", "SOC2-CC6.1", "Logical Access Security"},
	},
	"input_validation": {
		{"owasp", "OWASP-A03", "Injection"},
		{"soc2", "SOC2-CC8.1", "Change Management"},
	},
	"audit_logging": {
		{"cis", "CIS-8.1", "Audit Log Management"},
		{"owasp", "OWASP-A09", "Security Logging and Monitoring Failures"},
		{"soc2", "SOC2-CC7.2", "System Monitoring"},
	},
	"dependency_check": {
		{"cis", "CIS-16.1", "Application Software Security"},
		{"owasp", "OWASP-A06", "Vulnerable and Outdated Components"},
		{"soc2", "SOC2-CC8.1", "Change Management"},
	},
}

// ComplianceReportTool maps security audit findings to compliance framework controls.
type ComplianceReportTool struct {
	// RunAudit is a callback injected at registration time.
	RunAudit func(ctx context.Context) (map[string]any, error)
}

func (t *ComplianceReportTool) Name() string { return "compliance_report" }
func (t *ComplianceReportTool) Description() string {
	return "Map security audit findings to compliance framework controls (CIS, OWASP, SOC2)"
}
func (t *ComplianceReportTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "compliance_report",
		Description: "Run the platform security audit and map findings to compliance framework controls (CIS, OWASP Top 10, SOC 2). Returns a scored report with pass/fail status per control.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"framework": map[string]any{
					"type":        "string",
					"description": "Framework filter: 'cis', 'owasp', 'soc2', or 'all' (default: all)",
				},
			},
		},
	}
}

func (t *ComplianceReportTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	if t.RunAudit == nil {
		return map[string]any{"error": "security audit not configured"}, nil
	}

	framework := "all"
	if v, ok := args["framework"].(string); ok && v != "" {
		framework = strings.ToLower(v)
	}

	auditResult, err := t.RunAudit(ctx)
	if err != nil {
		return nil, fmt.Errorf("compliance_report: audit failed: %w", err)
	}

	// Build a set of failed check names from audit findings
	failedChecks := map[string]struct{ severity, description, remediation string }{}
	if findings, ok := auditResult["findings"].([]map[string]any); ok {
		for _, f := range findings {
			check, _ := f["check"].(string)
			sev, _ := f["severity"].(string)
			desc, _ := f["description"].(string)
			rem, _ := f["remediation"].(string)
			failedChecks[check] = struct{ severity, description, remediation string }{sev, desc, rem}
		}
	}

	generatedAt := time.Now().UTC().Format(time.RFC3339)
	controls := []map[string]any{}
	pass, fail, warn := 0, 0, 0

	for checkName, mappings := range complianceMapping {
		for _, m := range mappings {
			if framework != "all" && m.framework != framework {
				continue
			}
			status := "pass"
			severity := "info"
			evidence := "check passed"
			remediation := ""

			if fc, failed := failedChecks[checkName]; failed {
				status = "fail"
				severity = fc.severity
				evidence = fc.description
				remediation = fc.remediation
				fail++
			} else {
				pass++
			}

			controls = append(controls, map[string]any{
				"id":          m.id,
				"framework":   m.framework,
				"title":       m.title,
				"check":       checkName,
				"status":      status,
				"severity":    severity,
				"evidence":    evidence,
				"remediation": remediation,
			})
		}
	}

	// Score: percentage of controls that pass
	total := pass + fail + warn
	score := 0
	if total > 0 {
		score = pass * 100 / total
	}

	return map[string]any{
		"framework":    framework,
		"generated_at": generatedAt,
		"score":        score,
		"controls":     controls,
		"summary": map[string]int{
			"pass": pass,
			"fail": fail,
			"warn": warn,
		},
	}, nil
}

// ---- SecretAuditTool --------------------------------------------------------

// SecretAuditTool checks for stale secrets in the database and environment.
type SecretAuditTool struct {
	DB *sql.DB
}

func (t *SecretAuditTool) Name() string { return "secret_audit" }
func (t *SecretAuditTool) Description() string {
	return "Audit secrets for staleness: check database secrets table and RATCHET_* env vars against a max-age threshold"
}
func (t *SecretAuditTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "secret_audit",
		Description: "Audit secrets for age and rotation needs. Checks the `secrets` database table (if present) and RATCHET_* environment variables. Returns secrets older than max_age_days as needing rotation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"max_age_days": map[string]any{
					"type":        "integer",
					"description": "Maximum allowed secret age in days before rotation is required (default: 90)",
				},
			},
		},
	}
}

func (t *SecretAuditTool) Execute(_ context.Context, args map[string]any) (any, error) {
	maxAgeDays := 90
	if v, ok := args["max_age_days"].(float64); ok && v > 0 {
		maxAgeDays = int(v)
	}

	type secretEntry struct {
		Name          string `json:"name"`
		Source        string `json:"source"`
		AgeDays       int    `json:"age_days"`
		NeedsRotation bool   `json:"needs_rotation"`
	}

	secrets := []secretEntry{}
	needsRotationCount := 0
	envBasedCount := 0

	// Check database secrets table if available
	if t.DB != nil {
		// Check if the secrets table exists
		var tableName string
		err := t.DB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='secrets'").Scan(&tableName)
		if err == nil {
			// Table exists — query it
			rows, err := t.DB.Query("SELECT key, created_at FROM secrets")
			if err == nil {
				defer func() { _ = rows.Close() }()
				for rows.Next() {
					var key string
					var createdAt sql.NullString
					if err := rows.Scan(&key, &createdAt); err != nil {
						continue
					}
					ageDays := -1 // unknown
					needsRotation := false
					if createdAt.Valid && createdAt.String != "" {
						// Try common SQLite timestamp formats
						for _, layout := range []string{
							time.RFC3339,
							"2006-01-02T15:04:05Z",
							"2006-01-02 15:04:05",
							"2006-01-02",
						} {
							if parsed, parseErr := time.Parse(layout, createdAt.String); parseErr == nil {
								ageDays = int(time.Since(parsed).Hours() / 24)
								needsRotation = ageDays > maxAgeDays
								break
							}
						}
					}
					if needsRotation {
						needsRotationCount++
					}
					secrets = append(secrets, secretEntry{
						Name:          key,
						Source:        "database",
						AgeDays:       ageDays,
						NeedsRotation: needsRotation,
					})
				}
				_ = rows.Close()
			}
		}
	}

	// Scan RATCHET_* environment variables
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "RATCHET_") {
			continue
		}
		parts := strings.SplitN(env, "=", 2)
		name := strings.TrimPrefix(parts[0], "RATCHET_")
		// No creation date available for env vars — flag if maxAgeDays is very low
		needsRotation := maxAgeDays <= 0
		if needsRotation {
			needsRotationCount++
		}
		envBasedCount++
		secrets = append(secrets, secretEntry{
			Name:          name,
			Source:        "env",
			AgeDays:       -1,
			NeedsRotation: needsRotation,
		})
	}

	return map[string]any{
		"secrets":        secrets,
		"total":          len(secrets),
		"needs_rotation": needsRotationCount,
		"env_based":      envBasedCount,
		"max_age_days":   maxAgeDays,
	}, nil
}
