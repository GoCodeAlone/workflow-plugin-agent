package orchestrator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/plugin"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/secrets"

	"github.com/GoCodeAlone/modular"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// mockSecretsProvider implements secrets.Provider for testing.
type mockSecretsProvider struct {
	secrets map[string]string
}

func (m *mockSecretsProvider) Name() string { return "mock" }

func (m *mockSecretsProvider) Get(_ context.Context, name string) (string, error) {
	v, ok := m.secrets[name]
	if !ok {
		return "", fmt.Errorf("secret %q not found", name)
	}
	return v, nil
}

func (m *mockSecretsProvider) Set(_ context.Context, _, _ string) error {
	return secrets.ErrUnsupported
}

func (m *mockSecretsProvider) Delete(_ context.Context, _ string) error {
	return secrets.ErrUnsupported
}

func (m *mockSecretsProvider) List(_ context.Context) ([]string, error) {
	names := make([]string, 0, len(m.secrets))
	for k := range m.secrets {
		names = append(names, k)
	}
	return names, nil
}

// mockTool implements plugin.Tool for testing.
type mockTool struct {
	name   string
	result any
}

func (mt *mockTool) Name() string        { return mt.name }
func (mt *mockTool) Description() string { return "mock " + mt.name }
func (mt *mockTool) Definition() provider.ToolDef {
	return provider.ToolDef{Name: mt.name, Description: mt.Description()}
}
func (mt *mockTool) Execute(_ context.Context, _ map[string]any) (any, error) {
	return mt.result, nil
}

// mockApp implements the subset of modular.Application used by the factories.
type mockApp struct {
	modular.Application
	services map[string]any
}

func newMockApp() *mockApp {
	return &mockApp{services: make(map[string]any)}
}

func (m *mockApp) SvcRegistry() modular.ServiceRegistry {
	return modular.ServiceRegistry(m.services)
}

func (m *mockApp) RegisterService(name string, svc any) error {
	m.services[name] = svc
	return nil
}

// Logger returns a no-op logger so tests don't panic on nil embedded interface.
func (m *mockApp) Logger() modular.Logger { return &noopLogger{} }

type noopLogger struct{}

func (n *noopLogger) Info(msg string, args ...any)  {}
func (n *noopLogger) Error(msg string, args ...any) {}
func (n *noopLogger) Warn(msg string, args ...any)  {}
func (n *noopLogger) Debug(msg string, args ...any) {}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func initTranscriptsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(createTranscriptsTable)
	if err != nil {
		t.Fatalf("create transcripts table: %v", err)
	}
}

func createAllTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{createAgentsTable, createTasksTable, createMessagesTable, createProjectsTable, createTranscriptsTable, createMCPServersTable, createLLMProvidersTable} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// SecretGuard tests
// ---------------------------------------------------------------------------

func TestNewSecretGuard(t *testing.T) {
	p := &mockSecretsProvider{secrets: map[string]string{}}
	sg := NewSecretGuard(p, "file")
	if sg == nil {
		t.Fatal("NewSecretGuard returned nil")
		return
	}
	if sg.provider == nil {
		t.Fatal("provider is nil")
	}
	if sg.knownValues == nil {
		t.Fatal("knownValues map is nil")
	}
	if sg.BackendName() != "file" {
		t.Errorf("BackendName: got %q, want %q", sg.BackendName(), "file")
	}
}

func TestSecretGuard_LoadSecrets(t *testing.T) {
	p := &mockSecretsProvider{secrets: map[string]string{
		"API_KEY":  "sk-abc123",
		"DB_PASS":  "super-secret",
		"NO_VALUE": "",
	}}
	sg := NewSecretGuard(p, "test")
	ctx := context.Background()

	err := sg.LoadSecrets(ctx, []string{"API_KEY", "DB_PASS", "NO_VALUE", "MISSING"})
	if err != nil {
		t.Fatalf("LoadSecrets: %v", err)
	}

	// Redact should replace known values
	text := "key is sk-abc123 and password is super-secret"
	redacted := sg.Redact(text)
	if !strings.Contains(redacted, "[REDACTED:API_KEY]") {
		t.Errorf("expected [REDACTED:API_KEY], got %q", redacted)
	}
	if !strings.Contains(redacted, "[REDACTED:DB_PASS]") {
		t.Errorf("expected [REDACTED:DB_PASS], got %q", redacted)
	}
	if strings.Contains(redacted, "sk-abc123") {
		t.Errorf("secret value still present in redacted text")
	}
}

