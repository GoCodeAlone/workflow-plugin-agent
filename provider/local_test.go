package provider

import (
	"strings"
	"testing"
)

// --- ParseThinking ---

func TestParseThinkingBasic(t *testing.T) {
	thinking, content := ParseThinking("<think>reasoning</think>answer")
	if thinking != "reasoning" {
		t.Errorf("thinking=%q, want %q", thinking, "reasoning")
	}
	if content != "answer" {
		t.Errorf("content=%q, want %q", content, "answer")
	}
}

func TestParseThinkingNoTag(t *testing.T) {
	thinking, content := ParseThinking("just the answer")
	if thinking != "" {
		t.Errorf("thinking=%q, want empty", thinking)
	}
	if content != "just the answer" {
		t.Errorf("content=%q, want %q", content, "just the answer")
	}
}

func TestParseThinkingWhitespace(t *testing.T) {
	thinking, content := ParseThinking("<think>  think text  </think>  result  ")
	if thinking != "think text" {
		t.Errorf("thinking=%q, want trimmed", thinking)
	}
	if content != "result" {
		t.Errorf("content=%q, want trimmed", content)
	}
}

func TestParseThinkingUnclosedTag(t *testing.T) {
	thinking, content := ParseThinking("<think>incomplete")
	if thinking != "incomplete" {
		t.Errorf("thinking=%q, want %q", thinking, "incomplete")
	}
	if content != "" {
		t.Errorf("content=%q, want empty", content)
	}
}

func TestParseThinkingEmptyBlock(t *testing.T) {
	thinking, content := ParseThinking("<think></think>answer")
	if thinking != "" {
		t.Errorf("thinking=%q, want empty", thinking)
	}
	if content != "answer" {
		t.Errorf("content=%q, want %q", content, "answer")
	}
}

func TestParseThinkingMultipleBlocks(t *testing.T) {
	// Only the first block becomes thinking; remainder is content.
	thinking, content := ParseThinking("<think>first</think>middle<think>second</think>end")
	if thinking != "first" {
		t.Errorf("thinking=%q, want %q", thinking, "first")
	}
	// Content includes everything after the first </think>
	if !strings.Contains(content, "middle") {
		t.Errorf("content=%q should contain 'middle'", content)
	}
}

// --- ThinkingStreamParser ---

func collectEvents(p *ThinkingStreamParser, chunks []string) []StreamEvent {
	var all []StreamEvent
	for _, c := range chunks {
		all = append(all, p.Feed(c)...)
	}
	return all
}

func TestStreamParserNoThink(t *testing.T) {
	var p ThinkingStreamParser
	events := collectEvents(&p, []string{"hello ", "world"})
	for _, e := range events {
		if e.Type != "text" {
			t.Errorf("expected text event, got type=%q", e.Type)
		}
	}
	text := joinText(events)
	if text != "hello world" {
		t.Errorf("text=%q, want %q", text, "hello world")
	}
}

func TestStreamParserBasicThink(t *testing.T) {
	var p ThinkingStreamParser
	events := collectEvents(&p, []string{"<think>think this</think>answer"})
	assertThinking(t, events, "think this")
	assertText(t, events, "answer")
}

func TestStreamParserTagSplitAcrossChunks(t *testing.T) {
	var p ThinkingStreamParser
	// Split "<think>" across two chunks
	events := collectEvents(&p, []string{"<thi", "nk>reasoning</think>result"})
	assertThinking(t, events, "reasoning")
	assertText(t, events, "result")
}

func TestStreamParserCloseTagSplit(t *testing.T) {
	var p ThinkingStreamParser
	// Split "</think>" across two chunks
	events := collectEvents(&p, []string{"<think>reasoning</thi", "nk>result"})
	assertThinking(t, events, "reasoning")
	assertText(t, events, "result")
}

func TestStreamParserThinkingOnly(t *testing.T) {
	var p ThinkingStreamParser
	events := collectEvents(&p, []string{"<think>think</think>"})
	assertThinking(t, events, "think")
	if joinText(events) != "" {
		t.Errorf("expected no text events")
	}
}

func TestStreamParserTextBeforeThink(t *testing.T) {
	var p ThinkingStreamParser
	events := collectEvents(&p, []string{"before<think>thinking</think>after"})
	assertTextContains(t, events, "before")
	assertThinking(t, events, "thinking")
	assertTextContains(t, events, "after")
}

// helpers

func joinText(events []StreamEvent) string {
	var b strings.Builder
	for _, e := range events {
		if e.Type == "text" {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func joinThinking(events []StreamEvent) string {
	var b strings.Builder
	for _, e := range events {
		if e.Type == "thinking" {
			b.WriteString(e.Thinking)
		}
	}
	return b.String()
}

func assertThinking(t *testing.T, events []StreamEvent, want string) {
	t.Helper()
	got := joinThinking(events)
	if got != want {
		t.Errorf("thinking=%q, want %q", got, want)
	}
}

func assertText(t *testing.T, events []StreamEvent, want string) {
	t.Helper()
	got := joinText(events)
	if got != want {
		t.Errorf("text=%q, want %q", got, want)
	}
}

func assertTextContains(t *testing.T, events []StreamEvent, sub string) {
	t.Helper()
	got := joinText(events)
	if !strings.Contains(got, sub) {
		t.Errorf("text=%q should contain %q", got, sub)
	}
}

// --- LocalAuthMode ---

func TestLocalAuthMode(t *testing.T) {
	info := LocalAuthMode("ollama", "Ollama (Local)")
	if info.Mode != "ollama" {
		t.Errorf("Mode=%q, want %q", info.Mode, "ollama")
	}
	if info.DisplayName != "Ollama (Local)" {
		t.Errorf("DisplayName=%q", info.DisplayName)
	}
	if !info.ServerSafe {
		t.Error("ServerSafe should be true for local providers")
	}
	if info.Warning != "" {
		t.Errorf("Warning should be empty, got %q", info.Warning)
	}
}
