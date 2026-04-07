package genkit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// TestClaudeCodeInteractivePTY_MultiTurn tests Claude Code interactive PTY
// with a 3-message multi-turn conversation including complex output.
// Requires: claude binary in PATH, authenticated.
func TestClaudeCodeInteractivePTY_MultiTurn(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not in PATH")
	}
	if os.Getenv("PTY_INTEGRATION") == "" {
		t.Skip("set PTY_INTEGRATION=1 to run interactive PTY tests")
	}

	p, err := NewClaudeCodeProvider("")
	if err != nil {
		t.Fatalf("NewClaudeCodeProvider: %v", err)
	}
	defer p.(interface{ Close() error }).Close()

	ctx := context.Background()

	// Message 1: Establish context
	t.Log("=== Message 1: Establish context ===")
	ch1, err := p.Stream(ctx, []provider.Message{
		{Role: provider.RoleUser, Content: "My name is PTYTestBot. Remember this. Reply with just 'Noted, PTYTestBot.' and nothing else."},
	}, nil)
	if err != nil {
		t.Fatalf("Stream msg1: %v", err)
	}
	text1 := collectStream(t, ch1, 2*time.Minute)
	t.Logf("Response 1:\n%s", text1)
	if text1 == "" {
		t.Error("Message 1: empty response")
	}

	// Message 2: Complex code generation
	t.Log("=== Message 2: Complex code generation ===")
	ch2, err := p.Stream(ctx, []provider.Message{
		{Role: provider.RoleUser, Content: "Write a Python function called merge_sorted_lists that takes two sorted lists and returns a merged sorted list. Include type hints and a docstring. Only output the code block."},
	}, nil)
	if err != nil {
		t.Fatalf("Stream msg2: %v", err)
	}
	text2 := collectStream(t, ch2, 2*time.Minute)
	t.Logf("Response 2:\n%s", text2)
	if !strings.Contains(text2, "merge_sorted") && !strings.Contains(text2, "def ") {
		t.Error("Message 2: expected Python function, got:", text2[:min(100, len(text2))])
	}

	// Message 3: Recall context (tests multi-turn)
	t.Log("=== Message 3: Context recall ===")
	ch3, err := p.Stream(ctx, []provider.Message{
		{Role: provider.RoleUser, Content: "What was my name? Reply with just the name."},
	}, nil)
	if err != nil {
		t.Fatalf("Stream msg3: %v", err)
	}
	text3 := collectStream(t, ch3, 2*time.Minute)
	t.Logf("Response 3:\n%s", text3)
	if !strings.Contains(strings.ToLower(text3), "ptytestbot") && !strings.Contains(strings.ToLower(text3), "pty") {
		t.Error("Message 3: expected name recall, got:", text3[:min(100, len(text3))])
	}
}

// TestClaudeCodeInteractivePTY_MultiAgent tests that PTY output is readable
// when Claude Code uses tools (file_read, bash, etc.) which create tool call
// UI elements in the terminal.
func TestClaudeCodeInteractivePTY_MultiAgent(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not in PATH")
	}
	if os.Getenv("PTY_INTEGRATION") == "" {
		t.Skip("set PTY_INTEGRATION=1 to run interactive PTY tests")
	}

	p, err := NewClaudeCodeProvider("")
	if err != nil {
		t.Fatalf("NewClaudeCodeProvider: %v", err)
	}
	defer p.(interface{ Close() error }).Close()

	ctx := context.Background()

	// This prompt triggers tool usage (listing files), which creates
	// tool call cards in Claude Code's TUI (Read, Bash, etc.)
	t.Log("=== Tool-using prompt (triggers tool call UI) ===")
	ch, err := p.Stream(ctx, []provider.Message{
		{Role: provider.RoleUser, Content: "List the files in the current directory and tell me how many there are. Be brief."},
	}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	text := collectStream(t, ch, 3*time.Minute)
	t.Logf("Response:\n%s", text)
	if text == "" {
		t.Error("empty response from tool-using prompt")
	}
	// Should contain some file count or listing
	if !strings.Contains(text, "file") && !strings.Contains(text, "director") {
		t.Error("expected response about files/directories, got:", text[:min(100, len(text))])
	}
}

// TestCopilotInteractivePTY tests Copilot with interactive PTY enabled.
func TestCopilotInteractivePTY(t *testing.T) {
	if _, err := exec.LookPath("copilot"); err != nil {
		t.Skip("copilot not in PATH")
	}
	if os.Getenv("PTY_INTEGRATION") == "" {
		t.Skip("set PTY_INTEGRATION=1 to run interactive PTY tests")
	}

	p, err := NewCopilotCLIProvider("")
	if err != nil {
		t.Fatalf("NewCopilotCLIProvider: %v", err)
	}
	defer p.(interface{ Close() error }).Close()

	ctx := context.Background()

	// Simple query
	t.Log("=== Copilot: Simple query ===")
	ch1, err := p.Stream(ctx, []provider.Message{
		{Role: provider.RoleUser, Content: "What is the capital of Japan? Reply in one word."},
	}, nil)
	if err != nil {
		t.Fatalf("Stream msg1: %v", err)
	}
	text1 := collectStream(t, ch1, 2*time.Minute)
	t.Logf("Response 1:\n%s", text1)
	if !strings.Contains(strings.ToLower(text1), "tokyo") {
		t.Error("expected Tokyo, got:", text1[:min(100, len(text1))])
	}

	// Code generation
	t.Log("=== Copilot: Code generation ===")
	ch2, err := p.Stream(ctx, []provider.Message{
		{Role: provider.RoleUser, Content: "Write a Python function is_palindrome(s) that checks if a string is a palindrome. Only code."},
	}, nil)
	if err != nil {
		t.Fatalf("Stream msg2: %v", err)
	}
	text2 := collectStream(t, ch2, 2*time.Minute)
	t.Logf("Response 2:\n%s", text2)
	if !strings.Contains(text2, "palindrome") && !strings.Contains(text2, "def ") {
		t.Error("expected Python function")
	}
}

