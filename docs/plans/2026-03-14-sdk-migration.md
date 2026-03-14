# Provider SDK Migration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace hand-rolled HTTP provider implementations with official Go SDKs (anthropic-sdk-go, openai-go, generative-ai-go) and add a new Gemini provider.

**Architecture:** Each provider's internal HTTP calls are replaced with SDK client calls. The `Provider` interface, `StreamEvent` types, `ProviderRegistry`, and config structs remain unchanged. SDK streaming is adapted to our `chan StreamEvent` pattern via thin adapter goroutines.

**Tech Stack:** `github.com/anthropics/anthropic-sdk-go`, `github.com/openai/openai-go`, `github.com/google/generative-ai-go/genai`, Go 1.26

---

## Task 1: Add SDK dependencies

**Files:**
- Modify: `go.mod`

**Step 1:** Add the three official SDKs:
```bash
cd /Users/jon/workspace/workflow-plugin-agent
go get github.com/anthropics/anthropic-sdk-go
go get github.com/openai/openai-go
go get github.com/google/generative-ai-go/genai
go get google.golang.org/api/option
go mod tidy
```

**Step 2:** Verify build:
```bash
go build ./...
```

**Step 3:** Commit:
```bash
git add go.mod go.sum
git commit -m "chore: add official AI provider SDKs (anthropic, openai, google genai)"
```

---

## Task 2: Migrate Anthropic direct provider

**Files:**
- Modify: `provider/anthropic.go`
- Modify: `provider/anthropic_test.go`

**What to do:**

Read the current `provider/anthropic.go` fully. Then rewrite the internals:

1. Add SDK import: `anthropic "github.com/anthropics/anthropic-sdk-go"` and `"github.com/anthropics/anthropic-sdk-go/option"`

2. Replace `AnthropicProvider` struct internals — store an SDK client instead of config:
```go
type AnthropicProvider struct {
    client *anthropic.Client
    config AnthropicConfig
}
```

3. In `NewAnthropicProvider`, create the SDK client:
```go
func NewAnthropicProvider(cfg AnthropicConfig) *AnthropicProvider {
    opts := []option.RequestOption{
        option.WithAPIKey(cfg.APIKey),
    }
    if cfg.BaseURL != "" {
        opts = append(opts, option.WithBaseURL(cfg.BaseURL))
    }
    if cfg.HTTPClient != nil {
        opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
    }
    client := anthropic.NewClient(opts...)
    return &AnthropicProvider{client: client, config: cfg}
}
```

4. Rewrite `Chat()` — convert our `[]Message` + `[]ToolDef` to SDK types, call `client.Messages.New()`, convert SDK response back to our `*Response`.

Create helper functions for type conversion:
- `toAnthropicMessages(msgs []Message) []anthropic.MessageParam`
- `toAnthropicTools(tools []ToolDef) []anthropic.ToolParam`
- `fromAnthropicResponse(resp *anthropic.Message) *Response`

5. Rewrite `Stream()` — call `client.Messages.NewStreaming()`, iterate the SDK stream in a goroutine, convert events to `StreamEvent` and send on our channel.

The SDK streaming pattern is:
```go
stream := client.Messages.NewStreaming(ctx, params)
for stream.Next() {
    event := stream.Current()
    // Convert to our StreamEvent and send on channel
}
if err := stream.Err(); err != nil {
    // Send error event
}
```

6. Delete the old `readSSE`, `buildRequest`, `setHeaders` functions — they're replaced by the SDK.

7. Keep `AnthropicConfig` struct and `AuthModeInfo()` unchanged.

8. Update tests: existing tests use `httptest.NewServer` — the SDK client accepts custom base URLs via `option.WithBaseURL`, so mock servers still work. Update test assertions if response shapes changed.

**Run:**
```bash
go build ./... && go test ./provider/ -run TestAnthropic -v -count=1
```

**Commit:**
```bash
git add provider/anthropic.go provider/anthropic_test.go
git commit -m "refactor: migrate Anthropic provider to official anthropic-sdk-go"
```

---

## Task 3: Migrate Anthropic Bedrock provider

**Files:**
- Modify: `provider/anthropic_bedrock.go`
- Modify: `provider/anthropic_bedrock_test.go`

**What to do:**

1. Import the SDK bedrock subpackage: `"github.com/anthropics/anthropic-sdk-go/bedrock"` and `"github.com/anthropics/anthropic-sdk-go/option"`

