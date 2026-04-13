# Agentic Loop Construction Guide

How to build self-improvement pipelines with the workflow engine and agent plugin.

---

## Architecture Overview

The **agent-as-loop** pattern places `step.agent_execute` at the center of a pipeline. The agent step IS the iteration loop — it calls tools internally to read, modify, validate, and commit config changes. Pipeline steps that come *after* the agent serve as the deployment gate.

```
POST /improve
     │
     ▼
step.set            ← inject context (config_path, provider alias, system_prompt)
     │
     ▼
step.agent_execute  ← the loop: read → inspect → propose → validate → write → commit
     │                          (uses file tools, MCP tools, git tools, web_fetch)
     ▼
step.blackboard_post ← audit trail
     │
     ▼
step.self_improve_validate ← deployment gate: reject if config invalid or empty
     │
     ▼
step.self_improve_deploy   ← hot_reload: write config file, signal watcher
```

The agent decides how many iterations to run. The surrounding pipeline steps enforce quality before anything is deployed.

---

## Prerequisites

Four modules must be present in the agent config:

```yaml
modules:
  - name: ratchet-db
    type: storage.sqlite
    config:
      dbPath: /data/agent-state.db
      walMode: true

  - name: server
    type: http.server
    config:
      address: ":8081"

  - name: router
    type: http.router
    dependsOn: [server]

  - name: ai
    type: agent.provider
    config:
      provider: ollama
      model: gemma4:e2b
      base_url: "${OLLAMA_BASE_URL:-http://ollama:11434}"
      max_tokens: 8192

  - name: guardrails
    type: agent.guardrails
    config:
      defaults:
        enable_self_improvement: true
        require_diff_review: true
        max_iterations_per_cycle: 5
        deploy_strategy: hot_reload
        allowed_tools:
          - "mcp:wfctl:*"
          - "file_read"
          - "file_write"
          - "git_commit"
          - "web_fetch"
        command_policy:
          mode: allowlist
          block_pipe_to_shell: true
          block_script_execution: true
      immutable_sections:
        - path: "modules.guardrails"
          override: challenge_token
```

**Why `ratchet-db`?** The agent plugin persists tool call history, session state, and blackboard artifacts to SQLite. It must be present even if the pipeline itself has no DB steps.

**Why `immutable_sections`?** Prevents the agent from modifying its own guardrails. The `modules.guardrails` path is locked behind a challenge token so a rogue LLM cannot escalate its own permissions.

---

## Tool Wiring

### MCP Tools

The agent plugin automatically bridges MCP tools from registered `mcp.provider` services. Tools become available in the ToolRegistry with colon-separated names:

```
mcp:{server}:{tool}   →   e.g., mcp:wfctl:validate_config
```

Tools must appear in `guardrails.defaults.allowed_tools` using the same naming convention. Wildcards work:

```yaml
allowed_tools:
  - "mcp:wfctl:*"        # all wfctl tools
  - "mcp:self_improve:*" # all self-improvement tools
  - "mcp:lsp:diagnose"   # specific LSP tool
```

> **Note:** The design document mentions `mcp_{server}_{tool}` (underscore) as an earlier naming convention. The current convention uses colons. Use colon format in all new configs.

### File Tools

File tools (`file_read`, `file_write`, `file_list`) enforce path traversal protection. All paths are validated against a configured workspace. Without workspace configuration, absolute paths fail.

**Configure workspace on the step:**

```yaml
- name: designer
  type: step.agent_execute
  config:
    provider_service: ai
    workspace: /data        # ← required for absolute paths like /data/config/app.yaml
    system_prompt: |
      ...
```

The workspace is also injectable via context (`AGENT_WORKSPACE` environment variable), but the per-step `workspace` config is the most explicit and reliable option.

### Git Tools

`git_commit` and `git_status` run git commands in the process working directory (not the step workspace). They accept absolute file paths in the `paths` argument. The agent uses these to commit each iteration's changes:

```
git_commit: {"message": "feat(iter-1): add search endpoint", "paths": ["/data/config/app.yaml"]}
```

---

## Provider Configuration

The agent resolves its LLM provider via an alias in pipeline data, not by reading the step config directly.

**Step 1 — inject the alias with step.set:**

```yaml
- name: set_context
  type: step.set
  config:
    values:
      config_path: "${CONFIG_PATH:-/data/config/app.yaml}"
      provider: ai          # must match the agent.provider module name
```

**Step 2 — reference the alias in step.agent_execute:**

```yaml
- name: designer
  type: step.agent_execute
  config:
    provider_service: ai    # must match provider alias set above
    max_iterations: 20
    workspace: /data
    system_prompt: |
      ...
```

### Genkit Ollama and Tool Support

