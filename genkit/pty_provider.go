package genkit

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

// CLIAdapter defines per-tool behavior for driving a CLI via PTY.
type CLIAdapter interface {
	// Name returns the provider type name (e.g. "claude_code").
	Name() string
	// Binary returns the binary name to execute (e.g. "claude").
	Binary() string
	// NonInteractiveArgs returns CLI args for single-shot (non-interactive) mode.
	NonInteractiveArgs(msg string) []string
	// StreamingArgs returns CLI args for streaming JSON output mode.
	// Returns nil if the tool doesn't support structured streaming — falls back
	// to NonInteractiveArgs with line-by-line stdout reading.
	StreamingArgs(msg string) []string
	// HealthCheckArgs returns args for a quick health check invocation.
	HealthCheckArgs() []string
	// DetectPrompt returns true when the CLI output indicates it is ready for input.
	DetectPrompt(output string) bool
	// DetectResponseEnd returns true when the CLI output indicates the response is complete.
	DetectResponseEnd(output string) bool
	// ParseResponse cleans raw PTY output into plain response text.
	ParseResponse(raw string) string
	// ParseStreamEvent parses a line of streaming JSON output into text content.
	// Returns the text content and true if the line contained assistant text,
	// or empty string and false if the line should be skipped.
	ParseStreamEvent(line string) (string, bool)
	// SupportsInteractivePTY returns true if the tool's interactive TUI is
	// compatible with vt10x screen reading. Tools that return false skip the
	// interactive path and go straight to JSON streaming or non-interactive exec.
	SupportsInteractivePTY() bool
	// InteractiveArgs returns CLI args used when launching the interactive PTY session.
	// For example, Claude Code returns ["--permission-mode", "acceptEdits"] to
	// auto-approve file edits. Returns nil for no extra args.
	InteractiveArgs() []string
}

// ptyProvider implements provider.Provider by driving a CLI tool via PTY.
type ptyProvider struct {
	adapter  CLIAdapter
	binPath  string
	workDir  string
	authInfo provider.AuthModeInfo
	timeout  time.Duration

	// PTY session state (kept alive for multi-turn streaming)
	mu        sync.Mutex     // guards ptmx, cmd, vt field pointers
	sessionMu sync.Mutex     // serializes full turn lifecycle (prompt→send→read)
	ptmx      *os.File       // PTY master — nil when no active session
	cmd       *exec.Cmd      // running CLI process
	vt        vt10x.Terminal // virtual terminal screen buffer
	output        bytes.Buffer   // raw output accumulator (for fallback parsing)
	promptHandler *PromptHandler // auto-responds to known screen prompts
}

// Name implements provider.Provider.
func (p *ptyProvider) Name() string {
	return p.adapter.Name()
}

// AuthModeInfo implements provider.Provider.
func (p *ptyProvider) AuthModeInfo() provider.AuthModeInfo {
	return p.authInfo
}

// Chat runs the CLI in single-shot exec mode and returns the response.
// Always stateless — each call is independent with no session context.
// For multi-turn conversations, callers must use Stream() with an adapter
// that supports interactive PTY.
func (p *ptyProvider) Chat(ctx context.Context, messages []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	// Flatten messages into a single prompt for CLIs that take a single -p argument.
	msg := flattenMessages(messages)
	args := p.adapter.NonInteractiveArgs(msg)

	timeout := p.timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.binPath, args...)
	if p.workDir != "" {
		cmd.Dir = p.workDir
	}

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("pty provider %s: chat: %w", p.adapter.Name(), err)
	}

	content := p.adapter.ParseResponse(string(out))
	return &provider.Response{Content: content}, nil
}

