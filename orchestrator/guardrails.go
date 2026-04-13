package orchestrator

import (
	"context"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/safety"
	"github.com/GoCodeAlone/workflow/plugin"
)

// GuardrailsDefaults holds the default rules applied when no scope matches.
type GuardrailsDefaults struct {
	EnableSelfImprovement bool     `yaml:"enable_self_improvement" json:"enable_self_improvement"`
	EnableIacModification bool     `yaml:"enable_iac_modification" json:"enable_iac_modification"`
	RequireHumanApproval  bool     `yaml:"require_human_approval" json:"require_human_approval"`
	RequireDiffReview     bool     `yaml:"require_diff_review" json:"require_diff_review"`
	MaxIterationsPerCycle int      `yaml:"max_iterations_per_cycle" json:"max_iterations_per_cycle"`
	DeployStrategy        string   `yaml:"deploy_strategy" json:"deploy_strategy"`
	AllowedTools          []string `yaml:"allowed_tools" json:"allowed_tools"`
	BlockedTools          []string `yaml:"blocked_tools" json:"blocked_tools"`
	CommandPolicy         safety.Policy `yaml:"command_policy" json:"command_policy"`
}

// ScopeMatch defines which agents/teams/models/providers this scope applies to.
// Fields with empty string match any value. Patterns support * wildcard.
type ScopeMatch struct {
	Agent    string `yaml:"agent" json:"agent"`
	Team     string `yaml:"team" json:"team"`
	Model    string `yaml:"model" json:"model"`
	Provider string `yaml:"provider" json:"provider"`
}

// ScopeOverride holds the overriding rules for a specific scope.
// Non-nil/non-empty fields replace the defaults for matched agents.
type ScopeOverride struct {
	AllowedTools          []string      `yaml:"allowed_tools" json:"allowed_tools"`
	BlockedTools          []string      `yaml:"blocked_tools" json:"blocked_tools"`
	MaxIterationsPerCycle *int          `yaml:"max_iterations_per_cycle,omitempty" json:"max_iterations_per_cycle,omitempty"`
	CommandPolicy         *safety.Policy `yaml:"command_policy,omitempty" json:"command_policy,omitempty"`
	EnableIacModification *bool         `yaml:"enable_iac_modification,omitempty" json:"enable_iac_modification,omitempty"`
	RequireHumanApproval  *bool         `yaml:"require_human_approval,omitempty" json:"require_human_approval,omitempty"`
}

// ScopeRule is a scope + its override rules.
type ScopeRule struct {
	Match    ScopeMatch    `yaml:"match" json:"match"`
	Override ScopeOverride `yaml:"rules" json:"rules"`
}

// ImmutableSection is a config path that agents cannot modify without an override token.
type ImmutableSection struct {
	Path     string `yaml:"path" json:"path"`
	Override string `yaml:"override" json:"override"` // "challenge_token"
}

// OverrideConfig configures the challenge-token override mechanism.
type OverrideConfig struct {
	Mechanism      string `yaml:"mechanism" json:"mechanism"`
	AdminSecretEnv string `yaml:"admin_secret_env" json:"admin_secret_env"`
	Fallback       string `yaml:"fallback" json:"fallback"`
}

// ScopeContext carries the agent's identity for scope resolution.
type ScopeContext struct {
	Agent    string
	Team     string
	Model    string
	Provider string
}

// GuardrailsModule implements the agent.guardrails module type.
// It provides hierarchical tool access control, command safety, and config
// immutability enforcement. It also implements executor.TrustEvaluator so it
// can be passed directly as TrustEngine in executor.Config.
type GuardrailsModule struct {
	name              string
	defaults          GuardrailsDefaults
	scopes            []ScopeRule
	immutableSections []ImmutableSection
	override          OverrideConfig
	analyzer          *safety.CommandAnalyzer
}

// Ensure GuardrailsModule satisfies executor.TrustEvaluator at compile time.
var _ executor.TrustEvaluator = (*GuardrailsModule)(nil)

// Name implements modular.Module.
func (g *GuardrailsModule) Name() string { return g.name }

// Init registers the guardrails module as a named service.
func (g *GuardrailsModule) Init(app modular.Application) error {
	return app.RegisterService(g.name, g)
}

// ProvidesServices declares the guardrails service.
func (g *GuardrailsModule) ProvidesServices() []modular.ServiceProvider {
	return []modular.ServiceProvider{
		{
			Name:        g.name,
			Description: "Agent guardrails: " + g.name,
			Instance:    g,
		},
	}
}

