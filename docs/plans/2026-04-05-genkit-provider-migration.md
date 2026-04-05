# Genkit Provider Migration — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace all hand-rolled provider implementations in workflow-plugin-agent with Google Genkit Go SDK adapters, keeping the `provider.Provider` interface unchanged.

**Architecture:** A new `genkit/` package wraps Genkit plugins behind the existing `provider.Provider` interface. The `ProviderRegistry` factory functions are updated to call genkit factories. All provider implementation files (~25 files) are deleted.

**Tech Stack:** Go 1.26, Genkit Go v1.6.0, Genkit plugins (anthropic, openai, googlegenai, ollama, compat_oai), community plugins (aws-bedrock, azure-openai)

---

## Task 1: Add Genkit dependency and create package skeleton

**Files:**
- Modify: `go.mod`
- Create: `genkit/genkit.go`

**Step 1:** Add Genkit core dependency:
```bash
cd /Users/jon/workspace/workflow-plugin-agent
go get github.com/firebase/genkit/go@v1.6.0
go get github.com/firebase/genkit/go/plugins/anthropic
go get github.com/firebase/genkit/go/plugins/googlegenai
go get github.com/firebase/genkit/go/plugins/ollama
go get github.com/firebase/genkit/go/plugins/compat_oai
go mod tidy
```

**Step 2:** Create `genkit/genkit.go` — package with Genkit initialization:
```go
// Package genkit provides Genkit-backed implementations of provider.Provider.
package genkit

import (
	"context"
	"sync"

	gk "github.com/firebase/genkit/go/genkit"
)

var (
	instance *gk.Genkit
	once     sync.Once
)

// Instance returns the shared Genkit instance, initializing it lazily on first call.
// Plugins are registered dynamically when providers are created, not at init time.
func Instance(ctx context.Context) *gk.Genkit {
	once.Do(func() {
		instance = gk.Init(ctx)
	})
	return instance
}
```

**Step 3:** Build to verify: `go build ./...`

**Step 4:** Commit:
```bash
git add go.mod go.sum genkit/genkit.go
git commit -m "chore: add Genkit Go SDK dependency and package skeleton"
```

---

## Task 2: Implement type conversion layer

**Files:**
- Create: `genkit/convert.go`
- Create: `genkit/convert_test.go`

**Step 1:** Create `genkit/convert.go` — bidirectional type conversion between our `provider.*` types and Genkit `ai.*` types:
```go
package genkit

import (
	"github.com/firebase/genkit/go/ai"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
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

		parts := []*ai.Part{ai.NewTextPart(m.Content)}

		// Tool call results: add as ToolResponsePart
		if m.ToolCallID != "" {
			parts = []*ai.Part{ai.NewToolResponsePart(&ai.ToolResponse{
				Name:   m.ToolCallID,
				Output: map[string]any{"result": m.Content},
			})}
		}

		out = append(out, ai.NewMessage(role, nil, parts...))
	}
	return out
}

// toGenkitToolDefs converts our tool definitions to Genkit tool defs.
// Returns the tool names for WithModelName-style passing.
func toGenkitToolDefs(tools []provider.ToolDef) []ai.ToolDef {
	out := make([]ai.ToolDef, 0, len(tools))
	for _, t := range tools {
		out = append(out, ai.ToolDef{
			Name:         t.Name,
			Description:  t.Description,
			InputSchema:  t.Parameters,
		})
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
```

**Step 2:** Create `genkit/convert_test.go`:
```go
package genkit

import (
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

func TestToGenkitMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are helpful."},
		{Role: provider.RoleUser, Content: "Hello"},
		{Role: provider.RoleAssistant, Content: "Hi there"},
	}
	result := toGenkitMessages(msgs)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[0].Role != ai.RoleSystem {
		t.Errorf("expected system role, got %s", result[0].Role)
	}
	if result[2].Role != ai.RoleModel {
		t.Errorf("expected model role for assistant, got %s", result[2].Role)
	}
}

func TestFromGenkitResponse(t *testing.T) {
	resp := &ai.ModelResponse{
		Message: ai.NewModelTextMessage("Hello world"),
		Usage:   &ai.GenerationUsage{InputTokens: 10, OutputTokens: 5},
	}
	result := fromGenkitResponse(resp)
	if result.Content != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", result.Content)
	}
	if result.Usage.InputTokens != 10 {
		t.Errorf("expected 10 input tokens, got %d", result.Usage.InputTokens)
	}
}

func TestFromGenkitResponseNil(t *testing.T) {
	result := fromGenkitResponse(nil)
	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
}
```

**Step 3:** Run tests: `go test ./genkit/ -v -count=1`

**Step 4:** Commit:
```bash
git add genkit/convert.go genkit/convert_test.go
git commit -m "feat: add Genkit ↔ provider type conversion layer"
```

---

## Task 3: Implement the Genkit provider adapter

**Files:**
- Create: `genkit/adapter.go`
- Create: `genkit/adapter_test.go`

