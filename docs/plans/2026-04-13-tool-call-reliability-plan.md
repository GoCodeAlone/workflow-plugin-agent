# Agent Tool Call Reliability — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix 4 LLM agent tool call reliability issues and execute all 3 scenarios end-to-end with real outcomes.

**Architecture:** Changes to executor tool call loop, ToolRegistry error handling, file tool arg validation, and scenario prompt configs.

**Tech Stack:** Go, workflow-plugin-agent, Ollama + Gemma 4 / qwen2.5

---

## Task 1: Sequential Tool Execution Config

**Files:**
- Modify: `orchestrator/step_agent_execute.go` (factory + execution loop)
- Create: `orchestrator/step_agent_execute_sequential_test.go`

**Implementation:**

In the factory (~line 849), parse config:
```go
parallelToolCalls := true
if v, ok := cfg["parallel_tool_calls"].(bool); ok {
    parallelToolCalls = v
}
```

Store on step struct. In the tool execution loop (~line 370), when `!s.parallelToolCalls`:
```go
if !s.parallelToolCalls && len(resp.ToolCalls) > 0 {
    // Process only the first tool call
    tc := resp.ToolCalls[0]
    // Execute it
    result := executeToolCall(tc)
    // Append assistant + tool result to messages
    messages = append(messages, provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{tc}})
    messages = append(messages, provider.Message{Role: provider.RoleTool, Content: result, ToolCallID: tc.ID})
    // Continue loop — LLM will decide next action based on result
    continue
}
```

**Test:** Mock provider returns 3 tool calls. With parallel_tool_calls=false, verify only 1st executes per iteration. With parallel_tool_calls=true (default), all 3 execute.

**Commit:** `feat(orchestrator): add parallel_tool_calls config for sequential execution`

---

## Task 2: Helpful Error Messages with Fuzzy Suggestions

**Files:**
- Modify: `orchestrator/tool_registry.go` (Execute method)
- Create: `orchestrator/tool_fuzzy_match.go`
- Create: `orchestrator/tool_fuzzy_match_test.go`

**Implementation:**

```go
// tool_fuzzy_match.go
func suggestTool(name string, registry map[string]plugin.Tool) string {
    bestMatch := ""
    bestDist := 999
    for regName := range registry {
        // Check prefix match first (file_manager:read → file_read)
        if strings.Contains(regName, extractBaseName(name)) || strings.Contains(name, extractBaseName(regName)) {
            return regName
        }
        // Levenshtein as fallback
        dist := levenshtein(name, regName)
        if dist < bestDist && dist <= 5 {
            bestDist = dist
            bestMatch = regName
        }
    }
    return bestMatch
}
```

In `ToolRegistry.Execute()`, when tool not found:
```go
if !ok {
    suggestion := suggestTool(name, tr.tools)
    msg := fmt.Sprintf("tool %q not found in registry", name)
    if suggestion != "" {
        if t, exists := tr.tools[suggestion]; exists {
            def := t.Definition()
            msg += fmt.Sprintf(". Did you mean %q? Parameters: %v", suggestion, def.Parameters)
        }
    }
    msg += fmt.Sprintf(". Available tools: %s", strings.Join(toolNames(tr.tools), ", "))
    return nil, fmt.Errorf(msg)
}
```

For file tools, update `validatePath` error in `orchestrator/tools/file.go`:
```go
return "", fmt.Errorf("path traversal not allowed: %s. Workspace is %q — use paths like %q",
    relPath, workspace, filepath.Join(workspace, "data/config/app.yaml"))
```

**Test:** Verify "file_manager:read" suggests "file_read". Verify "mcp_wfctl_validate" suggests "mcp_wfctl__validate_config". Verify path error includes workspace.

**Commit:** `feat(orchestrator): add fuzzy tool name suggestions in error messages`

---

## Task 3: Placeholder Detection in Tool Args

**Files:**
- Create: `orchestrator/tools/arg_validation.go`
- Create: `orchestrator/tools/arg_validation_test.go`
- Modify: `orchestrator/tools/file.go` (FileWriteTool.Execute)

**Implementation:**

```go
// arg_validation.go
var placeholderPatterns = []string{
    `<[a-zA-Z_ ]+>`,     // <improved yaml>, <content here>
    `\$\{[^}]+\}`,        // ${VARIABLE}
    `TODO`, `FIXME`, `PLACEHOLDER`,
}

func DetectPlaceholder(content string) (bool, string) {
    for _, pattern := range placeholderPatterns {
        if matched, _ := regexp.MatchString(pattern, content); matched {
            return true, pattern
        }
    }
    if len(strings.TrimSpace(content)) < 10 {
        return true, "content too short (< 10 chars)"
    }
    return false, ""
}
```

In `FileWriteTool.Execute()`, before writing:
```go
if isPlaceholder, reason := DetectPlaceholder(content); isPlaceholder {
    return nil, fmt.Errorf("file_write content appears to be a placeholder (%s). "+
        "Provide the actual file content. If you read a file with file_read, "+
        "modify that content and pass the modified version to file_write", reason)
}
```

**Test:** Verify `<improved yaml>` is detected. Verify `${CONFIG}` is detected. Verify real YAML content passes.

**Commit:** `feat(tools): add placeholder detection for file_write content`

---

## Task 4: Update Scenario Prompts + Configs

**Files:**
- Modify: all 3 scenario agent-config.yaml files in workflow-scenarios

**Changes:**
1. Add `parallel_tool_calls: false` to all step.agent_execute configs
2. Rewrite system prompts with structured tool tables and exact examples:

```yaml
system_prompt: |
  You are a workflow config improvement agent.

  TOOLS (call by EXACT name):
  | Tool | Description | Args |
  |------|-------------|------|
  | file_read | Read a file | {"path": "/data/config/app.yaml"} |
  | file_write | Write a file | {"path": "/data/config/app.yaml", "content": "yaml..."} |
  | mcp_wfctl__validate_config | Validate YAML | {"yaml_content": "yaml..."} |
  | mcp_wfctl__inspect_config | Inspect config | {"yaml_content": "yaml..."} |

  RULES:
  1. Call ONE tool at a time. Wait for its result before calling the next.
  2. Use EXACT tool names from the table above. No other tools exist.
  3. The config file is at: /data/config/app.yaml
  4. After file_read returns content, use that content in subsequent calls.
  5. Never use placeholder text like <content> — always use real values.
```

3. Remove placeholder examples from task descriptions

**Commit:** `fix(scenarios): improve prompts for reliable tool chaining`

---

## Task 5: Execute All 3 Scenarios End-to-End

**This is not a code task — it's actual execution.**

For each scenario (85, 86, 87):
1. Build server binary with latest agent plugin
2. Start Ollama, base app, agent server
3. Trigger the improvement endpoint
4. Wait for pipeline completion
5. Query agent-state.db transcripts table
6. Report: what tool calls were made, what the agent produced, did it succeed

**Success criteria per scenario:**
- Scenario 85: Agent reads config, proposes search endpoint, validates, writes improved config
- Scenario 86: Agent creates analytics pipeline, validates, writes
- Scenario 87: Agent performs at least 2 iterations, each committed to git

**Report format:** For each scenario, include:
- Transcript (role, tool_calls, content summary)
- Blackboard artifacts
- Whether the config was actually modified on disk
- Whether new endpoints respond
