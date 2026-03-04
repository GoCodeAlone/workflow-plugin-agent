// Package provider defines the AI provider interface for agent backends.
package provider

import "context"

// Role identifies the sender of a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a single turn in a conversation.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // for tool results
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // for assistant messages with tool calls
}

// ToolDef describes a tool the agent can invoke.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
}

// ToolCall is a request from the AI to invoke a tool.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Response is a completed (non-streaming) provider response.
type Response struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     Usage      `json:"usage"`
}

// Usage tracks token consumption.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// StreamEvent is emitted during streaming responses.
type StreamEvent struct {
	Type  string    `json:"type"` // "text", "tool_call", "done", "error"
	Text  string    `json:"text,omitempty"`
	Tool  *ToolCall `json:"tool,omitempty"`
	Error string    `json:"error,omitempty"`
	Usage *Usage    `json:"usage,omitempty"`
}

// Provider is an AI backend that powers agent reasoning.
type Provider interface {
	// Name returns the provider identifier (e.g., "anthropic", "openai", "mock").
	Name() string

	// Chat sends a non-streaming request and returns the complete response.
	Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error)

	// Stream sends a streaming request. Events are delivered on the returned channel.
	// The channel is closed when the response is complete or an error occurs.
	Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error)
}

// Embedder is optionally implemented by providers that support text embedding.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// AsEmbedder checks if a Provider also implements Embedder.
func AsEmbedder(p Provider) (Embedder, bool) {
	e, ok := p.(Embedder)
	return e, ok
}
