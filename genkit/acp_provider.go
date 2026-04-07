package genkit

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// acpProvider implements provider.Provider by driving an ACP-compliant agent
// via stdio JSON-RPC. It launches the agent binary, connects as an ACP client,
// and translates between the provider interface and ACP protocol.
type acpProvider struct {
	name     string
	binPath  string
	args     []string // extra args passed to the agent binary
	workDir  string
	timeout  time.Duration
	authInfo provider.AuthModeInfo

	mu        sync.Mutex
	cmd       *exec.Cmd
	conn      *acpsdk.ClientSideConnection
	client    *acpClient
	stdin     io.WriteCloser
	sessionID acpsdk.SessionId
}

// acpClient implements acp.Client to receive session updates from the agent.
type acpClient struct {
	mu      sync.Mutex
	updates []acpsdk.SessionUpdate
}

func (c *acpClient) ReadTextFile(_ context.Context, p acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	b, err := os.ReadFile(p.Path)
	if err != nil {
		return acpsdk.ReadTextFileResponse{}, err
	}
	return acpsdk.ReadTextFileResponse{Content: string(b)}, nil
}

func (c *acpClient) WriteTextFile(_ context.Context, p acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, os.WriteFile(p.Path, []byte(p.Content), 0o644)
}

func (c *acpClient) RequestPermission(_ context.Context, p acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	// Auto-approve for agent orchestration.
	if len(p.Options) > 0 {
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.NewRequestPermissionOutcomeSelected(p.Options[0].OptionId),
		}, nil
	}
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeCancelled(),
	}, nil
}

func (c *acpClient) SessionUpdate(_ context.Context, n acpsdk.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, n.Update)
	c.mu.Unlock()
	return nil
}

func (c *acpClient) CreateTerminal(_ context.Context, _ acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{TerminalId: "t-1"}, nil
}

func (c *acpClient) KillTerminalCommand(_ context.Context, _ acpsdk.KillTerminalCommandRequest) (acpsdk.KillTerminalCommandResponse, error) {
	return acpsdk.KillTerminalCommandResponse{}, nil
}

func (c *acpClient) TerminalOutput(_ context.Context, _ acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{Output: "", Truncated: false}, nil
}

func (c *acpClient) ReleaseTerminal(_ context.Context, _ acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, nil
}

func (c *acpClient) WaitForTerminalExit(_ context.Context, _ acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, nil
}

// NewACPProvider creates a provider that drives an ACP-compliant agent binary.
func NewACPProvider(name, binPath string, args []string, workDir string) (provider.Provider, error) {
	if binPath == "" {
		return nil, fmt.Errorf("acp provider %s: binary path required", name)
	}
	if _, err := exec.LookPath(binPath); err != nil {
		return nil, fmt.Errorf("acp provider %s: binary not found: %w", name, err)
	}

	return &acpProvider{
		name:    name,
		binPath: binPath,
		args:    args,
		workDir: workDir,
		timeout: 5 * time.Minute,
		authInfo: provider.AuthModeInfo{
			Mode:        "none",
			DisplayName: "acp:" + name,
		},
	}, nil
}

func (p *acpProvider) Name() string                       { return p.name }
func (p *acpProvider) AuthModeInfo() provider.AuthModeInfo { return p.authInfo }

// ensureConnection starts the agent process and initializes the ACP connection.
func (p *acpProvider) ensureConnection(ctx context.Context) (*acpsdk.ClientSideConnection, *acpClient, acpsdk.SessionId, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn != nil {
		// Check if process is still alive.
		select {
		case <-p.conn.Done():
			// Connection closed, restart.
			p.cleanup()
		default:
			// Clear any leftover updates from previous calls.
			p.client.mu.Lock()
			p.client.updates = nil
			p.client.mu.Unlock()
			return p.conn, p.client, p.sessionID, nil
		}
	}

	cmd := exec.Command(p.binPath, p.args...) // Don't tie to request context; process is long-lived.
	if p.workDir != "" {
		cmd.Dir = p.workDir
	}
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, "", fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, "", fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, "", fmt.Errorf("start agent: %w", err)
	}

	client := &acpClient{}
	conn := acpsdk.NewClientSideConnection(client, stdin, stdout)

	// Initialize the protocol.
	_, err = conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{
			Fs: acpsdk.FileSystemCapability{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Terminal: true,
		},
		ClientInfo: &acpsdk.Implementation{
			Name:    "ratchet-orchestrator",
			Version: "1.0.0",
		},
	})
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, nil, "", fmt.Errorf("initialize: %w", err)
	}

	// Create a session.
	cwd := p.workDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	sessResp, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, nil, "", fmt.Errorf("new session: %w", err)
	}

	p.cmd = cmd
	p.conn = conn
	p.client = client
	p.stdin = stdin
	p.sessionID = sessResp.SessionId

	return conn, client, sessResp.SessionId, nil
}

func (p *acpProvider) cleanup() {
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	p.cmd = nil
	p.conn = nil
	p.client = nil
	p.stdin = nil
	p.sessionID = ""
}

// Chat sends a prompt to the ACP agent and collects the full response.
func (p *acpProvider) Chat(ctx context.Context, messages []provider.Message, _ []provider.ToolDef) (*provider.Response, error) {
	conn, client, sessID, err := p.ensureConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("acp provider %s: %w", p.name, err)
	}

	msg := flattenMessages(messages)
	prompt := []acpsdk.ContentBlock{acpsdk.TextBlock(msg)}

	_, err = conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sessID,
		Prompt:    prompt,
	})
	if err != nil {
		return nil, fmt.Errorf("acp provider %s: prompt: %w", p.name, err)
	}

	// Collect text from session updates.
	var content strings.Builder
	client.mu.Lock()
	for _, u := range client.updates {
		if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
			content.WriteString(u.AgentMessageChunk.Content.Text.Text)
		}
	}
	client.updates = nil
	client.mu.Unlock()

	return &provider.Response{Content: content.String()}, nil
}

// Stream sends a prompt and returns streaming events.
func (p *acpProvider) Stream(ctx context.Context, messages []provider.Message, _ []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	conn, client, sessID, err := p.ensureConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("acp provider %s: %w", p.name, err)
	}

	ch := make(chan provider.StreamEvent, 32)
	msg := flattenMessages(messages)
	prompt := []acpsdk.ContentBlock{acpsdk.TextBlock(msg)}

	go func() {
		defer close(ch)

		_, err := conn.Prompt(ctx, acpsdk.PromptRequest{
			SessionId: sessID,
			Prompt:    prompt,
		})

		// Drain collected updates as stream events.
		client.mu.Lock()
		for _, u := range client.updates {
			switch {
			case u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil:
				ch <- provider.StreamEvent{Type: "text", Text: u.AgentMessageChunk.Content.Text.Text}
			case u.AgentThoughtChunk != nil && u.AgentThoughtChunk.Content.Text != nil:
				ch <- provider.StreamEvent{Type: "thinking", Thinking: u.AgentThoughtChunk.Content.Text.Text}
			case u.ToolCall != nil:
				ch <- provider.StreamEvent{
					Type: "tool_call",
					Tool: &provider.ToolCall{
						ID:   string(u.ToolCall.ToolCallId),
						Name: u.ToolCall.Title,
					},
				}
			}
		}
		client.updates = nil
		client.mu.Unlock()

		if err != nil {
			ch <- provider.StreamEvent{Type: "error", Error: err.Error()}
			return
		}
		ch <- provider.StreamEvent{Type: "done"}
	}()

	return ch, nil
}

// Close terminates the agent process.
func (p *acpProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanup()
	return nil
}