func collectStream(t *testing.T, ch <-chan provider.StreamEvent, timeout time.Duration) string {
	t.Helper()
	var sb strings.Builder
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return sb.String()
			}
			switch ev.Type {
			case "text":
				sb.WriteString(ev.Text)
			case "done":
				return sb.String()
			case "error":
				t.Logf("stream error: %s", ev.Error)
				return sb.String()
			}
		case <-timer.C:
			t.Log("timeout waiting for stream")
			return sb.String()
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestExtractResponse_ClaudeCodeScreen tests extractResponse with realistic
// Claude Code screen content including tool call cards.
func TestExtractResponse_ClaudeCodeScreen(t *testing.T) {
	p := &ptyProvider{adapter: ClaudeCodeAdapter{}}

	// Simulate screen with tool call UI (Read tool, Bash tool)
	screen := `╭─────────────────────────────────────╮
│ ✻ Claude Code                       │
╰─────────────────────────────────────╯

  ❯ List the files and count them

  ⎿  Read tool
     Listed 5 files in current directory

  There are 5 files in the current directory:
  - main.go
  - go.mod
  - go.sum
  - README.md
  - Makefile

  ❯`

	result := p.extractResponse(screen)
	t.Logf("Extracted: %q", result)
	if !strings.Contains(result, "5 files") {
		t.Errorf("expected '5 files' in response, got: %s", result)
	}
}

// TestExtractResponse_CopilotScreen tests extractResponse with Copilot screen.
func TestExtractResponse_CopilotScreen(t *testing.T) {
	p := &ptyProvider{adapter: CopilotCLIAdapter{}}

	screen := fmt.Sprintf(`  💡 Tip: Use @workspace to ask about your project

  ❯ What is 2+2?

  ● The answer is 4.

  ❯  Type @`)

	result := p.extractResponse(screen)
	t.Logf("Extracted: %q", result)
	if !strings.Contains(result, "4") {
		t.Errorf("expected '4' in response, got: %s", result)
	}
}

// TestExtractResponseDiff_CopilotWithSystemLines tests that screen-diff extraction
// correctly ignores pre-existing system ● lines and only captures response content.
func TestExtractResponseDiff_CopilotWithSystemLines(t *testing.T) {
	p := &ptyProvider{adapter: CopilotCLIAdapter{}}

	// Pre-message screen: has system ● lines that should be ignored
	preScreen := `  ● Environment: macOS 14.5
  💡 Tip: Use @workspace to ask about your project
  ❯  Type @`

	preLines := screenLines(preScreen)

	// Post-message screen: user message + response + new prompt
	postScreen := `  ● Environment: macOS 14.5
  💡 Tip: Use @workspace to ask about your project

  ❯ What is 2+2?

  ● The answer is 4.

  ❯  Type @`

	result := p.extractResponseDiff(postScreen, preLines)
	t.Logf("Extracted: %q", result)
	if !strings.Contains(result, "4") {
		t.Errorf("expected '4' in response, got: %s", result)
	}
	// Must NOT contain the system Environment line
	if strings.Contains(result, "Environment") {
		t.Errorf("should not contain pre-existing system lines, got: %s", result)
	}
}

// TestExtractResponseDiff_ClaudeCodeWithToolCalls tests screen-diff extraction
// when Claude Code shows tool call cards.
func TestExtractResponseDiff_ClaudeCodeWithToolCalls(t *testing.T) {
	p := &ptyProvider{adapter: ClaudeCodeAdapter{}}

	preScreen := `╭─────────────────────────────────────╮
│ ✻ Claude Code                       │
╰─────────────────────────────────────╯
  ❯`

	preLines := screenLines(preScreen)

	postScreen := `╭─────────────────────────────────────╮
│ ✻ Claude Code                       │
╰─────────────────────────────────────╯

  ❯ List files

  ⎿  Bash(ls)
     main.go  go.mod  README.md

  There are 3 files in the directory.

  ❯`

	result := p.extractResponseDiff(postScreen, preLines)
	t.Logf("Extracted: %q", result)
	if !strings.Contains(result, "3 files") {
		t.Errorf("expected '3 files' in response, got: %s", result)
	}
}

// TestScreenLines verifies the screen snapshot helper.
func TestScreenLines(t *testing.T) {
	screen := "  line1  \n  line2\n\n  line3  "
	lines := screenLines(screen)
	if !lines["  line1"] {
		t.Error("expected '  line1' in set")
	}
	if lines[""] {
		t.Error("empty strings should not be in set")
	}
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
}