The Genkit Ollama plugin maintains a hardcoded `toolSupportedModels` allowlist. Models not in this list are registered without tool-call capability, causing `Chat()` to return `"does not support tool use"` before even contacting the server.

The agent plugin works around this by calling `DefineModel()` explicitly with `Tools: true` for the configured model. This means any Ollama model — including `gemma4:e2b` — will work as long as the model itself supports function calling. Verify with:

```bash
curl http://localhost:11434/api/chat -d '{
  "model": "gemma4:e2b",
  "messages": [{"role": "user", "content": "test"}],
  "tools": [{"type": "function", "function": {"name": "test", "parameters": {}}}]
}'
```

If the response contains a `tool_calls` field, the model supports tool use.

---

## System Prompt Design

The system prompt is read from **pipeline data** (`pc.Current["system_prompt"]`), not from the step config. Place it in `step.set` or in the `step.agent_execute` config block under `system_prompt:`.

A well-structured system prompt for a config-improvement agent:

```yaml
system_prompt: |
  You are an autonomous workflow configuration improvement agent.

  AVAILABLE TOOLS:
  - file_read: Read a file. Args: {"path": "/path/to/file"}
  - file_write: Write a file. Args: {"path": "/path", "content": "..."}
  - mcp:wfctl:validate_config: Validate workflow YAML. Args: {"yaml_content": "..."}
  - mcp:wfctl:inspect_config: Inspect config structure. Args: {"yaml_content": "..."}
  - mcp:wfctl:list_step_types: List available step types. Args: {}
  - git_commit: Commit changes. Args: {"message": "...", "paths": ["file"]}
  - web_fetch: Fetch a URL. Args: {"url": "http://..."}

  WORKFLOW ENGINE YAML RULES (violations cause silent failures):
  1. http.server uses 'address: ":8080"' NOT 'port: 8080'
  2. Database steps use 'database: db' NOT 'module: db'
  3. Query parameters use 'params:' NOT 'args:'
  4. DB query mode is 'list' or 'single' NOT 'many' or 'one'
  5. Response step type is 'step.json_response' NOT 'step.response'
  6. Request parse uses 'parse_body: true' NOT 'format: json'
  7. Pipelines have inline trigger blocks (type: http with path/method)

  PROCESS:
  1. Read current config: file_read
  2. Inspect structure: mcp:wfctl:inspect_config
  3. Generate improved YAML with new endpoints
  4. Validate: mcp:wfctl:validate_config (fix and retry on errors)
  5. Write: file_write
  6. Commit: git_commit
  7. Verify: web_fetch
```

**Key rules:**
- List tools by their exact registry names (what the agent will call them as)
- Include the YAML quirks section — LLMs do not know this engine's conventions
- Number the steps — models follow explicit sequences more reliably
- Put validation before write — always

---

## Pipeline Data Keys

`step.agent_execute` reads these keys from `pc.Current` (pipeline data):

| Key | Required | Description |
|-----|----------|-------------|
| `provider` | No | DB-level provider alias (Path 1: ProviderRegistry lookup). Independent of `provider_service`. |
| `system_prompt` | Yes (if not in step config) | Agent instructions |
| `config_path` | No | Path to the app config file |
| `base_app_url` | No | Base URL for web_fetch verification calls |
| `task` / `description` | No | Additional task context injected into the prompt |

Set these via `step.set` before the agent step:

```yaml
- name: set_context
  type: step.set
  config:
    values:
      config_path: "${CONFIG_PATH:-/data/config/app.yaml}"
      base_app_url: "${BASE_APP_URL:-http://localhost:8080}"
      provider: ai
```

---

## Common Pitfalls

### 1. Mock provider seeded by default

The DB is pre-seeded with a mock provider on first run. If `provider_service` doesn't match a registered real provider, the agent silently uses the mock and produces no meaningful output. Always set the `provider` key in pipeline data to match the `agent.provider` module name.

### 2. Empty `proposed_yaml` passes validation

If the agent does nothing (e.g., the mock provider returns empty content), `proposed_yaml` will be empty or `<no value>`. Without an explicit guard in `step.self_improve_validate`, the deploy step will happily overwrite your config with an empty file.

The validate step should be configured to reject empty content:

```yaml
- name: validate
  type: step.self_improve_validate
  config:
    validation_level: strict
    require_zero_errors: true
```

### 3. Tool names must match exactly

If the system prompt lists `mcp_wfctl_validate_config` but the registry key is `mcp:wfctl:validate_config`, the agent will attempt to call a tool that doesn't exist and the call will fail silently (or the agent will hallucinate a response). Always verify the tool names in the prompt match the `allowed_tools` list.

### 4. Absolute paths break without workspace config

