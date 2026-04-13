# Provider-Aware Context Management — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Prevent OOM in agentic loops by adding provider-aware context management, tool filtering, sensible compaction defaults, and Ollama memory control.

**Architecture:** Four independent changes to the agent plugin: (1) ContextStrategy interface for provider-managed sessions, (2) tool definition filtering against guardrails before Chat(), (3) compaction default 0.0→0.80, (4) context_window config for Ollama num_ctx.

**Tech Stack:** Go, workflow-plugin-agent, Ollama API, provider/provider.go interfaces.

---

## Task 1: ContextStrategy Interface

**Files:**
- Create: `provider/context_strategy.go`
- Create: `provider/context_strategy_test.go`

**Step 1: Write the interface and tests**

```go
// provider/context_strategy.go
package provider

import "context"

// ContextStrategy is an optional interface that providers can implement to
// declare they manage conversation context server-side. When implemented,
// the executor sends only new messages since the last call instead of the
// full history.
type ContextStrategy interface {
    // ManagesContext returns true if the provider maintains conversation
    // state between Chat() calls.
    ManagesContext() bool

    // ResetContext clears any accumulated server-side state.
    // Called when the executor compacts context or starts a new session.
    ResetContext(ctx context.Context) error
}
```

```go
// provider/context_strategy_test.go
package provider

import (
    "context"
    "testing"
)

type mockStatefulProvider struct {
    resetCalled bool
}

func (m *mockStatefulProvider) Name() string { return "mock-stateful" }
func (m *mockStatefulProvider) Chat(_ context.Context, _ []Message, _ []ToolDef) (*Response, error) {
    return &Response{Content: "ok"}, nil
}
func (m *mockStatefulProvider) Stream(_ context.Context, _ []Message, _ []ToolDef) (<-chan StreamEvent, error) {
    return nil, nil
}
func (m *mockStatefulProvider) AuthModeInfo() AuthModeInfo { return AuthModeInfo{} }
func (m *mockStatefulProvider) ManagesContext() bool        { return true }
func (m *mockStatefulProvider) ResetContext(_ context.Context) error {
    m.resetCalled = true
    return nil
}

func TestContextStrategy_Detection(t *testing.T) {
    var p Provider = &mockStatefulProvider{}
    cs, ok := p.(ContextStrategy)
    if !ok {
        t.Fatal("expected provider to implement ContextStrategy")
    }
    if !cs.ManagesContext() {
        t.Error("expected ManagesContext=true")
    }
}

func TestContextStrategy_NotImplemented(t *testing.T) {
    // A basic provider that doesn't implement ContextStrategy
    var p Provider = &mockProvider{responses: []string{"hi"}}
    _, ok := p.(ContextStrategy)
    if ok {
        t.Error("mockProvider should not implement ContextStrategy")
    }
}
```

**Step 2: Run tests**

Run: `cd /Users/jon/workspace/workflow-plugin-agent && go test ./provider/ -run TestContextStrategy -v`
Expected: PASS

**Step 3: Wire into executor**

Modify `orchestrator/step_agent_execute.go` — after provider resolution (~line 112), detect ContextStrategy:

```go
// After aiProvider is resolved:
var contextStrategy provider.ContextStrategy
if cs, ok := aiProvider.(provider.ContextStrategy); ok && cs.ManagesContext() {
    contextStrategy = cs
}
```

In the iteration loop (~line 280), when sending messages:

```go
var chatMessages []provider.Message
if contextStrategy != nil && iterCount > 0 {
    // Provider manages context — send only new messages since last call
    chatMessages = messages[lastSentIndex:]
} else {
    chatMessages = messages
}
resp, err := aiProvider.Chat(ctx, chatMessages, filteredToolDefs)
if contextStrategy != nil {
    lastSentIndex = len(messages)
}
```

On compaction, reset provider state:

```go
if cm.NeedsCompaction(messages) {
    if contextStrategy != nil {
        _ = contextStrategy.ResetContext(ctx)
    }
    // ... existing compaction logic ...
    if contextStrategy != nil {
        lastSentIndex = 0 // resend full compacted history
    }
}
```

**Step 4: Write integration test**

Create `orchestrator/context_strategy_test.go`:
- Test that a stateful provider receives only new messages after first iteration
- Test that compaction resets context and resends full compacted history
- Test that a stateless provider (no ContextStrategy) receives full history every time

**Step 5: Commit**

