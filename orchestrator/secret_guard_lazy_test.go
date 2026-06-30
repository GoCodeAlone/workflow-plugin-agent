package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/secrets"
)

// fakeVaultModule mimics the engine module.SecretsVaultModule shape: it is
// registered under its config name in the service registry and exposes a
// Provider() secrets.Provider accessor that the lazy-resolver type-asserts on.
type fakeVaultModule struct {
	name string
	p    secrets.Provider
}

func (f *fakeVaultModule) Provider() secrets.Provider { return f.p }

// lazyStubApp is a minimal modular.Application whose SvcRegistry returns the
// vault-module-shaped service the lazy-resolver looks up. It reuses the
// embedded modular.Application pattern from mockApp so it satisfies the full
// interface.
type lazyStubApp struct {
	modular.Application
	services map[string]any
}

func (a *lazyStubApp) SvcRegistry() modular.ServiceRegistry {
	return modular.ServiceRegistry(a.services)
}

func (a *lazyStubApp) RegisterService(name string, svc any) error {
	a.services[name] = svc
	return nil
}

func (a *lazyStubApp) Logger() modular.Logger { return &noopLogger{} }

// TestSecretGuardLazyResolve_PopulatesFromEngineModule proves the guard
// lazily resolves the engine secrets.vault module's Provider on first redaction
// call (post-Start lifecycle) and pre-loads knownValues from it.
func TestSecretGuardLazyResolve_PopulatesFromEngineModule(t *testing.T) {
	const vaultKey = "vault"
	known := &mockSecretsProvider{secrets: map[string]string{
		"DATABASE_URL": "postgres://super-secret-db-host:5432/prod",
	}}
	// Pre-Start: module registered but Provider() is nil; Start() populates it.
	vaultMod := &startableVaultModule{}

	app := &lazyStubApp{services: map[string]any{vaultKey: vaultMod}}

	// Construct the guard WITHOUT a provider (pre-Start hook wiring) and arm it
	// for lazy resolution against the engine module.
	sg := NewSecretGuard(nil, "")
	sg.AttachLazyVault(app, vaultKey)

	// module.Start() populates the engine module's Provider post-wiring.
	vaultMod.Start(known)

	// First redaction call must trigger lazy resolve -> SetProvider -> knownValues populated.
	msg := &provider.Message{Content: "the connection string is postgres://super-secret-db-host:5432/prod please"}
	changed := sg.CheckAndRedact(msg)
	if !changed {
		t.Fatal("expected CheckAndRedact to redact after lazy resolve, got no change")
	}
	if strings.Contains(msg.Content, "super-secret-db-host") {
		t.Errorf("secret value still present after lazy redact: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "[REDACTED:DATABASE_URL]") {
		t.Errorf("expected [REDACTED:DATABASE_URL], got %q", msg.Content)
	}

	// Provider must now be the engine module's provider, backend labeled "vault".
	if sg.Provider() != known {
		t.Errorf("provider not resolved to engine module's provider after lazy resolve")
	}
	if got := sg.BackendName(); got != "vault" {
		t.Errorf("backend name: got %q, want %q", got, "vault")
	}
}

// TestSecretGuardLazyResolve_NilAppNoPanic proves resolve() is a safe no-op when
// no app/module is attached (env-var-only redaction path), and redaction is a
// pass-through rather than a panic.
func TestSecretGuardLazyResolve_NilAppNoPanic(t *testing.T) {
	sg := NewSecretGuard(nil, "")
	// No AttachLazyVault call -> app == nil, vaultModuleKey == "".
	// Manually pre-seed one known value (env-var path) to confirm Redact still
	// works against values added via AddKnownSecret even when lazy resolve no-ops.
	sg.AddKnownSecret("ENV_TOKEN", "env-secret-value")

	text := "contains env-secret-value and an unknown value"
	got := sg.Redact(text)
	if !strings.Contains(got, "[REDACTED:ENV_TOKEN]") {
		t.Errorf("AddKnownSecret value not redacted on nil-app path: %q", got)
	}
	if strings.Contains(got, "env-secret-value") {
		t.Errorf("env secret value leaked: %q", got)
	}
}

// TestSecretGuardLazyResolve_NilAppEmptyKnownValuesPassThrough confirms that
// when app is nil AND knownValues is empty, Redact is a clean pass-through
// (no panic, no mutation).
func TestSecretGuardLazyResolve_NilAppEmptyKnownValuesPassThrough(t *testing.T) {
	sg := NewSecretGuard(nil, "")
	text := "nothing to redact"
	got := sg.Redact(text)
	if got != text {
		t.Errorf("expected unchanged pass-through, got %q", got)
	}
}

// TestSecretGuardLazyResolve_OnceGuarantee confirms lazy resolve runs exactly
// once even under concurrent first-call contention (sync.Once semantics), and
// the resolver does not deadlock under -race.
func TestSecretGuardLazyResolve_OnceGuarantee(t *testing.T) {
	const vaultKey = "vault"
	known := &mockSecretsProvider{secrets: map[string]string{"K": "val-k"}}
	vaultMod := &fakeVaultModule{name: vaultKey, p: known}
	app := &lazyStubApp{services: map[string]any{vaultKey: vaultMod}}

	sg := NewSecretGuard(nil, "")
	sg.AttachLazyVault(app, vaultKey)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := sg.Redact("contains val-k")
			if !strings.Contains(got, "[REDACTED:K]") {
				t.Errorf("concurrent redact missed value: %q", got)
			}
		}()
	}
	wg.Wait()
}

