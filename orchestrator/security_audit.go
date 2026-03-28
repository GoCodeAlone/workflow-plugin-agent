package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/GoCodeAlone/modular"
)

// AuditSeverity represents the severity of an audit finding.
type AuditSeverity string

const (
	SeverityCritical AuditSeverity = "critical"
	SeverityHigh     AuditSeverity = "high"
	SeverityMedium   AuditSeverity = "medium"
	SeverityLow      AuditSeverity = "low"
	SeverityInfo     AuditSeverity = "info"
)

// AuditFinding represents a single security finding.
type AuditFinding struct {
	Check       string
	Severity    AuditSeverity
	Title       string
	Description string
	Remediation string
}

// AuditReport contains all findings from a security audit run.
type AuditReport struct {
	Timestamp time.Time
	Findings  []AuditFinding
	Summary   map[AuditSeverity]int // count by severity
	Score     int                   // 0-100, higher is better
}

// AuditCheck is implemented by each individual security check.
type AuditCheck interface {
	Name() string
	Run(ctx context.Context) []AuditFinding
}

// SecurityAuditor orchestrates all audit checks.
type SecurityAuditor struct {
	db     *sql.DB
	app    modular.Application
	checks []AuditCheck
}

// NewSecurityAuditor creates a SecurityAuditor wired with all built-in checks.
func NewSecurityAuditor(db *sql.DB, app modular.Application) *SecurityAuditor {
	sa := &SecurityAuditor{db: db, app: app}
	sa.checks = []AuditCheck{
		&AuthCheck{app: app},
		&ProviderSecurityCheck{db: db, app: app},
		&AgentPermissionCheck{db: db},
		&VaultCheck{app: app},
		&CORSCheck{app: app},
		&RateLimitCheck{app: app},
		&ContainerSecurityCheck{db: db},
		&DatabaseSecurityCheck{},
		&MCPServerCheck{db: db},
		&SecretExposureCheck{db: db},
		&WebhookSecurityCheck{db: db},
		&DefaultCredentialCheck{app: app},
	}
	return sa
}

// RunAll executes all checks and returns an AuditReport.
func (sa *SecurityAuditor) RunAll(ctx context.Context) *AuditReport {
	report := &AuditReport{
		Timestamp: time.Now(),
		Summary:   make(map[AuditSeverity]int),
	}

	for _, check := range sa.checks {
		findings := check.Run(ctx)
		report.Findings = append(report.Findings, findings...)
	}

	// Tally summary and compute score.
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
	return report
}

// ---------------------------------------------------------------------------
// Check 1: AuthCheck — verify auth is enabled, check for default tokens
// ---------------------------------------------------------------------------

// AuthCheck verifies that authentication is properly configured.
type AuthCheck struct {
	app modular.Application
}

func (c *AuthCheck) Name() string { return "auth" }

// authMiddleware is a marker interface implemented by authentication middleware services.
// Any service that wants to be recognised as auth middleware by the security auditor
// should implement this interface.
type authMiddleware interface {
	IsAuthMiddleware() bool
}

func (c *AuthCheck) Run(_ context.Context) []AuditFinding {
	var findings []AuditFinding

	// Check if any auth middleware exists in the service registry.
	hasAuth := false
	for _, svc := range c.app.SvcRegistry() {
		// Check service name pattern for auth-related services.
		type namer interface{ Name() string }
		if n, ok := svc.(namer); ok {
			name := n.Name()
			if strings.Contains(strings.ToLower(name), "auth") {
				hasAuth = true
				break
			}
		}
		// Check for the authMiddleware marker interface (preferred over type-string comparison).
		if am, ok := svc.(authMiddleware); ok && am.IsAuthMiddleware() {
			hasAuth = true
			break
		}
	}

	if !hasAuth {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityCritical,
			Title:       "No authentication middleware detected",
			Description: "The platform does not appear to have any authentication middleware configured.",
			Remediation: "Configure http.middleware.auth in ratchet.yaml with a strong secret.",
		})
	}

	// Check for default dev token in use.
	token := os.Getenv("RATCHET_AUTH_TOKEN")
	if token == "" || token == defaultDevToken {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityCritical,
			Title:       "Default development auth token in use",
			Description: "The default insecure auth token is being used. This allows unauthorized access.",
			Remediation: "Set the RATCHET_AUTH_TOKEN environment variable to a strong, random secret.",
		})
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 2: ProviderSecurityCheck — verify API keys are in vault, not plaintext
// ---------------------------------------------------------------------------

