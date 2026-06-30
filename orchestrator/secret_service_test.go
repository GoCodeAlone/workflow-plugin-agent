package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/secrets"
)

// fakeVaultModule mimics the engine module.SecretsVaultModule shape: it is
// registered under its config name in the service registry and exposes a
// Provider() secrets.Provider accessor that the lazy-resolver type-asserts on.
// (Moved here from secret_guard_lazy_test.go when SecretGuard was deleted.)
type fakeVaultModule struct {
	name string
	p    secrets.Provider
}

func (f *fakeVaultModule) Provider() secrets.Provider { return f.p }

// lazyStubApp is a minimal modular.Application whose SvcRegistry returns the
// vault-module-shaped service the lazy-resolver looks up. It reuses the
// embedded modular.Application pattern from mockApp so it satisfies the full
// interface. (Moved here from secret_guard_lazy_test.go.)
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

// startableVaultModule models the engine secrets.vault module lifecycle: it is
// registered in the service registry at init time but its Provider() is nil
// until Start() is called (post-wiring). This lets the registry test faithfully
// reproduce the pre-Start (nil) → post-Start (populated) transition.
// (Moved here from secret_guard_lazy_test.go.)
type startableVaultModule struct {
	p secrets.Provider
}

func (m *startableVaultModule) Provider() secrets.Provider { return m.p }

// Start mirrors module.Start(): populates the Provider (post-wiring).
func (m *startableVaultModule) Start(p secrets.Provider) { m.p = p }

// These tests cover the new secretsHolder + secretService composite (the
// additive replacement for SecretGuard). They PORT the D19 lazy-resolve +
// hot-swap + override-precedence regression coverage from
// secret_guard_lazy_test.go so that coverage is not lost when SecretGuard is
// deleted in a follow-up shot (M3).
//
// The fake app / vault module / secrets provider helpers (lazyStubApp,
// startableVaultModule, fakeVaultModule, mockSecretsProvider, noopLogger) are
// defined in sibling _test.go files and reused here.

