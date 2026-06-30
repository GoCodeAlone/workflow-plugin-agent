package orchestrator

import (
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
	vaultMod := &fakeVaultModule{name: vaultKey, p: known}

	app := &lazyStubApp{services: map[string]any{vaultKey: vaultMod}}

	// Construct the guard WITHOUT a provider (pre-Start hook wiring) and arm it
	// for lazy resolution against the engine module.
	sg := NewSecretGuard(nil, "")
	sg.AttachLazyVault(app, vaultKey)

	if sg.Provider() != nil {
		t.Fatalf("precondition: provider must be nil before lazy resolve, got %T", sg.Provider())
	}

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
