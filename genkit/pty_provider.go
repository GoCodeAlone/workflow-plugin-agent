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
	output    bytes.Buffer   // raw output accumulator (for fallback parsing)
}

// Name implements provider.Provider.
func (p *ptyProvider) Name() string {
	return p.adapter.Name()
}

// AuthModeInfo implements provider.Provider.
func (p *ptyProvider) AuthModeInfo() provider.AuthModeInfo {
	return p.authInfo
}

// Chat runs the CLI in single-shot (non-interactive) mode and returns the response.
// This is stateless — no PTY is kept alive.
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

// Stream uses the interactive PTY (vt10x) path to maintain session context
// across multiple calls. The PTY session is kept alive for multi-turn conversation.
// Falls back to JSON streaming or non-interactive exec if the interactive session fails.
func (p *ptyProvider) Stream(ctx context.Context, messages []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	msg := flattenMessages(messages)

	ch := make(chan provider.StreamEvent, 32)

	go func() {
		defer close(ch)

		// Primary: interactive PTY session via vt10x virtual terminal.
		if err := p.streamInteractive(ctx, msg, ch); err != nil {
			// If interactive fails, try JSON streaming as fallback.
			if args := p.adapter.StreamingArgs(msg); args != nil {
				if jsonErr := p.streamJSON(ctx, args, ch); jsonErr == nil {
					return
				}
			}
			// Last resort: non-interactive exec.
			if fallbackErr := p.streamFallback(ctx, msg, ch); fallbackErr != nil {
				ch <- provider.StreamEvent{Type: "error", Error: err.Error()}
			}
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
		return fmt.Errorf("waiting for prompt: %w", err)
	}

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

	// Read output and emit stream events until response ends.
	return p.readResponse(ctx, ptmx, deadline, ch)
}

// startSession forks the CLI process under a PTY with a virtual terminal.
// Caller must hold p.mu.
func (p *ptyProvider) startSession() error {
	cmd := exec.Command(p.binPath)
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

		// Auto-handle trust prompts (e.g., "trust this folder" in Claude Code)
		if strings.Contains(screen, "trust") && strings.Contains(screen, "Yes") {
			ptmx.Write([]byte{'\r'})
			time.Sleep(1 * time.Second)
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
func (p *ptyProvider) readResponse(ctx context.Context, ptmx *os.File, deadline time.Time, ch chan<- provider.StreamEvent) error {
	// Snapshot the screen before the response to diff against.
	lastScreen := p.vt.String()
	var lastEmitted string

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			// On timeout, emit whatever we have and return done.
			ch <- provider.StreamEvent{Type: "done"}
			return nil
		}

		screen := p.vt.String()
		if screen != lastScreen {
			lastScreen = screen

			// Extract response text from screen (content between user message and next prompt).
			responseText := p.extractResponse(screen)

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

// extractResponse extracts the assistant's response text from the virtual terminal screen.
// It looks for text between the user's message and the next prompt indicator.
func (p *ptyProvider) extractResponse(screen string) string {
	lines := strings.Split(screen, "\n")
	var response []string
	inResponse := false

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
			if inResponse {
				// A horizontal rule after response content likely means end of response area
				continue
			}
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
