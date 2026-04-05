package genkit

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
)

// mockModel defines a canned in-memory model on a Genkit instance for testing.
func mockModel(g *gk.Genkit, name string, resp *ai.ModelResponse) {
	opts := &ai.ModelOptions{
		Supports: &ai.ModelSupports{Tools: true, SystemRole: true, Multiturn: true},
	}
	gk.DefineModel(g, name, opts, func(ctx context.Context, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		if cb != nil {
			// Stream text chunks
			if resp.Message != nil {
				for _, part := range resp.Message.Content {
					if !part.IsReasoning() {
						_ = cb(ctx, &ai.ModelResponseChunk{
							Content: []*ai.Part{part},
						})
					}
				}
			}
		}
		return resp, nil
	})
}

func newTestProvider(t *testing.T, modelName string, resp *ai.ModelResponse) *genkitProvider {
	t.Helper()
	g := gk.Init(context.Background())
	mockModel(g, modelName, resp)
	return &genkitProvider{
		g:         g,
		modelName: modelName,
		name:      "mock",
	}
}

func TestGenkitProviderChat(t *testing.T) {
	const modelName = "mock/test-model"
	resp := &ai.ModelResponse{
		Message: ai.NewModelTextMessage("Hello from mock"),
		Usage:   &ai.GenerationUsage{InputTokens: 5, OutputTokens: 3},
	}

	p := newTestProvider(t, modelName, resp)

	msgs := []provider.Message{{Role: provider.RoleUser, Content: "Hi"}}
	got, err := p.Chat(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if got.Content != "Hello from mock" {
		t.Errorf("expected 'Hello from mock', got %q", got.Content)
	}
	if got.Usage.InputTokens != 5 {
		t.Errorf("expected 5 input tokens, got %d", got.Usage.InputTokens)
	}
}

func TestGenkitProviderName(t *testing.T) {
	p := &genkitProvider{name: "test-provider"}
	if p.Name() != "test-provider" {
		t.Errorf("expected 'test-provider', got %q", p.Name())
	}
}

func TestGenkitProviderStream(t *testing.T) {
	const modelName = "mock/stream-model"
	resp := &ai.ModelResponse{
		Message: ai.NewModelTextMessage("streamed"),
		Usage:   &ai.GenerationUsage{InputTokens: 2, OutputTokens: 1},
	}

	p := newTestProvider(t, modelName, resp)

	msgs := []provider.Message{{Role: provider.RoleUser, Content: "Hi"}}
	ch, err := p.Stream(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}

	var events []provider.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	last := events[len(events)-1]
	if last.Type != "done" {
		t.Errorf("expected last event type 'done', got %q", last.Type)
	}
}

func TestGenkitProviderChatWithTools(t *testing.T) {
	const modelName = "mock/tool-model"

	// Mock model that returns a tool request
	resp := &ai.ModelResponse{
		Message: ai.NewMessage(ai.RoleModel, nil,
			ai.NewToolRequestPart(&ai.ToolRequest{
				Name:  "calculator",
				Input: map[string]any{"a": 1, "b": 2},
			}),
		),
	}

	p := newTestProvider(t, modelName, resp)

	msgs := []provider.Message{{Role: provider.RoleUser, Content: "Add 1+2"}}
	tools := []provider.ToolDef{{
		Name:        "calculator",
		Description: "Adds numbers",
		Parameters:  map[string]any{"type": "object"},
	}}

	got, err := p.Chat(context.Background(), msgs, tools)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	if len(got.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(got.ToolCalls))
	}
	if got.ToolCalls[0].Name != "calculator" {
		t.Errorf("expected tool 'calculator', got %q", got.ToolCalls[0].Name)
	}
}

func TestGenkitProviderChatToolDeduplication(t *testing.T) {
	const modelName = "mock/dedup-model"

	resp := &ai.ModelResponse{
		Message: ai.NewModelTextMessage("ok"),
	}

	p := newTestProvider(t, modelName, resp)

	tools := []provider.ToolDef{{
		Name:        "my-tool",
		Description: "A tool",
		Parameters:  map[string]any{"type": "object"},
	}}

	msgs := []provider.Message{{Role: provider.RoleUser, Content: "call 1"}}

	// First call - defines the tool
	_, err := p.Chat(context.Background(), msgs, tools)
	if err != nil {
		t.Fatalf("first Chat error: %v", err)
	}

	// Second call - must not panic due to duplicate tool registration
	msgs2 := []provider.Message{{Role: provider.RoleUser, Content: "call 2"}}
	_, err = p.Chat(context.Background(), msgs2, tools)
	if err != nil {
		t.Fatalf("second Chat error: %v", err)
	}
}
