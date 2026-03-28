package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadVaultConfig_NotExist(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadVaultConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config for non-existent file")
	}
}

func TestSaveAndLoadVaultConfig(t *testing.T) {
	dir := t.TempDir()
	want := &VaultConfigFile{
		Backend:   "vault-remote",
		Address:   "https://vault.example.com:8200",
		Token:     "s.mytoken",
		MountPath: "kv",
		Namespace: "prod",
	}

	if err := SaveVaultConfig(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadVaultConfig(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil config")
		return
	}
	if got.Backend != want.Backend {
		t.Errorf("backend: got %q, want %q", got.Backend, want.Backend)
	}
	if got.Address != want.Address {
		t.Errorf("address: got %q, want %q", got.Address, want.Address)
	}
	if got.Token != want.Token {
		t.Errorf("token: got %q, want %q", got.Token, want.Token)
	}
	if got.MountPath != want.MountPath {
		t.Errorf("mount_path: got %q, want %q", got.MountPath, want.MountPath)
	}
	if got.Namespace != want.Namespace {
		t.Errorf("namespace: got %q, want %q", got.Namespace, want.Namespace)
	}
}

func TestSaveVaultConfig_TokenEncryptedOnDisk(t *testing.T) {
	dir := t.TempDir()
	plainToken := "s.super-secret-vault-token-12345"
	cfg := &VaultConfigFile{
		Backend: "vault-remote",
		Address: "https://vault.example.com:8200",
		Token:   plainToken,
	}

	if err := SaveVaultConfig(dir, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Read raw file to verify token is NOT in plaintext
	raw, err := os.ReadFile(filepath.Join(dir, vaultConfigFilename))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}

	if strings.Contains(string(raw), plainToken) {
		t.Fatal("SECURITY: plaintext token found in config file on disk")
	}

	// Verify the raw JSON has the enc: prefix
	var rawCfg VaultConfigFile
	if err := json.Unmarshal(raw, &rawCfg); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if !strings.HasPrefix(rawCfg.Token, encryptedTokenPrefix) {
		t.Errorf("expected encrypted token with prefix %q, got %q", encryptedTokenPrefix, rawCfg.Token[:10])
	}

	// Verify decryption round-trip
	got, err := LoadVaultConfig(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Token != plainToken {
		t.Errorf("decrypted token: got %q, want %q", got.Token, plainToken)
	}
}

func TestSaveVaultConfig_KeyFileCreated(t *testing.T) {
	dir := t.TempDir()
	cfg := &VaultConfigFile{
		Backend: "vault-remote",
		Token:   "s.test-token",
	}

	if err := SaveVaultConfig(dir, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	keyPath := filepath.Join(dir, ".vault-key")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("key file permissions: got %o, want 0600", info.Mode().Perm())
	}
}

func TestSaveVaultConfig_DifferentDirsDifferentKeys(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	token := "s.same-token"

	cfg := &VaultConfigFile{Backend: "vault-remote", Token: token}

	if err := SaveVaultConfig(dir1, cfg); err != nil {
		t.Fatalf("save dir1: %v", err)
	}
	if err := SaveVaultConfig(dir2, cfg); err != nil {
		t.Fatalf("save dir2: %v", err)
	}

	// Read raw encrypted tokens — they should differ
	raw1, _ := os.ReadFile(filepath.Join(dir1, vaultConfigFilename))
	raw2, _ := os.ReadFile(filepath.Join(dir2, vaultConfigFilename))

	var cfg1, cfg2 VaultConfigFile
	_ = json.Unmarshal(raw1, &cfg1)
	_ = json.Unmarshal(raw2, &cfg2)

	if cfg1.Token == cfg2.Token {
		t.Error("encrypted tokens should differ between directories (different keys)")
	}

	// Both should decrypt to the same value
	got1, _ := LoadVaultConfig(dir1)
	got2, _ := LoadVaultConfig(dir2)
	if got1.Token != token || got2.Token != token {
		t.Errorf("decrypted tokens don't match: dir1=%q, dir2=%q", got1.Token, got2.Token)
	}
}

func TestSaveVaultConfig_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subdir", "nested")
	cfg := &VaultConfigFile{Backend: "vault-dev"}
	if err := SaveVaultConfig(dir, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	path := filepath.Join(dir, vaultConfigFilename)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestDeleteVaultConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := &VaultConfigFile{Backend: "vault-dev"}
	if err := SaveVaultConfig(dir, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := DeleteVaultConfig(dir); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, err := LoadVaultConfig(dir)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil config after delete")
	}
}

func TestDeleteVaultConfig_NotExist(t *testing.T) {
	dir := t.TempDir()
	if err := DeleteVaultConfig(dir); err != nil {
		t.Fatalf("delete non-existent: %v", err)
	}
}

func TestLoadVaultConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, vaultConfigFilename)
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadVaultConfig(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSaveVaultConfig_DevMinimal(t *testing.T) {
	dir := t.TempDir()
	cfg := &VaultConfigFile{Backend: "vault-dev"}
	if err := SaveVaultConfig(dir, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadVaultConfig(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Backend != "vault-dev" {
		t.Errorf("backend: got %q, want %q", got.Backend, "vault-dev")
	}
	if got.Address != "" {
		t.Errorf("address should be empty for vault-dev, got %q", got.Address)
	}
}

func TestSaveVaultConfig_EmptyTokenNotEncrypted(t *testing.T) {
	dir := t.TempDir()
	cfg := &VaultConfigFile{Backend: "vault-dev", Token: ""}
	if err := SaveVaultConfig(dir, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, vaultConfigFilename))
	if strings.Contains(string(raw), encryptedTokenPrefix) {
		t.Error("empty token should not be encrypted")
	}
}

func TestLoadVaultConfig_CorruptedEncryptedToken(t *testing.T) {
	dir := t.TempDir()
	// Write a config with a corrupted encrypted token
	cfg := map[string]string{
		"backend": "vault-remote",
		"token":   encryptedTokenPrefix + "not-valid-base64!!!",
	}
	data, _ := json.Marshal(cfg)
	_ = os.WriteFile(filepath.Join(dir, vaultConfigFilename), data, 0600)

	_, err := LoadVaultConfig(dir)
	if err == nil {
		t.Fatal("expected error for corrupted encrypted token")
	}
}
