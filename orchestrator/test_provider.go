package orchestrator

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/google/uuid"
)

// ResponseSource is the interface that pluggable backends implement
// to supply responses for the TestProvider.
type ResponseSource interface {
	// GetResponse receives the current interaction (messages + tools) and returns
	// a response. Implementations may block (e.g. waiting for human input).
	GetResponse(ctx context.Context, interaction Interaction) (*InteractionResponse, error)
}

// Interaction represents a single LLM call that needs a response.
type Interaction struct {
	ID        string             `json:"id"`
	Messages  []provider.Message `json:"messages"`
	Tools     []provider.ToolDef `json:"tools"`
	CreatedAt time.Time          `json:"created_at"`
}

// InteractionResponse is the response supplied by a ResponseSource.
type InteractionResponse struct {
	Content   string              `json:"content"`
	ToolCalls []provider.ToolCall `json:"tool_calls,omitempty"`
	Error     string              `json:"error,omitempty"`
	Usage     provider.Usage      `json:"usage,omitempty"`
}

// TestProvider implements provider.Provider by delegating to a ResponseSource.
// It enables interactive and scripted E2E testing of the agent execution pipeline.
type TestProvider struct {
	source       ResponseSource
	name         string
	timeout      time.Duration
	interactions atomic.Int64
}

// TestProviderOption configures a TestProvider.
type TestProviderOption func(*TestProvider)

// WithTimeout sets the maximum time to wait for a response from the source.
func WithTimeout(d time.Duration) TestProviderOption {
	return func(tp *TestProvider) {
		tp.timeout = d
	}
}

// WithName sets the provider name returned by Name().
func WithName(s string) TestProviderOption {
	return func(tp *TestProvider) {
		tp.name = s
	}
}

// NewTestProvider creates a TestProvider backed by the given ResponseSource.
func NewTestProvider(source ResponseSource, opts ...TestProviderOption) *TestProvider {
	tp := &TestProvider{
		source:  source,
		name:    "test",
		timeout: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(tp)
	}
	return tp
}

// Name implements provider.Provider.
func (tp *TestProvider) Name() string { return tp.name }

// Chat implements provider.Provider.
func (tp *TestProvider) Chat(ctx context.Context, messages []provider.Message, tools []provider.ToolDef) (*provider.Response, error) {
	interaction := Interaction{
		ID:        uuid.New().String(),
		Messages:  messages,
		Tools:     tools,
		CreatedAt: time.Now(),
	}
	tp.interactions.Add(1)

	// Apply timeout
	callCtx, cancel := context.WithTimeout(ctx, tp.timeout)
	defer cancel()

	resp, err := tp.source.GetResponse(callCtx, interaction)
	if err != nil {
		return nil, fmt.Errorf("test provider: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("test provider injected error: %s", resp.Error)
	}

	// Auto-generate tool call IDs if missing
	for i := range resp.ToolCalls {
		if resp.ToolCalls[i].ID == "" {
			resp.ToolCalls[i].ID = uuid.New().String()
		}
	}

	usage := resp.Usage
	if usage.InputTokens == 0 {
		usage.InputTokens = 10
	}
	if usage.OutputTokens == 0 {
		usage.OutputTokens = len(resp.Content) / 4
		if usage.OutputTokens == 0 {
			usage.OutputTokens = 1
		}
	}

	return &provider.Response{
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
		Usage:     usage,
	}, nil
}

// Stream implements provider.Provider by wrapping Chat() into stream events.
func (tp *TestProvider) Stream(ctx context.Context, messages []provider.Message, tools []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	resp, err := tp.Chat(ctx, messages, tools)
	if err != nil {
		return nil, err
	}

	ch := make(chan provider.StreamEvent, 2+len(resp.ToolCalls))
	if resp.Content != "" {
		ch <- provider.StreamEvent{Type: "text", Text: resp.Content}
	}
	for i := range resp.ToolCalls {
		ch <- provider.StreamEvent{Type: "tool_call", Tool: &resp.ToolCalls[i]}
	}
	ch <- provider.StreamEvent{Type: "done", Usage: &resp.Usage}
	close(ch)
	return ch, nil
}

// AuthModeInfo implements provider.Provider.
func (tp *TestProvider) AuthModeInfo() provider.AuthModeInfo {
	return provider.AuthModeInfo{Mode: "none", DisplayName: "Test provider"}
}

// InteractionCount returns how many interactions have been processed.
func (tp *TestProvider) InteractionCount() int64 {
	return tp.interactions.Load()
}

// Source returns the underlying ResponseSource.
func (tp *TestProvider) Source() ResponseSource {
	return tp.source
}
