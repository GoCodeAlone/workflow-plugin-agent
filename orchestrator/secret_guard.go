package orchestrator

import (
	"context"
	"strings"
	"sync"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/secrets"
)

// SecretGuard scans text for known secret values and redacts them.
type SecretGuard struct {
	mu          sync.RWMutex
	knownValues map[string]string // value → name (reversed for fast lookup)
	provider    secrets.Provider
	backendName string
}

func NewSecretGuard(p secrets.Provider, backend string) *SecretGuard {
	return &SecretGuard{
		knownValues: make(map[string]string),
		provider:    p,
		backendName: backend,
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