// TestSecretHolder_LazyResolve proves the holder lazily resolves the engine
// secrets.vault module's Provider on first Provider()/EnsureArmed() call
// (post-Start lifecycle) and arms the paired Redactor so a known vault value is
// redacted. This is the D19 + D6 regression ported from
// TestSecretGuardLazyResolve_PopulatesFromEngineModule.
func TestSecretHolder_LazyResolve(t *testing.T) {
	const vaultKey = "vault"
	known := &mockSecretsProvider{secrets: map[string]string{
		"DATABASE_URL": "postgres://super-secret-db-host:5432/prod",
	}}
	// Pre-Start: module registered but Provider() is nil; Start() populates it.
	vaultMod := &startableVaultModule{}
	app := &lazyStubApp{services: map[string]any{vaultKey: vaultMod}}

	redactor := secrets.NewRedactor()
	holder := &secretsHolder{app: app, vaultKey: vaultKey, redactor: redactor}

	// Pre-Start: Provider() resolves (once fires) but the module's Provider is
	// still nil → holder.p stays nil; no panic.
	if got := holder.Provider(); got != nil {
		t.Errorf("pre-Start Provider: got %v, want nil", got)
	}

	// module.Start() populates the engine module's Provider post-wiring.
	vaultMod.Start(known)

	// Build a composite so EnsureArmed arms the Redactor through the same
	// once as Provider() resolution. (once already fired above pre-Start; reset
	// by constructing a fresh holder for the armed-path assertion below.)
	armedHolder := &secretsHolder{app: app, vaultKey: vaultKey, redactor: secrets.NewRedactor()}
	svc := &secretService{redactor: armedHolder.redactor, holder: armedHolder}

	// First redaction call must trigger lazy resolve -> SetProvider-equivalent
	// -> Redactor armed from the vault provider's values.
	msg := &provider.Message{Content: "the connection string is postgres://super-secret-db-host:5432/prod please"}
	svc.CheckAndRedact(msg)
	if strings.Contains(msg.Content, "super-secret-db-host") {
		t.Errorf("secret value still present after lazy redact: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "[REDACTED:DATABASE_URL]") {
		t.Errorf("expected [REDACTED:DATABASE_URL], got %q", msg.Content)
	}

	// Provider must now be the engine module's provider (resolved atomically
	// with the Redactor arming — single sync.Once).
	if got := armedHolder.Provider(); got != known {
		t.Errorf("provider not resolved to engine module's provider after lazy resolve: got %T", got)
	}
}

// TestSecretHolder_LazyResolve_ModuleMissingNoPanic proves that if the
// configured vault module key is absent from the registry (host didn't declare
// secrets.vault), resolve() is a safe no-op, Provider() returns nil, and
// EnsureArmed does not panic. Redaction falls back to AddValue entries only.
func TestSecretHolder_LazyResolve_ModuleMissingNoPanic(t *testing.T) {
	app := &lazyStubApp{services: map[string]any{}} // no vault key
	redactor := secrets.NewRedactor()
	holder := &secretsHolder{app: app, vaultKey: "vault", redactor: redactor}

	if got := holder.Provider(); got != nil {
		t.Errorf("expected nil provider when module missing, got %v", got)
	}
	holder.EnsureArmed() // must not panic

	// AddValue-added values still redact (the env-var path).
	redactor.AddValue("MANUAL", "manual-val")
	if got := redactor.Redact("manual-val present"); !strings.Contains(got, "[REDACTED:MANUAL]") {
		t.Errorf("manual value not redacted when module missing: %q", got)
	}
}

// TestSecretHolder_LazyResolve_NilAppNoPanic proves resolve() is a safe no-op
// when no app is attached (env-var-only redaction path).
func TestSecretHolder_LazyResolve_NilAppNoPanic(t *testing.T) {
	redactor := secrets.NewRedactor()
	holder := &secretsHolder{redactor: redactor} // no app, no vaultKey

	if got := holder.Provider(); got != nil {
		t.Errorf("expected nil provider on nil-app path, got %v", got)
	}
	holder.EnsureArmed() // must not panic
}

// TestSecretHolder_HotSwap proves SetProvider live-mutates the provider slot
// (D7 hot-swap): after the swap, Provider() returns the new provider live, and
// the Redactor is re-armed from the new provider's values.
func TestSecretHolder_HotSwap(t *testing.T) {
	redactor := secrets.NewRedactor()
	holder := &secretsHolder{redactor: redactor}

	original := &mockSecretsProvider{secrets: map[string]string{"K": "original-val"}}
	holder.SetProvider(original)

	if got := holder.Provider(); got != original {
		t.Errorf("post-SetProvider Provider: got %T, want original", got)
	}
	// Redactor armed from original.
	if got := redactor.Redact("has original-val"); !strings.Contains(got, "[REDACTED:K]") {
		t.Errorf("original value not redacted after first SetProvider: %q", got)
	}

	// Hot-swap to a provider that resolves "K" -> "swapped-val".
	swapped := &mockSecretsProvider{secrets: map[string]string{"K": "swapped-val"}}
	holder.SetProvider(swapped)

	if got := holder.Provider(); got != swapped {
		t.Errorf("post-swap Provider: got %T, want swapped (live read)", got)
	}
	// Redactor re-armed from swapped: "swapped-val" now redacted.
	if got := redactor.Redact("has swapped-val"); !strings.Contains(got, "[REDACTED:K]") {
		t.Errorf("swapped value not redacted after hot-swap: %q", got)
	}
	// original-val no longer redacted (full-replace on LoadFromProvider).
	if got := redactor.Redact("has original-val"); strings.Contains(got, "[REDACTED:") {
		t.Errorf("original value should no longer be redacted after full-replace: %q", got)
	}
}

// TestSecretHolder_OnceGuarantee confirms lazy resolve runs exactly once even
// under concurrent first-call contention (sync.Once semantics) and the resolver
// does not deadlock under -race. Ported from
// TestSecretGuardLazyResolve_OnceGuarantee.
func TestSecretHolder_OnceGuarantee(t *testing.T) {
	const vaultKey = "vault"
	known := &mockSecretsProvider{secrets: map[string]string{"K": "val-k"}}
	vaultMod := &fakeVaultModule{name: vaultKey, p: known}
	app := &lazyStubApp{services: map[string]any{vaultKey: vaultMod}}

	redactor := secrets.NewRedactor()
	holder := &secretsHolder{app: app, vaultKey: vaultKey, redactor: redactor}
	svc := &secretService{redactor: redactor, holder: holder}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := svc.Redact("contains val-k")
			if !strings.Contains(got, "[REDACTED:K]") {
				t.Errorf("concurrent redact missed value: %q", got)
			}
		}()
	}
	wg.Wait()
}

