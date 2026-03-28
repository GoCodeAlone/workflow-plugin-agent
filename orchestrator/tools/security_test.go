package tools

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSecurityScanTool_Definition(t *testing.T) {
	tool := &SecurityScanTool{}
	if tool.Name() != "security_scan" {
		t.Fatalf("expected name security_scan, got %s", tool.Name())
	}
	def := tool.Definition()
	if def.Name != "security_scan" {
		t.Fatalf("expected def name security_scan, got %s", def.Name)
	}
}

func TestSecurityScanTool_Execute(t *testing.T) {
	tool := &SecurityScanTool{
		RunAudit: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"score": 85,
				"summary": map[string]int{
					"high":   1,
					"medium": 2,
				},
				"findings": []map[string]any{
					{"check": "auth", "severity": "high", "title": "Default credentials detected"},
					{"check": "cors", "severity": "medium", "title": "Wildcard CORS origin"},
					{"check": "rate_limit", "severity": "medium", "title": "No rate limiting configured"},
				},
				"passed_count": 9,
				"failed_count": 3,
			}, nil
		},
	}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if m["score"] != 85 {
		t.Fatalf("expected score 85, got %v", m["score"])
	}
}

func TestSecurityScanTool_Execute_NoCallback(t *testing.T) {
	tool := &SecurityScanTool{}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if _, ok := m["error"]; !ok {
		t.Fatal("expected error key when no callback configured")
	}
}

func TestVulnCheckTool_Definition(t *testing.T) {
	tool := &VulnCheckTool{}
	if tool.Name() != "vuln_check" {
		t.Fatalf("expected name vuln_check, got %s", tool.Name())
	}
	def := tool.Definition()
	params, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	if _, ok := params["module_path"]; !ok {
		t.Fatal("expected 'module_path' parameter")
	}
}

func TestVulnCheckTool_Execute_MissingPath(t *testing.T) {
	tool := &VulnCheckTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing module_path")
	}
}

// ---------- ComplianceReportTool ----------

func TestComplianceReportTool_Name(t *testing.T) {
	tool := &ComplianceReportTool{}
	if tool.Name() != "compliance_report" {
		t.Fatalf("expected name compliance_report, got %s", tool.Name())
	}
}

func TestComplianceReportTool_Execute(t *testing.T) {
	tool := &ComplianceReportTool{
		RunAudit: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"findings": []map[string]any{
					{
						"check":       "tls_enabled",
						"severity":    "high",
						"description": "TLS not configured",
						"remediation": "Enable TLS in config",
					},
				},
			}, nil
		},
	}

	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	if _, ok := m["controls"]; !ok {
		t.Fatal("expected 'controls' key in result")
	}
	if _, ok := m["score"]; !ok {
		t.Fatal("expected 'score' key in result")
	}
	if _, ok := m["summary"]; !ok {
		t.Fatal("expected 'summary' key in result")
	}
	if m["framework"] != "all" {
		t.Errorf("expected framework 'all', got %v", m["framework"])
	}

	controls, ok := m["controls"].([]map[string]any)
	if !ok {
		t.Fatalf("expected controls []map[string]any, got %T", m["controls"])
	}
	if len(controls) == 0 {
		t.Fatal("expected at least one control in result")
	}

	// Verify that tls_enabled maps to framework controls and at least one is "fail".
	foundFail := false
	for _, ctrl := range controls {
		if ctrl["check"] == "tls_enabled" && ctrl["status"] == "fail" {
			foundFail = true
			if ctrl["severity"] != "high" {
				t.Errorf("expected severity 'high' for tls_enabled control, got %v", ctrl["severity"])
			}
		}
	}
	if !foundFail {
		t.Error("expected at least one tls_enabled control with status 'fail'")
	}
}

func TestComplianceReportTool_Execute_FilterFramework(t *testing.T) {
	tool := &ComplianceReportTool{
		RunAudit: func(ctx context.Context) (map[string]any, error) {
			// Return no findings so all checks pass.
			return map[string]any{
				"findings": []map[string]any{},
			}, nil
		},
	}

	result, err := tool.Execute(context.Background(), map[string]any{"framework": "cis"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	if m["framework"] != "cis" {
		t.Errorf("expected framework 'cis', got %v", m["framework"])
	}

	controls, ok := m["controls"].([]map[string]any)
	if !ok {
		t.Fatalf("expected controls []map[string]any, got %T", m["controls"])
	}
	// Every returned control must belong to the cis framework.
	for _, ctrl := range controls {
		if ctrl["framework"] != "cis" {
			t.Errorf("expected all controls to be from 'cis' framework, got %v", ctrl["framework"])
		}
	}
	if len(controls) == 0 {
		t.Error("expected at least one CIS control in result")
	}
}

func TestComplianceReportTool_Execute_NoCallback(t *testing.T) {
	tool := &ComplianceReportTool{}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if _, ok := m["error"]; !ok {
		t.Fatal("expected error key when RunAudit is nil")
	}
}

// ---------- SecretAuditTool ----------

func setupSecretsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE secrets (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL DEFAULT '',
		created_at TEXT
	)`)
	if err != nil {
		t.Fatalf("create secrets table: %v", err)
	}
	return db
}

func TestSecretAuditTool_Name(t *testing.T) {
	tool := &SecretAuditTool{}
	if tool.Name() != "secret_audit" {
		t.Fatalf("expected name secret_audit, got %s", tool.Name())
	}
}

func TestSecretAuditTool_Execute(t *testing.T) {
	db := setupSecretsDB(t)

	// Insert a fresh secret (1 day old — should NOT need rotation with 90-day max).
	freshDate := "2024-12-01T00:00:00Z"
	// Insert a stale secret that is clearly older than 90 days relative to any
	// plausible "now" during CI (2020 timestamp).
	staleDate := "2020-01-01T00:00:00Z"

	_, _ = db.Exec("INSERT INTO secrets (key, value, created_at) VALUES (?, ?, ?)",
		"fresh_key", "v1", freshDate)
	_, _ = db.Exec("INSERT INTO secrets (key, value, created_at) VALUES (?, ?, ?)",
		"stale_key", "v2", staleDate)

	tool := &SecretAuditTool{DB: db}
	result, err := tool.Execute(context.Background(), map[string]any{
		"max_age_days": float64(90),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	if _, ok := m["secrets"]; !ok {
		t.Fatal("expected 'secrets' key in result")
	}
	if _, ok := m["total"]; !ok {
		t.Fatal("expected 'total' key in result")
	}
	if _, ok := m["needs_rotation"]; !ok {
		t.Fatal("expected 'needs_rotation' key in result")
	}

	// There should be at least the 2 DB secrets (env vars may add more).
	total, _ := m["total"].(int)
	if total < 2 {
		t.Errorf("expected at least 2 secrets, got %d", total)
	}

	// Verify via needs_rotation count (tool returns concrete struct slice).
	needsRotation, _ := m["needs_rotation"].(int)
	if needsRotation < 1 {
		t.Errorf("expected at least 1 secret needing rotation (stale_key), got %d", needsRotation)
	}
}

func TestSecretAuditTool_Execute_NilDB(t *testing.T) {
	tool := &SecretAuditTool{}
	// With no DB, only env vars are scanned — should not error.
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if _, ok := m["secrets"]; !ok {
		t.Fatal("expected 'secrets' key in result")
	}
	if _, ok := m["total"]; !ok {
		t.Fatal("expected 'total' key in result")
	}
}
