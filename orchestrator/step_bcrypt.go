package orchestrator

import (
	"context"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"golang.org/x/crypto/bcrypt"
)

// BcryptCheckStep compares a password against a bcrypt hash.
// Reads "password" and "password_hash" from pc.Current.
// Returns {match: true/false}.
type BcryptCheckStep struct {
	name string
}

func (s *BcryptCheckStep) Name() string { return s.name }

func (s *BcryptCheckStep) Execute(_ context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	password, _ := pc.Current["password"].(string)
	hash, _ := pc.Current["password_hash"].(string)

	match := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil

	return &module.StepResult{
		Output: map[string]any{"match": match},
	}, nil
}

func newBcryptCheckFactory() plugin.StepFactory {
	return func(name string, _ map[string]any, _ modular.Application) (any, error) {
		return &BcryptCheckStep{name: name}, nil
	}
}

// BcryptHashStep hashes a password with bcrypt.
// Reads "password" from pc.Current.
// Returns {hash: "$2a$..."}.
type BcryptHashStep struct {
	name string
}

func (s *BcryptHashStep) Name() string { return s.name }

func (s *BcryptHashStep) Execute(_ context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	password, _ := pc.Current["password"].(string)

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{"error": err.Error()},
		}, nil
	}

	return &module.StepResult{
		Output: map[string]any{"hash": string(hash)},
	}, nil
}

func newBcryptHashFactory() plugin.StepFactory {
	return func(name string, _ map[string]any, _ modular.Application) (any, error) {
		return &BcryptHashStep{name: name}, nil
	}
}
