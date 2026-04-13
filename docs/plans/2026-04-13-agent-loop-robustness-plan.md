# Agent Loop Robustness — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix 4 LLM agent behavior issues so agentic loops complete tool chains and produce real outcomes.

**Architecture:** Changes to agent executor loop (sequential enforcement, empty response handling), ToolRegistry (response pagination with cache), and MCP tool adapter (search/filter on list tools).

**Tech Stack:** Go, workflow-plugin-agent orchestrator package.

---

## Task 1: Sequential Mode — Error on Multiple Tool Calls

**Files:**
- Modify: `orchestrator/step_agent_execute.go:419-429`
- Create: `orchestrator/step_agent_execute_sequential_test.go`

**Current code (line 427-428):**
```go
if !s.parallelToolCalls && len(toolCallsToProcess) > 1 {
    toolCallsToProcess = toolCallsToProcess[:1]
}
```

**Replace with:**
```go
if !s.parallelToolCalls && len(resp.ToolCalls) > 1 {
    // Don't execute any — error back to the LLM
    errMsg := fmt.Sprintf(
        "[SYSTEM] You sent %d tool calls in one turn, but this agent requires sequential execution. "+
            "Call ONE tool at a time and wait for its result. "+
            "Your first intended call was %q — resend it as the only tool call.",
        len(resp.ToolCalls), resp.ToolCalls[0].Name,
    )
    messages = append(messages, provider.Message{
        Role:      provider.RoleAssistant,
        Content:   resp.Content,
        ToolCalls: resp.ToolCalls,
    })
    messages = append(messages, provider.Message{
        Role:    provider.RoleUser,
        Content: errMsg,
    })
    // Don't count toward max_iterations — this is a correction, not a real turn
    iterCount--
    continue
}
```

**Test:** Mock provider returns 3 tool calls. Verify: (1) no tools execute, (2) error message in conversation, (3) iterCount not incremented, (4) loop continues.

**Commit:** `feat(orchestrator): error on multiple tool calls in sequential mode`

---

## Task 2: Response Pagination with Cache

**Files:**
- Create: `orchestrator/response_paginator.go`
- Create: `orchestrator/response_paginator_test.go`
- Modify: `orchestrator/tool_registry.go` (wrap Execute with pagination)

**Implementation:**

```go
// response_paginator.go
type ResponsePaginator struct {
    mu              sync.Mutex
    cache           map[string]*cachedResponse // key: tool+args hash
    maxResponseSize int                        // chars before pagination triggers
    pageSize        int                        // items per page
}

type cachedResponse struct {
    items     []string   // split response lines
    createdAt time.Time
}

func NewResponsePaginator(contextWindow int) *ResponsePaginator {
    // Page size = ~10% of context window / ~80 chars per item
    pageSize := max((contextWindow/10)/80, 10)
    return &ResponsePaginator{
        cache:           make(map[string]*cachedResponse),
        maxResponseSize: contextWindow / 5, // ~20% of context triggers pagination
        pageSize:        pageSize,
    }
}

func (rp *ResponsePaginator) Paginate(toolName string, args map[string]any, result string) string {
    // Check if pagination requested (page param)
    if page, ok := args["page"]; ok {
        return rp.getPage(toolName, args, page)
    }
    // Check if result needs pagination
    if len(result) <= rp.maxResponseSize {
        return result // fits, return as-is
    }
    // Cache and return first page
    key := rp.cacheKey(toolName, args)
    lines := strings.Split(result, "\n")
    rp.mu.Lock()
    rp.cache[key] = &cachedResponse{items: lines, createdAt: time.Now()}
    rp.mu.Unlock()
    return rp.formatPage(lines, 1, key)
}

func (rp *ResponsePaginator) formatPage(lines []string, page int, key string) string {
    start := (page - 1) * rp.pageSize
    end := min(start+rp.pageSize, len(lines))
    totalPages := (len(lines) + rp.pageSize - 1) / rp.pageSize
    
    var sb strings.Builder
    fmt.Fprintf(&sb, "[Page %d of %d — showing items %d-%d of %d]\n",
        page, totalPages, start+1, end, len(lines))
    for _, line := range lines[start:end] {
        sb.WriteString(line)
        sb.WriteByte('\n')
    }
    fmt.Fprintf(&sb, "\n[To see next page: call with {\"page\": %d}]", page+1)
    if page > 1 {
        fmt.Fprintf(&sb, "\n[Previous page: {\"page\": %d}]", page-1)
    }
    sb.WriteString("\n[Search within results: {\"query\": \"keyword\"}]")
    return sb.String()
}
```

Wire into ToolRegistry.Execute() — after tool execution, pass result through paginator:
```go
result, err := t.Execute(ctx, args)
if err == nil && rp != nil {
    if s, ok := result.(string); ok {
        result = rp.Paginate(name, args, s)
    }
}
```

**Test:** Tool returns 200-line string. Verify: page 1 returned with navigation instructions, page 2 from cache, cache key uses tool+args hash.

**Commit:** `feat(orchestrator): add response pagination with cache for large tool outputs`

---

## Task 3: Search/Filter on List Tools

