// Package tools defines the Tool interface and Registry for agent tool execution.
package tools

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// Tool is an agent-callable function with a well-defined schema.
type Tool interface {
	// Name returns the unique identifier for this tool.
	Name() string

	// Definition returns the tool schema exposed to the LLM.
	Definition() provider.ToolDef

	// Execute runs the tool with the given arguments and returns the result.
	Execute(ctx context.Context, args map[string]any) (any, error)
}

// Registry merges built-in tools into a unified, policy-enforced registry.
type Registry struct {
	mu           sync.RWMutex
	tools        map[string]Tool
	policyEngine PolicyEngine
}

// PolicyEngine enforces access control on tool execution.
type PolicyEngine interface {
	// IsAllowed checks whether agentID (in teamID) may call toolName.
	// Returns (allowed, reason).
	IsAllowed(ctx context.Context, toolName, agentID, teamID string) (bool, string)
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// SetPolicyEngine attaches a PolicyEngine for access control enforcement.
func (r *Registry) SetPolicyEngine(pe PolicyEngine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policyEngine = pe
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// RegisterMCP registers MCP tools with a server-prefixed name.
func (r *Registry) RegisterMCP(serverName string, tools []Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range tools {
		name := "mcp_" + serverName + "__" + t.Name()
		r.tools[name] = t
	}
}

// UnregisterMCP removes all tools registered under the given MCP server name.
func (r *Registry) UnregisterMCP(serverName string) {
	prefix := "mcp_" + serverName + "__"
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.tools {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			delete(r.tools, name)
		}
	}
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// AllDefs returns tool definitions for all registered tools.
func (r *Registry) AllDefs() []provider.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]provider.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

// Execute runs a tool by name with the given arguments.
// If a policy engine is set, access control is checked before execution.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	pe := r.policyEngine
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("tool %q not found in registry", name)
	}

	if pe == nil {
		log.Printf("warning: no tool policy engine configured, allowing tool execution for %q", name)
	} else {
		agentID := AgentIDFromContext(ctx)
		teamID := TeamIDFromContext(ctx)
		if allowed, reason := pe.IsAllowed(ctx, name, agentID, teamID); !allowed {
			return nil, fmt.Errorf("tool %q denied by policy: %s", name, reason)
		}
	}

	return t.Execute(ctx, args)
}

// Names returns all registered tool names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}