// ProviderSecurityCheck verifies AI provider API keys are stored in the vault.
type ProviderSecurityCheck struct {
	db  *sql.DB
	app modular.Application
}

func (c *ProviderSecurityCheck) Name() string { return "provider_security" }

func (c *ProviderSecurityCheck) Run(ctx context.Context) []AuditFinding {
	var findings []AuditFinding
	if c.db == nil {
		return findings
	}

	// Check llm_providers for providers that may have plaintext API keys.
	rows, err := c.db.QueryContext(ctx,
		`SELECT alias, type, secret_name FROM llm_providers WHERE status != 'deleted'`)
	if err != nil {
		return findings
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var alias, provType, secretName string
		if err := rows.Scan(&alias, &provType, &secretName); err != nil {
			continue
		}
		// Providers that require API keys should have a secret_name referencing vault.
		needsKey := provType == "anthropic" || provType == "openai" || provType == "openai-compatible"
		if needsKey && secretName == "" {
			findings = append(findings, AuditFinding{
				Check:       c.Name(),
				Severity:    SeverityHigh,
				Title:       fmt.Sprintf("Provider %q has no secret configured", alias),
				Description: fmt.Sprintf("AI provider %q (type: %s) does not reference a vault secret for its API key.", alias, provType),
				Remediation: "Configure the provider's API key via the vault using step.secret_manage.",
			})
		}
	}

	// Check for RATCHET_* env vars that look like API keys.
	sensitivePatterns := []string{"API_KEY", "SECRET", "TOKEN", "PASSWORD"}
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if !strings.HasPrefix(key, "RATCHET_") {
			continue
		}
		for _, pattern := range sensitivePatterns {
			if strings.Contains(key, pattern) {
				findings = append(findings, AuditFinding{
					Check:       c.Name(),
					Severity:    SeverityMedium,
					Title:       fmt.Sprintf("Sensitive credential in environment variable %q", key),
					Description: "Credentials should be stored in the vault, not in environment variables.",
					Remediation: "Move this credential to the vault using step.secret_manage and remove the env var.",
				})
				break
			}
		}
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 3: AgentPermissionCheck — flag agents with access to all tools
// ---------------------------------------------------------------------------

// AgentPermissionCheck flags agents that have unrestricted tool access.
type AgentPermissionCheck struct {
	db *sql.DB
}

func (c *AgentPermissionCheck) Name() string { return "agent_permissions" }

func (c *AgentPermissionCheck) Run(ctx context.Context) []AuditFinding {
	var findings []AuditFinding
	if c.db == nil {
		return findings
	}

	// Check for wildcard allow policies.
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, scope, scope_id, tool_pattern FROM tool_policies WHERE action = 'allow' AND tool_pattern = '*'`)
	if err != nil {
		return findings
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, scope, scopeID, pattern string
		if err := rows.Scan(&id, &scope, &scopeID, &pattern); err != nil {
			continue
		}
		desc := fmt.Sprintf("Policy %q grants wildcard tool access at %s scope", id, scope)
		if scopeID != "" {
			desc = fmt.Sprintf("Policy %q grants wildcard tool access at %s scope (id: %s)", id, scope, scopeID)
		}
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityMedium,
			Title:       "Wildcard tool access policy detected",
			Description: desc,
			Remediation: "Replace wildcard policies with specific tool grants using group: prefixes or explicit tool names.",
		})
	}

	// If no tool_policies table rows at all, agents default to full access.
	var count int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_policies`).Scan(&count); err == nil && count == 0 {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityMedium,
			Title:       "No tool access policies configured",
			Description: "All agents have unrestricted access to all tools by default (no policies are defined).",
			Remediation: "Define tool policies using the /api/policies endpoints to restrict agent tool access.",
		})
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 4: VaultCheck — flag vault-dev in production
// ---------------------------------------------------------------------------

