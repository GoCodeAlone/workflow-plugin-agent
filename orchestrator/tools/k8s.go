package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// kubectlRun executes a kubectl command and returns stdout+stderr.
// It enforces a 30-second timeout to prevent runaway subprocesses.
func kubectlRun(ctx context.Context, args ...string) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", "", -1, fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), err)
		}
	}
	return stdout.String(), stderr.String(), exitCode, nil
}

// kubectlJSON runs a kubectl command with -o json and unmarshals the result.
func kubectlJSON(ctx context.Context, args ...string) (map[string]any, string, int, error) {
	args = append(args, "-o", "json")
	stdout, stderr, exitCode, err := kubectlRun(ctx, args...)
	if err != nil {
		return nil, stderr, exitCode, err
	}
	var result map[string]any
	if stdout != "" {
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			return nil, stderr, exitCode, fmt.Errorf("parse kubectl JSON: %w", err)
		}
	}
	return result, stderr, exitCode, nil
}

// ---- K8sGetPodsTool ---------------------------------------------------------

// K8sGetPodsTool lists pods in a namespace with status, restarts, and age.
type K8sGetPodsTool struct{}

func (t *K8sGetPodsTool) Name() string { return "k8s_get_pods" }
func (t *K8sGetPodsTool) Description() string {
	return "List Kubernetes pods in a namespace with their status, restart count, and age"
}
func (t *K8sGetPodsTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string", "description": "Kubernetes namespace (default: default)"},
				"selector":  map[string]any{"type": "string", "description": "Label selector to filter pods (e.g. app=nginx)"},
			},
		},
	}
}
func (t *K8sGetPodsTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}
	cmdArgs := []string{"get", "pods", "-n", ns}
	if sel, ok := args["selector"].(string); ok && sel != "" {
		cmdArgs = append(cmdArgs, "-l", sel)
	}

	result, stderr, exitCode, err := kubectlJSON(ctx, cmdArgs...)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]any{"error": stderr, "exit_code": exitCode}, nil
	}

	pods := extractPodSummaries(result)
	return map[string]any{
		"namespace": ns,
		"pods":      pods,
		"count":     len(pods),
	}, nil
}

// extractPodSummaries parses the kubectl JSON items list into simplified pod summaries.
func extractPodSummaries(raw map[string]any) []map[string]any {
	items, _ := raw["items"].([]any)
	summaries := make([]map[string]any, 0, len(items))
	for _, item := range items {
		pod, ok := item.(map[string]any)
		if !ok {
			continue
		}
		meta, _ := pod["metadata"].(map[string]any)
		spec, _ := pod["spec"].(map[string]any)
		status, _ := pod["status"].(map[string]any)

		name, _ := meta["name"].(string)
		phase, _ := status["phase"].(string)
		startTime, _ := status["startTime"].(string)

		// Count total restarts across all containers
		totalRestarts := 0
		var containerStatuses []map[string]any
		if cs, ok := status["containerStatuses"].([]any); ok {
			for _, c := range cs {
				if cMap, ok := c.(map[string]any); ok {
					containerStatuses = append(containerStatuses, cMap)
					if r, ok := cMap["restartCount"].(float64); ok {
						totalRestarts += int(r)
					}
				}
			}
		}

		// Determine readiness
		ready := 0
		total := 0
		if containers, ok := spec["containers"].([]any); ok {
			total = len(containers)
		}
		for _, cs := range containerStatuses {
			if r, ok := cs["ready"].(bool); ok && r {
				ready++
			}
		}

		// Detect CrashLoopBackOff or other waiting states
		waitingReason := ""
		for _, cs := range containerStatuses {
			state, _ := cs["state"].(map[string]any)
			if waiting, ok := state["waiting"].(map[string]any); ok {
				if reason, ok := waiting["reason"].(string); ok {
					waitingReason = reason
					break
				}
			}
		}

		s := map[string]any{
			"name":       name,
			"phase":      phase,
			"ready":      fmt.Sprintf("%d/%d", ready, total),
			"restarts":   totalRestarts,
			"start_time": startTime,
		}
		if waitingReason != "" {
			s["waiting_reason"] = waitingReason
		}
		summaries = append(summaries, s)
	}
	return summaries
}

// ---- K8sGetEventsTool -------------------------------------------------------

// K8sGetEventsTool retrieves cluster events, filtered by namespace and optionally by type.
type K8sGetEventsTool struct{}