// Stream operates in one of two distinct modes based on the adapter:
//
// INTERACTIVE MODE (SupportsInteractivePTY == true):
//
//	Uses vt10x PTY to maintain a persistent session. Multi-turn context is
//	preserved across calls. If the interactive session fails, it returns an
//	error — it does NOT silently fall back to exec mode, because that would
//	lose session context without the caller knowing.
//
// EXEC MODE (SupportsInteractivePTY == false):
//
//	Uses JSON streaming (StreamingArgs) or raw stdout (NonInteractiveArgs).
//	Each call is independent — no multi-turn session context. Suitable for
//	single-shot queries only.
func (p *ptyProvider) Stream(ctx context.Context, messages []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	msg := flattenMessages(messages)

	ch := make(chan provider.StreamEvent, 32)

	go func() {
		defer close(ch)

		// Two distinct modes — NO silent fallback between them:
		//
		// INTERACTIVE (SupportsInteractivePTY): Multi-turn session via vt10x PTY.
		//   Maintains conversation context across calls. Used by Claude Code, Copilot.
		//   If interactive fails, it errors — does NOT silently degrade to exec mode.
		//
		// EXEC (everything else): Single-shot execution. No session persistence.
		//   Uses JSON streaming (StreamingArgs) or raw stdout (NonInteractiveArgs).
		//   Each call is independent — no multi-turn context.

		if p.adapter.SupportsInteractivePTY() {
			if err := p.streamInteractive(ctx, msg, ch); err != nil {
				ch <- provider.StreamEvent{Type: "error", Error: fmt.Sprintf("interactive PTY: %v", err)}
			}
			return
		}

		// Exec mode: JSON streaming if available, otherwise raw stdout.
		if args := p.adapter.StreamingArgs(msg); args != nil {
			if err := p.streamJSON(ctx, args, ch); err == nil {
				return
			}
		}

		if err := p.streamFallback(ctx, msg, ch); err != nil {
			ch <- provider.StreamEvent{Type: "error", Error: err.Error()}
		}
	}()

	return ch, nil
}

