package orchestrator

import "github.com/GoCodeAlone/workflow/secrets"

// Provider returns the underlying secrets.Provider.
//
// It triggers the lazy-resolve of the engine secrets.vault module's Provider on
// first call (post-Start) — the same pathway as Redact. This matters because
// ProviderRegistry resolves secrets on demand via this accessor (post-Start,
// e.g. inside agent_execute); the lazy-resolve must fire here too, not only on
// redaction, otherwise vault-backed AI providers would silently get an empty
// API key (the regression fixed alongside the PR4 lazy-resolve change).
func (sg *SecretGuard) Provider() secrets.Provider {
	sg.resolveOnce.Do(sg.resolve)
	sg.mu.RLock()
	defer sg.mu.RUnlock()
	return sg.provider
}

// AddKnownSecret adds a secret value to the guard's redaction list.
func (sg *SecretGuard) AddKnownSecret(name, value string) {
	if value == "" {
		return
	}
	sg.mu.Lock()
	defer sg.mu.Unlock()
	sg.knownValues[value] = name
}
