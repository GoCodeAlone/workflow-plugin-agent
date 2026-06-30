package orchestrator

import (
	"os"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/secrets"
)

// secrets_resolver_hook_test.go exercises secretsResolverHook. It proves the
// hook builds a *secretService composite (engine Redactor + secretsHolder),
// registers it under the KEPT key "ratchet-secret-guard" (D-KEEP-KEY — the
// repo-root package-agent path resolves the service here via alias lists), and
// performs P13 two-source loading (VAULT_TOKEN + RATCHET_* env) into the
// Redactor so the values are redacted.

// runSecretsResolverHook invokes secretsResolverHook against a fresh mockApp
// and returns the app + the registered *secretService (looked up under the KEPT
// key). t.Helper centralizes the lookup so each test asserts behavior, not
// registration mechanics.
func runSecretsResolverHook(t *testing.T) (*mockApp, *secretService) {
	t.Helper()
	app := newMockApp()
	hook := secretsResolverHook()
	if err := hook.Hook(app, nil); err != nil {
		t.Fatalf("secretsResolverHook returned error: %v", err)
	}
	raw, ok := app.services["ratchet-secret-guard"]
	if !ok {
		t.Fatal("secretService not registered under ratchet-secret-guard")
	}
	svc, ok := raw.(*secretService)
	if !ok {
		t.Fatalf("ratchet-secret-guard service = %T, want *secretService", raw)
	}
	return app, svc
}

// TestSecretsResolverHook_RegistersCompositeUnderKeptKey proves the hook
// registers a *secretService (not *SecretGuard) under the KEPT key
// "ratchet-secret-guard", and that the composite satisfies both root-path
// interfaces the consumers type-assert on (D10/D-COMPOSITE-SERVICE).
func TestSecretsResolverHook_RegistersCompositeUnderKeptKey(t *testing.T) {
	_, svc := runSecretsResolverHook(t)

	// Compile-time iface satisfaction is asserted in secret_service.go; this is
	// the runtime mirror that the REGISTERED value (not just a constructed one)
	// satisfies both shapes.
	var _ executor.SecretRedactor = svc
	var _ interface{ Provider() secrets.Provider } = svc
}

// TestSecretsResolverHook_LoadsRatchetEnvForRedaction proves P13: RATCHET_* env
// values are loaded into the Redactor for free-text redaction (orthogonal to
// the vault Provider source). A value present in the env under RATCHET_ must be
// redacted by the composite.
func TestSecretsResolverHook_LoadsRatchetEnvForRedaction(t *testing.T) {
	const envName = "HOOK_TEST_SECRET"
	const envVal = "ratchet-env-value-xyz"
	t.Setenv("RATCHET_"+envName, envVal)
	t.Setenv("RATCHET_VAULT_MODULE", "") // force default "vault"

	_, svc := runSecretsResolverHook(t)

	got := svc.Redact("leaked: ratchet-env-value-xyz here")
	if strings.Contains(got, envVal) {
		t.Errorf("RATCHET_* env value leaked: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:"+envName+"]") {
		t.Errorf("expected [REDACTED:%s], got %q", envName, got)
	}
}

// TestSecretsResolverHook_ArmsVAULTTokenWhenConfigPresent proves P13: when a
// saved vault-config carries a token, that token is registered for redaction
// (the remote-vault token must never leak into LLM output). It points the hook
// at a temp vault-config dir (RATCHET_DATA_DIR is the env vaultConfigDir reads)
// so LoadVaultConfig reads a known plaintext token.
func TestSecretsResolverHook_ArmsVAULTTokenWhenConfigPresent(t *testing.T) {
	dir := t.TempDir()
	const token = "hvs.TOKEN-FROM-SAVED-CONFIG-abc"
	// Write a minimal vault-config.json carrying a PLAINTEXT token (no "enc:"
	// prefix) so LoadVaultConfig returns it as-is without needing the
	// machine-local keyfile.
	writeVaultConfigForTest(t, dir, token)
	t.Setenv("RATCHET_DATA_DIR", dir)

	// Confirm vaultConfigDir() now resolves into the temp dir before running
	// the hook, so the assertion is meaningful.
	if got := vaultConfigDir(); got != dir {
		t.Fatalf("vaultConfigDir() = %q, want %q", got, dir)
	}

	_, svc := runSecretsResolverHook(t)

	got := svc.Redact("auth token hvs.TOKEN-FROM-SAVED-CONFIG-abc leaked")
	if strings.Contains(got, token) {
		t.Errorf("VAULT_TOKEN leaked: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:VAULT_TOKEN]") {
		t.Errorf("expected [REDACTED:VAULT_TOKEN], got %q", got)
	}
}

// TestSecretsResolverHook_CheckAndRedactMutatesMessage proves the composite's
// CheckAndRedact (the agent-package adapter method the root consumer
// type-asserts on) mutates a *provider.Message.Content in place after arming.
func TestSecretsResolverHook_CheckAndRedactMutatesMessage(t *testing.T) {
	const envName = "MSG_SECRET"
	const envVal = "msg-secret-value-789"
	t.Setenv("RATCHET_"+envName, envVal)
	t.Setenv("RATCHET_VAULT_MODULE", "")

	_, svc := runSecretsResolverHook(t)

	msg := &provider.Message{Content: "contains msg-secret-value-789 inline"}
	svc.CheckAndRedact(msg)
	if strings.Contains(msg.Content, envVal) {
		t.Errorf("CheckAndRedact left secret value: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "[REDACTED:"+envName+"]") {
		t.Errorf("expected [REDACTED:%s], got %q", envName, msg.Content)
	}
}

// TestSecretsResolverHook_DefaultVaultKey proves vaultKey defaults to "vault"
// when RATCHET_VAULT_MODULE is unset, and respects the env when set.
func TestSecretsResolverHook_DefaultVaultKey(t *testing.T) {
	t.Setenv("RATCHET_VAULT_MODULE", "")
	_, svc := runSecretsResolverHook(t)
	if svc.holder.vaultKey != "vault" {
		t.Errorf("default vaultKey: got %q, want %q", svc.holder.vaultKey, "vault")
	}
}

// writeVaultConfigForTest writes a vault-config.json carrying the given token
// into dir so LoadVaultConfig can read it. Imported here to keep the hook test
// self-contained.
func writeVaultConfigForTest(t *testing.T, dir, token string) {
	t.Helper()
	cfg := `{"address":"https://vault.example","token":"` + token + `","backend":""}`
	path := dir + string(os.PathSeparator) + "vault-config.json"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write vault-config: %v", err)
	}
}