```bash
git add provider/context_strategy.go provider/context_strategy_test.go \
    orchestrator/step_agent_execute.go orchestrator/context_strategy_test.go
git commit -m "feat(provider): add ContextStrategy interface for provider-managed sessions

Providers that maintain server-side context can implement ContextStrategy
to receive only new messages per call instead of full history resend."
```

---

## Task 2: Tool Filtering at Send Time

**Files:**
- Modify: `orchestrator/guardrails.go` (add FilterTools method)
- Create: `orchestrator/guardrails_filter_test.go`
- Modify: `orchestrator/step_agent_execute.go` (filter before Chat)

**Step 1: Write failing test**

```go
// orchestrator/guardrails_filter_test.go
func TestGuardrails_FilterTools(t *testing.T) {
    g := &GuardrailsModule{}
    g.config = &GuardrailsConfig{
        Defaults: GuardrailsDefaults{
            AllowedTools: []string{"file_read", "file_write", "mcp_wfctl__*"},
        },
    }

    allTools := []provider.ToolDef{
        {Name: "file_read"},
        {Name: "file_write"},
        {Name: "shell_exec"},
        {Name: "mcp_wfctl__validate_config"},
        {Name: "mcp_wfctl__inspect_config"},
        {Name: "git_commit"},
        {Name: "google_search"},
    }

    filtered := g.FilterTools(allTools)
    names := make([]string, len(filtered))
    for i, t := range filtered {
        names[i] = t.Name
    }

    // Should include: file_read, file_write, mcp_wfctl__validate_config, mcp_wfctl__inspect_config
    // Should exclude: shell_exec, git_commit, google_search
    if len(filtered) != 4 {
        t.Errorf("expected 4 tools, got %d: %v", len(filtered), names)
    }
}
```

**Step 2: Implement FilterTools**

```go
// In orchestrator/guardrails.go
func (g *GuardrailsModule) FilterTools(tools []provider.ToolDef) []provider.ToolDef {
    if g == nil || g.config == nil {
        return tools // no guardrails = pass all
    }
    patterns := g.config.Defaults.AllowedTools
    if len(patterns) == 0 {
        return tools // no allowlist = pass all
    }
    var filtered []provider.ToolDef
    for _, t := range tools {
        for _, pattern := range patterns {
            if matchPattern(pattern, t.Name) {
                filtered = append(filtered, t)
                break
            }
        }
    }
    return filtered
}
```

**Step 3: Wire into executor**

In `orchestrator/step_agent_execute.go`, before the Chat() call (~line 270):

```go
// Filter tool definitions to only those allowed by guardrails
filteredToolDefs := toolDefs
if guardrailsSvc, ok := s.app.SvcRegistry()["agent-guardrails"]; ok {
    if gm, ok := guardrailsSvc.(*GuardrailsModule); ok {
        filteredToolDefs = gm.FilterTools(toolDefs)
    }
}
```

**Step 4: Run tests, commit**

```bash
git add orchestrator/guardrails.go orchestrator/guardrails_filter_test.go \
    orchestrator/step_agent_execute.go
git commit -m "feat(orchestrator): filter tool definitions against guardrails before Chat

Only send tool definitions matching allowed_tools to the LLM.
Reduces token usage and prevents hallucinated calls to unauthorized tools."
```

---

## Task 3: Compaction Default → 0.80 + Dynamic Model Limits

**Files:**
- Modify: `orchestrator/step_agent_execute.go` (~line 849, change default)
- Modify: `orchestrator/context_manager.go` (add gemma4, dynamic detection)
- Modify: `orchestrator/context_manager_test.go`

**Step 1: Change default**

In `orchestrator/step_agent_execute.go`, factory function:

```go
// Before:
compactionThreshold := 0.0

// After:
compactionThreshold := 0.80
```

**Step 2: Add modern model limits to ContextManager**

In `orchestrator/context_manager.go`, add to the model limits map:

```go
// Add to modelContextLimits:
"gemma4":    131072,
"gemma3":    131072,
"qwen2.5":   32768,
"qwen3":    131072,
"phi4":      16384,
"llama3.3":  131072,
```

**Step 3: Add SetModelLimit from provider query**

Add method to ContextManager:

```go
// SetModelLimitFromProvider queries the provider for context window size
// and updates the limit. Falls back to the hardcoded list if unavailable.
func (cm *ContextManager) SetModelLimitFromProvider(limit int) {
    if limit > 0 {
        cm.contextLimit = limit
    }
}
```

