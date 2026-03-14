package provider

import (
	"encoding/json"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

// TestToAnthropicParams_AssistantToolCalls verifies that assistant messages with
// tool calls are correctly serialized as tool_use content blocks (multi-turn tool use).
func TestToAnthropicParams_AssistantToolCalls(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: "What's the weather?"},
		{
			Role:    RoleAssistant,
			Content: "",
			ToolCalls: []ToolCall{
				{ID: "call_1", Name: "get_weather", Arguments: map[string]any{"location": "NYC"}},
			},
		},
		{Role: RoleTool, ToolCallID: "call_1", Content: "72°F, sunny"},
		{Role: RoleAssistant, Content: "It's 72°F and sunny in NYC."},
	}

	params := toAnthropicParams("claude-sonnet-4-20250514", 1024, messages, nil)

	if len(params.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(params.Messages))
	}

	// Message[1] should be an assistant message with a tool_use block.
	assistantMsg := params.Messages[1]
	if assistantMsg.Role != anthropic.MessageParamRoleAssistant {
		t.Fatalf("expected assistant role at index 1, got %q", assistantMsg.Role)
	}
	blocks := assistantMsg.Content
	if len(blocks) != 1 {
		t.Fatalf("expected 1 content block in assistant message, got %d", len(blocks))
	}
	toolBlock := blocks[0]
	if toolBlock.OfToolUse == nil {
		t.Fatal("expected tool_use block in assistant message")
	}
	if toolBlock.OfToolUse.ID != "call_1" {
		t.Errorf("tool_use ID = %q, want %q", toolBlock.OfToolUse.ID, "call_1")
	}
	if toolBlock.OfToolUse.Name != "get_weather" {
		t.Errorf("tool_use Name = %q, want %q", toolBlock.OfToolUse.Name, "get_weather")
	}
	inputBytes, ok := toolBlock.OfToolUse.Input.([]byte)
	if !ok {
		t.Fatalf("expected []byte input, got %T", toolBlock.OfToolUse.Input)
	}
	var args map[string]any
	if err := json.Unmarshal(inputBytes, &args); err != nil {
		t.Fatalf("unmarshal tool_use input: %v", err)
	}
	if args["location"] != "NYC" {
		t.Errorf("tool_use input location = %v, want NYC", args["location"])
	}

	// Message[2] should be a user message with a tool_result block.
	userMsg := params.Messages[2]
	if userMsg.Role != anthropic.MessageParamRoleUser {
		t.Fatalf("expected user role at index 2, got %q", userMsg.Role)
	}
}

// TestToAnthropicParams_AssistantTextAndToolCalls verifies mixed text + tool_use blocks.
func TestToAnthropicParams_AssistantTextAndToolCalls(t *testing.T) {
	messages := []Message{
		{
			Role:    RoleAssistant,
			Content: "Let me check that.",
			ToolCalls: []ToolCall{
				{ID: "call_2", Name: "search", Arguments: map[string]any{"q": "Go generics"}},
			},
		},
	}

	params := toAnthropicParams("claude-sonnet-4-20250514", 1024, messages, nil)
	if len(params.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(params.Messages))
	}
	blocks := params.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (text + tool_use), got %d", len(blocks))
	}
	if blocks[0].OfText == nil {
		t.Error("expected first block to be text")
	}
	if blocks[1].OfToolUse == nil {
		t.Error("expected second block to be tool_use")
	}
}

// TestFromAnthropicMessage_ToolCallUnmarshalError verifies that malformed tool
// call arguments produce an error instead of silent data loss.
func TestFromAnthropicMessage_ToolCallUnmarshalError(t *testing.T) {
	msg := &anthropic.Message{
		Content: []anthropic.ContentBlockUnion{
			{Type: "tool_use", ID: "call_bad", Name: "broken_tool", Input: json.RawMessage(`{invalid json}`)},
		},
	}
	_, err := fromAnthropicMessage(msg)
	if err == nil {
		t.Fatal("expected error for malformed tool call input, got nil")
	}
}
