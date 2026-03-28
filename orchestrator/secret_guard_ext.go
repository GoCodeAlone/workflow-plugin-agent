package orchestrator

import "github.com/GoCodeAlone/workflow/secrets"

// Provider returns the underlying secrets.Provider.
func (sg *SecretGuard) Provider() secrets.Provider {
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