func (t *K8sGetEventsTool) Name() string { return "k8s_get_events" }
func (t *K8sGetEventsTool) Description() string {
	return "Get Kubernetes cluster events — warnings, errors, and scheduling failures"
}
func (t *K8sGetEventsTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string", "description": "Kubernetes namespace (default: default)"},
				"type":      map[string]any{"type": "string", "description": "Event type filter: Warning or Normal (default: Warning)"},
				"limit":     map[string]any{"type": "integer", "description": "Maximum events to return (default: 20)"},
			},
		},
	}
}
func (t *K8sGetEventsTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}
	evtType := "Warning"
	if v, ok := args["type"].(string); ok && v != "" {
		evtType = v
	}
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	cmdArgs := []string{"get", "events", "-n", ns, "--sort-by=.lastTimestamp"}
	if evtType != "" {
		cmdArgs = append(cmdArgs, "--field-selector", "type="+evtType)
	}

	result, stderr, exitCode, err := kubectlJSON(ctx, cmdArgs...)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]any{"error": stderr, "exit_code": exitCode}, nil
	}

	items, _ := result["items"].([]any)
	events := make([]map[string]any, 0, limit)
	// Take the last `limit` events (most recent)
	start := 0
	if len(items) > limit {
		start = len(items) - limit
	}
	for _, item := range items[start:] {
		evt, ok := item.(map[string]any)
		if !ok {
			continue
		}
		meta, _ := evt["metadata"].(map[string]any)
		involvedObj, _ := evt["involvedObject"].(map[string]any)

		events = append(events, map[string]any{
			"name":             meta["name"],
			"reason":           evt["reason"],
			"message":          evt["message"],
			"type":             evt["type"],
			"count":            evt["count"],
			"first_time":       evt["firstTimestamp"],
			"last_time":        evt["lastTimestamp"],
			"object_kind":      involvedObj["kind"],
			"object_name":      involvedObj["name"],
			"object_namespace": involvedObj["namespace"],
		})
	}

	return map[string]any{
		"namespace": ns,
		"type":      evtType,
		"events":    events,
		"count":     len(events),
	}, nil
}

// ---- K8sGetLogsTool ---------------------------------------------------------

// K8sGetLogsTool retrieves container logs from a pod.
type K8sGetLogsTool struct{}

func (t *K8sGetLogsTool) Name() string { return "k8s_get_logs" }
func (t *K8sGetLogsTool) Description() string {
	return "Get logs from a Kubernetes pod container with optional tail/since filters"
}
func (t *K8sGetLogsTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pod":       map[string]any{"type": "string", "description": "Pod name"},
				"namespace": map[string]any{"type": "string", "description": "Namespace (default: default)"},
				"container": map[string]any{"type": "string", "description": "Container name (optional, uses first container if omitted)"},
				"tail":      map[string]any{"type": "integer", "description": "Number of log lines to tail (default: 100)"},
				"since":     map[string]any{"type": "string", "description": "Only return logs newer than a relative duration like 5m or 1h"},
				"previous":  map[string]any{"type": "boolean", "description": "Return logs from the previously terminated container"},
			},
			"required": []string{"pod"},
		},
	}
}
func (t *K8sGetLogsTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	pod, _ := args["pod"].(string)
	if pod == "" {
		return nil, fmt.Errorf("pod is required")
	}
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}
	tail := 100
	if v, ok := args["tail"].(float64); ok && v > 0 {
		tail = int(v)
	}
	if tail > 1000 {
		tail = 1000
	}

	cmdArgs := []string{"logs", pod, "-n", ns, fmt.Sprintf("--tail=%d", tail)}
	if container, ok := args["container"].(string); ok && container != "" {
		cmdArgs = append(cmdArgs, "-c", container)
	}
	if since, ok := args["since"].(string); ok && since != "" {
		cmdArgs = append(cmdArgs, "--since="+since)
	}
	if prev, ok := args["previous"].(bool); ok && prev {
		cmdArgs = append(cmdArgs, "--previous")
	}

	stdout, stderr, exitCode, err := kubectlRun(ctx, cmdArgs...)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]any{"error": stderr, "exit_code": exitCode}, nil
	}
	return map[string]any{
		"pod":    pod,
		"logs":   stdout,
		"stderr": stderr,
	}, nil
}

// ---- K8sDescribeTool --------------------------------------------------------

// K8sDescribeTool describes any Kubernetes resource.
type K8sDescribeTool struct{}

