package orchestrator

import (
	"context"
	"fmt"
	"os"
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
	proposedYAMLFile   string // file path to read proposed YAML from (overrides proposedKey if set)
	currentYAMLFile    string // file path to read current YAML from (overrides currentKey if set)
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

	var proposedYAML string
	if s.proposedYAMLFile != "" {
		data, err := os.ReadFile(s.proposedYAMLFile)
		if err != nil {
			return nil, fmt.Errorf("self_improve_diff step %q: reading proposed_yaml_file %q: %w", s.name, s.proposedYAMLFile, err)
		}
		proposedYAML = string(data)
	} else {
		proposedYAML = extractString(pc.Current, proposedKey, "")
	}

	var currentYAML string
	if s.currentYAMLFile != "" {
		data, err := os.ReadFile(s.currentYAMLFile)
		if err != nil {
			return nil, fmt.Errorf("self_improve_diff step %q: reading current_yaml_file %q: %w", s.name, s.currentYAMLFile, err)
		}
		currentYAML = string(data)
	} else {
		currentYAML = extractString(pc.Current, currentKey, "")
	}

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
// The Blackboard is TRULY-OPTIONAL here: when resolveServices hands back a
// NullBlackboard, the post is skipped (this method returns a benign error that
// Execute records as a non-fatal blackboard_warning, leaving the diff output
// intact). When the service IS present it posts via the BlackboardService
// interface.
func (s *SelfImproveDiffStep) postToBlackboard(ctx context.Context, pc *module.PipelineContext, diff []string) error {
	bb := resolveServices(s.app).Blackboard
	if IsNull(bb) {
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
		proposedYAMLFile, _ := cfg["proposed_yaml_file"].(string)
		currentYAMLFile, _ := cfg["current_yaml_file"].(string)
		force, _ := cfg["force"].(bool)
		includeIAC, _ := cfg["include_iac"].(bool)
		outputToBlackboard, _ := cfg["output_to_blackboard"].(bool)
		return &SelfImproveDiffStep{
			name:               name,
			proposedKey:        proposedKey,
			currentKey:         currentKey,
			proposedYAMLFile:   proposedYAMLFile,
			currentYAMLFile:    currentYAMLFile,
			force:              force,
			includeIAC:         includeIAC,
			outputToBlackboard: outputToBlackboard,
			app:                app,
		}, nil
	}
}
