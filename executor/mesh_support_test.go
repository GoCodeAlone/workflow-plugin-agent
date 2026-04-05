package executor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow-plugin-agent/tools"
)

// TestMeshSupport_EndToEnd simulates a mesh scenario: a scripted provider, inbox
// channel with delayed messages, OnEvent collector, and ShouldStop after seeing
// a specific condition. It verifies inbox messages appear in conversation, events
// stream in correct order, and early termination works.
func TestMeshSupport_EndToEnd(t *testing.T) {
	inbox := make(chan provider.Message, 5)

	var mu sync.Mutex
	var events []Event

	// Track which iteration we're on for ShouldStop.
	var toolCallCount int
	var toolCallMu sync.Mutex

	// Create a tool that the agent will call.
	reg := tools.NewRegistry()
	reg.Register(&simpleTool{
		name: "status_update",
		def:  provider.ToolDef{Name: "status_update", Description: "update agent status"},
		fn: func(_ context.Context, args map[string]any) (any, error) {
			toolCallMu.Lock()
			toolCallCount++
			toolCallMu.Unlock()
			return map[string]any{"updated": true, "status": args["status"]}, nil
		},
	})

	// Scripted provider: iteration 1 makes a tool call, iteration 2 makes a tool call,
	// iteration 3 should never happen because ShouldStop triggers after iteration 2.
	callN := 0
	var capturedMessages [][]provider.Message
	p := &captureProvider{
		onChat: func(msgs []provider.Message) (*provider.Response, error) {
			callN++
			mu.Lock()
			capturedMessages = append(capturedMessages, append([]provider.Message{}, msgs...))
			mu.Unlock()

			switch callN {
			case 1:
				return &provider.Response{
					Content:  "Starting work",
					Thinking: "I need to update status first",
					ToolCalls: []provider.ToolCall{
						{ID: "tc-1", Name: "status_update", Arguments: map[string]any{"status": "working"}},
					},
				}, nil
			case 2:
				return &provider.Response{
					Content: "Processing inbox message",
					ToolCalls: []provider.ToolCall{
						{ID: "tc-2", Name: "status_update", Arguments: map[string]any{"status": "done"}},
					},
				}, nil
			default:
				return &provider.Response{Content: "Unexpected call"}, nil
			}
		},
	}

	cfg := Config{
		Provider:      p,
		ToolRegistry:  reg,
		MaxIterations: 10,
		Inbox:         inbox,
		OnEvent: func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		},
		ShouldStop: func() string {
			toolCallMu.Lock()
			defer toolCallMu.Unlock()
			if toolCallCount >= 2 {
				return "agent completed all work"
			}
			return ""
		},
	}

	// Inject a message before execution to simulate mesh routing. The message
	// is buffered and will be drained at the start of the first iteration.
	inbox <- provider.Message{
		Role:    provider.RoleUser,
		Content: "Mesh message from Agent B: please finish up",
	}

	result, err := Execute(context.Background(), cfg, "You are a mesh agent.", "Do the task.", "mesh-agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// 1. Verify early termination via ShouldStop.
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
	if result.Content != "agent completed all work" {
		t.Errorf("Content: want %q, got %q", "agent completed all work", result.Content)
	}
	if result.Iterations > 3 {
		t.Errorf("Iterations: want <= 3, got %d", result.Iterations)
	}

	// 2. Verify events were emitted in a reasonable order.
	mu.Lock()
	eventsCopy := append([]Event{}, events...)
	mu.Unlock()

	if len(eventsCopy) == 0 {
		t.Fatal("expected events to be emitted")
	}

	// First event should be iteration.
	if eventsCopy[0].Type != EventIteration {
		t.Errorf("first event: want %q, got %q", EventIteration, eventsCopy[0].Type)
	}

	// Last event should be completed (from ShouldStop).
	lastEvent := eventsCopy[len(eventsCopy)-1]
	if lastEvent.Type != EventCompleted {
		t.Errorf("last event: want %q, got %q", EventCompleted, lastEvent.Type)
	}

	// Should contain tool_call_start and tool_call_result events.
	var hasToolStart, hasToolResult, hasThinking, hasText bool
	for _, e := range eventsCopy {
		switch e.Type {
		case EventToolCallStart:
			hasToolStart = true
		case EventToolCallResult:
			hasToolResult = true
		case EventThinking:
			hasThinking = true
		case EventText:
			hasText = true
		}
	}
	if !hasToolStart {
		t.Error("expected at least one tool_call_start event")
	}
	if !hasToolResult {
		t.Error("expected at least one tool_call_result event")
	}
	if !hasThinking {
		t.Error("expected at least one thinking event")
	}
	if !hasText {
		t.Error("expected at least one text event")
	}

	// 3. Verify the inbox message appeared in the conversation at some point.
	mu.Lock()
	capturedCopy := capturedMessages
	mu.Unlock()

	inboxMsgFound := false
	for _, msgs := range capturedCopy {
		for _, msg := range msgs {
			if strings.Contains(msg.Content, "Mesh message from Agent B") {
				inboxMsgFound = true
				break
			}
		}
		if inboxMsgFound {
			break
		}
	}
	if !inboxMsgFound {
		t.Error("expected inbox message from Agent B to appear in conversation")
	}
}

// TestMeshSupport_InboxClosedMidExecution verifies the executor handles a closed inbox gracefully.
func TestMeshSupport_InboxClosedMidExecution(t *testing.T) {
	inbox := make(chan provider.Message, 2)
	inbox <- provider.Message{Role: provider.RoleUser, Content: "last message"}
	close(inbox)

	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "ok"},
	}

	result, err := Execute(context.Background(), Config{
		Provider: p,
		Inbox:    inbox,
	}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
}

