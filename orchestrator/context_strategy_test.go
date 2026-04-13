package orchestrator

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// trackingProvider records the messages it receives on each Chat call.
type trackingProvider struct {
	calls    [][]provider.Message
	response string
}

func (p *trackingProvider) Name() string { return "tracking" }
func (p *trackingProvider) Chat(_ context.Context, messages []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	cp := make([]provider.Message, len(messages))
	copy(cp, messages)
	p.calls = append(p.calls, cp)
	return &provider.Response{Content: p.response}, nil
}
func (p *trackingProvider) Stream(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	return nil, nil
}
func (p *trackingProvider) AuthModeInfo() provider.AuthModeInfo { return provider.AuthModeInfo{} }

// statefulTrackingProvider adds ContextStrategy to trackingProvider.
type statefulTrackingProvider struct {
	trackingProvider
	resetCount int
}

func (p *statefulTrackingProvider) ManagesContext() bool { return true }
func (p *statefulTrackingProvider) ResetContext(_ context.Context) error {
	p.resetCount++
	return nil
}

func TestContextStrategy_DeltaMessages(t *testing.T) {
	// Simulate the delta-message logic used in step_agent_execute.
	p := &statefulTrackingProvider{trackingProvider: trackingProvider{response: "done"}}

	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "hello"},
	}

	var cs provider.ContextStrategy
	cs, ok := provider.Provider(p).(provider.ContextStrategy)
	if !ok || !cs.ManagesContext() {
		t.Fatal("expected statefulTrackingProvider to implement ContextStrategy")
	}

	lastSentIndex := 0

	// First call — sends full history.
	_, _ = p.Chat(context.Background(), messages[lastSentIndex:], nil)
	lastSentIndex = len(messages)

	// Append two more messages (simulating a tool call round-trip).
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, Content: "calling tool"},
		provider.Message{Role: provider.RoleTool, Content: "tool result"},
	)

	// Second call — should send only the two new messages.
	_, _ = p.Chat(context.Background(), messages[lastSentIndex:], nil)
	lastSentIndex = len(messages)

	if len(p.calls) != 2 {
		t.Fatalf("expected 2 Chat calls, got %d", len(p.calls))
	}
	// First call: 2 messages (system + user).
	if len(p.calls[0]) != 2 {
		t.Errorf("first call: expected 2 messages, got %d", len(p.calls[0]))
	}
	// Second call: 2 new messages (assistant + tool).
	if len(p.calls[1]) != 2 {
		t.Errorf("second call: expected 2 delta messages, got %d", len(p.calls[1]))
	}
	if p.calls[1][0].Role != provider.RoleAssistant {
		t.Errorf("delta first msg: expected assistant, got %s", p.calls[1][0].Role)
	}
}

func TestContextStrategy_StatelessReceivesFullHistory(t *testing.T) {
	// Stateless provider should receive the full message slice every call.
	p := &trackingProvider{response: "done"}

	// Verify it does NOT implement ContextStrategy.
	if _, ok := provider.Provider(p).(provider.ContextStrategy); ok {
		t.Fatal("trackingProvider should not implement ContextStrategy")
	}

	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "hello"},
	}

	// Simulate two iterations with full history each time.
	_, _ = p.Chat(context.Background(), messages, nil)
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, Content: "reply"},
	)
	_, _ = p.Chat(context.Background(), messages, nil)

	if len(p.calls[0]) != 2 {
		t.Errorf("first call: expected 2 messages, got %d", len(p.calls[0]))
	}
	if len(p.calls[1]) != 3 {
		t.Errorf("second call: expected 3 messages (full history), got %d", len(p.calls[1]))
	}
}

func TestContextStrategy_ResetOnCompaction(t *testing.T) {
	p := &statefulTrackingProvider{trackingProvider: trackingProvider{response: "done"}}
	cs := provider.ContextStrategy(p)

	// Simulate compaction: reset context, then set lastSentIndex=0 so full history resent.
	if err := cs.ResetContext(context.Background()); err != nil {
		t.Fatalf("ResetContext error: %v", err)
	}
	if p.resetCount != 1 {
		t.Errorf("expected resetCount=1, got %d", p.resetCount)
	}

	// After reset, the next call should receive the full compacted history (index=0).
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "[CONTEXT COMPACTED] summary"},
	}
	lastSentIndex := 0 // reset to 0 as the executor does after compaction
	_, _ = p.Chat(context.Background(), messages[lastSentIndex:], nil)

	if len(p.calls[0]) != 2 {
		t.Errorf("post-compaction call: expected full 2 messages, got %d", len(p.calls[0]))
	}
}
