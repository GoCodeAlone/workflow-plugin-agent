package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
)

// ReviewPipelineConfig holds provider configurations for the four review roles.
type ReviewPipelineConfig struct {
	DesignerProvider    string `yaml:"designer_provider" json:"designer_provider"`
	ImplementerProvider string `yaml:"implementer_provider" json:"implementer_provider"`
	ReviewerProvider    string `yaml:"reviewer_provider" json:"reviewer_provider"`
	SecurityProvider    string `yaml:"security_provider" json:"security_provider"`
	RequireApproval     bool   `yaml:"require_approval" json:"require_approval"`
}

// InputFromBlackboard specifies how to pull an artifact from the blackboard
// and inject it into an agent's context.
type InputFromBlackboard struct {
	Phase        string `yaml:"phase" json:"phase"`               // blackboard phase to read from
	ArtifactType string `yaml:"artifact_type" json:"artifact_type"` // optional artifact type filter
	LatestOnly   bool   `yaml:"latest_only" json:"latest_only"`   // if true, read only the latest artifact
	// InjectAs controls how the artifact is injected:
	//   "system_prompt_append" — appended to the agent's system prompt
	//   "user_message"         — added as a user message before the agent loop
	//   ""                     — stored in pc.Current["blackboard_input"] (default)
	InjectAs string `yaml:"inject_as" json:"inject_as"`
}

// InjectBlackboardInput reads the specified artifact(s) from the blackboard.
//
// Injection behaviour depends on cfg.InjectAs:
//   - "system_prompt_append" or "user_message": returns the artifact content as a
//     formatted string so the caller can append it to the system prompt or add it
//     as a user message. pc.Current is not modified.
//   - "" (default): stores the artifact(s) in pc.Current["blackboard_input"] and
//     returns an empty string.
//
// Returns ("", nil) if no blackboard is registered or no artifact is found.
func InjectBlackboardInput(ctx context.Context, app modular.Application, cfg InputFromBlackboard, pc *module.PipelineContext) (string, error) {
	if cfg.Phase == "" {
		return "", nil
	}

	var bb *Blackboard
	if svc, ok := app.SvcRegistry()["ratchet-blackboard"]; ok {
		bb, _ = svc.(*Blackboard)
	}
	if bb == nil {
		return "", nil // blackboard not wired — skip gracefully
	}

	promptMode := cfg.InjectAs == "system_prompt_append" || cfg.InjectAs == "user_message"

	if cfg.LatestOnly {
		art, err := bb.ReadLatest(ctx, cfg.Phase)
		if err != nil {
			return "", fmt.Errorf("input_from_blackboard: read latest phase %q: %w", cfg.Phase, err)
		}
		if art == nil {
			return "", nil
		}
		if promptMode {
			return fmt.Sprintf("[Blackboard artifact — phase: %s, type: %s]\n%v", art.Phase, art.Type, art.Content), nil
		}
		pc.Current["blackboard_input"] = artifactToMap(*art)
		return "", nil
	}

	artifacts, err := bb.Read(ctx, cfg.Phase, cfg.ArtifactType)
	if err != nil {
		return "", fmt.Errorf("input_from_blackboard: read phase %q: %w", cfg.Phase, err)
	}
	if len(artifacts) == 0 {
		return "", nil
	}

	if promptMode {
		var sb strings.Builder
		for i, a := range artifacts {
			if i > 0 {
				sb.WriteString("\n\n")
			}
			fmt.Fprintf(&sb, "[Blackboard artifact %d — phase: %s, type: %s]\n%v", i+1, a.Phase, a.Type, a.Content)
		}
		return sb.String(), nil
	}

	out := make([]map[string]any, 0, len(artifacts))
	for _, a := range artifacts {
		out = append(out, artifactToMap(a))
	}
	pc.Current["blackboard_input"] = out
	return "", nil
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
	ibb.InjectAs, _ = raw["inject_as"].(string)
	return ibb, ibb.Phase != ""
}
