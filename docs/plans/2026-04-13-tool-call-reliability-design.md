# Agent Tool Call Reliability — Design

**Date:** 2026-04-13
**Status:** Approved
**Repo:** `workflow-plugin-agent`

## Problem

LLM agents making tool calls exhibit 5 failure modes discovered during scenario 85 execution:
1. Path confusion — LLM ignores exact paths, uses guessed/shortened paths
2. Tool name hallucination — LLM invents names (file_manager:read instead of file_read)
3. Parallel calls without dependency — calls file_read + validate simultaneously
4. Literal placeholders — writes `<improved yaml>` instead of actual content
5. No learning from errors — agent repeats the same mistake

## Fixes

### 1. Sequential Tool Execution Config
`parallel_tool_calls: false` on step.agent_execute. When false, execute only the first tool call per LLM response, return result, let LLM decide next. Prevents dependency issues.

### 2. Helpful Error Messages with Fuzzy Suggestions
On unknown tool: fuzzy match against registry, suggest closest + show schema.
On path error: include workspace path and example of correct path.
On wrong args: show required parameters.
Do NOT auto-correct — let the agent learn from errors.

### 3. Placeholder Detection
Before file_write execution, check content for placeholder patterns (<...>, ${...}, TODO). Return descriptive error telling agent to provide real content.

### 4. Improved Prompts
Structured tool tables, exact example calls, "call ONE tool at a time" emphasis.
