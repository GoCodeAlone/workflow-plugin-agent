package provider

import (
	"testing"
	"time"

	ollamaapi "github.com/ollama/ollama/api"
)

// --- toOllamaMessages ---

func TestToOllamaMessages_Roles(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
		{Role: RoleTool, Content: "tool result", ToolCallID: "tc1"},
	}
	got := toOllamaMessages(msgs)
	if len(got) != 4 {
		t.Fatalf("len=%d, want 4", len(got))
	}
	if got[0].Role != "system" || got[0].Content != "sys" {
		t.Errorf("system msg wrong: %+v", got[0])
	}
	if got[1].Role != "user" || got[1].Content != "hi" {
		t.Errorf("user msg wrong: %+v", got[1])
	}
	if got[2].Role != "assistant" || got[2].Content != "hello" {
		t.Errorf("assistant msg wrong: %+v", got[2])
	}
	if got[3].Role != "tool" || got[3].ToolCallID != "tc1" {
		t.Errorf("tool msg wrong: %+v", got[3])
	}
}

func TestToOllamaMessages_ToolCalls(t *testing.T) {
	msgs := []Message{
		{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{
				{ID: "id1", Name: "search", Arguments: map[string]any{"q": "hello"}},
			},
		},
	}
	got := toOllamaMessages(msgs)
	if len(got[0].ToolCalls) != 1 {
		t.Fatalf("tool calls len=%d, want 1", len(got[0].ToolCalls))
	}
	tc := got[0].ToolCalls[0]
	if tc.ID != "id1" {
		t.Errorf("ID=%q, want %q", tc.ID, "id1")
	}
	if tc.Function.Name != "search" {
		t.Errorf("Name=%q, want %q", tc.Function.Name, "search")
	}
	v, ok := tc.Function.Arguments.Get("q")
	if !ok || v != "hello" {
		t.Errorf("arg q=%v ok=%v, want 'hello'", v, ok)
	}
}

// --- toOllamaTools ---

func TestToOllamaTools(t *testing.T) {
	tools := []ToolDef{
		{
			Name:        "calculator",
			Description: "does math",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"expr": map[string]any{"type": "string"},
				},
				"required": []any{"expr"},
			},
		},
	}
	got := toOllamaTools(tools)
	if len(got) != 1 {
		t.Fatalf("len=%d, want 1", len(got))
	}
	if got[0].Type != "function" {
		t.Errorf("Type=%q, want 'function'", got[0].Type)
	}
	if got[0].Function.Name != "calculator" {
		t.Errorf("Name=%q", got[0].Function.Name)
	}
	if got[0].Function.Description != "does math" {
		t.Errorf("Description=%q", got[0].Function.Description)
	}
}

func TestToOllamaTools_Empty(t *testing.T) {
	got := toOllamaTools(nil)
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d", len(got))
	}
}

// --- fromOllamaResponse ---

func TestFromOllamaResponse_Basic(t *testing.T) {
	resp := ollamaapi.ChatResponse{
		Message: ollamaapi.Message{Role: "assistant", Content: "hello world"},
		Metrics: ollamaapi.Metrics{PromptEvalCount: 10, EvalCount: 5},
	}
	got := fromOllamaResponse(resp)
	if got.Content != "hello world" {
		t.Errorf("Content=%q, want %q", got.Content, "hello world")
	}
	if got.Thinking != "" {
		t.Errorf("Thinking=%q, want empty", got.Thinking)
	}
	if got.Usage.InputTokens != 10 {
		t.Errorf("InputTokens=%d, want 10", got.Usage.InputTokens)
	}
	if got.Usage.OutputTokens != 5 {
		t.Errorf("OutputTokens=%d, want 5", got.Usage.OutputTokens)
	}
}

func TestFromOllamaResponse_ThinkTagExtraction(t *testing.T) {
	resp := ollamaapi.ChatResponse{
		Message: ollamaapi.Message{
			Role:    "assistant",
			Content: "<think>reasoning</think>answer",
		},
	}
	got := fromOllamaResponse(resp)
	if got.Thinking != "reasoning" {
		t.Errorf("Thinking=%q, want %q", got.Thinking, "reasoning")
	}
	if got.Content != "answer" {
		t.Errorf("Content=%q, want %q", got.Content, "answer")
	}
}

func TestFromOllamaResponse_NativeThinkingField(t *testing.T) {
	// When Thinking is set natively (Ollama Think mode), prefer it.
	resp := ollamaapi.ChatResponse{
		Message: ollamaapi.Message{
			Role:     "assistant",
			Content:  "answer",
			Thinking: "native thinking",
		},
	}
	got := fromOllamaResponse(resp)
	if got.Thinking != "native thinking" {
		t.Errorf("Thinking=%q, want %q", got.Thinking, "native thinking")
	}
	if got.Content != "answer" {
		t.Errorf("Content=%q, want %q", got.Content, "answer")
	}
}

func TestFromOllamaResponse_ToolCalls(t *testing.T) {
	args := ollamaapi.NewToolCallFunctionArguments()
	args.Set("query", "test")
	resp := ollamaapi.ChatResponse{
		Message: ollamaapi.Message{
			Role: "assistant",
			ToolCalls: []ollamaapi.ToolCall{
				{ID: "tc1", Function: ollamaapi.ToolCallFunction{Name: "search", Arguments: args}},
			},
		},
	}
	got := fromOllamaResponse(resp)
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len=%d, want 1", len(got.ToolCalls))
	}
	tc := got.ToolCalls[0]
	if tc.ID != "tc1" || tc.Name != "search" {
		t.Errorf("tc=%+v", tc)
	}
	if tc.Arguments["query"] != "test" {
		t.Errorf("arg query=%v, want 'test'", tc.Arguments["query"])
	}
}

// --- fromOllamaStreamChunk ---

func TestFromOllamaStreamChunk_Text(t *testing.T) {
	resp := ollamaapi.ChatResponse{
		Message:   ollamaapi.Message{Role: "assistant", Content: "chunk text"},
		Done:      false,
		CreatedAt: time.Now(),
	}
	text, toolCalls, done := fromOllamaStreamChunk(resp)
	if text != "chunk text" {
		t.Errorf("text=%q, want %q", text, "chunk text")
	}
	if len(toolCalls) != 0 {
		t.Errorf("toolCalls=%v, want empty", toolCalls)
	}
	if done {
		t.Error("done should be false")
	}
}

func TestFromOllamaStreamChunk_Done(t *testing.T) {
	resp := ollamaapi.ChatResponse{
		Message:   ollamaapi.Message{Role: "assistant", Content: ""},
		Done:      true,
		CreatedAt: time.Now(),
	}
	_, _, done := fromOllamaStreamChunk(resp)
	if !done {
		t.Error("done should be true")
	}
}

func TestFromOllamaStreamChunk_ToolCalls(t *testing.T) {
	args := ollamaapi.NewToolCallFunctionArguments()
	args.Set("x", 42)
	resp := ollamaapi.ChatResponse{
		Message: ollamaapi.Message{
			Role: "assistant",
			ToolCalls: []ollamaapi.ToolCall{
				{ID: "tc2", Function: ollamaapi.ToolCallFunction{Name: "calc", Arguments: args}},
			},
		},
		Done:      true,
		CreatedAt: time.Now(),
	}
	_, toolCalls, _ := fromOllamaStreamChunk(resp)
	if len(toolCalls) != 1 {
		t.Fatalf("len=%d, want 1", len(toolCalls))
	}
	if toolCalls[0].Name != "calc" {
		t.Errorf("Name=%q, want %q", toolCalls[0].Name, "calc")
	}
}