// TestSecretGuardLazyResolve_ModuleMissingNoPanic proves that if the configured
// vault module key is absent from the registry (host didn't declare secrets.vault),
// resolve() is a safe no-op and redaction falls back to AddKnownSecret values.
func TestSecretGuardLazyResolve_ModuleMissingNoPanic(t *testing.T) {
	app := &lazyStubApp{services: map[string]any{}} // no vault key
	sg := NewSecretGuard(nil, "")
	sg.AttachLazyVault(app, "vault")
	sg.AddKnownSecret("MANUAL", "manual-val")

	got := sg.Redact("manual-val present")
	if !strings.Contains(got, "[REDACTED:MANUAL]") {
		t.Errorf("manual value not redacted when module missing: %q", got)
	}
}

// TestSecretGuardLazyResolve_ModuleNotProviderShape proves a service registered
// under the vault key that does NOT expose Provider() is safely skipped (no panic).
func TestSecretGuardLazyResolve_ModuleNotProviderShape(t *testing.T) {
	app := &lazyStubApp{services: map[string]any{"vault": "not-a-vault-module"}}
	sg := NewSecretGuard(nil, "")
	sg.AttachLazyVault(app, "vault")

	// Must not panic; redaction is a no-op pass-through.
	text := "leaked-value"
	if got := sg.Redact(text); got != text {
		t.Errorf("expected pass-through for non-provider module shape, got %q", got)
	}
}

// startableVaultModule models the engine secrets.vault module lifecycle: it is
// registered in the service registry at init time but its Provider() is nil
// until Start() is called (post-wiring). This lets the registry test faithfully
// reproduce the pre-Start (nil) → post-Start (populated) transition.
type startableVaultModule struct {
	p secrets.Provider
}

func (m *startableVaultModule) Provider() secrets.Provider { return m.p }

// Start mirrors module.Start(): populates the Provider (post-wiring).
func (m *startableVaultModule) Start(p secrets.Provider) { m.p = p }

