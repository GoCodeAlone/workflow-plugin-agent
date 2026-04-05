package genkit

import (
	"context"
	"fmt"
	"sync"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
)

// genkitProvider adapts a Genkit model to provider.Provider.
type genkitProvider struct {
	g         *gk.Genkit
	modelName string // "provider/model" format e.g. "anthropic/claude-sonnet-4-6"
	name      string
	authInfo  provider.AuthModeInfo
	maxTokens int // 0 means use model default

	mu           sync.Mutex
	definedTools map[string]bool // tracks which tool names are registered
}

func (p *genkitProvider) Name() string                      { return p.name }
func (p *genkitProvider) AuthModeInfo() provider.AuthModeInfo { return p.authInfo }

// resolveToolRefs ensures each tool is registered exactly once and returns
// their ToolRef representations for use with WithTools.
func (p *genkitProvider) resolveToolRefs(tools []provider.ToolDef) []ai.ToolRef {
	if len(tools) == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.definedTools == nil {
		p.definedTools = make(map[string]bool)
	}

	refs := make([]ai.ToolRef, 0, len(tools))
	for _, t := range tools {
		if !p.definedTools[t.Name] {
			// Pass the exact JSON Schema from provider.ToolDef.Parameters
			// so the LLM gets accurate parameter definitions for tool calling.
			// WithInputSchema requires In=any (not map[string]any).
			tool := gk.DefineTool(p.g, t.Name, t.Description,
				func(ctx *ai.ToolContext, input any) (any, error) {
					// Tools are executed by the executor, not Genkit.
					return nil, fmt.Errorf("tool %s should not be called via Genkit", t.Name)
				},
				ai.WithInputSchema(t.Parameters),
			)
			refs = append(refs, tool)
			p.definedTools[t.Name] = true
		} else {
			refs = append(refs, ai.ToolName(t.Name))
		}
	}
	return refs
}

// generationConfig returns a WithConfig option when maxTokens is configured.
func (p *genkitProvider) generationConfig() ai.GenerateOption {
	if p.maxTokens > 0 {
		return ai.WithConfig(&ai.GenerationCommonConfig{MaxOutputTokens: p.maxTokens})
	}
	return nil
}

// Chat sends a non-streaming request and returns the complete response.
func (p *genkitProvider) Chat(ctx context.Context, messages []provider.Message, tools []provider.ToolDef) (*provider.Response, error) {
	opts := []ai.GenerateOption{
		ai.WithModelName(p.modelName),
		ai.WithMessages(toGenkitMessages(messages)...),
	}

	if cfg := p.generationConfig(); cfg != nil {
		opts = append(opts, cfg)
	}

	if len(tools) > 0 {
		opts = append(opts, ai.WithReturnToolRequests(true))
		for _, ref := range p.resolveToolRefs(tools) {
			opts = append(opts, ai.WithTools(ref))
		}
	}

	resp, err := gk.Generate(ctx, p.g, opts...)
	if err != nil {
		return nil, fmt.Errorf("genkit generate: %w", err)
	}

	return fromGenkitResponse(resp), nil
}

// Stream sends a streaming request. Events are delivered on the returned channel.
func (p *genkitProvider) Stream(ctx context.Context, messages []provider.Message, tools []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 64)

	opts := []ai.GenerateOption{
		ai.WithModelName(p.modelName),
		ai.WithMessages(toGenkitMessages(messages)...),
	}

	if cfg := p.generationConfig(); cfg != nil {
		opts = append(opts, cfg)
	}

	if len(tools) > 0 {
		opts = append(opts, ai.WithReturnToolRequests(true))
		for _, ref := range p.resolveToolRefs(tools) {
			opts = append(opts, ai.WithTools(ref))
		}
	}

	go func() {
		defer close(ch)

		stream := gk.GenerateStream(ctx, p.g, opts...)
		for result, err := range stream {
			if err != nil {
				ch <- provider.StreamEvent{Type: "error", Error: err.Error()}
				return
			}
			if result.Done {
				// Tool calls are emitted only from the final response to avoid
				// duplicates with unstable IDs from incremental chunks.
				if result.Response != nil {
					final := fromGenkitResponse(result.Response)
					for i := range final.ToolCalls {
						tc := final.ToolCalls[i]
						ch <- provider.StreamEvent{Type: "tool_call", Tool: &tc}
					}
					if final.Usage.InputTokens > 0 || final.Usage.OutputTokens > 0 {
						ch <- provider.StreamEvent{Type: "done", Usage: &final.Usage}
						return
					}
				}
				ch <- provider.StreamEvent{Type: "done"}
				return
			}
			if result.Chunk != nil {
				ev := fromGenkitChunk(result.Chunk)
				if ev.Text != "" || ev.Thinking != "" || ev.Tool != nil {
					ch <- ev
				}
			}
		}
		// Iterator exhausted without Done — send done anyway
		ch <- provider.StreamEvent{Type: "done"}
	}()

	return ch, nil
}
