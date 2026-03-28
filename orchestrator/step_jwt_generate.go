package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/golang-jwt/jwt/v5"
)

// JWTGenerateStep creates a signed JWT token.
// Reads "user_id", "username", "role" from pc.Current.
// Returns {token: "eyJ..."}.
type JWTGenerateStep struct {
	name   string
	secret []byte
}

func (s *JWTGenerateStep) Name() string { return s.name }

func (s *JWTGenerateStep) Execute(_ context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	userID, _ := pc.Current["user_id"].(string)
	username, _ := pc.Current["username"].(string)
	role, _ := pc.Current["role"].(string)

	now := time.Now()
	claims := jwt.MapClaims{
		"sub":      userID,
		"username": username,
		"role":     role,
		"iat":      now.Unix(),
		"exp":      now.Add(24 * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(s.secret)
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{"error": fmt.Sprintf("jwt sign: %v", err)},
		}, nil
	}

	return &module.StepResult{
		Output: map[string]any{"token": tokenString},
	}, nil
}

func newJWTGenerateFactory() plugin.StepFactory {
	return func(name string, config map[string]any, app modular.Application) (any, error) {
		// Try to get secret from step config first, then from config provider
		secret := ""
		if s, ok := config["secret"].(string); ok && s != "" {
			secret = s
		}

		// Fall back to config provider
		if secret == "" {
			if cp, ok := app.SvcRegistry()["config-provider"]; ok {
				if getter, ok := cp.(interface{ Get(string) string }); ok {
					secret = getter.Get("auth_secret")
				}
			}
		}

		if secret == "" {
			secret = "ratchet-dev-secret-change-me"
		}

		return &JWTGenerateStep{
			name:   name,
			secret: []byte(secret),
		}, nil
	}
}
