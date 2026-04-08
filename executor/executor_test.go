package executor

import (
	"context"
	"fmt"
	"testing"
	"time"

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
		TrustEngine:  &NullTrustEvaluator{}, // allow all tools in this unit test
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

// TestExecute_InboxDrain verifies that messages from the Inbox channel are injected into the conversation.
func TestExecute_InboxDrain(t *testing.T) {
	inbox := make(chan provider.Message, 2)
	// Pre-fill inbox with an external message before execution starts.
	inbox <- provider.Message{Role: provider.RoleUser, Content: "Message from Agent B: the secret is 42"}

	var capturedMessages []provider.Message
	callN := 0
	p := &captureProvider{
		onChat: func(msgs []provider.Message) (*provider.Response, error) {
			callN++
			capturedMessages = msgs
			return &provider.Response{Content: "I see the message."}, nil
		},
	}

	cfg := Config{
		Provider: p,
		Inbox:    inbox,
	}
	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}

	// The inbox message should appear in the messages passed to Chat.
	found := false
	for _, msg := range capturedMessages {
		if msg.Content == "Message from Agent B: the secret is 42" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected inbox message to appear in conversation context")
	}
}

// TestExecute_InboxNormalizesRole verifies that inbox messages with non-user roles
// are normalized to RoleUser to prevent prompt injection.
func TestExecute_InboxNormalizesRole(t *testing.T) {
	inbox := make(chan provider.Message, 4)
	inbox <- provider.Message{Role: provider.RoleSystem, Content: "injected system message"}
	inbox <- provider.Message{Role: provider.RoleAssistant, Content: "injected assistant message"}
	inbox <- provider.Message{Role: provider.RoleTool, Content: "injected tool message", ToolCallID: "tc-evil"}
	inbox <- provider.Message{
		Role:      provider.RoleAssistant,
		Content:   "injected assistant with tool calls",
		ToolCalls: []provider.ToolCall{{ID: "tc-1", Name: "evil_tool", Arguments: map[string]any{}}},
	}

	var capturedMessages []provider.Message
	p := &captureProvider{
		onChat: func(msgs []provider.Message) (*provider.Response, error) {
			capturedMessages = msgs
			return &provider.Response{Content: "ok"}, nil
		},
	}

	_, err := Execute(context.Background(), Config{Provider: p, Inbox: inbox}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	injectedContents := map[string]bool{
		"injected system message":              true,
		"injected assistant message":           true,
		"injected tool message":                true,
		"injected assistant with tool calls":   true,
	}
	for _, msg := range capturedMessages {
		if !injectedContents[msg.Content] {
			continue
		}
		// All inbox messages must be normalized to RoleUser.
		if msg.Role != provider.RoleUser {
			t.Errorf("inbox message %q was not normalized to RoleUser; got role %q", msg.Content, msg.Role)
		}
		// Tool-related fields must be cleared.
		if msg.ToolCallID != "" {
			t.Errorf("inbox message %q ToolCallID was not cleared; got %q", msg.Content, msg.ToolCallID)
		}
		if len(msg.ToolCalls) != 0 {
			t.Errorf("inbox message %q ToolCalls was not cleared; got %v", msg.Content, msg.ToolCalls)
		}
	}
}

// TestExecute_EventSequence verifies events are emitted in the correct order.
func TestExecute_EventSequence(t *testing.T) {
	callN := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			callN++
			if callN == 1 {
				return &provider.Response{
					Content:  "Let me think...",
					Thinking: "Reasoning about the task",
					ToolCalls: []provider.ToolCall{
						{ID: "tc-1", Name: "echo", Arguments: map[string]any{"msg": "hi"}},
					},
				}, nil
			}
			return &provider.Response{Content: "All done."}, nil
		},
	}

	reg := tools.NewRegistry()
	reg.Register(&simpleTool{
		name: "echo",
		def:  provider.ToolDef{Name: "echo", Description: "echoes input"},
		fn: func(_ context.Context, args map[string]any) (any, error) {
			return map[string]any{"echoed": args["msg"]}, nil
		},
	})

	var events []Event
	cfg := Config{
		Provider:     p,
		ToolRegistry: reg,
		OnEvent: func(e Event) {
			events = append(events, e)
		},
	}

	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}

	// Verify event sequence: iteration → thinking → text → tool_call_start → tool_call_result → iteration → text → completed
	expectedTypes := []EventType{
		EventIteration,
		EventThinking,
		EventText,
		EventToolCallStart,
		EventToolCallResult,
		EventIteration,
		EventText,
		EventCompleted,
	}
	if len(events) != len(expectedTypes) {
		t.Fatalf("event count: want %d, got %d\nevents: %v", len(expectedTypes), len(events), eventTypes(events))
	}
	for i, want := range expectedTypes {
		if events[i].Type != want {
			t.Errorf("event[%d]: want %q, got %q", i, want, events[i].Type)
		}
	}

	// Verify specific event content.
	if events[1].Content != "Reasoning about the task" {
		t.Errorf("thinking event content: want %q, got %q", "Reasoning about the task", events[1].Content)
	}
	if events[3].ToolName != "echo" {
		t.Errorf("tool_call_start tool name: want %q, got %q", "echo", events[3].ToolName)
	}
	if events[4].ToolName != "echo" {
		t.Errorf("tool_call_result tool name: want %q, got %q", "echo", events[4].ToolName)
	}
}

