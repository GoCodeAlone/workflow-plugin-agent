package orchestrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"

	_ "modernc.org/sqlite"
)

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

	reg := NewProviderRegistry(db, secrets)
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

	reg := NewProviderRegistry(db, sec)
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

	reg := NewProviderRegistry(db, sec)
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

	reg := NewProviderRegistry(db, sec)
	_, err := reg.GetDefault(context.Background())
	if err == nil {
		t.Fatal("expected error when no default provider exists")
	}
}

func TestProviderRegistryNotFound(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}

	reg := NewProviderRegistry(db, sec)
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

	reg := NewProviderRegistry(db, sec)

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

	reg := NewProviderRegistry(db, sec)

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

	reg := NewProviderRegistry(db, sec)
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

	reg := NewProviderRegistry(db, sec)
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
		wantName string
	}{
		{"copilot_models", "copilot_models", "{}", "copilot_models"},
		{"openai_azure", "openai_azure", `{"resource":"myres","deployment_name":"gpt4"}`, "openai_azure"},
		{"anthropic_foundry", "anthropic_foundry", `{"resource":"myres"}`, "anthropic_foundry"},
		{"anthropic_bedrock", "anthropic_bedrock", `{"region":"us-east-1","access_key_id":"AKID"}`, "anthropic_bedrock"},
	}
	// Note: anthropic_vertex requires valid GCP credentials JSON or ADC and is tested separately.

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			sec := &memSecretsProvider{data: map[string]string{
				"TEST_KEY": "test-secret",
			}}

			_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type, model, secret_name, settings)
				VALUES (?, ?, ?, 'test-model', 'TEST_KEY', ?)`,
				tt.name, tt.name, tt.typ, tt.settings)
			if err != nil {
				t.Fatalf("insert: %v", err)
			}

			reg := NewProviderRegistry(db, sec)
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
	reg := NewProviderRegistry(db, sec)

	if _, ok := reg.factories["anthropic_vertex"]; !ok {
		t.Fatal("anthropic_vertex factory not registered")
	}
}

func TestProviderRegistryChatMock(t *testing.T) {
	db := setupTestDB(t)
	sec := &memSecretsProvider{data: map[string]string{}}

	_, err := db.Exec(`INSERT INTO llm_providers (id, alias, type) VALUES ('p1', 'chat-mock', 'mock')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	reg := NewProviderRegistry(db, sec)
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
