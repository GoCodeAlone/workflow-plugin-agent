// Package policy implements a unified trust rules engine for agent tool access control.
// It supports both Claude Code (settings.json) and ratchet (config.yaml) rule formats,
// mode presets, scoped rules, and integrates with the existing ToolPolicyEngine.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Action defines the trust decision for a tool/path/command.
type Action string

const (
	Allow Action = "allow"
	Deny  Action = "deny"
	Ask   Action = "ask"
)

// TrustRule is a single trust policy entry.
type TrustRule struct {
	Pattern string // "file_read", "bash:git *", "path:~/.ssh/*", "Bash(rm:*)"
	Action  Action
	Scope   string // "global", "provider:claude_code", "agent:coder"
}

// PolicyEngine is an optional backing store for SQL-based policies.
// Matches the existing orchestrator.ToolPolicyEngine.IsAllowed signature.
type PolicyEngine interface {
	IsAllowed(ctx context.Context, toolName, agentID, teamID string) (bool, string)
}

// TrustEngine evaluates trust rules for tool calls, paths, and commands.
type TrustEngine struct {
	mu        sync.RWMutex
	rules     []TrustRule
	policyDB  PolicyEngine
	permStore *PermissionStore
	mode      string
}

// NewTrustEngine creates a TrustEngine with the given mode and explicit rules.
// If mode is a known preset, the preset rules are prepended.
// policyDB is optional (may be nil).
func NewTrustEngine(mode string, rules []TrustRule, policyDB PolicyEngine) *TrustEngine {
	te := &TrustEngine{
		policyDB: policyDB,
		mode:     mode,
	}
	if preset, ok := ModePresets[mode]; ok {
		te.rules = append(te.rules, preset...)
	}
	te.rules = append(te.rules, rules...)
	return te
}

// SetPermissionStore attaches a persistent permission store for "always" grants.
func (te *TrustEngine) SetPermissionStore(ps *PermissionStore) {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.permStore = ps
}

// Mode returns the current operating mode.
func (te *TrustEngine) Mode() string {
	te.mu.RLock()
	defer te.mu.RUnlock()
	return te.mode
}

// SetMode switches the active mode preset. Returns the new rules that were loaded.
// If mode is unknown, returns nil and mode is unchanged.
func (te *TrustEngine) SetMode(mode string) []TrustRule {
	preset, ok := ModePresets[mode]
	if !ok {
		return nil
	}
	te.mu.Lock()
	defer te.mu.Unlock()
	te.mode = mode
	// Replace preset rules but keep explicit (non-preset) rules.
	te.rules = make([]TrustRule, len(preset))
	copy(te.rules, preset)
	return preset
}

// AddRule appends a rule dynamically. Deny rules take precedence at evaluation time.
func (te *TrustEngine) AddRule(rule TrustRule) {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.rules = append(te.rules, rule)
}

// Rules returns a copy of the active rules.
func (te *TrustEngine) Rules() []TrustRule {
	te.mu.RLock()
	defer te.mu.RUnlock()
	out := make([]TrustRule, len(te.rules))
	copy(out, te.rules)
	return out
}

// Evaluate checks whether a tool call is allowed. Scope defaults to "global".
func (te *TrustEngine) Evaluate(ctx context.Context, toolName string, args map[string]any) Action {
	return te.EvaluateScoped(ctx, toolName, args, "global")
}

// EvaluateScoped checks whether a tool call is allowed in the given scope.
// Resolution order: deny-wins across all matching rules.
//  1. Per-scope rules matching the tool name
//  2. Global rules matching the tool name
//  3. PermissionStore persistent grants
//  4. ToolPolicyEngine (SQL)
//  5. Default: Deny
func (te *TrustEngine) EvaluateScoped(ctx context.Context, toolName string, args map[string]any, scope string) Action {
	te.mu.RLock()
	rules := te.rules
	permStore := te.permStore
	policyDB := te.policyDB
	te.mu.RUnlock()

	// Phase 1: Check trust rules. Deny wins across all matches.
	var matched []Action
	for _, r := range rules {
		if !ruleMatchesScope(r, scope) {
			continue
		}
		if matchToolPattern(r.Pattern, toolName) {
			matched = append(matched, r.Action)
		}
	}

	// Deny wins
	for _, a := range matched {
		if a == Deny {
			return Deny
		}
	}
	// If any rule matched, return the most permissive non-deny action.
	for _, a := range matched {
		if a == Allow {
			return Allow
		}
	}
	for _, a := range matched {
		if a == Ask {
			return Ask
		}
	}

	// Phase 2: Check persistent permission store.
	if permStore != nil {
		if action, ok := permStore.Check(toolName, scope); ok {
			return action
		}
	}

	// Phase 3: Check SQL-based ToolPolicyEngine.
	if policyDB != nil {
		agentID := extractAgentFromScope(scope)
		teamID := ""
		if allowed, _ := policyDB.IsAllowed(ctx, toolName, agentID, teamID); allowed {
			return Allow
		}
	}

	return Deny
}