func (t *K8sDescribeTool) Name() string { return "k8s_describe" }
func (t *K8sDescribeTool) Description() string {
	return "Describe a Kubernetes resource (pod, deployment, service, node, etc.) to get detailed status and events"
}
func (t *K8sDescribeTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":      map[string]any{"type": "string", "description": "Resource kind: pod, deployment, service, node, configmap, etc."},
				"name":      map[string]any{"type": "string", "description": "Resource name"},
				"namespace": map[string]any{"type": "string", "description": "Namespace (default: default)"},
			},
			"required": []string{"kind", "name"},
		},
	}
}
func (t *K8sDescribeTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	kind, _ := args["kind"].(string)
	if kind == "" {
		return nil, fmt.Errorf("kind is required")
	}
	name, _ := args["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}

	stdout, stderr, exitCode, err := kubectlRun(ctx, "describe", kind, name, "-n", ns)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]any{"error": stderr, "exit_code": exitCode}, nil
	}
	return map[string]any{
		"kind":      kind,
		"name":      name,
		"namespace": ns,
		"output":    stdout,
	}, nil
}

// ---- K8sRestartPodTool ------------------------------------------------------

// K8sRestartPodTool deletes a pod to trigger a restart (requires approval).
// The Deployment controller recreates it automatically.
type K8sRestartPodTool struct{}

func (t *K8sRestartPodTool) Name() string { return "k8s_restart_pod" }
func (t *K8sRestartPodTool) Description() string {
	return "Restart a Kubernetes pod by deleting it (the Deployment controller recreates it). " +
		"Use request_approval first for destructive restarts that may cause brief downtime."
}
func (t *K8sRestartPodTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pod":       map[string]any{"type": "string", "description": "Pod name to delete/restart"},
				"namespace": map[string]any{"type": "string", "description": "Namespace (default: default)"},
			},
			"required": []string{"pod"},
		},
	}
}
func (t *K8sRestartPodTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	pod, _ := args["pod"].(string)
	if pod == "" {
		return nil, fmt.Errorf("pod is required")
	}
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}

	_, stderr, exitCode, err := kubectlRun(ctx, "delete", "pod", pod, "-n", ns, "--grace-period=0")
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]any{"error": stderr, "exit_code": exitCode}, nil
	}
	return map[string]any{
		"pod":       pod,
		"namespace": ns,
		"action":    "restarted",
		"message":   fmt.Sprintf("Pod %s deleted; the Deployment controller will recreate it.", pod),
	}, nil
}

// ---- K8sScaleTool -----------------------------------------------------------

// K8sScaleTool scales a Deployment to a given replica count (requires approval).
type K8sScaleTool struct{}

func (t *K8sScaleTool) Name() string { return "k8s_scale" }
func (t *K8sScaleTool) Description() string {
	return "Scale a Kubernetes Deployment to a specified number of replicas. " +
		"Always use request_approval before scaling — scaling to 0 causes full downtime."
}
func (t *K8sScaleTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"deployment": map[string]any{"type": "string", "description": "Deployment name"},
				"replicas":   map[string]any{"type": "integer", "description": "Target replica count (must be >= 1 without explicit approval)"},
				"namespace":  map[string]any{"type": "string", "description": "Namespace (default: default)"},
			},
			"required": []string{"deployment", "replicas"},
		},
	}
}
func (t *K8sScaleTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	deployment, _ := args["deployment"].(string)
	if deployment == "" {
		return nil, fmt.Errorf("deployment is required")
	}
	replicas, ok := args["replicas"].(float64)
	if !ok {
		return nil, fmt.Errorf("replicas is required and must be a number")
	}
	if int(replicas) < 0 {
		return nil, fmt.Errorf("replicas cannot be negative")
	}
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}

	_, stderr, exitCode, err := kubectlRun(ctx,
		"scale", "deployment", deployment,
		fmt.Sprintf("--replicas=%d", int(replicas)),
		"-n", ns,
	)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]any{"error": stderr, "exit_code": exitCode}, nil
	}
	return map[string]any{
		"deployment": deployment,
		"namespace":  ns,
		"replicas":   int(replicas),
		"action":     "scaled",
	}, nil
}

// ---- K8sRollbackTool --------------------------------------------------------

// K8sRollbackTool rolls back a Deployment to the previous revision (requires approval).
type K8sRollbackTool struct{}

