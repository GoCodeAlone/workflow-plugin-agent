package genkit

import (
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/firebase/genkit/go/ai"
)

// toGenkitMessages converts our messages to Genkit messages.
func toGenkitMessages(msgs []provider.Message) []*ai.Message {
	out := make([]*ai.Message, 0, len(msgs))
	for _, m := range msgs {
		var role ai.Role
		switch m.Role {
		case provider.RoleSystem:
			role = ai.RoleSystem
		case provider.RoleUser:
			role = ai.RoleUser
		case provider.RoleAssistant:
			role = ai.RoleModel
		case provider.RoleTool:
			role = ai.RoleTool
		default:
			role = ai.RoleUser
		}

		var parts []*ai.Part

		// Tool call results: add as ToolResponsePart
		if m.ToolCallID != "" {
			parts = []*ai.Part{ai.NewToolResponsePart(&ai.ToolResponse{
				Name:   m.ToolCallID,
				Output: map[string]any{"result": m.Content},
			})}
		} else if len(m.ToolCalls) > 0 {
			// Assistant message with tool calls
			for _, tc := range m.ToolCalls {
				parts = append(parts, ai.NewToolRequestPart(&ai.ToolRequest{
					Name:  tc.Name,
					Input: tc.Arguments,
				}))
			}
		} else {
			parts = []*ai.Part{ai.NewTextPart(m.Content)}
		}

		out = append(out, ai.NewMessage(role, nil, parts...))
	}
	return out
}

// fromGenkitResponse converts a Genkit response to our Response type.
func fromGenkitResponse(resp *ai.ModelResponse) *provider.Response {
	if resp == nil {
		return &provider.Response{}
	}

	out := &provider.Response{
		Content: resp.Text(),
	}

	// Extract thinking/reasoning content
	if msg := resp.Message; msg != nil {
		for _, part := range msg.Content {
			if part.IsReasoning() {
				out.Thinking = part.Text
				break
			}
		}
	}

	// Extract tool calls
	if msg := resp.Message; msg != nil {
		for _, part := range msg.Content {
			if part.ToolRequest != nil {
				tc := provider.ToolCall{
					ID:        part.ToolRequest.Name,
					Name:      part.ToolRequest.Name,
					Arguments: make(map[string]any),
				}
				if input, ok := part.ToolRequest.Input.(map[string]any); ok {
					tc.Arguments = input
				}
				out.ToolCalls = append(out.ToolCalls, tc)
			}
		}
	}

	// Extract usage
	if resp.Usage != nil {
		out.Usage = provider.Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		}
	}

	return out
}

// fromGenkitChunk converts a Genkit stream chunk to our StreamEvent.
func fromGenkitChunk(chunk *ai.ModelResponseChunk) provider.StreamEvent {
	if chunk == nil {
		return provider.StreamEvent{Type: "done"}
	}

	// Check for thinking/reasoning parts first
	for _, part := range chunk.Content {
		if part.IsReasoning() {
			return provider.StreamEvent{Type: "thinking", Thinking: part.Text}
		}
	}

	// Check for text
	text := chunk.Text()
	if text != "" {
		return provider.StreamEvent{Type: "text", Text: text}
	}

	// Check for tool calls in chunk
	for _, part := range chunk.Content {
		if part.ToolRequest != nil {
			return provider.StreamEvent{
				Type: "tool_call",
				Tool: &provider.ToolCall{
					ID:        part.ToolRequest.Name,
					Name:      part.ToolRequest.Name,
					Arguments: func() map[string]any {
						if m, ok := part.ToolRequest.Input.(map[string]any); ok {
							return m
						}
						return nil
					}(),
				},
			}
		}
	}

	return provider.StreamEvent{Type: "text", Text: ""}
}
