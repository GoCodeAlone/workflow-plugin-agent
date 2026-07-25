package orchestrator

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"iter"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
	"github.com/GoCodeAlone/workflow-plugin-agent/orchestrator/tools"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"

	_ "modernc.org/sqlite"
)

type blockingPullDockerClient struct {
	pullEntered chan struct{}
	releasePull chan struct{}
}

func newBlockingPullDockerClient() *blockingPullDockerClient {
	return &blockingPullDockerClient{pullEntered: make(chan struct{}), releasePull: make(chan struct{})}
}

func (c *blockingPullDockerClient) ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	return client.ContainerCreateResult{ID: "container-1"}, nil
}

func (c *blockingPullDockerClient) ExecAttach(context.Context, string, client.ExecAttachOptions) (client.ExecAttachResult, error) {
	return client.ExecAttachResult{}, nil
}

func (c *blockingPullDockerClient) ExecCreate(context.Context, string, client.ExecCreateOptions) (client.ExecCreateResult, error) {
	return client.ExecCreateResult{ID: "exec-1"}, nil
}

func (c *blockingPullDockerClient) ExecInspect(context.Context, string, client.ExecInspectOptions) (client.ExecInspectResult, error) {
	return client.ExecInspectResult{}, nil
}

func (c *blockingPullDockerClient) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	return client.ContainerInspectResult{
		Container: container.InspectResponse{
			State: &container.State{Status: container.StateRunning, Running: true},
		},
	}, nil
}

func (c *blockingPullDockerClient) ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	return client.ContainerRemoveResult{}, nil
}

func (c *blockingPullDockerClient) ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, nil
}

func (c *blockingPullDockerClient) ContainerStop(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error) {
	return client.ContainerStopResult{}, nil
}

func (c *blockingPullDockerClient) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{}, errors.New("missing image")
}

func (c *blockingPullDockerClient) ImagePull(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error) {
	close(c.pullEntered)
	<-c.releasePull
	return newFakeImagePullResponse(""), nil
}

func (c *blockingPullDockerClient) Close() error {
	return nil
}

type recordingDockerClient struct {
	imageInspectRef      string
	imageInspectResponse client.ImageInspectResult
	imageInspectErr      error
	imagePullRef         string
	imagePullOptions     client.ImagePullOptions
	imagePullBody        string
	imagePullErr         error

	containerCreateOptions   client.ContainerCreateOptions
	containerCreateResponse  client.ContainerCreateResult
	containerCreateErr       error
	containerStartID         string
	containerStartOptions    client.ContainerStartOptions
	containerStartErr        error
	containerInspectID       string
	containerInspectOptions  client.ContainerInspectOptions
	containerInspectResponse client.ContainerInspectResult
	containerInspectErr      error
	containerStopID          string
	containerStopOptions     client.ContainerStopOptions
	containerStopErr         error
	containerRemoveID        string
	containerRemoveOptions   client.ContainerRemoveOptions
	containerRemoveErr       error

	execCreateContainerID string
	execCreateOptions     client.ExecCreateOptions
	execCreateResponse    client.ExecCreateResult
	execCreateErr         error
	execAttachID          string
	execAttachOptions     client.ExecAttachOptions
	execAttachResponse    client.ExecAttachResult
	execAttachErr         error
	execInspectID         string
	execInspectOptions    client.ExecInspectOptions
	execInspectResponse   client.ExecInspectResult
	execInspectErr        error
	closeErr              error
}

func (c *recordingDockerClient) ContainerCreate(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	c.containerCreateOptions = options
	return c.containerCreateResponse, c.containerCreateErr
}

func (c *recordingDockerClient) ExecAttach(_ context.Context, id string, options client.ExecAttachOptions) (client.ExecAttachResult, error) {
	c.execAttachID = id
	c.execAttachOptions = options
	return c.execAttachResponse, c.execAttachErr
}

func (c *recordingDockerClient) ExecCreate(_ context.Context, containerID string, options client.ExecCreateOptions) (client.ExecCreateResult, error) {
	c.execCreateContainerID = containerID
	c.execCreateOptions = options
	return c.execCreateResponse, c.execCreateErr
}

func (c *recordingDockerClient) ExecInspect(_ context.Context, id string, options client.ExecInspectOptions) (client.ExecInspectResult, error) {
	c.execInspectID = id
	c.execInspectOptions = options
	return c.execInspectResponse, c.execInspectErr
}