`filepath.Join(workspace, "/data/config/app.yaml")` produces `/data/data/config/app.yaml` — not what you want. Set `workspace: /data` on the step and the file tools will validate absolute paths against that workspace directly instead of joining.

### 5. Shell expansion in tool arguments

LLMs sometimes expand shell variables in tool arguments:

```json
{"command": "go test $GOPATH/..."}
```

The command policy's `enable_static_analysis` flag catches most cases, but the agent plugin's input validation layer is the primary guard. Ensure `block_pipe_to_shell: true` and `block_script_execution: true` are set.

### 6. Output key is `result`, not `content`

When referencing the agent's output in downstream steps, use `steps.<name>.result`:

```yaml
proposed_yaml: "${ steps.designer.result }"
```

Not `.content`, not `.output`. The agent step sets `.result`.

---

## Example: Self-Improving API (Scenario 85)

Full working config for a pipeline that audits a task management API and adds endpoints:

```yaml
modules:
  - name: ratchet-db
    type: storage.sqlite
    config:
      dbPath: /data/agent-state.db
      walMode: true

  - name: server
    type: http.server
    config:
      address: ":8081"

  - name: router
    type: http.router
    dependsOn: [server]

  - name: ai
    type: agent.provider
    config:
      provider: ollama
      model: gemma4:e2b
      base_url: "${OLLAMA_BASE_URL:-http://ollama:11434}"
      max_tokens: 8192

  - name: guardrails
    type: agent.guardrails
    config:
      defaults:
        enable_self_improvement: true
        require_diff_review: true
        max_iterations_per_cycle: 5
        deploy_strategy: hot_reload
        allowed_tools:
          - "mcp:wfctl:*"
          - "mcp:wfctl:validate_config"
          - "mcp:lsp:diagnose"
          - "file_read"
          - "file_write"
          - "file_list"
          - "git_status"
          - "git_commit"
          - "web_fetch"
          - "shell_exec"
        command_policy:
          mode: allowlist
          allowed_commands:
            - "go build"
            - "go test"
            - "wfctl"
            - "curl"
          enable_static_analysis: true
          block_pipe_to_shell: true
          block_script_execution: true
      immutable_sections:
        - path: "modules.guardrails"
          override: challenge_token
      override:
        mechanism: challenge_token
        admin_secret_env: "WFCTL_ADMIN_SECRET"

pipelines:
  self_improvement_loop:
    trigger:
      type: http
      config:
        path: /improve
        method: POST
    steps:
      - name: set_context
        type: step.set
        config:
          values:
            config_path: "${CONFIG_PATH:-/data/config/app.yaml}"
            base_app_url: "${BASE_APP_URL:-http://localhost:8080}"
            provider: ai

      - name: designer
        type: step.agent_execute
        config:
          provider_service: ai
          max_iterations: 20
          workspace: /data
          system_prompt: |
            You are an autonomous workflow configuration improvement agent.

            AVAILABLE TOOLS:
            - file_read: Read a file. Args: {"path": "/path/to/file"}
            - file_write: Write a file. Args: {"path": "/path", "content": "..."}
            - mcp:wfctl:validate_config: Validate workflow YAML. Args: {"yaml_content": "..."}
            - mcp:wfctl:inspect_config: Inspect config structure. Args: {"yaml_content": "..."}
            - mcp:wfctl:list_step_types: List available pipeline step types. Args: {}
            - mcp:wfctl:get_step_schema: Get schema for a step. Args: {"type": "step.db_query"}
            - mcp:wfctl:list_module_types: List available module types. Args: {}
            - mcp:lsp:diagnose: Run LSP diagnostics. Args: {"path": "/path/to/file"}
            - git_status: Check git status. Args: {}
            - git_commit: Commit changes. Args: {"message": "...", "paths": ["file1"]}
            - web_fetch: Fetch a URL. Args: {"url": "http://..."}

            WORKFLOW ENGINE YAML RULES:
            1. http.server uses 'address: ":8080"' NOT 'port: 8080'
            2. Database steps use 'database: db' NOT 'module: db'
            3. Query parameters use 'params:' NOT 'args:'
            4. DB query mode is 'list' or 'single' NOT 'many' or 'one'
            5. Response step type is 'step.json_response' NOT 'step.response'
            6. Request parse uses 'parse_body: true' NOT 'format: json'
            7. Pipelines have inline trigger blocks (type: http with path/method)
            8. step.json_response body can be object literal OR template string
            9. Workflows section has 'routes: []' (empty — routes come from triggers)

            PROCESS:
            1. Read the current config using file_read
            2. Inspect with mcp:wfctl:inspect_config to understand the structure
            3. Use mcp:wfctl:list_step_types to see what steps are available
            4. Design 1-2 new pipelines (search endpoint, stats endpoint)
            5. Generate the complete improved config YAML
            6. Validate with mcp:wfctl:validate_config
            7. If validation fails, fix the YAML and revalidate
            8. Write the config using file_write
            9. Commit with git_commit
            10. Report what you improved

      - name: map_output
        type: step.set
        config:
          values:
            proposed_yaml: "${ steps.designer.result }"

      - name: post_design
        type: step.blackboard_post
        config:
          phase: design
          artifact_type: config_proposal

      - name: validate
        type: step.self_improve_validate
        config:
          validation_level: strict
          require_zero_errors: true

      - name: diff
        type: step.self_improve_diff
        config:
          force: true
          output_to_blackboard: true

      - name: deploy
        type: step.self_improve_deploy
        config:
          strategy: hot_reload
          config_path: "${CONFIG_PATH:-/data/config/app.yaml}"

  health_check:
    trigger:
      type: http
      config:
        path: /healthz
        method: GET
    steps:
      - name: respond
        type: step.json_response
        config:
          status: 200
          body:
            status: healthy
            scenario: "85-self-improving-api"
            component: agent
```

