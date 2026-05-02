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

// agentStepSchemas returns the strict step contract descriptors for all steps
// provided by this plugin. These schemas are registered at plugin load time and
// used by wfctl docs/validate to enumerate expected config fields and outputs.
func agentStepSchemas() []*schema.StepSchema {
	return []*schema.StepSchema{
		{
			Type:        "step.agent_execute",
			Plugin:      "workflow-plugin-agent",
			Description: "Runs the autonomous agent loop: LLM call → tool execution → repeat until the task is complete or max_iterations is reached.",
			ConfigFields: []schema.ConfigFieldDef{
				{Key: "provider_service", Label: "Provider Service", Type: schema.FieldTypeString, Description: "Name of the AI provider service in the module registry (default: ratchet-ai)"},
				{Key: "max_iterations", Label: "Max Iterations", Type: schema.FieldTypeNumber, DefaultValue: 10, Description: "Maximum number of agent loop iterations before halting"},
				{Key: "approval_timeout", Label: "Approval Timeout", Type: schema.FieldTypeDuration, DefaultValue: "30m", Description: "How long to wait for human approval before timing out"},
				{Key: "request_timeout", Label: "Request Timeout", Type: schema.FieldTypeDuration, DefaultValue: "60m", Description: "Maximum duration for a single agent execution"},
				{Key: "loop_detection", Label: "Loop Detection", Type: schema.FieldTypeJSON, Description: "Loop detection tuning: max_consecutive, max_errors, max_alternating, max_no_progress"},
				{Key: "context", Label: "Context Management", Type: schema.FieldTypeJSON, Description: "Context window management options (compaction_threshold: 0.0–1.0)"},
			},
			Outputs: []schema.StepOutputDef{
				{Key: "result", Type: "string", Description: "Final text content produced by the agent"},
				{Key: "status", Type: "string", Description: "Completion status: completed, max_iterations, error"},
				{Key: "iterations", Type: "number", Description: "Number of agent loop iterations executed"},
				{Key: "thinking", Type: "string", Description: "Extended thinking output (present when the model supports it)"},
				{Key: "error", Type: "string", Description: "Error message when status is 'error'"},
			},
			ReadKeys: []string{
				"system_prompt", "description", "task", "agent_name", "name",
				"agent_id", "task_id", "id", "project_id", "provider",
			},
		},
		{
			Type:        "step.provider_test",
			Plugin:      "workflow-plugin-agent",
			Description: "Tests connectivity to a registered AI provider by alias, measuring round-trip latency.",
			ConfigFields: []schema.ConfigFieldDef{
				{Key: "alias", Label: "Provider Alias", Type: schema.FieldTypeString, Description: "Alias of the provider to test (can be a template expression)"},
			},
			Outputs: []schema.StepOutputDef{
				{Key: "success", Type: "boolean", Description: "true when the provider responded successfully"},
				{Key: "message", Type: "string", Description: "Human-readable result or error description"},
				{Key: "latency_ms", Type: "number", Description: "Round-trip latency in milliseconds"},
			},
			ReadKeys: []string{"alias"},
		},
		{
			Type:        "step.provider_models",
			Plugin:      "workflow-plugin-agent",
			Description: "Lists available models from an AI provider API, accepting provider credentials from the pipeline context.",
			ConfigFields: []schema.ConfigFieldDef{},
			Outputs: []schema.StepOutputDef{
				{Key: "success", Type: "boolean", Description: "true when model listing succeeded"},
				{Key: "models", Type: "array", Description: "Array of model objects: {id, name, context_window}"},
				{Key: "error", Type: "string", Description: "Error description when success is false"},
			},
			ReadKeys: []string{"type", "api_key", "base_url"},
		},
		{
			Type:        "step.model_pull",
			Plugin:      "workflow-plugin-agent",
			Description: "Ensures a local model is available by pulling it from Ollama or downloading from HuggingFace Hub.",
			ConfigFields: []schema.ConfigFieldDef{
				{Key: "provider", Label: "Source", Type: schema.FieldTypeSelect, Options: []string{"ollama", "huggingface"}, Required: true, Description: "Model source: ollama (local server) or huggingface (Hub download)"},
				{Key: "model", Label: "Model", Type: schema.FieldTypeString, Required: true, Description: "Model name (Ollama) or HuggingFace repo (org/repo)"},
				{Key: "file", Label: "File", Type: schema.FieldTypeString, Description: "Filename within the HuggingFace repo to download (required for huggingface source)"},
				{Key: "output_dir", Label: "Output Directory", Type: schema.FieldTypeFilePath, Description: "Local directory to store downloaded files (defaults to ~/.cache/workflow/models)"},
				{Key: "base_url", Label: "Ollama Base URL", Type: schema.FieldTypeString, DefaultValue: "http://localhost:11434", Description: "Ollama server base URL (only used for ollama source)"},
			},
			Outputs: []schema.StepOutputDef{
				{Key: "status", Type: "string", Description: "Result status: ready (already present), downloaded, or error"},
				{Key: "model_path", Type: "string", Description: "Local path or name of the model"},
				{Key: "size_bytes", Type: "number", Description: "File size in bytes (0 for Ollama models)"},
				{Key: "error", Type: "string", Description: "Error message when status is 'error'"},
			},
			ReadKeys: []string{},
		},
	}
}

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
				StepTypes:   []string{"step.agent_execute", "step.provider_test", "step.provider_models", "step.model_pull"},
				WiringHooks: []string{"agent.provider_registry"},
				StepSchemas: agentStepSchemas(),
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
		"step.model_pull":      newModelPullStepFactory(),
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

// NewModelPullStepFactory returns the plugin.StepFactory for "step.model_pull".
// Exported for use by host plugins that embed agent capabilities without loading
// AgentPlugin as a standalone plugin.
func NewModelPullStepFactory() plugin.StepFactory {
	return newModelPullStepFactory()
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
				{Key: "provider", Label: "Provider Type", Type: schema.FieldTypeSelect, Options: []string{"mock", "test", "anthropic", "openai", "copilot", "ollama", "llama_cpp"}, DefaultValue: "mock", Description: "AI provider backend"},
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

// StepSchemas returns strict contract descriptors for all pipeline step types
// registered by this plugin. Each descriptor declares config fields, outputs, and
// context keys so that wfctl docs/validate can enumerate expected shape without
// running the plugin.
func (p *AgentPlugin) StepSchemas() []*schema.StepSchema {
	return agentStepSchemas()
}
