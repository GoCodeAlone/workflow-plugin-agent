// Package plugin defines the Ratchet tool plugin interface.
// Tool plugins provide capabilities that agents can invoke during task execution.
// The plugin registry and lifecycle are managed by the workflow engine.
package plugin

import (
	"context"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// Tool extends Ratchet agents with additional capabilities.
type Tool interface {
	// Name returns the unique tool identifier.
	Name() string

	// Description returns a human-readable description.
	Description() string

	// Definition returns the tool definition for the AI provider.
	Definition() provider.ToolDef

	// Execute runs the tool with the given arguments.
	Execute(ctx context.Context, args map[string]any) (any, error)
}
