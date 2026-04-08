package orchestrator

import (
	"context"

	"github.com/GoCodeAlone/workflow-plugin-agent/executor"
)

// ContainerManagerAdapter wraps ContainerManager to satisfy executor.ContainerExecutor.
// It converts executor.SandboxConfig to orchestrator.WorkspaceSpec for the underlying manager.
type ContainerManagerAdapter struct {
	cm *ContainerManager
}

// NewContainerManagerAdapter wraps a ContainerManager as an executor.ContainerExecutor.
func NewContainerManagerAdapter(cm *ContainerManager) executor.ContainerExecutor {
	return &ContainerManagerAdapter{cm: cm}
}

func (a *ContainerManagerAdapter) IsAvailable() bool {
	return a.cm.IsAvailable()
}

func (a *ContainerManagerAdapter) EnsureContainer(ctx context.Context, projectID, workspacePath string, spec executor.SandboxConfig) (string, error) {
	ws := WorkspaceSpec{
		Image:        spec.Image,
		NetworkMode:  spec.Network,
		InitCommands: spec.InitCommands,
	}
	for _, m := range spec.Mounts {
		ws.Mounts = append(ws.Mounts, MountSpec{
			Source:   m.Src,
			Target:   m.Dst,
			ReadOnly: m.ReadOnly,
		})
	}
	return a.cm.EnsureContainer(ctx, projectID, workspacePath, ws)
}

func (a *ContainerManagerAdapter) ExecInContainer(ctx context.Context, projectID, command, workDir string, timeout int) (string, string, int, error) {
	return a.cm.ExecInContainer(ctx, projectID, command, workDir, timeout)
}
