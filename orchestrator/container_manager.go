package orchestrator

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// MountSpec describes a single bind mount.
type MountSpec struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readonly,omitempty"`
}

// WorkspaceSpec describes the container configuration for a project workspace.
type WorkspaceSpec struct {
	Image        string            `json:"image"`
	InitCommands []string          `json:"init_commands,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	MemoryLimit  int64             `json:"memory_limit,omitempty"`
	CPULimit     float64           `json:"cpu_limit,omitempty"`
	NetworkMode  string            `json:"network_mode,omitempty"`
	Mounts       []MountSpec       `json:"mounts,omitempty"`
}

// ContainerManager manages Docker containers for project workspaces.
// It maintains a cache of projectID -> containerID mappings and persists
// state to the workspace_containers table.
type ContainerManager struct {
	mu         sync.Mutex
	client     client.APIClient
	db         *sql.DB
	available  bool
	containers map[string]string // projectID -> containerID
}

// NewContainerManager creates a ContainerManager. It attempts to connect to
// the Docker daemon; if unavailable, the manager is marked as not available
// and all operations gracefully fall back.
func NewContainerManager(db *sql.DB) *ContainerManager {
	cm := &ContainerManager{
		db:         db,
		containers: make(map[string]string),
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return cm
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = cli.Ping(ctx)
	if err != nil {
		_ = cli.Close()
		return cm
	}

	cm.client = cli
	cm.available = true

	// Ensure the DB table exists
	if db != nil {
		_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS workspace_containers (
			project_id TEXT PRIMARY KEY,
			container_id TEXT NOT NULL,
			image TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)`)

		// Load existing containers into cache
		rows, err := db.Query("SELECT project_id, container_id FROM workspace_containers WHERE status = 'running'")
		if err == nil {
			func() {
				defer func() { _ = rows.Close() }()
				for rows.Next() {
					var pid, cid string
					if rows.Scan(&pid, &cid) == nil {
						cm.containers[pid] = cid
					}
				}
				if err := rows.Err(); err != nil {
					log.Printf("container_manager: error loading container cache: %v", err)
				}
			}()
		}
	}

	return cm
}

// IsAvailable returns true if the Docker daemon is reachable.
func (cm *ContainerManager) IsAvailable() bool {
	return cm.available
}

// EnsureContainer creates or reuses a container for the given project.
// The workspace path is bind-mounted at /workspace inside the container.
func (cm *ContainerManager) EnsureContainer(ctx context.Context, projectID, workspacePath string, spec WorkspaceSpec) (string, error) {
	if !cm.available {
		return "", fmt.Errorf("container manager: docker not available")
	}
	if spec.Image == "" {
		return "", fmt.Errorf("container manager: image is required")
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Check cache first
	if cid, ok := cm.containers[projectID]; ok {
		// Verify container is still running
		info, err := cm.client.ContainerInspect(ctx, cid)
		if err == nil && info.State.Running {
			return cid, nil
		}
		// Container gone or stopped — remove from cache
		delete(cm.containers, projectID)
	}

	// Ensure image is available
	if err := cm.ensureImage(ctx, spec.Image); err != nil {
		return "", fmt.Errorf("container manager: pull image: %w", err)
	}

	// Build env slice
	var env []string
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}

	// Create container with sleep infinity entrypoint
	containerCfg := &container.Config{
		Image:      spec.Image,
		Cmd:        []string{"sleep", "infinity"},
		Env:        env,
		WorkingDir: "/workspace",
	}

	mounts := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: workspacePath,
			Target: "/workspace",
		},
	}
	for _, m := range spec.Mounts {
		if err := validateMountPaths(m.Source, m.Target); err != nil {
			return "", fmt.Errorf("container manager: invalid mount: %w", err)
		}
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	hostCfg := &container.HostConfig{
		Mounts: mounts,
	}
	if spec.MemoryLimit > 0 {
		hostCfg.Memory = spec.MemoryLimit
	}
	if spec.CPULimit > 0 {
		hostCfg.NanoCPUs = int64(spec.CPULimit * 1e9)
	}
	if spec.NetworkMode != "" {
		hostCfg.NetworkMode = container.NetworkMode(spec.NetworkMode)
	}

	resp, err := cm.client.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "ratchet-"+projectID)
	if err != nil {
		return "", fmt.Errorf("container manager: create container: %w", err)
	}

	if err := cm.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Clean up on failure
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rmCancel()
		_ = cm.client.ContainerRemove(rmCtx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("container manager: start container: %w", err)
	}

	cm.containers[projectID] = resp.ID

	// Persist to DB
	if cm.db != nil {
		_, _ = cm.db.ExecContext(ctx,
			`INSERT OR REPLACE INTO workspace_containers (project_id, container_id, image, status, updated_at)
			 VALUES (?, ?, ?, 'running', datetime('now'))`,
			projectID, resp.ID, spec.Image,
		)
	}

	// Run init commands
	for _, initCmd := range spec.InitCommands {
		_, _, _, err := cm.execInContainerLocked(ctx, resp.ID, initCmd, "/workspace", 60)
		if err != nil {
			log.Printf("container_manager: init command %q failed: %v", initCmd, err)
		}
	}

	return resp.ID, nil
}