func TestSecretGuard_LoadAllSecrets(t *testing.T) {
	p := &mockSecretsProvider{secrets: map[string]string{
		"TOKEN": "tok-xyz",
	}}
	sg := NewSecretGuard(p, "test")

	err := sg.LoadAllSecrets(context.Background())
	if err != nil {
		t.Fatalf("LoadAllSecrets: %v", err)
	}

	redacted := sg.Redact("my token is tok-xyz")
	if !strings.Contains(redacted, "[REDACTED:TOKEN]") {
		t.Errorf("expected [REDACTED:TOKEN], got %q", redacted)
	}
}

func TestSecretGuard_LoadAllSecrets_NilProvider(t *testing.T) {
	sg := &SecretGuard{knownValues: make(map[string]string)}
	err := sg.LoadAllSecrets(context.Background())
	if err != nil {
		t.Fatalf("LoadAllSecrets with nil provider should not error, got: %v", err)
	}
}

func TestSecretGuard_CheckAndRedact(t *testing.T) {
	p := &mockSecretsProvider{secrets: map[string]string{"KEY": "secret123"}}
	sg := NewSecretGuard(p, "test")
	_ = sg.LoadSecrets(context.Background(), []string{"KEY"})

	msg := &provider.Message{Content: "the secret is secret123"}
	changed := sg.CheckAndRedact(msg)
	if !changed {
		t.Error("expected CheckAndRedact to return true")
	}
	if !strings.Contains(msg.Content, "[REDACTED:KEY]") {
		t.Errorf("expected redacted content, got %q", msg.Content)
	}
}

func TestSecretGuard_CheckAndRedact_NoChange(t *testing.T) {
	p := &mockSecretsProvider{secrets: map[string]string{"KEY": "secret123"}}
	sg := NewSecretGuard(p, "test")
	_ = sg.LoadSecrets(context.Background(), []string{"KEY"})

	msg := &provider.Message{Content: "nothing to redact here"}
	changed := sg.CheckAndRedact(msg)
	if changed {
		t.Error("expected CheckAndRedact to return false when no redaction occurs")
	}
}

func TestSecretGuard_Redact_NoSecretsLoaded(t *testing.T) {
	sg := NewSecretGuard(&mockSecretsProvider{secrets: map[string]string{}}, "test")
	text := "nothing secret here"
	if got := sg.Redact(text); got != text {
		t.Errorf("expected unchanged text, got %q", got)
	}
}

func TestSecretGuard_ConcurrentAccess(t *testing.T) {
	p := &mockSecretsProvider{secrets: map[string]string{
		"A": "val-a",
		"B": "val-b",
		"C": "val-c",
	}}
	sg := NewSecretGuard(p, "test")
	ctx := context.Background()

	var wg sync.WaitGroup
	// Load secrets concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sg.LoadSecrets(ctx, []string{"A", "B", "C"})
		}()
	}
	// Redact concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sg.Redact("contains val-a and val-b")
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// ToolRegistry tests
// ---------------------------------------------------------------------------

func TestToolRegistry_RegisterAndGet(t *testing.T) {
	tr := NewToolRegistry()
	tool := &mockTool{name: "read_file", result: "contents"}
	tr.Register(tool)

	got, ok := tr.Get("read_file")
	if !ok {
		t.Fatal("expected tool to be found")
		return
	}
	if got.Name() != "read_file" {
		t.Errorf("expected name read_file, got %s", got.Name())
	}
}

func TestToolRegistry_Get_NotFound(t *testing.T) {
	tr := NewToolRegistry()
	_, ok := tr.Get("nonexistent")
	if ok {
		t.Error("expected tool not to be found")
	}
}

