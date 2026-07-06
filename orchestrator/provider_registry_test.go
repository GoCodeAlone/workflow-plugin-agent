package orchestrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/secrets"

	_ "modernc.org/sqlite"
)

// newProviderRegistry wraps a concrete secrets.Provider in an accessor for
// tests that don't exercise the lazy path (back-compat with the pre-fix call
// shape NewProviderRegistry(db, sec)).
func newProviderRegistry(db *sql.DB, sec secrets.Provider) *ProviderRegistry {
	return NewProviderRegistry(db, func() secrets.Provider { return sec })
}

// memSecretsProvider is an in-memory secrets provider for testing.
type memSecretsProvider struct {
	data map[string]string
}

func (m *memSecretsProvider) Name() string { return "mem" }

func (m *memSecretsProvider) Get(_ context.Context, key string) (string, error) {
	v, ok := m.data[key]
	if !ok {
		return "", nil
	}
	return v, nil
}

func (m *memSecretsProvider) Set(_ context.Context, key, value string) error {
	m.data[key] = value
	return nil
}

func (m *memSecretsProvider) Delete(_ context.Context, key string) error {
	delete(m.data, key)
	return nil
}

func (m *memSecretsProvider) List(_ context.Context) ([]string, error) {
	var keys []string
	for k := range m.data {
		keys = append(keys, k)
	}
	return keys, nil
}

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS llm_providers (
		id TEXT PRIMARY KEY,
		alias TEXT NOT NULL UNIQUE,
		type TEXT NOT NULL DEFAULT 'mock',
		model TEXT NOT NULL DEFAULT '',
		secret_name TEXT NOT NULL DEFAULT '',
		base_url TEXT NOT NULL DEFAULT '',
		max_tokens INTEGER NOT NULL DEFAULT 4096,
		settings TEXT NOT NULL DEFAULT '{}',
		is_default INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT (datetime('now')),
		updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestProviderRegistryGetByAlias_Mock(t *testing.T) {
	db := setupTestDB(t)
	secrets := &memSecretsProvider{data: map[string]string{}}

	_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type) VALUES ('p1', 'test-mock', 'mock')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newProviderRegistry(db, secrets)
	p, err := reg.GetByAlias(context.Background(), "test-mock")
	if err != nil {
		t.Fatalf("GetByAlias: %v", err)
	}
	if p.Name() != "mock" {
		t.Errorf("expected mock provider, got %q", p.Name())
	}

	// Second call should hit cache
	p2, err := reg.GetByAlias(context.Background(), "test-mock")
	if err != nil {
		t.Fatalf("GetByAlias (cached): %v", err)
	}
	if p2 != p {
		t.Error("expected same cached instance")
	}
}

func TestProviderRegistryGetByAlias_Anthropic(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{
		"ANTHROPIC_API_KEY": "sk-test-123",
	}}

	_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type, model, secret_name)
		VALUES ('p2', 'my-claude', 'anthropic', 'claude-sonnet-4-20250514', 'ANTHROPIC_API_KEY')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newProviderRegistry(db, sec)
	p, err := reg.GetByAlias(context.Background(), "my-claude")
	if err != nil {
		t.Fatalf("GetByAlias: %v", err)
	}
	if p.Name() != "anthropic" {
		t.Errorf("expected anthropic provider, got %q", p.Name())
	}
}

