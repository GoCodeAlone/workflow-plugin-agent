package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// DeployStrategy identifies the deployment approach.
type DeployStrategy string

const (
	DeployStrategyHotReload DeployStrategy = "hot_reload"
	DeployStrategyGitPR     DeployStrategy = "git_pr"
	DeployStrategyCanary    DeployStrategy = "canary"
)

// SelfImproveDeployStep executes one of three deployment strategies after a
// mandatory pre-deploy validation gate.
//
// Config keys:
//
//	strategy     string — "hot_reload", "git_pr", or "canary" (default: "git_pr")
//	proposed_key string — key in pc.Current for proposed YAML (default: "proposed_yaml")
//	branch_prefix string — git branch prefix for git_pr strategy (default: "self-improve/")
//	skip_validation bool — skip the pre-deploy validation gate (not recommended)
type SelfImproveDeployStep struct {
	name           string
	strategy       DeployStrategy
	proposedKey    string
	branchPrefix   string
	skipValidation bool
	app            modular.Application
}

func (s *SelfImproveDeployStep) Name() string { return s.name }

func (s *SelfImproveDeployStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	proposedKey := s.proposedKey
	if proposedKey == "" {
		proposedKey = "proposed_yaml"
	}

	proposedYAML := extractString(pc.Current, proposedKey, "")
	if proposedYAML == "" {
		return nil, fmt.Errorf("self_improve_deploy step %q: %q is required", s.name, proposedKey)
	}

	// Pre-deploy validation gate (mandatory unless explicitly skipped)
	if !s.skipValidation {
		if err := s.runValidationGate(ctx, pc, proposedYAML); err != nil {
			return &module.StepResult{
				Output: map[string]any{
					"deployed": false,
					"strategy": string(s.strategy),
					"error":    "pre-deploy validation failed: " + err.Error(),
				},
			}, nil
		}
	}

	strategy := s.strategy
	if strategy == "" {
		// Fall back to guardrails default if configured
		if gm := findGuardrailsModule(s.app); gm != nil && gm.defaults.DeployStrategy != "" {
			strategy = DeployStrategy(gm.defaults.DeployStrategy)
		} else {
			strategy = DeployStrategyGitPR
		}
	}

	switch strategy {
	case DeployStrategyHotReload:
		return s.deployHotReload(ctx, pc, proposedYAML)
	case DeployStrategyGitPR:
		return s.deployGitPR(ctx, pc, proposedYAML)
	case DeployStrategyCanary:
		return s.deployCanary(ctx, pc, proposedYAML)
	default:
		return nil, fmt.Errorf("self_improve_deploy step %q: unknown strategy %q", s.name, strategy)
	}
}