**Step 1:** Create `genkit/adapter.go` — the `genkitProvider` struct that implements `provider.Provider`:
```go
package genkit

import (
	"context"
	"fmt"

	"github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// genkitProvider adapts a Genkit model to provider.Provider.
type genkitProvider struct {
	g         *gk.Genkit
	modelName string // "provider/model" format
	name      string
	authInfo  provider.AuthModeInfo
}

func (p *genkitProvider) Name() string                    { return p.name }
func (p *genkitProvider) AuthModeInfo() provider.AuthModeInfo { return p.authInfo }

func (p *genkitProvider) Chat(ctx context.Context, messages []provider.Message, tools []provider.ToolDef) (*provider.Response, error) {
	opts := []ai.GenerateOption{
		ai.WithModelName(p.modelName),
		ai.WithMessages(toGenkitMessages(messages)...),
	}

	// Pass tool definitions if provided — use WithReturnToolRequests so we handle tool
	// execution ourselves (the executor loop does this, not Genkit).
	if len(tools) > 0 {
		opts = append(opts, ai.WithReturnToolRequests(true))
		// Register tools dynamically for this call
		for _, t := range tools {
			tool := gk.DefineTool(p.g, t.Name, t.Description,
				func(ctx *ai.ToolContext, input map[string]any) (map[string]any, error) {
					// Placeholder — tools are executed by the executor, not here.
					return nil, fmt.Errorf("tool %s should not be called via Genkit", t.Name)
				},
			)
			opts = append(opts, ai.WithTools(tool))
		}
	}

	resp, err := gk.Generate(ctx, p.g, opts...)
	if err != nil {
		return nil, fmt.Errorf("genkit generate: %w", err)
	}

	return fromGenkitResponse(resp), nil
}

func (p *genkitProvider) Stream(ctx context.Context, messages []provider.Message, tools []provider.ToolDef) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 64)

	opts := []ai.GenerateOption{
		ai.WithModelName(p.modelName),
		ai.WithMessages(toGenkitMessages(messages)...),
	}

	if len(tools) > 0 {
		opts = append(opts, ai.WithReturnToolRequests(true))
		for _, t := range tools {
			tool := gk.DefineTool(p.g, t.Name, t.Description,
				func(ctx *ai.ToolContext, input map[string]any) (map[string]any, error) {
					return nil, fmt.Errorf("tool %s should not be called via Genkit", t.Name)
				},
			)
			opts = append(opts, ai.WithTools(tool))
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
				// Extract final response for tool calls and usage
				if result.Response != nil {
					final := fromGenkitResponse(result.Response)
					for _, tc := range final.ToolCalls {
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
				if ev.Type != "" && (ev.Text != "" || ev.Tool != nil) {
					ch <- ev
				}
			}
		}
		// Iterator exhausted without Done — send done anyway
		ch <- provider.StreamEvent{Type: "done"}
	}()

	return ch, nil
}
```

**Step 2:** Create `genkit/adapter_test.go` with mock model tests. Tests should verify Chat/Stream call paths and conversion.

**Step 3:** Run tests: `go test ./genkit/ -v -count=1`

**Step 4:** Commit:
```bash
git add genkit/adapter.go genkit/adapter_test.go
git commit -m "feat: implement Genkit provider adapter (provider.Provider interface)"
```

---

## Task 4: Implement provider factory functions

**Files:**
- Create: `genkit/providers.go`
- Create: `genkit/providers_test.go`

**Step 1:** Create `genkit/providers.go` with factory functions for each provider type. Each factory:
1. Initializes the appropriate Genkit plugin
2. Creates a `genkitProvider` with the correct model name format
3. Returns `provider.Provider`

Factory functions needed (one per ProviderRegistry factory):
- `NewAnthropicProvider(apiKey, model, baseURL string, maxTokens int) provider.Provider`
- `NewOpenAIProvider(apiKey, model, baseURL string, maxTokens int) provider.Provider`
- `NewGoogleAIProvider(apiKey, model string, maxTokens int) (provider.Provider, error)`
- `NewOllamaProvider(model, baseURL string, maxTokens int) provider.Provider`
- `NewOpenAICompatibleProvider(name, apiKey, model, baseURL string, maxTokens int) provider.Provider` — for OpenRouter, Copilot, Cohere, HuggingFace
- `NewBedrockProvider(region, model, accessKeyID, secretAccessKey, sessionToken string, maxTokens int) provider.Provider` — via community plugin or OpenAI-compatible
- `NewVertexAIProvider(projectID, region, model, credentialsJSON string, maxTokens int) (provider.Provider, error)`
- `NewAzureOpenAIProvider(resource, deploymentName, apiVersion, apiKey string, maxTokens int) provider.Provider` — via community plugin or OpenAI-compatible
- `NewAnthropicFoundryProvider(resource, model, apiKey, entraToken string, maxTokens int) provider.Provider`

Each factory calls `Instance(ctx)` to get the shared Genkit instance, registers the plugin if not already registered, and returns a `genkitProvider`.

**Step 2:** Create `genkit/providers_test.go` — test factory instantiation (not live API calls).

**Step 3:** Build: `go build ./...`

**Step 4:** Commit:
```bash
git add genkit/providers.go genkit/providers_test.go
git commit -m "feat: add Genkit provider factory functions for all provider types"
```

