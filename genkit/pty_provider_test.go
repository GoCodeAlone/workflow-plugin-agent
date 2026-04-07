package genkit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// mockCLIAdapter is a test adapter that drives a shell script mock CLI.
type mockCLIAdapter struct {
	name    string
	binary  string
	workDir string
}

func (m *mockCLIAdapter) Name() string  { return m.name }
func (m *mockCLIAdapter) Binary() string { return m.binary }

func (m *mockCLIAdapter) NonInteractiveArgs(msg string) []string {
	return []string{"--print", msg}
}

func (m *mockCLIAdapter) HealthCheckArgs() []string {
	return []string{"--print", "ok"}
}

func (m *mockCLIAdapter) DetectPrompt(output string) bool {
	return len(output) > 0 && output[len(output)-1] == '>'
}

func (m *mockCLIAdapter) DetectResponseEnd(output string) bool {
	// Count prompt chars.
	count := 0
	for _, ch := range output {
		if ch == '>' {
			count++
		}
	}
	return count >= 2
}

func (m *mockCLIAdapter) ParseResponse(raw string) string {
	return stripANSI(raw)
}

func (m *mockCLIAdapter) SupportsInteractivePTY() bool             { return false }
func (m *mockCLIAdapter) InteractiveArgs() []string                { return nil }
func (m *mockCLIAdapter) StreamingArgs(_ string) []string          { return nil }
func (m *mockCLIAdapter) ParseStreamEvent(_ string) (string, bool) { return "", false }

// buildMockCLI compiles a simple Go program that simulates a CLI for testing.
// It accepts --print <msg> for non-interactive mode.
func buildMockCLI(t *testing.T) string {
	t.Helper()

	src := `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]
	if len(args) >= 2 && args[0] == "--print" {
		msg := strings.Join(args[1:], " ")
		fmt.Printf("RESPONSE: %s\n", msg)
		os.Exit(0)
	}
	fmt.Println("unknown args")
	os.Exit(1)
}
`

	dir := t.TempDir()
	srcFile := filepath.Join(dir, "mock_cli.go")
	binFile := filepath.Join(dir, "mock_cli")

	if err := os.WriteFile(srcFile, []byte(src), 0644); err != nil {
		t.Fatalf("write mock CLI src: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", binFile, srcFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mock CLI: %v\n%s", err, out)
	}

	return binFile
}

func TestPTYProviderChat(t *testing.T) {
	binPath := buildMockCLI(t)

	adapter := &mockCLIAdapter{
		name:   "mock_cli",
		binary: filepath.Base(binPath),
	}

	p := &ptyProvider{
		adapter: adapter,
		binPath: binPath,
		timeout: 30 * time.Second,
		authInfo: provider.AuthModeInfo{
			Mode: "none",
		},
	}

	ctx := context.Background()
	resp, err := p.Chat(ctx, []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp == nil {
		t.Fatal("Chat() returned nil response")
	}
	if resp.Content == "" {
		t.Error("Chat() returned empty content")
	}
}

func TestPTYProviderName(t *testing.T) {
	adapter := ClaudeCodeAdapter{}
	p := &ptyProvider{
		adapter:  adapter,
		binPath:  "/usr/local/bin/claude",
		authInfo: provider.AuthModeInfo{Mode: "none"},
	}
	if p.Name() != "claude_code" {
		t.Errorf("Name() = %q, want %q", p.Name(), "claude_code")
	}
}

func TestPTYProviderAuthModeInfo(t *testing.T) {
	p := &ptyProvider{
		adapter: ClaudeCodeAdapter{},
		authInfo: provider.AuthModeInfo{
			Mode:        "none",
			DisplayName: "claude_code",
		},
	}
	info := p.AuthModeInfo()
	if info.Mode != "none" {
		t.Errorf("AuthModeInfo().Mode = %q, want %q", info.Mode, "none")
	}
}

func TestFlattenMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helper."},
		{Role: provider.RoleUser, Content: "What is 2+2?"},
	}
	got := flattenMessages(msgs)
	if got != "What is 2+2?" {
		t.Errorf("flattenMessages = %q, want last user message", got)
	}
}

func TestFlattenMessagesNoUser(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system prompt"},
	}
	got := flattenMessages(msgs)
	if got != "system prompt" {
		t.Errorf("flattenMessages = %q, want %q", got, "system prompt")
	}
}

func TestNewClaudeCodeProviderMissingBinary(t *testing.T) {
	// Override PATH to ensure the binary isn't found.
	old := os.Getenv("PATH")
	_ = os.Setenv("PATH", t.TempDir()) // empty dir, no binaries
	defer func() { _ = os.Setenv("PATH", old) }()

	_, err := NewClaudeCodeProvider("")
	if err == nil {
		t.Error("expected error when binary not in PATH, got nil")
	}
}

func TestNewCopilotCLIProviderMissingBinary(t *testing.T) {
	old := os.Getenv("PATH")
	_ = os.Setenv("PATH", t.TempDir())
	defer func() { _ = os.Setenv("PATH", old) }()

	_, err := NewCopilotCLIProvider("")
	if err == nil {
		t.Error("expected error when binary not in PATH, got nil")
	}
}

func TestIsTimeout(t *testing.T) {
	if isTimeout(nil) {
		t.Error("isTimeout(nil) should be false")
	}
	if isTimeout(fmt.Errorf("regular error")) {
		t.Error("isTimeout(regular error) should be false")
	}
}

func TestPTYProviderClose(t *testing.T) {
	p := &ptyProvider{
		adapter:  ClaudeCodeAdapter{},
		authInfo: provider.AuthModeInfo{Mode: "none"},
	}
	// Close with no active session should not panic or error.
	if err := p.Close(); err != nil {
		t.Errorf("Close() on idle provider = %v", err)
	}
}
