package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	gkprov "github.com/GoCodeAlone/workflow-plugin-agent/genkit"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/secrets"
)

// LLMProviderConfig represents a configured LLM provider stored in the database.
type LLMProviderConfig struct {
	ID         string `json:"id"`
	Alias      string `json:"alias"`
	Type       string `json:"type"`        // provider type (e.g. "anthropic", "openai", "copilot_models", "openai_azure", "anthropic_foundry", "anthropic_vertex", "anthropic_bedrock")
	Model      string `json:"model"`       // model identifier
	SecretName string `json:"secret_name"` // key in secrets provider for API key
	BaseURL    string `json:"base_url"`    // optional override
	MaxTokens  int    `json:"max_tokens"`  // optional override
	IsDefault  int    `json:"is_default"`  // 1 if this is the default provider
	Settings   string `json:"settings"`    // JSON object with provider-specific settings (resource, region, deployment_name, project_id, etc.)
}

// settings parses the Settings JSON into a map. Returns empty map on error.
func (c *LLMProviderConfig) settings() map[string]string {
	m := make(map[string]string)
	if c.Settings != "" && c.Settings != "{}" {
		_ = json.Unmarshal([]byte(c.Settings), &m)
	}
	return m
}

// ProviderFactory creates a provider.Provider from an API key and config.
type ProviderFactory func(apiKey string, cfg LLMProviderConfig) (provider.Provider, error)

// ProviderRegistry manages AI provider lifecycle: factory creation, caching, and DB lookup.
type ProviderRegistry struct {
	mu        sync.RWMutex
	db        *sql.DB
	secrets   secrets.Provider
	cache     map[string]provider.Provider
	factories map[string]ProviderFactory
}

// NewProviderRegistry creates a new ProviderRegistry with built-in factories registered.
func NewProviderRegistry(db *sql.DB, secretsProvider secrets.Provider) *ProviderRegistry {
	r := &ProviderRegistry{
		db:        db,
		secrets:   secretsProvider,
		cache:     make(map[string]provider.Provider),
		factories: make(map[string]ProviderFactory),
	}

	// Register built-in factories
	r.factories["mock"] = mockProviderFactory
	r.factories["anthropic"] = anthropicProviderFactory
	r.factories["openai"] = openaiProviderFactory
	r.factories["openrouter"] = openrouterProviderFactory
	r.factories["copilot"] = copilotProviderFactory
	r.factories["cohere"] = cohereProviderFactory
	r.factories["copilot_models"] = copilotModelsProviderFactory
	r.factories["openai_azure"] = openaiAzureProviderFactory
	r.factories["anthropic_foundry"] = anthropicFoundryProviderFactory
	r.factories["anthropic_vertex"] = anthropicVertexProviderFactory
	r.factories["anthropic_bedrock"] = anthropicBedrockProviderFactory
	r.factories["gemini"] = geminiProviderFactory
	r.factories["ollama"] = ollamaProviderFactory
	r.factories["llama_cpp"] = llamaCppProviderFactory

	return r
}

// GetByAlias looks up a provider by its alias. It checks the cache first,
// then falls back to DB lookup, secret resolution, and factory creation.
func (r *ProviderRegistry) GetByAlias(ctx context.Context, alias string) (provider.Provider, error) {
	// Check cache
	r.mu.RLock()
	if p, ok := r.cache[alias]; ok {
		r.mu.RUnlock()
		return p, nil
	}
	r.mu.RUnlock()

	// Look up config from DB
	cfg, err := r.loadConfig(ctx, alias)
	if err != nil {
		return nil, fmt.Errorf("provider registry: lookup alias %q: %w", alias, err)
	}

	return r.createAndCache(ctx, alias, cfg)
}

// GetDefault finds the default provider (is_default=1) and returns it.
func (r *ProviderRegistry) GetDefault(ctx context.Context) (provider.Provider, error) {
	if r.db == nil {
		return nil, fmt.Errorf("provider registry: no database configured")
	}

	var cfg LLMProviderConfig
	row := r.db.QueryRowContext(ctx,
		`SELECT id, alias, type, model, secret_name, base_url, max_tokens, settings, is_default
		 FROM llm_providers WHERE is_default = 1 LIMIT 1`)

	err := row.Scan(&cfg.ID, &cfg.Alias, &cfg.Type, &cfg.Model, &cfg.SecretName,
		&cfg.BaseURL, &cfg.MaxTokens, &cfg.Settings, &cfg.IsDefault)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("provider registry: no default provider configured")
		}
		return nil, fmt.Errorf("provider registry: query default: %w", err)
	}

	return r.createAndCache(ctx, cfg.Alias, &cfg)
}