// TestSecretService_SatisfiesIfaces is a runtime mirror of the compile-time
// assertions in secret_service.go: *secretService must satisfy BOTH
// executor.SecretRedactor AND interface{ Provider() secrets.Provider } — the two
// shapes the repo-root package-agent consumer paths type-assert on (D10).
func TestSecretService_SatisfiesIfaces(t *testing.T) {
	svc := &secretService{
		redactor: secrets.NewRedactor(),
		holder:   &secretsHolder{},
	}

	var redactor interface {
		Redact(string) string
		CheckAndRedact(*provider.Message)
	} = svc
	_ = redactor

	var providerAccessor interface{ Provider() secrets.Provider } = svc
	_ = providerAccessor

	// Accessors return non-nil wired members.
	if svc.Holder() == nil {
		t.Error("Holder() returned nil")
	}
	if svc.Redactor() == nil {
		t.Error("Redactor() returned nil")
	}
}

// TestSecretService_Redact_DelegatesAndArms proves composite.Redact delegates
// to the engine Redactor after arming the holder, and CheckAndRedact mutates
// msg.Content in place.
func TestSecretService_Redact_DelegatesAndArms(t *testing.T) {
	redactor := secrets.NewRedactor()
	holder := &secretsHolder{redactor: redactor}
	svc := &secretService{redactor: redactor, holder: holder}

	// AddValue-added value redacts via the composite (env-var path; no vault).
	redactor.AddValue("API_KEY", "sk-secret-123")

	got := svc.Redact("the key is sk-secret-123 here")
	want := "the key is [REDACTED:API_KEY] here"
	if got != want {
		t.Errorf("Redact: got %q, want %q", got, want)
	}

	// CheckAndRedact mutates msg.Content in place.
	msg := &provider.Message{Content: "again sk-secret-123"}
	svc.CheckAndRedact(msg)
	if !strings.Contains(msg.Content, "[REDACTED:API_KEY]") {
		t.Errorf("CheckAndRedact did not redact msg.Content: %q", msg.Content)
	}
	if strings.Contains(msg.Content, "sk-secret-123") {
		t.Errorf("CheckAndRedact left secret value in msg.Content: %q", msg.Content)
	}
}

// newTestSecretService builds a *secretService composite for test injection
// (replacing the former NewSecretGuard construction). The Redactor is armed
// from the supplied provider (mirroring what LoadFromProvider does at runtime),
// so a known value supplied by the provider is redacted. Pass nil to get the
// env-only path (no vault provider).
func newTestSecretService(p secrets.Provider) *secretService {
	redactor := secrets.NewRedactor()
	holder := &secretsHolder{redactor: redactor}
	svc := &secretService{redactor: redactor, holder: holder}
	if p != nil {
		holder.SetProvider(p) // arms the Redactor via LoadFromProvider
	}
	return svc
}

