package tools

import "context"

type contextKey int

const (
	contextKeyAgentID   contextKey = iota
	contextKeyTaskID    contextKey = iota
	contextKeyTeamID    contextKey = iota
	contextKeyProjectID contextKey = iota
)

// WithAgentID returns a context with the agent ID set.
func WithAgentID(ctx context.Context, agentID string) context.Context {
	return context.WithValue(ctx, contextKeyAgentID, agentID)
}

// AgentIDFromContext returns the agent ID from context, if set.
func AgentIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyAgentID).(string)
	return v
}

// WithTaskID returns a context with the task ID set.
func WithTaskID(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, contextKeyTaskID, taskID)
}

// TaskIDFromContext returns the task ID from context, if set.
func TaskIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyTaskID).(string)
	return v
}

// WithTeamID returns a context with the team ID set.
func WithTeamID(ctx context.Context, teamID string) context.Context {
	return context.WithValue(ctx, contextKeyTeamID, teamID)
}

// TeamIDFromContext returns the team ID from context, if set.
func TeamIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyTeamID).(string)
	return v
}

// WithProjectID returns a context with the project ID set.
func WithProjectID(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, contextKeyProjectID, projectID)
}

// ProjectIDFromContext returns the project ID from context, if set.
func ProjectIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyProjectID).(string)
	return v
}
