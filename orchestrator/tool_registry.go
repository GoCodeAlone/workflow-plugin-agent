package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	"github.com/GoCodeAlone/workflow-plugin-agent/plugin"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
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
	paginator    *ResponsePaginator
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

// SetPaginator attaches a ResponsePaginator that will be applied to large string
// tool results before they are returned to the agent loop.
func (tr *ToolRegistry) SetPaginator(rp *ResponsePaginator) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.paginator = rp
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
// Tool names use the registry key (e.g. "mcp_wfctl__validate_config")
// so the LLM calls tools by the same name that Execute() looks up.
func (tr *ToolRegistry) AllDefs() []provider.ToolDef {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	defs := make([]provider.ToolDef, 0, len(tr.tools))
	for regName, t := range tr.tools {
		def := t.Definition()
		def.Name = regName
		defs = append(defs, def)
	}
	return defs
}

// Execute runs a tool by name with the given arguments.
// If a policy engine is set, access control is checked before execution.
func (tr *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	tr.mu.RLock()
	t, ok := tr.tools[name]
	pe := tr.policyEngine
	rp := tr.paginator
	var snapshot map[string]plugin.Tool
	if !ok {
		snapshot = make(map[string]plugin.Tool, len(tr.tools))
		for k, v := range tr.tools {
			snapshot[k] = v
		}
	}
	tr.mu.RUnlock()
	if !ok {
		suggestion := suggestTool(name, snapshot)
		msg := fmt.Sprintf("tool %q not found in registry", name)
		if suggestion != "" {
			if st, exists := snapshot[suggestion]; exists {
				def := st.Definition()
				paramBytes, _ := json.Marshal(def.Parameters)
				msg += fmt.Sprintf(". Did you mean %q? Parameters: %s", suggestion, string(paramBytes))
			}
		}
		names := toolNames(snapshot)
		sort.Strings(names)
		msg += fmt.Sprintf(". Available tools: %s", strings.Join(names, ", "))
		return nil, fmt.Errorf("%s", msg)
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

	result, err := t.Execute(ctx, args)
	if err == nil && rp != nil {
		if s, ok := result.(string); ok {
			result = rp.Paginate(name, args, s)
		}
	}
	return result, err
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
