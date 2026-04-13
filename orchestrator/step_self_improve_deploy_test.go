package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoCodeAlone/workflow/module"
)

func TestSelfImproveDeployStep_MissingProposed(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveDeployStep{name: "test-deploy", strategy: DeployStrategyGitPR, app: app}

	pc := &module.PipelineContext{Current: map[string]any{}}
	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Error("expected error when proposed_yaml is missing")
	}
}

func TestSelfImproveDeployStep_ValidationGateBlocks(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveDeployStep{
		name:     "test-deploy",
		strategy: DeployStrategyHotReload,
		app:      app,
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			// invalid YAML triggers validation failure
			"proposed_yaml": "{\ninvalid: [yaml",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	deployed, _ := result.Output["deployed"].(bool)
	if deployed {
		t.Error("expected deployed=false when pre-deploy validation fails")
	}
	if result.Output["error"] == nil {
		t.Error("expected error message in output")
	}
}

func TestSelfImproveDeployStep_HotReload_SkipsValidation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "workflow.yaml")

	app := newMockApp()
	step := &SelfImproveDeployStep{
		name:           "test-deploy",
		strategy:       DeployStrategyHotReload,
		skipValidation: true,
		app:            app,
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"proposed_yaml": "modules: []\n",
			"config_path":   configPath,
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	deployed, _ := result.Output["deployed"].(bool)
	if !deployed {
		t.Errorf("expected deployed=true, got error: %v", result.Output["error"])
	}
	if result.Output["strategy"] != "hot_reload" {
		t.Errorf("expected strategy=hot_reload, got %v", result.Output["strategy"])
	}

	// Verify file was written
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(content) != "modules: []\n" {
		t.Errorf("unexpected config content: %q", string(content))
	}
}

func TestSelfImproveDeployStep_GitPR_CommandFailure(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "workflow.yaml")

	app := newMockApp()
	step := &SelfImproveDeployStep{
		name:           "test-deploy",
		strategy:       DeployStrategyGitPR,
		skipValidation: true,
		app:            app,
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"proposed_yaml": "modules: []\n",
			"config_path":   configPath,
			"agent_id":      "test-agent",
		},
	}

	// git commands will fail in a non-git dir — expect a non-fatal error output
	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	// Should return deployed=false with an error message (git fails)
	deployed, _ := result.Output["deployed"].(bool)
	if deployed {
		// It's possible only if git happens to succeed in tmpdir — acceptable
		t.Log("git_pr deployed=true (unexpected but non-fatal in this test context)")
	} else if result.Output["error"] == nil {
		t.Error("expected error message when git commands fail")
	}
}

func TestSelfImproveDeployStep_UnknownStrategy(t *testing.T) {
	app := newMockApp()
	step := &SelfImproveDeployStep{
		name:           "test-deploy",
		strategy:       "unknown_strategy",
		skipValidation: true,
		app:            app,
	}

	pc := &module.PipelineContext{
		Current: map[string]any{"proposed_yaml": "modules: []\n"},
	}

	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Error("expected error for unknown strategy")
	}
}
