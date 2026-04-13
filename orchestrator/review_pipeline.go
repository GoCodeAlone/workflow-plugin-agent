package orchestrator

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
)

// ReviewPipelineConfig holds provider configurations for the four review roles.
type ReviewPipelineConfig struct {
	DesignerProvider    string `yaml:"designer_provider" json:"designer_provider"`
	ImplementerProvider string `yaml:"implementer_provider" json:"implementer_provider"`
	ReviewerProvider    string `yaml:"reviewer_provider" json:"reviewer_provider"`
	SecurityProvider    string `yaml:"security_provider" json:"security_provider"`
}

// InputFromBlackboard specifies how to pull an artifact from the blackboard
// and inject it into an agent's context.
type InputFromBlackboard struct {
	Phase        string `yaml:"phase" json:"phase"`               // blackboard phase to read from
	ArtifactType string `yaml:"artifact_type" json:"artifact_type"` // optional artifact type filter
	LatestOnly   bool   `yaml:"latest_only" json:"latest_only"`   // if true, read only the latest artifact
	InjectKey    string `yaml:"inject_key" json:"inject_key"`     // key to inject into pc.Current (default: "blackboard_input")
}

// InjectBlackboardInput reads the specified artifact(s) from the blackboard
// and merges them into pc.Current under the configured inject key.
// Returns nil if no blackboard is registered or no artifact is found.
func InjectBlackboardInput(ctx context.Context, app modular.Application, cfg InputFromBlackboard, pc *module.PipelineContext) error {
	if cfg.Phase == "" {
		return nil
	}

	var bb *Blackboard
	if svc, ok := app.SvcRegistry()["ratchet-blackboard"]; ok {
		bb, _ = svc.(*Blackboard)
	}
	if bb == nil {
		return nil // blackboard not wired — skip gracefully
	}

	injectKey := cfg.InjectKey
	if injectKey == "" {
		injectKey = "blackboard_input"
	}

	if cfg.LatestOnly {
		art, err := bb.ReadLatest(ctx, cfg.Phase)
		if err != nil {
			return fmt.Errorf("input_from_blackboard: read latest phase %q: %w", cfg.Phase, err)
		}
		if art != nil {
			pc.Current[injectKey] = artifactToMap(*art)
		}
		return nil
	}

	artifacts, err := bb.Read(ctx, cfg.Phase, cfg.ArtifactType)
	if err != nil {
		return fmt.Errorf("input_from_blackboard: read phase %q: %w", cfg.Phase, err)
	}
	if len(artifacts) == 0 {
		return nil
	}

	out := make([]map[string]any, 0, len(artifacts))
	for _, a := range artifacts {
		out = append(out, artifactToMap(a))
	}
	pc.Current[injectKey] = out
	return nil
}

// parseInputFromBlackboard reads an "input_from_blackboard" config map into InputFromBlackboard.
func parseInputFromBlackboard(cfg map[string]any) (InputFromBlackboard, bool) {
	raw, ok := cfg["input_from_blackboard"].(map[string]any)
	if !ok {
		return InputFromBlackboard{}, false
	}
	var ibb InputFromBlackboard
	ibb.Phase, _ = raw["phase"].(string)
	ibb.ArtifactType, _ = raw["artifact_type"].(string)
	ibb.LatestOnly, _ = raw["latest_only"].(bool)
	ibb.InjectKey, _ = raw["inject_key"].(string)
	return ibb, ibb.Phase != ""
}
