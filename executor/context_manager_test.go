package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// TestNewContextManager_DefaultThreshold verifies default compaction threshold is 0.80.
func TestNewContextManager_DefaultThreshold(t *testing.T) {
	cm := NewContextManager("claude-sonnet-4", 0)
	if cm.threshold != defaultCompactionThreshold {
		t.Errorf("threshold: want %v, got %v", defaultCompactionThreshold, cm.threshold)
	}
}

// TestNewContextManager_KnownModel verifies known model limits are resolved.
func TestNewContextManager_KnownModel(t *testing.T) {
	cm := NewContextManager("claude-sonnet-4-20250514", 0)
	if cm.contextLimit != 200_000 {
		t.Errorf("contextLimit for claude-sonnet-4: want 200000, got %d", cm.contextLimit)
	}
}

// TestNewContextManager_UnknownModel falls back to default limit.
func TestNewContextManager_UnknownModel(t *testing.T) {
	cm := NewContextManager("some-unknown-model-v99", 0)
	if cm.contextLimit != defaultContextLimit {
		t.Errorf("contextLimit for unknown model: want %d, got %d", defaultContextLimit, cm.contextLimit)
	}
}

// TestContextManager_SetModelLimit allows overriding context limit per instance.
func TestContextManager_SetModelLimit(t *testing.T) {
	cm := NewContextManager("my-custom-model", 0)
	initial := cm.contextLimit

	cm.SetModelLimit("my-custom-model", 50_000)
	if cm.contextLimit != 50_000 {
		t.Errorf("after SetModelLimit: want 50000, got %d", cm.contextLimit)
	}
	// Verify initial was defaultContextLimit, not 50000.
	if initial == 50_000 {
		t.Error("initial limit should not have been 50000 before SetModelLimit")
	}
}

// TestEstimateTokens verifies rough token estimation scales with content size.
func TestEstimateTokens(t *testing.T) {
	empty := []provider.Message{}
	if EstimateTokens(empty) != 0 {
		t.Errorf("expected 0 tokens for empty message slice")
	}

	short := []provider.Message{{Role: provider.RoleUser, Content: "Hi"}}
	long := []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("word ", 100)}}

	shortTokens := EstimateTokens(short)
	longTokens := EstimateTokens(long)
	if longTokens <= shortTokens {
		t.Errorf("expected more tokens for longer message: short=%d long=%d", shortTokens, longTokens)
	}
}

// TestContextManager_NeedsCompaction triggers when estimated tokens exceed threshold.
func TestContextManager_NeedsCompaction(t *testing.T) {
	// Use a tiny limit to force compaction easily.
	cm := NewContextManager("gpt-3.5-turbo", 1.0)
	cm.SetModelLimit("gpt-3.5-turbo", 100)

	// Short messages should not trigger compaction.
	short := []provider.Message{
		{Role: provider.RoleSystem, Content: "hi"},
		{Role: provider.RoleUser, Content: "hello"},
	}
	if cm.NeedsCompaction(short) {
		t.Error("expected no compaction needed for short messages")
	}

	// Large messages should trigger compaction.
	bigContent := strings.Repeat("x", 500)
	large := []provider.Message{
		{Role: provider.RoleSystem, Content: bigContent},
		{Role: provider.RoleUser, Content: bigContent},
	}
	if !cm.NeedsCompaction(large) {
		t.Error("expected compaction needed for large messages with tiny limit")
	}
}

// TestContextManager_TokenUsage returns consistent estimates.
func TestContextManager_TokenUsage(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system prompt"},
		{Role: provider.RoleUser, Content: "user message"},
	}
	estimated, limit := cm.TokenUsage(msgs)
	if estimated <= 0 {
		t.Errorf("expected positive estimated tokens, got %d", estimated)
	}
	if limit != 128_000 {
		t.Errorf("expected gpt-4o limit=128000, got %d", limit)
	}
}

// TestContextManager_CompactTooFewMessages returns original slice unchanged.
func TestContextManager_CompactTooFewMessages(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "msg"},
	}
	mockP := &mockProvider{chatResponse: &provider.Response{Content: "summary"}}
	result := cm.Compact(context.Background(), msgs, mockP)
	if len(result) != len(msgs) {
		t.Errorf("expected original messages returned for small slice, got %d messages", len(result))
	}
}

// TestContextManager_CompactReducesMessageCount verifies compaction shrinks messages.
func TestContextManager_CompactReducesMessageCount(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)
	msgs := make([]provider.Message, 10)
	for i := range msgs {
		msgs[i] = provider.Message{Role: provider.RoleUser, Content: strings.Repeat("word ", 20)}
	}
	msgs[0].Role = provider.RoleSystem

	mockP := &mockProvider{chatResponse: &provider.Response{Content: "concise summary"}}
	result := cm.Compact(context.Background(), msgs, mockP)
	if len(result) >= len(msgs) {
		t.Errorf("expected compacted messages to be fewer than %d, got %d", len(msgs), len(result))
	}
	if cm.Compactions() != 1 {
		t.Errorf("expected 1 compaction, got %d", cm.Compactions())
	}
}

// TestContextManager_CompactFallbackOnProviderError uses fallback when LLM fails.
func TestContextManager_CompactFallbackOnProviderError(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)
	msgs := make([]provider.Message, 8)
	for i := range msgs {
		msgs[i] = provider.Message{Role: provider.RoleUser, Content: "some content"}
	}
	msgs[0].Role = provider.RoleSystem

	failP := &mockProvider{chatErr: fmt.Errorf("provider unavailable")}
	// Should not panic, should return a compacted slice with fallback summary.
	result := cm.Compact(context.Background(), msgs, failP)
	if len(result) == 0 {
		t.Error("expected non-empty result even when provider fails")
	}
}

// TestLookupContextLimit_GPT4Turbo verifies specific model resolution.
func TestLookupContextLimit_GPT4Turbo(t *testing.T) {
	limit := lookupContextLimit("gpt-4-turbo-2024-04-09")
	if limit != 128_000 {
		t.Errorf("gpt-4-turbo: want 128000, got %d", limit)
	}
}

// TestLookupContextLimit_Claude3Haiku verifies Claude haiku resolution.
func TestLookupContextLimit_Claude3Haiku(t *testing.T) {
	limit := lookupContextLimit("claude-3-haiku-20240307")
	if limit != 200_000 {
		t.Errorf("claude-3-haiku: want 200000, got %d", limit)
	}
}

// mockProvider is a minimal provider.Provider for testing.
type mockProvider struct {
	name         string
	chatResponse *provider.Response
	chatErr      error
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	return m.chatResponse, m.chatErr
}

func (m *mockProvider) Stream(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

func (m *mockProvider) AuthModeInfo() provider.AuthModeInfo {
	return provider.AuthModeInfo{}
}
