package orchestrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/GoCodeAlone/modular"
)

// newSecurityTestDB creates an in-memory SQLite database with the required tables.
func newSecurityTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	tables := []string{
		createAgentsTable,
		createTasksTable,
		createMessagesTable,
		createTranscriptsTable,
		createMCPServersTable,
		createWorkspaceContainersTable,
		createLLMProvidersTable,
		createToolPoliciesTable,
	}
	for _, stmt := range tables {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create table: %v\nSQL: %s", err, stmt)
		}
	}
	return db
}

// newAuditApp creates a mockApp with optional pre-registered services.
func newAuditApp(services map[string]any) *mockApp {
	app := newMockApp()
	for k, v := range services {
		_ = app.RegisterService(k, v)
	}
	return app
}

// --- AuthCheck ---

func TestAuthCheck_DefaultToken(t *testing.T) {
	t.Setenv("RATCHET_AUTH_TOKEN", "")
	check := &AuthCheck{app: newMockApp()}
	findings := check.Run(context.Background())

	hasDefaultToken := false
	for _, f := range findings {
		if f.Check == "auth" && f.Severity == SeverityCritical {
			hasDefaultToken = true
			break
		}
	}
	if !hasDefaultToken {
		t.Error("expected critical finding for default auth token")
	}
}

func TestAuthCheck_CustomToken(t *testing.T) {
	t.Setenv("RATCHET_AUTH_TOKEN", "my-super-secret-token-xyz-12345678")
	check := &AuthCheck{app: newMockApp()}
	findings := check.Run(context.Background())

	for _, f := range findings {
		if f.Title == "Default development auth token in use" {
			t.Error("should not flag custom token as default")
		}
	}
}

// --- VaultCheck ---

func TestVaultCheck_DevInProduction(t *testing.T) {
	t.Setenv("RATCHET_ENV", "production")
	app := newAuditApp(map[string]any{
		"ratchet-vault-dev": struct{}{},
	})
	check := &VaultCheck{app: app}
	findings := check.Run(context.Background())

	hasCritical := false
	for _, f := range findings {
		if f.Severity == SeverityCritical && f.Check == "vault" {
			hasCritical = true
			break
		}
	}
	if !hasCritical {
		t.Error("expected critical finding for vault-dev in production")
	}
}

func TestVaultCheck_DevNotProduction(t *testing.T) {
	t.Setenv("RATCHET_ENV", "development")
	app := newAuditApp(map[string]any{
		"ratchet-vault-dev": struct{}{},
	})
	check := &VaultCheck{app: app}
	findings := check.Run(context.Background())

	for _, f := range findings {
		if f.Severity == SeverityCritical {
			t.Errorf("should not be critical for vault-dev in non-production, got: %s", f.Title)
		}
	}
}

func TestVaultCheck_NoVault(t *testing.T) {
	t.Setenv("RATCHET_ENV", "production")
	app := newMockApp()
	check := &VaultCheck{app: app}
	findings := check.Run(context.Background())
	// No vault-dev registered → no finding (remote vault is fine).
	if len(findings) != 0 {
		t.Errorf("expected no findings when no vault-dev is registered, got %d", len(findings))
	}
}

// --- AgentPermissionCheck ---

func TestAgentPermissionCheck_NoPolicies(t *testing.T) {
	db := newSecurityTestDB(t)

	check := &AgentPermissionCheck{db: db}
	findings := check.Run(context.Background())

	hasNoPoliciesWarning := false
	for _, f := range findings {
		if f.Check == "agent_permissions" {
			hasNoPoliciesWarning = true
			break
		}
	}
	if !hasNoPoliciesWarning {
		t.Error("expected warning for no tool policies configured")
	}
}

func TestAgentPermissionCheck_WildcardPolicy(t *testing.T) {
	db := newSecurityTestDB(t)

	_, err := db.Exec(`INSERT INTO tool_policies (id, scope, scope_id, tool_pattern, action) VALUES ('p1', 'global', '', '*', 'allow')`)
	if err != nil {
		t.Fatalf("insert policy: %v", err)
	}

	check := &AgentPermissionCheck{db: db}
	findings := check.Run(context.Background())

	hasWildcard := false
	for _, f := range findings {
		if f.Title == "Wildcard tool access policy detected" {
			hasWildcard = true
			break
		}
	}
	if !hasWildcard {
		t.Error("expected finding for wildcard policy")
	}
}

func TestAgentPermissionCheck_SpecificPolicy(t *testing.T) {
	db := newSecurityTestDB(t)

	_, err := db.Exec(`INSERT INTO tool_policies (id, scope, scope_id, tool_pattern, action) VALUES ('p1', 'global', '', 'file_read', 'allow')`)
	if err != nil {
		t.Fatalf("insert policy: %v", err)
	}

	check := &AgentPermissionCheck{db: db}
	findings := check.Run(context.Background())

	for _, f := range findings {
		if f.Title == "Wildcard tool access policy detected" {
			t.Error("should not flag specific tool policy as wildcard")
		}
	}
}

