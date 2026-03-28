package orchestrator

import (
	"context"
	"net/http"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/golang-jwt/jwt/v5"
)

// JWTDecodeStep decodes a JWT token from the Authorization header.
// Tries multiple sources: HTTP request metadata, Current, trigger data.
// Returns decoded claims: {sub, username, role, authenticated}.
type JWTDecodeStep struct {
	name   string
	secret []byte
}

func (s *JWTDecodeStep) Name() string { return s.name }

func (s *JWTDecodeStep) Execute(_ context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	authHeader := ""

	// Try HTTP request from metadata first (most reliable)
	if req, ok := pc.Metadata["_http_request"].(*http.Request); ok && req != nil {
		authHeader = req.Header.Get("Authorization")
	}

	// Fall back to Current (set by step.set from request_parse)
	if authHeader == "" {
		if auth, ok := pc.Current["authorization"].(string); ok {
			authHeader = auth
		}
	}

	// Fall back to trigger data
	if authHeader == "" {
		if headers, ok := pc.TriggerData["headers"].(map[string]any); ok {
			if auth, ok := headers["Authorization"].(string); ok {
				authHeader = auth
			}
		}
	}

	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenStr == "" || tokenStr == authHeader {
		return &module.StepResult{
			Output: map[string]any{"error": "no bearer token", "authenticated": false},
		}, nil
	}

	token, err := jwt.Parse(tokenStr, func(_ *jwt.Token) (any, error) {
		return s.secret, nil
	})
	if err != nil || !token.Valid {
		return &module.StepResult{
			Output: map[string]any{"error": "invalid token", "authenticated": false},
		}, nil
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return &module.StepResult{
			Output: map[string]any{"error": "invalid claims", "authenticated": false},
		}, nil
	}

	output := map[string]any{"authenticated": true}
	if sub, ok := claims["sub"].(string); ok {
		output["sub"] = sub
	}
	if username, ok := claims["username"].(string); ok {
		output["username"] = username
	}
	if role, ok := claims["role"].(string); ok {
		output["role"] = role
	}

	return &module.StepResult{Output: output}, nil
}

func newJWTDecodeFactory() plugin.StepFactory {
	return func(name string, config map[string]any, app modular.Application) (any, error) {
		secret := ""
		if s, ok := config["secret"].(string); ok && s != "" {
			secret = s
		}
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

		return &JWTDecodeStep{
			name:   name,
			secret: []byte(secret),
		}, nil
	}
}
