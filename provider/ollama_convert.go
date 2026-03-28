package provider

import (
	"encoding/json"

	ollamaapi "github.com/ollama/ollama/api"
)

// toOllamaMessages converts provider messages to Ollama API messages.
func toOllamaMessages(msgs []Message) []ollamaapi.Message {
	result := make([]ollamaapi.Message, 0, len(msgs))
	for _, msg := range msgs {
		m := ollamaapi.Message{
			Role:       string(msg.Role),
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}
		for _, tc := range msg.ToolCalls {
			args := ollamaapi.NewToolCallFunctionArguments()
			for k, v := range tc.Arguments {
				args.Set(k, v)
			}
			m.ToolCalls = append(m.ToolCalls, ollamaapi.ToolCall{
				ID: tc.ID,
				Function: ollamaapi.ToolCallFunction{
					Name:      tc.Name,
					Arguments: args,
				},
			})
		}
		result = append(result, m)
	}
	return result
}

// toOllamaTools converts provider tool definitions to Ollama API tools.
// The JSON Schema parameters are marshaled and unmarshaled into the Ollama type.
func toOllamaTools(tools []ToolDef) []ollamaapi.Tool {
	result := make([]ollamaapi.Tool, 0, len(tools))
	for _, t := range tools {
		var params ollamaapi.ToolFunctionParameters
		if b, err := json.Marshal(t.Parameters); err == nil {
			_ = json.Unmarshal(b, &params)
		}
		result = append(result, ollamaapi.Tool{
			Type: "function",
			Function: ollamaapi.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return result
}

// fromOllamaResponse converts a completed Ollama ChatResponse to a provider Response.
// ParseThinking is applied to extract any <think>...</think> block from the content.
func fromOllamaResponse(resp ollamaapi.ChatResponse) *Response {
	thinking, content := ParseThinking(resp.Message.Content)
	// Prefer native thinking field if set (Ollama Think mode)
	if resp.Message.Thinking != "" {
		thinking = resp.Message.Thinking
		content = resp.Message.Content
	}
	r := &Response{
		Content:  content,
		Thinking: thinking,
		Usage: Usage{
			InputTokens:  resp.PromptEvalCount,
			OutputTokens: resp.EvalCount,
		},
	}
	for _, tc := range resp.Message.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments.ToMap(),
		})
	}
	return r
}

// fromOllamaStreamChunk extracts text content, tool calls, and done flag from a
// streaming Ollama ChatResponse chunk.
func fromOllamaStreamChunk(resp ollamaapi.ChatResponse) (text string, toolCalls []ToolCall, done bool) {
	text = resp.Message.Content
	for _, tc := range resp.Message.ToolCalls {
		toolCalls = append(toolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments.ToMap(),
		})
	}
	return text, toolCalls, resp.Done
}