// streamInteractive manages the PTY session and streams output events.
// sessionMu is held for the entire turn so concurrent Stream() calls are serialized.
func (p *ptyProvider) streamInteractive(ctx context.Context, msg string, ch chan<- provider.StreamEvent) error {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()

	p.mu.Lock()
	// Start a new PTY session if none is active.
	if p.ptmx == nil {
		if err := p.startSession(); err != nil {
			p.mu.Unlock()
			return fmt.Errorf("pty provider %s: start session: %w", p.adapter.Name(), err)
		}
	}
	ptmx := p.ptmx
	p.mu.Unlock()

	timeout := p.timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	deadline := time.Now().Add(timeout)

	// Wait for the prompt to appear before sending input.
	if err := p.waitForPrompt(ctx, ptmx, deadline); err != nil {
		// Log the screen state for debugging prompt detection.
		screen := p.vt.String()
		fmt.Fprintf(os.Stderr, "PTY %s: prompt wait failed, screen:\n---\n%s\n---\n", p.adapter.Name(), screen)
		return fmt.Errorf("waiting for prompt: %w", err)
	}

	// Snapshot the screen BEFORE sending — used for diff-based extraction.
	preScreen := p.vt.String()

	// Send the message character by character (some TUIs need this).
	for _, ch := range msg {
		if _, err := ptmx.Write([]byte(string(ch))); err != nil {
			return fmt.Errorf("writing to PTY: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Submit with carriage return (Enter key in terminal).
	if _, err := ptmx.Write([]byte{'\r'}); err != nil {
		return fmt.Errorf("sending enter: %w", err)
	}

	// Small delay to let the CLI process the input before reading.
	time.Sleep(1 * time.Second)

	// Read output and emit stream events until response ends.
	return p.readResponse(ctx, ptmx, deadline, preScreen, ch)
}

// startSession forks the CLI process under a PTY with a virtual terminal.
// Caller must hold p.mu.
func (p *ptyProvider) startSession() error {
	args := p.adapter.InteractiveArgs()
	cmd := exec.Command(p.binPath, args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if p.workDir != "" {
		cmd.Dir = p.workDir
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100})
	if err != nil {
		return fmt.Errorf("pty.StartWithSize: %w", err)
	}

	// Virtual terminal processes escape sequences and maintains screen buffer.
	vt := vt10x.New(vt10x.WithSize(100, 30))

	// Background goroutine feeds PTY output to the virtual terminal.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				vt.Write(buf[:n])
				p.mu.Lock()
				p.output.Write(buf[:n])
				p.mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	p.cmd = cmd
	p.ptmx = ptmx
	p.vt = vt
	p.output.Reset()
	return nil
}

// SetPromptHandler attaches a PromptHandler for auto-responding to screen prompts.
func (p *ptyProvider) SetPromptHandler(ph *PromptHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.promptHandler = ph
}

// waitForPrompt polls the virtual terminal screen until the adapter's DetectPrompt returns true.
// Also auto-handles trust prompts by pressing Enter.
func (p *ptyProvider) waitForPrompt(ctx context.Context, ptmx *os.File, deadline time.Time) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for CLI prompt")
		}

		screen := p.vt.String()
		lower := strings.ToLower(screen)

		// Auto-handle prompts via PromptHandler (trust dialogs, permission prompts, etc.).
		if p.promptHandler != nil {
			action, response := p.promptHandler.Evaluate(screen)
			if action == PromptActionRespond && response != "" {
				ptmx.Write([]byte(response))
				time.Sleep(2 * time.Second)
				continue
			}
		} else {
			// Fallback: hardcoded trust dialog handling when no PromptHandler is configured.
			if (strings.Contains(lower, "trust") || strings.Contains(lower, "safety check")) &&
				(strings.Contains(screen, "Yes") || strings.Contains(screen, "Enter to confirm") || strings.Contains(screen, "Enter to select")) {
				ptmx.Write([]byte{'\r'})
				time.Sleep(2 * time.Second)
				continue
			}
		}

		// Only detect the actual input prompt if we're NOT in a trust/dialog screen.
		if strings.Contains(screen, "Confirm folder") || strings.Contains(screen, "safety check") {
			// Still in a dialog — wait
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if p.adapter.DetectPrompt(screen) {
			return nil
		}

		time.Sleep(300 * time.Millisecond)
	}
}

// readResponse polls the virtual terminal screen after sending a message.
// Emits text diffs as stream events until the adapter detects the response is done
// (typically when a new prompt appears after the response text).
// preScreen is the screen snapshot taken before the message was sent, used for
// diff-based extraction so only new content is collected.
func (p *ptyProvider) readResponse(ctx context.Context, ptmx *os.File, deadline time.Time, preScreen string, ch chan<- provider.StreamEvent) error {
	lastScreen := p.vt.String()
	preLines := screenLines(preScreen)
	var lastEmitted string

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			screen := p.vt.String()
			fmt.Fprintf(os.Stderr, "PTY %s: readResponse timeout, screen:\n---\n%s\n---\n", p.adapter.Name(), screen)
			ch <- provider.StreamEvent{Type: "done"}
			return nil
		}

		screen := p.vt.String()
		if screen != lastScreen {
			lastScreen = screen

			// Don't extract while thinking/loading indicators are visible.
			// Claude Code uses spinner symbols: ✢ ✻ ✽ ⏺ ◐ followed by text + …
			// Copilot uses: ◉/◎ Thinking (Esc to cancel)
			if isThinking(screen) {
				time.Sleep(500 * time.Millisecond)
				continue
			}

			// Extract response using both adapter-specific logic and screen diff.
			responseText := p.extractResponseDiff(screen, preLines)

			// Only emit new text that hasn't been emitted yet.
			if responseText != lastEmitted && len(responseText) > len(lastEmitted) {
				newText := responseText[len(lastEmitted):]
				if newText != "" {
					ch <- provider.StreamEvent{Type: "text", Text: newText}
					lastEmitted = responseText
				}
			}

			// Check if the response is complete (new prompt appeared).
			if p.adapter.DetectResponseEnd(screen) {
				ch <- provider.StreamEvent{Type: "done"}
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// extractResponse extracts the most recent assistant response from the virtual terminal screen.
// Handles multiple output formats:
//   - Claude Code: response text appears between greyed ❯ (user input) and new ❯ prompt
//   - Copilot: responses are prefixed with ● (bullet marker)
func (p *ptyProvider) extractResponse(screen string) string {
	lines := strings.Split(screen, "\n")
	var response []string

	// Strategy 1: Look for ● (Copilot-style response markers) that come AFTER
	// the user's ❯ input line. Skip system ● lines (💡, Environment).
	seenUserInput := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Track user input lines
		if strings.Contains(trimmed, "❯") && !strings.Contains(trimmed, "Type @") && !strings.Contains(trimmed, "1. Yes") {
			seenUserInput = true
			response = nil // reset — only collect responses after the LAST user input
		}
		// Collect response lines after user input
		if seenUserInput && strings.HasPrefix(trimmed, "●") && !strings.Contains(trimmed, "💡") && !strings.Contains(trimmed, "Environment") {
			text := strings.TrimSpace(strings.TrimPrefix(trimmed, "●"))
			if text != "" {
				response = append(response, text)
			}
		}
	}
	if len(response) > 0 {
		return strings.Join(response, "\n")
	}

	// Strategy 2: Look for text between greyed ❯ and the next ❯ prompt (Claude Code style).
	inResponse := false
	response = nil
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip empty lines and UI chrome
		if trimmed == "" {
			if inResponse {
				response = append(response, "")
			}
			continue
		}

		// Skip horizontal rules (box-drawing chars)
		if len(trimmed) > 5 && strings.Count(trimmed, "─") > len(trimmed)/2 {
			continue
		}

		// Skip box-drawing and UI elements
		if strings.HasPrefix(trimmed, "╭") || strings.HasPrefix(trimmed, "│") ||
		   strings.HasPrefix(trimmed, "╰") || strings.HasPrefix(trimmed, "?") ||
		   strings.Contains(trimmed, "Update available") ||
		   strings.Contains(trimmed, "shortcuts") ||
		   strings.Contains(trimmed, "/effort") ||
		   strings.Contains(trimmed, "MCP server") {
			continue
		}

		// The greyed ❯ marks a prior user input — response starts after this line
		if strings.Contains(line, "❯") && !inResponse {
			inResponse = true
			continue
		}

		// A new bright ❯ with empty or different content = new prompt = end
		if inResponse && strings.Contains(line, "❯") {
			break
		}

		if inResponse {
			response = append(response, trimmed)
		}
	}

	// Trim trailing empty lines
	for len(response) > 0 && response[len(response)-1] == "" {
		response = response[:len(response)-1]
	}

	return strings.Join(response, "\n")
}

// isThinking returns true if the screen shows thinking/loading indicators
// from either Claude Code or Copilot.
func isThinking(screen string) bool {
	// Copilot thinking
	if strings.Contains(screen, "◉") || strings.Contains(screen, "◎") ||
		strings.Contains(screen, "Thinking") {
		return true
	}
	// Claude Code thinking: any line that ends with "…" (ellipsis) and is short
	// (thinking indicators like "✢ Percolating…", "· Moseying…", "✶ Scampering…").
	// Also ⏺ alone on a line (response placeholder before text fills in).
	for _, line := range strings.Split(screen, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Bare ⏺ (response marker without content = still loading)
		if trimmed == "⏺" {
			return true
		}
		// Pattern: short line containing "…" with few words
		if strings.Contains(trimmed, "…") && len(trimmed) < 60 {
			words := strings.Fields(trimmed)
			if len(words) <= 3 {
				return true
			}
		}
	}
	return false
}

// screenLines splits a screen snapshot into trimmed non-empty line set for diffing.
func screenLines(screen string) map[string]bool {
	m := make(map[string]bool)
	for _, line := range strings.Split(screen, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed != "" {
			m[trimmed] = true
		}
	}
	return m
}

// extractResponseDiff extracts the assistant response using screen-diff logic.
// Lines that existed in the pre-message screen are excluded, ensuring only
// new content from the current turn is captured.
func (p *ptyProvider) extractResponseDiff(screen string, preLines map[string]bool) string {
	// Try adapter-specific extraction first, then filter against pre-screen.
	adapterResult := p.extractResponse(screen)
	if adapterResult != "" {
		// Filter out lines that existed before this turn.
		var filtered []string
		for _, line := range strings.Split(adapterResult, "\n") {
			trimmed := strings.TrimRight(line, " \t")
			if trimmed == "" {
				filtered = append(filtered, "")
				continue
			}
			if preLines[trimmed] || preLines[strings.TrimSpace(trimmed)] {
				continue
			}
			filtered = append(filtered, line)
		}
		// Trim leading/trailing empty lines.
		for len(filtered) > 0 && filtered[0] == "" {
			filtered = filtered[1:]
		}
		for len(filtered) > 0 && filtered[len(filtered)-1] == "" {
			filtered = filtered[:len(filtered)-1]
		}
		if result := strings.Join(filtered, "\n"); result != "" {
			return result
		}
	}

	// Fallback: pure diff — collect lines that are new since pre-snapshot.
	var newLines []string
	for _, line := range strings.Split(screen, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "" {
			continue
		}
		if preLines[trimmed] {
			continue
		}
		clean := strings.TrimSpace(trimmed)
		if clean == "" {
			continue
		}
		// Skip UI chrome
		if strings.HasPrefix(clean, "╭") || strings.HasPrefix(clean, "│") ||
			strings.HasPrefix(clean, "╰") || strings.HasPrefix(clean, "─") {
			continue
		}
		// Skip prompt lines
		if strings.Contains(clean, "❯") || strings.Contains(clean, "Type @") {
			continue
		}
		// Skip thinking/loading indicators
		if strings.Contains(clean, "Thinking") || strings.Contains(clean, "Queued") ||
			strings.Contains(clean, "◉") || strings.Contains(clean, "◎") ||
			strings.Contains(clean, "[pending]") || strings.Contains(clean, "Esc to cancel") {
			continue
		}
		// Skip status bar / mode line / UI chrome
		if strings.Contains(clean, "shift+tab") || strings.Contains(clean, "ctrl+q") ||
			strings.Contains(clean, "Remaining reqs") || strings.Contains(clean, "switch mode") ||
			strings.Contains(clean, "esc to interrupt") || strings.Contains(clean, "/effort") ||
			strings.Contains(clean, "ctrl+o to expand") {
			continue
		}
		newLines = append(newLines, clean)
	}

	return strings.Join(newLines, "\n")
}

// handleSessionEnd cleans up when the CLI process exits.
func (p *ptyProvider) handleSessionEnd() {
	p.mu.Lock()
	defer p.mu.Unlock()
	cmd := p.cmd
	p.ptmx = nil
	p.cmd = nil
	p.vt = nil
	if cmd != nil {
		_ = cmd.Wait()
	}
}

// Close kills the PTY process and cleans up.
func (p *ptyProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ptmx != nil {
		_ = p.ptmx.Close()
		p.ptmx = nil
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		p.cmd = nil
	}
	return nil
}

// flattenMessages converts a []provider.Message into a single prompt string.
// For CLI providers, we send the last user message as the prompt.
func flattenMessages(messages []provider.Message) string {
	// Walk in reverse to find the last user message.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == provider.RoleUser {
			return messages[i].Content
		}
	}
	// Fallback: concatenate all content.
	var sb strings.Builder
	for _, m := range messages {
		if m.Content != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(m.Content)
		}
	}
	return sb.String()
}

