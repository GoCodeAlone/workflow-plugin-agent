# Agent Loop Robustness — Design

**Date:** 2026-04-13
**Status:** Approved
**Repo:** `workflow-plugin-agent`

## Problem

Three systemic LLM behavior issues prevent agents from completing tool-based workflows:

1. **Parallel tool calls** — LLMs return multiple tool calls per turn even when sequential is configured. Results from call A aren't available when call B executes, causing placeholder args and broken chains.
2. **Overwhelming responses** — `list_step_types` returns 182 items. Models return empty responses after receiving large payloads, terminating the loop prematurely.
3. **Empty/verbalized responses** — Model says "now let's write" as text without calling the tool, or returns completely empty. Loop terminates.

## Fixes

### 1. Sequential Mode: Error on Multiple Tool Calls

When `parallel_tool_calls: false`, if the LLM returns >1 tool call, do NOT execute any. Instead, return an error message to the LLM:

```
[SYSTEM] You sent 3 tool calls in one turn, but this agent is configured for
sequential execution. Please call ONE tool at a time and wait for its result
before calling the next. Your first intended call was "file_read" — please
resend it as the only tool call.
```

This teaches the model the constraint. The model gets feedback and retries with a single call. No tool executes until the model complies.

Implementation: In the tool execution section of `step_agent_execute.go`, when `!s.parallelToolCalls && len(resp.ToolCalls) > 1`:
- Don't execute any tool calls
- Append the assistant message (with its tool calls) to history
- Append a system/user message with the error + instruction
- Continue the loop (don't count this as an iteration toward max_iterations)

### 2. Required Search/Filter for Large Tool Lists

Replace raw `mcp_wfctl__list_step_types` with a filtered version. The MCP tool should require a `filter` or `query` parameter:

```json
{"name": "mcp_wfctl__list_step_types", "args": {"query": "db"}}
→ Returns: ["step.db_query", "step.db_exec", "step.db_migrate"]
```

When called with no filter, return a summary instead of the full list:
```
182 step types available. Use the "query" parameter to filter.
Categories: database (5), http (8), json (3), conditional (2), set (1), ...
Example: {"query": "db"} returns database-related steps.
```

This keeps the tool useful while preventing context overwhelm.

Implementation: In the MCP tool adapter or the wfctl MCP server, add filter support to the `list_step_types` and `list_module_types` tools.

### 3. Adaptive Response Pagination with Cache

For ANY tool response exceeding a configurable token threshold (based on the model's context window), automatically paginate:

```
[Page 1 of 4 — showing items 1-25 of 98]
step.db_query: Query a database with SQL
step.db_exec: Execute a SQL statement
...
step.set: Set values in pipeline context

[To see the next page, call this tool again with {"page": 2}]
[To search within results, call with {"query": "keyword"}]
```

**Cache mechanism:**
- First call executes the tool and stores the full result in a per-agent response cache (keyed by tool name + args hash)
- Subsequent calls with `{"page": N}` read from cache
- Cache expires after the agent loop completes or after N minutes
- Page size adapts to model context window: `page_size = (context_window * 0.1) / avg_chars_per_item`

Implementation: Add a `ResponsePaginator` to the ToolRegistry that wraps tool execution:
- Before returning a result, check token estimate
- If over threshold, cache full result, return first page + pagination instructions
- On subsequent calls with `page` param, return from cache

### 4. Empty Response Continuation

When the LLM returns empty (no text, no tool calls) or verbalizes intent without a tool call:

**Empty response:** Inject a continuation prompt and retry (up to 2 retries):
```
[SYSTEM] Your response was empty. Please continue with the next step.
If you're done, respond with "TASK COMPLETE" and a summary of what you accomplished.
```

**Verbalized intent:** Detect patterns like "let's write", "now I'll call", "I will use file_write" without an actual tool call. Inject:
```
[SYSTEM] You described your intent to call a tool but didn't actually call it.
Please make the tool call. For example, to write a file, call file_write with
{"path": "...", "content": "..."}.
```

Implementation: In the agent loop, after parsing the response:
- If `len(resp.ToolCalls) == 0 && resp.Content == ""`: inject empty continuation
- If `len(resp.ToolCalls) == 0 && containsToolIntent(resp.Content)`: inject verbalized intent prompt
- Track continuation retries — max 2 before terminating

`containsToolIntent()` checks for patterns: "let's call", "I'll use", "now I will", "let me call", tool names mentioned in prose without tool_call structure.

## Testing

- Sequential mode: mock provider returns 3 tool calls → verify error message returned, no tools execute
- Pagination: tool returns 200 items → verify first page returned with instructions, page 2 from cache
- Empty continuation: mock returns empty → verify retry prompt injected, max 2 retries
- Verbalized intent: mock returns "I'll call file_write now" → verify intent detection prompt
- Filter on list_step_types: verify {"query": "db"} returns only db-related steps
