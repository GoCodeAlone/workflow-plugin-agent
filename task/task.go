// Package task defines the task model for agent work items.
// Persistence is handled by the workflow engine's pipeline DB steps.
package task

import "time"

// Status represents the lifecycle state of a task.
type Status string

const (
	StatusPending    Status = "pending"
	StatusAssigned   Status = "assigned"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusCanceled   Status = "canceled"
)

// Priority determines task scheduling order.
type Priority int

const (
	PriorityLow      Priority = 0
	PriorityNormal   Priority = 1
	PriorityHigh     Priority = 2
	PriorityCritical Priority = 3
)

// Task is a unit of work for an agent.
type Task struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Status      Status            `json:"status"`
	Priority    Priority          `json:"priority"`
	AssignedTo  string            `json:"assigned_to,omitempty"`
	TeamID      string            `json:"team_id,omitempty"`
	ParentID    string            `json:"parent_id,omitempty"`
	DependsOn   []string          `json:"depends_on,omitempty"`
	Labels      []string          `json:"labels,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Result      string            `json:"result,omitempty"`
	Error       string            `json:"error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
}