func (t *K8sRollbackTool) Name() string { return "k8s_rollback" }
func (t *K8sRollbackTool) Description() string {
	return "Roll back a Kubernetes Deployment to its previous revision. " +
		"Always use request_approval before rolling back — this changes running software."
}
func (t *K8sRollbackTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"deployment": map[string]any{"type": "string", "description": "Deployment name"},
				"namespace":  map[string]any{"type": "string", "description": "Namespace (default: default)"},
				"revision":   map[string]any{"type": "integer", "description": "Specific revision to rollback to (default: previous)"},
			},
			"required": []string{"deployment"},
		},
	}
}
func (t *K8sRollbackTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	deployment, _ := args["deployment"].(string)
	if deployment == "" {
		return nil, fmt.Errorf("deployment is required")
	}
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}

	cmdArgs := []string{"rollout", "undo", "deployment/" + deployment, "-n", ns}
	if rev, ok := args["revision"].(float64); ok && rev > 0 {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--to-revision=%d", int(rev)))
	}

	_, stderr, exitCode, err := kubectlRun(ctx, cmdArgs...)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]any{"error": stderr, "exit_code": exitCode}, nil
	}
	return map[string]any{
		"deployment": deployment,
		"namespace":  ns,
		"action":     "rolled-back",
	}, nil
}

// ---- K8sApplyTool -----------------------------------------------------------

// K8sApplyTool applies a Kubernetes manifest from a YAML string (requires approval).
type K8sApplyTool struct{}

func (t *K8sApplyTool) Name() string { return "k8s_apply" }
func (t *K8sApplyTool) Description() string {
	return "Apply a Kubernetes manifest (YAML) to the cluster. " +
		"Always use request_approval before applying — this modifies cluster resources."
}
func (t *K8sApplyTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"manifest":  map[string]any{"type": "string", "description": "YAML manifest content to apply"},
				"namespace": map[string]any{"type": "string", "description": "Default namespace (default: default)"},
			},
			"required": []string{"manifest"},
		},
	}
}
func (t *K8sApplyTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	manifest, _ := args["manifest"].(string)
	if manifest == "" {
		return nil, fmt.Errorf("manifest is required")
	}
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-", "-n", ns)
	cmd.Stdin = strings.NewReader(manifest)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("kubectl apply: %w", err)
		}
	}
	if exitCode != 0 {
		return map[string]any{"error": stderr.String(), "exit_code": exitCode}, nil
	}
	return map[string]any{
		"namespace": ns,
		"output":    stdout.String(),
		"action":    "applied",
	}, nil
}

// ---- InfraHealthCheckTool ---------------------------------------------------

// InfraHealthCheckTool runs an aggregate health check across all namespaces and
// returns a summarized health score and list of detected issues.
type InfraHealthCheckTool struct{}