---

## Multi-Phase Pipelines (Scenario 86 Pattern)

For more complex scenarios where multiple tools need to be created in sequence, use multiple `step.agent_execute` steps with deploy gates between them:

```
agent (create tool A) → validate → deploy → agent (use tool A, create tool B) → deploy
```

Each agent step gets a narrowly-scoped system prompt. Deploy in between ensures the next agent works with an up-to-date environment.

See `scenarios/86-self-extending-mcp/config/agent-config.yaml` for a full example.

---

## Fully Autonomous Iteration (Scenario 87 Pattern)

For maximum autonomy, split the pipeline into explicit phases using separate agent steps for audit, plan, and verify. This improves observability and lets guardrails apply at each phase boundary:

```
audit (agent) → blackboard → plan (agent) → validate → deploy → blackboard → verify (agent) → git_commit
```

Each phase posts to the blackboard with a distinct phase tag (`phase: audit`, `phase: plan`, etc.), giving an auditable trail of what the agent observed, decided, and verified.

---

## Context Management

Agentic loops accumulate conversation history rapidly — tool results often include full YAML files, and 44+ tool definitions add constant overhead. Without context management, local models like Gemma 4 (7.2 GB) exhaust the 16 GB KV cache by iteration 2.

### Compaction (default: 80%)

`step.agent_execute` automatically compacts the conversation when it approaches the model's context window:

```yaml
- name: improve
  type: step.agent_execute
  config:
    provider_service: ai
    max_iterations: 10
    context:
      compaction_threshold: 0.80   # default; omit to keep this value
```

When compaction triggers, the executor calls the LLM to summarise the middle portion of the conversation, then replaces it with the summary plus the most recent exchanges. The agent continues without losing its goal or recent context.

Set `compaction_threshold: 0` to disable compaction entirely (not recommended for local models).

### context_window for Ollama (num_ctx)

For Ollama models, set `context_window` to limit KV cache memory allocation. This maps directly to Ollama's `num_ctx` parameter and also tells the compaction logic the accurate token budget:

```yaml
- name: ai
  type: agent.provider
  config:
    provider: ollama
    model: gemma4:e2b
    base_url: "${OLLAMA_BASE_URL:-http://ollama:11434}"
    max_tokens: 8192
    context_window: 16384   # sets Ollama num_ctx; limits KV cache to ~2 GB
```

Without `context_window`, Ollama allocates KV cache based on the model's default context length, which can be 128K+ tokens even for small quantized models.

**Recommended values for common models on 16 GB machines:**

| Model | Suggested context_window |
|-------|--------------------------|
| gemma4:e2b | 16384 |
| qwen3:8b | 32768 |
| llama3.3:70b (Q4) | 8192 |
| phi4 | 16384 |

### Tool Filtering (automatic)

When an `agent.guardrails` module is configured with `allowed_tools`, only matching tool definitions are sent to the LLM on each `Chat()` call. This reduces per-call token overhead and prevents the model from hallucinating calls to tools it isn't allowed to use.

No extra config needed — filtering activates automatically when `allowed_tools` is non-empty.

### ContextStrategy (advanced)

Providers that maintain server-side conversation state can implement `provider.ContextStrategy`. When `ManagesContext()` returns true, the executor sends only new messages since the last call instead of the full history. On compaction, it calls `ResetContext()` and resends the compacted summary.

This is an advanced use case for custom provider implementations. Standard Ollama/Anthropic/OpenAI providers do not implement this interface.

See `scenarios/87-autonomous-agile-agent/config/agent-config.yaml` for a full example.
