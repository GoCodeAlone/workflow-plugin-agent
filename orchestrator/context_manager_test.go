package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// --- lookupContextLimit ---

func TestLookupContextLimit_KnownModels(t *testing.T) {
	cases := []struct {
		name    string
		wantMin int // we just check it is at or above this
	}{
		{"claude-sonnet-4-5", 200_000},
		{"claude-opus-4-6", 200_000},
		{"gpt-4o", 128_000},
		{"gpt-4-turbo-preview", 128_000},
		{"gpt-3.5-turbo", 4_096},
		{"o1-preview", 128_000},
	}
	for _, tc := range cases {
		got := lookupContextLimit(tc.name)
		if got < tc.wantMin {
			t.Errorf("lookupContextLimit(%q) = %d, want >= %d", tc.name, got, tc.wantMin)
		}
	}
}

func TestLookupContextLimit_Unknown(t *testing.T) {
	got := lookupContextLimit("totally-unknown-model-xyz")
	if got != defaultContextLimit {
		t.Errorf("unknown model: expected defaultContextLimit %d, got %d", defaultContextLimit, got)
	}
}

func TestLookupContextLimit_CaseInsensitive(t *testing.T) {
	lower := lookupContextLimit("GPT-4O")
	upper := lookupContextLimit("gpt-4o")
	if lower != upper {
		t.Errorf("case sensitivity mismatch: %d vs %d", lower, upper)
	}
}

// --- EstimateTokens ---

func TestEstimateTokens_Empty(t *testing.T) {
	n := EstimateTokens(nil)
	if n != 0 {
		t.Errorf("empty messages: expected 0, got %d", n)
	}
}

func TestEstimateTokens_SingleMessage(t *testing.T) {
	// 100-char content → ~25 tokens + 4 overhead = ~29
	msg := provider.Message{Role: provider.RoleUser, Content: strings.Repeat("a", 100)}
	n := EstimateTokens([]provider.Message{msg})
	if n <= 0 {
		t.Errorf("expected positive token count, got %d", n)
	}
}

func TestEstimateTokens_GrowsWithContent(t *testing.T) {
	short := []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	long := []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("x", 10000)}}
	if EstimateTokens(short) >= EstimateTokens(long) {
		t.Error("longer content should produce more tokens")
	}
}

// --- NewContextManager ---

func TestNewContextManager_DefaultLimit(t *testing.T) {
	cm := NewContextManager("some-unknown-model", 0)
	if cm.ContextLimitTokens() != defaultContextLimit {
		t.Errorf("expected default limit %d, got %d", defaultContextLimit, cm.ContextLimitTokens())
	}
}

func TestNewContextManager_KnownProvider(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)
	if cm.ContextLimitTokens() < 128_000 {
		t.Errorf("gpt-4o: expected >= 128k, got %d", cm.ContextLimitTokens())
	}
}

func TestNewContextManager_ModernModels(t *testing.T) {
	cases := []struct {
		name      string
		wantMin   int
	}{
		{"gemma4:e2b", 131_072},
		{"gemma3:27b", 131_072},
		{"qwen2.5:7b", 32_768},
		{"qwen3:14b", 131_072},
		{"phi4", 16_384},
		{"llama3.3:70b", 131_072},
	}
	for _, tc := range cases {
		cm := NewContextManager(tc.name, 0)
		if cm.ContextLimitTokens() < tc.wantMin {
			t.Errorf("model %q: expected >= %d, got %d", tc.name, tc.wantMin, cm.ContextLimitTokens())
		}
	}
}

func TestSetModelLimitFromProvider(t *testing.T) {
	cm := NewContextManager("unknown-model", 0)
	before := cm.ContextLimitTokens()

	cm.SetModelLimitFromProvider(16_384)
	if cm.ContextLimitTokens() != 16_384 {
		t.Errorf("expected 16384 after SetModelLimitFromProvider, got %d", cm.ContextLimitTokens())
	}

	// Zero should be ignored.
	cm.SetModelLimitFromProvider(0)
	if cm.ContextLimitTokens() != 16_384 {
		t.Errorf("SetModelLimitFromProvider(0) should be no-op, got %d", cm.ContextLimitTokens())
	}

	_ = before // used only to show the value changed
}

// --- NeedsCompaction ---

func TestNeedsCompaction_BelowThreshold(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0) // 128k limit
	// A tiny message array is far below 80%
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are an agent."},
		{Role: provider.RoleUser, Content: "Hello"},
	}
	if cm.NeedsCompaction(msgs) {
		t.Error("tiny conversation should not need compaction")
	}
}

func TestNeedsCompaction_AboveThreshold(t *testing.T) {
	// Use a very small artificial limit so we can trigger it easily.
	cm := &ContextManager{
		contextLimit: 100,
		threshold:    0.80,
	}
	// Fill with content that clearly exceeds 80 tokens
	msgs := make([]provider.Message, 20)
	for i := range msgs {
		msgs[i] = provider.Message{Role: provider.RoleUser, Content: strings.Repeat("x", 50)}
	}
	if !cm.NeedsCompaction(msgs) {
		t.Error("large conversation should need compaction")
	}
}

// --- TokenUsage ---

func TestTokenUsage(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "hello world"},
	}
	est, limit := cm.TokenUsage(msgs)
	if est <= 0 {
		t.Errorf("estimated tokens should be positive, got %d", est)
	}
	if limit != cm.ContextLimitTokens() {
		t.Errorf("limit mismatch: got %d, want %d", limit, cm.ContextLimitTokens())
	}
}

// --- Compact ---

// mockSummaryProvider returns a fixed summary string for any chat request.
type mockSummaryProvider struct {
	summary string
	calls   int
}