// UpdateSecretsProvider swaps the underlying secrets provider and clears the cache.
func (r *ProviderRegistry) UpdateSecretsProvider(p secrets.Provider) {
	r.mu.Lock()
	r.secrets = p
	r.cache = make(map[string]provider.Provider)
	r.mu.Unlock()
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

// InvalidateCacheBySecret removes all cached providers that use the given secret name.
func (r *ProviderRegistry) InvalidateCacheBySecret(secretName string) {
	if r.db == nil {
		return
	}

	rows, err := r.db.Query(`SELECT alias FROM llm_providers WHERE secret_name = ?`, secretName)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()

	r.mu.Lock()
	defer r.mu.Unlock()
	for rows.Next() {
		var alias string
		if rows.Scan(&alias) == nil {
			delete(r.cache, alias)
		}
	}
}

// TestConnection sends a minimal test message to the provider and returns
// success, a status message, the latency, and any error.
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

// loadConfig retrieves an LLMProviderConfig from the database by alias.
func (r *ProviderRegistry) loadConfig(ctx context.Context, alias string) (*LLMProviderConfig, error) {
	if r.db == nil {
		return nil, fmt.Errorf("no database configured")
	}

	var cfg LLMProviderConfig
	row := r.db.QueryRowContext(ctx,
		`SELECT id, alias, type, model, secret_name, base_url, max_tokens, settings, is_default
		 FROM llm_providers WHERE alias = ?`, alias)

	err := row.Scan(&cfg.ID, &cfg.Alias, &cfg.Type, &cfg.Model, &cfg.SecretName,
		&cfg.BaseURL, &cfg.MaxTokens, &cfg.Settings, &cfg.IsDefault)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("alias %q not found", alias)
		}
		return nil, err
	}

	return &cfg, nil
}

// createAndCache resolves the secret, creates the provider via factory, and caches it.
func (r *ProviderRegistry) createAndCache(ctx context.Context, alias string, cfg *LLMProviderConfig) (provider.Provider, error) {
	// Resolve API key from secrets
	var apiKey string
	if cfg.SecretName != "" && r.secrets != nil {
		var err error
		apiKey, err = r.secrets.Get(ctx, cfg.SecretName)
		if err != nil {
			return nil, fmt.Errorf("provider registry: resolve secret %q: %w", cfg.SecretName, err)
		}
	}

	// Find factory
	factory, ok := r.factories[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("provider registry: unknown provider type %q", cfg.Type)
	}

	// Create provider
	p, err := factory(apiKey, *cfg)
	if err != nil {
		return nil, fmt.Errorf("provider registry: create %q: %w", alias, err)
	}

	// Cache
	r.mu.Lock()
	r.cache[alias] = p
	r.mu.Unlock()

	return p, nil
}

// Built-in factory functions

func mockProviderFactory(_ string, _ LLMProviderConfig) (provider.Provider, error) {
	return &mockProvider{responses: []string{"I have completed the task."}}, nil
}

func anthropicProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	return gkprov.NewAnthropicProvider(context.Background(), apiKey, cfg.Model, cfg.BaseURL, cfg.MaxTokens)
}

func openaiProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	return gkprov.NewOpenAIProvider(context.Background(), apiKey, cfg.Model, cfg.BaseURL, cfg.MaxTokens)
}

func openrouterProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	return gkprov.NewOpenAICompatibleProvider(context.Background(), "openrouter", apiKey, cfg.Model, baseURL, cfg.MaxTokens)
}

func copilotProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.githubcopilot.com"
	}
	return gkprov.NewOpenAICompatibleProvider(context.Background(), "copilot", apiKey, cfg.Model, baseURL, cfg.MaxTokens)
}

func cohereProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.cohere.ai/v1"
	}
	return gkprov.NewOpenAICompatibleProvider(context.Background(), "cohere", apiKey, cfg.Model, baseURL, cfg.MaxTokens)
}

func copilotModelsProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://models.inference.ai.azure.com"
	}
	return gkprov.NewOpenAICompatibleProvider(context.Background(), "copilot_models", apiKey, cfg.Model, baseURL, cfg.MaxTokens)
}

func openaiAzureProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	s := cfg.settings()
	return gkprov.NewAzureOpenAIProvider(context.Background(),
		s["resource"], s["deployment_name"], s["api_version"],
		apiKey, s["entra_token"], cfg.MaxTokens)
}

func anthropicFoundryProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	s := cfg.settings()
	return gkprov.NewAnthropicFoundryProvider(context.Background(),
		s["resource"], cfg.Model, apiKey, s["entra_token"], cfg.MaxTokens)
}

func anthropicVertexProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	s := cfg.settings()
	credJSON := s["credentials_json"]
	if credJSON == "" {
		credJSON = apiKey // fallback: secret may contain the full GCP credentials JSON
	}
	return gkprov.NewVertexAIProvider(context.Background(),
		s["project_id"], s["region"], cfg.Model, credJSON, cfg.MaxTokens)
}

func geminiProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	return gkprov.NewGoogleAIProvider(context.Background(), apiKey, cfg.Model, cfg.MaxTokens)
}

func ollamaProviderFactory(_ string, cfg LLMProviderConfig) (provider.Provider, error) {
	return gkprov.NewOllamaProvider(context.Background(), cfg.Model, cfg.BaseURL, cfg.MaxTokens)
}

func llamaCppProviderFactory(_ string, cfg LLMProviderConfig) (provider.Provider, error) {
	// llama.cpp serves an OpenAI-compatible API
	return gkprov.NewOpenAICompatibleProvider(context.Background(), "llama_cpp", "", cfg.Model, cfg.BaseURL, cfg.MaxTokens)
}

func anthropicBedrockProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	s := cfg.settings()
	return gkprov.NewBedrockProvider(context.Background(),
		s["region"], cfg.Model, s["access_key_id"], apiKey, s["session_token"], cfg.BaseURL, cfg.MaxTokens)
}
