package provider

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// toOpenAIMessages converts provider messages to SDK message params.
func toOpenAIMessages(msgs []Message) []openaisdk.ChatCompletionMessageParamUnion {
	result := make([]openaisdk.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case RoleTool:
			result = append(result, openaisdk.ToolMessage(msg.Content, msg.ToolCallID))
		case RoleAssistant:
			asst := openaisdk.ChatCompletionAssistantMessageParam{}
			if msg.Content != "" {
				asst.Content = openaisdk.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openaisdk.String(msg.Content),
				}
			}
			for _, tc := range msg.ToolCalls {
				args := "{}"
				if tc.Arguments != nil {
					if b, err := json.Marshal(tc.Arguments); err == nil {
						args = string(b)
					}
				}
				asst.ToolCalls = append(asst.ToolCalls, openaisdk.ChatCompletionMessageToolCallParam{
					ID: tc.ID,
					Function: openaisdk.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: args,
					},
				})
			}
			result = append(result, openaisdk.ChatCompletionMessageParamUnion{OfAssistant: &asst})
		case RoleSystem:
			result = append(result, openaisdk.SystemMessage(msg.Content))
		default: // RoleUser and others
			result = append(result, openaisdk.UserMessage(msg.Content))
		}
	}
	return result
}

// toOpenAITools converts provider tool definitions to SDK tool params.
func toOpenAITools(tools []ToolDef) []openaisdk.ChatCompletionToolParam {
	result := make([]openaisdk.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		schema := shared.FunctionParameters(t.Parameters)
		if schema == nil {
			schema = shared.FunctionParameters{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, openaisdk.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openaisdk.String(t.Description),
				Parameters:  schema,
			},
		})
	}
	return result
}

// fromOpenAIResponse converts an SDK ChatCompletion to the provider Response type.
func fromOpenAIResponse(resp *openaisdk.ChatCompletion) (*Response, error) {
	result := &Response{
		Usage: Usage{
			InputTokens:  int(resp.Usage.PromptTokens),
			OutputTokens: int(resp.Usage.CompletionTokens),
		},
	}
	if len(resp.Choices) == 0 {
		return result, nil
	}
	msg := resp.Choices[0].Message
	result.Content = msg.Content
	for _, tc := range msg.ToolCalls {
		var args map[string]any
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return nil, fmt.Errorf("openai: unmarshal tool call arguments for %q: %w", tc.Function.Name, err)
			}
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}
	return result, nil
}

// openaiChunkStream is satisfied by *ssestream.Stream[openaisdk.ChatCompletionChunk].
type openaiChunkStream interface {
	Next() bool
	Current() openaisdk.ChatCompletionChunk
	Err() error
	Close() error
}

// streamOpenAIEvents drains stream and sends StreamEvents to ch, then closes ch.
func streamOpenAIEvents(stream openaiChunkStream, ch chan<- StreamEvent) {
	defer close(ch)
	defer stream.Close()

	type pendingToolCall struct {
		id      string
		name    string
		argsBuf strings.Builder
	}
	pending := make(map[int64]*pendingToolCall)
	var usage *Usage

	for stream.Next() {
		chunk := stream.Current()

		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			usage = &Usage{
				InputTokens:  int(chunk.Usage.PromptTokens),
				OutputTokens: int(chunk.Usage.CompletionTokens),
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			ch <- StreamEvent{Type: "text", Text: delta.Content}
		}

		for _, tc := range delta.ToolCalls {
			ptc, exists := pending[tc.Index]
			if !exists {
				ptc = &pendingToolCall{}
				pending[tc.Index] = ptc
			}
			if tc.ID != "" {
				ptc.id = tc.ID
			}
			if tc.Function.Name != "" {
				ptc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				ptc.argsBuf.WriteString(tc.Function.Arguments)
			}
		}
	}

	if err := stream.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: err.Error()}
		return
	}

	indices := make([]int64, 0, len(pending))
	for idx := range pending {
		indices = append(indices, idx)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })

	for _, idx := range indices {
		ptc := pending[idx]
		var args map[string]any
		if ptc.argsBuf.Len() > 0 {
			if err := json.Unmarshal([]byte(ptc.argsBuf.String()), &args); err != nil {
				ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("openai: unmarshal tool call arguments for %q: %v", ptc.name, err)}
				return
			}
		}
		ch <- StreamEvent{
			Type: "tool_call",
			Tool: &ToolCall{ID: ptc.id, Name: ptc.name, Arguments: args},
		}
	}

	ch <- StreamEvent{Type: "done", Usage: usage}
}
