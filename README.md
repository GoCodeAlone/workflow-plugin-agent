# workflow-plugin-agent

AI agent primitives for workflow apps. An internal engine plugin providing:

- `agent.provider` module — AI provider abstraction (mock, test, anthropic, openai, copilot)
- `step.agent_execute` — autonomous agent loop (LLM call → tool execution → repeat)
- `step.provider_test` — test connectivity to a configured AI provider
- `step.provider_models` — list available models from a provider

## Install

```sh
GOPRIVATE=github.com/GoCodeAlone/* go get github.com/GoCodeAlone/workflow-plugin-agent@latest
```

## Usage

```go
import agent "github.com/GoCodeAlone/workflow-plugin-agent"

engine := workflow.NewEngine(
    workflow.WithPlugin(agent.New()),
)
```
