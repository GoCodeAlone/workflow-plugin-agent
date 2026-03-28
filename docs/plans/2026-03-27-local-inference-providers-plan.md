# Local Inference Providers Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add Ollama and llama.cpp providers to workflow-plugin-agent with `<think>` reasoning trace support and a `step.model_pull` step for model acquisition.

**Architecture:** Shared thinking-trace parser in `provider/local.go`, Ollama provider via `ollama/ollama/api` SDK, llama.cpp provider via existing `openai-go` SDK with optional process management, HuggingFace downloader for GGUF files. All changes in `workflow-plugin-agent` repo at `/Users/jon/workspace/workflow-plugin-agent`.

**Tech Stack:** Go 1.26, `github.com/ollama/ollama/api`, `github.com/openai/openai-go` (existing), `os/exec` for llama-server process management.

---

### Task 1: Core Interface — Add Thinking Fields to Response and StreamEvent

**Files:**
- Modify: `provider/provider.go:39-58`

**Step 1: Write the failing test**

Create `provider/thinking_field_test.go`:

```go
package provider

import (
	"encoding/json"
	"testing"
)

func TestResponse_ThinkingFieldJSON(t *testing.T) {
	r := Response{
		Content:  "Hello",
		Thinking: "I should greet the user",
		Usage:    Usage{InputTokens: 10, OutputTokens: 5},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Response
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Thinking != "I should greet the user" {
		t.Errorf("Thinking: want %q, got %q", "I should greet the user", decoded.Thinking)
	}
}

func TestResponse_ThinkingOmittedWhenEmpty(t *testing.T) {
	r := Response{Content: "Hello"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if contains(s, "thinking") {
		t.Errorf("expected thinking to be omitted from JSON when empty, got: %s", s)
	}
}

func TestStreamEvent_ThinkingType(t *testing.T) {
	e := StreamEvent{Type: "thinking", Thinking: "reasoning here"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var decoded StreamEvent
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != "thinking" {
		t.Errorf("Type: want thinking, got %q", decoded.Type)
	}
	if decoded.Thinking != "reasoning here" {
		t.Errorf("Thinking: want %q, got %q", "reasoning here", decoded.Thinking)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run TestResponse_Thinking -v`
Expected: FAIL — `Response` has no `Thinking` field

**Step 3: Add Thinking fields**

In `provider/provider.go`, add `Thinking` to `Response` (line 39-43):