func (c *recordingDockerClient) ContainerInspect(_ context.Context, id string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	c.containerInspectID = id
	c.containerInspectOptions = options
	return c.containerInspectResponse, c.containerInspectErr
}

func (c *recordingDockerClient) ContainerRemove(_ context.Context, id string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	c.containerRemoveID = id
	c.containerRemoveOptions = options
	return client.ContainerRemoveResult{}, c.containerRemoveErr
}

func (c *recordingDockerClient) ContainerStart(_ context.Context, id string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
	c.containerStartID = id
	c.containerStartOptions = options
	return client.ContainerStartResult{}, c.containerStartErr
}

func (c *recordingDockerClient) ContainerStop(_ context.Context, id string, options client.ContainerStopOptions) (client.ContainerStopResult, error) {
	c.containerStopID = id
	c.containerStopOptions = options
	return client.ContainerStopResult{}, c.containerStopErr
}

func (c *recordingDockerClient) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	c.imageInspectRef = ref
	return c.imageInspectResponse, c.imageInspectErr
}

func (c *recordingDockerClient) ImagePull(_ context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	c.imagePullRef = ref
	c.imagePullOptions = options
	if c.imagePullErr != nil {
		return nil, c.imagePullErr
	}
	return newFakeImagePullResponse(c.imagePullBody), nil
}

func (c *recordingDockerClient) Close() error {
	return c.closeErr
}

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

	_, err := cm.EnsureContainer(context.Background(), "proj-1", "/tmp/ws", executor.SandboxConfig{Image: "alpine"})
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

	_, err := cm.EnsureContainer(context.Background(), "proj-1", "/tmp/ws", executor.SandboxConfig{})
	if err == nil {
		t.Fatal("expected error for empty image")
	}
}

