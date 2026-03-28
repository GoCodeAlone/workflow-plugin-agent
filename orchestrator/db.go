package orchestrator

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/config"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

const createAgentsTable = `
CREATE TABLE IF NOT EXISTS agents (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'idle',
    team_id TEXT NOT NULL DEFAULT '',
    is_lead INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createTasksTable = `
CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    priority INTEGER NOT NULL DEFAULT 1,
    assigned_to TEXT NOT NULL DEFAULT '',
    team_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    task_role TEXT NOT NULL DEFAULT '',
    depends_on TEXT NOT NULL DEFAULT '[]',
    labels TEXT NOT NULL DEFAULT '[]',
    metadata TEXT NOT NULL DEFAULT '{}',
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    started_at DATETIME,
    completed_at DATETIME
);`

const createMessagesTable = `
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL DEFAULT 'direct',
    from_agent TEXT NOT NULL DEFAULT '',
    to_agent TEXT NOT NULL DEFAULT '',
    team_id TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    metadata TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createProjectsTable = `
CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    workspace_path TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createTranscriptsTable = `
CREATE TABLE IF NOT EXISTS transcripts (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    iteration INTEGER NOT NULL DEFAULT 0,
    role TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    tool_calls TEXT NOT NULL DEFAULT '[]',
    tool_call_id TEXT NOT NULL DEFAULT '',
    redacted INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createMCPServersTable = `
CREATE TABLE IF NOT EXISTS mcp_servers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    transport TEXT NOT NULL DEFAULT 'stdio',
    command TEXT NOT NULL DEFAULT '',
    args TEXT NOT NULL DEFAULT '[]',
    url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createProjectReposTable = `
CREATE TABLE IF NOT EXISTS project_repos (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    repo_url TEXT NOT NULL,
    clone_path TEXT NOT NULL DEFAULT '',
    branch TEXT NOT NULL DEFAULT 'main',
    auth_token_secret TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    last_synced_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createWorkspaceContainersTable = `
CREATE TABLE IF NOT EXISTS workspace_containers (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL UNIQUE,
    container_id TEXT NOT NULL DEFAULT '',
    image TEXT NOT NULL DEFAULT 'ubuntu:22.04',
    status TEXT NOT NULL DEFAULT 'pending',
    compose_file TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createLLMProvidersTable = `
CREATE TABLE IF NOT EXISTS llm_providers (
    id TEXT PRIMARY KEY,
    alias TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    base_url TEXT NOT NULL DEFAULT '',
    secret_name TEXT NOT NULL DEFAULT '',
    max_tokens INTEGER NOT NULL DEFAULT 0,
    settings TEXT NOT NULL DEFAULT '{}',
    is_default INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'unchecked',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createToolPoliciesTable = `
CREATE TABLE IF NOT EXISTS tool_policies (
    id TEXT PRIMARY KEY,
    scope TEXT NOT NULL DEFAULT 'global',
    scope_id TEXT NOT NULL DEFAULT '',
    tool_pattern TEXT NOT NULL,
    action TEXT NOT NULL DEFAULT 'allow',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createApprovalsTable = `
CREATE TABLE IF NOT EXISTS approvals (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    reviewer_comment TEXT NOT NULL DEFAULT '',
    timeout_minutes INTEGER NOT NULL DEFAULT 30,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    resolved_at DATETIME
);`

const createHumanRequestsTable = `
CREATE TABLE IF NOT EXISTS human_requests (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    request_type TEXT NOT NULL DEFAULT 'info',
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    urgency TEXT NOT NULL DEFAULT 'normal',
    status TEXT NOT NULL DEFAULT 'pending',
    response_data TEXT NOT NULL DEFAULT '',
    response_comment TEXT NOT NULL DEFAULT '',
    resolved_by TEXT NOT NULL DEFAULT '',
    timeout_minutes INTEGER NOT NULL DEFAULT 0,
    metadata TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    resolved_at DATETIME
);`

const createSkillsTable = `
CREATE TABLE IF NOT EXISTS skills (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT '',
    required_tools TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createAgentSkillsTable = `
CREATE TABLE IF NOT EXISTS agent_skills (
    agent_id TEXT NOT NULL,
    skill_id TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (agent_id, skill_id)
);`

const createWebhooksTable = `
CREATE TABLE IF NOT EXISTS webhooks (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL DEFAULT 'generic',
    name TEXT NOT NULL,
    secret_name TEXT NOT NULL DEFAULT '',
    filter TEXT NOT NULL DEFAULT '',
    task_template TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createSchemaVersionTable = `
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const createServerInfoTable = `
CREATE TABLE IF NOT EXISTS server_info (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);`

// migrations is the ordered list of incremental schema changes applied after
// the initial table creation. Each entry is identified by a monotonically
// increasing version number. A migration is skipped if its version is already
// present in the schema_version table.
var migrations = []struct {
	version int
	sql     string
}{
	// v1: add project_id column to tasks
	{1, "ALTER TABLE tasks ADD COLUMN project_id TEXT NOT NULL DEFAULT ''"},
	// v2: add workspace_spec column to projects
	{2, "ALTER TABLE projects ADD COLUMN workspace_spec TEXT NOT NULL DEFAULT '{}'"},
	// v3: add ephemeral sub-agent columns to agents
	{3, "ALTER TABLE agents ADD COLUMN is_ephemeral INTEGER NOT NULL DEFAULT 0"},
	// v4: add parent_agent_id column to agents
	{4, "ALTER TABLE agents ADD COLUMN parent_agent_id TEXT NOT NULL DEFAULT ''"},
	// v5: migrate seeded agents from hardcoded 'mock' provider to '' (use default)
	{5, "UPDATE agents SET provider = '' WHERE provider = 'mock'"},
	// v6: add human_requests table
	{6, "CREATE TABLE IF NOT EXISTS human_requests (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL DEFAULT '', request_type TEXT NOT NULL DEFAULT 'info', title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', urgency TEXT NOT NULL DEFAULT 'normal', status TEXT NOT NULL DEFAULT 'pending', response_data TEXT NOT NULL DEFAULT '', response_comment TEXT NOT NULL DEFAULT '', resolved_by TEXT NOT NULL DEFAULT '', timeout_minutes INTEGER NOT NULL DEFAULT 0, metadata TEXT NOT NULL DEFAULT '{}', created_at DATETIME NOT NULL DEFAULT (datetime('now')), resolved_at DATETIME)"},
	// v7: add task_role column to tasks for role-based assignment
	{7, "ALTER TABLE tasks ADD COLUMN task_role TEXT NOT NULL DEFAULT ''"},
}

// dbInitHook creates a WiringHook that initialises the ratchet database tables
// and seeds agents from the YAML config.
func dbInitHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.db_init",
		Priority: 100,
		Hook: func(app modular.Application, cfg *config.WorkflowConfig) error {
			// Find the DB service registered as "ratchet-db"
			svc, ok := app.SvcRegistry()["ratchet-db"]
			if !ok {
				return fmt.Errorf("ratchet.db_init: database service 'ratchet-db' not found")
			}

			dbProvider, ok := svc.(module.DBProvider)
			if !ok {
				return fmt.Errorf("ratchet.db_init: 'ratchet-db' does not implement DBProvider")
			}

			// The wiring hook runs during BuildFromConfig, before Start().
			// If the DB isn't open yet, start the storage module early so
			// we can create tables and seed data. The engine's Start() will
			// call Start() again, but SQLiteStorage just re-opens the conn.
			if dbProvider.DB() == nil {
				if starter, ok := svc.(interface{ Start(context.Context) error }); ok {
					if err := starter.Start(context.Background()); err != nil {
						return fmt.Errorf("ratchet.db_init: early start of database: %w", err)
					}
				}
			}

			db := dbProvider.DB()
			if db == nil {
				return fmt.Errorf("ratchet.db_init: database connection is nil")
			}
			db.SetMaxOpenConns(1)

			// Create base tables (all idempotent via IF NOT EXISTS)
			for _, ddl := range []string{
				createAgentsTable, createTasksTable, createMessagesTable,
				createProjectsTable, createTranscriptsTable, createMCPServersTable,
				createProjectReposTable, createWorkspaceContainersTable,
				createLLMProvidersTable, createToolPoliciesTable, createApprovalsTable,
				createHumanRequestsTable,
				createSkillsTable, createAgentSkillsTable, createWebhooksTable,
				createSchemaVersionTable, createServerInfoTable,
			} {
				if _, err := db.Exec(ddl); err != nil {
					return fmt.Errorf("ratchet.db_init: create table: %w", err)
				}
			}

			// Apply pending incremental migrations.
			// Each migration is only executed if its version is not yet recorded
			// in schema_version, making them safe to run on every boot.
			for _, m := range migrations {
				var count int
				_ = db.QueryRow("SELECT COUNT(*) FROM schema_version WHERE version = ?", m.version).Scan(&count)
				if count > 0 {
					continue // already applied
				}
				// ALTER TABLE returns an error if the column already exists in some
				// SQLite builds; treat it as a no-op so we can still record the version.
				// Any genuine data-corruption error will surface on subsequent queries.
				_, _ = db.Exec(m.sql)
				_, _ = db.Exec("INSERT OR IGNORE INTO schema_version (version) VALUES (?)", m.version)
			}

			// Record server start time. INSERT OR REPLACE so every restart
			// updates the timestamp, enabling accurate uptime calculation.
			_, _ = db.Exec("INSERT OR REPLACE INTO server_info (key, value) VALUES ('started_at', datetime('now'))")

			// Initialize memory tables (FTS5 + triggers managed by MemoryStore)
			ms := NewMemoryStore(db)
			if err := ms.InitTables(); err != nil {
				return fmt.Errorf("ratchet.db_init: memory tables: %w", err)
			}
			_ = app.RegisterService("ratchet-memory-store", ms)

			// Seed default mock provider if none exist
			var providerCount int
			_ = db.QueryRow("SELECT COUNT(*) FROM llm_providers").Scan(&providerCount)
			if providerCount == 0 {
				_, _ = db.Exec(`INSERT OR IGNORE INTO llm_providers (id, alias, type, model, is_default, status) VALUES ('mock-default', 'mock', 'mock', '', 1, 'active')`)
			}

			// Seed agents from config modules
			if cfg == nil {
				return nil
			}
			for _, modCfg := range cfg.Modules {
				// Support both the legacy ratchet.ai_provider type and the new agent.provider type.
				if modCfg.Type != "ratchet.ai_provider" && modCfg.Type != "agent.provider" {
					continue
				}
				agentsRaw, ok := modCfg.Config["agents"]
				if !ok {
					continue
				}
				agentsList, ok := agentsRaw.([]any)
				if !ok {
					continue
				}
				for _, item := range agentsList {
					agentMap, ok := item.(map[string]any)
					if !ok {
						continue
					}
					seed := extractAgentSeed(agentMap)
					if seed.ID == "" || seed.Name == "" {
						continue
					}
					isLeadInt := 0
					if seed.IsLead {
						isLeadInt = 1
					}
					_, err := db.Exec(`
INSERT OR IGNORE INTO agents (id, name, role, system_prompt, provider, model, team_id, is_lead)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						seed.ID, seed.Name, seed.Role, seed.SystemPrompt,
						seed.Provider, seed.Model, seed.TeamID, isLeadInt,
					)
					if err != nil {
						return fmt.Errorf("ratchet.db_init: seed agent %q: %w", seed.ID, err)
					}
				}
			}

			return nil
		},
	}
}

// extractAgentSeed reads an AgentSeed from a raw map (from YAML config).
func extractAgentSeed(m map[string]any) AgentSeed {
	s := AgentSeed{}
	if v, ok := m["id"].(string); ok {
		s.ID = v
	}
	if v, ok := m["name"].(string); ok {
		s.Name = v
	}
	if v, ok := m["role"].(string); ok {
		s.Role = v
	}
	if v, ok := m["system_prompt"].(string); ok {
		s.SystemPrompt = v
	}
	if v, ok := m["provider"].(string); ok {
		s.Provider = v
	}
	if v, ok := m["model"].(string); ok {
		s.Model = v
	}
	if v, ok := m["team_id"].(string); ok {
		s.TeamID = v
	}
	if v, ok := m["is_lead"].(bool); ok {
		s.IsLead = v
	}
	return s
}