// runValidationGate re-runs the validate step logic inline as a gate.
func (s *SelfImproveDeployStep) runValidationGate(ctx context.Context, pc *module.PipelineContext, proposedYAML string) error {
	validateStep := &SelfImproveValidateStep{
		name:        s.name + ":pre-deploy-validate",
		proposedKey: "proposed_yaml",
		app:         s.app,
	}
	gatePc := &module.PipelineContext{
		Current: map[string]any{"proposed_yaml": proposedYAML},
	}
	if current := extractString(pc.Current, "current_yaml", ""); current != "" {
		gatePc.Current["current_yaml"] = current
	}

	result, err := validateStep.Execute(ctx, gatePc)
	if err != nil {
		return err
	}
	valid, _ := result.Output["valid"].(bool)
	if !valid {
		errs, _ := result.Output["errors"].([]string)
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// deployHotReload writes the config and signals a reload.
// In practice this would call modular.ReloadOrchestrator() via a configwatcher.
func (s *SelfImproveDeployStep) deployHotReload(_ context.Context, pc *module.PipelineContext, proposedYAML string) (*module.StepResult, error) {
	configPath := extractString(pc.Current, "config_path", "workflow.yaml")

	// Write config file
	if err := writeFileContents(configPath, proposedYAML); err != nil {
		return nil, fmt.Errorf("hot_reload: write config: %w", err)
	}

	// Signal reload via config watcher service if available.
	if svc, ok := s.app.SvcRegistry()["ratchet-config-watcher"]; ok {
		if reloader, ok := svc.(interface{ Reload() error }); ok {
			if err := reloader.Reload(); err != nil {
				return &module.StepResult{
					Output: map[string]any{
						"deployed": false,
						"strategy": "hot_reload",
						"error":    "reload signal failed: " + err.Error(),
					},
				}, nil
			}
		}
	}

	return &module.StepResult{
		Output: map[string]any{
			"deployed":    true,
			"strategy":    "hot_reload",
			"config_path": configPath,
		},
	}, nil
}

// deployGitPR creates a branch, commits the config, pushes, and opens a PR.
func (s *SelfImproveDeployStep) deployGitPR(_ context.Context, pc *module.PipelineContext, proposedYAML string) (*module.StepResult, error) {
	prefix := s.branchPrefix
	if prefix == "" {
		prefix = "self-improve/"
	}
	configPath := extractString(pc.Current, "config_path", "workflow.yaml")
	branchName := prefix + "update"
	if agentID := extractString(pc.Current, "agent_id", ""); agentID != "" {
		branchName = prefix + agentID
	}

	// Write proposed config to file first
	if err := writeFileContents(configPath, proposedYAML); err != nil {
		return nil, fmt.Errorf("git_pr: write config: %w", err)
	}

	// Create branch, commit, push, and open PR via git/gh CLI.
	steps := [][]string{
		{"git", "checkout", "-b", branchName},
		{"git", "add", configPath},
		{"git", "commit", "-m", "chore: self-improvement config update"},
		{"git", "push", "origin", branchName},
		{"gh", "pr", "create", "--title", "Self-improvement: config update",
			"--body", "Automated config update proposed by self-improvement pipeline.",
			"--head", branchName},
	}

	var prURL string
	for _, args := range steps {
		out, err := runCommand(args[0], args[1:]...)
		if err != nil {
			return &module.StepResult{
				Output: map[string]any{
					"deployed": false,
					"strategy": "git_pr",
					"error":    fmt.Sprintf("%s failed: %v", args[0], err),
				},
			}, nil
		}
		if args[0] == "gh" {
			prURL = strings.TrimSpace(out)
		}
	}

	return &module.StepResult{
		Output: map[string]any{
			"deployed": true,
			"strategy": "git_pr",
			"branch":   branchName,
			"pr_url":   prURL,
		},
	}, nil
}

// deployCanary runs a Docker container with the proposed config, health-checks it,
// then promotes (replaces current) or rolls back.
func (s *SelfImproveDeployStep) deployCanary(_ context.Context, pc *module.PipelineContext, proposedYAML string) (*module.StepResult, error) {
	image := extractString(pc.Current, "docker_image", "")
	if image == "" {
		return nil, fmt.Errorf("canary deploy: docker_image is required in pipeline data")
	}

	configPath := extractString(pc.Current, "config_path", "workflow.yaml")
	if err := writeFileContents(configPath+".canary", proposedYAML); err != nil {
		return nil, fmt.Errorf("canary: write config: %w", err)
	}

	containerName := "ratchet-canary-" + extractString(pc.Current, "agent_id", "test")

	// Start canary container
	_, err := runCommand("docker", "run", "-d",
		"--name", containerName,
		"-v", configPath+".canary:/app/workflow.yaml",
		image,
	)
	if err != nil {
		return &module.StepResult{
			Output: map[string]any{
				"deployed": false,
				"strategy": "canary",
				"error":    "failed to start canary container: " + err.Error(),
			},
		}, nil
	}

	// Health check: inspect container status
	healthOut, healthErr := runCommand("docker", "inspect", "--format={{.State.Health.Status}}", containerName)
	healthy := healthErr == nil && strings.TrimSpace(healthOut) == "healthy"

	// Cleanup canary container regardless
	_, _ = runCommand("docker", "rm", "-f", containerName)

	if !healthy {
		return &module.StepResult{
			Output: map[string]any{
				"deployed":  false,
				"strategy":  "canary",
				"rolled_back": true,
				"error":     "canary health check failed; rolled back",
			},
		}, nil
	}

	// Promote: write proposed config as the live config
	if err := writeFileContents(configPath, proposedYAML); err != nil {
		return nil, fmt.Errorf("canary: promote config: %w", err)
	}

	return &module.StepResult{
		Output: map[string]any{
			"deployed": true,
			"strategy": "canary",
			"promoted": true,
		},
	}, nil
}

// writeFileContents writes content to path (used for config updates).
func writeFileContents(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// runCommand executes a shell command and returns stdout or an error.
func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}

// newSelfImproveDeployFactory returns a plugin.StepFactory for "step.self_improve_deploy".
func newSelfImproveDeployFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		strategy, _ := cfg["strategy"].(string)
		proposedKey, _ := cfg["proposed_key"].(string)
		branchPrefix, _ := cfg["branch_prefix"].(string)
		skipValidation, _ := cfg["skip_validation"].(bool)
		return &SelfImproveDeployStep{
			name:           name,
			strategy:       DeployStrategy(strategy),
			proposedKey:    proposedKey,
			branchPrefix:   branchPrefix,
			skipValidation: skipValidation,
			app:            app,
		}, nil
	}
}
