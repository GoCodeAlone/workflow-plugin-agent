package genkit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

// TestClaudeCodeScreenDump launches Claude Code in a PTY with vt10x and dumps
// the raw screen content at various stages. Used to debug extractResponse patterns.
func TestClaudeCodeScreenDump(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not in PATH")
	}
	if os.Getenv("PTY_INTEGRATION") == "" {
		t.Skip("set PTY_INTEGRATION=1")
	}

	cmd := exec.Command("claude")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100})
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	defer ptmx.Close()
	defer cmd.Process.Kill()

	vt := vt10x.New(vt10x.WithSize(100, 30))

	// Background reader
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				vt.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// Wait for initial screen
	t.Log("Waiting for Claude Code to start...")
	time.Sleep(10 * time.Second)
	screen := vt.String()
	t.Logf("=== INITIAL SCREEN ===\n%s\n=== END ===", screen)

	// Check for trust prompt
	lower := strings.ToLower(screen)
	if strings.Contains(lower, "trust") || strings.Contains(lower, "safety") {
		t.Log("Trust prompt detected, pressing Enter...")
		ptmx.Write([]byte{'\r'})
		time.Sleep(3 * time.Second)
		screen = vt.String()
		t.Logf("=== AFTER TRUST ===\n%s\n=== END ===", screen)
	}

	// Check for prompt
	adapter := ClaudeCodeAdapter{}

	// Debug: dump bytes around ❯ to check if it's the expected character
	for i, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, "❯") || strings.Contains(line, "\xe2\x9d\xaf") {
			t.Logf("Line %d with ❯: %q", i, line)
			t.Logf("  promptRegex match: %v", promptRegex.MatchString(line))
		}
		// Also check for Unicode heavy right-pointing angle (U+276F) which looks like ❯
		for _, r := range line {
			if r == '❯' || r == '>' || r == 0x276F {
				t.Logf("  Found rune %U at line %d", r, i)
			}
		}
	}

	if !adapter.DetectPrompt(screen) {
		t.Log("Prompt NOT detected after stripANSI.")
		clean := stripANSI(screen)
		for i, line := range strings.Split(clean, "\n") {
			if strings.Contains(line, "❯") {
				t.Logf("Clean line %d with ❯: %q", i, line)
			}
		}
		t.Log("Waiting 10 more seconds...")
		time.Sleep(10 * time.Second)
		screen = vt.String()
		t.Logf("=== AFTER WAIT ===\n%s\n=== END ===", screen)
		if !adapter.DetectPrompt(screen) {
			t.Fatalf("Prompt still not detected after 20s")
		}
	}
	t.Log("Prompt detected!")

	// Send a simple message
	msg := "Say just the word hello"
	for _, ch := range msg {
		ptmx.Write([]byte(string(ch)))
		time.Sleep(10 * time.Millisecond)
	}
	ptmx.Write([]byte{'\r'})
	t.Log("Message sent, waiting for response...")

	// Poll for response
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			screen = vt.String()
			t.Logf("=== TIMEOUT SCREEN ===\n%s\n=== END ===", screen)
			t.Fatal("Timed out waiting for response")
		default:
		}

		time.Sleep(2 * time.Second)
		screen = vt.String()

		// Check if response ended
		if adapter.DetectResponseEnd(screen) {
			t.Logf("=== RESPONSE COMPLETE SCREEN ===\n%s\n=== END ===", screen)
			break
		}

		// Log progress
		lines := strings.Split(screen, "\n")
		nonEmpty := 0
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				nonEmpty++
			}
		}
		fmt.Fprintf(os.Stderr, "screen: %d non-empty lines, DetectResponseEnd=%v\n", nonEmpty, adapter.DetectResponseEnd(screen))
	}

	// Extract response
	result := (&ptyProvider{adapter: adapter}).extractResponse(screen)
	t.Logf("Extracted response: %q", result)
}
