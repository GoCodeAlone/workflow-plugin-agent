package orchestrator

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	"github.com/GoCodeAlone/workflow/module"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// ContainerManager availability tests (no Docker required)
// ---------------------------------------------------------------------------

func TestContainerManager_NewWithoutDocker(t *testing.T) {
	// When Docker is not available, NewContainerManager should return
	// a manager that reports unavailable.
	db := openTestDB(t)
	cm := &ContainerManager{
		db:         db,
		containers: make(map[string]string),
		available:  false,
	}

	if cm.IsAvailable() {
		t.Error("expected IsAvailable() to be false when Docker is not connected")
	}
}

func TestContainerManager_EnsureContainer_Unavailable(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  false,
	}

	_, err := cm.EnsureContainer(context.Background(), "proj-1", "/tmp/ws", WorkspaceSpec{Image: "alpine"})
	if err == nil {
		t.Fatal("expected error when Docker is unavailable")
	}
}

func TestContainerManager_ExecInContainer_Unavailable(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  false,
	}

	_, _, _, err := cm.ExecInContainer(context.Background(), "proj-1", "echo hello", "/workspace", 30)
	if err == nil {
		t.Fatal("expected error when Docker is unavailable")
	}
}

func TestContainerManager_ExecInContainer_NoContainer(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  true,
	}

	_, _, _, err := cm.ExecInContainer(context.Background(), "nonexistent", "echo hello", "/workspace", 30)
	if err == nil {
		t.Fatal("expected error for missing container")
	}
}

func TestContainerManager_StopContainer_Unavailable(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  false,
	}

	err := cm.StopContainer(context.Background(), "proj-1")
	if err == nil {
		t.Fatal("expected error when Docker is unavailable")
	}
}

func TestContainerManager_RemoveContainer_Unavailable(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  false,
	}

	err := cm.RemoveContainer(context.Background(), "proj-1")
	if err == nil {
		t.Fatal("expected error when Docker is unavailable")
	}
}

func TestContainerManager_GetContainerStatus_Unavailable(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  false,
	}

	status, err := cm.GetContainerStatus(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "unavailable" {
		t.Errorf("expected status 'unavailable', got %q", status)
	}
}

func TestContainerManager_GetContainerStatus_NoContainer(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  true,
	}

	status, err := cm.GetContainerStatus(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "none" {
		t.Errorf("expected status 'none', got %q", status)
	}
}

func TestContainerManager_Close_Unavailable(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  false,
	}

	if err := cm.Close(); err != nil {
		t.Fatalf("Close on unavailable manager should not error: %v", err)
	}
}

func TestContainerManager_EnsureContainer_EmptyImage(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  true,
	}

	_, err := cm.EnsureContainer(context.Background(), "proj-1", "/tmp/ws", WorkspaceSpec{})
	if err == nil {
		t.Fatal("expected error for empty image")
	}
}

// ---------------------------------------------------------------------------
// WorkspaceSpec tests
// ---------------------------------------------------------------------------

func TestWorkspaceSpec_Defaults(t *testing.T) {
	spec := WorkspaceSpec{Image: "golang:1.22"}
	if spec.Image != "golang:1.22" {
		t.Errorf("expected image 'golang:1.22', got %q", spec.Image)
	}
	if spec.MemoryLimit != 0 {
		t.Errorf("expected default MemoryLimit 0, got %d", spec.MemoryLimit)
	}
	if spec.CPULimit != 0 {
		t.Errorf("expected default CPULimit 0, got %f", spec.CPULimit)
	}
}

// ---------------------------------------------------------------------------
// Context plumbing tests
// ---------------------------------------------------------------------------

func TestContextPlumbing_WorkspacePath(t *testing.T) {
	ctx := context.Background()

	// No workspace path set
	if _, ok := tools.WorkspacePathFromContext(ctx); ok {
		t.Error("expected no workspace path in empty context")
	}

	// Set workspace path
	ctx = tools.WithWorkspacePath(ctx, "/data/workspaces/proj-1")
	ws, ok := tools.WorkspacePathFromContext(ctx)
	if !ok {
		t.Fatal("expected workspace path to be set")
	}
	if ws != "/data/workspaces/proj-1" {
		t.Errorf("expected '/data/workspaces/proj-1', got %q", ws)
	}
}

func TestContextPlumbing_ProjectID(t *testing.T) {
	ctx := context.Background()

	if _, ok := tools.ProjectIDFromContext(ctx); ok {
		t.Error("expected no project ID in empty context")
	}

	ctx = tools.WithProjectID(ctx, "proj-42")
	pid, ok := tools.ProjectIDFromContext(ctx)
	if !ok {
		t.Fatal("expected project ID to be set")
	}
	if pid != "proj-42" {
		t.Errorf("expected 'proj-42', got %q", pid)
	}
}

func TestContextPlumbing_ContainerID(t *testing.T) {
	ctx := context.Background()

	if _, ok := tools.ContainerIDFromContext(ctx); ok {
		t.Error("expected no container ID in empty context")
	}

	ctx = tools.WithContainerID(ctx, "abc123")
	cid, ok := tools.ContainerIDFromContext(ctx)
	if !ok {
		t.Fatal("expected container ID to be set")
	}
	if cid != "abc123" {
		t.Errorf("expected 'abc123', got %q", cid)
	}
}