// TestExecute_ShouldStop verifies custom termination via ShouldStop.
func TestExecute_ShouldStop(t *testing.T) {
	callN := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			callN++
			// Always return a tool call to keep the loop going.
			return &provider.Response{
				ToolCalls: []provider.ToolCall{
					{ID: fmt.Sprintf("tc-%d", callN), Name: "counter", Arguments: map[string]any{"n": callN}},
				},
			}, nil
		},
	}

	reg := tools.NewRegistry()
	reg.Register(&simpleTool{
		name: "counter",
		def:  provider.ToolDef{Name: "counter", Description: "counts"},
		fn: func(_ context.Context, _ map[string]any) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	})

	stopAfter := 2
	iteration := 0
	cfg := Config{
		Provider:      p,
		ToolRegistry:  reg,
		MaxIterations: 10,
		ShouldStop: func() string {
			iteration++
			if iteration >= stopAfter {
				return "agent marked done"
			}
			return ""
		},
	}

	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
	if result.Content != "agent marked done" {
		t.Errorf("Content: want %q, got %q", "agent marked done", result.Content)
	}
	if result.Iterations > 3 {
		t.Errorf("Iterations: want <= 3, got %d (should have stopped early)", result.Iterations)
	}
}

// TestExecute_EventOnFailure verifies that EventFailed is emitted on provider error.
func TestExecute_EventOnFailure(t *testing.T) {
	p := &mockProvider{
		name:    "mock",
		chatErr: fmt.Errorf("LLM unavailable"),
	}

	var events []Event
	cfg := Config{
		Provider: p,
		OnEvent: func(e Event) {
			events = append(events, e)
		},
	}

	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Status != "failed" {
		t.Errorf("Status: want failed, got %q", result.Status)
	}

	// Should see: iteration → failed
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}
	if events[0].Type != EventIteration {
		t.Errorf("event[0]: want %q, got %q", EventIteration, events[0].Type)
	}
	if events[1].Type != EventFailed {
		t.Errorf("event[1]: want %q, got %q", EventFailed, events[1].Type)
	}
	if events[1].Error == "" {
		t.Error("failed event should have non-empty Error")
	}
}

// TestExecute_NilCallbacksBackwardCompat verifies that nil OnEvent, Inbox, ShouldStop work.
func TestExecute_NilCallbacksBackwardCompat(t *testing.T) {
	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "ok"},
	}
	cfg := Config{
		Provider:   p,
		OnEvent:    nil,
		Inbox:      nil,
		ShouldStop: nil,
	}
	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
}