// TestMeshSupport_EmptyInbox verifies that an empty inbox doesn't affect execution.
func TestMeshSupport_EmptyInbox(t *testing.T) {
	inbox := make(chan provider.Message, 5) // Empty.

	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "ok"},
	}

	result, err := Execute(context.Background(), Config{
		Provider: p,
		Inbox:    inbox,
	}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
}

// TestMeshSupport_ShouldStopWithNoToolCalls verifies ShouldStop isn't called when
// there are no tool calls (the loop naturally exits via the break).
func TestMeshSupport_ShouldStopWithNoToolCalls(t *testing.T) {
	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "immediate answer"},
	}

	shouldStopCalled := false
	result, err := Execute(context.Background(), Config{
		Provider: p,
		ShouldStop: func() string {
			shouldStopCalled = true
			return "should not trigger"
		},
	}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
	if result.Content != "immediate answer" {
		t.Errorf("Content: want %q, got %q", "immediate answer", result.Content)
	}
	if shouldStopCalled {
		t.Error("ShouldStop should not be called when there are no tool calls")
	}
}

// TestMeshSupport_EventAgentIDPropagation verifies AgentID is set on all events.
func TestMeshSupport_EventAgentIDPropagation(t *testing.T) {
	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "done"},
	}

	var events []Event
	_, err := Execute(context.Background(), Config{
		Provider: p,
		OnEvent: func(e Event) {
			events = append(events, e)
		},
	}, "sys", "task", "my-agent-42")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for i, e := range events {
		if e.AgentID != "my-agent-42" {
			t.Errorf("event[%d] (type=%s): AgentID want %q, got %q", i, e.Type, "my-agent-42", e.AgentID)
		}
	}
}

// TestMeshSupport_MultipleInboxMessages verifies multiple inbox messages are all drained.
func TestMeshSupport_MultipleInboxMessages(t *testing.T) {
	inbox := make(chan provider.Message, 5)
	inbox <- provider.Message{Role: provider.RoleUser, Content: "msg-1-from-mesh"}
	inbox <- provider.Message{Role: provider.RoleUser, Content: "msg-2-from-mesh"}
	inbox <- provider.Message{Role: provider.RoleUser, Content: "msg-3-from-mesh"}

	var capturedMessages []provider.Message
	p := &captureProvider{
		onChat: func(msgs []provider.Message) (*provider.Response, error) {
			capturedMessages = msgs
			return &provider.Response{Content: "ok"}, nil
		},
	}

	result, err := Execute(context.Background(), Config{
		Provider: p,
		Inbox:    inbox,
	}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}

	// All three messages should appear.
	foundCount := 0
	for _, msg := range capturedMessages {
		if strings.HasPrefix(msg.Content, "msg-") && strings.HasSuffix(msg.Content, "-from-mesh") {
			foundCount++
		}
	}
	if foundCount != 3 {
		t.Errorf("expected 3 inbox messages in conversation, found %d", foundCount)
	}
}

// TestMeshSupport_InboxTranscriptRecording verifies inbox messages are recorded in the transcript.
func TestMeshSupport_InboxTranscriptRecording(t *testing.T) {
	inbox := make(chan provider.Message, 1)
	inbox <- provider.Message{Role: provider.RoleUser, Content: "external-mesh-msg"}

	p := &mockProvider{
		name:         "mock",
		chatResponse: &provider.Response{Content: "ok"},
	}

	recorder := &recordingTranscript{}
	_, err := Execute(context.Background(), Config{
		Provider:   p,
		Inbox:      inbox,
		Transcript: recorder,
	}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	found := false
	for _, entry := range recorder.entries {
		if entry.Content == "external-mesh-msg" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected inbox message to be recorded in transcript")
	}
}

// TestMeshSupport_ShouldStopReasonEmptyNoStop verifies ShouldStop returning empty string does not stop.
func TestMeshSupport_ShouldStopReasonEmptyNoStop(t *testing.T) {
	callN := 0
	p := &callCountProvider{
		onChat: func() (*provider.Response, error) {
			callN++
			if callN <= 3 {
				return &provider.Response{
					ToolCalls: []provider.ToolCall{
						{ID: fmt.Sprintf("tc-%d", callN), Name: "noop", Arguments: map[string]any{"n": callN}},
					},
				}, nil
			}
			return &provider.Response{Content: "final"}, nil
		},
	}

	reg := tools.NewRegistry()
	reg.Register(&simpleTool{
		name: "noop",
		def:  provider.ToolDef{Name: "noop", Description: "no-op"},
		fn: func(_ context.Context, _ map[string]any) (any, error) {
			return "ok", nil
		},
	})

	shouldStopCalls := 0
	result, err := Execute(context.Background(), Config{
		Provider:      p,
		ToolRegistry:  reg,
		MaxIterations: 10,
		ShouldStop: func() string {
			shouldStopCalls++
			return "" // Never stop.
		},
	}, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status: want completed, got %q", result.Status)
	}
	if result.Content != "final" {
		t.Errorf("Content: want %q, got %q", "final", result.Content)
	}
	// ShouldStop should have been called for each iteration with tool calls (3 times).
	if shouldStopCalls != 3 {
		t.Errorf("ShouldStop calls: want 3, got %d", shouldStopCalls)
	}
}
