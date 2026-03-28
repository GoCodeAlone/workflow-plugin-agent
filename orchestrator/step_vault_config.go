package orchestrator

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/GoCodeAlone/workflow/secrets"
)

// validPathSegment matches alphanumeric, hyphens, underscores, and forward slashes.
var validPathSegment = regexp.MustCompile(`^[a-zA-Z0-9_/.-]+$`)

// VaultConfigStep manages vault backend configuration as a pipeline step.
// Actions: get_status, test, configure, migrate, reset
type VaultConfigStep struct {
	name   string
	action string
	app    modular.Application
	tmpl   *module.TemplateEngine
}

func (s *VaultConfigStep) Name() string { return s.name }

func (s *VaultConfigStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	switch s.action {
	case "get_status":
		return s.getStatus()
	case "test":
		return s.testConnection(ctx, pc)
	case "configure":
		return s.configure(ctx, pc)
	case "migrate":
		return s.migrate(ctx)
	case "reset":
		return s.reset(ctx)
	default:
		return nil, fmt.Errorf("vault_config step %q: unknown action %q", s.name, s.action)
	}
}

func (s *VaultConfigStep) getStatus() (*module.StepResult, error) {
	guard := s.lookupGuard()
	if guard == nil {
		return nil, fmt.Errorf("vault_config step %q: secret guard not available", s.name)
	}

	result := map[string]any{
		"backend": guard.BackendName(),
	}

	// Load saved config for address/mount info
	cfg, _ := LoadVaultConfig(vaultConfigDir())
	if cfg != nil {
		result["address"] = cfg.Address
		result["mount_path"] = cfg.MountPath
		result["namespace"] = cfg.Namespace
	} else {
		result["address"] = ""
		result["mount_path"] = ""
		result["namespace"] = ""
	}

	return &module.StepResult{Output: result}, nil
}

func (s *VaultConfigStep) testConnection(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	body := extractBody(pc)
	address := extractString(body, "address", "")
	token := extractString(body, "token", "")
	mountPath := extractString(body, "mount_path", "secret")
	namespace := extractString(body, "namespace", "")

	if address == "" {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "address is required",
			},
		}, nil
	}
	if token == "" {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "token is required",
			},
		}, nil
	}

	if msg, ok := validateVaultInputs(address, mountPath, namespace); !ok {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   msg,
			},
		}, nil
	}

	vp, err := secrets.NewVaultProvider(secrets.VaultConfig{
		Address:   address,
		Token:     token,
		MountPath: mountPath,
		Namespace: namespace,
	})
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "failed to create vault client - check address and credentials",
			},
		}, nil
	}

	// Test connectivity by listing secrets
	_, err = vp.List(ctx)
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "connection test failed - verify vault is reachable and token is valid",
			},
		}, nil
	}

	return &module.StepResult{
		Output: map[string]any{
			"success": true,
			"message": "connection successful",
		},
	}, nil
}

func (s *VaultConfigStep) configure(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	guard := s.lookupGuard()
	if guard == nil {
		return nil, fmt.Errorf("vault_config step %q: secret guard not available", s.name)
	}

	body := extractBody(pc)
	address := extractString(body, "address", "")
	token := extractString(body, "token", "")
	mountPath := extractString(body, "mount_path", "secret")
	namespace := extractString(body, "namespace", "")

	if address == "" || token == "" {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "address and token are required",
			},
		}, nil
	}

	if msg, ok := validateVaultInputs(address, mountPath, namespace); !ok {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   msg,
			},
		}, nil
	}

	// Create new vault provider to verify it works
	vp, err := secrets.NewVaultProvider(secrets.VaultConfig{
		Address:   address,
		Token:     token,
		MountPath: mountPath,
		Namespace: namespace,
	})
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "failed to create vault client - check address and credentials",
			},
		}, nil
	}

	// Test connectivity
	if _, err := vp.List(ctx); err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "connection test failed - verify vault is reachable and token is valid",
			},
		}, nil
	}

	// Optionally migrate secrets
	migrateStr := extractString(body, "migrate_secrets", "")
	if migrateStr == "true" {
		if err := s.migrateSecrets(ctx, guard.Provider(), vp); err != nil {
			return &module.StepResult{
				Output: map[string]any{
					"success": false,
					"error":   "migration failed - some secrets may not have been copied",
				},
			}, nil
		}
	}

	// Save config file
	cfg := &VaultConfigFile{
		Backend:   "vault-remote",
		Address:   address,
		Token:     token,
		MountPath: mountPath,
		Namespace: namespace,
	}
	if err := SaveVaultConfig(vaultConfigDir(), cfg); err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "failed to save vault configuration",
			},
		}, nil
	}

	// Hot-swap the provider
	guard.SetProvider(vp, "vault-remote")

	// Register vault token for redaction so it never appears in logs/transcripts
	guard.AddKnownSecret("VAULT_TOKEN", token)

	// Update ProviderRegistry's secrets provider reference
	s.updateProviderRegistry(vp)

	return &module.StepResult{
		Output: map[string]any{
			"success": true,
			"message": "configured remote vault backend",
			"backend": "vault-remote",
		},
	}, nil
}

