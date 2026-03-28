package orchestrator

import (
	"context"
	"sync"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// AgentSeed holds the definition of an agent to seed into the database.
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

// AIProviderModule wraps an AI provider.Provider as a modular.Module.
// It registers itself in the service registry so steps can look it up by name.
type AIProviderModule struct {
	name       string
	provider   provider.Provider
	agents     []AgentSeed
	httpSource *HTTPSource // non-nil when test provider is in HTTP mode
}

// Name implements modular.Module.
func (m *AIProviderModule) Name() string { return m.name }

// Init registers this module as a named service.
// Returns an error if the module was constructed with an invalid provider type.
func (m *AIProviderModule) Init(app modular.Application) error {
	if ep, ok := m.provider.(*errProvider); ok {
		return ep.err
	}
	return app.RegisterService(m.name, m)
}

// ProvidesServices declares the provider service.
func (m *AIProviderModule) ProvidesServices() []modular.ServiceProvider {
	return []modular.ServiceProvider{
		{
			Name:        m.name,
			Description: "Ratchet AI provider: " + m.name,
			Instance:    m,
		},
	}
}

// RequiresServices declares no dependencies.
func (m *AIProviderModule) RequiresServices() []modular.ServiceDependency {
	return nil
}

// Start implements modular.Startable (no-op).
func (m *AIProviderModule) Start(_ context.Context) error { return nil }

// Stop implements modular.Stoppable (no-op).
func (m *AIProviderModule) Stop(_ context.Context) error { return nil }

// Provider returns the underlying AI provider.
func (m *AIProviderModule) Provider() provider.Provider { return m.provider }

// Agents returns the agent seeds configured for this provider module.
func (m *AIProviderModule) Agents() []AgentSeed { return m.agents }

// TestHTTPSource returns the HTTPSource if the provider is a test provider in HTTP mode.
func (m *AIProviderModule) TestHTTPSource() *HTTPSource { return m.httpSource }

// mockProvider is a simple scripted AI provider for testing and demos.
type mockProvider struct {
	responses []string
	idx       int
	mu        sync.Mutex
}

func (m *mockProvider) Name() string { return "mock" }

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

func (m *mockProvider) AuthModeInfo() provider.AuthModeInfo {
	return provider.AuthModeInfo{Mode: "none", DisplayName: "Mock (no auth)"}
}

// errProvider is a sentinel provider that always returns a configuration error.
// It is used when the factory receives an unrecognized provider type so that
// AIProviderModule.Init can fail fast rather than silently degrading to a stub.
type errProvider struct {
	err error
}

func (e *errProvider) Name() string { return "error" }

func (e *errProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	return nil, e.err
}

func (e *errProvider) Stream(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	return nil, e.err
}

func (e *errProvider) AuthModeInfo() provider.AuthModeInfo {
	return provider.AuthModeInfo{Mode: "none", DisplayName: "Error provider"}
}
