package executor

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/tools"
)

// TestExecute_RequiresProvider verifies that a nil provider returns an error.
func TestExecute_RequiresProvider(t *testing.T) {
	_, err := Execute(context.Background(), Config{}, "sys", "task", "agent-1")
	if err == nil {
		t.Fatal("expected error when Provider is nil")
	}
}

// TestExecute_SimpleCompletion verifies the happy path: LLM responds with no tool calls.
func TestExecute_SimpleCompletion(t *testing.T) {
	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "Task completed successfully."},
	}
	cfg := Config{Provider: p}

	result, err := Execute(context.Background(), cfg, "You are a helper.", "Do something.", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
	if result.Content != "Task completed successfully." {
		t.Errorf("Content: want %q, got %q", "Task completed successfully.", result.Content)
	}
	if result.Iterations != 1 {
		t.Errorf("Iterations: want 1, got %d", result.Iterations)
	}
}

// TestExecute_ProviderError returns failed status when Chat fails.
func TestExecute_ProviderError(t *testing.T) {
	p := &mockProvider{
		name:    "mock",
		chatErr: fmt.Errorf("LLM unavailable"),
	}
	cfg := Config{Provider: p}

	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Status != "failed" {
		t.Errorf("Status: want failed, got %q", result.Status)
	}
}

// TestExecute_MaxIterationsCapped verifies the loop exits at MaxIterations.
// We use a provider that returns a final text response after the configured limit
// so we can verify iterations is bounded.
func TestExecute_MaxIterationsCapped(t *testing.T) {
	callN := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			callN++
			// Return a tool call with unique args each time to avoid loop detector.
			// After reaching MaxIterations the outer loop stops; if we still get
			// called we return a final answer.
			return &provider.Response{
				Content: "",
				ToolCalls: []provider.ToolCall{
					{ID: fmt.Sprintf("tc-%d", callN), Name: "counter_tool", Arguments: map[string]any{"n": callN}},
				},
			}, nil
		},
	}
	cfg := Config{
		Provider:      p,
		MaxIterations: 5,
	}

	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Iterations should be bounded by MaxIterations.
	if result.Iterations > 5 {
		t.Errorf("Iterations exceeded MaxIterations: got %d, want <= 5", result.Iterations)
	}
	if result.Iterations <= 0 {
		t.Errorf("Iterations should be positive, got %d", result.Iterations)
	}
}

// TestExecute_ToolExecution verifies tool calls are dispatched through the registry.
func TestExecute_ToolExecution(t *testing.T) {
	toolExecuted := false
	echoTool := &simpleTool{
		name: "echo",
		def:  provider.ToolDef{Name: "echo", Description: "echoes input"},
		fn: func(_ context.Context, args map[string]any) (any, error) {
			toolExecuted = true
			return map[string]any{"echoed": args["msg"]}, nil
		},
	}

	reg := tools.NewRegistry()
	reg.Register(echoTool)

	callN := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			callN++
			if callN == 1 {
				// First call: request tool execution.
				return &provider.Response{
					ToolCalls: []provider.ToolCall{
						{ID: "tc-echo", Name: "echo", Arguments: map[string]any{"msg": "hello"}},
					},
				}, nil
			}
			// Second call: final answer after tool result.
			return &provider.Response{Content: "Done with tool."}, nil
		},
	}

	cfg := Config{
		Provider:     p,
		ToolRegistry: reg,
	}
	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !toolExecuted {
		t.Error("expected echo tool to be executed")
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
}

// TestExecute_MemoryInjection verifies memory entries are included in system prompt.
func TestExecute_MemoryInjection(t *testing.T) {
	var capturedSystemPrompt string
	p := &captureProvider{
		onChat: func(msgs []provider.Message) (*provider.Response, error) {
			if len(msgs) > 0 {
				capturedSystemPrompt = msgs[0].Content
			}
			return &provider.Response{Content: "ok"}, nil
		},
	}

	mem := &stubMemoryStore{
		entries: []MemoryEntry{
			{ID: "m1", AgentID: "agent-1", Content: "User prefers Go over Python.", Category: "preference"},
		},
	}

	cfg := Config{
		Provider: p,
		Memory:   mem,
	}
	_, err := Execute(context.Background(), cfg, "Base prompt.", "Do task.", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if capturedSystemPrompt == "" {
		t.Fatal("expected system prompt to be captured")
	}
	// Memory entry should be injected into the system prompt.
	if len(capturedSystemPrompt) <= len("Base prompt.") {
		t.Errorf("expected system prompt to be augmented with memory; got: %q", capturedSystemPrompt)
	}
}

// TestExecute_TranscriptRecording verifies all messages are recorded.
func TestExecute_TranscriptRecording(t *testing.T) {
	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "All done."},
	}
	recorder := &countingTranscript{}
	cfg := Config{
		Provider:   p,
		Transcript: recorder,
	}
	_, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// At minimum: system msg + user msg + assistant response = 3 entries.
	if recorder.count < 3 {
		t.Errorf("expected at least 3 transcript entries, got %d", recorder.count)
	}
}

// TestExecute_NullApproverIsDefault verifies NullApprover is used when Approver is nil.
func TestExecute_NullApproverIsDefault(t *testing.T) {
	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "ok"},
	}
	cfg := Config{Provider: p} // No Approver set.
	result, err := Execute(context.Background(), cfg, "sys", "task", "a")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
}

// --- test helpers ---

// callCountProvider calls onChat for each Chat invocation.
type callCountProvider struct {
	onChat func() (*provider.Response, error)
}

func (c *callCountProvider) Name() string { return "call-count" }

func (c *callCountProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	return c.onChat()
}

func (c *callCountProvider) Stream(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

func (c *callCountProvider) AuthModeInfo() provider.AuthModeInfo { return provider.AuthModeInfo{} }

// captureProvider captures the messages passed to Chat.
type captureProvider struct {
	onChat func([]provider.Message) (*provider.Response, error)
}

func (c *captureProvider) Name() string { return "capture" }

func (c *captureProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	return c.onChat(msgs)
}

func (c *captureProvider) Stream(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

func (c *captureProvider) AuthModeInfo() provider.AuthModeInfo { return provider.AuthModeInfo{} }

// stubMemoryStore returns preset entries from Search.
type stubMemoryStore struct {
	entries []MemoryEntry
}

func (s *stubMemoryStore) Search(_ context.Context, _, _ string, _ int) ([]MemoryEntry, error) {
	return s.entries, nil
}

func (s *stubMemoryStore) Save(_ context.Context, _ MemoryEntry) error { return nil }

func (s *stubMemoryStore) ExtractAndSave(_ context.Context, _ string, _ string, _ provider.Embedder) error {
	return nil
}

// countingTranscript counts Record calls.
type countingTranscript struct {
	count int
}

func (c *countingTranscript) Record(_ context.Context, _ TranscriptEntry) error {
	c.count++
	return nil
}

// simpleTool is a minimal tools.Tool implementation.
type simpleTool struct {
	name string
	def  provider.ToolDef
	fn   func(context.Context, map[string]any) (any, error)
}

func (s *simpleTool) Name() string                   { return s.name }
func (s *simpleTool) Definition() provider.ToolDef   { return s.def }
func (s *simpleTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	return s.fn(ctx, args)
}
