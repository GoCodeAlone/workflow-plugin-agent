package genkit

import (
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/firebase/genkit/go/ai"
)

func TestToGenkitMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are helpful."},
		{Role: provider.RoleUser, Content: "Hello"},
		{Role: provider.RoleAssistant, Content: "Hi there"},
	}
	result := toGenkitMessages(msgs)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[0].Role != ai.RoleSystem {
		t.Errorf("expected system role, got %s", result[0].Role)
	}
	if result[2].Role != ai.RoleModel {
		t.Errorf("expected model role for assistant, got %s", result[2].Role)
	}
}

func TestToGenkitMessagesToolResult(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleTool, Content: "42", ToolCallID: "add"},
	}
	result := toGenkitMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Role != ai.RoleTool {
		t.Errorf("expected tool role, got %s", result[0].Role)
	}
	if len(result[0].Content) == 0 || result[0].Content[0].ToolResponse == nil {
		t.Error("expected ToolResponsePart in message content")
	}
}

func TestFromGenkitResponse(t *testing.T) {
	resp := &ai.ModelResponse{
		Message: ai.NewModelTextMessage("Hello world"),
		Usage:   &ai.GenerationUsage{InputTokens: 10, OutputTokens: 5},
	}
	result := fromGenkitResponse(resp)
	if result.Content != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", result.Content)
	}
	if result.Usage.InputTokens != 10 {
		t.Errorf("expected 10 input tokens, got %d", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 5 {
		t.Errorf("expected 5 output tokens, got %d", result.Usage.OutputTokens)
	}
}

func TestFromGenkitResponseNil(t *testing.T) {
	result := fromGenkitResponse(nil)
	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
}

func TestFromGenkitResponseThinking(t *testing.T) {
	msg := ai.NewMessage(ai.RoleModel, nil,
		ai.NewReasoningPart("I think therefore I am", nil),
		ai.NewTextPart("Result text"),
	)
	resp := &ai.ModelResponse{Message: msg}
	result := fromGenkitResponse(resp)
	if result.Thinking != "I think therefore I am" {
		t.Errorf("expected thinking trace, got %q", result.Thinking)
	}
	if result.Content != "Result text" {
		t.Errorf("expected 'Result text', got %q", result.Content)
	}
}

func TestFromGenkitChunkText(t *testing.T) {
	chunk := &ai.ModelResponseChunk{
		Content: []*ai.Part{ai.NewTextPart("hello")},
	}
	ev := fromGenkitChunk(chunk)
	if ev.Type != "text" {
		t.Errorf("expected type 'text', got %q", ev.Type)
	}
	if ev.Text != "hello" {
		t.Errorf("expected 'hello', got %q", ev.Text)
	}
}

func TestFromGenkitChunkThinking(t *testing.T) {
	chunk := &ai.ModelResponseChunk{
		Content: []*ai.Part{ai.NewReasoningPart("thinking...", nil)},
	}
	ev := fromGenkitChunk(chunk)
	if ev.Type != "thinking" {
		t.Errorf("expected type 'thinking', got %q", ev.Type)
	}
	if ev.Thinking != "thinking..." {
		t.Errorf("expected 'thinking...', got %q", ev.Thinking)
	}
}

func TestFromGenkitChunkNil(t *testing.T) {
	ev := fromGenkitChunk(nil)
	if ev.Type != "done" {
		t.Errorf("expected type 'done', got %q", ev.Type)
	}
}