---

## Task 5: Update ProviderRegistry to use Genkit factories

**Files:**
- Modify: `orchestrator/provider_registry.go`

**Step 1:** Update all factory functions in `provider_registry.go` to call `genkit.New*Provider()` instead of `provider.New*Provider()`:

```go
import gkprov "github.com/GoCodeAlone/workflow-plugin-agent/genkit"

func anthropicProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	return gkprov.NewAnthropicProvider(apiKey, cfg.Model, cfg.BaseURL, cfg.MaxTokens), nil
}

func openaiProviderFactory(apiKey string, cfg LLMProviderConfig) (provider.Provider, error) {
	return gkprov.NewOpenAIProvider(apiKey, cfg.Model, cfg.BaseURL, cfg.MaxTokens), nil
}
// ... repeat for all 13 factory functions
```

**Step 2:** Build: `go build ./...`

**Step 3:** Run registry tests: `go test ./orchestrator/ -run TestProviderRegistry -v -count=1`

**Step 4:** Run full suite: `go test ./... -count=1`

**Step 5:** Commit:
```bash
git add orchestrator/provider_registry.go
git commit -m "feat: update ProviderRegistry to use Genkit-backed factories"
```

---

## Task 6: Delete old provider implementation files

**Files:**
- Delete: All provider implementation files listed in design doc

**Step 1:** Delete the old provider files (keep `provider.go`, `models.go`, `auth_modes.go`, and all `test_provider*.go` files):
```bash
cd /Users/jon/workspace/workflow-plugin-agent
# Delete implementation files
rm provider/anthropic.go provider/anthropic_bedrock.go provider/anthropic_foundry.go provider/anthropic_vertex.go
rm provider/anthropic_convert.go provider/anthropic_convert_test.go
rm provider/openai.go provider/openai_azure.go provider/openai_azure_test.go
rm provider/gemini.go provider/gemini_test.go
rm provider/ollama.go provider/ollama_convert.go provider/ollama_test.go provider/ollama_convert_test.go
rm provider/copilot.go provider/copilot_test.go provider/copilot_models.go provider/copilot_models_test.go
rm provider/openrouter.go provider/openrouter_test.go
rm provider/cohere.go
rm provider/huggingface.go provider/huggingface_test.go
rm provider/llama_cpp.go provider/llama_cpp_test.go provider/llama_cpp_download.go provider/llama_cpp_download_test.go
rm provider/local.go provider/local_test.go
rm provider/ssrf.go provider/models_ssrf_test.go
```

**Step 2:** Fix any compilation errors from dangling references. Key areas to check:
- `provider/models.go` — may reference deleted types
- `provider/auth_modes.go` — may reference deleted types
- `provider/thinking_field_test.go` — may import deleted code
- Any file in `orchestrator/` that directly imports deleted provider constructors

**Step 3:** Build: `go build ./...`

**Step 4:** Run tests: `go test ./... -count=1`

**Step 5:** Commit:
```bash
git add -u  # stages deletions
git commit -m "refactor: delete hand-rolled provider implementations (replaced by Genkit)"
```

---

## Task 7: Clean up dependencies and fix remaining issues

**Files:**
- Modify: `go.mod`
- Modify: any files with compilation errors

**Step 1:** Remove direct SDK dependencies that are now transitive via Genkit:
```bash
go mod tidy
```

Check if `github.com/anthropics/anthropic-sdk-go`, `github.com/openai/openai-go`, `github.com/google/generative-ai-go`, `github.com/ollama/ollama` are still needed directly. If only Genkit imports them, they'll move to `indirect`.

**Step 2:** Fix any remaining compile errors or test failures.

**Step 3:** Run full test suite with race detector:
```bash
go test -race ./... -count=1
```

**Step 4:** Run linter:
```bash
golangci-lint run
```

**Step 5:** Commit:
```bash
git add go.mod go.sum
git commit -m "chore: clean up dependencies after Genkit migration"
```

---

## Task 8: Write integration tests and verify

**Files:**
- Create: `genkit/integration_test.go`

**Step 1:** Create integration tests that verify the full path: `ProviderRegistry → genkit factory → genkitProvider → Genkit → mock model`. These tests use in-memory DB + mock secrets like existing registry tests.

**Step 2:** Verify existing executor tests pass (they use `provider.Provider` interface which is unchanged):
```bash
go test ./executor/ -v -count=1
```

**Step 3:** Verify orchestrator tests:
```bash
go test ./orchestrator/ -v -count=1
```

**Step 4:** Full regression:
```bash
go test -race ./... -count=1
golangci-lint run
```

**Step 5:** Commit:
```bash
git add genkit/integration_test.go
git commit -m "test: add Genkit integration tests and verify full regression"
```

---

## Execution Order

```
Task 1 (deps + skeleton) → Task 2 (convert) → Task 3 (adapter) → Task 4 (factories)
                                                                        ↓
Task 5 (registry update) → Task 6 (delete old files) → Task 7 (cleanup) → Task 8 (tests)
```

All tasks are sequential — each builds on the previous.
