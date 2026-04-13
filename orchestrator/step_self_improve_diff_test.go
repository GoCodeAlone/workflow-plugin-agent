package orchestrator

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow/module"
)

func TestSelfImproveDiffStep_BasicDiff(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveDiffStep{name: "test-diff", app: app}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"current_yaml":  "line1\nline2\nline3\n",
			"proposed_yaml": "line1\nline2-modified\nline3\nline4\n",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	hasChanges, _ := result.Output["has_changes"].(bool)
	if !hasChanges {
		t.Error("expected has_changes=true")
	}
	added, _ := result.Output["lines_added"].(int)
	removed, _ := result.Output["lines_removed"].(int)
	if added == 0 || removed == 0 {
		t.Errorf("expected added>0 and removed>0, got added=%d removed=%d", added, removed)
	}
}

func TestSelfImproveDiffStep_NoChanges(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveDiffStep{name: "test-diff", app: app}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"current_yaml":  "same content\n",
			"proposed_yaml": "same content\n",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	hasChanges, _ := result.Output["has_changes"].(bool)
	if hasChanges {
		t.Error("expected has_changes=false when content is identical")
	}
}

func TestSelfImproveDiffStep_ForcedDiff(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveDiffStep{name: "test-diff", force: true, app: app}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"current_yaml":  "same\n",
			"proposed_yaml": "same\n",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// With force=true, step always returns output even with no changes
	if _, ok := result.Output["diff"]; !ok {
		t.Error("expected diff key in output with force=true")
	}
}

func TestSelfImproveDiffStep_MissingProposed(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveDiffStep{name: "test-diff", app: app}

	pc := &module.PipelineContext{Current: map[string]any{}}
	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Error("expected error when proposed_yaml is missing")
	}
}

func TestSelfImproveDiffStep_PostToBlackboard(t *testing.T) {
	bb := newTestBlackboard(t)
	app := newMockApp()
	_ = app.RegisterService("ratchet-blackboard", bb)

	step := &SelfImproveDiffStep{
		name:               "test-diff",
		outputToBlackboard: true,
		app:                app,
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"current_yaml":  "old: value\n",
			"proposed_yaml": "new: value\n",
			"phase":         "implement",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output["blackboard_warning"] != nil {
		t.Errorf("unexpected blackboard warning: %v", result.Output["blackboard_warning"])
	}

	// Verify artifact posted
	artifacts, err := bb.Read(context.Background(), "implement", "config_diff")
	if err != nil {
		t.Fatalf("Read blackboard: %v", err)
	}
	if len(artifacts) != 1 {
		t.Errorf("expected 1 artifact in blackboard, got %d", len(artifacts))
	}
}

func TestComputeTextDiff(t *testing.T) {
	tests := []struct {
		name     string
		old      string
		new      string
		wantAdded   int
		wantRemoved int
	}{
		{
			name: "add lines",
			old:  "a\nb\n",
			new:  "a\nb\nc\n",
			wantAdded: 1, wantRemoved: 0,
		},
		{
			name: "remove lines",
			old:  "a\nb\nc\n",
			new:  "a\nb\n",
			wantAdded: 0, wantRemoved: 1,
		},
		{
			name: "modify lines",
			old:  "a\nb\n",
			new:  "a\nb-mod\n",
			wantAdded: 1, wantRemoved: 1,
		},
		{
			name: "no changes",
			old:  "same\n",
			new:  "same\n",
			wantAdded: 0, wantRemoved: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := computeTextDiff(tt.old, tt.new)
			added, removed := countDiffLines(diff)
			if added != tt.wantAdded {
				t.Errorf("lines_added: want %d, got %d", tt.wantAdded, added)
			}
			if removed != tt.wantRemoved {
				t.Errorf("lines_removed: want %d, got %d", tt.wantRemoved, removed)
			}
		})
	}
}