func (s *VaultConfigStep) migrate(ctx context.Context) (*module.StepResult, error) {
	guard := s.lookupGuard()
	if guard == nil {
		return nil, fmt.Errorf("vault_config step %q: secret guard not available", s.name)
	}

	// Load saved config to create destination provider
	cfg, err := LoadVaultConfig(vaultConfigDir())
	if err != nil || cfg == nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "no vault config found - configure first",
			},
		}, nil
	}

	if cfg.Backend != "vault-remote" {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "migration only supported when remote vault is configured",
			},
		}, nil
	}

	dst, err := secrets.NewVaultProvider(secrets.VaultConfig{
		Address:   cfg.Address,
		Token:     cfg.Token,
		MountPath: cfg.MountPath,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "failed to connect to destination vault - check saved configuration",
			},
		}, nil
	}

	migrated, err := s.doMigrate(ctx, guard.Provider(), dst)
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "migration failed - some secrets may not have been copied",
			},
		}, nil
	}

	return &module.StepResult{
		Output: map[string]any{
			"success":  true,
			"message":  fmt.Sprintf("migrated %d secrets", migrated),
			"migrated": migrated,
		},
	}, nil
}

func (s *VaultConfigStep) reset(ctx context.Context) (*module.StepResult, error) {
	guard := s.lookupGuard()
	if guard == nil {
		return nil, fmt.Errorf("vault_config step %q: secret guard not available", s.name)
	}

	// Delete config file
	if err := DeleteVaultConfig(vaultConfigDir()); err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"success": false,
				"error":   "failed to reset vault configuration",
			},
		}, nil
	}

	// Try to start vault-dev
	dp, err := secrets.NewDevVaultProvider(secrets.DevVaultConfig{})
	if err != nil {
		// Fall back to file provider
		secretsDir := os.Getenv("RATCHET_SECRETS_DIR")
		if secretsDir == "" {
			secretsDir = "data/secrets"
		}
		_ = os.MkdirAll(secretsDir, 0700)
		fp := secrets.NewFileProvider(secretsDir)
		guard.SetProvider(fp, "file")
		s.updateProviderRegistry(fp)

		return &module.StepResult{
			Output: map[string]any{
				"success": true,
				"message": "reset to file backend (vault binary not available)",
				"backend": "file",
			},
		}, nil
	}

	// Register dev vault for cleanup
	_ = s.app.RegisterService("ratchet-vault-dev", dp)
	guard.SetProvider(dp, "vault-dev")
	s.updateProviderRegistry(dp)

	return &module.StepResult{
		Output: map[string]any{
			"success": true,
			"message": "reset to vault-dev backend",
			"backend": "vault-dev",
		},
	}, nil
}

// migrateSecrets copies all secrets from src to dst.
func (s *VaultConfigStep) migrateSecrets(ctx context.Context, src, dst secrets.Provider) error {
	_, err := s.doMigrate(ctx, src, dst)
	return err
}

func (s *VaultConfigStep) doMigrate(ctx context.Context, src, dst secrets.Provider) (int, error) {
	keys, err := src.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list source secrets: %w", err)
	}

	count := 0
	for _, key := range keys {
		val, err := src.Get(ctx, key)
		if err != nil {
			continue // skip secrets that can't be read
		}
		if err := dst.Set(ctx, key, val); err != nil {
			return count, fmt.Errorf("set %q in destination: %w", key, err)
		}
		count++
	}
	return count, nil
}

func (s *VaultConfigStep) lookupGuard() *SecretGuard {
	if svc, ok := s.app.SvcRegistry()["ratchet-secret-guard"]; ok {
		if guard, ok := svc.(*SecretGuard); ok {
			return guard
		}
	}
	return nil
}

func (s *VaultConfigStep) updateProviderRegistry(p secrets.Provider) {
	if svc, ok := s.app.SvcRegistry()["ratchet-provider-registry"]; ok {
		if registry, ok := svc.(*ProviderRegistry); ok {
			registry.UpdateSecretsProvider(p)
		}
	}
}

// validateVaultInputs validates address, mount_path, and namespace fields.
func validateVaultInputs(address, mountPath, namespace string) (string, bool) {
	// Validate address is a well-formed URL with http(s) scheme
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return "address must be a valid URL (e.g. https://vault.example.com:8200)", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "address must use http or https scheme", false
	}
	if u.Path != "" && u.Path != "/" {
		return "address should not include a path — use mount_path instead", false
	}

	// Validate mount_path: alphanumeric, hyphens, underscores, dots, slashes only
	if mountPath != "" && !validPathSegment.MatchString(mountPath) {
		return "mount_path contains invalid characters (allowed: alphanumeric, hyphens, underscores, dots, slashes)", false
	}
	if len(mountPath) > 128 {
		return "mount_path is too long (max 128 characters)", false
	}

	// Validate namespace: same rules as mount_path
	if namespace != "" && !validPathSegment.MatchString(namespace) {
		return "namespace contains invalid characters (allowed: alphanumeric, hyphens, underscores, dots, slashes)", false
	}
	if len(namespace) > 128 {
		return "namespace is too long (max 128 characters)", false
	}

	return "", true
}

// extractBody extracts the request body from the pipeline context.
// After step.request_parse, the body is in pc.Current["body"].
func extractBody(pc *module.PipelineContext) map[string]any {
	if body, ok := pc.Current["body"].(map[string]any); ok {
		return body
	}
	return pc.Current
}

// vaultConfigDir returns the directory for vault config storage.
func vaultConfigDir() string {
	dir := os.Getenv("RATCHET_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	return dir
}

// newVaultConfigFactory returns a plugin.StepFactory for "step.vault_config".
func newVaultConfigFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		action, _ := cfg["action"].(string)
		if action == "" {
			action = "get_status"
		}

		return &VaultConfigStep{
			name:   name,
			action: action,
			app:    app,
			tmpl:   module.NewTemplateEngine(),
		}, nil
	}
}
