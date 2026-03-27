package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/plugin"
)

// AgentSeed holds the definition of an agent to seed into the database on startup.
type AgentSeed struct {
	ID           string `yaml:"id"`
	Name         string `yaml:"name"`
	Role         string `yaml:"role"`
	SystemPrompt string `yaml:"system_prompt"`
	Provider     string `yaml:"provider"`
	Model        string `yaml:"model"`
	TeamID       string `yaml:"team_id"`
	IsLead       bool   `yaml:"is_lead"`
}

// ProviderModule wraps a provider.Provider as a modular.Module.
// It registers itself in the service registry so steps can look it up by name.
type ProviderModule struct {
	name       string
	prov       provider.Provider
	agents     []AgentSeed
	httpSource *provider.HTTPSource // non-nil when test provider uses HTTP mode
}

// Name implements modular.Module.
func (m *ProviderModule) Name() string { return m.name }

// Init registers this module as a named service.
func (m *ProviderModule) Init(app modular.Application) error {
	if ep, ok := m.prov.(*errProvider); ok {
		return ep.err
	}
	return app.RegisterService(m.name, m)
}

// ProvidesServices declares the provider service.
func (m *ProviderModule) ProvidesServices() []modular.ServiceProvider {
	return []modular.ServiceProvider{
		{
			Name:        m.name,
			Description: "AI provider: " + m.name,
			Instance:    m,
		},
	}
}

// RequiresServices declares no dependencies.
func (m *ProviderModule) RequiresServices() []modular.ServiceDependency { return nil }

// Start implements modular.Startable (no-op).
func (m *ProviderModule) Start(_ context.Context) error { return nil }

// Stop implements modular.Stoppable (no-op).
func (m *ProviderModule) Stop(_ context.Context) error { return nil }

// Provider returns the underlying AI provider.
func (m *ProviderModule) Provider() provider.Provider { return m.prov }

// Agents returns the agent seeds configured for this provider module.
func (m *ProviderModule) Agents() []AgentSeed { return m.agents }

// TestHTTPSource returns the HTTPSource if the provider is a test provider in HTTP mode.
func (m *ProviderModule) TestHTTPSource() *provider.HTTPSource { return m.httpSource }

// newProviderModuleFactory returns a plugin.ModuleFactory for "agent.provider".
func newProviderModuleFactory() plugin.ModuleFactory {
	return func(name string, cfg map[string]any) modular.Module {
		providerType, _ := cfg["provider"].(string)
		if providerType == "" {
			providerType = "mock"
		}
		model, _ := cfg["model"].(string)
		apiKey, _ := cfg["api_key"].(string)
		baseURL, _ := cfg["base_url"].(string)
		maxTokens := 0
		switch v := cfg["max_tokens"].(type) {
		case int:
			maxTokens = v
		case float64:
			maxTokens = int(v)
		}

		var p provider.Provider
		var httpSource *provider.HTTPSource

		switch providerType {
		case "mock":
			var responses []string
			if raw, ok := cfg["responses"]; ok {
				if list, ok := raw.([]any); ok {
					for _, item := range list {
						if s, ok := item.(string); ok {
							responses = append(responses, s)
						}
					}
				}
			}
			if len(responses) == 0 {
				responses = []string{"I have completed the task."}
			}
			p = &mockProvider{responses: responses}

		case "test":
			testMode, _ := cfg["test_mode"].(string)
			if testMode == "" {
				testMode = "scripted"
			}
			var source provider.ResponseSource
			switch testMode {
			case "scripted":
				var steps []provider.ScriptedStep
				if scenarioFile, ok := cfg["scenario_file"].(string); ok && scenarioFile != "" {
					scenario, err := provider.LoadScenario(scenarioFile)
					if err != nil {
						p = &mockProvider{responses: []string{fmt.Sprintf("Failed to load scenario: %v", err)}}
						break
					}
					source = provider.NewScriptedSourceFromScenario(scenario)
				} else {
					if raw, ok := cfg["steps"]; ok {
						if list, ok := raw.([]any); ok {
							for _, item := range list {
								if m, ok := item.(map[string]any); ok {
									step := provider.ScriptedStep{}
									step.Content, _ = m["content"].(string)
									step.Error, _ = m["error"].(string)
									if tcRaw, ok := m["tool_calls"]; ok {
										if tcList, ok := tcRaw.([]any); ok {
											for _, tcItem := range tcList {
												if tcMap, ok := tcItem.(map[string]any); ok {
													tc := provider.ToolCall{}
													tc.ID, _ = tcMap["id"].(string)
													tc.Name, _ = tcMap["name"].(string)
													if args, ok := tcMap["arguments"].(map[string]any); ok {
														tc.Arguments = args
													}
													step.ToolCalls = append(step.ToolCalls, tc)
												}
											}
										}
									}
									steps = append(steps, step)
								}
							}
						}
					}
					if len(steps) == 0 {
						steps = []provider.ScriptedStep{{Content: "Test provider: no steps configured."}}
					}
					loop := false
					if v, ok := cfg["loop"].(bool); ok {
						loop = v
					}
					source = provider.NewScriptedSource(steps, loop)
				}
			case "channel":
				channelSource, _, _ := provider.NewChannelSource()
				source = channelSource
			case "http":
				httpSource = provider.NewHTTPSource(nil)
				source = httpSource
			default:
				p = &mockProvider{responses: []string{fmt.Sprintf("Unknown test_mode %q", testMode)}}
			}
			if source != nil {
				var opts []provider.TestProviderOption
				if timeoutStr, ok := cfg["timeout"].(string); ok && timeoutStr != "" {
					if d, err := time.ParseDuration(timeoutStr); err == nil {
						opts = append(opts, provider.WithTimeout(d))
					}
				}
				p = provider.NewTestProvider(source, opts...)
			}

		case "anthropic":
			p = provider.NewAnthropicProvider(provider.AnthropicConfig{
				APIKey:    apiKey,
				Model:     model,
				BaseURL:   baseURL,
				MaxTokens: maxTokens,
			})

		case "openai":
			p = provider.NewOpenAIProvider(provider.OpenAIConfig{
				APIKey:    apiKey,
				Model:     model,
				BaseURL:   baseURL,
				MaxTokens: maxTokens,
			})

		case "copilot":
			p = provider.NewCopilotProvider(provider.CopilotConfig{
				Token:     apiKey,
				Model:     model,
				BaseURL:   baseURL,
				MaxTokens: maxTokens,
			})

		case "ollama":
			p = provider.NewOllamaProvider(provider.OllamaConfig{
				Model:     model,
				BaseURL:   baseURL,
				MaxTokens: maxTokens,
			})

		case "llama_cpp":
			p = provider.NewLlamaCppProvider(provider.LlamaCppConfig{
				BaseURL:   baseURL,
				MaxTokens: maxTokens,
			})

		default:
			p = &errProvider{err: fmt.Errorf("agent.provider %q: unrecognized provider type %q (supported: mock, test, anthropic, openai, copilot, ollama, llama_cpp)", name, providerType)}
		}

		// Parse agent seeds
		var agents []AgentSeed
		if raw, ok := cfg["agents"]; ok {
			if list, ok := raw.([]any); ok {
				for _, item := range list {
					if m, ok := item.(map[string]any); ok {
						agents = append(agents, extractAgentSeed(m))
					}
				}
			}
		}

		return &ProviderModule{
			name:       name,
			prov:       p,
			agents:     agents,
			httpSource: httpSource,
		}
	}
}

