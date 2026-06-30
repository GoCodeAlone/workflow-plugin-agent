package orchestrator

import (
	"context"
	"sync"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/secrets"
)

// vaultProvider is the structural interface the lazy-resolver type-asserts on
// when looking up the engine secrets.vault module in the service registry. It
// is declared in orchestrator/secret_guard.go (the legacy SecretGuard file) and
// reused here unchanged; it will collapse onto this file when SecretGuard is
// removed in a follow-up shot.

// secretsHolder is the live, hot-swappable slot for the engine secrets.vault
// module's Provider. It preserves the D19 lazy-resolve lifecycle (wiring hooks
// run pre-Start, so the module's Provider() is nil at hook time and is resolved
// on first redaction/accessor call post-Start) AND the hot-swap semantics of
// the former SecretGuard.Provider method value.
//
// A single sync.Once (once) makes resolve+arm ATOMIC (D6): there is no window
// where Provider() returns a resolved provider but the Redactor has not yet
// been armed with that provider's values. resolve() looks up the vault module
// in the service registry under vaultKey, type-asserts it to vaultProvider, and
// — if it yields a non-nil Provider — stores it (h.p) AND arms the Redactor via
// LoadFromProvider in the same once-gated pass.
//
// mu protects h.p only. holder.mu is independent of redactor.mu (the Redactor
// takes its own internal lock during LoadFromProvider/Redact), so resolve()
// calling LoadFromProvider while NOT holding holder.mu is deadlock-free.
//
// This holder holds NO redaction state itself: redaction (the known-value scan)
// is delegated to the engine *secrets.Redactor the holder is paired with (see
// secretService). The holder only owns provider lifecycle.
type secretsHolder struct {
	app      modular.Application
	vaultKey string
	redactor *secrets.Redactor

	mu   sync.RWMutex
	p    secrets.Provider
	once sync.Once
}

// Provider returns the lazily-resolved engine secrets.vault module's Provider.
//
// It is a METHOD VALUE (not a closure), so it can be passed to consumers that
// expect func() secrets.Provider (e.g. NewProviderRegistry) and reads h.p LIVE
// on every invocation — preserving the hot-swap semantics of
// SecretGuard.Provider (D7). Calling it before the vault module has started
// triggers the lazy resolve (a no-op if the module's Provider is still nil);
// subsequent calls return h.p directly once resolve has fired via once.Do.
//
// Safe to call with no app/key attached: resolve is a no-op and the returned
// Provider is nil (the env-var-only redaction path).
func (h *secretsHolder) Provider() secrets.Provider {
	h.once.Do(h.resolve)
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.p
}

// SetProvider hot-swaps the secrets provider and re-arms the paired Redactor
// from the new provider. It preserves the live-mutability that
// UpdateSecretsProvider/hot-swap callers depend on (D7). If the holder has no
// Redactor attached the swap only updates the provider slot.
func (h *secretsHolder) SetProvider(p secrets.Provider) {
	h.mu.Lock()
	h.p = p
	h.mu.Unlock()
	if h.redactor != nil {
		_ = h.redactor.LoadFromProvider(context.Background(), p)
	}
}

// EnsureArmed forces the lazy resolve to fire if it hasn't already. It is the
// D-ARM-ON-REDACT hook: the redaction path (secretService.Redact /
// CheckAndRedact) calls EnsureArmed before delegating to the Redactor so the
// Redactor is armed with the vault module's values on the FIRST redaction call
// post-Start — atomically with the provider resolution (single sync.Once).
//
// Idempotent: once.Do is a no-op after the first call.
func (h *secretsHolder) EnsureArmed() {
	h.once.Do(h.resolve)
}