// ---------------------------------------------------------------------------
// ContainerControlStep tests
// ---------------------------------------------------------------------------

func TestContainerControlStep_MissingProjectID(t *testing.T) {
	step := &ContainerControlStep{
		name:   "cc",
		action: "status",
		tmpl:   module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{},
	}

	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
}

func TestContainerControlStep_Unavailable(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  false,
	}
	app := newMockApp()
	app.services["ratchet-container-manager"] = cm

	step := &ContainerControlStep{
		name:   "cc",
		action: "status",
		app:    app,
		tmpl:   module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"project_id": "proj-1",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["status"] != "unavailable" {
		t.Errorf("expected status 'unavailable', got %v", result.Output["status"])
	}
}

func TestContainerControlStep_NilManager(t *testing.T) {
	app := newMockApp() // no container manager registered

	step := &ContainerControlStep{
		name:   "cc",
		action: "status",
		app:    app,
		tmpl:   module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"project_id": "proj-1",
		},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["status"] != "unavailable" {
		t.Errorf("expected status 'unavailable', got %v", result.Output["status"])
	}
}

func TestContainerControlStep_UnknownAction(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  true,
	}
	app := newMockApp()
	app.services["ratchet-container-manager"] = cm

	step := &ContainerControlStep{
		name:   "cc",
		action: "explode",
		app:    app,
		tmpl:   module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"project_id": "proj-1",
		},
	}

	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestContainerControlStep_StartMissingWorkspace(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  true,
	}
	app := newMockApp()
	app.services["ratchet-container-manager"] = cm

	step := &ContainerControlStep{
		name:   "cc",
		action: "start",
		app:    app,
		tmpl:   module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"project_id": "proj-1",
			"image":      "alpine",
		},
	}

	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Fatal("expected error for missing workspace_path")
	}
}

func TestContainerControlStep_StartMissingImage(t *testing.T) {
	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  true,
	}
	app := newMockApp()
	app.services["ratchet-container-manager"] = cm

	step := &ContainerControlStep{
		name:   "cc",
		action: "start",
		app:    app,
		tmpl:   module.NewTemplateEngine(),
	}

	pc := &module.PipelineContext{
		Current: map[string]any{
			"project_id":     "proj-1",
			"workspace_path": "/tmp/ws",
		},
	}

	_, err := step.Execute(context.Background(), pc)
	if err == nil {
		t.Fatal("expected error for missing image")
	}
}

// ---------------------------------------------------------------------------
// ContainerControlStep factory tests
// ---------------------------------------------------------------------------

func TestContainerControlFactory_DefaultAction(t *testing.T) {
	factory := newContainerControlFactory()
	app := newMockApp()

	stepRaw, err := factory("cc", map[string]any{}, app)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	step, ok := stepRaw.(*ContainerControlStep)
	if !ok {
		t.Fatalf("expected *ContainerControlStep, got %T", stepRaw)
		return
	}
	if step.action != "status" {
		t.Errorf("expected default action 'status', got %q", step.action)
	}
}

func TestContainerControlFactory_CustomAction(t *testing.T) {
	factory := newContainerControlFactory()
	app := newMockApp()

	stepRaw, err := factory("cc", map[string]any{"action": "stop"}, app)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	step := stepRaw.(*ContainerControlStep)
	if step.action != "stop" {
		t.Errorf("expected action 'stop', got %q", step.action)
	}
}

func TestContainerControlFactory_WithContainerManager(t *testing.T) {
	factory := newContainerControlFactory()
	app := newMockApp()

	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  false,
	}
	app.services["ratchet-container-manager"] = cm

	stepRaw, err := factory("cc", map[string]any{}, app)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	step := stepRaw.(*ContainerControlStep)
	if step.app == nil {
		t.Error("expected app to be set from factory")
	}
	// ContainerManager is looked up lazily in Execute(), not stored on struct
}

// ---------------------------------------------------------------------------
// Verify ContainerManager implements ContainerExecer
// ---------------------------------------------------------------------------

var _ tools.ContainerExecer = (*ContainerManager)(nil)

// ---------------------------------------------------------------------------
// AgentExecuteStep container manager lookup test
// ---------------------------------------------------------------------------

func TestAgentExecuteStepFactory_ContainerManagerLookup(t *testing.T) {
	mp := &mockProvider{responses: []string{"Done."}}
	providerMod := &AIProviderModule{name: "ratchet-ai", provider: mp}

	cm := &ContainerManager{
		containers: make(map[string]string),
		available:  false,
	}

	app := newMockApp()
	app.services["ratchet-ai"] = providerMod
	app.services["ratchet-container-manager"] = cm

	factory := newAgentExecuteStepFactory()
	stepRaw, err := factory("agent-exec", map[string]any{}, app)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	step, ok := stepRaw.(*AgentExecuteStep)
	if !ok {
		t.Fatalf("expected *AgentExecuteStep, got %T", stepRaw)
		return
	}
	if step.app == nil {
		t.Error("expected app to be set from factory")
	}
	// ContainerManager is looked up lazily in Execute(), not stored on struct
}
