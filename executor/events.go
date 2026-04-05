package executor

// EventType identifies what happened in the executor loop.
type EventType string

const (
	EventToolCallStart  EventType = "tool_call_start"
	EventToolCallResult EventType = "tool_call_result"
	EventThinking       EventType = "thinking"
	EventText           EventType = "text"
	EventIteration      EventType = "iteration"
	EventCompleted      EventType = "completed"
	EventFailed         EventType = "failed"
)

// Event is emitted during execution for real-time observation.
type Event struct {
	Type       EventType      `json:"type"`
	AgentID    string         `json:"agent_id,omitempty"`
	Iteration  int            `json:"iteration,omitempty"`
	Content    string         `json:"content,omitempty"`     // text content or thinking trace
	ToolName   string         `json:"tool_name,omitempty"`   // for tool_call_start/result
	ToolCallID string         `json:"tool_call_id,omitempty"` // for tool_call_start/result
	ToolArgs   map[string]any `json:"tool_args,omitempty"`   // for tool_call_start
	ToolResult string         `json:"tool_result,omitempty"` // for tool_call_result
	ToolError  bool           `json:"tool_error,omitempty"`  // for tool_call_result
	Error      string         `json:"error,omitempty"`       // for failed events
}