// TestProviderRegistryResolvesSecretViaLazyGuard proves the ProviderRegistry
// resolves a vault-backed secret via the LAZY SecretGuard accessor (the method
// value guard.Provider), NOT a nil wiring-time snapshot.
//
// This is the regression test for the PR4 lazy-resolve change: providerRegistryHook
// runs at wiring time (pre-Start), where the engine secrets.vault module's
// Provider() is nil. The registry must defer resolution to provider-resolution
// time (post-Start) so vault-backed AI providers (those with SecretName) get
// their real API key instead of an empty one.
func TestProviderRegistryResolvesSecretViaLazyGuard(t *testing.T) {
	const vaultKey = "vault"
	known := &mockSecretsProvider{secrets: map[string]string{
		"ANTHROPIC_API_KEY": "sk-vault-resolved-key",
	}}
	// Pre-Start: module registered, Provider() nil.
	vaultMod := &startableVaultModule{}
	app := &lazyStubApp{services: map[string]any{vaultKey: vaultMod}}

	// === WIRING TIME (pre-Start) ===
	// Construct the guard exactly as secretsGuardHook does: no pre-set provider,
	// armed for lazy resolution against the engine module.
	guard := NewSecretGuard(nil, "")
	guard.AttachLazyVault(app, vaultKey)

	// The registry is built with the guard.Provider METHOD VALUE (lazy accessor),
	// exactly as providerRegistryHook now wires it. It must NOT snapshot nil here.
	reg := NewProviderRegistry(setupTestDB(t), guard.Provider)

	_, err := reg.db.Exec(`INSERT INTO llm_providers (id, alias, type, model, secret_name)
		VALUES ('p1', 'vaulted-claude', 'anthropic', 'claude-sonnet-4-20250514', 'ANTHROPIC_API_KEY')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// === POST-START ===
	// module.Start() runs after wiring hooks and populates the module's Provider.
	vaultMod.Start(known)

	// === PROVIDER-RESOLUTION TIME (post-Start, e.g. agent_execute) ===
	// Resolve the provider. The registry invokes the lazy accessor, which triggers
	// the guard's lazy-resolve of the now-populated engine module Provider. If the
	// registry had snapshotted nil at wiring time, the API key would be empty and
	// the anthropic factory would reject it ("APIKey is required").
	p, err := reg.GetByAlias(context.Background(), "vaulted-claude")
	if err != nil {
		t.Fatalf("GetByAlias via lazy guard: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider resolved via lazy guard")
	}
	if p.Name() != "anthropic" {
		t.Errorf("expected anthropic provider, got %q", p.Name())
	}

	// The guard must now have resolved the engine module's provider (lazy resolve
	// fired during the registry's resolution), proving the accessor pathway works
	// end-to-end rather than a nil snapshot.
	if guard.Provider() != known {
		t.Errorf("guard.Provider() not resolved to engine module provider after registry resolution: got %T", guard.Provider())
	}
}

// TestProviderRegistryNilAccessorNoPanic proves the wiring-time nil-accessor path
// (no SecretGuard registered, or guard absent) degrades gracefully: the registry
// builds without panic and a SecretName provider resolves with an empty API key
// (existing behavior — no secret resolution, but no panic either).
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
// over the lazy guard accessor: after the swap, resolution uses the new provider
// and the lazy accessor is no longer consulted.
func TestProviderRegistryUpdateSecretsProviderOverridesLazy(t *testing.T) {
	const vaultKey = "vault"
	// Lazy-guard-backed provider would resolve "K" -> "lazy-val".
	lazyProvider := &mockSecretsProvider{secrets: map[string]string{"K": "lazy-val"}}
	vaultMod := &startableVaultModule{}
	vaultMod.Start(lazyProvider)
	app := &lazyStubApp{services: map[string]any{vaultKey: vaultMod}}

	guard := NewSecretGuard(nil, "")
	guard.AttachLazyVault(app, vaultKey)
	reg := NewProviderRegistry(setupTestDB(t), guard.Provider)

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
	// proving it took precedence over the lazy guard accessor.
	if resolvedKey != "hotswap-val" {
		t.Errorf("hot-swap did not take precedence: resolved key %q, want %q", resolvedKey, "hotswap-val")
	}
}
