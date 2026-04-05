# Executor Mesh Support Design

**Date:** 2026-04-04
**Status:** Proposed
**Repo:** workflow-plugin-agent
**Downstream:** ratchet-cli agent mesh (see `ratchet-cli/docs/plans/2026-04-04-agent-mesh-design.md`)

## Problem

The current `executor.Execute()` is a closed synchronous loop. It works for single-agent task execution but lacks three capabilities required by ratchet-cli's agent mesh:

1. **No message injection** — When Agent B sends a message to Agent A via the mesh, there's no way to push that message into A's running conversation. The loop only appends tool results; external input can't arrive mid-loop.

2. **No event streaming** — The executor returns a final `Result` but emits no real-time events. The mesh (and TUI) need to observe tool calls, thinking blocks, token deltas, and tool results as they happen.

3. **No custom termination** — The loop exits when the LLM stops making tool calls or max iterations is reached. Mesh agents need to signal "done" explicitly (e.g. by writing to the blackboard status section), which the executor can't detect.

## Design

Extend `executor.Config` with three optional fields. All are backward-compatible — existing callers that don't set them get identical behavior to today.

### 1. Inbox Channel

```go
// Config additions:
type Config struct {
    // ... existing fields ...

    // Inbox receives external messages injected into the conversation
    // between loop iterations. Nil means no external messages.
    Inbox <-chan provider.Message
}
```

**Behavior:** At the top of each loop iteration (after compaction check, before the `provider.Chat()` call), drain all pending messages from `Inbox` and append them to the conversation. This is non-blocking — if no messages are waiting, the loop proceeds immediately.

This lets the mesh push messages from other agents into a running agent's conversation as user-role messages.

### 2. Event Callback

```go
// EventType identifies what happened in the executor loop.
type EventType string

const (
    EventToolCallStart  EventType = "tool_call_start"
    EventToolCallResult EventType = "tool_call_result"
    EventThinking       EventType = "thinking"
    EventText           EventType = "text"
    EventIteration      EventType = "iteration"
    EventCompleted      EventType = "completed"
    EventFailed         EventType = "failed"
)

// Event is emitted during execution for real-time observation.
type Event struct {
    Type       EventType
    AgentID    string
    Iteration  int
    Content    string            // text content or thinking trace
    ToolName   string            // for tool_call_start/result
    ToolCallID string            // for tool_call_start/result
    ToolArgs   map[string]any    // for tool_call_start
    ToolResult string            // for tool_call_result
    ToolError  bool              // for tool_call_result
    Error      string            // for failed events
}

// Config additions:
type Config struct {
    // ... existing fields ...

    // OnEvent is called for each executor event. Nil means no events emitted.
    // The callback must not block — use a buffered channel internally if needed.
    OnEvent func(Event)
}
```

**Behavior:** Emit events at each point in the loop: iteration start, after LLM response (thinking + text), before/after each tool call, and on completion/failure. The callback is synchronous and must not block — the mesh wraps it in a channel send internally.

### 3. Custom Termination

```go
// Config additions:
type Config struct {
    // ... existing fields ...

    // ShouldStop is called after each tool execution round. If it returns
    // a non-empty string, the loop exits with status "completed" and that
    // string as the Result.Content. Nil means no custom termination.
    ShouldStop func() (reason string)
}
```

**Behavior:** After all tool calls in an iteration are processed (but before the next `provider.Chat()` call), call `ShouldStop()`. If it returns a non-empty string, exit the loop with status "completed". The mesh sets this to check the blackboard status section for a "done" or "approved" entry written by the current agent.

## Integration Points

The mesh uses these three features together:

```
LocalNode.Run():
  1. Create tools.Registry with blackboard + messaging tools
  2. Create a buffered inbox channel
  3. Set OnEvent to forward events to the mesh's TeamEvent stream
  4. Set ShouldStop to check blackboard["status"][myNodeID] == "done"
  5. Call executor.Execute(ctx, cfg, systemPrompt, task, nodeID)
  6. Mesh routes incoming messages to the inbox channel
```

## Scope

**In scope:**
- Three new optional fields on `executor.Config`
- Inbox drain logic in the main loop
- Event emission at each loop point
- ShouldStop check after tool execution
- Unit tests for each new feature
- Backward compatibility: zero-value Config behaves identically to today

**Out of scope:**
- Streaming provider support in the executor (already available via `provider.Stream()`, used separately)
- Changes to the Provider interface
- Changes to the Tool interface
