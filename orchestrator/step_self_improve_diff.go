package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// SelfImproveDiffStep generates a diff between current and proposed configs,
// optionally posting the result to the Blackboard.
//
// Config keys:
//
//	proposed_key        string — key in pc.Current for proposed YAML (default: "proposed_yaml")
//	current_key         string — key in pc.Current for current YAML (default: "current_yaml")
//	force               bool   — always generate diff even if content is identical
//	include_iac         bool   — include IaC-relevant fields in diff output
//	output_to_blackboard bool  — post diff artifact to blackboard
type SelfImproveDiffStep struct {
	name               string
	proposedKey        string
	currentKey         string
	force              bool
	includeIAC         bool
	outputToBlackboard bool
	app                modular.Application
}

func (s *SelfImproveDiffStep) Name() string { return s.name }

func (s *SelfImproveDiffStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	proposedKey := s.proposedKey
	if proposedKey == "" {
		proposedKey = "proposed_yaml"
	}
	currentKey := s.currentKey
	if currentKey == "" {
		currentKey = "current_yaml"
	}

	proposedYAML := extractString(pc.Current, proposedKey, "")
	currentYAML := extractString(pc.Current, currentKey, "")

	if proposedYAML == "" {
		return nil, fmt.Errorf("self_improve_diff step %q: %q is required", s.name, proposedKey)
	}

	diff := computeTextDiff(currentYAML, proposedYAML)
	hasChanges := len(diff) > 0

	if !hasChanges && !s.force {
		return &module.StepResult{
			Output: map[string]any{
				"diff":        "",
				"has_changes": false,
				"lines_added": 0,
				"lines_removed": 0,
			},
		}, nil
	}

	linesAdded, linesRemoved := countDiffLines(diff)

	output := map[string]any{
		"diff":          strings.Join(diff, "\n"),
		"has_changes":   hasChanges,
		"lines_added":   linesAdded,
		"lines_removed": linesRemoved,
	}

	if s.includeIAC {
		output["iac_relevant"] = true
	}

	if s.outputToBlackboard && hasChanges {
		if err := s.postToBlackboard(ctx, pc, diff); err != nil {
			// Non-fatal: log but continue
			output["blackboard_warning"] = err.Error()
		}
	}

	return &module.StepResult{Output: output}, nil
}

// postToBlackboard posts the diff as a config_diff artifact to the blackboard.
func (s *SelfImproveDiffStep) postToBlackboard(ctx context.Context, pc *module.PipelineContext, diff []string) error {
	var bb *Blackboard
	if svc, ok := s.app.SvcRegistry()["ratchet-blackboard"]; ok {
		bb, _ = svc.(*Blackboard)
	}
	if bb == nil {
		return fmt.Errorf("blackboard not available")
	}

	phase := extractString(pc.Current, "phase", "implement")
	agentID := extractString(pc.Current, "agent_id", "")

	linesAdded, _ := countDiffLines(diff)
	art := Artifact{
		Phase:   phase,
		AgentID: agentID,
		Type:    "config_diff",
		Content: map[string]any{
			"diff":        strings.Join(diff, "\n"),
			"lines_added": linesAdded,
		},
		Tags: []string{"diff"},
	}
	return bb.Post(ctx, art)
}

// computeTextDiff returns a simple unified-style diff between old and new text.
// Each line is prefixed with "+" (added), "-" (removed), or " " (unchanged).
func computeTextDiff(oldText, newText string) []string {
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	var result []string
	maxLen := len(oldLines)
	if len(newLines) > maxLen {
		maxLen = len(newLines)
	}

	for i := 0; i < maxLen; i++ {
		switch {
		case i >= len(oldLines):
			result = append(result, "+"+newLines[i])
		case i >= len(newLines):
			result = append(result, "-"+oldLines[i])
		case oldLines[i] != newLines[i]:
			result = append(result, "-"+oldLines[i])
			result = append(result, "+"+newLines[i])
		}
	}
	return result
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func countDiffLines(diff []string) (added, removed int) {
	for _, line := range diff {
		if strings.HasPrefix(line, "+") {
			added++
		} else if strings.HasPrefix(line, "-") {
			removed++
		}
	}
	return
}

// newSelfImproveDiffFactory returns a plugin.StepFactory for "step.self_improve_diff".
func newSelfImproveDiffFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		proposedKey, _ := cfg["proposed_key"].(string)
		currentKey, _ := cfg["current_key"].(string)
		force, _ := cfg["force"].(bool)
		includeIAC, _ := cfg["include_iac"].(bool)
		outputToBlackboard, _ := cfg["output_to_blackboard"].(bool)
		return &SelfImproveDiffStep{
			name:               name,
			proposedKey:        proposedKey,
			currentKey:         currentKey,
			force:              force,
			includeIAC:         includeIAC,
			outputToBlackboard: outputToBlackboard,
			app:                app,
		}, nil
	}
}