// RequiresServices declares no dependencies.
func (g *GuardrailsModule) RequiresServices() []modular.ServiceDependency {
	return nil
}

// CheckTool checks whether a tool is permitted by the default rules.
// Returns (allowed, reason).
func (g *GuardrailsModule) CheckTool(toolName string) (bool, string) {
	return g.CheckToolScoped(toolName, ScopeContext{})
}

// CheckToolScoped checks tool access for the given scope context.
// Scope precedence: agent > team > model > provider > defaults.
func (g *GuardrailsModule) CheckToolScoped(toolName string, sc ScopeContext) (bool, string) {
	allowed, blocked := g.resolveToolLists(sc)
	return checkToolAccess(toolName, allowed, blocked)
}

// CheckCommand checks whether a shell command is safe.
func (g *GuardrailsModule) CheckCommand(cmd string) (bool, string) {
	v, err := g.analyzer.Analyze(cmd)
	if err != nil {
		return false, "command analysis error: " + err.Error()
	}
	if !v.Safe {
		return false, v.Reason
	}
	return true, ""
}

// CheckImmutableSection returns whether the given config path is immutable
// and what override mechanism is required.
func (g *GuardrailsModule) CheckImmutableSection(path string) (protected bool, override string) {
	for _, sec := range g.immutableSections {
		if matchConfigPath(sec.Path, path) {
			return true, sec.Override
		}
	}
	return false, ""
}

// Defaults returns a copy of the default guardrails configuration.
func (g *GuardrailsModule) Defaults() GuardrailsDefaults {
	return g.defaults
}

