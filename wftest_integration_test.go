package agent_test

import (
	"testing"

	agent "github.com/GoCodeAlone/workflow-plugin-agent"
	"github.com/GoCodeAlone/workflow/wftest"
)

// TestAgentPlugin_ExecuteStep verifies that step.agent_execute can be mocked
// and that the mock is invoked once with the expected pipeline data.
func TestAgentPlugin_ExecuteStep(t *testing.T) {
	execRec := wftest.RecordStep("step.agent_execute")
	execRec.WithOutput(map[string]any{
		"result":     "The answer is 42",
		"status":     "completed",
		"iterations": 1,
	})

	h := wftest.New(t,
		wftest.WithPlugin(agent.New()),
		wftest.WithYAML(`
pipelines:
  run-agent:
    trigger:
      type: manual
    steps:
      - name: execute
        type: step.agent_execute
        config:
          provider_service: mock-provider
          max_iterations: 3
`),
		execRec,
	)

	result := h.ExecutePipeline("run-agent", map[string]any{
		"task":        "What is the meaning of life?",
		"system_prompt": "You are a helpful assistant.",
	})
	if result.Error != nil {
		t.Fatalf("pipeline failed: %v", result.Error)
	}
	if execRec.CallCount() != 1 {
		t.Errorf("expected 1 call to step.agent_execute, got %d", execRec.CallCount())
	}
}

// TestAgentPlugin_ProviderModelsStep verifies that step.provider_models can be
// mocked and that the recorder captures the invocation correctly.
func TestAgentPlugin_ProviderModelsStep(t *testing.T) {
	modelsRec := wftest.RecordStep("step.provider_models")
	modelsRec.WithOutput(map[string]any{
		"success": true,
		"models": []any{
			map[string]any{"id": "claude-3-5-sonnet", "name": "Claude 3.5 Sonnet"},
			map[string]any{"id": "claude-3-haiku", "name": "Claude 3 Haiku"},
		},
	})

	h := wftest.New(t,
		wftest.WithPlugin(agent.New()),
		wftest.WithYAML(`
pipelines:
  list-models:
    trigger:
      type: manual
    steps:
      - name: get-models
        type: step.provider_models
`),
		modelsRec,
	)

	result := h.ExecutePipeline("list-models", map[string]any{
		"type":    "anthropic",
		"api_key": "test-key",
	})
	if result.Error != nil {
		t.Fatalf("pipeline failed: %v", result.Error)
	}
	if modelsRec.CallCount() != 1 {
		t.Errorf("expected 1 call to step.provider_models, got %d", modelsRec.CallCount())
	}

	models, ok := result.Output["models"].([]any)
	if !ok {
		t.Fatalf("expected models in output, got %T", result.Output["models"])
	}
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %d", len(models))
	}
}

// TestAgentPlugin_ProviderTestStep verifies that step.provider_test can be mocked
// and returns a successful connection result.
func TestAgentPlugin_ProviderTestStep(t *testing.T) {
	testRec := wftest.RecordStep("step.provider_test")
	testRec.WithOutput(map[string]any{
		"success":    true,
		"message":    "Connection successful",
		"latency_ms": int64(42),
	})

	h := wftest.New(t,
		wftest.WithPlugin(agent.New()),
		wftest.WithYAML(`
pipelines:
  test-provider:
    trigger:
      type: manual
    steps:
      - name: test-conn
        type: step.provider_test
        config:
          alias: my-anthropic
`),
		testRec,
	)

	result := h.ExecutePipeline("test-provider", map[string]any{
		"alias": "my-anthropic",
	})
	if result.Error != nil {
		t.Fatalf("pipeline failed: %v", result.Error)
	}
	if testRec.CallCount() != 1 {
		t.Errorf("expected 1 call to step.provider_test, got %d", testRec.CallCount())
	}

	success, ok := result.Output["success"].(bool)
	if !ok || !success {
		t.Errorf("expected success=true in output, got %v", result.Output["success"])
	}
}