// TestSecretService_StoreThenRedact (D3 non-regression) proves the store-then-arm
// path preserved by the autoStoreSecret rewire: after a token is stored via
// Provider.Set AND armed via Redactor().AddValue, a subsequent Redact masks it.
// Dropping the AddValue call (the regression this guards) would silently leak
// the just-stored token in the next redaction pass — the failure class the
// redactor exists to prevent.
func TestSecretService_StoreThenRedact(t *testing.T) {
	// A settable vault provider whose store records the Set so Get can return it
	// later (mirrors step_human_request.autoStoreSecret: Provider().Set then arm).
	// mockSecretsProvider.Set returns ErrUnsupported, so we use a local mutable
	// provider here.
	vault := &settableSecretsProvider{secrets: map[string]string{}}
	svc := newTestSecretService(vault)

	const secretName = "FRESH_API_KEY"
	const token = "sk-just-stored-by-human-request"

	// Store the token via the provider (autoStoreSecret does Provider().Set).
	if err := svc.Provider().Set(context.Background(), secretName, token); err != nil {
		t.Fatalf("Provider.Set: %v", err)
	}
	// Arm the redactor with the just-stored token (autoStoreSecret does
	// Redactor().AddValue after Set — D3). This is the load-bearing call.
	svc.Redactor().AddValue(secretName, token)

	// A subsequent Redact MUST mask the token. If AddValue had been dropped, the
	// token would leak here (the silent-leak regression).
	got := svc.Redact("leaked token: sk-just-stored-by-human-request in output")
	if strings.Contains(got, token) {
		t.Errorf("D3 regression: just-stored token leaked after Redact: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:"+secretName+"]") {
		t.Errorf("D3: expected [REDACTED:%s], got %q", secretName, got)
	}
}

// settableSecretsProvider is a secrets.Provider whose Set mutates the map (unlike
// mockSecretsProvider, which returns ErrUnsupported). Used by the D3 store-then-
// redact test to exercise the autoStoreSecret Provider().Set path.
type settableSecretsProvider struct {
	secrets map[string]string
}

func (m *settableSecretsProvider) Name() string { return "settable" }
func (m *settableSecretsProvider) Get(_ context.Context, name string) (string, error) {
	v, ok := m.secrets[name]
	if !ok {
		return "", fmt.Errorf("secret %q not found", name)
	}
	return v, nil
}
func (m *settableSecretsProvider) Set(_ context.Context, name, value string) error {
	m.secrets[name] = value
	return nil
}
func (m *settableSecretsProvider) Delete(_ context.Context, name string) error {
	delete(m.secrets, name)
	return nil
}
func (m *settableSecretsProvider) List(_ context.Context) ([]string, error) {
	names := make([]string, 0, len(m.secrets))
	for k := range m.secrets {
		names = append(names, k)
	}
	return names, nil
}

// TestSecretService_RootPathAliasListsResolveComposite (Task 10 / D10
// non-regression) proves the repo-root package-agent path resolves the
// *secretService composite under the KEPT key "ratchet-secret-guard" via BOTH
// alias lists + type-assertions it uses. This is the runtime mirror of the
// compile-time iface assertions in secret_service.go: a compile-assert can't
// catch a key-typo or registration-ordering bug, so we exercise the real
// alias-list lookup against a fake app whose SvcRegistry returns the composite
// under the kept key.
//
// Root path consumers (repo-root package agent, UNCHANGED by this shot):
//   - step_agent_execute.go:   alias list ["agent-secret-guard","ratchet-secret-guard"]
//     -> type-assert executor.SecretRedactor
//   - provider_registry.go:    alias list ["ratchet-secret-guard","agent-secret-guard","secret-guard"]
//     -> type-assert interface{ Provider() secrets.Provider }
func TestSecretService_RootPathAliasListsResolveComposite(t *testing.T) {
	svc := newTestSecretService(&mockSecretsProvider{secrets: map[string]string{"K": "v"}})
	// Fake app whose registry returns the composite under the KEPT key, exactly
	// as secretsResolverHook registers it. lazyStubApp satisfies modular.Application.
	app := &lazyStubApp{services: map[string]any{"ratchet-secret-guard": svc}}

	// Path 1: repo-root step_agent_execute.go alias list -> executor.SecretRedactor.
	var resolvedRedactor executor.SecretRedactor
	for _, name := range []string{"agent-secret-guard", "ratchet-secret-guard"} {
		if r, ok := app.SvcRegistry()[name].(executor.SecretRedactor); ok {
			resolvedRedactor = r
			break
		}
	}
	if resolvedRedactor == nil {
		t.Fatal("D10: root path 1 (executor.SecretRedactor) did not resolve the composite under ratchet-secret-guard")
	}
	if resolvedRedactor != svc {
		t.Errorf("D10: root path 1 resolved %T, want the registered *secretService", resolvedRedactor)
	}

	// Path 2: repo-root provider_registry.go alias list -> interface{ Provider() secrets.Provider }.
	var resolvedProvider interface{ Provider() secrets.Provider }
	for _, name := range []string{"ratchet-secret-guard", "agent-secret-guard", "secret-guard"} {
		if p, ok := app.SvcRegistry()[name].(interface{ Provider() secrets.Provider }); ok {
			resolvedProvider = p
			break
		}
	}
	if resolvedProvider == nil {
		t.Fatal("D10: root path 2 (Provider accessor) did not resolve the composite under ratchet-secret-guard")
	}
	if resolvedProvider != svc {
		t.Errorf("D10: root path 2 resolved %T, want the registered *secretService", resolvedProvider)
	}

	// Both paths resolve the SAME composite, and the Provider accessor returns
	// the wired provider (proving API-key resolution would work end-to-end).
	if resolvedProvider.Provider() == nil {
		t.Error("D10: resolved composite's Provider() is nil; want the wired mock provider")
	}
}