// FilterTools returns only the tool definitions allowed by the guardrails default rules.
// Blocked tools are excluded first (blocked wins), then allowed patterns are applied.
// If neither list is configured, all tools are passed through.
func (g *GuardrailsModule) FilterTools(tools []provider.ToolDef) []provider.ToolDef {
	if g == nil {
		return tools
	}
	allowed := g.defaults.AllowedTools
	blocked := g.defaults.BlockedTools
	if len(allowed) == 0 && len(blocked) == 0 {
		return tools
	}
	var filtered []provider.ToolDef
	for _, t := range tools {
		ok, _ := checkToolAccess(t.Name, allowed, blocked)
		if ok {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// --- executor.TrustEvaluator implementation ---

// Evaluate implements executor.TrustEvaluator.
// Checks whether a tool call is allowed using only the default rules (no scope matching).
// Use CheckToolInScope for scope-aware evaluation.
func (g *GuardrailsModule) Evaluate(_ context.Context, toolName string, _ map[string]any) executor.Action {
	ok, _ := g.CheckTool(toolName)
	if ok {
		return executor.ActionAllow
	}
	return executor.ActionDeny
}

// EvaluateCommand implements executor.TrustEvaluator.
// Delegates to the command analyzer for shell AST safety analysis.
func (g *GuardrailsModule) EvaluateCommand(cmd string) executor.Action {
	ok, _ := g.CheckCommand(cmd)
	if ok {
		return executor.ActionAllow
	}
	return executor.ActionDeny
}

// EvaluatePath implements executor.TrustEvaluator.
// Paths are allowed by default; use trust rules for path restrictions.
func (g *GuardrailsModule) EvaluatePath(_ string) executor.Action {
	return executor.ActionAllow
}

// --- scope resolution ---

// resolveToolLists returns the effective allowed/blocked tool lists for the scope.
// Precedence: agent > team > model > provider > defaults.
func (g *GuardrailsModule) resolveToolLists(sc ScopeContext) (allowed, blocked []string) {
	// Check from most to least specific, return first match.
	for _, rule := range g.scopes {
		if sc.Agent != "" && rule.Match.Agent != "" && matchPattern(rule.Match.Agent, sc.Agent) {
			return rule.Override.AllowedTools, rule.Override.BlockedTools
		}
	}
	for _, rule := range g.scopes {
		if sc.Team != "" && rule.Match.Team != "" && matchPattern(rule.Match.Team, sc.Team) {
			return rule.Override.AllowedTools, rule.Override.BlockedTools
		}
	}
	for _, rule := range g.scopes {
		if sc.Model != "" && rule.Match.Model != "" && matchPattern(rule.Match.Model, sc.Model) {
			return rule.Override.AllowedTools, rule.Override.BlockedTools
		}
	}
	for _, rule := range g.scopes {
		if sc.Provider != "" && rule.Match.Provider != "" && matchPattern(rule.Match.Provider, sc.Provider) {
			return rule.Override.AllowedTools, rule.Override.BlockedTools
		}
	}
	return g.defaults.AllowedTools, g.defaults.BlockedTools
}

// checkToolAccess returns whether toolName passes the allowed/blocked lists.
// Blocked list is checked first (deny wins). Then allowed list (glob match).
func checkToolAccess(toolName string, allowed, blocked []string) (bool, string) {
	for _, pattern := range blocked {
		if matchPattern(pattern, toolName) {
			return false, "tool " + toolName + " matches blocked pattern " + pattern
		}
	}
	for _, pattern := range allowed {
		if matchPattern(pattern, toolName) {
			return true, ""
		}
	}
	if len(allowed) == 0 {
		// No restrictions configured — allow all.
		return true, ""
	}
	return false, "tool " + toolName + " not in allowed list"
}

// matchPattern matches value against pattern using two rules:
//  1. Exact match: pattern == value.
//  2. Prefix match: if pattern ends with "*", the prefix before "*" must match the start of value
//     (e.g. "mcp:wfctl:validate_*" matches "mcp:wfctl:validate_config").
//
// The standalone "*" and "**" patterns match any value.
func matchPattern(pattern, value string) bool {
	if pattern == "*" || pattern == "**" {
		return true
	}
	if pattern == value {
		return true
	}
	// Suffix wildcard: "mcp:wfctl:validate_*" matches "mcp:wfctl:validate_config"
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(value, prefix)
	}
	return false
}

// matchConfigPath matches a config section path, supporting * wildcard in last segment.
// e.g. "security.*" matches "security.tls", "security.auth"
func matchConfigPath(pattern, path string) bool {
	if pattern == path {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, ".*")
		return strings.HasPrefix(path, prefix+".")
	}
	return false
}

// NewGuardrailsModule creates a GuardrailsModule with the given name and defaults.
// Useful for testing and programmatic construction.
func NewGuardrailsModule(name string, defaults GuardrailsDefaults) *GuardrailsModule {
	analyzerPolicy := defaults.CommandPolicy
	if analyzerPolicy.Mode == "" {
		analyzerPolicy = safety.DefaultPolicy()
	}
	return &GuardrailsModule{
		name:     name,
		defaults: defaults,
		analyzer: safety.NewCommandAnalyzer(analyzerPolicy),
	}
}

// --- factory and plugin registration ---

func newGuardrailsModuleFactory() plugin.ModuleFactory {
	return func(name string, cfg map[string]any) modular.Module {
		defaults := parseGuardrailsDefaults(cfg)
		scopes := parseGuardrailsScopes(cfg)
		immutable := parseImmutableSections(cfg)
		overrideCfg := parseOverrideConfig(cfg)

		analyzerPolicy := defaults.CommandPolicy
		if analyzerPolicy.Mode == "" {
			analyzerPolicy = safety.DefaultPolicy()
		}

		return &GuardrailsModule{
			name:              name,
			defaults:          defaults,
			scopes:            scopes,
			immutableSections: immutable,
			override:          overrideCfg,
			analyzer:          safety.NewCommandAnalyzer(analyzerPolicy),
		}
	}
}

func parseGuardrailsDefaults(cfg map[string]any) GuardrailsDefaults {
	defaults := GuardrailsDefaults{
		MaxIterationsPerCycle: 5,
		DeployStrategy:        "git_pr",
		CommandPolicy:         safety.DefaultPolicy(),
	}
	d, _ := cfg["defaults"].(map[string]any)
	if d == nil {
		return defaults
	}
	if v, ok := d["enable_self_improvement"].(bool); ok {
		defaults.EnableSelfImprovement = v
	}
	if v, ok := d["enable_iac_modification"].(bool); ok {
		defaults.EnableIacModification = v
	}
	if v, ok := d["require_human_approval"].(bool); ok {
		defaults.RequireHumanApproval = v
	}
	if v, ok := d["require_diff_review"].(bool); ok {
		defaults.RequireDiffReview = v
	}
	switch v := d["max_iterations_per_cycle"].(type) {
	case int:
		defaults.MaxIterationsPerCycle = v
	case int64:
		defaults.MaxIterationsPerCycle = int(v)
	case float64:
		defaults.MaxIterationsPerCycle = int(v)
	}
	if v, ok := d["deploy_strategy"].(string); ok {
		defaults.DeployStrategy = v
	}
	if v, ok := d["allowed_tools"].([]any); ok {
		for _, t := range v {
			if s, ok := t.(string); ok {
				defaults.AllowedTools = append(defaults.AllowedTools, s)
			}
		}
	}
	if v, ok := d["blocked_tools"].([]any); ok {
		for _, t := range v {
			if s, ok := t.(string); ok {
				defaults.BlockedTools = append(defaults.BlockedTools, s)
			}
		}
	}
	if cp, ok := d["command_policy"].(map[string]any); ok {
		defaults.CommandPolicy = parseCommandPolicy(cp)
	}
	return defaults
}

func parseCommandPolicy(cfg map[string]any) safety.Policy {
	p := safety.DefaultPolicy()
	if mode, ok := cfg["mode"].(string); ok {
		p.Mode = safety.PolicyMode(mode)
	}
	if v, ok := cfg["block_pipe_to_shell"].(bool); ok {
		p.BlockPipeToShell = v
	}
	if v, ok := cfg["block_script_execution"].(bool); ok {
		p.BlockScriptExec = v
	}
	if v, ok := cfg["enable_static_analysis"].(bool); ok {
		p.EnableStaticAnalysis = v
	}
	switch v := cfg["max_command_length"].(type) {
	case int:
		p.MaxCommandLength = v
	case int64:
		p.MaxCommandLength = int(v)
	case float64:
		p.MaxCommandLength = int(v)
	}
	if v, ok := cfg["allowed_commands"].([]any); ok {
		p.AllowedCommands = nil
		for _, c := range v {
			if s, ok := c.(string); ok {
				p.AllowedCommands = append(p.AllowedCommands, s)
			}
		}
	}
	if v, ok := cfg["blocked_patterns"].([]any); ok {
		p.BlockedPatterns = nil
		for _, c := range v {
			if s, ok := c.(string); ok {
				p.BlockedPatterns = append(p.BlockedPatterns, s)
			}
		}
	}
	return p
}

func parseGuardrailsScopes(cfg map[string]any) []ScopeRule {
	scopesCfg, _ := cfg["scopes"].([]any)
	if len(scopesCfg) == 0 {
		return nil
	}
	rules := make([]ScopeRule, 0, len(scopesCfg))
	for _, s := range scopesCfg {
		sm, _ := s.(map[string]any)
		if sm == nil {
			continue
		}
		var rule ScopeRule
		if match, ok := sm["match"].(map[string]any); ok {
			rule.Match.Agent, _ = match["agent"].(string)
			rule.Match.Team, _ = match["team"].(string)
			rule.Match.Model, _ = match["model"].(string)
			rule.Match.Provider, _ = match["provider"].(string)
		}
		if r, ok := sm["rules"].(map[string]any); ok {
			if v, ok := r["allowed_tools"].([]any); ok {
				for _, t := range v {
					if s, ok := t.(string); ok {
						rule.Override.AllowedTools = append(rule.Override.AllowedTools, s)
					}
				}
			}
			if v, ok := r["blocked_tools"].([]any); ok {
				for _, t := range v {
					if s, ok := t.(string); ok {
						rule.Override.BlockedTools = append(rule.Override.BlockedTools, s)
					}
				}
			}
		}
		rules = append(rules, rule)
	}
	return rules
}

func parseImmutableSections(cfg map[string]any) []ImmutableSection {
	sectionsCfg, _ := cfg["immutable_sections"].([]any)
	if len(sectionsCfg) == 0 {
		return nil
	}
	sections := make([]ImmutableSection, 0, len(sectionsCfg))
	for _, s := range sectionsCfg {
		sm, _ := s.(map[string]any)
		if sm == nil {
			continue
		}
		path, _ := sm["path"].(string)
		override, _ := sm["override"].(string)
		if path != "" {
			sections = append(sections, ImmutableSection{Path: path, Override: override})
		}
	}
	return sections
}

func parseOverrideConfig(cfg map[string]any) OverrideConfig {
	o, _ := cfg["override"].(map[string]any)
	if o == nil {
		return OverrideConfig{}
	}
	oc := OverrideConfig{}
	oc.Mechanism, _ = o["mechanism"].(string)
	oc.AdminSecretEnv, _ = o["admin_secret_env"].(string)
	oc.Fallback, _ = o["fallback"].(string)
	return oc
}