func (t *InfraHealthCheckTool) Name() string { return "infra_health_check" }
func (t *InfraHealthCheckTool) Description() string {
	return "Run an aggregate infrastructure health check across all namespaces. " +
		"Returns a health score (0-100), list of unhealthy pods, and recent warning events."
}
func (t *InfraHealthCheckTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string", "description": "Namespace to check (default: all namespaces)"},
			},
		},
	}
}
func (t *InfraHealthCheckTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	ns := ""
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}

	// Get all pods
	podArgs := []string{"get", "pods"}
	if ns != "" {
		podArgs = append(podArgs, "-n", ns)
	} else {
		podArgs = append(podArgs, "--all-namespaces")
	}
	podsResult, podStderr, podExitCode, err := kubectlJSON(ctx, podArgs...)
	if err != nil {
		return nil, fmt.Errorf("get pods: %w", err)
	}
	if podExitCode != 0 {
		return map[string]any{"error": podStderr}, nil
	}

	// Analyze pods
	type issueRecord struct {
		Pod       string `json:"pod"`
		Namespace string `json:"namespace"`
		Issue     string `json:"issue"`
		Restarts  int    `json:"restarts"`
	}

	items, _ := podsResult["items"].([]any)
	totalPods := len(items)
	unhealthyPods := 0
	var issues []issueRecord

	for _, item := range items {
		pod, ok := item.(map[string]any)
		if !ok {
			continue
		}
		meta, _ := pod["metadata"].(map[string]any)
		status, _ := pod["status"].(map[string]any)

		podName, _ := meta["name"].(string)
		podNs, _ := meta["namespace"].(string)
		phase, _ := status["phase"].(string)

		restarts := 0
		if cs, ok := status["containerStatuses"].([]any); ok {
			for _, c := range cs {
				if cMap, ok := c.(map[string]any); ok {
					if r, ok := cMap["restartCount"].(float64); ok {
						restarts += int(r)
					}
					state, _ := cMap["state"].(map[string]any)
					if waiting, ok := state["waiting"].(map[string]any); ok {
						reason, _ := waiting["reason"].(string)
						if reason == "CrashLoopBackOff" || reason == "OOMKilled" || reason == "Error" ||
							reason == "ImagePullBackOff" || reason == "ErrImagePull" || reason == "CreateContainerConfigError" {
							unhealthyPods++
							issues = append(issues, issueRecord{
								Pod:       podName,
								Namespace: podNs,
								Issue:     reason,
								Restarts:  restarts,
							})
						}
					}
				}
			}
		}

		if phase != "Running" && phase != "Succeeded" && phase != "Pending" {
			unhealthyPods++
			issues = append(issues, issueRecord{
				Pod:       podName,
				Namespace: podNs,
				Issue:     "phase=" + phase,
				Restarts:  restarts,
			})
		} else if restarts > 5 {
			issues = append(issues, issueRecord{
				Pod:       podName,
				Namespace: podNs,
				Issue:     fmt.Sprintf("high_restart_count=%d", restarts),
				Restarts:  restarts,
			})
		}
	}

	// Compute health score (100 = all healthy)
	score := 100
	if totalPods > 0 {
		unhealthyRatio := float64(unhealthyPods) / float64(totalPods)
		score = 100 - int(unhealthyRatio*100)
		if score < 0 {
			score = 0
		}
	}

	severity := "healthy"
	switch {
	case score < 50:
		severity = "critical"
	case score < 75:
		severity = "degraded"
	case score < 90:
		severity = "warning"
	}

	return map[string]any{
		"health_score":   score,
		"severity":       severity,
		"total_pods":     totalPods,
		"unhealthy_pods": unhealthyPods,
		"issues":         issues,
	}, nil
}

// ---- DeploymentStatusTool ---------------------------------------------------

// DeploymentStatusTool returns a structured rollout status for a Kubernetes Deployment.
type DeploymentStatusTool struct{}

func (t *DeploymentStatusTool) Name() string { return "deployment_status" }
func (t *DeploymentStatusTool) Description() string {
	return "Get rollout status, replica counts, image, and conditions for a Kubernetes Deployment"
}
func (t *DeploymentStatusTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "deployment_status",
		Description: "Fetch detailed rollout status for a Kubernetes Deployment: replica counts (desired/ready/updated/available/unavailable), current image, rollout revision, strategy, conditions, and whether the rollout is complete.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"deployment": map[string]any{"type": "string", "description": "Deployment name"},
				"namespace":  map[string]any{"type": "string", "description": "Namespace (default: default)"},
			},
			"required": []string{"deployment"},
		},
	}
}
func (t *DeploymentStatusTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	deployment, _ := args["deployment"].(string)
	if deployment == "" {
		return nil, fmt.Errorf("deployment is required")
	}
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}

	result, stderr, exitCode, err := kubectlJSON(ctx, "get", "deployment", deployment, "-n", ns)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]any{"error": stderr, "exit_code": exitCode}, nil
	}

	// Extract fields from the JSON response
	meta, _ := result["metadata"].(map[string]any)
	spec, _ := result["spec"].(map[string]any)
	status, _ := result["status"].(map[string]any)

	annotations, _ := meta["annotations"].(map[string]any)
	revision, _ := annotations["deployment.kubernetes.io/revision"].(string)

	desiredReplicas := intFromJSON(spec["replicas"])
	readyReplicas := intFromJSON(status["readyReplicas"])
	updatedReplicas := intFromJSON(status["updatedReplicas"])
	availableReplicas := intFromJSON(status["availableReplicas"])
	unavailableReplicas := intFromJSON(status["unavailableReplicas"])

	strategy := ""
	if strategyMap, ok := spec["strategy"].(map[string]any); ok {
		strategy, _ = strategyMap["type"].(string)
	}

	image := ""
	if tmpl, ok := spec["template"].(map[string]any); ok {
		if tSpec, ok := tmpl["spec"].(map[string]any); ok {
			if containers, ok := tSpec["containers"].([]any); ok && len(containers) > 0 {
				if c, ok := containers[0].(map[string]any); ok {
					image, _ = c["image"].(string)
				}
			}
		}
	}

	rolloutComplete := desiredReplicas > 0 &&
		readyReplicas == desiredReplicas &&
		updatedReplicas == desiredReplicas &&
		unavailableReplicas == 0

	conditions := []map[string]any{}
	if conds, ok := status["conditions"].([]any); ok {
		for _, c := range conds {
			if cm, ok := c.(map[string]any); ok {
				conditions = append(conditions, map[string]any{
					"type":    cm["type"],
					"status":  cm["status"],
					"reason":  cm["reason"],
					"message": cm["message"],
				})
			}
		}
	}

	return map[string]any{
		"deployment":           deployment,
		"namespace":            ns,
		"revision":             revision,
		"strategy":             strategy,
		"image":                image,
		"desired_replicas":     desiredReplicas,
		"ready_replicas":       readyReplicas,
		"updated_replicas":     updatedReplicas,
		"available_replicas":   availableReplicas,
		"unavailable_replicas": unavailableReplicas,
		"rollout_complete":     rolloutComplete,
		"conditions":           conditions,
	}, nil
}

