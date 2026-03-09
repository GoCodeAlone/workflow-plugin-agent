package agent

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/config"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/GoCodeAlone/workflow/secrets"
)

// LLMProviderConfig represents a configured LLM provider stored in the database.
type LLMProviderConfig struct {
	ID         string `json:"id"`
	Alias      string `json:"alias"`
	Type       string `json:"type"`
	Model      string `json:"model"`
	SecretName string `json:"secret_name"`
	BaseURL    string `json:"base_url"`
	MaxTokens  int    `json:"max_tokens"`
	IsDefault  int    `json:"is_default"`
}

// ProviderFactory creates a provider.Provider from an API key and config.
type ProviderFactory func(apiKey string, cfg LLMProviderConfig) (provider.Provider, error)

// ProviderRegistry manages AI provider lifecycle: factory creation, caching, and DB lookup.
type ProviderRegistry struct {
	mu        sync.RWMutex
	db        *sql.DB
	secrets   secrets.Provider
	cache     map[string]provider.Provider
	Factories map[string]ProviderFactory
}

// NewProviderRegistry creates a new ProviderRegistry with built-in factories registered.
func NewProviderRegistry(db *sql.DB, secretsProvider secrets.Provider) *ProviderRegistry {
	r := &ProviderRegistry{
		db:        db,
		secrets:   secretsProvider,
		cache:     make(map[string]provider.Provider),
		Factories: make(map[string]ProviderFactory),
	}

	r.Factories["mock"] = func(_ string, _ LLMProviderConfig) (provider.Provider, error) {
		return &mockProvider{responses: []string{"I have completed the task."}}, nil
	}
	r.Factories["anthropic"] = func(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
		return provider.NewAnthropicProvider(provider.AnthropicConfig{
			APIKey:    apiKey,
			Model:     cfg.Model,
			BaseURL:   cfg.BaseURL,
			MaxTokens: cfg.MaxTokens,
		}), nil
	}
	r.Factories["openai"] = func(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
		return provider.NewOpenAIProvider(provider.OpenAIConfig{
			APIKey:    apiKey,
			Model:     cfg.Model,
			BaseURL:   cfg.BaseURL,
			MaxTokens: cfg.MaxTokens,
		}), nil
	}
	r.Factories["openrouter"] = func(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://openrouter.ai/api/v1"
		}
		return provider.NewOpenAIProvider(provider.OpenAIConfig{
			APIKey:    apiKey,
			Model:     cfg.Model,
			BaseURL:   cfg.BaseURL,
			MaxTokens: cfg.MaxTokens,
		}), nil
	}
	r.Factories["copilot"] = func(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
		return provider.NewCopilotProvider(provider.CopilotConfig{
			Token:     apiKey,
			Model:     cfg.Model,
			BaseURL:   cfg.BaseURL,
			MaxTokens: cfg.MaxTokens,
		}), nil
	}

	return r
}

// GetByAlias looks up a provider by its alias.
func (r *ProviderRegistry) GetByAlias(ctx context.Context, alias string) (provider.Provider, error) {
	r.mu.RLock()
	if p, ok := r.cache[alias]; ok {
		r.mu.RUnlock()
		return p, nil
	}
	r.mu.RUnlock()

	cfg, err := r.loadConfig(ctx, alias)
	if err != nil {
		return nil, fmt.Errorf("provider registry: lookup alias %q: %w", alias, err)
	}

	return r.createAndCache(ctx, alias, cfg)
}

// GetDefault finds the default provider (is_default=1).
func (r *ProviderRegistry) GetDefault(ctx context.Context) (provider.Provider, error) {
	if r.db == nil {
		return nil, fmt.Errorf("provider registry: no database configured")
	}

	var cfg LLMProviderConfig
	row := r.db.QueryRowContext(ctx,
		`SELECT id, alias, type, model, secret_name, base_url, max_tokens, is_default
		 FROM llm_providers WHERE is_default = 1 LIMIT 1`)

	err := row.Scan(&cfg.ID, &cfg.Alias, &cfg.Type, &cfg.Model, &cfg.SecretName,
		&cfg.BaseURL, &cfg.MaxTokens, &cfg.IsDefault)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("provider registry: no default provider configured")
		}
		return nil, fmt.Errorf("provider registry: query default: %w", err)
	}

	return r.createAndCache(ctx, cfg.Alias, &cfg)
}

