// Package agent defines agent types and configuration.
// The agent runtime loop is handled by the workflow engine's step.agent_execute pipeline step.
package agent

import "time"

// Status represents the current state of an agent.
type Status string

const (
	StatusIdle    Status = "idle"
	StatusActive  Status = "active"
	StatusWorking Status = "working"
	StatusStopped Status = "stopped"
	StatusError   Status = "error"
)

// Personality defines the agent's behavior, tone, and role.
type Personality struct {
	Name         string `json:"name" yaml:"name"`
	Role         string `json:"role" yaml:"role"`
	SystemPrompt string `json:"system_prompt" yaml:"system_prompt"`
	Model        string `json:"model,omitempty" yaml:"model"`
}

// Info provides read-only metadata about an agent.
type Info struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Personality *Personality `json:"personality,omitempty"`
	Status      Status       `json:"status"`
	CurrentTask string       `json:"current_task,omitempty"`
	StartedAt   time.Time    `json:"started_at"`
	TeamID      string       `json:"team_id,omitempty"`
	IsLead      bool         `json:"is_lead"`
}
