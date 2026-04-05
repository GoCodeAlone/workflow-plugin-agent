package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

const defaultAnthropicMaxTokens = 4096

// toAnthropicParams converts provider types to SDK MessageNewParams.
func toAnthropicParams(model string, maxTokens int, messages []Message, tools []ToolDef) anthropic.MessageNewParams {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(maxTokens),
	}
	for _, msg := range messages {
		if msg.Role == RoleSystem {
			params.System = append(params.System, anthropic.TextBlockParam{Text: msg.Content})
			continue
		}
		if msg.Role == RoleTool {
			params.Messages = append(params.Messages,
				anthropic.NewUserMessage(anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false)),
			)
			continue
		}
		if msg.Role == RoleAssistant {
			var blocks []anthropic.ContentBlockParamUnion
			if msg.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
			}
			for _, tc := range msg.ToolCalls {
				inputJSON, _ := json.Marshal(tc.Arguments)
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfToolUse: &anthropic.ToolUseBlockParam{
						ID:    tc.ID,
						Name:  tc.Name,
						Input: inputJSON,
					},
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropic.NewTextBlock(""))
			}
			params.Messages = append(params.Messages,
				anthropic.NewAssistantMessage(blocks...),
			)
		} else {
			params.Messages = append(params.Messages,
				anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)),
			)
		}
	}
	for _, t := range tools {
		extras := make(map[string]any)
		for k, v := range t.Parameters {
			if k != "type" {
				extras[k] = v
			}
		}
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: anthropic.ToolInputSchemaParam{ExtraFields: extras},
			},
		})
	}
	return params
}

// fromAnthropicMessage converts an SDK Message to a provider Response.
func fromAnthropicMessage(msg *anthropic.Message) (*Response, error) {
	resp := &Response{
		Usage: Usage{
			InputTokens:  int(msg.Usage.InputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
		},
	}
	var textParts []string
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			var args map[string]any
			if len(block.Input) > 0 {
				if err := json.Unmarshal(block.Input, &args); err != nil {
					return nil, fmt.Errorf("anthropic: unmarshal tool call arguments for %q: %w", block.Name, err)
				}
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		}
	}
	resp.Content = strings.Join(textParts, "")
	return resp, nil
}

// streamAnthropicEvents reads SDK stream events and sends them to ch.
func streamAnthropicEvents(stream *ssestream.Stream[anthropic.MessageStreamEventUnion], ch chan<- StreamEvent) {
	defer close(ch)
	defer stream.Close()

	var usage *Usage
	var currentToolID, currentToolName string
	var toolInputBuf bytes.Buffer

	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "message_start":
			e := event.AsMessageStart()
			usage = &Usage{
				InputTokens:  int(e.Message.Usage.InputTokens),
				OutputTokens: int(e.Message.Usage.OutputTokens),
			}
		case "content_block_start":
			e := event.AsContentBlockStart()
			if e.ContentBlock.Type == "tool_use" {
				currentToolID = e.ContentBlock.ID
				currentToolName = e.ContentBlock.Name
				toolInputBuf.Reset()
			}
		case "content_block_delta":
			e := event.AsContentBlockDelta()
			switch e.Delta.Type {
			case "text_delta":
				ch <- StreamEvent{Type: "text", Text: e.Delta.Text}
			case "input_json_delta":
				toolInputBuf.WriteString(e.Delta.PartialJSON)
			}
		case "content_block_stop":
			if currentToolID != "" {
				var args map[string]any
				if toolInputBuf.Len() > 0 {
					if err := json.Unmarshal(toolInputBuf.Bytes(), &args); err != nil {
						ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("anthropic: unmarshal tool call arguments for %q: %v", currentToolName, err)}
						return
					}
				}
				ch <- StreamEvent{
					Type: "tool_call",
					Tool: &ToolCall{
						ID:        currentToolID,
						Name:      currentToolName,
						Arguments: args,
					},
				}
				currentToolID = ""
				currentToolName = ""
				toolInputBuf.Reset()
			}
		case "message_delta":
			e := event.AsMessageDelta()
			if usage != nil {
				usage.OutputTokens = int(e.Usage.OutputTokens)
			}
		case "message_stop":
			ch <- StreamEvent{Type: "done", Usage: usage}
			return
		}
	}

	if err := stream.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Error: err.Error()}
	}
}
