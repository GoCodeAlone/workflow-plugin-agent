// Package agent is a workflow EnginePlugin that provides AI agent primitives:
// the agent.provider module type, the step.agent_execute pipeline step,
// and related utility steps.
//
// Usage:
//
//	engine := workflow.NewEngine(workflow.WithPlugin(agent.New()))
package agent

import (
	"github.com/GoCodeAlone/workflow/capability"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/GoCodeAlone/workflow/schema"
)

// AgentPlugin implements plugin.EnginePlugin.
type AgentPlugin struct {
	plugin.BaseEnginePlugin
}

// New creates a new AgentPlugin ready to register with the workflow engine.
func New() *AgentPlugin {
	return &AgentPlugin{
		BaseEnginePlugin: plugin.BaseEnginePlugin{
			BaseNativePlugin: plugin.BaseNativePlugin{
				PluginName:        "agent",
				PluginVersion:     "0.1.0",
				PluginDescription: "AI agent primitives for workflow apps",
			},
			Manifest: plugin.PluginManifest{
				Name:        "workflow-plugin-agent",
				Version:     "0.1.0",
				Author:      "GoCodeAlone",
				Description: "AI agent primitives for workflow apps",
				ModuleTypes: []string{"agent.provider"},
				StepTypes:   []string{"step.agent_execute", "step.provider_test", "step.provider_models"},
				WiringHooks: []string{"agent.provider_registry"},
			},
		},
	}
}

// Capabilities returns the capability contracts for this plugin.
func (p *AgentPlugin) Capabilities() []capability.Contract {
	return nil
}

// ModuleFactories returns the module factories registered by this plugin.
func (p *AgentPlugin) ModuleFactories() map[string]plugin.ModuleFactory {
	return map[string]plugin.ModuleFactory{
		"agent.provider": newProviderModuleFactory(),
	}
}

// StepFactories returns the pipeline step factories registered by this plugin.
func (p *AgentPlugin) StepFactories() map[string]plugin.StepFactory {
	return map[string]plugin.StepFactory{
		"step.agent_execute":   newAgentExecuteStepFactory(),
		"step.provider_test":   newProviderTestFactory(),
		"step.provider_models": newProviderModelsFactory(),
	}
}

// WiringHooks returns the post-init wiring hooks for this plugin.
func (p *AgentPlugin) WiringHooks() []plugin.WiringHook {
	return []plugin.WiringHook{
		providerRegistryHook(),
	}
}

// NewProviderModuleFactory returns the plugin.ModuleFactory for "agent.provider".
// This is exported so other plugins can embed the factory without loading the
// full AgentPlugin (which would cause duplicate step type registration conflicts).
func NewProviderModuleFactory() plugin.ModuleFactory {
	return newProviderModuleFactory()
}

// NewProviderTestFactory returns the plugin.StepFactory for "step.provider_test".
// Exported for use by host plugins that embed agent capabilities without loading
// AgentPlugin as a standalone plugin.
func NewProviderTestFactory() plugin.StepFactory {
	return newProviderTestFactory()
}

// NewProviderModelsFactory returns the plugin.StepFactory for "step.provider_models".
// Exported for use by host plugins that embed agent capabilities without loading
// AgentPlugin as a standalone plugin.
func NewProviderModelsFactory() plugin.StepFactory {
	return newProviderModelsFactory()
}

// ProviderRegistryHook returns the wiring hook that creates the agent-provider-registry.
// Exported for use by host plugins that embed agent capabilities without loading
// AgentPlugin as a standalone plugin.
func ProviderRegistryHook() plugin.WiringHook {
	return providerRegistryHook()
}

// ModuleSchemas returns schema definitions for IDE completions and config validation.
func (p *AgentPlugin) ModuleSchemas() []*schema.ModuleSchema {
	return []*schema.ModuleSchema{
		{
			Type:        "agent.provider",
			Label:       "AI Provider",
			Category:    "AI",
			Description: "Wraps an AI provider (mock, test, anthropic, openai, copilot) as a module for agent execution.",
			ConfigFields: []schema.ConfigFieldDef{
				{Key: "provider", Label: "Provider Type", Type: schema.FieldTypeSelect, Options: []string{"mock", "test", "anthropic", "openai", "copilot"}, DefaultValue: "mock", Description: "AI provider backend"},
				{Key: "model", Label: "Model", Type: schema.FieldTypeString, Description: "Model identifier (e.g., claude-sonnet-4-20250514)"},
				{Key: "api_key", Label: "API Key", Type: schema.FieldTypeString, Description: "API key for real providers"},
				{Key: "base_url", Label: "Base URL", Type: schema.FieldTypeString, Description: "Optional base URL override"},
				{Key: "max_tokens", Label: "Max Tokens", Type: schema.FieldTypeNumber, Description: "Maximum tokens per response"},
				{Key: "responses", Label: "Mock Responses", Type: schema.FieldTypeArray, ArrayItemType: "string", Description: "Scripted responses for mock provider"},
				{Key: "test_mode", Label: "Test Mode", Type: schema.FieldTypeSelect, Options: []string{"scripted", "channel", "http"}, DefaultValue: "scripted", Description: "Test provider mode"},
				{Key: "scenario_file", Label: "Scenario File", Type: schema.FieldTypeFilePath, Description: "YAML file with test scenario steps"},
				{Key: "loop", Label: "Loop Responses", Type: schema.FieldTypeBool, DefaultValue: false, Description: "Loop scripted responses when exhausted"},
				{Key: "timeout", Label: "Timeout", Type: schema.FieldTypeDuration, Description: "Timeout for test provider HTTP mode"},
			},
			DefaultConfig: map[string]any{"provider": "mock"},
		},
	}
}
