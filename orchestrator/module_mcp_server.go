package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"github.com/google/uuid"
)

// MCPServerModule exposes Ratchet APIs as MCP tools over HTTP/JSON-RPC.
type MCPServerModule struct {
	name string
	path string
	db   *sql.DB
	app  modular.Application
}

func (m *MCPServerModule) Name() string { return m.name }

func (m *MCPServerModule) Init(app modular.Application) error {
	m.app = app
	return app.RegisterService(m.name, m)
}

func (m *MCPServerModule) ProvidesServices() []modular.ServiceProvider {
	return []modular.ServiceProvider{
		{Name: m.name, Description: "Ratchet MCP server: " + m.name, Instance: m},
	}
}

func (m *MCPServerModule) RequiresServices() []modular.ServiceDependency {
	return []modular.ServiceDependency{
		{Name: "ratchet-db", Required: false},
	}
}

func (m *MCPServerModule) Start(_ context.Context) error {
	// Resolve DB in Start() — SQLiteStorage opens connection during Start, not Init
	if svc, ok := m.app.SvcRegistry()["ratchet-db"]; ok {
		if dbp, ok := svc.(module.DBProvider); ok {
			m.db = dbp.DB()
		}
	}
	return nil
}
func (m *MCPServerModule) Stop(_ context.Context) error { return nil }

// Path returns the configured MCP endpoint path.
func (m *MCPServerModule) Path() string { return m.path }

// ServeHTTP handles MCP JSON-RPC requests over HTTP POST.
func (m *MCPServerModule) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONRPCErrorResp(w, 0, -32700, "parse error")
		return
	}

	result, rpcErr := m.handleMethod(r.Context(), req.Method, req.Params)
	if rpcErr != nil {
		writeJSONRPCErrorResp(w, req.ID, rpcErr.Code, rpcErr.Message)
		return
	}

	resultJSON, _ := json.Marshal(result)
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  resultJSON,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *MCPServerModule) handleMethod(ctx context.Context, method string, params any) (any, *jsonRPCError) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ratchet", "version": "1.0.0"},
		}, nil
	case "tools/list":
		return m.toolsList(), nil
	case "tools/call":
		return m.toolsCall(ctx, params)
	default:
		return nil, &jsonRPCError{Code: -32601, Message: "method not found"}
	}
}

func (m *MCPServerModule) toolsList() map[string]any {
	tools := []map[string]any{
		{
			"name": "ratchet_list_agents", "description": "List all agents",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "ratchet_create_agent", "description": "Create a new agent",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":          map[string]any{"type": "string"},
					"role":          map[string]any{"type": "string"},
					"system_prompt": map[string]any{"type": "string"},
					"team_id":       map[string]any{"type": "string"},
				},
				"required": []string{"name"},
			},
		},
		{
			"name": "ratchet_list_tasks", "description": "List all tasks",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "ratchet_create_task", "description": "Create a new task",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":       map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
					"assigned_to": map[string]any{"type": "string"},
					"project_id":  map[string]any{"type": "string"},
				},
				"required": []string{"title"},
			},
		},
		{
			"name": "ratchet_update_task", "description": "Update a task",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     map[string]any{"type": "string"},
					"status": map[string]any{"type": "string"},
					"result": map[string]any{"type": "string"},
				},
				"required": []string{"id"},
			},
		},
		{
			"name": "ratchet_list_projects", "description": "List all projects",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "ratchet_send_message", "description": "Send a message to an agent",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from":    map[string]any{"type": "string"},
					"to":      map[string]any{"type": "string"},
					"subject": map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []string{"to", "content"},
			},
		},
		{
			"name": "ratchet_start_agent", "description": "Start an agent",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
				},
				"required": []string{"id"},
			},
		},
		{
			"name": "ratchet_stop_agent", "description": "Stop an agent",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
				},
				"required": []string{"id"},
			},
		},
		{
			"name": "ratchet_git_log_stats", "description": "Analyze git history for file change frequency and contributor activity",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo_path": map[string]any{"type": "string"},
					"days":      map[string]any{"type": "integer"},
					"limit":     map[string]any{"type": "integer"},
				},
				"required": []string{"repo_path"},
			},
		},
		{
			"name": "ratchet_test_coverage", "description": "Analyze Go test coverage per package",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":     map[string]any{"type": "string"},
					"packages": map[string]any{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
		{
			"name": "ratchet_deployment_status", "description": "Check Kubernetes deployment rollout status",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"deployment": map[string]any{"type": "string"},
					"namespace":  map[string]any{"type": "string"},
				},
				"required": []string{"deployment"},
			},
		},
		{
			"name": "ratchet_k8s_top", "description": "Get resource usage (CPU/memory) for pods or nodes",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"resource":  map[string]any{"type": "string"},
					"namespace": map[string]any{"type": "string"},
					"selector":  map[string]any{"type": "string"},
				},
				"required": []string{"resource"},
			},
		},
		{
			"name": "ratchet_compliance_report", "description": "Generate compliance report tagged to CIS/OWASP/SOC2 frameworks",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"framework": map[string]any{"type": "string"},
				},
			},
		},
		{
			"name": "ratchet_secret_audit", "description": "Audit secrets for age, rotation needs, and unused entries",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"max_age_days": map[string]any{"type": "integer"},
				},
			},
		},
		{
			"name": "ratchet_schema_inspect", "description": "Inspect database schema structure, columns, indexes, and foreign keys",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"table": map[string]any{"type": "string"},
				},
			},
		},
		{
			"name": "ratchet_data_profile", "description": "Profile data quality: null rates, cardinality, min/max, distributions",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"table":       map[string]any{"type": "string"},
					"sample_size": map[string]any{"type": "integer"},
				},
				"required": []string{"table"},
			},
		},
	}
	return map[string]any{"tools": tools}
}

