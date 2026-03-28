package orchestrator

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// ContainerControlStep manages container lifecycle as a pipeline step.
// Actions: "start", "stop", "remove", "status".
type ContainerControlStep struct {
	name   string
	action string
	app    modular.Application
	tmpl   *module.TemplateEngine
}

func (s *ContainerControlStep) Name() string { return s.name }

func (s *ContainerControlStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	projectID := extractString(pc.Current, "project_id", "")
	if projectID == "" {
		return nil, fmt.Errorf("container_control step %q: project_id is required", s.name)
	}

	// Lazy-lookup ContainerManager (registered by wiring hook after step factories)
	var containerMgr *ContainerManager
	if svc, ok := s.app.SvcRegistry()["ratchet-container-manager"]; ok {
		containerMgr, _ = svc.(*ContainerManager)
	}

	if containerMgr == nil || !containerMgr.IsAvailable() {
		return &module.StepResult{
			Output: map[string]any{
				"status":  "unavailable",
				"message": "container manager not available",
			},
		}, nil
	}

	switch s.action {
	case "start":
		workspacePath := extractString(pc.Current, "workspace_path", "")
		if workspacePath == "" {
			return nil, fmt.Errorf("container_control step %q: workspace_path required for start", s.name)
		}
		imageStr := extractString(pc.Current, "image", "")
		if imageStr == "" {
			return nil, fmt.Errorf("container_control step %q: image required for start", s.name)
		}

		spec := WorkspaceSpec{Image: imageStr}
		cid, err := containerMgr.EnsureContainer(ctx, projectID, workspacePath, spec)
		if err != nil {
			return nil, fmt.Errorf("container_control step %q: %w", s.name, err)
		}
		return &module.StepResult{
			Output: map[string]any{
				"status":       "running",
				"container_id": cid,
				"project_id":   projectID,
			},
		}, nil

	case "stop":
		if err := containerMgr.StopContainer(ctx, projectID); err != nil {
			return nil, fmt.Errorf("container_control step %q: %w", s.name, err)
		}
		return &module.StepResult{
			Output: map[string]any{
				"status":     "stopped",
				"project_id": projectID,
			},
		}, nil

	case "remove":
		if err := containerMgr.RemoveContainer(ctx, projectID); err != nil {
			return nil, fmt.Errorf("container_control step %q: %w", s.name, err)
		}
		return &module.StepResult{
			Output: map[string]any{
				"status":     "removed",
				"project_id": projectID,
			},
		}, nil

	case "status":
		status, err := containerMgr.GetContainerStatus(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("container_control step %q: %w", s.name, err)
		}
		return &module.StepResult{
			Output: map[string]any{
				"status":     status,
				"project_id": projectID,
			},
		}, nil

	default:
		return nil, fmt.Errorf("container_control step %q: unknown action %q (want start|stop|remove|status)", s.name, s.action)
	}
}

// newContainerControlFactory returns a plugin.StepFactory for "step.container_control".
func newContainerControlFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, app modular.Application) (any, error) {
		action, _ := cfg["action"].(string)
		if action == "" {
			action = "status"
		}

		return &ContainerControlStep{
			name:   name,
			action: action,
			app:    app,
			tmpl:   module.NewTemplateEngine(),
		}, nil
	}
}