func TestProviderRegistryGetDefault(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}

	_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type, is_default) VALUES ('p1', 'default-mock', 'mock', 1)`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newProviderRegistry(db, sec)
	p, err := reg.GetDefault(context.Background())
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if p.Name() != "mock" {
		t.Errorf("expected mock provider, got %q", p.Name())
	}
}

func TestProviderRegistryGetDefault_NoDefault(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}

	reg := newProviderRegistry(db, sec)
	_, err := reg.GetDefault(context.Background())
	if err == nil {
		t.Fatal("expected error when no default provider exists")
	}
}

func TestProviderRegistryNotFound(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}

	reg := newProviderRegistry(db, sec)
	_, err := reg.GetByAlias(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent alias")
	}
}

func TestProviderRegistryInvalidateCache(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}

	_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type) VALUES ('p1', 'cached', 'mock')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newProviderRegistry(db, sec)

	// Populate cache
	p1, err := reg.GetByAlias(context.Background(), "cached")
	if err != nil {
		t.Fatalf("GetByAlias: %v", err)
	}

	// Invalidate by alias
	reg.InvalidateCacheAlias("cached")

	// Should create a new instance
	p2, err := reg.GetByAlias(context.Background(), "cached")
	if err != nil {
		t.Fatalf("GetByAlias after invalidate: %v", err)
	}
	if p1 == p2 {
		t.Error("expected different instance after cache invalidation")
	}
}

func TestProviderRegistryInvalidateCacheBySecret(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{
		"MY_KEY": "test-value",
	}}

	_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type, secret_name)
		VALUES ('p1', 'uses-secret', 'mock', 'MY_KEY')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newProviderRegistry(db, sec)

	// Populate cache
	_, err = reg.GetByAlias(context.Background(), "uses-secret")
	if err != nil {
		t.Fatalf("GetByAlias: %v", err)
	}

	// Verify cached
	reg.mu.RLock()
	_, cached := reg.cache["uses-secret"]
	reg.mu.RUnlock()
	if !cached {
		t.Fatal("expected provider to be cached")
	}

	// Invalidate by secret
	reg.InvalidateCacheBySecret("MY_KEY")

	reg.mu.RLock()
	_, cached = reg.cache["uses-secret"]
	reg.mu.RUnlock()
	if cached {
		t.Error("expected cache to be invalidated")
	}
}

func TestProviderRegistryUnknownType(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}

	_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type) VALUES ('p1', 'bad-type', 'unknown_provider')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newProviderRegistry(db, sec)
	_, err = reg.GetByAlias(context.Background(), "bad-type")
	if err == nil {
		t.Fatal("expected error for unknown provider type")
	}
}

func TestProviderRegistryTestConnection(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}

	_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type) VALUES ('p1', 'test-conn', 'mock')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newProviderRegistry(db, sec)
	ok, msg, latency, err := reg.TestConnection(context.Background(), "test-conn")
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if !ok {
		t.Error("expected successful connection test")
	}
	if msg != "connection successful" {
		t.Errorf("expected 'connection successful', got %q", msg)
	}
	if latency <= 0 {
		t.Error("expected positive latency")
	}
}

func TestProviderRegistryNewProviderTypes(t *testing.T) {
	tests := []struct {
		name     string
		typ      string
		settings string
		secret   string
		wantName string
	}{
		{"copilot_models", "copilot_models", "{}", "test-secret", "copilot_models"},
		{"openai_chatgpt", "openai_chatgpt", "{}", `{"access_token":"token","refresh_token":"refresh","account_id":"acct"}`, "openai_chatgpt"},
		{"openai_azure", "openai_azure", `{"resource":"myres","deployment_name":"gpt4"}`, "test-secret", "openai_azure"},
		{"anthropic_foundry", "anthropic_foundry", `{"resource":"myres"}`, "test-secret", "anthropic_foundry"},
		{"anthropic_bedrock", "anthropic_bedrock", `{"region":"us-east-1","access_key_id":"AKID"}`, "test-secret", "anthropic_bedrock"},
	}
	// Note: anthropic_vertex requires valid GCP credentials JSON or ADC and is tested separately.

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			sec := &memSecretsProvider{data: map[string]string{
				"TEST_KEY": tt.secret,
			}}

			_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type, model, secret_name, settings)
				VALUES (?, ?, ?, 'test-model', 'TEST_KEY', ?)`,
				tt.name, tt.name, tt.typ, tt.settings)
			if err != nil {
				t.Fatalf("insert: %v", err)
			}

			reg := newProviderRegistry(db, sec)
			p, err := reg.GetByAlias(t.Context(), tt.name)
			if err != nil {
				t.Fatalf("GetByAlias: %v", err)
			}
			if p.Name() != tt.wantName {
				t.Errorf("expected %q provider, got %q", tt.wantName, p.Name())
			}
		})
	}
}

