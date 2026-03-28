package orchestrator

import (
	"database/sql"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	"github.com/GoCodeAlone/workflow/module"
)

// TestHarness wraps a channel-mode TestProvider with convenient methods
// for driving the agent loop from a test goroutine.
type TestHarness struct {
	t              *testing.T
	Provider       *TestProvider
	interactionsCh <-chan Interaction
	responsesCh    chan<- InteractionResponse
}

// NewTestHarness creates a TestHarness backed by a ChannelSource.
func NewTestHarness(t *testing.T) *TestHarness {
	t.Helper()
	source, iCh, rCh := NewChannelSource()
	tp := NewTestProvider(source, WithTimeout(30*time.Second))
	return &TestHarness{
		t:              t,
		Provider:       tp,
		interactionsCh: iCh,
		responsesCh:    rCh,
	}
}

// ExpectInteraction waits for the next interaction from the agent loop.
// Fails the test if no interaction arrives within the timeout.
func (h *TestHarness) ExpectInteraction(timeout time.Duration) Interaction {
	h.t.Helper()
	select {
	case interaction := <-h.interactionsCh:
		return interaction
	case <-time.After(timeout):
		h.t.Fatalf("TestHarness: timed out waiting for interaction after %v", timeout)
		return Interaction{} // unreachable
	}
}

// RespondText sends a text-only response (no tool calls), which will end
// the agent loop.
func (h *TestHarness) RespondText(content string) {
	h.t.Helper()
	h.responsesCh <- InteractionResponse{Content: content}
}

// RespondToolCall sends a response with content and one or more tool calls,
// which will cause the agent loop to execute the tools and continue.
func (h *TestHarness) RespondToolCall(content string, toolCalls ...provider.ToolCall) {
	h.t.Helper()
	h.responsesCh <- InteractionResponse{
		Content:   content,
		ToolCalls: toolCalls,
	}
}

// RespondError sends an error response, which will cause the agent loop
// to report a provider error.
func (h *TestHarness) RespondError(msg string) {
	h.t.Helper()
	h.responsesCh <- InteractionResponse{Error: msg}
}

// E2ETestEnv holds all the components needed for an E2E agent execution test.
type E2ETestEnv struct {
	App          *mockApp
	DB           *sql.DB
	ToolRegistry *ToolRegistry
	Recorder     *TranscriptRecorder
	Step         *AgentExecuteStep
}

// SetupE2EAgent creates a complete test environment with in-memory DB,
// all tables, tool registry with basic tools, transcript recorder,
// and a ready-to-execute AgentExecuteStep.
func SetupE2EAgent(t *testing.T, p provider.Provider) *E2ETestEnv {
	t.Helper()

	db := openTestDB(t)

	// Create all tables
	for _, ddl := range []string{createAgentsTable, createTasksTable, createMessagesTable, createProjectsTable, createTranscriptsTable} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	// Set up components
	sg := NewSecretGuard(&mockSecretsProvider{secrets: map[string]string{}}, "test")
	rec := NewTranscriptRecorder(db, sg)
	tr := NewToolRegistry()

	// Register basic tools for E2E testing
	tr.Register(&tools.FileReadTool{})
	tr.Register(&tools.FileWriteTool{})
	tr.Register(&tools.FileListTool{})

	// Provider module
	providerMod := &AIProviderModule{
		name:     "ratchet-ai",
		provider: p,
	}

	// Build mock app
	app := newMockApp()
	app.services["ratchet-ai"] = providerMod
	app.services["ratchet-tool-registry"] = tr
	app.services["ratchet-secret-guard"] = sg
	app.services["ratchet-transcript-recorder"] = rec

	step := &AgentExecuteStep{
		name:            "agent-exec-test",
		maxIterations:   10,
		providerService: "ratchet-ai",
		app:             app,
		tmpl:            module.NewTemplateEngine(),
	}

	return &E2ETestEnv{
		App:          app,
		DB:           db,
		ToolRegistry: tr,
		Recorder:     rec,
		Step:         step,
	}
}

// NewPipelineContext creates a PipelineContext with standard agent/task fields.
func NewPipelineContext(agentID, taskID, taskDescription string) *module.PipelineContext {
	return &module.PipelineContext{
		Current: map[string]any{
			"system_prompt": "You are a test agent. Use tools to complete the task.",
			"task":          taskDescription,
			"agent_name":    agentID,
			"agent_id":      agentID,
			"task_id":       taskID,
		},
	}
}
