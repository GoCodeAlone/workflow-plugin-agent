package provider

import (
	"encoding/json"
	"testing"
)

func TestResponseThinkingOmitEmpty(t *testing.T) {
	r := Response{Content: "hello", Usage: Usage{InputTokens: 1, OutputTokens: 2}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["thinking"]; ok {
		t.Error("thinking field should be omitted when empty")
	}
}

func TestResponseThinkingRoundTrip(t *testing.T) {
	r := Response{Content: "answer", Thinking: "let me think", Usage: Usage{InputTokens: 5, OutputTokens: 3}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var got Response
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Thinking != "let me think" {
		t.Errorf("got Thinking=%q, want %q", got.Thinking, "let me think")
	}
	if got.Content != "answer" {
		t.Errorf("got Content=%q, want %q", got.Content, "answer")
	}
}

func TestStreamEventThinkingType(t *testing.T) {
	e := StreamEvent{Type: "thinking", Thinking: "reasoning here"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got StreamEvent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "thinking" {
		t.Errorf("got Type=%q, want %q", got.Type, "thinking")
	}
	if got.Thinking != "reasoning here" {
		t.Errorf("got Thinking=%q, want %q", got.Thinking, "reasoning here")
	}
}

func TestStreamEventThinkingOmitEmpty(t *testing.T) {
	e := StreamEvent{Type: "text", Text: "hello"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["thinking"]; ok {
		t.Error("thinking field should be omitted when empty")
	}
}

func TestStreamEventExistingTypesUnchanged(t *testing.T) {
	types := []string{"text", "tool_call", "done", "error"}
	for _, typ := range types {
		e := StreamEvent{Type: typ}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal type=%q: %v", typ, err)
		}
		var got StreamEvent
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal type=%q: %v", typ, err)
		}
		if got.Type != typ {
			t.Errorf("got Type=%q, want %q", got.Type, typ)
		}
	}
}
