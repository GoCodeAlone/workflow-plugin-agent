package genkit

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
	gk "github.com/firebase/genkit/go/genkit"
	anthropicPlugin "github.com/firebase/genkit/go/plugins/anthropic"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	openaiPlugin "github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	ollamaPlugin "github.com/firebase/genkit/go/plugins/ollama"
	"github.com/openai/openai-go/option"
)

// Default models per provider when none specified.
const (
	defaultAnthropicModel = "claude-sonnet-4-6"
	defaultOpenAIModel    = "gpt-4.1"
	defaultGeminiModel    = "gemini-2.5-flash"
	defaultOllamaModel    = "qwen3:8b"
)

// vertexCredsMu guards the GOOGLE_APPLICATION_CREDENTIALS env var
// to prevent races when multiple VertexAI providers initialize concurrently.
var vertexCredsMu sync.Mutex


// initGenkitWithPlugin creates a Genkit instance with a single plugin registered.
func initGenkitWithPlugin(ctx context.Context, plugin gk.GenkitOption) *gk.Genkit {
	return gk.Init(ctx, plugin)
}

// NewAnthropicProvider creates a provider backed by Genkit's Anthropic plugin.
func NewAnthropicProvider(ctx context.Context, apiKey, model, baseURL string, maxTokens int) (provider.Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic: APIKey is required")
	}
	if model == "" {
		model = defaultAnthropicModel
	}
	if err := provider.ValidateBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	p := &anthropicPlugin.Anthropic{APIKey: apiKey, BaseURL: baseURL}
	g := initGenkitWithPlugin(ctx, gk.WithPlugins(p))
	return &genkitProvider{
		g:         g,
		modelName: "anthropic/" + model,
		name:      "anthropic",
		maxTokens: maxTokens,
		authInfo: provider.AuthModeInfo{
			Mode:        "api_key",
			DisplayName: "Anthropic",
			ServerSafe:  true,
		},
	}, nil
}

// NewOpenAIProvider creates a provider backed by Genkit's OpenAI plugin.
func NewOpenAIProvider(ctx context.Context, apiKey, model, baseURL string, maxTokens int) (provider.Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("openai: APIKey is required")
	}
	if model == "" {
		model = defaultOpenAIModel
	}
	if err := provider.ValidateBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	var extraOpts []option.RequestOption
	if baseURL != "" {
		extraOpts = append(extraOpts, option.WithBaseURL(baseURL))
	}
	p := &openaiPlugin.OpenAI{APIKey: apiKey, Opts: extraOpts}
	g := initGenkitWithPlugin(ctx, gk.WithPlugins(p))
	return &genkitProvider{
		g:         g,
		modelName: "openai/" + model,
		name:      "openai",
		maxTokens: maxTokens,
		authInfo: provider.AuthModeInfo{
			Mode:        "api_key",
			DisplayName: "OpenAI",
			ServerSafe:  true,
		},
	}, nil
}

// NewGoogleAIProvider creates a provider backed by Genkit's Google AI plugin (Gemini API).
func NewGoogleAIProvider(ctx context.Context, apiKey, model string, maxTokens int) (provider.Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("googleai: APIKey is required")
	}
	if model == "" {
		model = defaultGeminiModel
	}
	p := &googlegenai.GoogleAI{APIKey: apiKey}
	g := initGenkitWithPlugin(ctx, gk.WithPlugins(p))
	return &genkitProvider{
		g:         g,
		modelName: "googleai/" + model,
		name:      "googleai",
		maxTokens: maxTokens,
		authInfo: provider.AuthModeInfo{
			Mode:        "api_key",
			DisplayName: "Google AI (Gemini)",
			ServerSafe:  true,
		},
	}, nil
}

