package genkit

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ansiEscape matches ANSI escape sequences for stripping from PTY output,
// including CSI sequences, OSC sequences (e.g. terminal title/hyperlinks), and Fe escapes.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*(?:\x07|\x1b\\)|\x1b[()][A-Z0-9]|\x1b[^[]`)

// stripANSI removes ANSI escape codes from s.
func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// promptRegex matches a line starting with ❯ or > (prompt indicators).
var promptRegex = regexp.MustCompile(`(?m)^[❯>]\s`)

// detectPromptDefault returns true when a standard prompt character appears.
func detectPromptDefault(output string) bool {
	clean := stripANSI(output)
	return promptRegex.MatchString(clean)
}

// detectResponseEndDefault returns true when the prompt reappears after content.
// We require at least some non-whitespace content before the prompt.
func detectResponseEndDefault(output string) bool {
	clean := stripANSI(output)
	// Find prompt positions
	locs := promptRegex.FindAllStringIndex(clean, -1)
	if len(locs) < 2 {
		return false
	}
	// Check there's non-whitespace content between first and second prompt.
	between := clean[locs[0][1]:locs[1][0]]
	return strings.TrimSpace(between) != ""
}

// parseResponseDefault strips ANSI, trims whitespace, and removes spinner lines.
func parseResponseDefault(raw string) string {
	clean := stripANSI(raw)
	var lines []string
	for _, line := range strings.Split(clean, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip empty lines, prompt lines, and spinner/status indicators.
		if trimmed == "" || promptRegex.MatchString(line) {
			continue
		}
		lines = append(lines, trimmed)
	}
	return strings.Join(lines, "\n")
}

// ── Claude Code ──────────────────────────────────────────────────────────────

// ClaudeCodeAdapter drives the `claude` CLI.
type ClaudeCodeAdapter struct{}

func (ClaudeCodeAdapter) Name() string { return "claude_code" }
func (ClaudeCodeAdapter) Binary() string { return "claude" }

func (ClaudeCodeAdapter) NonInteractiveArgs(msg string) []string {
	return []string{"-p", msg, "--output-format", "text"}
}

func (ClaudeCodeAdapter) HealthCheckArgs() []string {
	return []string{"-p", "say ok", "--output-format", "text"}
}

func (ClaudeCodeAdapter) DetectPrompt(output string) bool {
	return detectPromptDefault(output)
}

func (ClaudeCodeAdapter) DetectResponseEnd(output string) bool {
	return detectResponseEndDefault(output)
}

func (ClaudeCodeAdapter) ParseResponse(raw string) string {
	return parseResponseDefault(raw)
}

func (ClaudeCodeAdapter) StreamingArgs(msg string) []string {
	return []string{"-p", msg, "--output-format", "stream-json", "--verbose"}
}

func (ClaudeCodeAdapter) ParseStreamEvent(line string) (string, bool) {
	return parseClaudeStreamEvent(line)
}

// parseClaudeStreamEvent extracts text content from Claude Code's stream-json format.
func parseClaudeStreamEvent(line string) (string, bool) {
	var event struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return "", false
	}
	if event.Type != "assistant" {
		return "", false
	}
	var text strings.Builder
	for _, part := range event.Message.Content {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	return text.String(), text.Len() > 0
}

// noStreamingArgs is the default for tools without JSON streaming support.
func noStreamingArgs(_ string) []string { return nil }

// noParseStreamEvent is the default for tools without JSON streaming support.
func noParseStreamEvent(_ string) (string, bool) { return "", false }

// ── Copilot CLI ───────────────────────────────────────────────────────────────

// CopilotCLIAdapter drives the `copilot` CLI.
type CopilotCLIAdapter struct{}

func (CopilotCLIAdapter) Name() string { return "copilot_cli" }
func (CopilotCLIAdapter) Binary() string { return "copilot" }

func (CopilotCLIAdapter) NonInteractiveArgs(msg string) []string {
	return []string{"-p", msg}
}

func (CopilotCLIAdapter) HealthCheckArgs() []string {
	return []string{"-p", "say ok"}
}

// copilotPromptRegex matches the Copilot input prompt: ❯ followed by placeholder text.
// The current prompt has "Type @" in it; prior input prompts are greyed ❯ with user text.
var copilotPromptRegex = regexp.MustCompile(`❯\s+(Type @|$)`)

func (CopilotCLIAdapter) DetectPrompt(output string) bool {
	// The input prompt shows "❯  Type @..." — this is distinct from greyed prior-input ❯ lines.
	return copilotPromptRegex.MatchString(output)
}

func (CopilotCLIAdapter) DetectResponseEnd(output string) bool {
	// Response is complete when we see ● (assistant marker) followed by a new ❯ prompt.
	hasResponse := strings.Contains(output, "●")
	hasNewPrompt := copilotPromptRegex.MatchString(output)
	return hasResponse && hasNewPrompt
}

func (CopilotCLIAdapter) ParseResponse(raw string) string {
	clean := stripANSI(raw)
	var lines []string
	for _, line := range strings.Split(clean, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || copilotPromptRegex.MatchString(line) {
			continue
		}
		lines = append(lines, trimmed)
	}
	return strings.Join(lines, "\n")
}