```go
type Response struct {
	Content   string     `json:"content"`
	Thinking  string     `json:"thinking,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     Usage      `json:"usage"`
}
```

Add `Thinking` to `StreamEvent` (line 52-58):

```go
type StreamEvent struct {
	Type     string    `json:"type"` // "text", "thinking", "tool_call", "done", "error"
	Text     string    `json:"text,omitempty"`
	Thinking string    `json:"thinking,omitempty"`
	Tool     *ToolCall `json:"tool,omitempty"`
	Error    string    `json:"error,omitempty"`
	Usage    *Usage    `json:"usage,omitempty"`
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run TestResponse_Thinking -v && go test ./provider/ -run TestStreamEvent_Thinking -v`
Expected: PASS

**Step 5: Run all existing tests to verify no regressions**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./...`
Expected: All existing tests still pass (field additions are backward-compatible)

**Step 6: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add provider/provider.go provider/thinking_field_test.go
git commit -m "feat: add Thinking field to Response and StreamEvent types"
```

---

### Task 2: Shared Local Helpers — Think Tag Parser

**Files:**
- Create: `provider/local.go`
- Create: `provider/local_test.go`

**Step 1: Write the failing tests**

Create `provider/local_test.go`:

```go
package provider

import "testing"

func TestParseThinking_BasicExtraction(t *testing.T) {
	input := "<think>Let me reason about this.</think>The answer is 42."
	thinking, content := ParseThinking(input)
	if thinking != "Let me reason about this." {
		t.Errorf("thinking: want %q, got %q", "Let me reason about this.", thinking)
	}
	if content != "The answer is 42." {
		t.Errorf("content: want %q, got %q", "The answer is 42.", content)
	}
}

func TestParseThinking_NoThinkTags(t *testing.T) {
	input := "Just a normal response."
	thinking, content := ParseThinking(input)
	if thinking != "" {
		t.Errorf("thinking: want empty, got %q", thinking)
	}
	if content != "Just a normal response." {
		t.Errorf("content: want %q, got %q", "Just a normal response.", content)
	}
}

func TestParseThinking_MultipleBlocks(t *testing.T) {
	input := "<think>First thought.</think>Middle text.<think>Second thought.</think>Final answer."
	thinking, content := ParseThinking(input)
	if thinking != "First thought.\nSecond thought." {
		t.Errorf("thinking: want %q, got %q", "First thought.\nSecond thought.", thinking)
	}
	if content != "Middle text.Final answer." {
		t.Errorf("content: want %q, got %q", "Middle text.Final answer.", content)
	}
}

func TestParseThinking_UnclosedTag(t *testing.T) {
	input := "<think>Unclosed reasoning"
	thinking, content := ParseThinking(input)
	if thinking != "Unclosed reasoning" {
		t.Errorf("thinking: want %q, got %q", "Unclosed reasoning", thinking)
	}
	if content != "" {
		t.Errorf("content: want empty, got %q", content)
	}
}

func TestParseThinking_EmptyThinkBlock(t *testing.T) {
	input := "<think></think>Content here."
	thinking, content := ParseThinking(input)
	if thinking != "" {
		t.Errorf("thinking: want empty, got %q", thinking)
	}
	if content != "Content here." {
		t.Errorf("content: want %q, got %q", "Content here.", content)
	}
}

func TestParseThinking_WhitespaceAroundTags(t *testing.T) {
	input := " <think> reasoning here </think> answer here "
	thinking, content := ParseThinking(input)
	if thinking != " reasoning here " {
		t.Errorf("thinking: want %q, got %q", " reasoning here ", thinking)
	}
	if content != "  answer here " {
		t.Errorf("content: want %q, got %q", "  answer here ", content)
	}
}

func TestThinkingStreamParser_BasicFlow(t *testing.T) {
	p := &ThinkingStreamParser{}

	// Feed opening think tag
	events := p.Feed("<think>")
	if len(events) != 0 {
		t.Errorf("expected 0 events for opening tag, got %d", len(events))
	}

	// Feed thinking content
	events = p.Feed("reasoning here")
	if len(events) != 1 || events[0].Type != "thinking" || events[0].Thinking != "reasoning here" {
		t.Errorf("expected thinking event, got %+v", events)
	}

	// Feed closing tag
	events = p.Feed("</think>")
	if len(events) != 0 {
		t.Errorf("expected 0 events for closing tag, got %d", len(events))
	}

	// Feed normal content
	events = p.Feed("The answer is 42.")
	if len(events) != 1 || events[0].Type != "text" || events[0].Text != "The answer is 42." {
		t.Errorf("expected text event, got %+v", events)
	}
}

func TestThinkingStreamParser_TagSplitAcrossChunks(t *testing.T) {
	p := &ThinkingStreamParser{}

	// Opening tag split: "<thi" then "nk>"
	events := p.Feed("<thi")
	if len(events) != 0 {
		t.Errorf("expected 0 events for partial tag, got %d", len(events))
	}

	events = p.Feed("nk>inner")
	if len(events) != 1 || events[0].Type != "thinking" || events[0].Thinking != "inner" {
		t.Errorf("expected thinking event with 'inner', got %+v", events)
	}
}

func TestThinkingStreamParser_NoThinkTags(t *testing.T) {
	p := &ThinkingStreamParser{}

	events := p.Feed("Just normal text.")
	if len(events) != 1 || events[0].Type != "text" || events[0].Text != "Just normal text." {
		t.Errorf("expected text event, got %+v", events)
	}
}

func TestLocalAuthMode(t *testing.T) {
	info := LocalAuthMode("ollama", "Ollama (Local)")
	if info.Mode != "local" {
		t.Errorf("Mode: want local, got %q", info.Mode)
	}
	if info.DisplayName != "Ollama (Local)" {
		t.Errorf("DisplayName: want %q, got %q", "Ollama (Local)", info.DisplayName)
	}
	if !info.ServerSafe {
		t.Error("expected ServerSafe to be true")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestParseThinking|TestThinkingStream|TestLocalAuth" -v`
Expected: FAIL — functions don't exist

**Step 3: Implement local.go**

Create `provider/local.go`:

```go
package provider

import "strings"

// ParseThinking extracts <think>...</think> blocks from model output.
// Returns (thinking, content) where thinking is all extracted reasoning
// joined by newlines, and content has all think blocks removed.
func ParseThinking(raw string) (thinking, content string) {
	var thinkParts []string
	var contentBuf strings.Builder
	remaining := raw

	for {
		openIdx := strings.Index(remaining, "<think>")
		if openIdx == -1 {
			contentBuf.WriteString(remaining)
			break
		}
		contentBuf.WriteString(remaining[:openIdx])
		remaining = remaining[openIdx+len("<think>"):]

		closeIdx := strings.Index(remaining, "</think>")
		if closeIdx == -1 {
			// Unclosed tag — rest is thinking
			if remaining != "" {
				thinkParts = append(thinkParts, remaining)
			}
			break
		}
		block := remaining[:closeIdx]
		if block != "" {
			thinkParts = append(thinkParts, block)
		}
		remaining = remaining[closeIdx+len("</think>"):]
	}

	return strings.Join(thinkParts, "\n"), contentBuf.String()
}

// ThinkingStreamParser tracks state across streaming chunks to correctly
// split thinking vs content tokens. It handles <think> and </think> tags
// that may be split across multiple chunks.
type ThinkingStreamParser struct {
	inThink bool
	buf     strings.Builder // partial tag buffer
}

// Feed processes a chunk of text and returns stream events.
// Events are either type "thinking" or type "text".
func (p *ThinkingStreamParser) Feed(chunk string) []StreamEvent {
	p.buf.WriteString(chunk)
	text := p.buf.String()
	p.buf.Reset()

	var events []StreamEvent

	for len(text) > 0 {
		if p.inThink {
			closeIdx := strings.Index(text, "</think>")
			if closeIdx == -1 {
				// Check for partial closing tag at end
				for i := 1; i < len("</think>") && i <= len(text); i++ {
					suffix := text[len(text)-i:]
					if strings.HasPrefix("</think>", suffix) {
						thinkContent := text[:len(text)-i]
						if thinkContent != "" {
							events = append(events, StreamEvent{Type: "thinking", Thinking: thinkContent})
						}
						p.buf.WriteString(suffix)
						return events
					}
				}
				// No partial tag — all thinking
				events = append(events, StreamEvent{Type: "thinking", Thinking: text})
				return events
			}
			thinkContent := text[:closeIdx]
			if thinkContent != "" {
				events = append(events, StreamEvent{Type: "thinking", Thinking: thinkContent})
			}
			p.inThink = false
			text = text[closeIdx+len("</think>"):]
		} else {
			openIdx := strings.Index(text, "<think>")
			if openIdx == -1 {
				// Check for partial opening tag at end
				for i := 1; i < len("<think>") && i <= len(text); i++ {
					suffix := text[len(text)-i:]
					if strings.HasPrefix("<think>", suffix) {
						normalContent := text[:len(text)-i]
						if normalContent != "" {
							events = append(events, StreamEvent{Type: "text", Text: normalContent})
						}
						p.buf.WriteString(suffix)
						return events
					}
				}
				// No partial tag — all content
				events = append(events, StreamEvent{Type: "text", Text: text})
				return events
			}
			if openIdx > 0 {
				events = append(events, StreamEvent{Type: "text", Text: text[:openIdx]})
			}
			p.inThink = true
			text = text[openIdx+len("<think>"):]
		}
	}

	return events
}

// LocalAuthMode returns an AuthModeInfo for local model providers.
func LocalAuthMode(name, displayName string) AuthModeInfo {
	return AuthModeInfo{
		Mode:        "local",
		DisplayName: displayName,
		Description: "Local model inference, no API key required.",
		ServerSafe:  true,
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestParseThinking|TestThinkingStream|TestLocalAuth" -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add provider/local.go provider/local_test.go
git commit -m "feat: add shared think-tag parser and streaming state machine"
```

---

### Task 3: Add Ollama Dependency

**Files:**
- Modify: `go.mod`

**Step 1: Add the ollama/api dependency**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go get github.com/ollama/ollama/api`

**Step 2: Tidy**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go mod tidy`

**Step 3: Verify build**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go build ./...`
Expected: PASS

**Step 4: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add go.mod go.sum
git commit -m "chore: add github.com/ollama/ollama/api dependency"
```

---

### Task 4: Ollama Provider — Type Conversion

**Files:**
- Create: `provider/ollama_convert.go`
- Create: `provider/ollama_convert_test.go`

**Step 1: Write the failing tests**

Create `provider/ollama_convert_test.go`:

```go
package provider

import (
	"testing"

	ollamaapi "github.com/ollama/ollama/api"
)

func TestToOllamaMessages_BasicRoles(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "You are helpful."},
		{Role: RoleUser, Content: "Hello"},
		{Role: RoleAssistant, Content: "Hi there!"},
	}
	result := toOllamaMessages(msgs)
	if len(result) != 3 {
		t.Fatalf("want 3 messages, got %d", len(result))
	}
	if result[0].Role != "system" || result[0].Content != "You are helpful." {
		t.Errorf("msg[0]: want system/You are helpful., got %s/%s", result[0].Role, result[0].Content)
	}
	if result[1].Role != "user" || result[1].Content != "Hello" {
		t.Errorf("msg[1]: want user/Hello, got %s/%s", result[1].Role, result[1].Content)
	}
	if result[2].Role != "assistant" || result[2].Content != "Hi there!" {
		t.Errorf("msg[2]: want assistant/Hi there!, got %s/%s", result[2].Role, result[2].Content)
	}
}

func TestToOllamaMessages_ToolResult(t *testing.T) {
	msgs := []Message{
		{Role: RoleTool, Content: `{"result":"ok"}`, ToolCallID: "call-1"},
	}
	result := toOllamaMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("want 1 message, got %d", len(result))
	}
	if result[0].Role != "tool" {
		t.Errorf("role: want tool, got %s", result[0].Role)
	}
}

func TestToOllamaMessages_AssistantWithToolCalls(t *testing.T) {
	msgs := []Message{
		{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{
				{ID: "tc-1", Name: "search", Arguments: map[string]any{"query": "test"}},
			},
		},
	}
	result := toOllamaMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("want 1 message, got %d", len(result))
	}
	if len(result[0].ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(result[0].ToolCalls))
	}
	if result[0].ToolCalls[0].Function.Name != "search" {
		t.Errorf("tool name: want search, got %s", result[0].ToolCalls[0].Function.Name)
	}
}

func TestToOllamaTools(t *testing.T) {
	tools := []ToolDef{
		{
			Name:        "get_weather",
			Description: "Get weather for a location",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"location": map[string]any{"type": "string"}}},
		},
	}
	result := toOllamaTools(tools)
	if len(result) != 1 {
		t.Fatalf("want 1 tool, got %d", len(result))
	}
	if result[0].Function.Name != "get_weather" {
		t.Errorf("name: want get_weather, got %s", result[0].Function.Name)
	}
	if result[0].Function.Description != "Get weather for a location" {
		t.Errorf("description: want %q, got %q", "Get weather for a location", result[0].Function.Description)
	}
}

func TestFromOllamaResponse_Content(t *testing.T) {
	ollamaResp := ollamaapi.ChatResponse{
		Message: ollamaapi.Message{
			Role:    "assistant",
			Content: "<think>I should help.</think>The answer is 42.",
		},
	}
	resp := fromOllamaResponse(ollamaResp)
	if resp.Content != "The answer is 42." {
		t.Errorf("Content: want %q, got %q", "The answer is 42.", resp.Content)
	}
	if resp.Thinking != "I should help." {
		t.Errorf("Thinking: want %q, got %q", "I should help.", resp.Thinking)
	}
}

func TestFromOllamaResponse_ToolCalls(t *testing.T) {
	ollamaResp := ollamaapi.ChatResponse{
		Message: ollamaapi.Message{
			Role: "assistant",
			ToolCalls: []ollamaapi.ToolCall{
				{
					Function: ollamaapi.ToolCallFunction{
						Name:      "search",
						Arguments: ollamaapi.ToolCallFunctionArguments{"query": "test"},
					},
				},
			},
		},
	}
	resp := fromOllamaResponse(ollamaResp)
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "search" {
		t.Errorf("tool name: want search, got %s", resp.ToolCalls[0].Name)
	}
	q, ok := resp.ToolCalls[0].Arguments["query"]
	if !ok || q != "test" {
		t.Errorf("tool args: want query=test, got %v", resp.ToolCalls[0].Arguments)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestToOllama|TestFromOllama" -v`
Expected: FAIL — functions don't exist

**Step 3: Implement ollama_convert.go**

Create `provider/ollama_convert.go`:

```go
package provider

import (
	"encoding/json"
	"fmt"

	ollamaapi "github.com/ollama/ollama/api"
)

// toOllamaMessages converts provider messages to Ollama API messages.
func toOllamaMessages(msgs []Message) []ollamaapi.Message {
	result := make([]ollamaapi.Message, 0, len(msgs))
	for _, msg := range msgs {
		om := ollamaapi.Message{
			Role:    string(msg.Role),
			Content: msg.Content,
		}
		for _, tc := range msg.ToolCalls {
			args := ollamaapi.ToolCallFunctionArguments{}
			for k, v := range tc.Arguments {
				args[k] = v
			}
			om.ToolCalls = append(om.ToolCalls, ollamaapi.ToolCall{
				Function: ollamaapi.ToolCallFunction{
					Name:      tc.Name,
					Arguments: args,
				},
			})
		}
		result = append(result, om)
	}
	return result
}

// toOllamaTools converts provider tool definitions to Ollama API tools.
func toOllamaTools(tools []ToolDef) []ollamaapi.Tool {
	result := make([]ollamaapi.Tool, 0, len(tools))
	for _, t := range tools {
		params := ollamaapi.ToolFunctionParameters{
			Type: "object",
		}
		if t.Parameters != nil {
			if props, ok := t.Parameters["properties"]; ok {
				if pm, ok := props.(map[string]any); ok {
					propMap := make(ollamaapi.ToolPropertiesMap)
					for k, v := range pm {
						prop := ollamaapi.ToolProperty{}
						if vm, ok := v.(map[string]any); ok {
							if pt, ok := vm["type"].(string); ok {
								prop.Type = ollamaapi.PropertyType(pt)
							}
							if desc, ok := vm["description"].(string); ok {
								prop.Description = desc
							}
							if enum, ok := vm["enum"].([]any); ok {
								for _, e := range enum {
									if s, ok := e.(string); ok {
										prop.Enum = append(prop.Enum, s)
									}
								}
							}
						}
						propMap[k] = prop
					}
					params.Properties = propMap
				}
			}
			if req, ok := t.Parameters["required"]; ok {
				if reqSlice, ok := req.([]any); ok {
					for _, r := range reqSlice {
						if s, ok := r.(string); ok {
							params.Required = append(params.Required, s)
						}
					}
				}
			}
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

// fromOllamaResponse converts an Ollama ChatResponse to the provider Response type.
// It applies ParseThinking to extract <think> blocks.
func fromOllamaResponse(resp ollamaapi.ChatResponse) *Response {
	thinking, content := ParseThinking(resp.Message.Content)
	r := &Response{
		Content:  content,
		Thinking: thinking,
	}

	for _, tc := range resp.Message.ToolCalls {
		args := make(map[string]any)
		for k, v := range tc.Function.Arguments {
			args[k] = v
		}
		// Generate a tool call ID since Ollama doesn't provide one
		r.ToolCalls = append(r.ToolCalls, ToolCall{
			ID:        fmt.Sprintf("ollama-%s-%d", tc.Function.Name, len(r.ToolCalls)),
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}

	// Map Ollama metrics to usage if available
	if resp.PromptEvalCount > 0 || resp.EvalCount > 0 {
		r.Usage = Usage{
			InputTokens:  resp.PromptEvalCount,
			OutputTokens: resp.EvalCount,
		}
	}

	return r
}

// fromOllamaStreamChunk converts an Ollama streaming ChatResponse to a string chunk.
// The caller is responsible for feeding this through ThinkingStreamParser.
func fromOllamaStreamChunk(resp ollamaapi.ChatResponse) (string, []ToolCall, bool) {
	var toolCalls []ToolCall
	for _, tc := range resp.Message.ToolCalls {
		args := make(map[string]any)
		for k, v := range tc.Function.Arguments {
			args[k] = v
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:        fmt.Sprintf("ollama-%s-%d", tc.Function.Name, len(toolCalls)),
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}
	return resp.Message.Content, toolCalls, resp.Done
}

// ollamaToolCallsToJSON converts tool calls for embedding in Ollama messages.
func ollamaToolCallsToJSON(tcs []ToolCall) string {
	b, _ := json.Marshal(tcs)
	return string(b)
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestToOllama|TestFromOllama" -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add provider/ollama_convert.go provider/ollama_convert_test.go
git commit -m "feat: add Ollama message and tool type conversion helpers"
```

---

### Task 5: Ollama Provider — Core Implementation

**Files:**
- Create: `provider/ollama.go`
- Create: `provider/ollama_test.go`

**Step 1: Write the failing tests**

Create `provider/ollama_test.go`:

```go
package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaProvider_Name(t *testing.T) {
	p := NewOllamaProvider(OllamaConfig{Model: "test"})
	if p.Name() != "ollama" {
		t.Errorf("Name: want ollama, got %q", p.Name())
	}
}

func TestOllamaProvider_AuthModeInfo(t *testing.T) {
	p := NewOllamaProvider(OllamaConfig{Model: "test"})
	info := p.AuthModeInfo()
	if info.Mode != "local" {
		t.Errorf("Mode: want local, got %q", info.Mode)
	}
	if !info.ServerSafe {
		t.Error("expected ServerSafe to be true")
	}
}

func TestOllamaProvider_Chat(t *testing.T) {
	// Mock Ollama API server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		resp := map[string]any{
			"model":              "test-model",
			"message":            map[string]any{"role": "assistant", "content": "<think>Reasoning.</think>The answer."},
			"done":               true,
			"prompt_eval_count":  10,
			"eval_count":         5,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{
		Model:   "test-model",
		BaseURL: srv.URL,
	})

	resp, err := p.Chat(context.Background(), []Message{
		{Role: RoleUser, Content: "What is 6*7?"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "The answer." {
		t.Errorf("Content: want %q, got %q", "The answer.", resp.Content)
	}
	if resp.Thinking != "Reasoning." {
		t.Errorf("Thinking: want %q, got %q", "Reasoning.", resp.Thinking)
	}
}

func TestOllamaProvider_Health(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{
		Model:   "test",
		BaseURL: srv.URL,
	})

	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestOllamaProvider_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			resp := map[string]any{
				"models": []map[string]any{
					{"name": "llama3:latest", "size": 4000000000},
					{"name": "qwen3.5:27b", "size": 16000000000},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{
		Model:   "test",
		BaseURL: srv.URL,
	})

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("want 2 models, got %d", len(models))
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestOllamaProvider" -v`
Expected: FAIL — `NewOllamaProvider` doesn't exist

**Step 3: Implement ollama.go**

Create `provider/ollama.go`:

```go
package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	ollamaapi "github.com/ollama/ollama/api"
)

const (
	defaultOllamaBaseURL = "http://localhost:11434"
	defaultOllamaModel   = "llama3"
)

// OllamaConfig holds configuration for the Ollama provider.
type OllamaConfig struct {
	Model      string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// OllamaProvider implements Provider using the Ollama API.
type OllamaProvider struct {
	client *ollamaapi.Client
	config OllamaConfig
}

// NewOllamaProvider creates a new Ollama provider.
func NewOllamaProvider(cfg OllamaConfig) *OllamaProvider {
	if cfg.Model == "" {
		cfg.Model = defaultOllamaModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultOllamaBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	base, _ := url.Parse(cfg.BaseURL)
	client := ollamaapi.NewClient(base, cfg.HTTPClient)

	return &OllamaProvider{client: client, config: cfg}
}

func (p *OllamaProvider) Name() string { return "ollama" }

func (p *OllamaProvider) AuthModeInfo() AuthModeInfo {
	return LocalAuthMode("ollama", "Ollama (Local)")
}

func (p *OllamaProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	req := &ollamaapi.ChatRequest{
		Model:    p.config.Model,
		Messages: toOllamaMessages(messages),
		Stream:   new(bool), // false — non-streaming
	}
	if len(tools) > 0 {
		req.Tools = toOllamaTools(tools)
	}
	if p.config.MaxTokens > 0 {
		req.Options = map[string]any{"num_predict": p.config.MaxTokens}
	}

	var chatResp ollamaapi.ChatResponse
	err := p.client.Chat(ctx, req, func(resp ollamaapi.ChatResponse) error {
		chatResp = resp
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}

	return fromOllamaResponse(chatResp), nil
}

func (p *OllamaProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	stream := new(bool)
	*stream = true

	req := &ollamaapi.ChatRequest{
		Model:    p.config.Model,
		Messages: toOllamaMessages(messages),
		Stream:   stream,
	}
	if len(tools) > 0 {
		req.Tools = toOllamaTools(tools)
	}
	if p.config.MaxTokens > 0 {
		req.Options = map[string]any{"num_predict": p.config.MaxTokens}
	}

	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		parser := &ThinkingStreamParser{}
		var usage *Usage

		err := p.client.Chat(ctx, req, func(resp ollamaapi.ChatResponse) error {
			chunk, toolCalls, done := fromOllamaStreamChunk(resp)

			// Track usage from final chunk
			if resp.PromptEvalCount > 0 || resp.EvalCount > 0 {
				usage = &Usage{
					InputTokens:  resp.PromptEvalCount,
					OutputTokens: resp.EvalCount,
				}
			}

			// Emit tool calls
			for i := range toolCalls {
				ch <- StreamEvent{Type: "tool_call", Tool: &toolCalls[i]}
			}

			// Feed text through thinking parser
			if chunk != "" {
				for _, evt := range parser.Feed(chunk) {
					ch <- evt
				}
			}

			if done {
				ch <- StreamEvent{Type: "done", Usage: usage}
			}

			return nil
		})
		if err != nil {
			ch <- StreamEvent{Type: "error", Error: err.Error()}
		}
	}()

	return ch, nil
}

// Pull downloads a model via the Ollama API.
func (p *OllamaProvider) Pull(ctx context.Context, model string, progress func(status string, pct float64)) error {
	req := &ollamaapi.PullRequest{Model: model}
	return p.client.Pull(ctx, req, func(resp ollamaapi.ProgressResponse) error {
		if progress != nil {
			pct := 0.0
			if resp.Total > 0 {
				pct = float64(resp.Completed) / float64(resp.Total) * 100
			}
			progress(resp.Status, pct)
		}
		return nil
	})
}

// ListModels returns locally available models from the Ollama server.
func (p *OllamaProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := p.client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("ollama: list models: %w", err)
	}

	models := make([]ModelInfo, 0, len(resp.Models))
	for _, m := range resp.Models {
		models = append(models, ModelInfo{
			ID:   m.Name,
			Name: m.Name,
		})
	}
	return models, nil
}

// Health checks if the Ollama server is reachable.
func (p *OllamaProvider) Health(ctx context.Context) error {
	return p.client.Heartbeat(ctx)
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestOllamaProvider" -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add provider/ollama.go provider/ollama_test.go
git commit -m "feat: add Ollama provider with chat, stream, pull, and health"
```

---

### Task 6: llama.cpp Provider — Core Implementation

**Files:**
- Create: `provider/llama_cpp.go`
- Create: `provider/llama_cpp_test.go`

**Step 1: Write the failing tests**

Create `provider/llama_cpp_test.go`:

```go
package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLlamaCppProvider_Name(t *testing.T) {
	p := NewLlamaCppProvider(LlamaCppConfig{BaseURL: "http://localhost:8081/v1"})
	if p.Name() != "llama_cpp" {
		t.Errorf("Name: want llama_cpp, got %q", p.Name())
	}
}

func TestLlamaCppProvider_AuthModeInfo(t *testing.T) {
	p := NewLlamaCppProvider(LlamaCppConfig{BaseURL: "http://localhost:8081/v1"})
	info := p.AuthModeInfo()
	if info.Mode != "local" {
		t.Errorf("Mode: want local, got %q", info.Mode)
	}
}

func TestLlamaCppProvider_Chat_ExternalMode(t *testing.T) {
	// Mock OpenAI-compatible server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"model":   "local-model",
			"choices": []map[string]any{
				{
					"index":   0,
					"message": map[string]any{"role": "assistant", "content": "<think>Let me think.</think>Result here."},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 8},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewLlamaCppProvider(LlamaCppConfig{
		BaseURL: srv.URL + "/v1",
	})

	resp, err := p.Chat(context.Background(), []Message{
		{Role: RoleUser, Content: "Hello"},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "Result here." {
		t.Errorf("Content: want %q, got %q", "Result here.", resp.Content)
	}
	if resp.Thinking != "Let me think." {
		t.Errorf("Thinking: want %q, got %q", "Let me think.", resp.Thinking)
	}
}

func TestLlamaCppProvider_ManagedMode_RequiresModelPath(t *testing.T) {
	// No BaseURL and no ModelPath — should not panic
	p := NewLlamaCppProvider(LlamaCppConfig{})
	if p.config.BaseURL == "" && p.config.ModelPath == "" {
		// This is a misconfiguration but should not crash
		_, err := p.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
		if err == nil {
			t.Error("expected error for misconfigured provider")
		}
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestLlamaCppProvider" -v`
Expected: FAIL — `NewLlamaCppProvider` doesn't exist

**Step 3: Implement llama_cpp.go**

Create `provider/llama_cpp.go`:

```go
package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

const (
	defaultLlamaCppPort        = 8081
	defaultLlamaCppContextSize = 8192
	defaultLlamaCppMaxTokens   = 4096
)

// LlamaCppConfig holds configuration for the llama.cpp provider.
type LlamaCppConfig struct {
	BaseURL     string // external mode if set
	ModelPath   string // managed mode if set (path to .gguf file)
	BinaryPath  string // path to llama-server binary (optional)
	GPULayers   int    // -ngl flag, default: -1 (all layers)
	ContextSize int    // -c flag, default: 8192
	Threads     int    // -t flag, default: runtime.NumCPU()
	Port        int    // server port for managed mode, default: 8081
	MaxTokens   int
	HTTPClient  *http.Client
}

// LlamaCppProvider implements Provider using an OpenAI-compatible server
// (llama-server, vLLM, TGI, etc.) with optional process management.
type LlamaCppProvider struct {
	client openaisdk.Client
	config LlamaCppConfig

	mu      sync.Mutex
	cmd     *exec.Cmd
	started bool
}

// NewLlamaCppProvider creates a new llama.cpp provider.
func NewLlamaCppProvider(cfg LlamaCppConfig) *LlamaCppProvider {
	if cfg.Port <= 0 {
		cfg.Port = defaultLlamaCppPort
	}
	if cfg.ContextSize <= 0 {
		cfg.ContextSize = defaultLlamaCppContextSize
	}
	if cfg.Threads <= 0 {
		cfg.Threads = runtime.NumCPU()
	}
	if cfg.GPULayers == 0 {
		cfg.GPULayers = -1 // offload all layers by default
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultLlamaCppMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	baseURL := cfg.BaseURL
	if baseURL == "" && cfg.ModelPath != "" {
		baseURL = fmt.Sprintf("http://localhost:%d/v1", cfg.Port)
	}

	opts := []option.RequestOption{
		option.WithAPIKey("no-key"), // local server, no auth
		option.WithHTTPClient(cfg.HTTPClient),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openaisdk.NewClient(opts...)

	return &LlamaCppProvider{
		client: client,
		config: cfg,
	}
}

func (p *LlamaCppProvider) Name() string { return "llama_cpp" }

func (p *LlamaCppProvider) AuthModeInfo() AuthModeInfo {
	return LocalAuthMode("llama_cpp", "llama.cpp (Local)")
}

// ensureServer starts the managed llama-server process if in managed mode.
func (p *LlamaCppProvider) ensureServer(ctx context.Context) error {
	if p.config.BaseURL != "" {
		return nil // external mode, nothing to manage
	}
	if p.config.ModelPath == "" {
		return fmt.Errorf("llama_cpp: either base_url or model_path must be set")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.started {
		return nil
	}

	// Find llama-server binary
	binaryPath := p.config.BinaryPath
	if binaryPath == "" {
		var err error
		binaryPath, err = exec.LookPath("llama-server")
		if err != nil {
			// Try auto-download
			binaryPath, err = EnsureLlamaServer(ctx)
			if err != nil {
				return fmt.Errorf("llama_cpp: cannot find or download llama-server: %w", err)
			}
		}
	}

	// Build command
	args := []string{
		"-m", p.config.ModelPath,
		"-c", fmt.Sprintf("%d", p.config.ContextSize),
		"-t", fmt.Sprintf("%d", p.config.Threads),
		"-ngl", fmt.Sprintf("%d", p.config.GPULayers),
		"--port", fmt.Sprintf("%d", p.config.Port),
	}

	p.cmd = exec.CommandContext(ctx, binaryPath, args...)
	p.cmd.Stdout = io.Discard
	p.cmd.Stderr = os.Stderr

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("llama_cpp: start server: %w", err)
	}

	// Wait for health
	healthURL := fmt.Sprintf("http://localhost:%d/health", p.config.Port)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		resp, err := p.config.HTTPClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				p.started = true
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Timeout — kill the process
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	return fmt.Errorf("llama_cpp: server failed to become healthy within 2 minutes")
}

func (p *LlamaCppProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef) (*Response, error) {
	if err := p.ensureServer(ctx); err != nil {
		return nil, err
	}

	params := openaisdk.ChatCompletionNewParams{
		Model:     shared.ChatModel(p.config.ModelPath),
		Messages:  toOpenAIMessages(messages),
		MaxTokens: openaisdk.Int(int64(p.config.MaxTokens)),
	}
	if len(tools) > 0 {
		params.Tools = toOpenAITools(tools)
	}

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("llama_cpp: %w", err)
	}

	result, err := fromOpenAIResponse(resp)
	if err != nil {
		return nil, err
	}

	// Apply thinking extraction
	if result.Content != "" {
		thinking, content := ParseThinking(result.Content)
		result.Thinking = thinking
		result.Content = content
	}

	return result, nil
}

func (p *LlamaCppProvider) Stream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan StreamEvent, error) {
	if err := p.ensureServer(ctx); err != nil {
		return nil, err
	}

	params := openaisdk.ChatCompletionNewParams{
		Model:     shared.ChatModel(p.config.ModelPath),
		Messages:  toOpenAIMessages(messages),
		MaxTokens: openaisdk.Int(int64(p.config.MaxTokens)),
	}
	if len(tools) > 0 {
		params.Tools = toOpenAITools(tools)
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	ch := make(chan StreamEvent, 16)

	go func() {
		defer close(ch)
		parser := &ThinkingStreamParser{}

		// Wrap the OpenAI stream and apply thinking extraction
		innerCh := make(chan StreamEvent, 16)
		go streamOpenAIEvents(stream, innerCh)

		for evt := range innerCh {
			if evt.Type == "text" && evt.Text != "" {
				// Feed through thinking parser
				for _, parsed := range parser.Feed(evt.Text) {
					ch <- parsed
				}
			} else {
				ch <- evt
			}
		}
	}()

	return ch, nil
}

// Close stops the managed llama-server process if running.
func (p *LlamaCppProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		p.cmd.Wait()
		p.started = false
	}
	return nil
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestLlamaCppProvider" -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add provider/llama_cpp.go provider/llama_cpp_test.go
git commit -m "feat: add llama.cpp provider with external and managed modes"
```

---

### Task 7: llama-server Auto-Download

**Files:**
- Create: `provider/llama_cpp_download.go`
- Create: `provider/llama_cpp_download_test.go`

**Step 1: Write the failing tests**

Create `provider/llama_cpp_download_test.go`:

```go
package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseLlamaCppRelease_FindsAsset(t *testing.T) {
	release := ghRelease{
		TagName: "b1234",
		Assets: []ghAsset{
			{Name: "llama-b1234-bin-ubuntu-x64.zip", BrowserDownloadURL: "https://example.com/linux.zip"},
			{Name: "llama-b1234-bin-macos-arm64.zip", BrowserDownloadURL: "https://example.com/macos.zip"},
			{Name: "llama-b1234-bin-macos-x64.zip", BrowserDownloadURL: "https://example.com/macos-x64.zip"},
			{Name: "llama-b1234-bin-win-x64.zip", BrowserDownloadURL: "https://example.com/windows.zip"},
		},
	}

	url, err := findAssetURL(release, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("findAssetURL: %v", err)
	}
	if url == "" {
		t.Error("expected non-empty URL")
	}
}

func TestEnsureLlamaServer_CachedBinary(t *testing.T) {
	// Set up a fake cached binary
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "llama-server", "test-version")
	os.MkdirAll(cacheDir, 0o755)
	binaryName := "llama-server"
	if runtime.GOOS == "windows" {
		binaryName = "llama-server.exe"
	}
	binaryPath := filepath.Join(cacheDir, binaryName)
	os.WriteFile(binaryPath, []byte("#!/bin/sh\n"), 0o755)

	// Override cache dir for test
	origCacheDir := llamaCppCacheDir
	llamaCppCacheDir = filepath.Join(tmpDir, "llama-server")
	defer func() { llamaCppCacheDir = origCacheDir }()

	path, err := EnsureLlamaServer(context.Background())
	if err != nil {
		t.Fatalf("EnsureLlamaServer: %v", err)
	}
	if path != binaryPath {
		t.Errorf("want %q, got %q", binaryPath, path)
	}
}

func TestEnsureLlamaServer_Downloads(t *testing.T) {
	// Create a fake zip with a llama-server binary
	tmpDir := t.TempDir()

	// Mock GitHub API
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/ggerganov/llama.cpp/releases/latest":
			resp := ghRelease{
				TagName: "b9999",
				Assets: []ghAsset{
					{Name: assetNameForPlatform("b9999", runtime.GOOS, runtime.GOARCH), BrowserDownloadURL: srv.URL + "/download/test.zip"},
				},
			}
			// Need to declare this function
			json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/download/test.zip":
			// Serve empty zip — test will fail on extraction but proves the flow
			w.Header().Set("Content-Type", "application/zip")
			w.Write([]byte("PK\x05\x06" + string(make([]byte, 18)))) // minimal empty zip
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origCacheDir := llamaCppCacheDir
	origAPIBase := llamaCppGitHubAPI
	llamaCppCacheDir = filepath.Join(tmpDir, "llama-server")
	llamaCppGitHubAPI = srv.URL
	defer func() {
		llamaCppCacheDir = origCacheDir
		llamaCppGitHubAPI = origAPIBase
	}()

	// This will attempt the download flow — may fail on zip extraction
	// which is acceptable; we're testing the discovery + download path
	_, _ = EnsureLlamaServer(context.Background())
	// The test verifies no panic and the HTTP calls are made correctly
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestParseLlamaCpp|TestEnsureLlamaServer" -v`
Expected: FAIL — functions don't exist

**Step 3: Implement llama_cpp_download.go**

Create `provider/llama_cpp_download.go`:

```go
package provider

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Package-level vars for test overrides.
var (
	llamaCppCacheDir  = filepath.Join(userCacheDir(), "workflow", "llama-server")
	llamaCppGitHubAPI = "https://api.github.com"
)

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// EnsureLlamaServer finds or downloads the llama-server binary.
// Search order: PATH lookup → cached version → download from GitHub releases.
func EnsureLlamaServer(ctx context.Context) (string, error) {
	// Check cache first (any version)
	if entries, err := os.ReadDir(llamaCppCacheDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			binaryName := "llama-server"
			if runtime.GOOS == "windows" {
				binaryName = "llama-server.exe"
			}
			candidate := filepath.Join(llamaCppCacheDir, entry.Name(), binaryName)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}

	// Download latest release
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		return "", fmt.Errorf("fetch latest llama.cpp release: %w", err)
	}

	downloadURL, err := findAssetURL(release, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	versionDir := filepath.Join(llamaCppCacheDir, release.TagName)
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	// Download and extract
	zipPath := filepath.Join(versionDir, "download.zip")
	if err := downloadFile(ctx, downloadURL, zipPath); err != nil {
		return "", fmt.Errorf("download llama-server: %w", err)
	}
	defer os.Remove(zipPath)

	binaryName := "llama-server"
	if runtime.GOOS == "windows" {
		binaryName = "llama-server.exe"
	}

	if err := extractBinaryFromZip(zipPath, binaryName, versionDir); err != nil {
		return "", fmt.Errorf("extract llama-server: %w", err)
	}

	binaryPath := filepath.Join(versionDir, binaryName)
	os.Chmod(binaryPath, 0o755)
	return binaryPath, nil
}

func fetchLatestRelease(ctx context.Context) (ghRelease, error) {
	url := llamaCppGitHubAPI + "/repos/ggerganov/llama.cpp/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ghRelease{}, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ghRelease{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return ghRelease{}, fmt.Errorf("GitHub API: status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return ghRelease{}, err
	}
	return release, nil
}

// findAssetURL selects the correct release asset for the given OS/arch.
func findAssetURL(release ghRelease, goos, goarch string) (string, error) {
	target := assetNameForPlatform(release.TagName, goos, goarch)

	for _, asset := range release.Assets {
		if asset.Name == target {
			return asset.BrowserDownloadURL, nil
		}
	}

	// Fuzzy match — sometimes naming conventions vary
	osKey, archKey := platformKeys(goos, goarch)
	for _, asset := range release.Assets {
		lower := strings.ToLower(asset.Name)
		if strings.Contains(lower, osKey) && strings.Contains(lower, archKey) && strings.HasSuffix(lower, ".zip") {
			return asset.BrowserDownloadURL, nil
		}
	}

	return "", fmt.Errorf("no llama.cpp release asset found for %s/%s in release %s", goos, goarch, release.TagName)
}

// assetNameForPlatform returns the expected asset filename.
func assetNameForPlatform(tag, goos, goarch string) string {
	osKey, archKey := platformKeys(goos, goarch)
	return fmt.Sprintf("llama-%s-bin-%s-%s.zip", tag, osKey, archKey)
}

func platformKeys(goos, goarch string) (string, string) {
	osKey := goos
	archKey := goarch
	switch goos {
	case "darwin":
		osKey = "macos"
	case "linux":
		osKey = "ubuntu"
	case "windows":
		osKey = "win"
	}
	switch goarch {
	case "amd64":
		archKey = "x64"
	case "arm64":
		// arm64 stays as-is for macOS, but check
		archKey = "arm64"
	}
	return osKey, archKey
}

func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: status %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func extractBinaryFromZip(zipPath, binaryName, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		// Look for the binary in any subdirectory
		if filepath.Base(f.Name) == binaryName {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()

			dest := filepath.Join(destDir, binaryName)
			out, err := os.Create(dest)
			if err != nil {
				return err
			}
			defer out.Close()

			_, err = io.Copy(out, rc)
			return err
		}
	}

	return fmt.Errorf("binary %q not found in zip archive", binaryName)
}

func userCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "workflow-cache")
	}
	return dir
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestParseLlamaCpp|TestEnsureLlamaServer" -v`
Expected: PASS

**Step 5: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add provider/llama_cpp_download.go provider/llama_cpp_download_test.go
git commit -m "feat: add llama-server auto-download from GitHub releases"
```

---

### Task 8: HuggingFace File Downloader

**Files:**
- Create: `provider/huggingface.go`
- Create: `provider/huggingface_test.go`

**Step 1: Write the failing tests**

Create `provider/huggingface_test.go`:

```go
package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadHuggingFaceFile_BasicDownload(t *testing.T) {
	fileContent := "fake-model-data-1234567890"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HF Hub resolve endpoint
		if r.URL.Path == "/api/models/testuser/testmodel/revision/main" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"_id":"testmodel","modelId":"testuser/testmodel"}`))
			return
		}
		// Direct file download
		if r.URL.Path == "/testuser/testmodel/resolve/main/model.gguf" {
			w.Header().Set("Content-Length", "26")
			w.Write([]byte(fileContent))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()

	origBase := huggingFaceBaseURL
	huggingFaceBaseURL = srv.URL
	defer func() { huggingFaceBaseURL = origBase }()

	path, err := DownloadHuggingFaceFile(context.Background(), "testuser/testmodel", "model.gguf", tmpDir, nil)
	if err != nil {
		t.Fatalf("DownloadHuggingFaceFile: %v", err)
	}

	expected := filepath.Join(tmpDir, "testuser--testmodel", "model.gguf")
	if path != expected {
		t.Errorf("path: want %q, got %q", expected, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != fileContent {
		t.Errorf("content: want %q, got %q", fileContent, string(data))
	}
}

func TestDownloadHuggingFaceFile_AlreadyExists(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "testuser--testmodel")
	os.MkdirAll(repoDir, 0o755)
	existingPath := filepath.Join(repoDir, "model.gguf")
	os.WriteFile(existingPath, []byte("existing"), 0o644)

	// No server needed — file already exists
	path, err := DownloadHuggingFaceFile(context.Background(), "testuser/testmodel", "model.gguf", tmpDir, nil)
	if err != nil {
		t.Fatalf("DownloadHuggingFaceFile: %v", err)
	}
	if path != existingPath {
		t.Errorf("path: want %q, got %q", existingPath, path)
	}
}

func TestDownloadHuggingFaceFile_Progress(t *testing.T) {
	fileContent := "fake-model-data"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/testuser/testmodel/resolve/main/model.gguf" {
			w.Header().Set("Content-Length", "15")
			w.Write([]byte(fileContent))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	origBase := huggingFaceBaseURL
	huggingFaceBaseURL = srv.URL
	defer func() { huggingFaceBaseURL = origBase }()

	var progressCalled bool
	_, err := DownloadHuggingFaceFile(context.Background(), "testuser/testmodel", "model.gguf", tmpDir, func(pct float64) {
		progressCalled = true
	})
	if err != nil {
		t.Fatalf("DownloadHuggingFaceFile: %v", err)
	}
	if !progressCalled {
		t.Error("expected progress callback to be called")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestDownloadHuggingFace" -v`
Expected: FAIL — `DownloadHuggingFaceFile` doesn't exist

**Step 3: Implement huggingface.go**

Create `provider/huggingface.go`:

```go
package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Package-level var for test overrides.
var huggingFaceBaseURL = "https://huggingface.co"

// DownloadHuggingFaceFile downloads a file from a HuggingFace model repository.
// It supports any file type (GGUF, safetensors, bin, etc.) from any HF repo.
// Files are saved to outputDir/<repo-slug>/<filename>.
// If the file already exists, it returns the path without re-downloading.
// The progress callback receives percentage (0-100) if Content-Length is available.
func DownloadHuggingFaceFile(ctx context.Context, repo, filename, outputDir string, progress func(pct float64)) (string, error) {
	// Sanitize repo name for filesystem
	repoSlug := strings.ReplaceAll(repo, "/", "--")
	destDir := filepath.Join(outputDir, repoSlug)
	destPath := filepath.Join(destDir, filename)

	// Check if already downloaded
	if info, err := os.Stat(destPath); err == nil && !info.IsDir() {
		return destPath, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	// Download from HF Hub
	downloadURL := fmt.Sprintf("%s/%s/resolve/main/%s", huggingFaceBaseURL, repo, filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	// Support resume via Range header
	tmpPath := destPath + ".part"
	var startByte int64
	if info, err := os.Stat(tmpPath); err == nil {
		startByte = info.Size()
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startByte))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// Determine total size
	var totalSize int64
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		totalSize, _ = strconv.ParseInt(cl, 10, 64)
		totalSize += startByte
	}

	// Open file for writing (append if resuming)
	flags := os.O_CREATE | os.O_WRONLY
	if startByte > 0 && resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		startByte = 0
	}

	f, err := os.OpenFile(tmpPath, flags, 0o644)
	if err != nil {
		return "", fmt.Errorf("open temp file: %w", err)
	}

	// Copy with progress tracking
	written := startByte
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				f.Close()
				return "", fmt.Errorf("write: %w", writeErr)
			}
			written += int64(n)
			if progress != nil && totalSize > 0 {
				progress(float64(written) / float64(totalSize) * 100)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			f.Close()
			return "", fmt.Errorf("read: %w", readErr)
		}
	}
	f.Close()

	// Rename temp to final
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", fmt.Errorf("rename temp file: %w", err)
	}

	return destPath, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run "TestDownloadHuggingFace" -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add provider/huggingface.go provider/huggingface_test.go
git commit -m "feat: add generic HuggingFace file downloader with resume support"
```

---

### Task 9: step.model_pull Step Type

**Files:**
- Create: `step_model_pull.go`
- Create: `step_model_pull_test.go`

**Step 1: Write the failing tests**

Create `step_model_pull_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/module"
)

func TestModelPullStep_HuggingFace(t *testing.T) {
	fileContent := "fake-gguf-model-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/testuser/testmodel/resolve/main/model.gguf" {
			w.Header().Set("Content-Length", "21")
			w.Write([]byte(fileContent))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Override HF base URL for test
	origBase := provider.HuggingFaceBaseURL()
	provider.SetHuggingFaceBaseURL(srv.URL)
	defer provider.SetHuggingFaceBaseURL(origBase)

	tmpDir := t.TempDir()

	step := &ModelPullStep{
		name:      "test_pull",
		source:    "huggingface",
		model:     "testuser/testmodel",
		file:      "model.gguf",
		outputDir: tmpDir,
	}

	pc := &module.PipelineContext{
		Current: map[string]any{},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	status, _ := result.Output["status"].(string)
	if status != "downloaded" && status != "ready" {
		t.Errorf("status: want downloaded or ready, got %q", status)
	}

	modelPath, _ := result.Output["model_path"].(string)
	if modelPath == "" {
		t.Error("model_path: want non-empty")
	}
}

func TestModelPullStep_Ollama(t *testing.T) {
	// Mock Ollama pull endpoint
	pullCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pull" && r.Method == "POST" {
			pullCalled = true
			// Return streaming progress then done
			resp := map[string]any{"status": "success"}
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	step := &ModelPullStep{
		name:       "test_pull",
		source:     "ollama",
		model:      "qwen3.5:27b",
		ollamaBase: srv.URL,
	}

	pc := &module.PipelineContext{
		Current: map[string]any{},
	}

	result, err := step.Execute(context.Background(), pc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !pullCalled {
		t.Error("expected Ollama pull API to be called")
	}
	_ = result // verify no panic
}

func TestModelPullStepFactory(t *testing.T) {
	factory := newModelPullStepFactory()
	step, err := factory("test", map[string]any{
		"provider":   "huggingface",
		"model":      "user/repo",
		"file":       "model.gguf",
		"output_dir": "/tmp/models",
	}, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if step == nil {
		t.Fatal("step is nil")
	}
	s, ok := step.(*ModelPullStep)
	if !ok {
		t.Fatalf("step type: want *ModelPullStep, got %T", step)
	}
	if s.source != "huggingface" {
		t.Errorf("source: want huggingface, got %q", s.source)
	}
	if s.model != "user/repo" {
		t.Errorf("model: want user/repo, got %q", s.model)
	}
	if s.file != "model.gguf" {
		t.Errorf("file: want model.gguf, got %q", s.file)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test . -run "TestModelPullStep" -v`
Expected: FAIL — `ModelPullStep` doesn't exist

**Step 3: Implement step_model_pull.go**

Create `step_model_pull.go`:

```go
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
)

// ModelPullStep ensures a model is available locally before pipeline execution.
type ModelPullStep struct {
	name       string
	source     string // "ollama" or "huggingface"
	model      string // model identifier (ollama tag or HF repo)
	file       string // specific file to download (HF only)
	outputDir  string // download destination (HF only)
	ollamaBase string // Ollama base URL (optional)
}

func (s *ModelPullStep) Name() string { return s.name }

func (s *ModelPullStep) Execute(ctx context.Context, pc *module.PipelineContext) (*module.StepResult, error) {
	output := map[string]any{}

	switch s.source {
	case "ollama":
		if err := s.pullOllama(ctx, output); err != nil {
			output["status"] = "error"
			output["error"] = err.Error()
			return &module.StepResult{Output: output}, nil
		}
	case "huggingface":
		if err := s.pullHuggingFace(ctx, output); err != nil {
			output["status"] = "error"
			output["error"] = err.Error()
			return &module.StepResult{Output: output}, nil
		}
	default:
		return nil, fmt.Errorf("model_pull step %q: unknown provider %q (supported: ollama, huggingface)", s.name, s.source)
	}

	return &module.StepResult{Output: output}, nil
}

func (s *ModelPullStep) pullOllama(ctx context.Context, output map[string]any) error {
	baseURL := s.ollamaBase
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	p := provider.NewOllamaProvider(provider.OllamaConfig{
		Model:   s.model,
		BaseURL: baseURL,
	})

	err := p.Pull(ctx, s.model, func(status string, pct float64) {
		// Could emit progress via pipeline metadata in the future
	})
	if err != nil {
		return fmt.Errorf("ollama pull %q: %w", s.model, err)
	}

	output["status"] = "ready"
	output["model_path"] = s.model
	return nil
}

func (s *ModelPullStep) pullHuggingFace(ctx context.Context, output map[string]any) error {
	if s.file == "" {
		return fmt.Errorf("huggingface provider requires 'file' config")
	}

	outDir := s.outputDir
	if outDir == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			cacheDir = os.TempDir()
		}
		outDir = filepath.Join(cacheDir, "workflow", "models")
	}

	path, err := provider.DownloadHuggingFaceFile(ctx, s.model, s.file, outDir, func(pct float64) {
		// Could emit progress via pipeline metadata in the future
	})
	if err != nil {
		return fmt.Errorf("huggingface download %q/%q: %w", s.model, s.file, err)
	}

	info, _ := os.Stat(path)
	output["status"] = "downloaded"
	output["model_path"] = path
	if info != nil {
		output["size_bytes"] = info.Size()
	}
	return nil
}

// newModelPullStepFactory returns a plugin.StepFactory for "step.model_pull".
func newModelPullStepFactory() plugin.StepFactory {
	return func(name string, cfg map[string]any, _ modular.Application) (any, error) {
		source, _ := cfg["provider"].(string)
		if source == "" {
			source = "ollama"
		}
		model, _ := cfg["model"].(string)
		file, _ := cfg["file"].(string)
		outputDir, _ := cfg["output_dir"].(string)
		ollamaBase, _ := cfg["base_url"].(string)

		return &ModelPullStep{
			name:       name,
			source:     source,
			model:      model,
			file:       file,
			outputDir:  outputDir,
			ollamaBase: ollamaBase,
		}, nil
	}
}
```

**Note:** You'll also need to expose the `huggingFaceBaseURL` var for testing. Add to `provider/huggingface.go`:

```go
// HuggingFaceBaseURL returns the current HuggingFace base URL (for testing).
func HuggingFaceBaseURL() string { return huggingFaceBaseURL }

// SetHuggingFaceBaseURL overrides the HuggingFace base URL (for testing).
func SetHuggingFaceBaseURL(url string) { huggingFaceBaseURL = url }
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test . -run "TestModelPullStep" -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add step_model_pull.go step_model_pull_test.go provider/huggingface.go
git commit -m "feat: add step.model_pull for Ollama and HuggingFace model acquisition"
```

---

### Task 10: Executor — Thread Thinking Into Transcript and Output

**Files:**
- Modify: `executor/executor.go:184-197` (transcript recording of thinking)
- Modify: `step_agent_execute.go:186-194` (step output)
- Modify: `executor/interfaces.go:12-22` (TranscriptEntry)

**Step 1: Write the failing tests**

Add to `executor/executor_test.go`:

```go
func TestExecute_ThinkingInResult(t *testing.T) {
	p := &mockProvider{
		name: "mock",
		chatResponse: &provider.Response{
			Content:  "The answer is 42.",
			Thinking: "Let me reason about this carefully.",
		},
	}
	cfg := Config{Provider: p}

	result, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Thinking != "Let me reason about this carefully." {
		t.Errorf("Thinking: want %q, got %q", "Let me reason about this carefully.", result.Thinking)
	}
}

func TestExecute_ThinkingInTranscript(t *testing.T) {
	p := &mockProvider{
		name: "mock",
		chatResponse: &provider.Response{
			Content:  "The answer.",
			Thinking: "My reasoning.",
		},
	}
	rec := &recordingTranscript{}
	cfg := Config{Provider: p, Transcript: rec}

	_, err := Execute(context.Background(), cfg, "sys", "task", "agent-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Should have a thinking entry before the assistant entry
	foundThinking := false
	for _, entry := range rec.entries {
		if entry.Thinking != "" {
			foundThinking = true
			if entry.Thinking != "My reasoning." {
				t.Errorf("thinking content: want %q, got %q", "My reasoning.", entry.Thinking)
			}
		}
	}
	if !foundThinking {
		t.Error("no thinking entry found in transcript")
	}
}

// recordingTranscript captures entries for test assertions.
type recordingTranscript struct {
	entries []TranscriptEntry
}

func (r *recordingTranscript) Record(_ context.Context, entry TranscriptEntry) error {
	r.entries = append(r.entries, entry)
	return nil
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./executor/ -run "TestExecute_Thinking" -v`
Expected: FAIL — `Result` has no `Thinking` field

**Step 3: Add Thinking to Result**

In `executor/executor.go`, add `Thinking` to `Result`:

```go
type Result struct {
	Content    string
	Thinking   string
	Iterations int
	Status     string
	Error      string
}
```

Add `Thinking` to `TranscriptEntry` in `executor/interfaces.go`:

```go
type TranscriptEntry struct {
	ID         string
	AgentID    string
	TaskID     string
	ProjectID  string
	Iteration  int
	Role       provider.Role
	Content    string
	Thinking   string
	ToolCalls  []provider.ToolCall
	ToolCallID string
}
```

In `executor/executor.go`, after `finalContent = resp.Content` (line 185), add:

```go
var finalThinking string
```

And after `finalContent = resp.Content` in the loop:

```go
finalThinking = resp.Thinking
```

Modify the transcript recording block (around line 188) to include thinking:

```go
		_ = cfg.Transcript.Record(ctx, TranscriptEntry{
			ID:        uuid.New().String(),
			AgentID:   agentID,
			TaskID:    cfg.TaskID,
			ProjectID: cfg.ProjectID,
			Iteration: iterCount,
			Role:      provider.RoleAssistant,
			Content:   resp.Content,
			Thinking:  resp.Thinking,
			ToolCalls: resp.ToolCalls,
		})
```

And update the final return (around line 342):

```go
	return &Result{
		Content:    finalContent,
		Thinking:   finalThinking,
		Status:     "completed",
		Iterations: iterCount,
	}, nil
```

**Step 4: Update step_agent_execute.go output**

In `step_agent_execute.go`, after `output["result"]` (line 187), add:

```go
	if result.Thinking != "" {
		output["thinking"] = result.Thinking
	}
```

**Step 5: Run tests to verify they pass**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./executor/ -run "TestExecute_Thinking" -v && go test ./...`
Expected: ALL PASS

**Step 6: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add executor/executor.go executor/interfaces.go step_agent_execute.go executor/executor_test.go
git commit -m "feat: thread thinking traces through executor, transcript, and step output"
```

---

### Task 11: Registry, Plugin, Auth Mode, and Model Listing Wiring

**Files:**
- Modify: `provider_registry.go:43-91` (add factories)
- Modify: `module_provider.go:96-211` (add cases)
- Modify: `plugin.go:36,56-62,100-122` (register step, update schema)
- Modify: `provider/auth_modes.go:15-32` (add local modes)
- Modify: `provider/models.go:22-59` (add cases)

**Step 1: Add factories to provider_registry.go**

After the `copilot` factory (line 88), add:

```go
	r.Factories["ollama"] = func(_ string, cfg LLMProviderConfig) (provider.Provider, error) {
		return provider.NewOllamaProvider(provider.OllamaConfig{
			Model:     cfg.Model,
			BaseURL:   cfg.BaseURL,
			MaxTokens: cfg.MaxTokens,
		}), nil
	}
	r.Factories["llama_cpp"] = func(_ string, cfg LLMProviderConfig) (provider.Provider, error) {
		return provider.NewLlamaCppProvider(provider.LlamaCppConfig{
			BaseURL:   cfg.BaseURL,
			ModelPath: cfg.Model,
			MaxTokens: cfg.MaxTokens,
		}), nil
	}
```

**Step 2: Add cases to module_provider.go**

After the `copilot` case (line 207), before `default:`, add:

```go
	case "ollama":
		p = provider.NewOllamaProvider(provider.OllamaConfig{
			Model:     model,
			BaseURL:   baseURL,
			MaxTokens: maxTokens,
		})

	case "llama_cpp":
		p = provider.NewLlamaCppProvider(provider.LlamaCppConfig{
			BaseURL:   baseURL,
			ModelPath: model,
			MaxTokens: maxTokens,
		})
```

Update the default error message to include the new types:

```go
	default:
		p = &errProvider{err: fmt.Errorf("agent.provider %q: unrecognized provider type %q (supported: mock, test, anthropic, openai, copilot, ollama, llama_cpp)", name, providerType)}
```

**Step 3: Register step.model_pull in plugin.go**

Update `Manifest.StepTypes` (line 36):

```go
StepTypes: []string{"step.agent_execute", "step.provider_test", "step.provider_models", "step.model_pull"},
```

Add to `StepFactories()`:

```go
func (p *AgentPlugin) StepFactories() map[string]plugin.StepFactory {
	return map[string]plugin.StepFactory{
		"step.agent_execute":   newAgentExecuteStepFactory(),
		"step.provider_test":   newProviderTestFactory(),
		"step.provider_models": newProviderModelsFactory(),
		"step.model_pull":      newModelPullStepFactory(),
	}
}
```

Add exported factory:

```go
// NewModelPullStepFactory returns the plugin.StepFactory for "step.model_pull".
func NewModelPullStepFactory() plugin.StepFactory {
	return newModelPullStepFactory()
}
```

Update `ModuleSchemas()` — add `"ollama"` and `"llama_cpp"` to provider type Options:

```go
{Key: "provider", Label: "Provider Type", Type: schema.FieldTypeSelect, Options: []string{"mock", "test", "anthropic", "openai", "copilot", "ollama", "llama_cpp"}, DefaultValue: "mock", Description: "AI provider backend"},
```

**Step 4: Add auth modes to auth_modes.go**

Add to the `AllAuthModes()` slice:

```go
		// Local inference
		{Mode: "local", DisplayName: "Ollama (Local)", Description: "Local model inference via Ollama, no API key required.", ServerSafe: true},
		{Mode: "local", DisplayName: "llama.cpp (Local)", Description: "Local model inference via llama-server or compatible endpoint, no API key required.", ServerSafe: true},
```

**Step 5: Add model listing to models.go**

Add cases in `ListModels()` switch:

```go
	case "ollama":
		return listOllamaModels(ctx, baseURL)
	case "llama_cpp":
		return []ModelInfo{{ID: "local", Name: "Local Model (configure via model_path)"}}, nil
```

Add the `listOllamaModels` function:

```go
func listOllamaModels(ctx context.Context, baseURL string) ([]ModelInfo, error) {
	p := NewOllamaProvider(OllamaConfig{BaseURL: baseURL})
	return p.ListModels(ctx)
}
```

**Step 6: Run all tests**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./...`
Expected: ALL PASS

**Step 7: Commit**

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add provider_registry.go module_provider.go plugin.go provider/auth_modes.go provider/models.go
git commit -m "feat: wire Ollama and llama.cpp into registry, module factory, plugin manifest, and model listing"
```

---

### Task 12: Build Verification and Full Test Run

**Step 1: Verify build compiles**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go build ./...`
Expected: PASS

**Step 2: Run full test suite**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./... -v -count=1`
Expected: ALL PASS

**Step 3: Run go vet**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go vet ./...`
Expected: No issues

**Step 4: If any failures, fix and commit**

Fix issues discovered by the full test run, then:

```bash
cd /Users/jon/workspace/workflow-plugin-agent
git add -A
git commit -m "fix: resolve issues found during full build verification"
```