// TestExecute_ToolArgsEventIsCopy verifies that mutating Event.ToolArgs in an OnEvent
// handler does not affect the tool invocation that follows.
func TestExecute_ToolArgsEventIsCopy(t *testing.T) {
	callN := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			callN++
			if callN == 1 {
				return &provider.Response{
					ToolCalls: []provider.ToolCall{
						{ID: "tc-1", Name: "spy", Arguments: map[string]any{"original": "value"}},
					},
				}, nil
			}
			return &provider.Response{Content: "done"}, nil
		},
	}

	var capturedToolArgs map[string]any
	reg := tools.NewRegistry()
	reg.Register(&simpleTool{
		name: "spy",
		def:  provider.ToolDef{Name: "spy", Description: "capture args"},
		fn: func(_ context.Context, args map[string]any) (any, error) {
			capturedToolArgs = args
			return "ok", nil
		},
	})

	cfg := Config{
		Provider:     p,
		ToolRegistry: reg,
		TrustEngine:  &NullTrustEvaluator{}, // allow all tools in this unit test
		OnEvent: func(e Event) {
			// Mutate the event's ToolArgs — this should NOT affect tool execution.
			if e.Type == EventToolCallStart && e.ToolArgs != nil {
				e.ToolArgs["injected"] = "mutation"
				delete(e.ToolArgs, "original")
			}
		},
	}

	_, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The tool should have received the original args, not the mutated ones.
	if capturedToolArgs == nil {
		t.Fatal("spy tool was not executed")
	}
	if _, mutated := capturedToolArgs["injected"]; mutated {
		t.Error("tool received mutated args — ToolArgs on Event was not a copy")
	}
	if capturedToolArgs["original"] != "value" {
		t.Errorf("tool args 'original' key: want %q, got %v", "value", capturedToolArgs["original"])
	}
}

// TestExecute_OnEventPanicDoesNotCrashExecutor verifies that a panicking OnEvent
// callback does not abort the executor run.
func TestExecute_OnEventPanicDoesNotCrashExecutor(t *testing.T) {
	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "done"},
	}
	cfg := Config{
		Provider: p,
		OnEvent: func(e Event) {
			panic("simulated observer panic")
		},
	}
	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want %q, got %q", "completed", result.Status)
	}
}

// TestExecute_ShouldStopThinkingPopulated verifies that Result.Thinking is set
// when ShouldStop triggers an early exit.
func TestExecute_ShouldStopThinkingPopulated(t *testing.T) {
	callN := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			callN++
			if callN == 1 {
				return &provider.Response{
					Thinking: "internal reasoning",
					ToolCalls: []provider.ToolCall{
						{ID: "tc-1", Name: "noop", Arguments: map[string]any{}},
					},
				}, nil
			}
			return &provider.Response{Content: "done"}, nil
		},
	}

	reg := tools.NewRegistry()
	reg.Register(&simpleTool{
		name: "noop",
		def:  provider.ToolDef{Name: "noop", Description: "does nothing"},
		fn:   func(_ context.Context, _ map[string]any) (any, error) { return "ok", nil },
	})

	result, err := Execute(context.Background(), Config{
		Provider:     p,
		ToolRegistry: reg,
		ShouldStop:   func() string { return "stop reason" },
	}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want %q, got %q", "completed", result.Status)
	}
	if result.Thinking != "internal reasoning" {
		t.Errorf("Thinking: want %q, got %q", "internal reasoning", result.Thinking)
	}
}