func TestContainerManager_EnsureContainerPullDoesNotStarveStatus(t *testing.T) {
	client := newBlockingPullDockerClient()
	cm := &ContainerManager{
		client:     client,
		containers: make(map[string]string),
		available:  true,
	}

	ensureDone := make(chan error, 1)
	go func() {
		_, err := cm.EnsureContainer(context.Background(), "proj-1", "/tmp/ws", executor.SandboxConfig{Image: "alpine"})
		ensureDone <- err
	}()

	select {
	case <-client.pullEntered:
	case <-time.After(time.Second):
		close(client.releasePull)
		t.Fatal("EnsureContainer did not reach image pull")
	}

	statusDone := make(chan error, 1)
	go func() {
		status, err := cm.GetContainerStatus(context.Background(), "proj-2")
		if err == nil && status != "none" {
			err = errors.New("unexpected status: " + status)
		}
		statusDone <- err
	}()

	select {
	case err := <-statusDone:
		if err != nil {
			close(client.releasePull)
			t.Fatalf("GetContainerStatus: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(client.releasePull)
		t.Fatal("GetContainerStatus blocked behind EnsureContainer image pull")
	}

	close(client.releasePull)
	if err := <-ensureDone; err != nil {
		t.Fatalf("EnsureContainer: %v", err)
	}
}

func TestContainerManagerDockerLifecycleOptions(t *testing.T) {
	client := &recordingDockerClient{
		imageInspectErr:         errors.New("image not found"),
		containerCreateResponse: client.ContainerCreateResult{ID: "container-1"},
		containerInspectResponse: client.ContainerInspectResult{
			Container: container.InspectResponse{
				State: &container.State{Status: container.ContainerState("PAUSED")},
			},
		},
		execCreateResponse:  client.ExecCreateResult{ID: "exec-1"},
		execInspectResponse: client.ExecInspectResult{ExitCode: 7},
	}
	client.execAttachResponse = dockerExecResponse(t, "stdout", "stderr")
	cm := &ContainerManager{
		client:     client,
		containers: make(map[string]string),
		available:  true,
	}

	id, err := cm.EnsureContainer(t.Context(), "project-1", "/tmp/workspace", executor.SandboxConfig{
		Image:   "alpine:3.22",
		Memory:  "512m",
		CPU:     1.5,
		Network: "bridge",
		Mounts: []executor.SandboxMount{
			{Src: "/tmp/cache", Dst: "/cache", ReadOnly: true},
		},
	})
	if err != nil {
		t.Fatalf("EnsureContainer: %v", err)
	}
	if id != "container-1" {
		t.Fatalf("container ID = %q, want container-1", id)
	}
	if client.imageInspectRef != "alpine:3.22" || client.imagePullRef != "alpine:3.22" {
		t.Fatalf("image inspect/pull refs = %q/%q", client.imageInspectRef, client.imagePullRef)
	}
	if client.containerCreateOptions.Name != "ratchet-project-1" {
		t.Errorf("create name = %q", client.containerCreateOptions.Name)
	}
	createConfig := client.containerCreateOptions.Config
	if createConfig.Image != "alpine:3.22" ||
		createConfig.WorkingDir != "/workspace" ||
		!slices.Equal(createConfig.Cmd, []string{"sleep", "infinity"}) {
		t.Errorf("create config = %#v", createConfig)
	}
	hostConfig := client.containerCreateOptions.HostConfig
	if hostConfig.Memory != 512*1024*1024 || hostConfig.NanoCPUs != 1_500_000_000 ||
		hostConfig.NetworkMode != "bridge" {
		t.Errorf("host config resources = %#v", hostConfig.Resources)
	}
	if len(hostConfig.Mounts) != 2 ||
		hostConfig.Mounts[0].Source != "/tmp/workspace" ||
		hostConfig.Mounts[1].Source != "/tmp/cache" ||
		hostConfig.Mounts[1].Target != "/cache" ||
		!hostConfig.Mounts[1].ReadOnly {
		t.Errorf("host config mounts = %#v", hostConfig.Mounts)
	}
	if client.containerStartID != "container-1" {
		t.Errorf("start ID = %q", client.containerStartID)
	}

	status, err := cm.GetContainerStatus(t.Context(), "project-1")
	if err != nil {
		t.Fatalf("GetContainerStatus: %v", err)
	}
	if status != "paused" || client.containerInspectID != "container-1" {
		t.Fatalf("status/inspect ID = %q/%q", status, client.containerInspectID)
	}

	stdout, stderr, exitCode, err := cm.ExecInContainer(
		t.Context(), "project-1", "echo hello", "/workspace/subdir", 45,
	)
	if err != nil {
		t.Fatalf("ExecInContainer: %v", err)
	}
	if stdout != "stdout" || stderr != "stderr" || exitCode != 7 {
		t.Fatalf("exec result = %q/%q/%d", stdout, stderr, exitCode)
	}
	if client.execCreateContainerID != "container-1" ||
		!slices.Equal(client.execCreateOptions.Cmd, []string{"sh", "-c", "echo hello"}) ||
		client.execCreateOptions.WorkingDir != "/workspace/subdir" ||
		!client.execCreateOptions.AttachStdout ||
		!client.execCreateOptions.AttachStderr {
		t.Errorf("exec create = container %q options %#v", client.execCreateContainerID, client.execCreateOptions)
	}
	if client.execAttachID != "exec-1" || client.execInspectID != "exec-1" {
		t.Errorf("exec attach/inspect IDs = %q/%q", client.execAttachID, client.execInspectID)
	}

	if err := cm.StopContainer(t.Context(), "project-1"); err != nil {
		t.Fatalf("StopContainer: %v", err)
	}
	if client.containerStopID != "container-1" {
		t.Errorf("stop ID = %q", client.containerStopID)
	}

	cm.containers["project-1"] = "container-1"
	if err := cm.RemoveContainer(t.Context(), "project-1"); err != nil {
		t.Fatalf("RemoveContainer: %v", err)
	}
	if client.containerRemoveID != "container-1" || !client.containerRemoveOptions.Force {
		t.Errorf("remove ID/options = %q/%#v", client.containerRemoveID, client.containerRemoveOptions)
	}
}

func TestContainerManagerDockerErrors(t *testing.T) {
	t.Run("image pull", func(t *testing.T) {
		client := &recordingDockerClient{
			imageInspectErr: errors.New("missing"),
			imagePullErr:    errors.New("registry unavailable"),
		}
		cm := newAvailableContainerManager(client)
		_, err := cm.EnsureContainer(t.Context(), "project-1", "/tmp/workspace", executor.SandboxConfig{Image: "alpine"})
		if err == nil || !strings.Contains(err.Error(), "registry unavailable") {
			t.Fatalf("EnsureContainer error = %v", err)
		}
	})

	t.Run("start cleans up created container", func(t *testing.T) {
		client := &recordingDockerClient{
			containerCreateResponse: client.ContainerCreateResult{ID: "container-1"},
			containerStartErr:       errors.New("start failed"),
		}
		cm := newAvailableContainerManager(client)
		_, err := cm.EnsureContainer(t.Context(), "project-1", "/tmp/workspace", executor.SandboxConfig{Image: "alpine"})
		if err == nil || !strings.Contains(err.Error(), "start failed") {
			t.Fatalf("EnsureContainer error = %v", err)
		}
		if client.containerRemoveID != "container-1" || !client.containerRemoveOptions.Force {
			t.Fatalf("cleanup remove = %q/%#v", client.containerRemoveID, client.containerRemoveOptions)
		}
	})

	t.Run("status inspect", func(t *testing.T) {
		client := &recordingDockerClient{containerInspectErr: errors.New("inspect failed")}
		cm := newAvailableContainerManager(client)
		cm.containers["project-1"] = "container-1"
		status, err := cm.GetContainerStatus(t.Context(), "project-1")
		if status != "unknown" || err == nil || !strings.Contains(err.Error(), "inspect failed") {
			t.Fatalf("GetContainerStatus = %q, %v", status, err)
		}
	})

	t.Run("exec create", func(t *testing.T) {
		client := &recordingDockerClient{execCreateErr: errors.New("exec failed")}
		cm := newAvailableContainerManager(client)
		cm.containers["project-1"] = "container-1"
		_, _, exitCode, err := cm.ExecInContainer(t.Context(), "project-1", "false", "/workspace", 30)
		if exitCode != -1 || err == nil || !strings.Contains(err.Error(), "exec failed") {
			t.Fatalf("ExecInContainer exit/error = %d/%v", exitCode, err)
		}
	})

	t.Run("stop and remove", func(t *testing.T) {
		client := &recordingDockerClient{containerStopErr: errors.New("stop failed")}
		cm := newAvailableContainerManager(client)
		cm.containers["project-1"] = "container-1"
		if err := cm.StopContainer(t.Context(), "project-1"); err == nil || !strings.Contains(err.Error(), "stop failed") {
			t.Fatalf("StopContainer error = %v", err)
		}

		client.containerRemoveErr = errors.New("remove failed")
		cm.containers["project-1"] = "container-1"
		if err := cm.RemoveContainer(t.Context(), "project-1"); err == nil || !strings.Contains(err.Error(), "remove failed") {
			t.Fatalf("RemoveContainer error = %v", err)
		}
	})
}

func newAvailableContainerManager(client dockerAPIClient) *ContainerManager {
	return &ContainerManager{
		client:     client,
		containers: make(map[string]string),
		available:  true,
	}
}

func dockerExecResponse(t *testing.T, stdout, stderr string) client.ExecAttachResult {
	t.Helper()
	var stream bytes.Buffer
	if err := writeDockerFrame(&stream, stdcopy.Stdout, stdout); err != nil {
		t.Fatalf("encode stdout: %v", err)
	}
	if err := writeDockerFrame(&stream, stdcopy.Stderr, stderr); err != nil {
		t.Fatalf("encode stderr: %v", err)
	}

	server, connection := net.Pipe()
	go func() {
		_, _ = server.Write(stream.Bytes())
		_ = server.Close()
	}()
	return client.ExecAttachResult{
		HijackedResponse: client.NewHijackedResponse(connection, "application/vnd.docker.multiplexed-stream"),
	}
}

func writeDockerFrame(writer io.Writer, stream stdcopy.StdType, payload string) error {
	header := [8]byte{0: byte(stream)}
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err := io.WriteString(writer, payload)
	return err
}

type fakeImagePullResponse struct {
	io.ReadCloser
}

func newFakeImagePullResponse(body string) *fakeImagePullResponse {
	return &fakeImagePullResponse{ReadCloser: io.NopCloser(strings.NewReader(body))}
}

func (r *fakeImagePullResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}

func (r *fakeImagePullResponse) Wait(context.Context) error {
	_, err := io.Copy(io.Discard, r)
	return err
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
