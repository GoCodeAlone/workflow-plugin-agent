# Self-Improvement Loop Integration Fixes — Design

**Date:** 2026-04-13
**Status:** Approved
**Repos:** `workflow-plugin-agent`, `workflow-scenarios`

## Problem

The self-improvement pipeline executes but accomplishes nothing. Eight integration gaps prevent the loop from actually improving the application.

## Fixes

### 1. MCP Tools Wiring Hook (agent plugin)

New wiring hook `ratchet.mcp_tools` that bridges MCP tools into the ToolRegistry:

- Discovers `mcp.provider` (in-process wfctl) from service registry
- Creates `InProcessServer` as fallback if no provider registered
- Discovers external MCP servers via `ratchet.mcp_client` module
- Wraps each tool as `MCPToolAdapter` implementing `plugin.Tool`
- Tool names: `mcp_{server}_{tool}` (e.g., `mcp_wfctl_validate_config`)
- Registers all tools in the ToolRegistry via `RegisterMCP()`
- Supports 3rd-party MCP servers configured in YAML

### 2. Validate Empty Guard (agent plugin)

`step_self_improve_validate.go` rejects empty/`<no value>` proposed_yaml with an error instead of passing silently.

### 3. Agent-as-Loop Architecture (scenario configs)

The agent step IS the iteration loop. It uses tools internally:
- `file_read` to read current config
- `mcp_wfctl_*` tools to inspect, validate, get schemas
- `file_write` to write improved config
- `git_commit` to track changes

Pipeline steps after the agent are the deployment gate (validate → deploy).

### 4. System Prompt with Config Context + Quirks

System prompt includes:
- Instructions to read config via file_read tool
- The config field quirks (address not port, database not module, etc.)
- Explicit step-by-step instructions

### 5. Output Key Fix

Template uses `{{ .steps.designer.result }}` (not `content`).

### 6. Real Deploy

Deploy step writes config file. For hot_reload, the scenario uses a single server process that handles both the API and the agent — config changes take effect on next request.

### 7. Verification via Agent Tools

Agent uses `web_fetch` tool to hit `/healthz` and new endpoints after deploy.

### 8. Git Tracking via Agent Tools

Agent uses `git_commit` tool to commit changes after writing config.

## Testing

Run all 3 scenarios (85, 86, 87) with real Ollama + Gemma 4, verifying:
- Agent uses MCP tools (visible in logs as tool_call events)
- Config is actually modified on disk
- New endpoints respond
- Git history shows iterations
