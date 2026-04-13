package orchestrator

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow/module"
)

func TestInjectBlackboardInput_NoPhase(t *testing.T) {
	app := newMockApp()
	pc := &module.PipelineContext{Current: map[string]any{}}

	_, err := InjectBlackboardInput(context.Background(), app, InputFromBlackboard{}, pc)
	if err != nil {
		t.Fatalf("expected no error with empty phase, got: %v", err)
	}
	// pc.Current should be unmodified
	if len(pc.Current) != 0 {
		t.Errorf("expected pc.Current unchanged, got: %v", pc.Current)
	}
}

func TestInjectBlackboardInput_NoBlackboard(t *testing.T) {
	app := newMockApp() // no blackboard registered
	pc := &module.PipelineContext{Current: map[string]any{}}

	cfg := InputFromBlackboard{Phase: "design"}
	_, err := InjectBlackboardInput(context.Background(), app, cfg, pc)
	if err != nil {
		t.Fatalf("expected nil when blackboard not available, got: %v", err)
	}
	if pc.Current["blackboard_input"] != nil {
		t.Errorf("expected no injection without blackboard")
	}
}

func TestInjectBlackboardInput_LatestOnly(t *testing.T) {
	bb := newTestBlackboard(t)
	_ = bb.Post(context.Background(), Artifact{
		Phase: "design", AgentID: "a", Type: "yaml_config",
		Content: map[string]any{"v": 1},
	})
	_ = bb.Post(context.Background(), Artifact{
		Phase: "design", AgentID: "a", Type: "yaml_config",
		Content: map[string]any{"v": 2},
	})

	app := newMockApp()
	_ = app.RegisterService("ratchet-blackboard", bb)
	pc := &module.PipelineContext{Current: map[string]any{}}

	cfg := InputFromBlackboard{Phase: "design", LatestOnly: true}
	_, err := InjectBlackboardInput(context.Background(), app, cfg, pc)
	if err != nil {
		t.Fatalf("InjectBlackboardInput: %v", err)
	}

	injected, ok := pc.Current["blackboard_input"].(map[string]any)
	if !ok {
		t.Fatalf("expected artifact map in blackboard_input, got %T", pc.Current["blackboard_input"])
	}
	content, _ := injected["content"].(map[string]any)
	if content["v"] == nil {
		t.Errorf("expected v in content, got %v", content)
	}
}

func TestInjectBlackboardInput_MultipleArtifacts(t *testing.T) {
	bb := newTestBlackboard(t)
	_ = bb.Post(context.Background(), Artifact{Phase: "review", AgentID: "a", Type: "review_findings", Content: map[string]any{}})
	_ = bb.Post(context.Background(), Artifact{Phase: "review", AgentID: "b", Type: "review_findings", Content: map[string]any{}})

	app := newMockApp()
	_ = app.RegisterService("ratchet-blackboard", bb)
	pc := &module.PipelineContext{Current: map[string]any{}}

	cfg := InputFromBlackboard{Phase: "review", ArtifactType: "review_findings"}
	_, err := InjectBlackboardInput(context.Background(), app, cfg, pc)
	if err != nil {
		t.Fatalf("InjectBlackboardInput: %v", err)
	}

	reviews, ok := pc.Current["blackboard_input"].([]map[string]any)
	if !ok {
		t.Fatalf("expected slice in blackboard_input, got %T", pc.Current["blackboard_input"])
	}
	if len(reviews) != 2 {
		t.Errorf("expected 2 reviews, got %d", len(reviews))
	}
}

func TestInjectBlackboardInput_EmptyResult(t *testing.T) {
	bb := newTestBlackboard(t)

	app := newMockApp()
	_ = app.RegisterService("ratchet-blackboard", bb)
	pc := &module.PipelineContext{Current: map[string]any{}}

	cfg := InputFromBlackboard{Phase: "nonexistent"}
	_, err := InjectBlackboardInput(context.Background(), app, cfg, pc)
	if err != nil {
		t.Fatalf("InjectBlackboardInput: %v", err)
	}
	// No artifacts found — key should not be injected
	if pc.Current["blackboard_input"] != nil {
		t.Errorf("expected no injection for empty phase, got: %v", pc.Current["blackboard_input"])
	}
}

func TestInjectBlackboardInput_SystemPromptAppend(t *testing.T) {
	bb := newTestBlackboard(t)
	_ = bb.Post(context.Background(), Artifact{
		Phase: "design", AgentID: "a", Type: "yaml_config",
		Content: map[string]any{"spec": "v1"},
	})

	app := newMockApp()
	_ = app.RegisterService("ratchet-blackboard", bb)
	pc := &module.PipelineContext{Current: map[string]any{}}

	cfg := InputFromBlackboard{Phase: "design", LatestOnly: true, InjectAs: "system_prompt_append"}
	content, err := InjectBlackboardInput(context.Background(), app, cfg, pc)
	if err != nil {
		t.Fatalf("InjectBlackboardInput: %v", err)
	}
	if content == "" {
		t.Error("expected non-empty content for system_prompt_append mode")
	}
	// pc.Current should NOT be modified in prompt mode
	if pc.Current["blackboard_input"] != nil {
		t.Errorf("expected pc.Current unmodified in prompt mode, got: %v", pc.Current["blackboard_input"])
	}
}

func TestInjectBlackboardInput_UserMessage(t *testing.T) {
	bb := newTestBlackboard(t)
	_ = bb.Post(context.Background(), Artifact{
		Phase: "review", AgentID: "b", Type: "review_findings",
		Content: map[string]any{"finding": "ok"},
	})

	app := newMockApp()
	_ = app.RegisterService("ratchet-blackboard", bb)
	pc := &module.PipelineContext{Current: map[string]any{}}

	cfg := InputFromBlackboard{Phase: "review", ArtifactType: "review_findings", InjectAs: "user_message"}
	content, err := InjectBlackboardInput(context.Background(), app, cfg, pc)
	if err != nil {
		t.Fatalf("InjectBlackboardInput: %v", err)
	}
	if content == "" {
		t.Error("expected non-empty content for user_message mode")
	}
	if pc.Current["blackboard_input"] != nil {
		t.Errorf("expected pc.Current unmodified in user_message mode")
	}
}

func TestParseInputFromBlackboard(t *testing.T) {
	cfg := map[string]any{
		"input_from_blackboard": map[string]any{
			"phase":         "design",
			"artifact_type": "yaml_config",
			"latest_only":   true,
			"inject_as":     "system_prompt_append",
		},
	}

	ibb, ok := parseInputFromBlackboard(cfg)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ibb.Phase != "design" {
		t.Errorf("phase: want design, got %q", ibb.Phase)
	}
	if ibb.ArtifactType != "yaml_config" {
		t.Errorf("artifact_type: want yaml_config, got %q", ibb.ArtifactType)
	}
	if !ibb.LatestOnly {
		t.Error("expected latest_only=true")
	}
	if ibb.InjectAs != "system_prompt_append" {
		t.Errorf("inject_as: want system_prompt_append, got %q", ibb.InjectAs)
	}
}

func TestParseInputFromBlackboard_Missing(t *testing.T) {
	_, ok := parseInputFromBlackboard(map[string]any{})
	if ok {
		t.Error("expected ok=false when input_from_blackboard not configured")
	}
}