2. The bedrock subpackage handles AWS SigV4 signing natively. Replace the struct:
```go
type anthropicBedrockProvider struct {
    client *anthropic.Client
    config AnthropicBedrockConfig
}
```

3. In the constructor, create the client with bedrock options:
```go
func NewAnthropicBedrockProvider(cfg AnthropicBedrockConfig) (*anthropicBedrockProvider, error) {
    opts := []option.RequestOption{
        bedrock.WithRegion(cfg.Region),
        bedrock.WithAccessKey(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
    }
    if cfg.HTTPClient != nil {
        opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
    }
    client := anthropic.NewClient(opts...)
    return &anthropicBedrockProvider{client: client, config: cfg}, nil
}
```

4. Reuse the same `toAnthropicMessages`, `toAnthropicTools`, `fromAnthropicResponse` helpers from Task 2. Import them or move them to a shared file.

5. Delete the entire `sigv4Sign` function and all manual AWS signing code (~80 lines).

6. Delete `bedrockReadSSE`, `bedrockBuildRequest` — replaced by SDK.

**Run:**
```bash
go build ./... && go test ./provider/ -run TestBedrock -v -count=1
```

**Commit:**
```bash
git add provider/anthropic_bedrock.go provider/anthropic_bedrock_test.go
git commit -m "refactor: migrate Anthropic Bedrock to SDK, delete hand-rolled SigV4"
```

---

## Task 4: Migrate Anthropic Vertex provider

**Files:**
- Modify: `provider/anthropic_vertex.go`
- Modify: `provider/anthropic_vertex_test.go`

**What to do:**

1. Import: `"github.com/anthropics/anthropic-sdk-go/vertex"` and `"github.com/anthropics/anthropic-sdk-go/option"`

2. Replace struct and constructor:
```go
func NewAnthropicVertexProvider(cfg AnthropicVertexConfig) (*anthropicVertexProvider, error) {
    opts := []option.RequestOption{
        vertex.WithRegion(cfg.Region),
        vertex.WithProjectID(cfg.ProjectID),
    }
    if cfg.CredentialsJSON != "" {
        opts = append(opts, vertex.WithCredentialsJSON([]byte(cfg.CredentialsJSON)))
    }
    // else uses ADC automatically
    if cfg.HTTPClient != nil {
        opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
    }
    client := anthropic.NewClient(opts...)
    return &anthropicVertexProvider{client: client, config: cfg}, nil
}
```

3. Reuse shared Anthropic helpers. Delete `vertexReadSSE`, `vertexBuildRequest`.

4. Delete the manual `oauth2.TokenSource` handling — the SDK vertex subpackage handles GCP credentials natively.

**Run:**
```bash
go build ./... && go test ./provider/ -run TestVertex -v -count=1
```

**Commit:**
```bash
git add provider/anthropic_vertex.go provider/anthropic_vertex_test.go
git commit -m "refactor: migrate Anthropic Vertex to SDK, delete manual GCP token handling"
```

---

## Task 5: Extract shared Anthropic conversion helpers

**Files:**
- Create: `provider/anthropic_convert.go`
- Modify: `provider/anthropic.go` (move helpers out)
- Modify: `provider/anthropic_bedrock.go` (use shared helpers)
- Modify: `provider/anthropic_vertex.go` (use shared helpers)

**What to do:**

After Tasks 2-4, the three Anthropic providers will each have their own copy of the type conversion helpers. Extract into a shared file:

```go
// provider/anthropic_convert.go
package provider

// toAnthropicMessages converts our Message slice to SDK MessageParam slice.
func toAnthropicMessages(msgs []Message) []anthropic.MessageParam { ... }

// toAnthropicTools converts our ToolDef slice to SDK ToolParam slice.
func toAnthropicTools(tools []ToolDef) []anthropic.ToolParam { ... }

// fromAnthropicResponse converts an SDK Message to our Response type.
func fromAnthropicResponse(resp *anthropic.Message) *Response { ... }

// streamAnthropicEvents reads from an SDK stream and sends on our channel.
func streamAnthropicEvents(stream *anthropic.MessageStream, ch chan<- StreamEvent) { ... }
```

Remove duplicates from the three provider files.

**Run:**
```bash
go build ./... && go test ./provider/ -run "TestAnthropic|TestBedrock|TestVertex" -v -count=1
```

