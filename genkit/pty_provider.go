package genkit

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/creack/pty"
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
	mu        sync.Mutex // guards ptmx, cmd, output field pointers
	sessionMu sync.Mutex // serializes full turn lifecycle (prompt→send→read)
	ptmx      *os.File   // PTY master — nil when no active session
	cmd       *exec.Cmd  // running CLI process
	output    bytes.Buffer
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

// Stream runs the CLI tool and streams response events.
// If the adapter supports StreamingArgs (JSON streaming), uses that for reliable
// structured output. Otherwise falls back to non-interactive exec with line-by-line reading.
func (p *ptyProvider) Stream(ctx context.Context, messages []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	msg := flattenMessages(messages)

	ch := make(chan provider.StreamEvent, 32)

	go func() {
		defer close(ch)

		// Prefer structured JSON streaming if the adapter supports it.
		if args := p.adapter.StreamingArgs(msg); args != nil {
			if err := p.streamJSON(ctx, args, ch); err != nil {
				ch <- provider.StreamEvent{Type: "error", Error: err.Error()}
			}
			return
		}

		// Fallback: run non-interactive and emit result as single event.
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
		return fmt.Errorf("waiting for prompt: %w", err)
	}

	// Send the message.
	if _, err := fmt.Fprintf(ptmx, "%s\n", msg); err != nil {
		return fmt.Errorf("writing to PTY: %w", err)
	}

	// Reset output accumulator for this turn.
	p.mu.Lock()
	p.output.Reset()
	p.mu.Unlock()

	// Read output and emit stream events until response ends.
	return p.readResponse(ctx, ptmx, deadline, ch)
}

// startSession forks the CLI process under a PTY. Caller must hold p.mu.
func (p *ptyProvider) startSession() error {
	cmd := exec.Command(p.binPath)
	if p.workDir != "" {
		cmd.Dir = p.workDir
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return fmt.Errorf("pty.StartWithSize: %w", err)
	}

	p.cmd = cmd
	p.ptmx = ptmx
	p.output.Reset()
	return nil
}

// waitForPrompt reads PTY output until the adapter's DetectPrompt returns true.
func (p *ptyProvider) waitForPrompt(ctx context.Context, ptmx *os.File, deadline time.Time) error {
	buf := make([]byte, 4096)
	var accumulated strings.Builder

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for CLI prompt")
		}

		_ = ptmx.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := ptmx.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			accumulated.WriteString(chunk)
			if p.adapter.DetectPrompt(accumulated.String()) {
				return nil
			}
		}
		if err != nil && !isTimeout(err) {
			return fmt.Errorf("reading PTY: %w", err)
		}
	}
}

// readResponse reads PTY output after sending a message, emitting stream events.
func (p *ptyProvider) readResponse(ctx context.Context, ptmx *os.File, deadline time.Time, ch chan<- provider.StreamEvent) error {
	buf := make([]byte, 4096)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for response")
		}

		_ = ptmx.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := ptmx.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])

			p.mu.Lock()
			p.output.WriteString(chunk)
			accumulated := p.output.String()
			p.mu.Unlock()

			// Emit text chunk (tool approval prompts pass through as text).
			ch <- provider.StreamEvent{Type: "text", Text: chunk}

			if p.adapter.DetectResponseEnd(accumulated) {
				ch <- provider.StreamEvent{Type: "done"}
				return nil
			}
		}
		if err != nil && !isTimeout(err) {
			if err == io.EOF {
				// Process exited — reap it to avoid zombie, then clean up session.
				p.mu.Lock()
				cmd := p.cmd
				p.ptmx = nil
				p.cmd = nil
				p.mu.Unlock()
				if cmd != nil {
					_ = cmd.Wait()
				}
				ch <- provider.StreamEvent{Type: "done"}
				return nil
			}
			return fmt.Errorf("reading PTY response: %w", err)
		}
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