**Step 4: Write test for new default**

```go
func TestCompactionDefault(t *testing.T) {
    // Create step factory with no explicit threshold
    factory := newAgentExecuteStepFactory()
    step, err := factory("test", map[string]any{}, mockApp)
    // Verify the step has compactionThreshold == 0.80
}
```

**Step 5: Commit**

```bash
git add orchestrator/step_agent_execute.go orchestrator/context_manager.go \
    orchestrator/context_manager_test.go
git commit -m "feat(orchestrator): enable context compaction by default at 80%

Change compactionThreshold default from 0.0 (disabled) to 0.80.
Add modern model context limits (gemma4, qwen3, llama3.3).
Add SetModelLimitFromProvider for dynamic limit detection."
```

---

## Task 4: Ollama context_window Config (num_ctx)

**Files:**
- Modify: `module_provider.go` (parse context_window, pass to provider)
- Modify: `genkit/providers.go` (accept contextWindow param, set Ollama options)
- Create: `genkit/providers_context_test.go`

**Step 1: Add context_window to provider config parsing**

In `module_provider.go`, in the factory function:

```go
contextWindow := 0
switch v := cfg["context_window"].(type) {
case int:
    contextWindow = v
case float64:
    contextWindow = int(v)
}
```

Pass to `NewOllamaProvider`:

```go
case "ollama":
    if prov, err := gkprov.NewOllamaProvider(context.TODO(), model, baseURL, maxTokens, contextWindow); err != nil {
```

**Step 2: Update NewOllamaProvider signature**

In `genkit/providers.go`:

```go
func NewOllamaProvider(ctx context.Context, model, serverAddress string, maxTokens, contextWindow int) (provider.Provider, error) {
```

Store `contextWindow` on the genkitProvider struct. Pass as `num_ctx` in the Ollama generation config:

```go
if contextWindow > 0 {
    ollamaCfg.NumCtx = &contextWindow
}
```

Also implement a method to return the context window for ContextManager:

```go
func (p *genkitProvider) ContextWindow() int {
    return p.contextWindow
}
```

**Step 3: Wire context window into ContextManager**

In `orchestrator/step_agent_execute.go`, after provider is resolved:

```go
// Set context limit from provider if available
if cw, ok := aiProvider.(interface{ ContextWindow() int }); ok {
    if w := cw.ContextWindow(); w > 0 {
        cm.SetModelLimitFromProvider(w)
    }
}
```

**Step 4: Write test**

```go
func TestOllamaProvider_ContextWindow(t *testing.T) {
    // Create provider with context_window=8192
    // Verify ContextWindow() returns 8192
    // Verify num_ctx is passed in Ollama config
}
```

**Step 5: Commit**

```bash
git add module_provider.go genkit/providers.go genkit/providers_context_test.go \
    orchestrator/step_agent_execute.go
git commit -m "feat(provider): add context_window config for Ollama num_ctx control

Limits Ollama KV cache memory allocation. When set, also updates the
ContextManager's token limit for accurate compaction triggering."
```

---

## Task 5: Update Scenarios + Re-test

**Files:**
- Modify: scenario 85/86/87 agent configs (add compaction + context_window)
- Re-run scenario 85 with gemma4:e2b to verify no OOM

**Step 1: Update configs**

Add to each scenario's step.agent_execute config:

```yaml
context:
  compaction_threshold: 0.80
```

Add to agent.provider module:

```yaml
context_window: 16384
```

**Step 2: Re-run scenario 85**

Build, start, trigger POST /improve, verify:
- Agent runs multiple iterations without OOM
- Compaction triggers (visible in logs)
- Tool filtering reduces tool count in Chat calls

**Step 3: Commit**

```bash
git add scenarios/85-*/config/ scenarios/86-*/config/ scenarios/87-*/config/
git commit -m "fix(scenarios): add compaction threshold and context_window to agent configs"
```

---

## Task 6: Update Agentic Loop Guide

**Files:**
- Modify: `docs/agentic-loop-guide.md`

Add section on context management:
- Compaction threshold (default 0.80, how it works)
- context_window for Ollama memory control
- Tool filtering (automatic when guardrails configured)
- ContextStrategy for advanced providers

**Commit:**
```bash
git commit -m "docs: add context management section to agentic loop guide"
```