**Commit:**
```bash
git add provider/anthropic_convert.go provider/anthropic.go provider/anthropic_bedrock.go provider/anthropic_vertex.go
git commit -m "refactor: extract shared Anthropic type conversion helpers"
```

---

## Task 6: Migrate OpenAI direct provider

**Files:**
- Modify: `provider/openai.go`
- Modify: `provider/openai_test.go`

**What to do:**

1. Import: `openaisdk "github.com/openai/openai-go"` and `"github.com/openai/openai-go/option"`

2. Replace struct:
```go
type OpenAIProvider struct {
    client *openaisdk.Client
    config OpenAIConfig
}
```

3. Constructor:
```go
func NewOpenAIProvider(cfg OpenAIConfig) *OpenAIProvider {
    opts := []option.RequestOption{
        option.WithAPIKey(cfg.APIKey),
    }
    if cfg.BaseURL != "" {
        opts = append(opts, option.WithBaseURL(cfg.BaseURL+"/v1"))
    }
    if cfg.HTTPClient != nil {
        opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
    }
    client := openaisdk.NewClient(opts...)
    return &OpenAIProvider{client: client, config: cfg}
}
```

4. Create conversion helpers:
- `toOpenAIMessages(msgs []Message) []openaisdk.ChatCompletionMessageParamUnion`
- `toOpenAITools(tools []ToolDef) []openaisdk.ChatCompletionToolParam`
- `fromOpenAIResponse(resp *openaisdk.ChatCompletion) *Response`

5. `Chat()` → `client.Chat.Completions.New(ctx, params)`

6. `Stream()` → `client.Chat.Completions.NewStreaming(ctx, params)`, iterate with `stream.Next()`, convert chunks to `StreamEvent`.

7. Delete the old `readSSE` method.

**IMPORTANT:** `OpenRouterProvider` and `CopilotModelsProvider` embed `*OpenAIProvider`. After this change they get the SDK automatically. Verify they still compile and their constructors still work.

**Run:**
```bash
go build ./... && go test ./provider/ -run "TestOpenAI|TestOpenRouter|TestCopilotModels" -v -count=1
```

**Commit:**
```bash
git add provider/openai.go provider/openai_test.go
git commit -m "refactor: migrate OpenAI provider to official openai-go SDK"
```

---

## Task 7: Migrate OpenAI Azure provider

**Files:**
- Modify: `provider/openai_azure.go`
- Modify: `provider/openai_azure_test.go`

**What to do:**

The `openai-go` SDK supports Azure via configuration options. Check if `openai-go` has an Azure option, or use `azure-sdk-for-go/sdk/ai/azopenai`.

If `openai-go` supports Azure:
```go
func NewOpenAIAzureProvider(cfg OpenAIAzureConfig) (*OpenAIAzureProvider, error) {
    opts := []option.RequestOption{
        option.WithBaseURL(fmt.Sprintf("https://%s.openai.azure.com/openai/deployments/%s",
            cfg.Resource, cfg.DeploymentName)),
        option.WithHeader("api-key", cfg.APIKey),
        option.WithQuery("api-version", cfg.APIVersion),
    }
    client := openaisdk.NewClient(opts...)
    // ...
}
```

If not, use the Azure SDK: `"github.com/Azure/azure-sdk-for-go/sdk/ai/azopenai"`.

Reuse `toOpenAIMessages`, `toOpenAITools`, `fromOpenAIResponse` from Task 6.

Delete the hack of creating a throwaway `&OpenAIProvider{}` for `readSSE`.

**Run:**
```bash
go build ./... && go test ./provider/ -run TestAzure -v -count=1
```

**Commit:**
```bash
git add provider/openai_azure.go provider/openai_azure_test.go
git commit -m "refactor: migrate OpenAI Azure to SDK"
```

---

## Task 8: Update Copilot provider to use OpenAI SDK for chat

**Files:**
- Modify: `provider/copilot.go`
- Modify: `provider/copilot_test.go`

**What to do:**

Copilot uses a custom two-step token exchange (GitHub OAuth token → short-lived bearer token) then sends OpenAI-compatible chat requests. Keep the token exchange, replace the chat part with the OpenAI SDK.

1. Store an `*openaisdk.Client` alongside the existing token cache fields.

2. `ensureBearerToken()` stays as-is — it manages the GitHub token exchange.