// VaultCheck flags when vault-dev is used in a production environment.
type VaultCheck struct {
	app modular.Application
}

func (c *VaultCheck) Name() string { return "vault" }

func (c *VaultCheck) Run(_ context.Context) []AuditFinding {
	var findings []AuditFinding

	env := os.Getenv("RATCHET_ENV")
	isProduction := strings.ToLower(env) == "production"

	// Check for vault-dev in service registry.
	_, hasVaultDev := c.app.SvcRegistry()["ratchet-vault-dev"]

	if hasVaultDev && isProduction {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityCritical,
			Title:       "Vault-dev backend in use in production",
			Description: "The development vault backend is not suitable for production use. It stores secrets in memory and is not persistent.",
			Remediation: "Configure a remote HashiCorp Vault instance using step.vault_config with action: configure.",
		})
	} else if hasVaultDev {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityInfo,
			Title:       "Vault-dev backend in use",
			Description: "The development vault backend is active. Secrets are stored in memory and will be lost on restart.",
			Remediation: "For persistent secrets, configure a remote HashiCorp Vault instance.",
		})
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 5: CORSCheck — flag wildcard CORS origins
// ---------------------------------------------------------------------------

// CORSCheck flags wildcard CORS configurations.
type CORSCheck struct {
	app modular.Application
}

func (c *CORSCheck) Name() string { return "cors" }