**Files:**
- Modify: `orchestrator/mcp_tool_adapter.go` (intercept list_step_types/list_module_types calls)
- Create: `orchestrator/mcp_tool_filter_test.go`

**Implementation:** In `inProcessMCPToolAdapter.Execute()`, intercept calls to list tools:

```go
func (a *inProcessMCPToolAdapter) Execute(ctx context.Context, args map[string]any) (any, error) {
    // For list tools, add filter/summary support
    if a.toolName == "list_step_types" || a.toolName == "list_module_types" || a.toolName == "list_trigger_types" {
        return a.executeListWithFilter(ctx, args)
    }
    return a.provider.CallTool(ctx, a.toolName, args)
}

func (a *inProcessMCPToolAdapter) executeListWithFilter(ctx context.Context, args map[string]any) (any, error) {
    // Get full result
    result, err := a.provider.CallTool(ctx, a.toolName, args)
    if err != nil {
        return nil, err
    }
    
    resultStr, _ := result.(string)
    
    // If query param provided, filter
    if query, ok := args["query"].(string); ok && query != "" {
        lines := strings.Split(resultStr, "\n")
        var filtered []string
        for _, line := range lines {
            if strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
                filtered = append(filtered, line)
            }
        }
        return strings.Join(filtered, "\n"), nil
    }
    
    // No query — return summary with categories
    lines := strings.Split(strings.TrimSpace(resultStr), "\n")
    if len(lines) > 30 {
        categories := categorizeItems(lines)
        var sb strings.Builder
        fmt.Fprintf(&sb, "%d items available. Use {\"query\": \"keyword\"} to filter.\n\nCategories:\n", len(lines))
        for cat, count := range categories {
            fmt.Fprintf(&sb, "  %s: %d items\n", cat, count)
        }
        sb.WriteString("\nExample: {\"query\": \"db\"} returns database-related items.")
        return sb.String(), nil
    }
    
    return result, nil
}
```

**Test:** Call list_step_types with no filter → get summary. Call with {"query": "db"} → get filtered list. Call with {"query": "xyz"} → empty.

**Commit:** `feat(orchestrator): add search/filter support for MCP list tools`

---

## Task 4: Empty Response Continuation

**Files:**
- Modify: `orchestrator/step_agent_execute.go:419-421` (empty response handling)
- Create: `orchestrator/tool_intent_detector.go`
- Create: `orchestrator/tool_intent_detector_test.go`

**Current code (line 419-421):**
```go
if len(resp.ToolCalls) == 0 {
    break
}
```

**Replace with:**
```go
if len(resp.ToolCalls) == 0 {
    // Check for empty response or verbalized tool intent
    content := strings.TrimSpace(resp.Content)
    
    if content == "" && emptyRetries < 2 {
        // Empty response — prompt to continue
        messages = append(messages, provider.Message{
            Role:      provider.RoleAssistant,
            Content:   "",
        })
        messages = append(messages, provider.Message{
            Role:    provider.RoleUser,
            Content: "[SYSTEM] Your response was empty. Please continue with the next step. " +
                "If you are done, respond with TASK COMPLETE and a summary.",
        })
        emptyRetries++
        iterCount-- // don't count toward max
        continue
    }
    
    if containsToolIntent(content, toolDefs) && intentRetries < 2 {
        // Verbalized intent without tool call
        messages = append(messages, provider.Message{
            Role:      provider.RoleAssistant,
            Content:   content,
        })
        messages = append(messages, provider.Message{
            Role:    provider.RoleUser,
            Content: "[SYSTEM] You described your intent to call a tool but didn't actually " +
                "make the tool call. Please execute the tool by making a proper tool call.",
        })
        intentRetries++
        iterCount--
        continue
    }
    
    // Real completion — break
    break
}
```

Add `emptyRetries` and `intentRetries` counters (init to 0 before the loop).

**tool_intent_detector.go:**
```go
func containsToolIntent(content string, toolDefs []provider.ToolDef) bool {
    lower := strings.ToLower(content)
    intentPatterns := []string{
        "let's call", "i'll call", "i will call", "let me call",
        "i'll use", "i will use", "let me use", "let's use",
        "now i'll", "now let's", "next step is to call",
    }
    for _, p := range intentPatterns {
        if strings.Contains(lower, p) {
            return true
        }
    }
    // Check if any tool name appears in prose
    for _, td := range toolDefs {
        if strings.Contains(lower, strings.ToLower(td.Name)) {
            return true
        }
    }
    return false
}
```

**Test:** Empty response → continuation injected, max 2. "I'll call file_write" → intent detected. "TASK COMPLETE" → breaks normally.

**Commit:** `feat(orchestrator): add empty response continuation and verbalized intent detection`

---

## Task 5: Update Scenarios + Re-Execute

**Files:**
- Modify: scenario 85/86/87 agent configs (ensure parallel_tool_calls: false)
- Update go.mods to new agent plugin tag

**Execution:** Build, start, trigger, report full transcripts for all 3 scenarios. Success criteria: at least scenario 85 reads config, validates improvement, and writes modified config to disk.

**Commit:** `fix(scenarios): update configs for agent loop robustness`