3. `Chat()` and `Stream()` call `ensureBearerToken()` first, then create a per-request client with the bearer token:
```go
func (p *CopilotProvider) Chat(ctx context.Context, msgs []Message, tools []ToolDef) (*Response, error) {
    token, err := p.ensureBearerToken(ctx)
    if err != nil {
        return nil, err
    }
    client := openaisdk.NewClient(
        option.WithAPIKey(token),
        option.WithBaseURL(p.config.BaseURL),
        option.WithHeader("Copilot-Integration-Id", "ratchet"),
    )
    // Use OpenAI SDK helpers from Task 6
    ...
}
```

4. Delete the manual SSE parsing and `copilotResponse` struct.

**Run:**
```bash
go build ./... && go test ./provider/ -run TestCopilot -v -count=1
```

**Commit:**
```bash
git add provider/copilot.go provider/copilot_test.go
git commit -m "refactor: migrate Copilot chat to OpenAI SDK, keep custom token exchange"
```

---

## Task 9: Extract shared OpenAI conversion helpers

**Files:**
- Create: `provider/openai_convert.go`
- Modify: `provider/openai.go` (move helpers out)

Same pattern as Task 5. Extract `toOpenAIMessages`, `toOpenAITools`, `fromOpenAIResponse`, `streamOpenAIEvents` so OpenAI, Azure, Copilot, OpenRouter, and CopilotModels all share them.

**Run:**
```bash
go build ./... && go test ./provider/ -v -count=1
```

**Commit:**
```bash
git add provider/openai_convert.go provider/openai.go
git commit -m "refactor: extract shared OpenAI type conversion helpers"
```

---

## Task 10: Add Gemini provider

**Files:**
- Create: `provider/gemini.go`
- Create: `provider/gemini_test.go`

**What to do:**

1. Import: `"github.com/google/generative-ai-go/genai"` and `"google.golang.org/api/option"`

2. Define config and struct:
```go
type GeminiConfig struct {
    APIKey     string
    Model      string // default: "gemini-2.5-pro"
    MaxTokens  int
    HTTPClient *http.Client
}

type GeminiProvider struct {
    client *genai.Client
    config GeminiConfig
}
```

3. Constructor:
```go
func NewGeminiProvider(cfg GeminiConfig) (*GeminiProvider, error) {
    opts := []option.ClientOption{
        option.WithAPIKey(cfg.APIKey),
    }
    if cfg.HTTPClient != nil {
        opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
    }
    client, err := genai.NewClient(context.Background(), opts...)
    if err != nil {
        return nil, fmt.Errorf("create genai client: %w", err)
    }
    if cfg.Model == "" {
        cfg.Model = "gemini-2.5-pro"
    }
    if cfg.MaxTokens == 0 {
        cfg.MaxTokens = 4096
    }
    return &GeminiProvider{client: client, config: cfg}, nil
}
```

4. Implement `Chat()`:
- Convert our `[]Message` to genai content parts
- Call `model.GenerateContent(ctx, parts...)`
- Convert response to our `*Response`

5. Implement `Stream()`:
- Call `model.GenerateContentStream(ctx, parts...)`
- Iterate with `for resp, err := iter.Next(); err != iterator.Done;`
- Convert chunks to `StreamEvent`, send on channel

6. Implement `AuthModeInfo()`:
```go
func (p *GeminiProvider) AuthModeInfo() AuthModeInfo {
    return AuthModeInfo{ProviderType: "gemini", AuthType: "api_key"}
}
```

7. Implement tool use conversion:
- Gemini uses `genai.Tool` with `FunctionDeclarations`
- Convert our `ToolDef` → `genai.FunctionDeclaration`
- Convert `genai.FunctionCall` responses → our `ToolCall`

8. Write tests with mock server (genai client supports custom endpoints).

**Run:**
```bash
go build ./... && go test ./provider/ -run TestGemini -v -count=1
```

**Commit:**
```bash
git add provider/gemini.go provider/gemini_test.go
git commit -m "feat: add Gemini provider using official google/generative-ai-go SDK"
```

---

## Task 11: Register Gemini in ratchet's ProviderRegistry

**Files:**
- Modify: `/Users/jon/workspace/ratchet/ratchetplugin/provider_registry.go`

**What to do:**

Add a `"gemini"` factory to the registry, following the same pattern as other providers:

```go
reg.Register("gemini", func(alias, apiKey, model, baseURL string, maxTokens int, settings map[string]string) (provider.Provider, error) {
    cfg := provider.GeminiConfig{
        APIKey:    apiKey,
        Model:     model,
        MaxTokens: maxTokens,
    }
    return provider.NewGeminiProvider(cfg)
})
```