// EvaluateCommand checks whether a bash command is allowed.
// Matches rules with "bash:" prefix patterns.
func (te *TrustEngine) EvaluateCommand(cmd string) Action {
	te.mu.RLock()
	rules := te.rules
	te.mu.RUnlock()

	var matched []Action
	for _, r := range rules {
		if matchCommandPattern(r.Pattern, cmd) {
			matched = append(matched, r.Action)
		}
	}

	for _, a := range matched {
		if a == Deny {
			return Deny
		}
	}
	for _, a := range matched {
		if a == Allow {
			return Allow
		}
	}
	for _, a := range matched {
		if a == Ask {
			return Ask
		}
	}
	return Deny
}

// EvaluatePath checks whether a file path is accessible.
// Matches rules with "path:" prefix patterns.
func (te *TrustEngine) EvaluatePath(path string) Action {
	te.mu.RLock()
	rules := te.rules
	te.mu.RUnlock()

	var matched []Action
	for _, r := range rules {
		if matchPathPattern(r.Pattern, path) {
			matched = append(matched, r.Action)
		}
	}

	for _, a := range matched {
		if a == Deny {
			return Deny
		}
	}
	for _, a := range matched {
		if a == Allow {
			return Allow
		}
	}
	for _, a := range matched {
		if a == Ask {
			return Ask
		}
	}
	return Deny
}

// GrantPersistent stores an "always allow/deny" decision for future sessions.
func (te *TrustEngine) GrantPersistent(pattern string, action Action, scope, grantedBy string) error {
	te.mu.RLock()
	ps := te.permStore
	te.mu.RUnlock()
	if ps == nil {
		return nil
	}
	return ps.Grant(pattern, action, scope, grantedBy)
}

