package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/GoCodeAlone/modular"
	ratchetplugin_pkg "github.com/GoCodeAlone/workflow-plugin-agent/plugin"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/plugin"
)

// mcpProtocolVersion is the MCP protocol version sent during initialization.
const mcpProtocolVersion = "2024-11-05"

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpClient manages a connection to a single MCP server via stdio.
type mcpClient struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	mu     sync.Mutex
	nextID atomic.Int64
}

func newMCPClient(name, command string, args []string) (*mcpClient, error) {
	cmd := exec.Command(command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp client %s: stdin pipe: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp client %s: stdout pipe: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp client %s: start: %w", name, err)
	}
	return &mcpClient{
		name:   name,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewScanner(stdout),
	}, nil
}

func (c *mcpClient) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	data, _ := json.Marshal(req)
	data = append(data, '\n')

	if _, err := c.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	if !c.stdout.Scan() {
		return nil, fmt.Errorf("read response: EOF")
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(c.stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

func (c *mcpClient) close() error {
	_ = c.stdin.Close()
	return c.cmd.Wait()
}

// mcpToolListResult matches the MCP tools/list response.
type mcpToolListResult struct {
	Tools []mcpToolInfo `json:"tools"`
}

type mcpToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// MCPToolAdapter wraps an MCP tool as a ratchet plugin.Tool.
type MCPToolAdapter struct {
	client *mcpClient
	info   mcpToolInfo
}

func (t *MCPToolAdapter) Name() string        { return t.info.Name }
func (t *MCPToolAdapter) Description() string { return t.info.Description }
func (t *MCPToolAdapter) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.info.Name,
		Description: t.info.Description,
		Parameters:  t.info.InputSchema,
	}
}
func (t *MCPToolAdapter) Execute(ctx context.Context, args map[string]any) (any, error) {
	params := map[string]any{
		"name":      t.info.Name,
		"arguments": args,
	}
	result, err := t.client.call("tools/call", params)
	if err != nil {
		return nil, err
	}
	var out any
	_ = json.Unmarshal(result, &out)
	return out, nil
}

// MCPClientModule connects to external MCP servers.
type MCPClientModule struct {
	name     string
	clients  map[string]*mcpClient
	registry *ToolRegistry
	servers  []mcpServerConfig
}

type mcpServerConfig struct {
	Name    string   `json:"name" yaml:"name"`
	Command string   `json:"command" yaml:"command"`
	Args    []string `json:"args" yaml:"args"`
}

func (m *MCPClientModule) Name() string { return m.name }

func (m *MCPClientModule) Init(app modular.Application) error {
	// Look up ToolRegistry from service registry
	if svc, ok := app.SvcRegistry()["ratchet-tool-registry"]; ok {
		if tr, ok := svc.(*ToolRegistry); ok {
			m.registry = tr
		}
	}
	return app.RegisterService(m.name, m)
}

func (m *MCPClientModule) ProvidesServices() []modular.ServiceProvider {
	return []modular.ServiceProvider{
		{Name: m.name, Description: "Ratchet MCP client: " + m.name, Instance: m},
	}
}

func (m *MCPClientModule) RequiresServices() []modular.ServiceDependency {
	return nil
}

func (m *MCPClientModule) Start(ctx context.Context) error {
	m.clients = make(map[string]*mcpClient)
	for _, srv := range m.servers {
		if srv.Command == "" {
			continue
		}
		client, err := newMCPClient(srv.Name, srv.Command, srv.Args)
		if err != nil {
			// Log but don't fail — MCP servers are optional
			continue
		}
		m.clients[srv.Name] = client

		// Initialize the MCP connection
		_, _ = client.call("initialize", map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "ratchet",
				"version": "1.0.0",
			},
		})

		// Discover tools
		result, err := client.call("tools/list", nil)
		if err != nil {
			continue
		}
		var toolList mcpToolListResult
		if err := json.Unmarshal(result, &toolList); err != nil {
			continue
		}

		// Register tools
		if m.registry != nil {
			var adapted []ratchetplugin_pkg.Tool
			for _, info := range toolList.Tools {
				adapted = append(adapted, &MCPToolAdapter{client: client, info: info})
			}
			m.registry.RegisterMCP(srv.Name, adapted)
		}
	}
	return nil
}

func (m *MCPClientModule) Stop(_ context.Context) error {
	for _, c := range m.clients {
		_ = c.close()
	}
	return nil
}

// ReloadServers stops all existing MCP clients and starts new ones from the given configs.
// Returns the count of successfully started servers and any error messages.
func (m *MCPClientModule) ReloadServers(configs []mcpServerConfig) (int, []string) {
	// Stop and unregister existing clients
	for name, c := range m.clients {
		_ = c.close()
		if m.registry != nil {
			m.registry.UnregisterMCP(name)
		}
	}
	m.clients = make(map[string]*mcpClient)
	m.servers = configs

	reloaded := 0
	var errors []string

	for _, srv := range configs {
		if srv.Command == "" {
			continue
		}
		client, err := newMCPClient(srv.Name, srv.Command, srv.Args)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", srv.Name, err))
			continue
		}
		m.clients[srv.Name] = client

		// Initialize
		_, _ = client.call("initialize", map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "ratchet",
				"version": "1.0.0",
			},
		})

		// Discover tools
		result, err := client.call("tools/list", nil)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: tool discovery failed: %v", srv.Name, err))
			reloaded++
			continue
		}
		var toolList mcpToolListResult
		if err := json.Unmarshal(result, &toolList); err != nil {
			errors = append(errors, fmt.Sprintf("%s: tool parse failed: %v", srv.Name, err))
			reloaded++
			continue
		}

		if m.registry != nil {
			var adapted []ratchetplugin_pkg.Tool
			for _, info := range toolList.Tools {
				adapted = append(adapted, &MCPToolAdapter{client: client, info: info})
			}
			m.registry.RegisterMCP(srv.Name, adapted)
		}
		reloaded++
	}

	return reloaded, errors
}

func newMCPClientFactory() plugin.ModuleFactory {
	return func(name string, cfg map[string]any) modular.Module {
		mod := &MCPClientModule{
			name: name,
		}
		// Parse servers from config
		if serversRaw, ok := cfg["servers"]; ok {
			if serversList, ok := serversRaw.([]any); ok {
				for _, item := range serversList {
					if m, ok := item.(map[string]any); ok {
						srv := mcpServerConfig{}
						if v, ok := m["name"].(string); ok {
							srv.Name = v
						}
						if v, ok := m["command"].(string); ok {
							srv.Command = v
						}
						if v, ok := m["args"].([]any); ok {
							for _, a := range v {
								if s, ok := a.(string); ok {
									srv.Args = append(srv.Args, s)
								}
							}
						}
						mod.servers = append(mod.servers, srv)
					}
				}
			}
		}
		return mod
	}
}