// intFromJSON safely converts a JSON float64 (or nil) to int.
func intFromJSON(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// ---- K8sTopTool -------------------------------------------------------------

// K8sTopTool returns resource usage (CPU / memory) for pods or nodes.
type K8sTopTool struct{}

func (t *K8sTopTool) Name() string { return "k8s_top" }
func (t *K8sTopTool) Description() string {
	return "Show CPU and memory usage for Kubernetes pods or nodes (requires metrics-server)"
}
func (t *K8sTopTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "k8s_top",
		Description: "Run `kubectl top` to get live CPU and memory usage for pods or nodes. Requires metrics-server to be installed in the cluster. Returns a list of resources with their CPU and memory consumption.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource":  map[string]any{"type": "string", "description": "Resource type: 'pods' or 'nodes'"},
				"namespace": map[string]any{"type": "string", "description": "Namespace for pods (default: default); ignored for nodes"},
				"selector":  map[string]any{"type": "string", "description": "Label selector to filter pods (optional)"},
			},
			"required": []string{"resource"},
		},
	}
}
func (t *K8sTopTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	resource, _ := args["resource"].(string)
	if resource != "pods" && resource != "nodes" {
		return nil, fmt.Errorf("k8s_top: resource must be 'pods' or 'nodes'")
	}
	ns := "default"
	if v, ok := args["namespace"].(string); ok && v != "" {
		ns = v
	}

	cmdArgs := []string{"top", resource}
	if resource == "pods" {
		cmdArgs = append(cmdArgs, "-n", ns)
		if sel, ok := args["selector"].(string); ok && sel != "" {
			cmdArgs = append(cmdArgs, "--selector="+sel)
		}
	}

	stdout, stderr, exitCode, err := kubectlRun(ctx, cmdArgs...)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		// Friendly message for missing metrics-server
		if strings.Contains(stderr, "metrics") || strings.Contains(stderr, "Metrics") {
			return map[string]any{
				"error":    "metrics-server not available in this cluster",
				"raw":      stderr,
				"resource": resource,
			}, nil
		}
		return map[string]any{"error": stderr, "exit_code": exitCode}, nil
	}

	items := parseTopOutput(stdout, resource)
	result := map[string]any{
		"resource": resource,
		"items":    items,
		"count":    len(items),
	}
	if resource == "pods" {
		result["namespace"] = ns
	}
	return result, nil
}

// parseTopOutput parses the tabular output of kubectl top (skip header line).
// Pod line format:  NAME  CPU(cores)  MEMORY(bytes)
// Node line format: NAME  CPU(cores)  CPU%  MEMORY(bytes)  MEMORY%
func parseTopOutput(output, resource string) []map[string]any {
	items := []map[string]any{}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i, line := range lines {
		if i == 0 { // skip header
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if resource == "pods" && len(fields) >= 3 {
			items = append(items, map[string]any{
				"name":   fields[0],
				"cpu":    fields[1],
				"memory": fields[2],
			})
		} else if resource == "nodes" && len(fields) >= 5 {
			items = append(items, map[string]any{
				"name":        fields[0],
				"cpu":         fields[1],
				"cpu_percent": fields[2],
				"memory":      fields[3],
				"mem_percent": fields[4],
			})
		} else if len(fields) >= 3 {
			items = append(items, map[string]any{
				"name":   fields[0],
				"cpu":    fields[1],
				"memory": fields[2],
			})
		}
	}
	return items
}