// ── Codex CLI ─────────────────────────────────────────────────────────────────

// CodexCLIAdapter drives the `codex` CLI.
type CodexCLIAdapter struct{}

func (CodexCLIAdapter) Name() string { return "codex_cli" }
func (CodexCLIAdapter) Binary() string { return "codex" }

func (CodexCLIAdapter) NonInteractiveArgs(msg string) []string {
	return []string{"exec", msg}
}

func (CodexCLIAdapter) HealthCheckArgs() []string {
	return []string{"exec", "say ok"}
}

// codexPromptRegex matches Codex's composer input area indicator.
var codexPromptRegex = regexp.MustCompile(`(?m)^[>❯]\s|composer|Type your`)

func (CodexCLIAdapter) DetectPrompt(output string) bool {
	clean := stripANSI(output)
	return codexPromptRegex.MatchString(clean)
}

func (CodexCLIAdapter) DetectResponseEnd(output string) bool {
	clean := stripANSI(output)
	locs := codexPromptRegex.FindAllStringIndex(clean, -1)
	if len(locs) < 2 {
		return false
	}
	between := clean[locs[0][1]:locs[1][0]]
	return strings.TrimSpace(between) != ""
}

func (CodexCLIAdapter) ParseResponse(raw string) string {
	return parseResponseDefault(raw)
}

// ── Gemini CLI ────────────────────────────────────────────────────────────────

// GeminiCLIAdapter drives the `gemini` CLI.
type GeminiCLIAdapter struct{}

func (GeminiCLIAdapter) Name() string { return "gemini_cli" }
func (GeminiCLIAdapter) Binary() string { return "gemini" }

func (GeminiCLIAdapter) NonInteractiveArgs(msg string) []string {
	return []string{"-p", msg}
}

func (GeminiCLIAdapter) HealthCheckArgs() []string {
	return []string{"-p", "say ok"}
}

func (GeminiCLIAdapter) DetectPrompt(output string) bool {
	return detectPromptDefault(output)
}

func (GeminiCLIAdapter) DetectResponseEnd(output string) bool {
	return detectResponseEndDefault(output)
}

func (GeminiCLIAdapter) ParseResponse(raw string) string {
	return parseResponseDefault(raw)
}

// ── Cursor CLI ────────────────────────────────────────────────────────────────

// CursorCLIAdapter drives the `agent` binary (Cursor's agent CLI).
type CursorCLIAdapter struct{}

func (CursorCLIAdapter) Name() string { return "cursor_cli" }
func (CursorCLIAdapter) Binary() string { return "agent" }

func (CursorCLIAdapter) NonInteractiveArgs(msg string) []string {
	return []string{"-p", msg}
}

func (CursorCLIAdapter) HealthCheckArgs() []string {
	return []string{"-p", "say ok"}
}

var cursorPromptRegex = regexp.MustCompile(`(?m)^>\s`)

func (CursorCLIAdapter) DetectPrompt(output string) bool {
	clean := stripANSI(output)
	return cursorPromptRegex.MatchString(clean)
}

func (CursorCLIAdapter) DetectResponseEnd(output string) bool {
	clean := stripANSI(output)
	locs := cursorPromptRegex.FindAllStringIndex(clean, -1)
	if len(locs) < 2 {
		return false
	}
	between := clean[locs[0][1]:locs[1][0]]
	return strings.TrimSpace(between) != ""
}

func (CursorCLIAdapter) ParseResponse(raw string) string {
	clean := stripANSI(raw)
	var lines []string
	for _, line := range strings.Split(clean, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || cursorPromptRegex.MatchString(line) {
			continue
		}
		lines = append(lines, trimmed)
	}
	return strings.Join(lines, "\n")
}

// StreamingArgs/ParseStreamEvent for adapters without JSON streaming support.
// They use the streamFallback path (non-interactive exec).

func (CopilotCLIAdapter) StreamingArgs(_ string) []string          { return nil }
func (CopilotCLIAdapter) ParseStreamEvent(_ string) (string, bool) { return "", false }

func (CodexCLIAdapter) StreamingArgs(_ string) []string          { return nil }
func (CodexCLIAdapter) ParseStreamEvent(_ string) (string, bool) { return "", false }

func (GeminiCLIAdapter) StreamingArgs(msg string) []string {
	return []string{"-p", msg, "--output-format", "stream-json"}
}

func (GeminiCLIAdapter) ParseStreamEvent(line string) (string, bool) {
	// Gemini CLI uses similar JSON event format — parse for text content.
	var event struct {
		Type    string `json:"type"`
		Content string `json:"content"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return "", false
	}
	if event.Text != "" {
		return event.Text, true
	}
	if event.Content != "" {
		return event.Content, true
	}
	return "", false
}

func (CursorCLIAdapter) StreamingArgs(_ string) []string          { return nil }
func (CursorCLIAdapter) ParseStreamEvent(_ string) (string, bool) { return "", false }