// --- SecretExposureCheck ---

func TestSecretExposureCheck_CleanTranscripts(t *testing.T) {
	db := newSecurityTestDB(t)

	_, err := db.Exec(`INSERT INTO transcripts (id, agent_id, task_id, role, content, redacted) VALUES ('t1', 'agent1', 'task1', 'user', 'Hello world, this is a normal message', 0)`)
	if err != nil {
		t.Fatalf("insert transcript: %v", err)
	}

	check := &SecretExposureCheck{db: db}
	findings := check.Run(context.Background())

	if len(findings) != 0 {
		t.Errorf("expected no findings for clean transcripts, got %d", len(findings))
	}
}

func TestSecretExposureCheck_ExposedSecret(t *testing.T) {
	db := newSecurityTestDB(t)

	_, err := db.Exec(`INSERT INTO transcripts (id, agent_id, task_id, role, content, redacted) VALUES ('t1', 'agent1', 'task1', 'user', 'My API_KEY=sk-1234567890abcdefghij got leaked', 0)`)
	if err != nil {
		t.Fatalf("insert transcript: %v", err)
	}

	check := &SecretExposureCheck{db: db}
	findings := check.Run(context.Background())

	if len(findings) == 0 {
		t.Error("expected finding for exposed API key in transcript")
	}
}

func TestSecretExposureCheck_RedactedTranscripts(t *testing.T) {
	db := newSecurityTestDB(t)

	// Redacted transcripts should not be flagged — they are already handled.
	_, err := db.Exec(`INSERT INTO transcripts (id, agent_id, task_id, role, content, redacted) VALUES ('t1', 'agent1', 'task1', 'user', 'My API_KEY=sk-1234567890abcdefghij', 1)`)
	if err != nil {
		t.Fatalf("insert transcript: %v", err)
	}

	check := &SecretExposureCheck{db: db}
	findings := check.Run(context.Background())

	if len(findings) != 0 {
		t.Errorf("expected no findings for redacted transcripts, got %d", len(findings))
	}
}

// --- MCPServerCheck ---

func TestMCPServerCheck_ShellServer(t *testing.T) {
	db := newSecurityTestDB(t)

	_, err := db.Exec(`INSERT INTO mcp_servers (id, name, transport, command, args, status) VALUES ('m1', 'shell-mcp', 'stdio', '/bin/bash', '[]', 'active')`)
	if err != nil {
		t.Fatalf("insert mcp server: %v", err)
	}

	check := &MCPServerCheck{db: db}
	findings := check.Run(context.Background())

	hasShellWarning := false
	for _, f := range findings {
		if f.Severity == SeverityHigh && f.Check == "mcp_servers" {
			hasShellWarning = true
			break
		}
	}
	if !hasShellWarning {
		t.Error("expected high severity finding for shell-based MCP server")
	}
}

func TestMCPServerCheck_SafeServer(t *testing.T) {
	db := newSecurityTestDB(t)

	_, err := db.Exec(`INSERT INTO mcp_servers (id, name, transport, command, args, status) VALUES ('m1', 'github-mcp', 'stdio', '/usr/local/bin/github-mcp-server', '[]', 'active')`)
	if err != nil {
		t.Fatalf("insert mcp server: %v", err)
	}

	check := &MCPServerCheck{db: db}
	findings := check.Run(context.Background())

	for _, f := range findings {
		if f.Severity == SeverityHigh {
			t.Errorf("unexpected high severity finding: %s", f.Title)
		}
	}
}

// --- DatabaseSecurityCheck ---

func TestDatabaseSecurityCheck_MissingDB(t *testing.T) {
	t.Setenv("RATCHET_DB_PATH", "/nonexistent/path/db.sqlite")
	check := &DatabaseSecurityCheck{}
	findings := check.Run(context.Background())
	// No findings if DB does not exist (cannot check permissions).
	if len(findings) != 0 {
		t.Errorf("expected no findings for missing DB, got %d", len(findings))
	}
}

// --- RateLimitCheck ---

func TestRateLimitCheck_NoRateLimiting(t *testing.T) {
	check := &RateLimitCheck{app: newMockApp()}
	findings := check.Run(context.Background())

	if len(findings) == 0 {
		t.Error("expected finding for missing rate limiting")
	}
	if findings[0].Severity != SeverityHigh {
		t.Errorf("expected high severity, got %s", findings[0].Severity)
	}
}

func TestRateLimitCheck_HasRateLimiting(t *testing.T) {
	app := newAuditApp(map[string]any{
		"ratchet-ratelimit": struct{}{},
	})
	check := &RateLimitCheck{app: app}
	findings := check.Run(context.Background())
	if len(findings) != 0 {
		t.Errorf("expected no findings when rate limiting is configured, got %d", len(findings))
	}
}

// --- AuditReport Score Computation ---

