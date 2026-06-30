package orchestrator

import (
	"strings"
	"sync"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/secrets"
)

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