// ExecInContainer executes a command inside the container for the given project.
func (cm *ContainerManager) ExecInContainer(ctx context.Context, projectID, command, workDir string, timeout int) (stdout, stderr string, exitCode int, err error) {
	if !cm.available {
		return "", "", -1, fmt.Errorf("container manager: docker not available")
	}

	cm.mu.Lock()
	cid, ok := cm.containers[projectID]
	cm.mu.Unlock()

	if !ok {
		return "", "", -1, fmt.Errorf("container manager: no container for project %q", projectID)
	}

	return cm.execInContainerLocked(ctx, cid, command, workDir, timeout)
}

// execInContainerLocked performs docker exec. Caller may or may not hold the mutex.
func (cm *ContainerManager) execInContainerLocked(ctx context.Context, containerID, command, workDir string, timeout int) (string, string, int, error) {
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		timeout = 300
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", command},
		WorkingDir:   workDir,
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := cm.client.ContainerExecCreate(execCtx, containerID, execCfg)
	if err != nil {
		return "", "", -1, fmt.Errorf("container exec create: %w", err)
	}

	attachResp, err := cm.client.ContainerExecAttach(execCtx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", "", -1, fmt.Errorf("container exec attach: %w", err)
	}
	defer attachResp.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	_, err = stdcopy.StdCopy(&stdoutBuf, &stderrBuf, attachResp.Reader)
	if err != nil {
		return "", "", -1, fmt.Errorf("container exec read: %w", err)
	}

	inspectResp, err := cm.client.ContainerExecInspect(execCtx, execResp.ID)
	if err != nil {
		return stdoutBuf.String(), stderrBuf.String(), -1, fmt.Errorf("container exec inspect: %w", err)
	}

	return stdoutBuf.String(), stderrBuf.String(), inspectResp.ExitCode, nil
}

// StopContainer stops the container for a project.
func (cm *ContainerManager) StopContainer(ctx context.Context, projectID string) error {
	if !cm.available {
		return fmt.Errorf("container manager: docker not available")
	}

	cm.mu.Lock()
	cid, ok := cm.containers[projectID]
	if ok {
		delete(cm.containers, projectID)
	}
	cm.mu.Unlock()

	if !ok {
		return fmt.Errorf("container manager: no container for project %q", projectID)
	}

	if err := cm.client.ContainerStop(ctx, cid, container.StopOptions{}); err != nil {
		return fmt.Errorf("container manager: stop: %w", err)
	}

	if cm.db != nil {
		_, _ = cm.db.ExecContext(ctx,
			"UPDATE workspace_containers SET status = 'stopped', updated_at = datetime('now') WHERE project_id = ?",
			projectID,
		)
	}

	return nil
}

// RemoveContainer stops and removes the container for a project.
func (cm *ContainerManager) RemoveContainer(ctx context.Context, projectID string) error {
	if !cm.available {
		return fmt.Errorf("container manager: docker not available")
	}

	cm.mu.Lock()
	cid, ok := cm.containers[projectID]
	if ok {
		delete(cm.containers, projectID)
	}
	cm.mu.Unlock()

	if !ok {
		return fmt.Errorf("container manager: no container for project %q", projectID)
	}

	if err := cm.client.ContainerRemove(ctx, cid, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("container manager: remove: %w", err)
	}

	if cm.db != nil {
		_, _ = cm.db.ExecContext(ctx,
			"DELETE FROM workspace_containers WHERE project_id = ?",
			projectID,
		)
	}

	return nil
}

// GetContainerStatus returns the status of the container for a project.
func (cm *ContainerManager) GetContainerStatus(ctx context.Context, projectID string) (string, error) {
	if !cm.available {
		return "unavailable", nil
	}

	cm.mu.Lock()
	cid, ok := cm.containers[projectID]
	cm.mu.Unlock()

	if !ok {
		return "none", nil
	}

	info, err := cm.client.ContainerInspect(ctx, cid)
	if err != nil {
		return "unknown", fmt.Errorf("container manager: inspect: %w", err)
	}

	return strings.ToLower(info.State.Status), nil
}

// Close stops all managed containers and closes the Docker client.
func (cm *ContainerManager) Close() error {
	if !cm.available || cm.client == nil {
		return nil
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for pid, cid := range cm.containers {
		_ = cm.client.ContainerStop(ctx, cid, container.StopOptions{})
		delete(cm.containers, pid)
	}

	return cm.client.Close()
}

// ensureImage pulls the image if not present locally.
func (cm *ContainerManager) ensureImage(ctx context.Context, img string) error {
	_, err := cm.client.ImageInspect(ctx, img)
	if err == nil {
		return nil
	}

	reader, err := cm.client.ImagePull(ctx, img, image.PullOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	_, err = io.Copy(io.Discard, reader)
	return err
}

// validateMountPaths checks that a bind mount source and target are safe.
// Source must be an absolute path. Target must not be a critical system directory.
func validateMountPaths(source, target string) error {
	if !filepath.IsAbs(source) {
		return fmt.Errorf("mount source must be an absolute path: %q", source)
	}
	if !filepath.IsAbs(target) {
		return fmt.Errorf("mount target must be an absolute path: %q", target)
	}
	cleanTarget := filepath.Clean(target)
	for _, critical := range []string{"/", "/etc", "/bin", "/usr", "/lib", "/lib64", "/sbin", "/sys", "/proc", "/dev"} {
		if cleanTarget == critical {
			return fmt.Errorf("mount target %q is a critical system path", cleanTarget)
		}
	}
	return nil
}