func (c *CORSCheck) Run(_ context.Context) []AuditFinding {
	var findings []AuditFinding

	env := os.Getenv("RATCHET_ENV")
	isProduction := strings.ToLower(env) == "production"

	// Look for CORS middleware in service registry.
	for key, svc := range c.app.SvcRegistry() {
		if !strings.Contains(strings.ToLower(key), "cors") {
			continue
		}
		// Try to inspect CORS config via type assertion to known interface.
		type corsConfigGetter interface {
			AllowedOrigins() []string
		}
		if cg, ok := svc.(corsConfigGetter); ok {
			origins := cg.AllowedOrigins()
			for _, o := range origins {
				if o == "*" && isProduction {
					findings = append(findings, AuditFinding{
						Check:       c.Name(),
						Severity:    SeverityHigh,
						Title:       "Wildcard CORS origin in production",
						Description: "The CORS middleware allows requests from any origin (*). This is insecure in production.",
						Remediation: "Set allowedOrigins to specific domains in ratchet.yaml CORS middleware config.",
					})
					break
				}
			}
		}
	}

	// Check env var for CORS override.
	corsOrigin := os.Getenv("RATCHET_CORS_ORIGIN")
	if corsOrigin == "*" && isProduction {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityHigh,
			Title:       "Wildcard CORS origin via environment variable",
			Description: "RATCHET_CORS_ORIGIN is set to * which allows all origins in production.",
			Remediation: "Set RATCHET_CORS_ORIGIN to your specific frontend domain.",
		})
	}

	if isProduction && len(findings) == 0 {
		// Try a heuristic: check if the ratchet.yaml config mentions allowedOrigins: ["*"].
		// We do a best-effort file read; if unavailable, skip.
		data, err := os.ReadFile("ratchet.yaml")
		if err == nil {
			content := string(data)
			if strings.Contains(content, `allowedOrigins: ["*"]`) ||
				strings.Contains(content, "allowedOrigins:\n        - \"*\"") ||
				strings.Contains(content, "allowedOrigins:\n      - \"*\"") {
				findings = append(findings, AuditFinding{
					Check:       c.Name(),
					Severity:    SeverityHigh,
					Title:       "Wildcard CORS origin in ratchet.yaml",
					Description: "ratchet.yaml configures CORS to allow all origins (*) which is insecure in production.",
					Remediation: "Update allowedOrigins in ratchet.yaml to list specific trusted domains.",
				})
			}
		}
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 6: RateLimitCheck — verify rate limiting is enabled and reasonable
// ---------------------------------------------------------------------------

// RateLimitCheck verifies rate limiting is properly configured.
type RateLimitCheck struct {
	app modular.Application
}

func (c *RateLimitCheck) Name() string { return "rate_limit" }

func (c *RateLimitCheck) Run(_ context.Context) []AuditFinding {
	var findings []AuditFinding

	hasRateLimit := false
	for key := range c.app.SvcRegistry() {
		if strings.Contains(strings.ToLower(key), "ratelimit") ||
			strings.Contains(strings.ToLower(key), "rate_limit") ||
			strings.Contains(strings.ToLower(key), "rate-limit") {
			hasRateLimit = true
			break
		}
	}

	if !hasRateLimit {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityHigh,
			Title:       "No rate limiting configured",
			Description: "No rate limiting middleware was detected. The API is vulnerable to abuse and DoS attacks.",
			Remediation: "Add http.middleware.ratelimit to ratchet.yaml with appropriate requestsPerMinute and burstSize settings.",
		})
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 7: ContainerSecurityCheck — check for privileged containers
// ---------------------------------------------------------------------------

// ContainerSecurityCheck inspects container configurations for security issues.
type ContainerSecurityCheck struct {
	db *sql.DB
}

func (c *ContainerSecurityCheck) Name() string { return "container_security" }

func (c *ContainerSecurityCheck) Run(ctx context.Context) []AuditFinding {
	var findings []AuditFinding
	if c.db == nil {
		return findings
	}

	rows, err := c.db.QueryContext(ctx,
		`SELECT id, project_id, image, compose_file FROM workspace_containers WHERE status != 'stopped'`)
	if err != nil {
		return findings
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, projectID, image, composeFile string
		if err := rows.Scan(&id, &projectID, &image, &composeFile); err != nil {
			continue
		}

		// Check for privileged mode in compose file.
		if strings.Contains(composeFile, "privileged: true") {
			findings = append(findings, AuditFinding{
				Check:       c.Name(),
				Severity:    SeverityCritical,
				Title:       fmt.Sprintf("Privileged container detected for project %q", projectID),
				Description: "Container is running in privileged mode, which grants full host access.",
				Remediation: "Remove 'privileged: true' from the container configuration and use specific capability grants instead.",
			})
		}

		// Warn if using a very permissive image.
		if image == "" || image == "ubuntu:latest" || image == "debian:latest" {
			findings = append(findings, AuditFinding{
				Check:       c.Name(),
				Severity:    SeverityLow,
				Title:       fmt.Sprintf("Container %q uses a broad base image", id),
				Description: fmt.Sprintf("Container uses image %q which may include unnecessary tools and attack surface.", image),
				Remediation: "Use a minimal, purpose-built container image and pin to a specific version.",
			})
		}

		// Check for missing resource limits in compose.
		if composeFile != "" && !strings.Contains(composeFile, "mem_limit") && !strings.Contains(composeFile, "memory:") {
			findings = append(findings, AuditFinding{
				Check:       c.Name(),
				Severity:    SeverityMedium,
				Title:       fmt.Sprintf("Container %q has no memory limit", id),
				Description: "Container does not define a memory limit, which could lead to resource exhaustion.",
				Remediation: "Add memory limits to the container compose configuration.",
			})
		}
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 8: DatabaseSecurityCheck — check DB file permissions
// ---------------------------------------------------------------------------

// DatabaseSecurityCheck checks the SQLite database file permissions.
type DatabaseSecurityCheck struct{}

func (c *DatabaseSecurityCheck) Name() string { return "database_security" }

func (c *DatabaseSecurityCheck) Run(_ context.Context) []AuditFinding {
	var findings []AuditFinding

	// Find the DB file.
	dbPath := os.Getenv("RATCHET_DB_PATH")
	if dbPath == "" {
		dbPath = "data/ratchet.db"
	}

	info, err := os.Stat(dbPath)
	if err != nil {
		// DB file not found; could be in-memory or not yet created.
		return findings
	}

	// Check for world-readable permissions.
	mode := info.Mode()
	if mode&fs.FileMode(0o004) != 0 {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityHigh,
			Title:       "Database file is world-readable",
			Description: fmt.Sprintf("Database file %q has permissions %s which allows any user to read it.", dbPath, mode),
			Remediation: "Run: chmod 600 " + dbPath + " to restrict access to the owning user only.",
		})
	} else if mode&fs.FileMode(0o040) != 0 {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityMedium,
			Title:       "Database file is group-readable",
			Description: fmt.Sprintf("Database file %q has permissions %s which allows group members to read it.", dbPath, mode),
			Remediation: "Run: chmod 600 " + dbPath + " to restrict access to the owning user only.",
		})
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 9: MCPServerCheck — warn about MCP servers with shell access
// ---------------------------------------------------------------------------

// MCPServerCheck inspects MCP server configurations for risky capabilities.
type MCPServerCheck struct {
	db *sql.DB
}

func (c *MCPServerCheck) Name() string { return "mcp_servers" }

func (c *MCPServerCheck) Run(ctx context.Context) []AuditFinding {
	var findings []AuditFinding
	if c.db == nil {
		return findings
	}

	rows, err := c.db.QueryContext(ctx,
		`SELECT id, name, transport, command, args FROM mcp_servers WHERE status = 'active'`)
	if err != nil {
		return findings
	}
	defer func() { _ = rows.Close() }()

	shellCommands := []string{"bash", "sh", "zsh", "fish", "cmd", "powershell", "python", "ruby", "perl", "node"}

	for rows.Next() {
		var id, name, transport, command, args string
		if err := rows.Scan(&id, &name, &transport, &command, &args); err != nil {
			continue
		}

		// Check if the MCP server command uses a shell interpreter.
		for _, shell := range shellCommands {
			if strings.HasSuffix(command, "/"+shell) || command == shell ||
				strings.Contains(args, shell) {
				findings = append(findings, AuditFinding{
					Check:       c.Name(),
					Severity:    SeverityHigh,
					Title:       fmt.Sprintf("MCP server %q uses shell access (%s)", name, shell),
					Description: fmt.Sprintf("MCP server %q (id: %s) executes via shell interpreter %q which could allow arbitrary command execution.", name, id, shell),
					Remediation: "Review the MCP server's tool definitions. Restrict its access using tool policies or run it in a sandboxed container.",
				})
				break
			}
		}

		// Warn about stdio transport (unencrypted).
		if transport == "stdio" {
			findings = append(findings, AuditFinding{
				Check:       c.Name(),
				Severity:    SeverityInfo,
				Title:       fmt.Sprintf("MCP server %q uses stdio transport", name),
				Description: "Stdio transport is only suitable for local process execution. Ensure the MCP server process is trusted.",
				Remediation: "For remote MCP servers, prefer HTTP transport with authentication.",
			})
		}
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 10: SecretExposureCheck — scan recent transcripts for secret patterns
// ---------------------------------------------------------------------------

// secretPattern matches common credential patterns in text.
var secretPattern = regexp.MustCompile(
	`(?i)(api[_-]?key|secret[_-]?key|access[_-]?token|password|auth[_-]?token|bearer\s+[a-zA-Z0-9._-]{20,}|sk-[a-zA-Z0-9]{20,}|ghp_[a-zA-Z0-9]{36}|AKIA[A-Z0-9]{16})`)

// SecretExposureCheck scans recent transcripts for potential credential leakage.
type SecretExposureCheck struct {
	db *sql.DB
}

func (c *SecretExposureCheck) Name() string { return "secret_exposure" }

func (c *SecretExposureCheck) Run(ctx context.Context) []AuditFinding {
	var findings []AuditFinding
	if c.db == nil {
		return findings
	}

	// Scan the most recent 1000 transcript entries that are not already redacted.
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, agent_id, task_id, content FROM transcripts
		 WHERE redacted = 0
		 ORDER BY created_at DESC
		 LIMIT 1000`)
	if err != nil {
		return findings
	}
	defer func() { _ = rows.Close() }()

	exposedIDs := make(map[string]bool) // avoid duplicate findings per transcript
	count := 0

	for rows.Next() {
		var id, agentID, taskID, content string
		if err := rows.Scan(&id, &agentID, &taskID, &content); err != nil {
			continue
		}
		if exposedIDs[id] {
			continue
		}
		if secretPattern.MatchString(content) {
			exposedIDs[id] = true
			count++
		}
	}

	if count > 0 {
		findings = append(findings, AuditFinding{
			Check:       c.Name(),
			Severity:    SeverityHigh,
			Title:       fmt.Sprintf("Potential secret exposure in %d transcript(s)", count),
			Description: fmt.Sprintf("%d transcript entries contain patterns that look like credentials (API keys, tokens, passwords) and are not redacted.", count),
			Remediation: "Enable secret redaction via the SecretGuard. Rotate any potentially exposed credentials immediately.",
		})
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 11: WebhookSecurityCheck — flag webhooks without HMAC verification
// ---------------------------------------------------------------------------

// WebhookSecurityCheck looks for webhook configurations lacking HMAC verification.
type WebhookSecurityCheck struct {
	db *sql.DB
}

func (c *WebhookSecurityCheck) Name() string { return "webhook_security" }

func (c *WebhookSecurityCheck) Run(ctx context.Context) []AuditFinding {
	var findings []AuditFinding
	if c.db == nil {
		return findings
	}

	// Check the webhooks table if it exists.
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, name, secret_name FROM webhooks WHERE enabled = 1`)
	if err != nil {
		// Table may not exist yet; not an error.
		return findings
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, name, secretName string
		if err := rows.Scan(&id, &name, &secretName); err != nil {
			continue
		}
		if secretName == "" {
			findings = append(findings, AuditFinding{
				Check:       c.Name(),
				Severity:    SeverityMedium,
				Title:       fmt.Sprintf("Webhook %q has no HMAC secret", name),
				Description: fmt.Sprintf("Webhook %q (id: %s) does not have an HMAC signing secret configured. Requests cannot be verified.", name, id),
				Remediation: "Configure an HMAC secret for the webhook to verify that requests originate from the expected source.",
			})
		}
	}

	return findings
}

// ---------------------------------------------------------------------------
// Check 12: DefaultCredentialCheck — check for default admin passwords/tokens
// ---------------------------------------------------------------------------

// DefaultCredentialCheck looks for known default or weak credentials.
type DefaultCredentialCheck struct {
	app modular.Application
}

func (c *DefaultCredentialCheck) Name() string { return "default_credentials" }

func (c *DefaultCredentialCheck) Run(_ context.Context) []AuditFinding {
	var findings []AuditFinding

	// Check for the default JWT secret in auth middleware config.
	knownWeakSecrets := []string{
		"ratchet-dev-secret-change-me",
		"secret",
		"changeme",
		"password",
		"admin",
		"ratchet",
		"default",
	}

	// Inspect the auth middleware secret via the service registry.
	for _, svc := range c.app.SvcRegistry() {
		type secretGetter interface {
			Secret() string
		}
		if sg, ok := svc.(secretGetter); ok {
			secret := sg.Secret()
			for _, weak := range knownWeakSecrets {
				if strings.EqualFold(secret, weak) {
					findings = append(findings, AuditFinding{
						Check:       c.Name(),
						Severity:    SeverityCritical,
						Title:       "Weak or default JWT secret in use",
						Description: fmt.Sprintf("The auth middleware JWT secret is set to a known weak value %q.", weak),
						Remediation: "Set a strong random JWT secret in the auth middleware configuration.",
					})
					break
				}
			}
		}
	}

	// Also check the ratchet.yaml file for known default secrets.
	data, err := os.ReadFile("ratchet.yaml")
	if err == nil {
		for _, weak := range knownWeakSecrets {
			if strings.Contains(string(data), `secret: "`+weak+`"`) ||
				strings.Contains(string(data), "secret: "+weak) {
				findings = append(findings, AuditFinding{
					Check:       c.Name(),
					Severity:    SeverityCritical,
					Title:       "Default JWT secret found in ratchet.yaml",
					Description: fmt.Sprintf("ratchet.yaml contains the default/weak JWT secret %q.", weak),
					Remediation: "Replace the secret value in ratchet.yaml with a strong random string, or use an environment variable.",
				})
				break
			}
		}
	}

	return findings
}
