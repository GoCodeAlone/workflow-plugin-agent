package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/GoCodeAlone/workflow/secrets"
)

// SecretManageStep manages secrets (set, delete, list) as a pipeline step.
type SecretManageStep struct {
	name    string
	action  string
	keyExpr string // template expression for the secret key
	valExpr string // template expression for the secret value
	app     modular.Application
	tmpl    *module.TemplateEngine
}

func (s *SecretManageStep) Name() string { return s.name }

func (s *SecretManageStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	// Lazy-lookup SecretGuard (registered by wiring hook after step factories)
	var guard *SecretGuard
	if svc, ok := s.app.SvcRegistry()["ratchet-secret-guard"]; ok {
		guard, _ = svc.(*SecretGuard)
	}
	if guard == nil {
		return nil, fmt.Errorf("secret_manage step %q: secret guard not available", s.name)
	}

	sp := guard.Provider()
	if sp == nil {
		return nil, fmt.Errorf("secret_manage step %q: secrets provider not available", s.name)
	}

	switch s.action {
	case "set":
		key, err := s.resolveKey(pc)
		if err != nil || key == "" {
			return nil, fmt.Errorf("secret_manage step %q: key is required", s.name)
		}
		value, err := s.resolveValue(pc)
		if err != nil || value == "" {
			return nil, fmt.Errorf("secret_manage step %q: value is required", s.name)
		}

		if err := sp.Set(ctx, key, value); err != nil {
			return nil, fmt.Errorf("secret_manage step %q: set secret: %w", s.name, err)
		}

		// Reload the secret into SecretGuard so it can be redacted
		_ = guard.LoadSecrets(ctx, []string{key})

		// Invalidate ProviderRegistry cache for providers using this secret
		s.invalidateProviderCache(key)

		return &module.StepResult{
			Output: map[string]any{
				"success": true,
				"key":     key,
				"action":  "set",
			},
		}, nil

	case "delete":
		key, err := s.resolveKey(pc)
		if err != nil || key == "" {
			return nil, fmt.Errorf("secret_manage step %q: key is required", s.name)
		}

		if err := sp.Delete(ctx, key); err != nil {
			if isNotFound(err) {
				return &module.StepResult{
					Output: map[string]any{
						"success": false,
						"key":     key,
						"action":  "delete",
						"error":   "secret not found",
					},
				}, nil
			}
			return nil, fmt.Errorf("secret_manage step %q: delete secret: %w", s.name, err)
		}

		// Reload all secrets to update SecretGuard
		_ = guard.LoadAllSecrets(ctx)

		// Invalidate ProviderRegistry cache for providers using this secret
		s.invalidateProviderCache(key)

		return &module.StepResult{
			Output: map[string]any{
				"success": true,
				"key":     key,
				"action":  "delete",
			},
		}, nil

	case "list":
		keys, err := sp.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("secret_manage step %q: list secrets: %w", s.name, err)
		}
		if keys == nil {
			keys = []string{}
		}

		return &module.StepResult{
			Output: map[string]any{
				"keys":   keys,
				"action": "list",
			},
		}, nil

	default:
		return nil, fmt.Errorf("secret_manage step %q: unknown action %q (want set|delete|list)", s.name, s.action)
	}
}

// resolveKey resolves the key expression from config or falls back to pc.Current.
func (s *SecretManageStep) resolveKey(pc *module.PipelineContext) (string, error) {
	if s.keyExpr != "" {
		raw, err := s.tmpl.Resolve(s.keyExpr, pc)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%v", raw), nil
	}
	return extractString(pc.Current, "key", ""), nil
}

// resolveValue resolves the value expression from config or falls back to pc.Current.
func (s *SecretManageStep) resolveValue(pc *module.PipelineContext) (string, error) {
	if s.valExpr != "" {
		raw, err := s.tmpl.Resolve(s.valExpr, pc)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%v", raw), nil
	}
	return extractString(pc.Current, "value", ""), nil
}

// invalidateProviderCache clears the ProviderRegistry cache for the given secret key.
func (s *SecretManageStep) invalidateProviderCache(secretName string) {
	if svc, ok := s.app.SvcRegistry()["ratchet-provider-registry"]; ok {
		if registry, ok := svc.(*ProviderRegistry); ok {
			registry.InvalidateCacheBySecret(secretName)
		}
	}
}

// isNotFound checks if an error is a secrets.ErrNotFound.
func isNotFound(err error) bool {
	return errors.Is(err, secrets.ErrNotFound)
}

// newSecretManageFactory returns a plugin.StepFactory for "step.secret_manage".
func newSecretManageFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		action, _ := cfg["action"].(string)
		if action == "" {
			action = "list"
		}

		keyExpr, _ := cfg["key"].(string)
		valExpr, _ := cfg["value"].(string)

		return &SecretManageStep{
			name:    name,
			action:  action,
			keyExpr: keyExpr,
			valExpr: valExpr,
			app:     app,
			tmpl:    module.NewTemplateEngine(),
		}, nil
	}
}