// InvalidateCache clears all cached providers.
func (r *ProviderRegistry) InvalidateCache() {
	r.mu.Lock()
	r.cache = make(map[string]provider.Provider)
	r.mu.Unlock()
}

// InvalidateCacheAlias removes a specific cached provider by alias.
func (r *ProviderRegistry) InvalidateCacheAlias(alias string) {
	r.mu.Lock()
	delete(r.cache, alias)
	r.mu.Unlock()
}

// TestConnection sends a minimal test message to the provider.
func (r *ProviderRegistry) TestConnection(ctx context.Context, alias string) (bool, string, time.Duration, error) {
	p, err := r.GetByAlias(ctx, alias)
	if err != nil {
		return false, fmt.Sprintf("failed to resolve provider: %v", err), 0, err
	}

	start := time.Now()
	_, err = p.Chat(ctx, []provider.Message{
		{Role: provider.RoleUser, Content: "Hello"},
	}, nil)
	elapsed := time.Since(start)

	if err != nil {
		return false, fmt.Sprintf("connection failed: %v", err), elapsed, err
	}
	return true, "connection successful", elapsed, nil
}

func (r *ProviderRegistry) loadConfig(ctx context.Context, alias string) (*LLMProviderConfig, error) {
	if r.db == nil {
		return nil, fmt.Errorf("no database configured")
	}

	var cfg LLMProviderConfig
	row := r.db.QueryRowContext(ctx,
		`SELECT id, alias, type, model, secret_name, base_url, max_tokens, is_default
		 FROM llm_providers WHERE alias = ?`, alias)

	err := row.Scan(&cfg.ID, &cfg.Alias, &cfg.Type, &cfg.Model, &cfg.SecretName,
		&cfg.BaseURL, &cfg.MaxTokens, &cfg.IsDefault)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("alias %q not found", alias)
		}
		return nil, err
	}

	return &cfg, nil
}

func (r *ProviderRegistry) createAndCache(ctx context.Context, alias string, cfg *LLMProviderConfig) (provider.Provider, error) {
	var apiKey string
	if cfg.SecretName != "" && r.secrets != nil {
		var err error
		apiKey, err = r.secrets.Get(ctx, cfg.SecretName)
		if err != nil {
			return nil, fmt.Errorf("provider registry: resolve secret %q: %w", cfg.SecretName, err)
		}
	}

	factory, ok := r.Factories[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("provider registry: unknown provider type %q", cfg.Type)
	}

	p, err := factory(apiKey, *cfg)
	if err != nil {
		return nil, fmt.Errorf("provider registry: create %q: %w", alias, err)
	}

	r.mu.Lock()
	r.cache[alias] = p
	r.mu.Unlock()

	return p, nil
}

// providerRegistryHook creates a ProviderRegistry and registers it in the service registry.
func providerRegistryHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "agent.provider_registry",
		Priority: 83,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			if db == nil {
				return nil // no DB, skip
			}

			var sp secrets.Provider
			// Allow any secret guard that implements secrets.Provider to be wired in.
			// This is a best-effort lookup — consumers can also call RegisterService directly.
			for _, name := range []string{"ratchet-secret-guard", "agent-secret-guard", "secret-guard"} {
				if svc, ok := app.SvcRegistry()[name]; ok {
					if p, ok := svc.(interface{ Provider() secrets.Provider }); ok {
						sp = p.Provider()
						break
					}
				}
			}

			registry := NewProviderRegistry(db, sp)
			_ = app.RegisterService("agent-provider-registry", registry)
			return nil
		},
	}
}