func (m *MCPServerModule) toolsCall(ctx context.Context, params any) (any, *jsonRPCError) {
	paramsMap, ok := params.(map[string]any)
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params"}
	}
	toolName, _ := paramsMap["name"].(string)
	args, _ := paramsMap["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}

	if m.db == nil {
		return nil, &jsonRPCError{Code: -32603, Message: "database not available"}
	}

	switch toolName {
	case "ratchet_list_agents":
		return m.queryRows(ctx, "SELECT id, name, role, status, team_id FROM agents ORDER BY created_at ASC")
	case "ratchet_create_agent":
		return m.createAgent(ctx, args)
	case "ratchet_list_tasks":
		return m.queryRows(ctx, "SELECT id, title, status, priority, assigned_to, project_id FROM tasks ORDER BY created_at DESC LIMIT 100")
	case "ratchet_create_task":
		return m.createTask(ctx, args)
	case "ratchet_update_task":
		return m.updateTask(ctx, args)
	case "ratchet_list_projects":
		return m.queryRows(ctx, "SELECT id, name, status, workspace_path FROM projects ORDER BY created_at ASC")
	case "ratchet_send_message":
		return m.sendMessage(ctx, args)
	case "ratchet_start_agent":
		id, _ := args["id"].(string)
		_, err := m.db.ExecContext(ctx, "UPDATE agents SET status = 'active', updated_at = datetime('now') WHERE id = ?", id)
		if err != nil {
			return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
		}
		return map[string]any{"status": "active"}, nil
	case "ratchet_stop_agent":
		id, _ := args["id"].(string)
		_, err := m.db.ExecContext(ctx, "UPDATE agents SET status = 'stopped', updated_at = datetime('now') WHERE id = ?", id)
		if err != nil {
			return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
		}
		return map[string]any{"status": "stopped"}, nil
	default:
		return nil, &jsonRPCError{Code: -32601, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}
}

func (m *MCPServerModule) queryRows(ctx context.Context, query string) (any, *jsonRPCError) {
	rows, err := m.db.QueryContext(ctx, query)
	if err != nil {
		return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
	}
	defer func() { _ = rows.Close() }()

	cols, _ := rows.Columns()
	var results []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]any)
		for i, col := range cols {
			row[col] = vals[i]
		}
		results = append(results, row)
	}
	if results == nil {
		results = []map[string]any{}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": mustJSON(results)}}}, nil
}

func (m *MCPServerModule) createAgent(ctx context.Context, args map[string]any) (any, *jsonRPCError) {
	name, _ := args["name"].(string)
	if name == "" {
		return nil, &jsonRPCError{Code: -32602, Message: "name is required"}
	}
	role, _ := args["role"].(string)
	systemPrompt, _ := args["system_prompt"].(string)
	teamID, _ := args["team_id"].(string)
	id := uuid.New().String()

	_, err := m.db.ExecContext(ctx,
		`INSERT INTO agents (id, name, role, system_prompt, team_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		id, name, role, systemPrompt, teamID,
	)
	if err != nil {
		return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("Agent created: %s", id)}}}, nil
}

func (m *MCPServerModule) createTask(ctx context.Context, args map[string]any) (any, *jsonRPCError) {
	title, _ := args["title"].(string)
	if title == "" {
		return nil, &jsonRPCError{Code: -32602, Message: "title is required"}
	}
	description, _ := args["description"].(string)
	assignedTo, _ := args["assigned_to"].(string)
	projectID, _ := args["project_id"].(string)
	id := uuid.New().String()

	_, err := m.db.ExecContext(ctx,
		`INSERT INTO tasks (id, title, description, assigned_to, project_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		id, title, description, assignedTo, projectID,
	)
	if err != nil {
		return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("Task created: %s", id)}}}, nil
}

func (m *MCPServerModule) updateTask(ctx context.Context, args map[string]any) (any, *jsonRPCError) {
	id, _ := args["id"].(string)
	if id == "" {
		return nil, &jsonRPCError{Code: -32602, Message: "id is required"}
	}
	status, _ := args["status"].(string)
	result, _ := args["result"].(string)

	if status != "" {
		if _, err := m.db.ExecContext(ctx, "UPDATE tasks SET status = ?, updated_at = datetime('now') WHERE id = ?", status, id); err != nil {
			return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
		}
	}
	if result != "" {
		if _, err := m.db.ExecContext(ctx, "UPDATE tasks SET result = ?, updated_at = datetime('now') WHERE id = ?", result, id); err != nil {
			return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
		}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("Task updated: %s", id)}}}, nil
}

func (m *MCPServerModule) sendMessage(ctx context.Context, args map[string]any) (any, *jsonRPCError) {
	to, _ := args["to"].(string)
	content, _ := args["content"].(string)
	from, _ := args["from"].(string)
	subject, _ := args["subject"].(string)
	if to == "" || content == "" {
		return nil, &jsonRPCError{Code: -32602, Message: "to and content are required"}
	}
	id := uuid.New().String()
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO messages (id, type, from_agent, to_agent, subject, content, created_at)
		 VALUES (?, 'direct', ?, ?, ?, ?, datetime('now'))`,
		id, from, to, subject, content,
	)
	if err != nil {
		return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("Message sent: %s", id)}}}, nil
}

func writeJSONRPCErrorResp(w http.ResponseWriter, id int64, code int, msg string) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func newMCPServerFactory() plugin.ModuleFactory {
	return func(name string, cfg map[string]any) modular.Module {
		path, _ := cfg["path"].(string)
		if path == "" {
			path = "/mcp"
		}
		return &MCPServerModule{
			name: name,
			path: path,
		}
	}
}