func TestProviderRegistryVertexFactoryRegistered(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}
	reg := newProviderRegistry(db, sec)

	if _, ok := reg.factories["anthropic_vertex"]; !ok {
		t.Fatal("anthropic_vertex factory not registered")
	}
}

func TestProviderRegistry_HasOllamaFactory(t *testing.T) {
	r := NewProviderRegistry(nil, nil)
	if _, ok := r.factories["ollama"]; !ok {
		t.Error("expected 'ollama' factory to be registered")
	}
}

func TestProviderRegistry_HasLlamaCppFactory(t *testing.T) {
	r := NewProviderRegistry(nil, nil)
	if _, ok := r.factories["llama_cpp"]; !ok {
		t.Error("expected 'llama_cpp' factory to be registered")
	}
}

func TestProviderRegistryChatMock(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}

	_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type) VALUES ('p1', 'chat-mock', 'mock')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := newProviderRegistry(db, sec)
	p, err := reg.GetByAlias(context.Background(), "chat-mock")
	if err != nil {
		t.Fatalf("GetByAlias: %v", err)
	}

	resp, err := p.Chat(context.Background(), []provider.Message{
		{Role: provider.RoleUser, Content: "Hello"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content == "" {
		t.Error("expected non-empty response")
	}
}

// ---------------------------------------------------------------------------
// Lazy-accessor + hot-swap registry tests (ported from secret_guard_lazy_test.go
// when SecretGuard was deleted; M3). These prove the ProviderRegistry resolves a
// vault-backed secret via the LAZY holder.Provider accessor (the method value
// secretsResolverHook wires, not a nil wiring-time snapshot) and that runtime
// hot-swap via UpdateSecretsProvider takes precedence over the lazy accessor.
// The accessor source changed from SecretGuard.Provider to secretsHolder.Provider;
// the registry behavior under test is unchanged.
// ---------------------------------------------------------------------------

// TestProviderRegistryResolvesSecretViaLazyHolder proves the ProviderRegistry
// resolves a vault-backed secret via the LAZY holder.Provider accessor (the
// method value providerRegistryHook wires), NOT a nil wiring-time snapshot.
//
// This is the regression test for the PR4 lazy-resolve change: providerRegistryHook
// runs at wiring time (pre-Start), where the engine secrets.vault module's
// Provider() is nil. The registry must defer resolution to provider-resolution
// time (post-Start) so vault-backed AI providers (those with SecretName) get
// their real API key instead of an empty one.
func TestProviderRegistryResolvesSecretViaLazyHolder(t *testing.T) {
	const vaultKey = "vault"
	known := &mockSecretsProvider{secrets: map[string]string{
		"ANTHROPIC_API_KEY": "sk-vault-resolved-key",
	}}
	// Pre-Start: module registered, Provider() nil.
	vaultMod := &startableVaultModule{}
	app := &lazyStubApp{services: map[string]any{vaultKey: vaultMod}}

	// === WIRING TIME (pre-Start) ===
	// Construct the holder exactly as secretsResolverHook does: no pre-set
	// provider, armed for lazy resolution against the engine module.
	holder := &secretsHolder{app: app, vaultKey: vaultKey, redactor: secrets.NewRedactor()}

	// The registry is built with the holder.Provider METHOD VALUE (lazy
	// accessor), exactly as providerRegistryHook wires it. It must NOT snapshot
	// nil here.
	reg := NewProviderRegistry(setupTestDB(t), holder.Provider)

	_, err := reg.db.Exec(`INSERT INTO llm_providers (id, alias, type, model, secret_name)
		VALUES ('p1', 'vaulted-claude', 'anthropic', 'claude-sonnet-4-20250514', 'ANTHROPIC_API_KEY')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// === POST-START ===
	// module.Start() runs after wiring hooks and populates the module's Provider.
	vaultMod.Start(known)

	// === PROVIDER-RESOLUTION TIME (post-Start, e.g. agent_execute) ===
	// Resolve the provider. The registry invokes the lazy accessor, which
	// triggers the holder's lazy-resolve of the now-populated engine module
	// Provider. If the registry had snapshotted nil at wiring time, the API key
	// would be empty and the anthropic factory would reject it.
	p, err := reg.GetByAlias(context.Background(), "vaulted-claude")
	if err != nil {
		t.Fatalf("GetByAlias via lazy holder: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider resolved via lazy holder")
	}
	if p.Name() != "anthropic" {
		t.Errorf("expected anthropic provider, got %q", p.Name())
	}

	// The holder must now have resolved the engine module's provider (lazy
	// resolve fired during the registry's resolution), proving the accessor
	// pathway works end-to-end rather than a nil snapshot.
	if holder.Provider() != known {
		t.Errorf("holder.Provider() not resolved to engine module provider after registry resolution: got %T", holder.Provider())
	}
}

// TestProviderRegistryNilAccessorNoPanic proves the wiring-time nil-accessor
// path (no secretService registered, or holder unresolved) degrades gracefully:
// the registry builds without panic and a SecretName provider resolves with an
// empty API key (existing behavior — no secret resolution, but no panic either).
func TestProviderRegistryNilAccessorNoPanic(t *testing.T) {
	// NewProviderRegistry with a nil accessor must not panic; it's internally
	// normalized to func() secrets.Provider { return nil }.
	reg := NewProviderRegistry(setupTestDB(t), nil)

	// A SecretName provider resolves with an empty key (nil accessor → sp == nil
	// → secret resolution skipped). This mirrors the pre-fix nil-snapshot
	// behavior for the no-vault path.
	_, err := reg.db.Exec(`INSERT INTO llm_providers (id, alias, type, secret_name)
		VALUES ('p1', 'no-vault', 'mock', 'SOME_SECRET')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	p, err := reg.GetByAlias(context.Background(), "no-vault")
	if err != nil {
		t.Fatalf("GetByAlias with nil accessor: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil mock provider despite nil accessor")
	}
	if p.Name() != "mock" {
		t.Errorf("expected mock provider, got %q", p.Name())
	}
}

// TestProviderRegistryUpdateSecretsProviderOverridesLazy proves the runtime
// hot-swap (UpdateSecretsProvider on the ProviderRegistry) takes precedence
// over the lazy holder accessor: after the swap, resolution uses the new
// provider and the lazy accessor is no longer consulted.
func TestProviderRegistryUpdateSecretsProviderOverridesLazy(t *testing.T) {
	const vaultKey = "vault"
	// Lazy-holder-backed provider would resolve "K" -> "lazy-val".
	lazyProvider := &mockSecretsProvider{secrets: map[string]string{"K": "lazy-val"}}
	vaultMod := &startableVaultModule{}
	vaultMod.Start(lazyProvider)
	app := &lazyStubApp{services: map[string]any{vaultKey: vaultMod}}

	holder := &secretsHolder{app: app, vaultKey: vaultKey, redactor: secrets.NewRedactor()}
	reg := NewProviderRegistry(setupTestDB(t), holder.Provider)

	// A factory that records the resolved apiKey, so we can assert WHICH provider
	// supplied the secret (hot-swap vs lazy).
	var resolvedKey string
	reg.factories["recording"] = func(_ context.Context, apiKey string, _ LLMProviderConfig) (provider.Provider, error) {
		resolvedKey = apiKey
		return &mockProvider{responses: []string{"ok"}}, nil
	}

	_, err := reg.db.Exec(`INSERT INTO llm_providers (id, alias, type, secret_name)
		VALUES ('p1', 'swap-test', 'recording', 'K')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Hot-swap to a provider that resolves "K" -> "hotswap-val".
	hotSwap := &mockSecretsProvider{secrets: map[string]string{"K": "hotswap-val"}}
	reg.UpdateSecretsProvider(hotSwap)

	if _, err := reg.GetByAlias(context.Background(), "swap-test"); err != nil {
		t.Fatalf("GetByAlias after hot-swap: %v", err)
	}

	// The registry must have resolved the secret from the HOT-SWAP provider,
	// proving it took precedence over the lazy holder accessor.
	if resolvedKey != "hotswap-val" {
		t.Errorf("hot-swap did not take precedence: resolved key %q, want %q", resolvedKey, "hotswap-val")
	}
}