// NewOllamaProvider creates a provider backed by Genkit's Ollama plugin.
func NewOllamaProvider(ctx context.Context, model, serverAddress string, maxTokens int) (provider.Provider, error) {
	if serverAddress == "" {
		serverAddress = "http://localhost:11434"
	}
	if model == "" {
		model = defaultOllamaModel
	}
	p := &ollamaPlugin.Ollama{ServerAddress: serverAddress, Timeout: 300} // 5 min — model loading can be slow
	g := initGenkitWithPlugin(ctx, gk.WithPlugins(p))
	return &genkitProvider{
		g:         g,
		modelName: "ollama/" + model,
		name:      "ollama",
		maxTokens: maxTokens,
		authInfo: provider.AuthModeInfo{
			Mode:        "none",
			DisplayName: "Ollama (local)",
			ServerSafe:  true,
		},
	}, nil
}

// NewOpenAICompatibleProvider creates a provider for OpenAI-compatible endpoints.
// Used for OpenRouter, Copilot, Cohere, HuggingFace, llama.cpp, etc.
func NewOpenAICompatibleProvider(ctx context.Context, providerName, apiKey, model, baseURL string, maxTokens int) (provider.Provider, error) {
	if model == "" {
		model = defaultOpenAIModel
	}
	// Skip SSRF validation for local providers that use localhost endpoints.
	switch providerName {
	case "llama_cpp", "local", "test":
		// Local providers intentionally use http://localhost — skip SSRF checks.
	default:
		if err := provider.ValidateBaseURL(baseURL); err != nil {
			return nil, fmt.Errorf("%s: %w", providerName, err)
		}
	}
	effectiveKey := apiKey
	if effectiveKey == "" {
		// Use a placeholder to avoid errors when no key is needed (e.g., local endpoints)
		effectiveKey = "no-key"
	}
	p := &compat_oai.OpenAICompatible{
		Provider: providerName,
		APIKey:   effectiveKey,
		BaseURL:  baseURL,
	}
	g := initGenkitWithPlugin(ctx, gk.WithPlugins(p))
	return &genkitProvider{
		g:         g,
		modelName: providerName + "/" + model,
		name:      providerName,
		maxTokens: maxTokens,
		authInfo: provider.AuthModeInfo{
			Mode:        "api_key",
			DisplayName: providerName,
			ServerSafe:  true,
		},
	}, nil
}

// NewAzureOpenAIProvider creates a provider for Azure OpenAI Service.
func NewAzureOpenAIProvider(ctx context.Context, resource, deploymentName, apiVersion, apiKey, entraToken string, maxTokens int) (provider.Provider, error) {
	if resource == "" {
		return nil, fmt.Errorf("openai_azure: resource is required")
	}
	if apiKey == "" && entraToken == "" {
		return nil, fmt.Errorf("openai_azure: apiKey or entraToken is required")
	}
	if apiVersion == "" {
		apiVersion = "2024-10-21"
	}

	baseURL := fmt.Sprintf("https://%s.openai.azure.com/openai/deployments/%s", resource, deploymentName)

	opts := []option.RequestOption{
		option.WithBaseURL(baseURL),
		option.WithHeaderDel("authorization"),
		option.WithQuery("api-version", apiVersion),
	}
	if apiKey != "" {
		opts = append(opts, option.WithHeader("api-key", apiKey))
	} else {
		opts = append(opts, option.WithHeader("authorization", "Bearer "+entraToken))
	}

	// Use a placeholder API key to avoid requiring OPENAI_API_KEY env var
	p := &openaiPlugin.OpenAI{APIKey: "azure-placeholder", Opts: opts}
	g := initGenkitWithPlugin(ctx, gk.WithPlugins(p))
	return &genkitProvider{
		g:         g,
		modelName: "openai/" + deploymentName,
		name:      "openai_azure",
		maxTokens: maxTokens,
		authInfo: provider.AuthModeInfo{
			Mode:        "azure",
			DisplayName: "OpenAI (Azure OpenAI Service)",
			ServerSafe:  true,
		},
	}, nil
}