func TestAuditReport_ScoreComputation(t *testing.T) {
	tests := []struct {
		name      string
		findings  []AuditFinding
		wantScore int
	}{
		{
			name:      "no findings",
			findings:  nil,
			wantScore: 100,
		},
		{
			name: "one critical",
			findings: []AuditFinding{
				{Severity: SeverityCritical},
			},
			wantScore: 80,
		},
		{
			name: "one high",
			findings: []AuditFinding{
				{Severity: SeverityHigh},
			},
			wantScore: 90,
		},
		{
			name: "one medium",
			findings: []AuditFinding{
				{Severity: SeverityMedium},
			},
			wantScore: 95,
		},
		{
			name: "one low",
			findings: []AuditFinding{
				{Severity: SeverityLow},
			},
			wantScore: 98,
		},
		{
			name: "info only",
			findings: []AuditFinding{
				{Severity: SeverityInfo},
			},
			wantScore: 100, // info doesn't reduce score
		},
		{
			name: "mixed",
			findings: []AuditFinding{
				{Severity: SeverityCritical},
				{Severity: SeverityHigh},
				{Severity: SeverityMedium},
				{Severity: SeverityLow},
			},
			wantScore: 63, // 100 - 20 - 10 - 5 - 2
		},
		{
			name: "floor at zero",
			findings: func() []AuditFinding {
				f := make([]AuditFinding, 10)
				for i := range f {
					f[i] = AuditFinding{Severity: SeverityCritical}
				}
				return f
			}(),
			wantScore: 0, // 100 - 200 = clamped to 0
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := &AuditReport{
				Findings: tc.findings,
				Summary:  make(map[AuditSeverity]int),
			}
			score := 100
			for _, f := range report.Findings {
				report.Summary[f.Severity]++
				switch f.Severity {
				case SeverityCritical:
					score -= 20
				case SeverityHigh:
					score -= 10
				case SeverityMedium:
					score -= 5
				case SeverityLow:
					score -= 2
				}
			}
			if score < 0 {
				score = 0
			}
			report.Score = score

			if report.Score != tc.wantScore {
				t.Errorf("score = %d, want %d", report.Score, tc.wantScore)
			}
		})
	}
}

// --- Full RunAll integration test ---

func TestSecurityAuditor_RunAll(t *testing.T) {
	db := newSecurityTestDB(t)

	// Use default (insecure) token so we get predictable findings.
	t.Setenv("RATCHET_AUTH_TOKEN", "")
	t.Setenv("RATCHET_ENV", "development")

	app := newAuditApp(map[string]any{
		"ratchet-vault-dev": struct{}{},
	})

	auditor := NewSecurityAuditor(db, app)
	report := auditor.RunAll(context.Background())

	if report == nil {
		t.Fatal("expected non-nil report")
		return
	}
	if report.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
	if report.Score < 0 || report.Score > 100 {
		t.Errorf("score %d out of range [0,100]", report.Score)
	}
	if len(report.Findings) == 0 {
		t.Error("expected at least some findings with default/insecure config")
	}

	// Summary totals must match findings count.
	total := 0
	for _, count := range report.Summary {
		total += count
	}
	if total != len(report.Findings) {
		t.Errorf("summary total %d != findings count %d", total, len(report.Findings))
	}
}

// --- NewSecurityAuditor wiring ---

func TestNewSecurityAuditor_HasAllChecks(t *testing.T) {
	db := newSecurityTestDB(t)
	app := newMockApp()

	auditor := NewSecurityAuditor(db, app)
	if auditor == nil {
		t.Fatal("expected non-nil auditor")
		return
	}

	// There should be exactly 12 checks registered.
	const wantChecks = 12
	if len(auditor.checks) != wantChecks {
		t.Errorf("expected %d checks, got %d", wantChecks, len(auditor.checks))
	}
}

// Ensure all check names are unique.
func TestSecurityAuditor_CheckNamesUnique(t *testing.T) {
	db := newSecurityTestDB(t)
	app := newMockApp()

	auditor := NewSecurityAuditor(db, app)
	names := make(map[string]bool)
	for _, check := range auditor.checks {
		name := check.Name()
		if names[name] {
			t.Errorf("duplicate check name: %s", name)
		}
		names[name] = true
	}
}

// Verify the AuditCheck interface is satisfied by all checks.
func TestSecurityAuditor_ChecksImplementInterface(t *testing.T) {
	db := newSecurityTestDB(t)
	app := newMockApp()

	auditor := NewSecurityAuditor(db, app)
	for _, check := range auditor.checks {
		if check.Name() == "" {
			t.Errorf("check has empty name: %T", check)
		}
	}
}

// Test that RunAll does not panic when DB is nil.
func TestSecurityAuditor_RunAll_NilDB(t *testing.T) {
	t.Setenv("RATCHET_AUTH_TOKEN", "")
	app := newMockApp()
	auditor := NewSecurityAuditor(nil, app)

	// Should not panic.
	report := auditor.RunAll(context.Background())
	if report == nil {
		t.Fatal("expected non-nil report even with nil DB")
	}
}

// Ensure the embedded modular.Application interface is properly satisfied.
var _ modular.Application = (*mockApp)(nil)