func (m *mockSummaryProvider) Name() string { return "mock-summary" }
func (m *mockSummaryProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	m.calls++
	return &provider.Response{Content: m.summary}, nil
}
func (m *mockSummaryProvider) Stream(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}
func (m *mockSummaryProvider) AuthModeInfo() provider.AuthModeInfo {
	return provider.AuthModeInfo{Mode: "none", DisplayName: "Mock summary"}
}

func TestCompact_ShortConversation(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system prompt"},
		{Role: provider.RoleUser, Content: "user msg"},
	}
	mock := &mockSummaryProvider{summary: "summary text"}
	result := cm.Compact(context.Background(), msgs, mock)
	// Too short to compact — should return original unchanged
	if len(result) != len(msgs) {
		t.Errorf("short conversation should not be compacted: got %d msgs, want %d", len(result), len(msgs))
	}
	if mock.calls != 0 {
		t.Error("LLM should not be called for short conversations")
	}
}

func TestCompact_LongConversation(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)

	// Build a long conversation: system + many middle turns + tail
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helpful agent."},
	}
	for i := 0; i < 10; i++ {
		msgs = append(msgs,
			provider.Message{Role: provider.RoleUser, Content: "user turn"},
			provider.Message{Role: provider.RoleAssistant, Content: "assistant turn"},
		)
	}

	mock := &mockSummaryProvider{summary: "Key findings: tool X returned Y."}
	result := cm.Compact(context.Background(), msgs, mock)

	// Result should be shorter than original
	if len(result) >= len(msgs) {
		t.Errorf("compacted messages (%d) should be fewer than original (%d)", len(result), len(msgs))
	}

	// First message should be the system prompt
	if result[0].Role != provider.RoleSystem {
		t.Errorf("first message should be system, got %s", result[0].Role)
	}

	// Second message should be the summary note
	if result[1].Role != provider.RoleUser {
		t.Errorf("second message should be user (summary note), got %s", result[1].Role)
	}
	if !strings.Contains(result[1].Content, "Key findings") {
		t.Errorf("summary note should contain LLM summary, got: %s", result[1].Content)
	}
	if !strings.Contains(result[1].Content, "CONTEXT COMPACTED") {
		t.Errorf("summary note should contain compaction header, got: %s", result[1].Content)
	}

	// LLM should have been called once for summarisation
	if mock.calls != 1 {
		t.Errorf("expected 1 LLM summarisation call, got %d", mock.calls)
	}

	// Compaction counter should increment
	if cm.Compactions() != 1 {
		t.Errorf("expected 1 compaction, got %d", cm.Compactions())
	}
}

func TestCompact_PreservesSystemAndTail(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)
	systemContent := "You are a very specific agent."
	tailContent := "latest tool result"

	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: systemContent},
	}
	// Middle messages
	for i := 0; i < 8; i++ {
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: "middle"})
	}
	// Tail messages
	msgs = append(msgs,
		provider.Message{Role: provider.RoleAssistant, Content: "assistant final"},
		provider.Message{Role: provider.RoleTool, Content: tailContent},
		provider.Message{Role: provider.RoleAssistant, Content: "second final"},
		provider.Message{Role: provider.RoleTool, Content: "second tool"},
	)

	mock := &mockSummaryProvider{summary: "summary"}
	result := cm.Compact(context.Background(), msgs, mock)

	// System prompt preserved
	if result[0].Content != systemContent {
		t.Errorf("system content changed: got %q", result[0].Content)
	}

	// Tail content preserved in last messages
	found := false
	for _, m := range result {
		if strings.Contains(m.Content, tailContent) {
			found = true
			break
		}
	}
	if !found {
		t.Error("tail content should be preserved after compaction")
	}
}

func TestCompact_LLMFailureFallback(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)

	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
	}
	for i := 0; i < 8; i++ {
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: "middle msg"})
	}
	msgs = append(msgs,
		provider.Message{Role: provider.RoleAssistant, Content: "tail1"},
		provider.Message{Role: provider.RoleTool, Content: "tail2"},
	)

	// Provider that always errors
	errProvider := &errorProvider{}
	result := cm.Compact(context.Background(), msgs, errProvider)

	// Should still compact (with fallback summary)
	if len(result) >= len(msgs) {
		t.Error("should still compact even if LLM summary fails")
	}
	// Summary note should exist with fallback text
	if !strings.Contains(result[1].Content, "auto-summary unavailable") {
		t.Errorf("fallback message expected, got: %s", result[1].Content)
	}
}

type errorProvider struct{}

func (e *errorProvider) Name() string { return "error-provider" }
func (e *errorProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	return nil, fmt.Errorf("provider error")
}
func (e *errorProvider) Stream(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	return nil, fmt.Errorf("provider error")
}
func (e *errorProvider) AuthModeInfo() provider.AuthModeInfo {
	return provider.AuthModeInfo{Mode: "none", DisplayName: "Error provider"}
}

func TestCompact_MultipleCompactions(t *testing.T) {
	cm := NewContextManager("gpt-4o", 0)
	mock := &mockSummaryProvider{summary: "summary"}

	msgs := make([]provider.Message, 0, 15)
	msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: "system"})
	for i := 0; i < 14; i++ {
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: "turn"})
	}

	result1 := cm.Compact(context.Background(), msgs, mock)
	result2 := cm.Compact(context.Background(), result1, mock)

	if cm.Compactions() != 2 {
		t.Errorf("expected 2 compactions, got %d", cm.Compactions())
	}
	// Second compaction note should mention compaction #2
	if !strings.Contains(result2[1].Content, "compaction #2") {
		t.Errorf("second compaction should be labelled #2, got: %s", result2[1].Content)
	}
}