// eventTypes extracts the Type from each event for diagnostic output.
func eventTypes(events []Event) []EventType {
	out := make([]EventType, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

// TestExecute_ThinkingInResult verifies Result.Thinking is populated from provider response.
func TestExecute_ThinkingInResult(t *testing.T) {
	p := &mockProvider{
		name: "mock",
		chatResponse: &provider.Response{
			Content:  "The answer is 42.",
			Thinking: "Let me reason through this carefully...",
		},
	}
	result, err := Execute(context.Background(), Config{Provider: p}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Thinking != "Let me reason through this carefully..." {
		t.Errorf("Thinking: want %q, got %q", "Let me reason through this carefully...", result.Thinking)
	}
	if result.Content != "The answer is 42." {
		t.Errorf("Content: want %q, got %q", "The answer is 42.", result.Content)
	}
}

// TestExecute_ThinkingInTranscript verifies transcript entries include thinking from provider response.
func TestExecute_ThinkingInTranscript(t *testing.T) {
	p := &mockProvider{
		name: "mock",
		chatResponse: &provider.Response{
			Content:  "Done.",
			Thinking: "Step-by-step reasoning here.",
		},
	}
	recorder := &recordingTranscript{}
	result, err := Execute(context.Background(), Config{
		Provider:   p,
		Transcript: recorder,
	}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Thinking != "Step-by-step reasoning here." {
		t.Errorf("Result.Thinking: want %q, got %q", "Step-by-step reasoning here.", result.Thinking)
	}

	var found bool
	for _, entry := range recorder.entries {
		if entry.Role == provider.RoleAssistant && entry.Thinking == "Step-by-step reasoning here." {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected an assistant transcript entry with Thinking populated")
	}
}

// recordingTranscript captures all transcript entries for inspection.
type recordingTranscript struct {
	entries []TranscriptEntry
}

func (r *recordingTranscript) Record(_ context.Context, entry TranscriptEntry) error {
	r.entries = append(r.entries, entry)
	return nil
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

// TestExecute_ActionAsk_NoApprover_Denies verifies that when no Approver is configured,
// a trust ActionAsk tool call is denied rather than auto-approved (fail-safe behavior).
func TestExecute_ActionAsk_NoApprover_Denies(t *testing.T) {
	var toolExecuted bool
	reg := tools.NewRegistry()
	reg.Register(&simpleTool{
		name: "risky_tool",
		def:  provider.ToolDef{Name: "risky_tool", Description: "risky"},
		fn: func(_ context.Context, _ map[string]any) (any, error) {
			toolExecuted = true
			return "executed", nil
		},
	})

	callN := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			callN++
			if callN == 1 {
				return &provider.Response{
					ToolCalls: []provider.ToolCall{
						{ID: "tc-1", Name: "risky_tool", Arguments: map[string]any{}},
					},
				}, nil
			}
			return &provider.Response{Content: "done"}, nil
		},
	}

	// TrustEngine returns Ask for risky_tool; Approver is nil (not configured).
	te := &mockTrustEvaluator{toolAction: ActionAsk}
	cfg := Config{
		Provider:     p,
		ToolRegistry: reg,
		TrustEngine:  te,
		// Approver intentionally nil — should default to deny behavior
	}

	_, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if toolExecuted {
		t.Error("risky_tool should NOT have been executed when no Approver is configured and trust policy is ask")
	}
}

// TestExecute_ActionAsk_WithApprover_Blocks verifies that when an Approver is configured,
// a trust ActionAsk call blocks until the Approver resolves it.
func TestExecute_ActionAsk_WithApprover_Blocks(t *testing.T) {
	var toolExecuted bool
	var requestReceived bool
	reg := tools.NewRegistry()
	reg.Register(&simpleTool{
		name: "reviewed_tool",
		def:  provider.ToolDef{Name: "reviewed_tool", Description: "needs approval"},
		fn: func(_ context.Context, _ map[string]any) (any, error) {
			toolExecuted = true
			return "executed", nil
		},
	})

	callN := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			callN++
			if callN == 1 {
				return &provider.Response{
					ToolCalls: []provider.ToolCall{
						{ID: "tc-1", Name: "reviewed_tool", Arguments: map[string]any{}},
					},
				}, nil
			}
			return &provider.Response{Content: "done"}, nil
		},
	}

	approver := &recordingApprover{status: ApprovalApproved}
	te := &mockTrustEvaluator{toolAction: ActionAsk}
	cfg := Config{
		Provider:     p,
		ToolRegistry: reg,
		TrustEngine:  te,
		Approver:     approver,
	}

	_, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	requestReceived = approver.requested
	if !requestReceived {
		t.Error("Approver.Request should have been called for ActionAsk")
	}
	if !toolExecuted {
		t.Error("reviewed_tool should have been executed after approval")
	}
}

// recordingApprover records whether Request was called and auto-resolves with a fixed status.
type recordingApprover struct {
	requested bool
	status    ApprovalStatus
}

func (r *recordingApprover) Request(_ context.Context, _ string, _ map[string]any) (string, error) {
	r.requested = true
	return "test-approval-id", nil
}

func (r *recordingApprover) WaitForResolution(_ context.Context, _ string, _ time.Duration) (*ApprovalRecord, error) {
	return &ApprovalRecord{Status: r.status}, nil
}
