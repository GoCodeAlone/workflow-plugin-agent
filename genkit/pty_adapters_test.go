package genkit

import (
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"\x1b[32mhello\x1b[0m", "hello"},
		{"\x1b[1;31merror\x1b[0m world", "error world"},
		{"no escapes", "no escapes"},
		{"\x1b[2J\x1b[H", ""},
	}
	for _, tc := range cases {
		got := stripANSI(tc.in)
		if got != tc.want {
			t.Errorf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClaudeCodeAdapter(t *testing.T) {
	a := ClaudeCodeAdapter{}

	if a.Name() != "claude_code" {
		t.Errorf("Name() = %q, want %q", a.Name(), "claude_code")
	}
	if a.Binary() != "claude" {
		t.Errorf("Binary() = %q, want %q", a.Binary(), "claude")
	}

	args := a.NonInteractiveArgs("hello world")
	if len(args) < 2 || args[0] != "-p" || args[1] != "hello world" {
		t.Errorf("NonInteractiveArgs = %v, expected -p as first arg", args)
	}

	hc := a.HealthCheckArgs()
	if len(hc) == 0 {
		t.Error("HealthCheckArgs() returned empty slice")
	}
}

func TestClaudeCodeAdapterDetectPrompt(t *testing.T) {
	a := ClaudeCodeAdapter{}

	cases := []struct {
		output string
		want   bool
	}{
		{"❯ ", true},
		{"> ", true},
		{"some output\n❯ ", true},
		{"no prompt here", false},
		{"\x1b[32m❯\x1b[0m ", true}, // ANSI-wrapped prompt
	}
	for _, tc := range cases {
		got := a.DetectPrompt(tc.output)
		if got != tc.want {
			t.Errorf("DetectPrompt(%q) = %v, want %v", tc.output, got, tc.want)
		}
	}
}

func TestClaudeCodeAdapterDetectResponseEnd(t *testing.T) {
	a := ClaudeCodeAdapter{}

	// Two prompts with content between them — response ended.
	twoPrompts := "❯ \nThe answer is 42\n❯ "
	if !a.DetectResponseEnd(twoPrompts) {
		t.Error("DetectResponseEnd should be true when second prompt appears after content")
	}

	// Only one prompt — not yet ended.
	onePrompt := "❯ \nstill streaming..."
	if a.DetectResponseEnd(onePrompt) {
		t.Error("DetectResponseEnd should be false with only one prompt")
	}
}

func TestClaudeCodeAdapterParseResponse(t *testing.T) {
	a := ClaudeCodeAdapter{}

	raw := "❯ \n\x1b[32mHello, world!\x1b[0m\nThis is a response.\n❯ "
	got := a.ParseResponse(raw)

	if !strings.Contains(got, "Hello, world!") {
		t.Errorf("ParseResponse(%q) = %q, want content to include 'Hello, world!'", raw, got)
	}
	// Prompt lines should be stripped.
	if strings.Contains(got, "❯") {
		t.Errorf("ParseResponse(%q) = %q, prompt chars should be stripped", raw, got)
	}
}

func TestCopilotCLIAdapter(t *testing.T) {
	a := CopilotCLIAdapter{}
	if a.Name() != "copilot_cli" {
		t.Errorf("Name() = %q", a.Name())
	}
	if a.Binary() != "copilot" {
		t.Errorf("Binary() = %q", a.Binary())
	}
	args := a.NonInteractiveArgs("test")
	if len(args) < 2 || args[0] != "-p" {
		t.Errorf("NonInteractiveArgs = %v", args)
	}
}

func TestCodexCLIAdapter(t *testing.T) {
	a := CodexCLIAdapter{}
	if a.Name() != "codex_cli" {
		t.Errorf("Name() = %q", a.Name())
	}
	if a.Binary() != "codex" {
		t.Errorf("Binary() = %q", a.Binary())
	}
	args := a.NonInteractiveArgs("test")
	if len(args) < 2 || args[0] != "exec" {
		t.Errorf("NonInteractiveArgs = %v, want exec as first arg", args)
	}
}

func TestGeminiCLIAdapter(t *testing.T) {
	a := GeminiCLIAdapter{}
	if a.Name() != "gemini_cli" {
		t.Errorf("Name() = %q", a.Name())
	}
	if a.Binary() != "gemini" {
		t.Errorf("Binary() = %q", a.Binary())
	}
	args := a.NonInteractiveArgs("test")
	if len(args) < 2 || args[0] != "-p" {
		t.Errorf("NonInteractiveArgs = %v", args)
	}
}

func TestCursorCLIAdapter(t *testing.T) {
	a := CursorCLIAdapter{}
	if a.Name() != "cursor_cli" {
		t.Errorf("Name() = %q", a.Name())
	}
	if a.Binary() != "agent" {
		t.Errorf("Binary() = %q", a.Binary())
	}
	args := a.NonInteractiveArgs("test")
	if len(args) < 2 || args[0] != "-p" {
		t.Errorf("NonInteractiveArgs = %v", args)
	}
}

func TestAllAdaptersHealthCheckArgs(t *testing.T) {
	adapters := []CLIAdapter{
		ClaudeCodeAdapter{},
		CopilotCLIAdapter{},
		CodexCLIAdapter{},
		GeminiCLIAdapter{},
		CursorCLIAdapter{},
	}
	for _, a := range adapters {
		hc := a.HealthCheckArgs()
		if len(hc) == 0 {
			t.Errorf("%s.HealthCheckArgs() returned empty slice", a.Name())
		}
	}
}
