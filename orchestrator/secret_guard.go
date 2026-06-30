package orchestrator

import (
	"context"
	"strings"
	"sync"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/secrets"
)

// vaultProvider exposes the Provider() accessor the lazy-resolver type-asserts
// on. The engine module.SecretsVaultModule implements this shape; defining it as
// a local interface keeps the dependency one-directional (we assert against a
// structural interface rather than importing the concrete module type, which
// would create an import cycle since workflow/module is a heavy dep).
type vaultProvider interface {
	Provider() secrets.Provider
}

// SecretGuard scans text for known secret values and redacts them.
type SecretGuard struct {
	mu          sync.RWMutex
	knownValues map[string]string // value → name (reversed for fast lookup)
	provider    secrets.Provider
	backendName string

	// Lazy-resolve fields (D19). Wiring hooks run in BuildFromConfig BEFORE
	// module Start(), so the engine secrets.vault module's Provider() is nil at
	// hook time. The guard therefore lazily resolves it on the first redaction
	// call (which executes post-Start) via sync.Once.
	app            modular.Application
	vaultModuleKey string
	resolveOnce    sync.Once
}

func NewSecretGuard(p secrets.Provider, backend string) *SecretGuard {
	return &SecretGuard{
		knownValues: make(map[string]string),
		provider:    p,
		backendName: backend,
	}
}

// AttachLazyVault arms the guard to lazily resolve the engine secrets.vault
// module's Provider from the service registry on the first redaction call.
// This is the D19 fix for the lifecycle inversion: wiring hooks run pre-Start
// (the module's Provider() is nil at hook time), so resolution is deferred to
// the first post-Start redaction. vaultModuleKey is the module's config name
// (the host declares it; default "vault"). Safe to call with a nil app or empty
// key — resolve() becomes a no-op and redaction falls back to AddKnownSecret
// values (the env-var redaction path).
func (sg *SecretGuard) AttachLazyVault(app modular.Application, vaultModuleKey string) {
	sg.mu.Lock()
	defer sg.mu.Unlock()
	sg.app = app
	sg.vaultModuleKey = vaultModuleKey
}

// resolve looks up the engine secrets.vault module in the service registry and,
// if present and provider-shaped, hot-swaps it in via SetProvider (which
// pre-loads knownValues + atomic-swaps). It is invoked exactly once by
// resolveOnce.Do from Redact/CheckAndRedact. Failures (nil app, missing key,
// wrong shape) are silent no-ops — env-var-added values remain redacted.
func (sg *SecretGuard) resolve() {
	// Read app + key without holding the lock during the registry lookup +
	// SetProvider (SetProvider takes its own write lock). sync.Once serializes
	// concurrent first-callers so there is no read-during-populate race.
	sg.mu.RLock()
	app := sg.app
	key := sg.vaultModuleKey
	sg.mu.RUnlock()

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
	if p := vp.Provider(); p != nil {
		sg.SetProvider(p, "vault")
	}
}

// SetProvider hot-swaps the secrets provider and reloads all secrets.
// Secrets are loaded from the new provider before swapping to avoid
// a window where no secrets are available for redaction.
func (sg *SecretGuard) SetProvider(p secrets.Provider, backend string) {
	// Pre-load secrets from the new provider outside the lock
	newValues := make(map[string]string)
	if p != nil {
		ctx := context.Background()
		if names, err := p.List(ctx); err == nil {
			for _, name := range names {
				if val, err := p.Get(ctx, name); err == nil && val != "" {
					newValues[val] = name
				}
			}
		}
	}

	// Atomic swap under write lock
	sg.mu.Lock()
	sg.provider = p
	sg.backendName = backend
	sg.knownValues = newValues
	sg.mu.Unlock()
}

// BackendName returns the name of the current backend.
func (sg *SecretGuard) BackendName() string {
	sg.mu.RLock()
	defer sg.mu.RUnlock()
	return sg.backendName
}

// LoadSecrets loads secret values from the provider for the given keys.
func (sg *SecretGuard) LoadSecrets(ctx context.Context, names []string) error {
	sg.mu.Lock()
	defer sg.mu.Unlock()
	for _, name := range names {
		val, err := sg.provider.Get(ctx, name)
		if err != nil {
			continue // skip secrets that don't exist
		}
		if val != "" {
			sg.knownValues[val] = name
		}
	}
	return nil
}

// LoadAllSecrets loads all available secrets from the provider.
func (sg *SecretGuard) LoadAllSecrets(ctx context.Context) error {
	if sg.provider == nil {
		return nil
	}
	names, err := sg.provider.List(ctx)
	if err != nil {
		return err
	}
	return sg.LoadSecrets(ctx, names)
}

// Redact replaces known secret values with [REDACTED:name].
func (sg *SecretGuard) Redact(text string) string {
	// Lazy-resolve the engine secrets.vault module's Provider on the first call
	// (post-Start). sync.Once.Do serializes concurrent first-callers; SetProvider
	// takes its own write lock (released before Once.Do returns), so subsequent
	// callers safely take the RLock below.
	sg.resolveOnce.Do(sg.resolve)

	sg.mu.RLock()
	defer sg.mu.RUnlock()
	for val, name := range sg.knownValues {
		if strings.Contains(text, val) {
			text = strings.ReplaceAll(text, val, "[REDACTED:"+name+"]")
		}
	}
	return text
}

// CheckAndRedact redacts secret values in a message. Returns true if redaction occurred.
func (sg *SecretGuard) CheckAndRedact(msg *provider.Message) bool {
	original := msg.Content
	msg.Content = sg.Redact(msg.Content)
	return msg.Content != original
}
