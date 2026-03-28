package orchestrator

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestPolicyEngine(t *testing.T) *ToolPolicyEngine {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	engine := NewToolPolicyEngine(db)
	if err := engine.InitTable(); err != nil {
		t.Fatalf("InitTable: %v", err)
	}
	return engine
}

func TestToolPolicy_DefaultDeny(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	// With no policies, the default is deny (fail-closed).
	allowed, reason := engine.IsAllowed(ctx, "file_read", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected default deny (fail-closed), got allowed: %s", reason)
	}
}

func TestToolPolicy_DefaultAllowWhenConfigured(t *testing.T) {
	engine := newTestPolicyEngine(t)
	engine.DefaultPolicy = PolicyAllow
	ctx := context.Background()

	// With no policies and default_policy=allow, tool should be allowed.
	allowed, reason := engine.IsAllowed(ctx, "file_read", "agent-1", "team-1")
	if !allowed {
		t.Errorf("expected allow when DefaultPolicy=allow and no policies: %s", reason)
	}
}

func TestToolPolicy_GlobalAllow(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "p1",
		Scope:       PolicyScopeGlobal,
		ToolPattern: "file_read",
		Action:      PolicyAllow,
	})

	allowed, reason := engine.IsAllowed(ctx, "file_read", "agent-1", "team-1")
	if !allowed {
		t.Errorf("expected allow, got denied: %s", reason)
	}
}

func TestToolPolicy_GlobalDeny(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "p1",
		Scope:       PolicyScopeGlobal,
		ToolPattern: "shell_exec",
		Action:      PolicyDeny,
	})

	allowed, reason := engine.IsAllowed(ctx, "shell_exec", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected deny, got allowed: %s", reason)
	}
}

func TestToolPolicy_TeamOverride(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	// Global deny
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "global-deny",
		Scope:       PolicyScopeGlobal,
		ToolPattern: "shell_exec",
		Action:      PolicyDeny,
	})

	// Team allow — deny-wins means the global deny still takes effect
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "team-allow",
		Scope:       PolicyScopeTeam,
		ScopeID:     "team-1",
		ToolPattern: "shell_exec",
		Action:      PolicyAllow,
	})

	// With deny-wins semantics, global deny beats team allow
	allowed, reason := engine.IsAllowed(ctx, "shell_exec", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected deny (deny-wins), got allowed: %s", reason)
	}
}

func TestToolPolicy_AgentLevelOverride(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	// Team deny
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "team-deny",
		Scope:       PolicyScopeTeam,
		ScopeID:     "team-1",
		ToolPattern: "file_write",
		Action:      PolicyDeny,
	})

	// Agent-level allow for a specific agent — deny-wins means team deny still applies
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "agent-allow",
		Scope:       PolicyScopeAgent,
		ScopeID:     "agent-1",
		ToolPattern: "file_write",
		Action:      PolicyAllow,
	})

	// deny-wins: team deny takes precedence
	allowed, reason := engine.IsAllowed(ctx, "file_write", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected deny (deny-wins), got allowed: %s", reason)
	}
	_ = reason
}

func TestToolPolicy_DenyWins(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	// Global deny
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "global-deny",
		Scope:       PolicyScopeGlobal,
		ToolPattern: "file_read",
		Action:      PolicyDeny,
	})

	// Agent-specific allow
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "agent-allow",
		Scope:       PolicyScopeAgent,
		ScopeID:     "agent-1",
		ToolPattern: "file_read",
		Action:      PolicyAllow,
	})

	// Deny-wins: global deny beats agent allow
	allowed, reason := engine.IsAllowed(ctx, "file_read", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected deny (deny-wins), got allowed: %s", reason)
	}
	_ = reason
}

func TestToolPolicy_GroupExpansion(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	// Deny all filesystem tools via group
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "deny-fs",
		Scope:       PolicyScopeGlobal,
		ToolPattern: "group:fs",
		Action:      PolicyDeny,
	})

	for _, toolName := range toolGroups["group:fs"] {
		allowed, reason := engine.IsAllowed(ctx, toolName, "agent-1", "team-1")
		if allowed {
			t.Errorf("expected %q to be denied via group:fs, got allowed: %s", toolName, reason)
		}
	}

	// shell_exec has no matching policy — default deny applies.
	allowed, reason := engine.IsAllowed(ctx, "shell_exec", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected shell_exec to be denied by default (no policy), got allowed: %s", reason)
	}
}

func TestToolPolicy_WildcardPattern(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	// Deny all MCP tools via wildcard prefix
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "deny-mcp",
		Scope:       PolicyScopeGlobal,
		ToolPattern: "mcp_*",
		Action:      PolicyDeny,
	})

	allowed, reason := engine.IsAllowed(ctx, "mcp_github__get_file", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected mcp tool to be denied via wildcard, got allowed: %s", reason)
	}

	// file_read has no matching policy — default deny applies.
	allowed, _ = engine.IsAllowed(ctx, "file_read", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected file_read to be denied by default (no matching policy)")
	}
}

func TestToolPolicy_TeamScopeOnlyMatchesCorrectTeam(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	// Deny for team-1 only
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "team1-deny",
		Scope:       PolicyScopeTeam,
		ScopeID:     "team-1",
		ToolPattern: "shell_exec",
		Action:      PolicyDeny,
	})

	// team-1 should be denied by explicit policy
	allowed, _ := engine.IsAllowed(ctx, "shell_exec", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected shell_exec to be denied for team-1")
	}

	// team-2 has no matching policy — default deny applies.
	allowed, _ = engine.IsAllowed(ctx, "shell_exec", "agent-2", "team-2")
	if allowed {
		t.Errorf("expected shell_exec to be denied for team-2 (no policy, default deny)")
	}
}

func TestToolPolicy_ListAndRemove(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "p1",
		Scope:       PolicyScopeGlobal,
		ToolPattern: "file_read",
		Action:      PolicyDeny,
	})

	policies, err := engine.ListPolicies(ctx)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(policies))
	}

	// Remove it
	if err := engine.RemovePolicy(ctx, "p1"); err != nil {
		t.Fatalf("RemovePolicy: %v", err)
	}

	policies, _ = engine.ListPolicies(ctx)
	if len(policies) != 0 {
		t.Errorf("expected 0 policies after removal, got %d", len(policies))
	}

	// Default deny applies after all policies are removed.
	allowed, _ := engine.IsAllowed(ctx, "file_read", "agent-1", "team-1")
	if allowed {
		t.Errorf("expected default deny after all policies removed")
	}
}

func TestToolPolicy_AllStarWildcard(t *testing.T) {
	engine := newTestPolicyEngine(t)
	ctx := context.Background()

	// Deny everything
	_ = engine.AddPolicy(ctx, ToolPolicy{
		ID:          "deny-all",
		Scope:       PolicyScopeGlobal,
		ToolPattern: "*",
		Action:      PolicyDeny,
	})

	for _, toolName := range []string{"file_read", "shell_exec", "web_fetch", "git_clone"} {
		allowed, reason := engine.IsAllowed(ctx, toolName, "agent-1", "team-1")
		if allowed {
			t.Errorf("expected %q to be denied by *, got allowed: %s", toolName, reason)
		}
	}
}

func TestToolRegistry_NilPolicyEngineDenies(t *testing.T) {
	registry := NewToolRegistry()
	// No policy engine set — registry.policyEngine is nil.

	ctx := context.Background()
	_, err := registry.Execute(ctx, "file_read", nil)
	if err == nil {
		t.Fatal("expected error when no policy engine configured, got nil")
	}
}
