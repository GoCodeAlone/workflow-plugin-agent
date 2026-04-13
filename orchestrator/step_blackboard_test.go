package orchestrator

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow/module"
)

func newStepTestBlackboard(t *testing.T) (*Blackboard, *mockApp) {
	t.Helper()
	db := openTestDB(t)
	bb := NewBlackboard(db, nil)
	if err := bb.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	app := newMockApp()
	_ = app.RegisterService("ratchet-blackboard", bb)
	return bb, app
}

func TestBlackboardPostStep(t *testing.T) {
	bb, app := newStepTestBlackboard(t)
	ctx := context.Background()

	step := &BlackboardPostStep{
		name:         "test-post",
		phase:        "design",
		artifactType: "yaml_config",
		agentID:      "agent-1",
		app:          app,
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"content": map[string]any{"spec": "v1"},
			"tags":    []any{"important"},
		},
	}

	result, err := step.Execute(ctx, pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output["success"] != true {
		t.Errorf("expected success=true, got %v", result.Output["success"])
	}
	if result.Output["phase"] != "design" {
		t.Errorf("expected phase=design, got %v", result.Output["phase"])
	}

	artifacts, err := bb.Read(ctx, "design", "yaml_config")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	if artifacts[0].Content["spec"] != "v1" {
		t.Errorf("content: expected spec=v1, got %v", artifacts[0].Content)
	}
}

func TestBlackboardPostStepFallbackToCurrentData(t *testing.T) {
	bb, app := newStepTestBlackboard(t)
	ctx := context.Background()

	step := &BlackboardPostStep{
		name: "test-post-fallback",
		app:  app,
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"phase":         "review",
			"artifact_type": "review_findings",
			"agent_id":      "agent-99",
		},
	}

	result, err := step.Execute(ctx, pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output["phase"] != "review" {
		t.Errorf("expected phase=review, got %v", result.Output["phase"])
	}

	artifacts, err := bb.Read(ctx, "review", "review_findings")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
}

func TestBlackboardReadStep(t *testing.T) {
	bb, app := newStepTestBlackboard(t)
	ctx := context.Background()

	_ = bb.Post(ctx, Artifact{Phase: "security", AgentID: "a", Type: "iac_plan", Content: map[string]any{"ok": true}})
	_ = bb.Post(ctx, Artifact{Phase: "security", AgentID: "a", Type: "iac_plan", Content: map[string]any{"ok": false}})

	step := &BlackboardReadStep{
		name:         "test-read",
		phase:        "security",
		artifactType: "iac_plan",
		app:          app,
	}

	pc := &module.PipelineContext{Current: map[string]any{}}
	result, err := step.Execute(ctx, pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	count, _ := result.Output["count"].(int)
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}

	artifacts, ok := result.Output["artifacts"].([]map[string]any)
	if !ok || len(artifacts) != 2 {
		t.Errorf("expected 2 artifacts in output, got %v", result.Output["artifacts"])
	}
}

func TestBlackboardReadStepLatestOnly(t *testing.T) {
	bb, app := newStepTestBlackboard(t)
	ctx := context.Background()

	_ = bb.Post(ctx, Artifact{Phase: "approve", AgentID: "a", Type: "approval_decision", Content: map[string]any{"v": 1}})
	_ = bb.Post(ctx, Artifact{Phase: "approve", AgentID: "a", Type: "approval_decision", Content: map[string]any{"v": 2}})

	step := &BlackboardReadStep{
		name:       "test-read-latest",
		phase:      "approve",
		latestOnly: true,
		app:        app,
	}

	pc := &module.PipelineContext{Current: map[string]any{}}
	result, err := step.Execute(ctx, pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	found, _ := result.Output["found"].(bool)
	if !found {
		t.Error("expected found=true")
	}

	art, ok := result.Output["artifact"].(map[string]any)
	if !ok {
		t.Fatal("expected artifact in output")
	}
	content, _ := art["content"].(map[string]any)
	if content["v"] == nil {
		t.Errorf("expected v in content, got %v", content)
	}
}

func TestBlackboardReadStepNoBlackboard(t *testing.T) {
	app := newMockApp() // no blackboard registered

	step := &BlackboardReadStep{
		name:  "test-no-bb",
		phase: "design",
		app:   app,
	}

	pc := &module.PipelineContext{Current: map[string]any{}}
	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Error("expected error when blackboard not available")
	}
}

func TestBlackboardPostStepNoBlackboard(t *testing.T) {
	app := newMockApp() // no blackboard registered

	step := &BlackboardPostStep{
		name:  "test-no-bb",
		phase: "design",
		app:   app,
	}

	pc := &module.PipelineContext{Current: map[string]any{}}
	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Error("expected error when blackboard not available")
	}
}