// matchToolPattern checks if a pattern matches a tool name.
// Supports: exact match, wildcard "*", prefix wildcard "blackboard_*",
// and Claude Code format "Bash(cmd:*)" → converted to "bash:cmd *".
func matchToolPattern(pattern, toolName string) bool {
	if pattern == "*" {
		return true
	}
	// Skip path: and bash: patterns — they're for EvaluatePath/EvaluateCommand.
	if strings.HasPrefix(pattern, "path:") || strings.HasPrefix(pattern, "bash:") {
		return false
	}
	if pattern == toolName {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(toolName, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

// matchCommandPattern checks if a rule pattern matches a bash command.
func matchCommandPattern(pattern, cmd string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.HasPrefix(pattern, "bash:") {
		return false
	}
	bashPattern := strings.TrimPrefix(pattern, "bash:")
	if bashPattern == "*" {
		return true
	}
	// "git *" matches "git status", "git commit -m foo", etc.
	if strings.HasSuffix(bashPattern, " *") {
		prefix := strings.TrimSuffix(bashPattern, " *")
		cmdParts := strings.Fields(cmd)
		if len(cmdParts) > 0 && cmdParts[0] == prefix {
			return true
		}
		// Also match "prefix subcommand" pattern: "rm -rf *" matches "rm -rf /"
		if strings.HasPrefix(cmd, strings.TrimSuffix(bashPattern, "*")) {
			return true
		}
	}
	return bashPattern == cmd
}

// matchPathPattern checks if a rule pattern matches a file path.
func matchPathPattern(pattern, path string) bool {
	if !strings.HasPrefix(pattern, "path:") {
		return false
	}
	pathPattern := strings.TrimPrefix(pattern, "path:")
	// Expand ~ to match absolute paths
	if strings.HasPrefix(pathPattern, "~/") {
		// For matching, just check if the path ends with the same suffix
		suffix := strings.TrimPrefix(pathPattern, "~")
		if strings.HasSuffix(pathPattern, "*") {
			prefix := strings.TrimSuffix(suffix, "*")
			// Check all common home dirs
			return strings.Contains(path, prefix)
		}
	}
	if strings.HasSuffix(pathPattern, "*") {
		prefix := strings.TrimSuffix(pathPattern, "*")
		return strings.HasPrefix(path, prefix)
	}
	return pathPattern == path
}

// ruleMatchesScope returns true if the rule applies to the given scope.
func ruleMatchesScope(r TrustRule, scope string) bool {
	if r.Scope == "" || r.Scope == "global" {
		return true
	}
	return r.Scope == scope
}

// extractAgentFromScope extracts the agent ID from a scope like "agent:coder".
func extractAgentFromScope(scope string) string {
	if strings.HasPrefix(scope, "agent:") {
		return strings.TrimPrefix(scope, "agent:")
	}
	return ""
}

// ModePresets defines the built-in trust modes.
var ModePresets = map[string][]TrustRule{
	"conservative": {
		{Pattern: "file_read", Action: Allow},
		{Pattern: "blackboard_*", Action: Allow},
		{Pattern: "send_message", Action: Allow},
		{Pattern: "bash:git *", Action: Allow},
		{Pattern: "bash:go *", Action: Allow},
		{Pattern: "file_write", Action: Ask},
		{Pattern: "bash:*", Action: Ask},
		{Pattern: "path:~/.ssh/*", Action: Deny},
		{Pattern: "path:~/.aws/*", Action: Deny},
	},
	"permissive": {
		{Pattern: "*", Action: Allow},
		{Pattern: "bash:rm -rf /*", Action: Deny},
		{Pattern: "bash:sudo *", Action: Deny},
		{Pattern: "path:~/.ssh/*", Action: Deny},
	},
	"locked": {
		{Pattern: "file_read", Action: Allow},
		{Pattern: "blackboard_*", Action: Allow},
		{Pattern: "*", Action: Ask},
	},
	"sandbox": {
		{Pattern: "*", Action: Allow},
	},
}

// ParseClaudeCodeSettings parses a .claude/settings.json file into TrustRules.
// allowedTools → Allow, disallowedTools → Deny. Bash(cmd:*) is normalized to bash:cmd *.
func ParseClaudeCodeSettings(data []byte) ([]TrustRule, error) {
	var settings struct {
		AllowedTools    []string `json:"allowedTools"`
		DisallowedTools []string `json:"disallowedTools"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse claude code settings: %w", err)
	}

	var rules []TrustRule
	for _, t := range settings.AllowedTools {
		rules = append(rules, TrustRule{
			Pattern: normalizeClaudeToolPattern(t),
			Action:  Allow,
			Scope:   "provider:claude_code",
		})
	}
	for _, t := range settings.DisallowedTools {
		rules = append(rules, TrustRule{
			Pattern: normalizeClaudeToolPattern(t),
			Action:  Deny,
			Scope:   "provider:claude_code",
		})
	}
	return rules, nil
}

// normalizeClaudeToolPattern converts Claude Code format "Bash(cmd:*)" to "bash:cmd *".
func normalizeClaudeToolPattern(pattern string) string {
	if strings.HasPrefix(pattern, "Bash(") && strings.HasSuffix(pattern, ")") {
		inner := pattern[5 : len(pattern)-1] // strip "Bash(" and ")"
		// "git:*" → "git *", "rm -rf:*" → "rm -rf *"
		inner = strings.Replace(inner, ":*", " *", 1)
		inner = strings.Replace(inner, ":", " ", 1)
		return "bash:" + inner
	}
	return pattern
}

// RatchetTrustConfig is the trust section from ~/.ratchet/config.yaml.
type RatchetTrustConfig struct {
	Mode         string               `yaml:"mode" json:"mode"`
	Rules        []RatchetTrustRule   `yaml:"rules" json:"rules"`
	ProviderArgs map[string][]string  `yaml:"provider_args" json:"provider_args"`
	Prompts      []RatchetPromptPattern `yaml:"prompts" json:"prompts"`
}

// RatchetTrustRule is a single rule in ratchet format.
type RatchetTrustRule struct {
	Pattern string `yaml:"pattern" json:"pattern"`
	Action  string `yaml:"action" json:"action"`
}

// RatchetPromptPattern is a user-defined screen prompt auto-response pattern.
type RatchetPromptPattern struct {
	Name   string `yaml:"name" json:"name"`
	Match  string `yaml:"match" json:"match"`
	Action string `yaml:"action" json:"action"`
}

// ParseRatchetTrustConfig converts ratchet YAML trust config into TrustRules.
func ParseRatchetTrustConfig(cfg RatchetTrustConfig) []TrustRule {
	var rules []TrustRule
	for _, r := range cfg.Rules {
		var action Action
		switch strings.ToLower(r.Action) {
		case "allow":
			action = Allow
		case "deny":
			action = Deny
		case "ask":
			action = Ask
		default:
			action = Deny
		}
		rules = append(rules, TrustRule{
			Pattern: r.Pattern,
			Action:  action,
			Scope:   "global",
		})
	}
	return rules
}