// streamJSON runs the CLI with structured JSON streaming args and parses events.
func (p *ptyProvider) streamJSON(ctx context.Context, args []string, ch chan<- provider.StreamEvent) error {
	timeout := p.timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.binPath, args...)
	if p.workDir != "" {
		cmd.Dir = p.workDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = nil // discard stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Read stdout line-by-line and parse each JSON event.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for large events
	for scanner.Scan() {
		line := scanner.Text()
		if text, ok := p.adapter.ParseStreamEvent(line); ok && text != "" {
			ch <- provider.StreamEvent{Type: "text", Text: text}
		}
	}

	ch <- provider.StreamEvent{Type: "done"}
	_ = cmd.Wait()
	return nil
}

// streamFallback runs the CLI non-interactively and emits the result as a single event.
func (p *ptyProvider) streamFallback(ctx context.Context, msg string, ch chan<- provider.StreamEvent) error {
	args := p.adapter.NonInteractiveArgs(msg)
	timeout := p.timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.binPath, args...)
	if p.workDir != "" {
		cmd.Dir = p.workDir
	}

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	content := p.adapter.ParseResponse(string(out))
	if content != "" {
		ch <- provider.StreamEvent{Type: "text", Text: content}
	}
	ch <- provider.StreamEvent{Type: "done"}
	return nil
}

// isTimeout returns true if err is a network/OS timeout error.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	type timeoutErr interface{ Timeout() bool }
	if te, ok := err.(timeoutErr); ok {
		return te.Timeout()
	}
	return false
}
