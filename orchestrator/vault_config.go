package orchestrator

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const vaultConfigFilename = "vault-config.json"

// encryptedTokenPrefix marks a token as encrypted in the JSON file.
const encryptedTokenPrefix = "enc:"

// VaultConfigFile stores persistent vault backend configuration.
// Saved as JSON to data/vault-config.json so it's available before DB init.
// The token field is encrypted at rest using AES-256-GCM.
type VaultConfigFile struct {
	Backend   string `json:"backend"`              // "vault-dev" or "vault-remote"
	Address   string `json:"address,omitempty"`    // remote vault address
	Token     string `json:"token,omitempty"`      // encrypted remote vault token
	MountPath string `json:"mount_path,omitempty"` // KV v2 mount path
	Namespace string `json:"namespace,omitempty"`  // vault namespace
}

// LoadVaultConfig reads vault config from dir/vault-config.json.
// Returns nil (not error) if the file doesn't exist.
// Tokens are decrypted transparently.
func LoadVaultConfig(dir string) (*VaultConfigFile, error) {
	path := filepath.Join(dir, vaultConfigFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg VaultConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// Decrypt token if encrypted
	if len(cfg.Token) > len(encryptedTokenPrefix) && cfg.Token[:len(encryptedTokenPrefix)] == encryptedTokenPrefix {
		plain, err := decryptToken(cfg.Token[len(encryptedTokenPrefix):], dir)
		if err != nil {
			return nil, fmt.Errorf("decrypt vault token: %w", err)
		}
		cfg.Token = plain
	}
	return &cfg, nil
}

// SaveVaultConfig writes vault config to dir/vault-config.json.
// The token is encrypted at rest using AES-256-GCM with a machine-local key.
func SaveVaultConfig(dir string, cfg *VaultConfigFile) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	// Clone so we don't mutate the caller's struct
	out := *cfg
	if out.Token != "" {
		enc, err := encryptToken(out.Token, dir)
		if err != nil {
			return fmt.Errorf("encrypt vault token: %w", err)
		}
		out.Token = encryptedTokenPrefix + enc
	}

	data, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, vaultConfigFilename)
	return os.WriteFile(path, data, 0600)
}

// DeleteVaultConfig removes the vault config file.
func DeleteVaultConfig(dir string) error {
	path := filepath.Join(dir, vaultConfigFilename)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// deriveKey derives a 32-byte AES key from the config directory path and a
// machine-local keyfile. The keyfile is created on first use and stored at
// dir/.vault-key with 0600 permissions. This ensures the token can only be
// decrypted on the same machine in the same data directory.
func deriveKey(dir string) ([]byte, error) {
	keyPath := filepath.Join(dir, ".vault-key")
	keyMaterial, err := os.ReadFile(keyPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// Generate new random key material
		keyMaterial = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, keyMaterial); err != nil {
			return nil, fmt.Errorf("generate key material: %w", err)
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyPath, keyMaterial, 0600); err != nil {
			return nil, fmt.Errorf("write key file: %w", err)
		}
	}
	// Derive final key using SHA-256 of the key material + dir path as domain separation
	h := sha256.New()
	h.Write(keyMaterial)
	h.Write([]byte(dir))
	return h.Sum(nil), nil
}

func encryptToken(plaintext, dir string) (string, error) {
	key, err := deriveKey(dir)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decryptToken(encoded, dir string) (string, error) {
	key, err := deriveKey(dir)
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}
