# Provider-Aware Context Management — Design

**Date:** 2026-04-13
**Status:** Approved
**Repo:** `workflow-plugin-agent`

## Problem

The agent executor resends the entire conversation history on every LLM call. With 44+ tool definitions and growing conversation (tool results include full YAML files), context grows rapidly. On 16GB machines with Gemma 4 (7.2GB model), the KV cache exhausts memory by iteration 2. Compaction exists but defaults to disabled (threshold 0.0).

## Changes

### 1. ContextStrategy Interface

Optional interface for providers that maintain server-side conversation state:

```go
type ContextStrategy interface {
    ManagesContext() bool
    ResetContext(ctx context.Context) error
}
```

Executor checks if provider implements this. If `ManagesContext()` is true, sends only new messages since last call. On compaction, calls `ResetContext()` and resends compacted summary. No breaking change — default behavior unchanged for providers that don't implement it.

### 2. Tool Filtering at Send Time

Before each `Chat()` call, filter `toolDefs` against guardrails `allowed_tools` patterns. Only send matching tool definitions to the LLM. Reduces token overhead from ~44 tools to ~8, and prevents hallucinated calls to unauthorized tools.

Implementation: `GuardrailsModule.FilterTools(defs, scopeContext)` returns filtered slice.

### 3. Compaction Default → 0.80

Change default `compactionThreshold` from `0.0` to `0.80`. Update ContextManager model limits to include modern models (gemma4: 131K). Add dynamic limit detection via Ollama `/api/show` `context_length` field.

### 4. Ollama context_window Config

Add `context_window` field to `agent.provider` config. Passed as `num_ctx` to Ollama API, limiting KV cache memory allocation. Also sets the ContextManager's token limit to match.

```yaml
- name: ratchet-ai
  type: agent.provider
  config:
    provider: ollama
    model: gemma4:e2b
    context_window: 16384
```

## Testing

- Unit tests for ContextStrategy detection and message delta sending
- Unit tests for tool filtering against guardrails patterns
- Integration test: verify compaction triggers at 80% with gemma4 limit
- Scenario 85 re-run with all fixes: verify no OOM on multi-iteration