// mockProvider is a simple scripted AI provider for testing and demos.
type mockProvider struct {
	responses []string
	idx       int
	mu        sync.Mutex
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) AuthModeInfo() provider.AuthModeInfo {
	return provider.AuthModeInfo{
		Mode:        "mock",
		DisplayName: "Mock Provider",
		Description: "Scripted mock provider for testing and demos.",
		ServerSafe:  true,
	}
}

func (m *mockProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	m.mu.Lock()
	resp := m.responses[m.idx%len(m.responses)]
	m.idx++
	m.mu.Unlock()
	return &provider.Response{
		Content: resp,
		Usage:   provider.Usage{InputTokens: 10, OutputTokens: len(resp)},
	}, nil
}

func (m *mockProvider) Stream(ctx context.Context, messages []provider.Message, tools []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	resp, err := m.Chat(ctx, messages, tools)
	if err != nil {
		return nil, err
	}
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: "text", Text: resp.Content}
	ch <- provider.StreamEvent{Type: "done", Usage: &resp.Usage}
	close(ch)
	return ch, nil
}

// errProvider is a sentinel provider that always returns a configuration error.
type errProvider struct {
	err error
}

func (e *errProvider) Name() string { return "error" }

func (e *errProvider) AuthModeInfo() provider.AuthModeInfo {
	return provider.AuthModeInfo{
		Mode:        "error",
		DisplayName: "Error Provider",
		Description: "Sentinel provider that always returns a configuration error.",
	}
}
func (e *errProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	return nil, e.err
}
func (e *errProvider) Stream(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	return nil, e.err
}

// extractAgentSeed pulls an AgentSeed from a raw map.
func extractAgentSeed(m map[string]any) AgentSeed {
	seed := AgentSeed{}
	seed.ID, _ = m["id"].(string)
	seed.Name, _ = m["name"].(string)
	seed.Role, _ = m["role"].(string)
	seed.SystemPrompt, _ = m["system_prompt"].(string)
	seed.Provider, _ = m["provider"].(string)
	seed.Model, _ = m["model"].(string)
	seed.TeamID, _ = m["team_id"].(string)
	seed.IsLead, _ = m["is_lead"].(bool)
	return seed
}