// NewAnthropicFoundryProvider creates a provider for Anthropic on Azure AI Foundry.
func NewAnthropicFoundryProvider(ctx context.Context, resource, model, apiKey, entraToken string, maxTokens int) (provider.Provider, error) {
	if resource == "" {
		return nil, fmt.Errorf("anthropic_foundry: resource is required")
	}
	effectiveKey := apiKey
	if effectiveKey == "" {
		effectiveKey = entraToken
	}
	if effectiveKey == "" {
		return nil, fmt.Errorf("anthropic_foundry: apiKey or entraToken is required")
	}

	// Azure AI Foundry Anthropic endpoints
	baseURL := fmt.Sprintf("https://%s.services.ai.azure.com/models", resource)
	p := &anthropicPlugin.Anthropic{APIKey: effectiveKey, BaseURL: baseURL}
	g := initGenkitWithPlugin(ctx, gk.WithPlugins(p))
	return &genkitProvider{
		g:         g,
		modelName: "anthropic/" + model,
		name:      "anthropic_foundry",
		maxTokens: maxTokens,
		authInfo: provider.AuthModeInfo{
			Mode:        "azure",
			DisplayName: "Anthropic (Azure AI Foundry)",
			ServerSafe:  true,
		},
	}, nil
}

// NewVertexAIProvider creates a provider backed by Genkit's Vertex AI plugin.
func NewVertexAIProvider(ctx context.Context, projectID, region, model, credentialsJSON string, maxTokens int) (provider.Provider, error) {
	if projectID == "" {
		return nil, fmt.Errorf("vertexai: projectID is required")
	}
	if region == "" {
		region = "us-central1"
	}

	// Genkit's VertexAI plugin uses credentials.DetectDefault() which reads
	// GOOGLE_APPLICATION_CREDENTIALS. When inline JSON is provided, write it
	// to a temp file, set the env var, init Genkit, then clean up.
	var tempCredFile string
	if credentialsJSON != "" {
		vertexCredsMu.Lock()
		defer vertexCredsMu.Unlock()

		prevCreds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")

		f, err := os.CreateTemp("", "vertexai-creds-*.json")
		if err != nil {
			return nil, fmt.Errorf("vertexai: create temp credentials file: %w", err)
		}
		tempCredFile = f.Name()
		if _, err := f.WriteString(credentialsJSON); err != nil {
			_ = f.Close()
			_ = os.Remove(tempCredFile)
			return nil, fmt.Errorf("vertexai: write credentials: %w", err)
		}
		_ = f.Close()
		_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", tempCredFile)
		defer func() {
			// Restore previous env var and remove temp file.
			if prevCreds == "" {
				_ = os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
			} else {
				_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", prevCreds)
			}
			_ = os.Remove(tempCredFile)
		}()
	}

	p := &googlegenai.VertexAI{
		ProjectID: projectID,
		Location:  region,
	}
	g := initGenkitWithPlugin(ctx, gk.WithPlugins(p))
	return &genkitProvider{
		g:         g,
		modelName: "vertexai/" + model,
		name:      "vertexai",
		maxTokens: maxTokens,
		authInfo: provider.AuthModeInfo{
			Mode:        "gcp",
			DisplayName: "Vertex AI",
			ServerSafe:  true,
		},
	}, nil
}

// NewBedrockProvider creates a provider for AWS Bedrock using an OpenAI-compatible endpoint.
// Wraps existing Bedrock implementation as a provider.Provider until a native Genkit plugin is available.
func NewBedrockProvider(ctx context.Context, region, model, accessKeyID, secretAccessKey, sessionToken, baseURL string, maxTokens int) (provider.Provider, error) {
	if secretAccessKey == "" {
		return nil, fmt.Errorf("anthropic_bedrock: secretAccessKey is required")
	}
	if region == "" {
		region = "us-east-1"
	}
	return provider.NewAnthropicBedrockProvider(provider.AnthropicBedrockConfig{
		Region:          region,
		Model:           model,
		MaxTokens:       maxTokens,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		SessionToken:    sessionToken,
		BaseURL:         baseURL,
	})
}