func TestToolRegistry_RegisterMCP(t *testing.T) {
	tr := NewToolRegistry()
	tools := []plugin.Tool{
		&mockTool{name: "list_files", result: nil},
		&mockTool{name: "read_file", result: nil},
	}
	tr.RegisterMCP("github", tools)

	_, ok := tr.Get("mcp_github__list_files")
	if !ok {
		t.Error("expected mcp_github__list_files to be found")
	}
	_, ok = tr.Get("mcp_github__read_file")
	if !ok {
		t.Error("expected mcp_github__read_file to be found")
	}
	// Original name should not exist
	_, ok = tr.Get("list_files")
	if ok {
		t.Error("original name should not be registered")
	}
}

func TestToolRegistry_AllDefs(t *testing.T) {
	tr := NewToolRegistry()
	tr.Register(&mockTool{name: "a", result: nil})
	tr.Register(&mockTool{name: "b", result: nil})

	defs := tr.AllDefs()
	if len(defs) != 2 {
		t.Fatalf("expected 2 defs, got %d", len(defs))
	}
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["a"] || !names["b"] {
		t.Errorf("expected defs for a and b, got %v", names)
	}
}

func TestToolRegistry_Execute(t *testing.T) {
	tr := NewToolRegistry()
	tr.Register(&mockTool{name: "echo", result: "hello"})
	// Set up a permissive policy engine so the fail-closed default doesn't block.
	pe := &ToolPolicyEngine{DefaultPolicy: PolicyAllow}
	tr.SetPolicyEngine(pe)

	result, err := tr.Execute(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "hello" {
		t.Errorf("expected hello, got %v", result)
	}
}

func TestToolRegistry_Execute_NotFound(t *testing.T) {
	tr := NewToolRegistry()
	_, err := tr.Execute(context.Background(), "missing", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
		return
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got %q", err.Error())
	}
}

func TestToolRegistry_Names(t *testing.T) {
	tr := NewToolRegistry()
	tr.Register(&mockTool{name: "x", result: nil})
	tr.Register(&mockTool{name: "y", result: nil})

	names := tr.Names()
	sort.Strings(names)
	if len(names) != 2 || names[0] != "x" || names[1] != "y" {
		t.Errorf("expected [x, y], got %v", names)
	}
}

func TestToolRegistry_ConcurrentAccess(t *testing.T) {
	tr := NewToolRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := fmt.Sprintf("tool_%d", n)
			tr.Register(&mockTool{name: name, result: n})
			tr.Get(name)
			tr.Names()
			tr.AllDefs()
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// TranscriptRecorder tests
// ---------------------------------------------------------------------------

func TestTranscriptRecorder_RecordAndGetByTask(t *testing.T) {
	db := openTestDB(t)
	initTranscriptsTable(t, db)
	rec := NewTranscriptRecorder(db, nil)

	ctx := context.Background()
	entry := TranscriptEntry{
		ID:      "entry-1",
		AgentID: "agent-a",
		TaskID:  "task-1",
		Role:    provider.RoleUser,
		Content: "hello from test",
	}
	if err := rec.Record(ctx, entry); err != nil {
		t.Fatalf("Record: %v", err)
	}

	entries, err := rec.GetByTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetByTask: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Content != "hello from test" {
		t.Errorf("unexpected content: %q", entries[0].Content)
	}
	if entries[0].AgentID != "agent-a" {
		t.Errorf("unexpected agent_id: %q", entries[0].AgentID)
	}
}

func TestTranscriptRecorder_RecordAndGetByAgent(t *testing.T) {
	db := openTestDB(t)
	initTranscriptsTable(t, db)
	rec := NewTranscriptRecorder(db, nil)
	ctx := context.Background()

	_ = rec.Record(ctx, TranscriptEntry{ID: "e1", AgentID: "agent-x", TaskID: "t1", Role: provider.RoleUser, Content: "msg1"})
	_ = rec.Record(ctx, TranscriptEntry{ID: "e2", AgentID: "agent-x", TaskID: "t2", Role: provider.RoleAssistant, Content: "msg2"})
	_ = rec.Record(ctx, TranscriptEntry{ID: "e3", AgentID: "agent-y", TaskID: "t1", Role: provider.RoleUser, Content: "msg3"})

	entries, err := rec.GetByAgent(ctx, "agent-x")
	if err != nil {
		t.Fatalf("GetByAgent: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for agent-x, got %d", len(entries))
	}
}

func TestTranscriptRecorder_SecretRedaction(t *testing.T) {
	db := openTestDB(t)
	initTranscriptsTable(t, db)

	p := &mockSecretsProvider{secrets: map[string]string{"API_KEY": "sk-secret-val"}}
	sg := NewSecretGuard(p, "test")
	_ = sg.LoadAllSecrets(context.Background())

	rec := NewTranscriptRecorder(db, sg)
	ctx := context.Background()

	err := rec.Record(ctx, TranscriptEntry{
		ID:      "r1",
		AgentID: "a1",
		TaskID:  "t1",
		Role:    provider.RoleUser,
		Content: "my key is sk-secret-val",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	entries, err := rec.GetByTask(ctx, "t1")
	if err != nil {
		t.Fatalf("GetByTask: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if strings.Contains(entries[0].Content, "sk-secret-val") {
		t.Error("secret value should be redacted in DB")
	}
	if !strings.Contains(entries[0].Content, "[REDACTED:API_KEY]") {
		t.Errorf("expected [REDACTED:API_KEY], got %q", entries[0].Content)
	}
	if !entries[0].Redacted {
		t.Error("expected redacted flag to be true")
	}
}

// ---------------------------------------------------------------------------
// mockProvider tests
// ---------------------------------------------------------------------------

func TestMockProvider_Name(t *testing.T) {
	mp := &mockProvider{responses: []string{"a"}}
	if mp.Name() != "mock" {
		t.Errorf("expected 'mock', got %q", mp.Name())
	}
}

func TestMockProvider_Chat_CyclesResponses(t *testing.T) {
	mp := &mockProvider{responses: []string{"first", "second", "third"}}
	ctx := context.Background()

	for i, want := range []string{"first", "second", "third", "first"} {
		resp, err := mp.Chat(ctx, nil, nil)
		if err != nil {
			t.Fatalf("Chat #%d: %v", i, err)
		}
		if resp.Content != want {
			t.Errorf("Chat #%d: expected %q, got %q", i, want, resp.Content)
		}
	}
}

func TestMockProvider_Stream(t *testing.T) {
	mp := &mockProvider{responses: []string{"streamed"}}
	ctx := context.Background()

	ch, err := mp.Stream(ctx, nil, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var events []provider.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != "text" || events[0].Text != "streamed" {
		t.Errorf("first event: %+v", events[0])
	}
	if events[1].Type != "done" {
		t.Errorf("second event type: %q", events[1].Type)
	}
}

// ---------------------------------------------------------------------------
// SSEHub tests
// ---------------------------------------------------------------------------

func TestSSEHub_Factory_DefaultPath(t *testing.T) {
	factory := newSSEHubFactory()
	mod := factory("my-hub", map[string]any{})
	hub, ok := mod.(*SSEHub)
	if !ok {
		t.Fatal("expected *SSEHub")
		return
	}
	if hub.Name() != "my-hub" {
		t.Errorf("expected name my-hub, got %s", hub.Name())
	}
	if hub.Path() != "/events" {
		t.Errorf("expected default path /events, got %s", hub.Path())
	}
}

func TestSSEHub_Factory_CustomPath(t *testing.T) {
	factory := newSSEHubFactory()
	mod := factory("hub2", map[string]any{"path": "/sse"})
	hub := mod.(*SSEHub)
	if hub.Path() != "/sse" {
		t.Errorf("expected /sse, got %s", hub.Path())
	}
}

func TestSSEHub_BroadcastEvent_Format(t *testing.T) {
	hub := &SSEHub{
		name:    "test",
		path:    "/events",
		clients: make(map[chan []byte]struct{}),
	}

	ch := make(chan []byte, 16)
	hub.mu.Lock()
	hub.clients[ch] = struct{}{}
	hub.mu.Unlock()

	hub.BroadcastEvent("agent_update", `{"id":"a1"}`)

	msg := <-ch
	expected := `event: agent_update` + "\n" + `data: {"id":"a1"}`
	if string(msg) != expected {
		t.Errorf("expected %q, got %q", expected, string(msg))
	}
}

func TestSSEHub_Stop_ClosesClients(t *testing.T) {
	hub := &SSEHub{
		name:    "test",
		path:    "/events",
		clients: make(map[chan []byte]struct{}),
	}

	ch1 := make(chan []byte, 4)
	ch2 := make(chan []byte, 4)
	hub.clients[ch1] = struct{}{}
	hub.clients[ch2] = struct{}{}

	if err := hub.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Channels should be closed
	if _, open := <-ch1; open {
		t.Error("ch1 should be closed")
	}
	if _, open := <-ch2; open {
		t.Error("ch2 should be closed")
	}

	if len(hub.clients) != 0 {
		t.Errorf("expected 0 clients, got %d", len(hub.clients))
	}
}

// ---------------------------------------------------------------------------
// MCPServerModule tests
// ---------------------------------------------------------------------------

func setupMCPServer(t *testing.T) (*MCPServerModule, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	createAllTables(t, db)
	return &MCPServerModule{name: "mcp-test", path: "/mcp", db: db}, db
}

func mcpCall(t *testing.T, handler http.Handler, method string, params any) map[string]any {
	t.Helper()
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, rr.Body.String())
	}
	return resp
}

func TestMCPServer_Initialize(t *testing.T) {
	srv, _ := setupMCPServer(t)
	resp := mcpCall(t, srv, "initialize", nil)

	result, ok := resp["result"]
	if !ok {
		t.Fatalf("expected result in response, got %v", resp)
	}
	// result is json.RawMessage-decoded as map after re-encoding
	var resultMap map[string]any
	resultBytes, _ := json.Marshal(result)
	_ = json.Unmarshal(resultBytes, &resultMap)

	if pv, _ := resultMap["protocolVersion"].(string); pv != "2024-11-05" {
		t.Errorf("expected protocolVersion 2024-11-05, got %v", resultMap["protocolVersion"])
	}
}

func TestMCPServer_ToolsList(t *testing.T) {
	srv, _ := setupMCPServer(t)
	resp := mcpCall(t, srv, "tools/list", nil)

	var resultMap map[string]any
	resultBytes, _ := json.Marshal(resp["result"])
	_ = json.Unmarshal(resultBytes, &resultMap)

	toolsRaw, ok := resultMap["tools"]
	if !ok {
		t.Fatal("expected tools in result")
	}
	toolsList, ok := toolsRaw.([]any)
	if !ok {
		t.Fatalf("expected tools to be a list, got %T", toolsRaw)
	}
	if len(toolsList) != 17 {
		t.Errorf("expected 17 tools, got %d", len(toolsList))
	}
}

func TestMCPServer_CreateAndListAgents(t *testing.T) {
	srv, _ := setupMCPServer(t)

	// Create an agent
	createResp := mcpCall(t, srv, "tools/call", map[string]any{
		"name":      "ratchet_create_agent",
		"arguments": map[string]any{"name": "test-agent", "role": "coder"},
	})
	if _, hasErr := createResp["error"]; hasErr {
		t.Fatalf("create agent error: %v", createResp["error"])
	}

	// List agents
	listResp := mcpCall(t, srv, "tools/call", map[string]any{
		"name":      "ratchet_list_agents",
		"arguments": map[string]any{},
	})
	if _, hasErr := listResp["error"]; hasErr {
		t.Fatalf("list agents error: %v", listResp["error"])
	}
}

func TestMCPServer_UnknownMethod(t *testing.T) {
	srv, _ := setupMCPServer(t)
	resp := mcpCall(t, srv, "nonexistent/method", nil)

	errObj, ok := resp["error"]
	if !ok {
		t.Fatal("expected error in response")
	}
	errMap, _ := errObj.(map[string]any)
	code, _ := errMap["code"].(float64)
	if int(code) != -32601 {
		t.Errorf("expected error code -32601, got %v", code)
	}
}

func TestMCPServer_GET_Returns405(t *testing.T) {
	srv, _ := setupMCPServer(t)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestMCPServer_InvalidJSON(t *testing.T) {
	srv, _ := setupMCPServer(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	errObj, _ := resp["error"].(map[string]any)
	code, _ := errObj["code"].(float64)
	if int(code) != -32700 {
		t.Errorf("expected parse error code -32700, got %v", code)
	}
}

// ---------------------------------------------------------------------------
// WorkspaceInitStep tests
// ---------------------------------------------------------------------------

func TestWorkspaceInitStep_Execute(t *testing.T) {
	tmpDir := t.TempDir()

	app := newMockApp()
	step := &WorkspaceInitStep{
		name:    "ws-init",
		dataDir: tmpDir,
		app:     app,
		tmpl:    module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"project_id": "proj-1",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	wsPath, _ := result.Output["workspace_path"].(string)
	if wsPath == "" {
		t.Fatal("expected workspace_path in output")
	}

	// Check directories were created
	for _, sub := range []string{"src", "output", "logs"} {
		dir := filepath.Join(wsPath, sub)
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
		} else if !info.IsDir() {
			t.Errorf("expected %s to be a directory", sub)
		}
	}
}

func TestWorkspaceInitStep_Execute_EmptyProjectID(t *testing.T) {
	app := newMockApp()
	step := &WorkspaceInitStep{
		name:    "ws-init",
		dataDir: t.TempDir(),
		app:     app,
		tmpl:    module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{},
	}

	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Fatal("expected error for empty project_id")
		return
	}
	if !strings.Contains(err.Error(), "project_id is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWorkspaceInitFactory_DefaultDataDir(t *testing.T) {
	factory := newWorkspaceInitFactory()
	app := newMockApp()

	stepRaw, err := factory("ws-init", map[string]any{}, app)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	step, ok := stepRaw.(*WorkspaceInitStep)
	if !ok {
		t.Fatalf("expected *WorkspaceInitStep, got %T", stepRaw)
		return
	}
	if step.dataDir != "./data" {
		t.Errorf("expected default data_dir ./data, got %q", step.dataDir)
	}
}

// ---------------------------------------------------------------------------
// AgentExecuteStep tests
// ---------------------------------------------------------------------------

func TestAgentExecuteStep_SimpleCompletion(t *testing.T) {
	db := openTestDB(t)
	initTranscriptsTable(t, db)

	// Set up mock provider module
	mp := &mockProvider{responses: []string{"Task completed successfully."}}
	providerMod := &AIProviderModule{
		name:     "ratchet-ai",
		provider: mp,
	}

	// Set up guard
	sg := NewSecretGuard(&mockSecretsProvider{secrets: map[string]string{}}, "test")

	// Set up recorder
	rec := NewTranscriptRecorder(db, sg)

	// Set up tool registry
	tr := NewToolRegistry()

	// Build mock app
	app := newMockApp()
	app.services["ratchet-ai"] = providerMod
	app.services["ratchet-tool-registry"] = tr
	app.services["ratchet-secret-guard"] = sg
	app.services["ratchet-transcript-recorder"] = rec

	step := &AgentExecuteStep{
		name:            "agent-exec",
		maxIterations:   5,
		providerService: "ratchet-ai",
		app:             app,
		tmpl:            module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"system_prompt": "You are a test agent.",
			"task":          "Do something simple.",
			"agent_name":    "test-agent",
			"agent_id":      "a1",
			"task_id":       "t1",
			"project_id":    "p1",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	output := result.Output
	if output["status"] != "completed" {
		t.Errorf("expected status completed, got %v", output["status"])
	}
	if output["result"] != "Task completed successfully." {
		t.Errorf("unexpected result: %v", output["result"])
	}
	if output["iterations"] != 1 {
		t.Errorf("expected 1 iteration, got %v", output["iterations"])
	}

	// Verify transcripts were recorded
	entries, err := rec.GetByTask(context.Background(), "t1")
	if err != nil {
		t.Fatalf("GetByTask: %v", err)
	}
	// Should have: system, user, assistant = 3 entries
	if len(entries) != 3 {
		t.Errorf("expected 3 transcript entries, got %d", len(entries))
	}
}

func TestAgentExecuteStep_SecretRedaction(t *testing.T) {
	db := openTestDB(t)
	initTranscriptsTable(t, db)

	mp := &mockProvider{responses: []string{"Done."}}
	providerMod := &AIProviderModule{name: "ratchet-ai", provider: mp}

	sg := NewSecretGuard(&mockSecretsProvider{secrets: map[string]string{"PASS": "my-secret-pass"}}, "test")
	_ = sg.LoadAllSecrets(context.Background())

	rec := NewTranscriptRecorder(db, sg)
	app := newMockApp()
	app.services["ratchet-ai"] = providerMod
	app.services["ratchet-secret-guard"] = sg
	app.services["ratchet-transcript-recorder"] = rec

	step := &AgentExecuteStep{
		name:            "agent-exec",
		maxIterations:   5,
		providerService: "ratchet-ai",
		app:             app,
		tmpl:            module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"system_prompt": "You are an agent with password my-secret-pass",
			"task":          "Do it",
			"agent_name":    "test",
			"agent_id":      "a1",
			"task_id":       "t1",
		},
	}

	_, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The secret should have been redacted before passing to provider
	// (since the step calls CheckAndRedact on messages)
	entries, _ := rec.GetByTask(context.Background(), "t1")
	for _, e := range entries {
		if strings.Contains(e.Content, "my-secret-pass") {
			t.Errorf("secret value found in transcript entry (role=%s): %q", e.Role, e.Content)
		}
	}
}

// ---------------------------------------------------------------------------
// Plugin registration tests
// ---------------------------------------------------------------------------

func TestPlugin_New(t *testing.T) {
	p := New()
	if p == nil {
		t.Fatal("New() returned nil")
		return
	}
	if p.PluginName != "ratchet" {
		t.Errorf("expected plugin name 'ratchet', got %q", p.PluginName)
	}
	if p.PluginVersion != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", p.PluginVersion)
	}
}

func TestPlugin_ModuleFactories(t *testing.T) {
	p := New()
	factories := p.ModuleFactories()

	// agent.provider is included here as ratchetplugin absorbs workflow-plugin-agent
	// to avoid duplicate step type registration.
	expected := []string{
		"agent.provider",
		"ratchet.sse_hub",
		"ratchet.scheduler",
		"ratchet.mcp_client",
		"ratchet.mcp_server",
		"ratchet.tool_policy_engine",
		"authz.casbin",
		"agent.guardrails",
	}
	for _, name := range expected {
		if _, ok := factories[name]; !ok {
			t.Errorf("missing module factory: %s", name)
		}
	}
	if len(factories) != len(expected) {
		t.Errorf("expected %d module factories, got %d", len(expected), len(factories))
	}
}

func TestPlugin_StepFactories(t *testing.T) {
	p := New()
	factories := p.StepFactories()

	// step.provider_test and step.provider_models are delegated to the agent plugin's
	// factories since ratchetplugin absorbs the agent plugin to avoid duplicate step
	// type registration. step.agent_execute remains here as ratchet's richer override.
	expected := []string{
		"step.agent_execute", "step.provider_test", "step.provider_models", "step.model_pull",
		"step.workspace_init", "step.container_control",
		"step.secret_manage", "step.vault_config",
		"step.mcp_reload", "step.oauth_exchange",
		"step.approval_resolve", "step.webhook_process", "step.security_audit",
		"step.test_interact", "step.human_request_resolve",
		"step.memory_extract",
		"step.bcrypt_check", "step.bcrypt_hash",
		"step.jwt_generate", "step.jwt_decode",
		"step.authz_check_casbin", "step.authz_add_policy",
		"step.authz_remove_policy", "step.authz_role_assign",
		"step.blackboard_post", "step.blackboard_read",
		"step.self_improve_validate", "step.self_improve_diff",
		"step.self_improve_deploy", "step.lsp_diagnose",
	}
	for _, name := range expected {
		if _, ok := factories[name]; !ok {
			t.Errorf("missing step factory: %s", name)
		}
	}
	if len(factories) != len(expected) {
		t.Errorf("expected %d step factories, got %d", len(expected), len(factories))
	}
}

func TestPlugin_WiringHooks(t *testing.T) {
	p := New()
	hooks := p.WiringHooks()

	if len(hooks) != 20 {
		t.Fatalf("expected 20 wiring hooks, got %d", len(hooks))
	}

	expectedNames := map[string]bool{
		"agent.provider_registry":               false,
		"ratchet.sse_route_registration":        false,
		"ratchet.mcp_server_route_registration": false,
		"ratchet.db_init":                       false,
		"ratchet.auth_token":                    false,
		"ratchet.secrets_guard":                 false,
		"ratchet.provider_registry":             false,
		"ratchet.tool_registry":                 false,
		"ratchet.container_manager":             false,
		"ratchet.transcript_recorder":           false,
		"ratchet.tool_policy_engine":            false,
		"ratchet.sub_agent_manager":             false,
		"ratchet.skill_manager":                 false,
		"ratchet.approval_manager":              false,
		"ratchet.human_request_manager":         false,
		"ratchet.webhook_manager":               false,
		"ratchet.security_auditor":              false,
		"ratchet.browser_manager":               false,
		"ratchet.test_interaction":              false,
		"ratchet.blackboard":                    false,
	}
	for _, h := range hooks {
		if _, ok := expectedNames[h.Name]; !ok {
			t.Errorf("unexpected hook name: %s", h.Name)
		}
		expectedNames[h.Name] = true
	}
	for name, found := range expectedNames {
		if !found {
			t.Errorf("missing hook: %s", name)
		}
	}

	// Verify priorities: db_init(100) > auth_token(90) > secrets_guard(85) > provider_registry(83) > tool_registry(80) > transcript_recorder(75)
	expectedPriorities := map[string]int{
		"ratchet.db_init":             100,
		"ratchet.auth_token":          90,
		"ratchet.secrets_guard":       85,
		"ratchet.provider_registry":   83,
		"ratchet.tool_registry":       80,
		"ratchet.transcript_recorder": 75,
	}
	for _, h := range hooks {
		if want, ok := expectedPriorities[h.Name]; ok {
			if h.Priority != want {
				t.Errorf("hook %s: expected priority %d, got %d", h.Name, want, h.Priority)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// extractAgentSeed tests
// ---------------------------------------------------------------------------

func TestExtractAgentSeed(t *testing.T) {
	m := map[string]any{
		"id":            "agent-001",
		"name":          "Alice",
		"role":          "leader",
		"system_prompt": "You lead the team.",
		"provider":      "anthropic",
		"model":         "claude-3",
		"team_id":       "team-alpha",
		"is_lead":       true,
	}

	seed := extractAgentSeed(m)
	if seed.ID != "agent-001" {
		t.Errorf("ID: %q", seed.ID)
	}
	if seed.Name != "Alice" {
		t.Errorf("Name: %q", seed.Name)
	}
	if seed.Role != "leader" {
		t.Errorf("Role: %q", seed.Role)
	}
	if seed.SystemPrompt != "You lead the team." {
		t.Errorf("SystemPrompt: %q", seed.SystemPrompt)
	}
	if seed.Provider != "anthropic" {
		t.Errorf("Provider: %q", seed.Provider)
	}
	if seed.Model != "claude-3" {
		t.Errorf("Model: %q", seed.Model)
	}
	if seed.TeamID != "team-alpha" {
		t.Errorf("TeamID: %q", seed.TeamID)
	}
	if !seed.IsLead {
		t.Error("IsLead should be true")
	}
}

// ---------------------------------------------------------------------------
// extractString tests
// ---------------------------------------------------------------------------

func TestExtractString(t *testing.T) {
	m := map[string]any{
		"name":  "hello",
		"empty": "",
		"num":   42,
	}

	t.Run("existing key", func(t *testing.T) {
		if got := extractString(m, "name", "default"); got != "hello" {
			t.Errorf("expected hello, got %q", got)
		}
	})

	t.Run("missing key returns default", func(t *testing.T) {
		if got := extractString(m, "missing", "fallback"); got != "fallback" {
			t.Errorf("expected fallback, got %q", got)
		}
	})

	t.Run("empty string returns default", func(t *testing.T) {
		if got := extractString(m, "empty", "fallback"); got != "fallback" {
			t.Errorf("expected fallback for empty string, got %q", got)
		}
	})

	t.Run("non-string value returns default", func(t *testing.T) {
		if got := extractString(m, "num", "fallback"); got != "fallback" {
			t.Errorf("expected fallback for non-string, got %q", got)
		}
	})
}
