package orchestrator

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/GoCodeAlone/workflow-plugin-agent/plugin"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
)

// agentIDFromToolCtx reads the agent ID set by tools.WithAgentID so that
// tool policy enforcement uses the same context key as the rest of the system.
func agentIDFromToolCtx(ctx context.Context) string {
	v, _ := ctx.Value(tools.ContextKeyAgentID).(string)
	return v
}

// teamIDFromToolCtx reads the team ID from context.
// Team ID is stored under its own key to avoid collisions.
type teamContextKey int

const teamContextKeyTeamID teamContextKey = 0

// WithTeamID returns a context with the team ID set for policy enforcement.
func WithTeamID(ctx context.Context, teamID string) context.Context {
	return context.WithValue(ctx, teamContextKeyTeamID, teamID)
}

// TeamIDFromContext returns the team ID from context, if set.
func TeamIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(teamContextKeyTeamID).(string)
	return v
}

// ToolRegistry merges built-in tools and MCP tools into a unified registry.
type ToolRegistry struct {
	mu           sync.RWMutex
	tools        map[string]plugin.Tool
	policyEngine *ToolPolicyEngine
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]plugin.Tool),
	}
}

// SetPolicyEngine attaches a ToolPolicyEngine for access control enforcement.
func (tr *ToolRegistry) SetPolicyEngine(engine *ToolPolicyEngine) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.policyEngine = engine
}

// Register adds a tool to the registry.
func (tr *ToolRegistry) Register(tool plugin.Tool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.tools[tool.Name()] = tool
}

// RegisterMCP registers MCP tools with a server-prefixed name.
func (tr *ToolRegistry) RegisterMCP(serverName string, tools []plugin.Tool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, t := range tools {
		name := "mcp_" + serverName + "__" + t.Name()
		tr.tools[name] = t
	}
}

// UnregisterMCP removes all tools registered under the given MCP server name.
func (tr *ToolRegistry) UnregisterMCP(serverName string) {
	prefix := "mcp_" + serverName + "__"
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for name := range tr.tools {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			delete(tr.tools, name)
		}
	}
}

// Get returns a tool by name.
func (tr *ToolRegistry) Get(name string) (plugin.Tool, bool) {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	t, ok := tr.tools[name]
	return t, ok
}

// AllDefs returns tool definitions for all registered tools.
func (tr *ToolRegistry) AllDefs() []provider.ToolDef {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	defs := make([]provider.ToolDef, 0, len(tr.tools))
	for _, t := range tr.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

// Execute runs a tool by name with the given arguments.
// If a policy engine is set, access control is checked before execution.
func (tr *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	tr.mu.RLock()
	t, ok := tr.tools[name]
	pe := tr.policyEngine
	tr.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tool %q not found in registry", name)
	}

	if pe == nil {
		log.Printf("warning: no tool policy engine configured, denying tool execution for %q", name)
		return nil, fmt.Errorf("tool %q denied: no tool policy engine configured", name)
	}

	agentID := agentIDFromToolCtx(ctx)
	teamID := TeamIDFromContext(ctx)
	if allowed, reason := pe.IsAllowed(ctx, name, agentID, teamID); !allowed {
		return nil, fmt.Errorf("tool %q denied by policy: %s", name, reason)
	}

	return t.Execute(ctx, args)
}

// Names returns all registered tool names.
func (tr *ToolRegistry) Names() []string {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	names := make([]string, 0, len(tr.tools))
	for name := range tr.tools {
		names = append(names, name)
	}
	return names
}