After this, ratchet-cli's onboarding wizard (`providerTypes` in `onboarding.go`) already has Gemini listed — it just needs the backend to exist.

**Run (in ratchet repo):**
```bash
cd /Users/jon/workspace/ratchet
go build ./... && go test ./ratchetplugin/ -v -count=1
```

**Commit (in ratchet repo):**
```bash
git add ratchetplugin/provider_registry.go
git commit -m "feat: register Gemini provider in ProviderRegistry"
```

---

## Task 12: Update ratchet-cli model listing for Gemini

**Files:**
- Modify: `/Users/jon/workspace/ratchet-cli/internal/provider/models.go`

**What to do:**

The existing `listGeminiModels` uses raw HTTP to `generativelanguage.googleapis.com`. Replace with the SDK:

```go
func listGeminiModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
    client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
    if err != nil {
        return nil, err
    }
    defer client.Close()

    iter := client.ListModels(ctx)
    var models []ModelInfo
    for {
        m, err := iter.Next()
        if err == iterator.Done {
            break
        }
        if err != nil {
            return nil, err
        }
        // Filter to models that support generateContent
        if !slices.Contains(m.SupportedGenerationMethods, "generateContent") {
            continue
        }
        id := strings.TrimPrefix(m.Name, "models/")
        name := m.DisplayName
        if name == "" {
            name = id
        }
        models = append(models, ModelInfo{ID: id, Name: name})
    }
    sortModels(models)
    return models, nil
}
```

**Run (in ratchet-cli):**
```bash
cd /Users/jon/workspace/ratchet-cli
go build ./... && go test ./internal/provider/ -v -count=1
```

**Commit:**
```bash
git add internal/provider/models.go
git commit -m "refactor: use genai SDK for Gemini model listing"
```

---

## Task 13: Full test suite + tag releases

**Step 1:** Run full test suite in workflow-plugin-agent:
```bash
cd /Users/jon/workspace/workflow-plugin-agent
go test ./... -race -count=1
```

**Step 2:** Run full test suite in ratchet:
```bash
cd /Users/jon/workspace/ratchet
go test ./... -count=1
```

**Step 3:** Run full test suite in ratchet-cli:
```bash
cd /Users/jon/workspace/ratchet-cli
go test ./... -race -count=1
```

All must pass.

**Step 4:** Tag and push workflow-plugin-agent:
```bash
cd /Users/jon/workspace/workflow-plugin-agent
git tag -a v0.4.0 -m "v0.4.0: Migrate to official AI provider SDKs + add Gemini"
git push origin main --tags
```

**Step 5:** Update ratchet to use new workflow-plugin-agent:
```bash
cd /Users/jon/workspace/ratchet
go get github.com/GoCodeAlone/workflow-plugin-agent@v0.4.0
go mod tidy
go test ./... -count=1
git add go.mod go.sum ratchetplugin/provider_registry.go
git commit -m "feat: upgrade workflow-plugin-agent v0.4.0 (SDK providers + Gemini)"
git tag -a v0.1.16 -m "v0.1.16: SDK provider migration + Gemini"
git push origin main --tags
```

**Step 6:** Update ratchet-cli to use new versions:
```bash
cd /Users/jon/workspace/ratchet-cli
go get github.com/GoCodeAlone/workflow-plugin-agent@v0.4.0
go get github.com/GoCodeAlone/ratchet@v0.1.16
go mod tidy
go test ./... -race -count=1
git add go.mod go.sum internal/provider/models.go
git commit -m "chore: upgrade to SDK-based providers (agent v0.4.0, ratchet v0.1.16)"
git tag -a v0.3.0 -m "v0.3.0: Official SDK providers + Gemini support"
git push origin master --tags
```

---

## Execution Order

```
Task 1 (deps) → Task 2 (Anthropic) → Task 3 (Bedrock) → Task 4 (Vertex) → Task 5 (shared helpers)
                Task 6 (OpenAI) → Task 7 (Azure) → Task 8 (Copilot) → Task 9 (shared helpers)
                Task 10 (Gemini) → Task 11 (registry) → Task 12 (model listing)
Task 13 (test + release)
```

**Parallel groups:**
- Group A: Tasks 2-5 (Anthropic family)
- Group B: Tasks 6-9 (OpenAI family)
- Group C: Tasks 10-12 (Gemini — new)
- All three groups can run in parallel after Task 1
