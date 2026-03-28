package tools

import "context"

// Context key types for workspace and container injection.
type contextKey int

const (
	// ContextKeyWorkspacePath overrides tool workspace paths.
	ContextKeyWorkspacePath contextKey = iota
	// ContextKeyContainerID signals a container exec context for shell_exec.
	ContextKeyContainerID
	// ContextKeyProjectID carries the current project ID.
	ContextKeyProjectID
	// ContextKeyAgentID carries the ID of the currently executing agent.
	ContextKeyAgentID
	// ContextKeyTaskID carries the ID of the current task.
	ContextKeyTaskID
)

// WorkspacePathFromContext returns the workspace path from context, if set.
func WorkspacePathFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContextKeyWorkspacePath).(string)
	return v, ok && v != ""
}

// ContainerIDFromContext returns the active container ID from context, if set.
func ContainerIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContextKeyContainerID).(string)
	return v, ok && v != ""
}

// ProjectIDFromContext returns the project ID from context, if set.
func ProjectIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContextKeyProjectID).(string)
	return v, ok && v != ""
}

// WithWorkspacePath returns a context with the workspace path set.
func WithWorkspacePath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, ContextKeyWorkspacePath, path)
}

// WithContainerID returns a context with the container ID set.
func WithContainerID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ContextKeyContainerID, id)
}

// WithProjectID returns a context with the project ID set.
func WithProjectID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ContextKeyProjectID, id)
}

// AgentIDFromContext returns the agent ID from context, if set.
func AgentIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContextKeyAgentID).(string)
	return v, ok && v != ""
}

// WithAgentID returns a context with the agent ID set.
func WithAgentID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ContextKeyAgentID, id)
}

// WithTaskID returns a context with the task ID set.
func WithTaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ContextKeyTaskID, id)
}

// TaskIDFromContext returns the task ID from context, if set.
func TaskIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContextKeyTaskID).(string)
	return v, ok && v != ""
}