// resolve is the single once-gated resolve+arm pass (D6). It looks up the vault
// module under h.vaultKey in the app's service registry, type-asserts it to
// vaultProvider, and — if it yields a non-nil Provider — stores it under h.p
// (write lock) AND arms the paired Redactor via LoadFromProvider.
//
// Failures (nil app, missing key, wrong shape, nil provider) are silent
// no-ops: env-var-added values (loaded by secretsResolverHook) remain redacted
// via the Redactor's additive AddValue set. LoadFromProvider is called OUTSIDE
// h.mu (the Redactor takes its own internal lock), avoiding any lock-ordering
// hazard.
func (h *secretsHolder) resolve() {
	// Read app + key without holding the lock during the registry lookup +
	// Redactor arming (LoadFromProvider takes the Redactor's own write lock).
	// sync.Once serializes concurrent first-callers so there is no
	// read-during-populate race on h.p.
	h.mu.RLock()
	app := h.app
	key := h.vaultKey
	h.mu.RUnlock()

	if app == nil || key == "" {
		return
	}
	svc, ok := app.SvcRegistry()[key]
	if !ok {
		return
	}
	vp, ok := svc.(vaultProvider)
	if !ok {
		return
	}
	p := vp.Provider()
	if p == nil {
		return
	}
	h.mu.Lock()
	h.p = p
	h.mu.Unlock()
	if h.redactor != nil {
		_ = h.redactor.LoadFromProvider(context.Background(), p)
	}
}

// secretService is the thin composite registered under the KEPT service key
// "ratchet-secret-guard". It replaces SecretGuard as the registered service but
// is NOT SecretGuard: it holds no redaction state and implements no redaction
// logic — it delegates redaction to the engine *secrets.Redactor and provider
// access to the *secretsHolder. Its job is to satisfy BOTH structural
// interfaces the two consumer paths type-assert on, so that the repo-root
// package-agent path (which resolves the service via alias lists and
// type-asserts to executor.SecretRedactor and interface{ Provider() secrets.Provider })
// keeps working unchanged (D-COMPOSITE-SERVICE + D-KEEP-KEY):
//
//   - executor.SecretRedactor -> Redact + CheckAndRedact(*provider.Message)
//   - interface{ Provider() secrets.Provider } -> Provider
//
// CheckAndRedact takes a *provider.Message (an AGENT-package type the engine
// secrets package cannot import); it lives HERE on the composite, not on the
// engine Redactor, keeping the engine string-based and dependency-free
// (D-CHECKANDREDACT-ADAPTER).
type secretService struct {
	redactor *secrets.Redactor
	holder   *secretsHolder
}

// Compile-time assertions that *secretService satisfies both interfaces the
// repo-root package-agent consumer paths type-assert on. These are
// load-bearing: the root step_agent_execute.go / provider_registry.go resolve
// the service under the kept key "ratchet-secret-guard" and type-assert to
// these exact shapes; a silent signature drift would break redaction + API-key
// resolution on the live root-agent path (D10).
var (
	_ executor.SecretRedactor                  = (*secretService)(nil)
	_ interface{ Provider() secrets.Provider } = (*secretService)(nil)
)

// Redact redacts known secret values from text. It delegates to the engine
// *secrets.Redactor after arming the holder (D-ARM-ON-REDACT: ensures the
// Redactor has been armed from the vault module on the first call post-Start).
func (s *secretService) Redact(text string) string {
	s.holder.EnsureArmed()
	return s.redactor.Redact(text)
}

// CheckAndRedact redacts secret values in a message in place. It mutates
// msg.Content with the redacted text. (executor.SecretRedactor returns no bool;
// the change is observable via the mutated Content field, matching the iface
// contract the root consumer type-asserts on.)
func (s *secretService) CheckAndRedact(msg *provider.Message) {
	s.holder.EnsureArmed()
	msg.Content = s.redactor.Redact(msg.Content)
}

// Provider returns the lazily-resolved secrets provider (delegates to the
// holder's method value). Satisfies interface{ Provider() secrets.Provider }
// for the root provider_registry.go API-key resolution path.
func (s *secretService) Provider() secrets.Provider {
	return s.holder.Provider()
}

// Holder returns the underlying secretsHolder. Consumers that need the
// method-value accessor form (func() secrets.Provider) — e.g.
// NewProviderRegistry(db, holder.Provider) — retrieve it via this accessor.
func (s *secretService) Holder() *secretsHolder { return s.holder }

// Redactor returns the underlying engine *secrets.Redactor. Consumers that arm
// the redactor additively after a Provider.Set (D3 store-then-arm) retrieve it
// via this accessor to call AddValue.
func (s *secretService) Redactor() *secrets.Redactor { return s.redactor }
